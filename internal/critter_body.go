package internal

import (
	"errors"
	"math"

	"graphics.gd/classdb/ArrayMesh"
	"graphics.gd/classdb/CollisionShape3D"
	"graphics.gd/classdb/ConcavePolygonShape3D"
	"graphics.gd/classdb/Material"
	"graphics.gd/classdb/Mesh"
	"graphics.gd/classdb/MeshInstance3D"
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/PackedScene"
	"graphics.gd/classdb/Skeleton3D"
	"graphics.gd/classdb/Skin"
	"graphics.gd/classdb/StaticBody3D"
	"graphics.gd/variant/AABB"
	"graphics.gd/variant/Basis"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Quaternion"
	"graphics.gd/variant/Transform3D"
	"graphics.gd/variant/Vector3"

	"the.quetzal.community/aviary/internal/critter"
)

// CritterBody bridges a pure-Go critter.Critter to a Godot
// MeshInstance3D. It owns a persistent ArrayMesh and re-surfaces it
// in place whenever a slider tick moves the spine — mirroring
// CitizenBody's pattern so the keepalive walker (graphics.gd's GC)
// sees the MeshInstance3D and ArrayMesh via CritterEditor →
// CritterBody → these fields and keeps them alive each frame.
//
// A child StaticBody3D + ConcavePolygonShape3D mirrors the visible
// mesh so the editor's MousePicker raycast can land on the body
// surface — that drives part (muzzle, antler, …) placement.
//
// Parts attached via AttachPart are stored as children of `parts`,
// which is itself a child of the MeshInstance3D so parts ride
// along with the body's transform. Each attached part records a
// (t, theta) anchor pair onto the procedural surface so when the
// body deforms via sliders, every part snaps back onto the new
// surface via repositionParts() at the end of rebuild().
type CritterBody struct {
	critter *critter.Critter
	// mesh is the editor-owned MeshInstance3D rendering the critter.
	mesh MeshInstance3D.Instance
	// arrayMesh is the Resource backing the displayed mesh; kept
	// alive via the same keepalive path as `mesh`.
	arrayMesh ArrayMesh.Instance
	vertexBuf []Vector3.XYZ
	normalBuf []Vector3.XYZ
	indices   []int32
	// collisionShape holds the ConcavePolygonShape3D that mirrors
	// the visible mesh so the picker can ray-hit the body.
	collisionShape CollisionShape3D.Instance
	concaveShape   ConcavePolygonShape3D.Instance
	faceBuf        []Vector3.XYZ
	// parts is the container for runtime-placed parts (muzzles,
	// claws, …); each child is a PackedScene instance.
	parts Node3D.Instance
	// partAnchors maps each placed part's Node3D.ID to its surface
	// anchor parameters; consulted by repositionParts on every
	// body rebuild so parts follow the deforming surface.
	partAnchors map[Node3D.ID]PartAnchor
	// partScale is applied uniformly to each placed part. Single
	// global for v1 — per-part scaling can move into PartAnchor
	// later if we want size sliders.
	partScale float32

	// rebuildPaused / rebuildPending implement a manual batch
	// for the editor's bone-drag path. Each Sculpt that lands
	// runs SetBoneAxis → rebuild, and with the radial-propagation
	// feature one drag frame can emit a Sculpt per bone. Rebuilding
	// the body mesh, collision shape and repositioning every
	// attached part once per bone burns ~10× the work needed —
	// the drag handler calls PauseRebuild around its batch and
	// ResumeRebuild fires a single rebuild at the end.
	rebuildPaused  bool
	rebuildPending bool

	// legNodes / legArrayMeshes carry one MeshInstance3D + ArrayMesh
	// per leg in the data model. Kept aligned with critter.Legs() by
	// rebuildLegs(); excess entries get queue-freed when legs are
	// removed, and new entries spawn fresh nodes when legs grow.
	// Each MeshInstance3D is a child of the body's mesh node so it
	// rides along with the body transform.
	legNodes       []MeshInstance3D.Instance
	legArrayMeshes []ArrayMesh.Instance
	// legMaterialOverride is applied to every leg MeshInstance3D
	// after each rebuild. The ribcage view sets this to the same
	// dark-transparent material it puts on the body so leg meshes
	// render with the same xray look as the body; outside that view
	// it's Material.Nil (leg meshes use their default material).
	legMaterialOverride Material.Instance

	// legOneSided suppresses the −X mirror half during BuildLegMesh
	// when true. The ribcage view sets this so the camera-facing
	// leg isn't doubled up with the back-of-body one in the side
	// projection (where they'd otherwise occupy the same screen
	// pixels and compound their alpha). Default false → both sides
	// render as normal.
	legOneSided bool

	// skeleton + skin drive GPU skinning of the body mesh against
	// the spine bones. One Skeleton3D bone per critter.Bone, parented
	// in a chain (tail → head), so SetBonePose on any bone deforms
	// the body from that bone outward. skin holds the inverse rest
	// transforms (bind poses) the renderer multiplies through. Both
	// resources are rebuilt from scratch each time the bone count
	// changes — bone positions changing in-place can be applied via
	// SetBoneRest without recreating the resources, but for v1 we
	// just rebuild from scratch each rebuild() pass since the cost
	// is trivial compared to the mesh rebuild itself.
	skeleton Skeleton3D.Instance
	skin     Skin.Instance

	// animatedFeet caches the per-leg foot positions last pushed via
	// SetAnimatedLegFeet. RepositionPartsAnimated reads it so OnLeg
	// parts (steppers) ride the gait-animated foot every frame,
	// instead of snapping back to the data-model rest pose whenever
	// Process runs in between PhysicsProcess ticks. Nil/empty means
	// no gait override is active — the OnLeg branch falls back to
	// leg.Foot.
	animatedFeet [][2]critter.Vec3

	// matricesBuf is the per-bone skin-matrix scratch buffer reused
	// each RepositionPartsAnimated tick so the variable-Hz Process
	// path doesn't allocate a fresh slice every frame.
	matricesBuf []Transform3D.BasisOrigin
}

// PartAnchor parameterises one attached part's position on the
// critter's surface OR on the foot joint of one of its legs.
//
// Body-surface mode (OnLeg=false, the default): (T, Theta) match
// the convention from critter.AnchorPoint: T runs tail→head, Theta
// sweeps around the spine. Offset has two meanings depending on T
// (see critter.AnchorPoint): for body anchors it lifts the part
// above the surface along the outward normal; for cap anchors
// (T∈{0,1}) it's the radial distance from the spine endpoint
// within the cap disc plane.
//
// Leg-foot mode (OnLeg=true): the part sits at LegFoot leg's Foot
// joint in body-local space, with the body's own basis as
// orientation. LegSide picks which mirrored side of the leg the
// part rides: 0 = +X (the canonical storage side), 1 = −X (the
// mirrored side). T / Theta / Offset are ignored in this mode.
//
// The flag-first layout means the Go zero value is a body anchor —
// existing PartAnchor literals (and existing serialised Changes,
// which decode through tryAttachChange below with Bounds defaulting
// to zero) keep working without a schema migration.
type PartAnchor struct {
	T, Theta float32
	Offset   float32
	OnLeg    bool
	LegFoot  int
	LegSide  int
}

// AttachCritterBody creates a fresh ArrayMesh, runs an initial mesh
// build from the critter's default state, sets the mesh on mi, adds
// a child StaticBody3D with a ConcavePolygonShape3D so the body is
// mouse-pickable, and prepares the `parts` container that holds
// runtime-placed parts.
func AttachCritterBody(mi MeshInstance3D.Instance, c *critter.Critter) (CritterBody, error) {
	if mi == MeshInstance3D.Nil {
		return CritterBody{}, errors.New("critter: nil MeshInstance3D")
	}
	if c == nil {
		return CritterBody{}, errors.New("critter: nil Critter")
	}
	staticBody := StaticBody3D.New()
	// Move the body onto layer 2 so the global selection raycast
	// (which masks out 1<<1) skips it — the body is procedural and
	// has no scene Owner, so letting selection's `node.Owner().ID()`
	// chain hit it segfaults. Editor-internal MousePicker uses
	// PhysicsRayQueryParameters3D.Create's default all-layers mask
	// so it still hits the body for muzzle hover placement.
	staticBody.AsCollisionObject3D().SetCollisionLayer(1 << 1)
	collisionShape := CollisionShape3D.New()
	concave := ConcavePolygonShape3D.New()
	collisionShape.SetShape(concave.AsShape3D())
	staticBody.AsNode().AddChild(collisionShape.AsNode())
	mi.AsNode().AddChild(staticBody.AsNode())
	parts := Node3D.New()
	mi.AsNode().AddChild(parts.AsNode())
	// Skeleton3D for GPU skinning. Godot's convention is that the
	// skeleton sits as a SIBLING of the MeshInstance3D under a
	// shared parent — MeshInstance3D.skeleton defaults to
	// `"../Skeleton3D"`. We mirror that here so the path resolves
	// without any extra fiddling: take the MI's parent and attach
	// the skeleton there, with the conventional name. (The editor
	// holds at most one critter body, so name collisions aren't a
	// concern; we'd need to scope this if that changed.)
	skeleton := Skeleton3D.New()
	skeleton.AsNode().SetName("Skeleton3D")
	if parent := mi.AsNode().GetParent(); parent != Node.Nil {
		parent.AddChild(skeleton.AsNode())
	} else {
		// Pre-tree state — fall back to attaching as a child of mi
		// itself. SetSkeleton below switches to "Skeleton3D" in
		// that case so the path still resolves.
		mi.AsNode().AddChild(skeleton.AsNode())
	}
	skin := Skin.New()
	body := CritterBody{
		critter:        c,
		mesh:           mi,
		arrayMesh:      ArrayMesh.New(),
		collisionShape: collisionShape,
		concaveShape:   concave,
		parts:          parts,
		partAnchors:    make(map[Node3D.ID]PartAnchor),
		partScale:      2.0,
		skeleton:       skeleton,
		skin:           skin,
	}
	body.rebuild()
	mi.SetMesh(body.arrayMesh.AsMesh())
	// Custom AABB so the renderer doesn't recompute the skinned AABB
	// from per-bone arrays every frame — that recompute path fires
	// the "bs > sbs" warning whenever the skin's bind count lags the
	// mesh's referenced bones (intermittent during the first frame
	// after a rebuild, when Skeleton3D's update notification hasn't
	// caught up to the new bind_count yet). Generous bounds cover
	// default-critter extents plus head-look swing + jump arc;
	// matches the existing implicit visibility window for the body
	// node so culling behaviour is unchanged.
	body.arrayMesh.SetCustomAabb(AABB.PositionSize{
		Position: Vector3.New(-1.5, -1, -2),
		Size:     Vector3.New(3, 3, 4),
	})
	mi.SetSkin(skin)
	// Standard Godot convention: skeleton lives next to the MI.
	mi.SetSkeleton("../Skeleton3D")
	return body, nil
}

// SetWeight forwards a (legacy) macro slider change to the underlying
// critter and re-surfaces the mesh if the value actually changed.
// Kept for tabs/UI that still emit macros while the  bone
// editing layer takes over; new code should prefer the SetBone*
// methods below.
func (b *CritterBody) SetWeight(name string, weight float32) {
	if !b.critter.SetWeight(name, weight) {
		return
	}
	b.rebuild()
}

// Critter exposes the underlying pure-Go critter model so the
// editor can read its current bone state (e.g. to compute new
// handle positions or extrapolate a grow). Callers must not
// mutate it directly — use the SetBone* / AppendBone* / RemoveBone*
// methods below so the mesh + collision rebuild fires.
func (b *CritterBody) Critter() *critter.Critter { return b.critter }

// SetBonePos updates a bone's rest position and rebuilds the body
// mesh + collider. No-op if i is out of range.
func (b *CritterBody) SetBonePos(i int, pos critter.Vec3) {
	if !b.critter.MoveBone(i, pos) {
		return
	}
	b.rebuild()
}

// SetBoneAxis updates a single axis of bone i's rest position
// (axis 0=X, 1=Y, 2=Z). Mirrors the per-axis Sculpt encoding so
// network-driven edits can land one component at a time.
func (b *CritterBody) SetBoneAxis(i, axis int, value float32) {
	bones := b.critter.Bones()
	if i < 0 || i >= len(bones) {
		return
	}
	p := bones[i].Pos
	switch axis {
	case 0:
		p.X = value
	case 1:
		p.Y = value
	case 2:
		p.Z = value
	default:
		return
	}
	if b.critter.MoveBone(i, p) {
		b.rebuild()
	}
}

// SetBoneRadius updates a bone's body radius and rebuilds. No-op
// if i is out of range.
func (b *CritterBody) SetBoneRadius(i int, r float32) {
	if !b.critter.SetBoneRadius(i, r) {
		return
	}
	b.rebuild()
}

// AppendHead extends the chain past the current head tip; the new
// bone position is extrapolated from the last two by the model.
// Returns the new bone's index.
func (b *CritterBody) AppendHead() int {
	idx := b.critter.AppendHead()
	b.rebuild()
	return idx
}

// AppendTail extends the chain past the current tail tip,
// shifting existing bones up by 1.
func (b *CritterBody) AppendTail() int {
	idx := b.critter.AppendTail()
	b.rebuild()
	return idx
}

// RemoveHead drops the last bone (refuses below 2 bones).
func (b *CritterBody) RemoveHead() bool {
	if !b.critter.RemoveHead() {
		return false
	}
	b.rebuild()
	return true
}

// RemoveTail drops the first bone, shifting indices down by 1.
func (b *CritterBody) RemoveTail() bool {
	if !b.critter.RemoveTail() {
		return false
	}
	b.rebuild()
	return true
}

// AppendLeg adds a leg with default rest pose at a sensible
// spine bone (front-quarter of the chain) and rebuilds so the new
// leg geometry shows up immediately. Returns the new leg's index.
func (b *CritterBody) AppendLeg() int {
	idx := b.critter.AppendLeg()
	b.rebuild()
	return idx
}

// AppendLegAt adds a leg socketed to the named spine bone.
func (b *CritterBody) AppendLegAt(attach int) int {
	idx := b.critter.AppendLegAt(attach)
	b.rebuild()
	return idx
}

// AppendLegAtPos adds a leg whose hip lands at the given body-local
// position (knee/foot derived from the hip). Used by the free-attach
// placement path so legs can be sockets anywhere on the body
// surface, not just on a spine bone.
func (b *CritterBody) AppendLegAtPos(hip critter.Vec3) int {
	idx := b.critter.AppendLegAtPos(hip)
	b.rebuild()
	return idx
}

// RemoveLeg drops leg i.
func (b *CritterBody) RemoveLeg(i int) bool {
	if !b.critter.RemoveLeg(i) {
		return false
	}
	b.rebuild()
	return true
}

// SetLegAttach re-sockets leg i to a different spine bone.
func (b *CritterBody) SetLegAttach(i, bone int) {
	if b.critter.SetLegAttach(i, bone) {
		b.rebuild()
	}
}

// SetLegJointAxis sets one axis of one joint on leg i. axis: 0=X,
// 1=Y, 2=Z. Matches the per-axis Sculpt encoding so a drag
// emitting (Y,Z) ships two messages rather than one bundle.
func (b *CritterBody) SetLegJointAxis(i int, joint critter.LegJoint, axis int, v float32) {
	if b.critter.SetLegJointAxis(i, joint, axis, v) {
		b.rebuild()
	}
}

// SetLegMaterialOverride changes the material applied to every leg
// MeshInstance3D and re-applies it immediately. Pass Material.Nil
// to clear (leg meshes fall back to their default appearance).
// Used by the ribcage view to render legs as semi-transparent dark
// shapes alongside the darkened body.
func (b *CritterBody) SetLegMaterialOverride(m Material.Instance) {
	b.legMaterialOverride = m
	for _, mi := range b.legNodes {
		mi.AsGeometryInstance3D().SetMaterialOverride(m)
	}
}

// SetLegOneSided toggles single-side leg geometry. Used by the
// ribcage view, where the side projection makes a back-of-body leg
// overlap with the front one and double the visible alpha. Calling
// this triggers a full rebuild so the change takes effect
// immediately.
func (b *CritterBody) SetLegOneSided(v bool) {
	if b.legOneSided == v {
		return
	}
	b.legOneSided = v
	b.rebuild()
}

// SetLegRadius sets the tube radius for leg i at every joint
// (convenience for uniform thickness).
func (b *CritterBody) SetLegRadius(i int, r float32) {
	if b.critter.SetLegRadius(i, r) {
		b.rebuild()
	}
}

// SetLegJointRadius sets the tube radius at one joint of leg i.
func (b *CritterBody) SetLegJointRadius(i int, joint critter.LegJoint, r float32) {
	if b.critter.SetLegJointRadius(i, joint, r) {
		b.rebuild()
	}
}

// AttachPart instantiates the supplied PackedScene under the parts
// container and records its anchor parameters. Returns the new
// node so the caller (editor) can map it to a musical.Entity. If
// scene is nil the part is added as a plain Node3D — useful for
// placeholders before an import lands.
//
// If the parts container has somehow gone away (Nil), we recreate
// it from b.mesh — this used to silently drop placements during
// history replay (so muzzles appeared locally but never persisted),
// because the round-trip Change arrived before something else
// repopulated b.parts.
func (b *CritterBody) AttachPart(anchor PartAnchor, scene PackedScene.Instance) Node3D.Instance {
	var node Node3D.Instance
	if scene != PackedScene.Nil {
		inst := scene.Instantiate()
		node = Object.To[Node3D.Instance](inst)
	}
	return b.AttachPartNode(anchor, node)
}

// AttachPartNode is the pre-built-node variant of AttachPart, used
// by code paths that build the part's Node3D outside the
// PackedScene importer (e.g. the .obj fallback for MakeHuman
// clothing). Passing Node3D.Nil falls back to a fresh empty Node3D
// so the entity ID still has somewhere to live until the real
// geometry arrives.
func (b *CritterBody) AttachPartNode(anchor PartAnchor, node Node3D.Instance) Node3D.Instance {
	if b.parts == Node3D.Nil {
		if b.mesh == MeshInstance3D.Nil {
			return Node3D.Nil
		}
		b.parts = Node3D.New()
		b.mesh.AsNode().AddChild(b.parts.AsNode())
	}
	if node == Node3D.Nil {
		node = Node3D.New()
	}
	b.parts.AsNode().AddChild(node.AsNode())
	// Library scenes already ship with a trimesh StaticBody3D
	// baked in by the import-time AviaryModelLoader extension
	// (Owner=root), so clicking the part lands on the global
	// selection raycast and our Delete handler can find the
	// entity. Nothing extra to wire here for selection.
	id := node.ID()
	b.partAnchors[id] = anchor
	b.positionPart(node, anchor)
	return node
}

// PartSelectionMask is the layer mask the editor's placement
// picker passes to PhysicsRayQueryParameters3D so hover/click
// for new placements skips already-placed parts. Library-imported
// part scenes ship with their StaticBody3D on the default layer
// 1; the critter body's own collider is on layer 2. The mask
// here clears layer 1 so the picker reaches the body underneath
// instead of stacking new placements on top of existing parts.
const PartSelectionMask = uint32(^uint32(0)) & ^uint32(1<<0)

// DetachPart removes a previously-attached part by ID. The Godot
// node is queue-freed and the anchor map entry dropped.
func (b *CritterBody) DetachPart(id Node3D.ID) {
	if node, ok := id.Instance(); ok {
		node.AsNode().QueueFree()
	}
	delete(b.partAnchors, id)
}

// SetPartAnchor updates the anchor for an existing part and snaps
// it to the new location/orientation immediately. Returns true if
// the part existed.
func (b *CritterBody) SetPartAnchor(id Node3D.ID, anchor PartAnchor) bool {
	if _, ok := b.partAnchors[id]; !ok {
		return false
	}
	b.partAnchors[id] = anchor
	if node, ok := id.Instance(); ok {
		b.positionPart(node, anchor)
	}
	return true
}

// Parts returns the container Node3D so callers (idle animation,
// for example) can iterate placed parts. Nil before
// AttachCritterBody runs.
func (b *CritterBody) Parts() Node3D.Instance { return b.parts }

// ClosestAnchor maps a world-space (== body-local, since parts ride
// the body transform) hit point to the (T, Theta, Offset) anchor
// that best describes it on the current surface.
func (b *CritterBody) ClosestAnchor(p Vector3.XYZ) PartAnchor {
	if b.critter == nil {
		return PartAnchor{}
	}
	t, theta, off := b.critter.ClosestAnchor(critter.Vec3{
		X: float32(p.X), Y: float32(p.Y), Z: float32(p.Z),
	})
	return PartAnchor{T: t, Theta: theta, Offset: off}
}

// rebuild regenerates the tube mesh from the critter's current
// shape, refreshes the collision face data, and snaps every
// attached part back onto the new surface via its anchor params.
// PauseRebuild defers further rebuild() calls until ResumeRebuild
// fires the (single) pending rebuild. Used by the bone-drag path
// to batch dozens of per-bone Sculpts into one mesh + collision +
// reposition pass per frame.
func (b *CritterBody) PauseRebuild() { b.rebuildPaused = true }

// ResumeRebuild releases the rebuild gate and flushes any rebuild
// that was elided while paused. Safe to call even when no rebuild
// is pending — it just clears the flag.
func (b *CritterBody) ResumeRebuild() {
	b.rebuildPaused = false
	if b.rebuildPending {
		b.rebuildPending = false
		b.rebuild()
	}
}

func (b *CritterBody) rebuild() {
	if b.rebuildPaused {
		b.rebuildPending = true
		return
	}
	if b.arrayMesh == ArrayMesh.Nil {
		return
	}
	const samplesAlong = 24
	const segmentsAround = 12
	m := b.critter.BuildMesh(samplesAlong, segmentsAround)
	if cap(b.vertexBuf) < len(m.Verts) {
		b.vertexBuf = make([]Vector3.XYZ, len(m.Verts))
	} else {
		b.vertexBuf = b.vertexBuf[:len(m.Verts)]
	}
	for i, v := range m.Verts {
		b.vertexBuf[i] = Vector3.XYZ{X: Float.X(v.X), Y: Float.X(v.Y), Z: Float.X(v.Z)}
	}
	if cap(b.normalBuf) < len(m.Normals) {
		b.normalBuf = make([]Vector3.XYZ, len(m.Normals))
	} else {
		b.normalBuf = b.normalBuf[:len(m.Normals)]
	}
	for i, n := range m.Normals {
		b.normalBuf[i] = Vector3.XYZ{X: Float.X(n.X), Y: Float.X(n.Y), Z: Float.X(n.Z)}
	}
	b.indices = m.Indices
	// Populate the skeleton + skin BEFORE creating the surface: the
	// surface's per-vertex bone indices need to point at bones the
	// skeleton actually owns at upload time. Building the surface
	// against a 0-bone skeleton and then back-filling the bones
	// leaves the renderer with a mesh that references nothing —
	// every skinning matrix evaluates to zero and the body collapses
	// out of view.
	b.rebuildSkeleton()
	b.arrayMesh.ClearSurfaces()
	var arrays [Mesh.ArrayMax]any
	arrays[Mesh.ArrayVertex] = b.vertexBuf
	arrays[Mesh.ArrayNormal] = b.normalBuf
	arrays[Mesh.ArrayIndex] = b.indices
	// Skinning arrays: present iff BuildMesh emitted them (it does
	// for the spine tube; leg / preview meshes don't). The four-
	// influences-per-vertex flat layout matches what the GPU
	// pipeline reads with the default surface format — no flag bit
	// needed.
	if len(m.Bones) > 0 && len(m.Weights) > 0 {
		arrays[Mesh.ArrayBones] = m.Bones
		arrays[Mesh.ArrayWeights] = m.Weights
	}
	b.arrayMesh.AddSurfaceFromArrays(Mesh.PrimitiveTriangles, arrays[:])

	if b.concaveShape != ConcavePolygonShape3D.Nil && len(b.indices)%3 == 0 {
		need := len(b.indices)
		if cap(b.faceBuf) < need {
			b.faceBuf = make([]Vector3.XYZ, need)
		} else {
			b.faceBuf = b.faceBuf[:need]
		}
		for i, idx := range b.indices {
			b.faceBuf[i] = b.vertexBuf[idx]
		}
		b.concaveShape.SetData(b.faceBuf)
	}

	b.repositionParts()
	b.rebuildLegs()
}

// rebuildSkeleton repopulates the Skeleton3D + Skin from the
// critter's current spine bones. Bones are parented in a chain
// (tail → head): bone 0 has no parent, bone i has parent bone i−1.
// Rest pose is each control point's position relative to its
// parent, identity rotation. The skin's binds carry the inverse of
// each bone's GLOBAL rest (which by chain construction is just the
// raw control point position) — so when every bone is at rest the
// skinning math collapses to identity and vertices stay where
// BuildMesh put them.
//
// Called from rebuild() so this stays in sync with the freshly-
// extruded mesh's bone indices. Skipped silently when the body has
// no skeleton/skin set up (defensive — no construction path leaves
// them nil today).
func (b *CritterBody) rebuildSkeleton() {
	if b.skeleton == Skeleton3D.Nil || b.skin == Skin.Nil || b.critter == nil {
		return
	}
	// BuildMesh's vertex bone-indices reference positions from
	// ComputeShape (which the macro sliders can shift relative to the
	// raw Bones list). The skeleton has to stand the bones up at the
	// same positions BuildMesh extruded the mesh against, or the
	// bind-pose math at rest will not cancel out and the head end
	// (where shape/neck_lift etc. apply) folds back into the body.
	controls, _ := b.critter.ComputeShape()
	b.skeleton.ClearBones()
	identityBasis := Basis.XYZ{
		Vector3.New(float32(1), float32(0), float32(0)),
		Vector3.New(float32(0), float32(1), float32(0)),
		Vector3.New(float32(0), float32(0), float32(1)),
	}
	for i, ctrl := range controls {
		b.skeleton.AddBone("spine" + boneIndexStr(i))
		if i > 0 {
			b.skeleton.SetBoneParent(i, i-1)
		}
		var restOrigin Vector3.XYZ
		if i == 0 {
			restOrigin = Vector3.XYZ{
				X: Float.X(ctrl.X), Y: Float.X(ctrl.Y), Z: Float.X(ctrl.Z),
			}
		} else {
			parent := controls[i-1]
			restOrigin = Vector3.XYZ{
				X: Float.X(ctrl.X - parent.X),
				Y: Float.X(ctrl.Y - parent.Y),
				Z: Float.X(ctrl.Z - parent.Z),
			}
		}
		b.skeleton.SetBoneRest(i, Transform3D.BasisOrigin{
			Basis: identityBasis, Origin: restOrigin,
		})
		// Live pose initialised to rest so SetHeadLookYaw (and any
		// future per-frame pose tweaks) layer over a known baseline.
		b.skeleton.SetBonePose(i, Transform3D.BasisOrigin{
			Basis: identityBasis, Origin: restOrigin,
		})
	}
	// Build the skin from Godot's own helper: it walks parentless
	// bones outward, accumulating rest transforms along the chain,
	// then inverts each accumulated transform to get the bind pose.
	// Doing it this way (vs computing binds ourselves) guarantees
	// the binds line up exactly with whatever Godot considers each
	// bone's global rest — closing every potential off-by-one /
	// composition-order gap that left the head folded with our
	// hand-rolled binds.
	freshSkin := b.skeleton.CreateSkinFromRestTransforms()
	b.skin = freshSkin
	if b.mesh != MeshInstance3D.Nil {
		b.mesh.SetSkin(freshSkin)
	}
}

// RepositionPartsAnimated walks every attached part and rebinds it
// to the SKINNED surface position computed from the spine's
// current bone poses — so an eye on the head end, a hat on the
// neck, or a pendant on the chest all track the bone-driven
// deformation (breathing, head-look, etc.) instead of staying
// locked at their rest-pose anchor.
//
// The math is linear-blend skinning applied to each part's
// AnchorPoint result:
//
//	pos_posed   = w0·M0·pos_rest + w1·M1·pos_rest
//	out_posed   = w0·M0_basis·out_rest + w1·M1_basis·out_rest
//	Mi          = bone_i_current_global ∘ bind_pose_i
//
// where (boneA, boneB, w0, w1) come from mapping the anchor's T
// onto the spine's segment chain — same weight assignment the
// vertex shader uses, so parts deform consistently with the body
// skin under them.
//
// Leg-foot anchored parts (duck-foot steppers) take the legs path
// instead — they ride animated leg joints rather than the body
// skeleton.
func (b *CritterBody) RepositionPartsAnimated() {
	if b.critter == nil {
		return
	}
	if b.skeleton == Skeleton3D.Nil || b.skin == Skin.Nil {
		b.repositionParts()
		return
	}
	boneCount := b.critter.BoneCount()
	if boneCount == 0 {
		return
	}
	if cap(b.matricesBuf) < boneCount {
		b.matricesBuf = make([]Transform3D.BasisOrigin, boneCount)
	} else {
		b.matricesBuf = b.matricesBuf[:boneCount]
	}
	matrices := b.matricesBuf
	for i := 0; i < boneCount; i++ {
		global := b.skeleton.GetBoneGlobalPose(i)
		bind := b.skin.GetBindPose(i)
		matrices[i] = Transform3D.Mul(global, bind)
	}
	legCount := b.critter.LegCount()
	var legs []critter.Leg
	for id, anchor := range b.partAnchors {
		node, ok := id.Instance()
		if !ok {
			delete(b.partAnchors, id)
			continue
		}
		if anchor.OnLeg {
			if anchor.LegFoot < 0 || anchor.LegFoot >= legCount {
				continue
			}
			var foot critter.Vec3
			if anchor.LegFoot < len(b.animatedFeet) && anchor.LegSide >= 0 && anchor.LegSide <= 1 {
				foot = b.animatedFeet[anchor.LegFoot][anchor.LegSide]
			} else {
				if legs == nil {
					legs = b.critter.LegsView()
				}
				foot = legs[anchor.LegFoot].Foot
				if anchor.LegSide == 1 {
					foot.X = -foot.X
				}
			}
			b.positionPartFlatAtFoot(node, foot)
			continue
		}
		pos, outward, _ := b.critter.AnchorPoint(anchor.T, anchor.Theta, anchor.Offset)
		restPos := Vector3.XYZ{
			X: Float.X(pos.X), Y: Float.X(pos.Y), Z: Float.X(pos.Z),
		}
		restOut := Vector3.XYZ{
			X: Float.X(outward.X), Y: Float.X(outward.Y), Z: Float.X(outward.Z),
		}
		// Map T to (boneA, weightA, boneB, weightB) using the same
		// segment partition BuildMesh uses for vertex weights —
		// keeps parts consistent with the skin under them.
		var boneA, boneB int
		var wA, wB Float.X
		if boneCount <= 1 {
			boneA, boneB, wA, wB = 0, 0, 1, 0
		} else {
			idx, local := critter.MapToSegment(anchor.T, boneCount-1)
			boneA = idx
			boneB = idx + 1
			if boneB >= boneCount {
				boneB = boneCount - 1
			}
			wA = Float.X(1 - local)
			wB = Float.X(local)
		}
		// LBS-blend position (full transform) and outward direction
		// (basis only — no translation, since "outward" is a unit
		// direction not a point).
		posA := Transform3D.Transform(restPos, matrices[boneA])
		posB := Transform3D.Transform(restPos, matrices[boneB])
		posedPos := Vector3.XYZ{
			X: wA*posA.X + wB*posB.X,
			Y: wA*posA.Y + wB*posB.Y,
			Z: wA*posA.Z + wB*posB.Z,
		}
		outA := Basis.Transform(restOut, matrices[boneA].Basis)
		outB := Basis.Transform(restOut, matrices[boneB].Basis)
		posedOut := Vector3.Normalized(Vector3.XYZ{
			X: wA*outA.X + wB*outB.X,
			Y: wA*outA.Y + wB*outB.Y,
			Z: wA*outA.Z + wB*outB.Z,
		})
		right, up := partOrientation(posedOut)
		s := b.partScale
		basis := Basis.XYZ{
			Vector3.MulX(right, s),
			Vector3.MulX(up, s),
			Vector3.MulX(posedOut, s),
		}
		node.AsNode3D().SetTransform(Transform3D.BasisOrigin{
			Basis: basis, Origin: posedPos,
		})
	}
}

// SetBreathe puffs the chest area outward by `amount` (a small
// fraction — 0.02 means 2 % radial expansion). amount=0 returns to
// rest. Only mutates the chest bones' pose SCALE so head-look
// (which sets rotation) can run on the same bones in parallel
// without either feature clobbering the other.
//
// The bone-chain naturally propagates scale to children, so the
// whole upper body breathes subtly with the chest. Parts attached
// via PartAnchor stay anchored to the rest-pose surface and don't
// move — that's the win over the prior MI-level scale breathing,
// which dragged eyes and dressings along for the ride.
func (b *CritterBody) SetBreathe(amount float32) {
	if b.skeleton == Skeleton3D.Nil || b.critter == nil {
		return
	}
	n := b.critter.BoneCount()
	if n < 2 {
		return
	}
	// Concentrate the breathe on the middle of the spine — index n/2
	// for the canonical 5-bone chain lands on the "body" bone, which
	// is what we want. For shorter chains, fall back to whatever bone
	// sits closest to centre.
	chestIdx := n / 2
	if chestIdx >= n {
		chestIdx = n - 1
	}
	s := 1 + amount
	b.skeleton.SetBonePoseScale(chestIdx, Vector3.New(s, s, float32(1)))
}

// SetHeadLookYaw rotates the last few spine bones by `yaw` radians
// around their parent so the head end of the body swings sideways
// without affecting the tail. The total yaw is distributed across
// the head-end bones with a linearly increasing weight, so the
// neck reads as a bend rather than a hinge.
//
// Pass yaw=0 to clear the look — every affected bone snaps back to
// its rest pose. Mutates only the bone POSE ROTATION (via
// SetBonePoseRotation) so it composes with SetBreathe's scale on
// the same bones — the two can run simultaneously without one
// overriding the other.
func (b *CritterBody) SetHeadLookYaw(yaw float32) {
	if b.skeleton == Skeleton3D.Nil || b.critter == nil {
		return
	}
	n := b.critter.BoneCount()
	if n < 2 {
		return
	}
	// Affect the last min(3, n-1) bones — enough for a visible neck
	// arc on a 5-bone default critter, but never the whole spine
	// (which would turn the head-look into a body swivel).
	count := 3
	if count > n-1 {
		count = n - 1
	}
	// Linearly increasing weights summing to 1 across the affected
	// bones; the head end bends the most.
	weights := headLookWeights[count-1]
	for i := 0; i < count; i++ {
		boneIdx := n - count + i
		// Y-axis yaw quaternion: q = (0, sin(θ/2), 0, cos(θ/2)).
		// SetBonePoseRotation leaves position + scale untouched so
		// SetBreathe's chest scale on the same bone composes.
		theta := float64(yaw * weights[i])
		half := theta * 0.5
		s := Float.X(math.Sin(half))
		c := Float.X(math.Cos(half))
		b.skeleton.SetBonePoseRotation(boneIdx, Quaternion.IJKX{
			I: 0, J: s, K: 0, X: c,
		})
	}
}

// headLookWeights[count-1] is the weight vector for `count` affected
// bones (count ∈ 1..3). Linearly increasing, sum to 1. Precomputed
// to avoid the per-frame alloc + division loop in SetHeadLookYaw.
var headLookWeights = [3][]float32{
	{1.0},
	{1.0 / 3, 2.0 / 3},
	{1.0 / 6, 2.0 / 6, 3.0 / 6},
}

// boneIndexStr formats a small non-negative int as decimal without
// pulling in strconv just for the skeleton bone names. Skeleton3D
// requires names but doesn't care what they are — bone index is
// the primary identifier everywhere we look it up later.
func boneIndexStr(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	n := len(buf)
	for i > 0 {
		n--
		buf[n] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[n:])
}

// rebuildLegs re-skins the per-leg MeshInstance3D children to match
// the current critter.Legs() slice. Spawns new MeshInstance3Ds for
// new legs, frees ones that fell off the end, and re-uploads
// vertex/index data for the rest. Each leg keeps its own ArrayMesh
// so the GPU can update them independently — sharing one mesh
// across legs would force a rebuild of all legs whenever any leg
// changed.
func (b *CritterBody) rebuildLegs() {
	if b.critter == nil || b.mesh == MeshInstance3D.Nil {
		return
	}
	legs := b.critter.LegsView()
	// Trim excess.
	for i := len(legs); i < len(b.legNodes); i++ {
		if b.legNodes[i] != MeshInstance3D.Nil {
			b.legNodes[i].AsNode().QueueFree()
		}
	}
	if len(legs) < len(b.legNodes) {
		b.legNodes = b.legNodes[:len(legs)]
		b.legArrayMeshes = b.legArrayMeshes[:len(legs)]
	}
	// Grow.
	for len(b.legNodes) < len(legs) {
		mi := MeshInstance3D.New()
		am := ArrayMesh.New()
		mi.AsMeshInstance3D().SetMesh(am.AsMesh())
		b.mesh.AsNode().AddChild(mi.AsNode())
		b.legNodes = append(b.legNodes, mi)
		b.legArrayMeshes = append(b.legArrayMeshes, am)
	}
	// Re-apply the current material override to every leg — this
	// covers freshly-spawned legs created via the sculpt protocol
	// during ribcage view (which needs the dark/transparent look)
	// as well as the normal case where the override is Nil.
	for _, mi := range b.legNodes {
		mi.AsGeometryInstance3D().SetMaterialOverride(b.legMaterialOverride)
	}
	// Skin each leg's mesh.
	const ringsPerSegment = 6
	const segmentsAround = 8
	for i, leg := range legs {
		m := b.critter.BuildLegMesh(leg, ringsPerSegment, segmentsAround, !b.legOneSided)
		UploadCritterMesh(b.legArrayMeshes[i], m)
	}
}

// UploadCritterMesh copies a critter.Mesh (CPU-side verts/normals/
// indices) into the given ArrayMesh, replacing its single surface.
// Shared by the body's leg renders, the gait pipeline's animated
// leg renders, and the leg-placement ghost preview so they all
// follow the same upload format.
func UploadCritterMesh(am ArrayMesh.Instance, m critter.Mesh) {
	verts := make([]Vector3.XYZ, len(m.Verts))
	for j, v := range m.Verts {
		verts[j] = Vector3.XYZ{X: Float.X(v.X), Y: Float.X(v.Y), Z: Float.X(v.Z)}
	}
	normals := make([]Vector3.XYZ, len(m.Normals))
	for j, n := range m.Normals {
		normals[j] = Vector3.XYZ{X: Float.X(n.X), Y: Float.X(n.Y), Z: Float.X(n.Z)}
	}
	am.ClearSurfaces()
	var arrays [Mesh.ArrayMax]any
	arrays[Mesh.ArrayVertex] = verts
	arrays[Mesh.ArrayNormal] = normals
	arrays[Mesh.ArrayIndex] = m.Indices
	am.AddSurfaceFromArrays(Mesh.PrimitiveTriangles, arrays[:])
}

// repositionParts walks every recorded anchor and snaps the
// corresponding Node3D to the new surface point/orientation.
// Drops anchors for any part whose node has been freed (e.g.
// QueueFree'd by DetachPart and finally collected).
func (b *CritterBody) repositionParts() {
	for id, anchor := range b.partAnchors {
		node, ok := id.Instance()
		if !ok {
			delete(b.partAnchors, id)
			continue
		}
		b.positionPart(node, anchor)
	}
}

// partOrientation builds the (right, up) basis vectors for a part
// whose +Z axis (forward) points along fwd. Up is biased toward
// world +Y so the part is never installed upside-down — a muzzle
// stuck on the underside of the head still has its upper jaw on
// top. When fwd is nearly vertical (no unambiguous "horizontal
// right" exists), we fall back to using +Z as the up reference so
// the basis stays defined.
func partOrientation(fwd Vector3.XYZ) (right, up Vector3.XYZ) {
	worldUp := Vector3.XYZ{X: 0, Y: 1, Z: 0}
	upRef := worldUp
	if Float.Abs(Vector3.Dot(fwd, worldUp)) > 0.99 {
		upRef = Vector3.XYZ{X: 0, Y: 0, Z: 1}
	}
	right = Vector3.Normalized(Vector3.Cross(upRef, fwd))
	up = Vector3.Cross(fwd, right)
	return right, up
}

// positionPart computes the body-space transform for one anchor
// and applies it (plus partScale) to the given Node3D. For body-
// surface anchors the part's local +Z faces outward from the body,
// +Y runs along the spine toward the head; the +X axis falls out of
// the cross product. For leg-foot anchors (LegFoot ≥ 0) the part
// instead sits at the named leg's Foot joint with the body's own
// basis as orientation — duck-foot-style steppers ride the foot
// rather than the body surface so they stay glued to the toe even
// as the user re-articulates the limb.
func (b *CritterBody) positionPart(node Node3D.Instance, anchor PartAnchor) {
	if b.critter == nil {
		return
	}
	if anchor.OnLeg {
		legs := b.critter.LegsView()
		if anchor.LegFoot < 0 || anchor.LegFoot >= len(legs) {
			// Leg was removed since the part was anchored. Leave the
			// node where it last was rather than snapping it to the
			// origin — the editor's RemoveLeg flow is expected to
			// drop these parts explicitly; if it doesn't, the user
			// will at least still be able to find and delete them.
			return
		}
		foot := legs[anchor.LegFoot].Foot
		if anchor.LegSide == 1 {
			foot.X = -foot.X
		}
		b.positionPartFlatAtFoot(node, foot)
		return
	}
	pos, outward, _ := b.critter.AnchorPoint(anchor.T, anchor.Theta, anchor.Offset)
	fwd := Vector3.Normalized(Vector3.XYZ{
		X: Float.X(outward.X), Y: Float.X(outward.Y), Z: Float.X(outward.Z),
	})
	right, up := partOrientation(fwd)
	origin := Vector3.XYZ{
		X: Float.X(pos.X), Y: Float.X(pos.Y), Z: Float.X(pos.Z),
	}
	basis := Basis.XYZ{
		Vector3.MulX(right, b.partScale),
		Vector3.MulX(up, b.partScale),
		Vector3.MulX(fwd, b.partScale),
	}
	node.AsNode3D().SetTransform(Transform3D.BasisOrigin{Basis: basis, Origin: origin})
}

// positionPartFlatAtFoot positions a part at footLocal (body-local
// coordinates) with a world-horizontal basis. Orientation tracks
// only the body's yaw — roll and pitch are projected away so a
// duck-foot stepper stays parallel to the ground while the body
// bobs and leans during gait. World Y is clamped to ≥ 0 so the
// stepper sits on (or above) the ground plate even if the leg's
// own foot would dip below it through body bob or aggressive
// joint drags.
//
// Used both by the rest-pose path (positionPart's OnLeg branch
// above) and by the per-frame animated path (SetAnimatedLegFeet
// below) so both share the same orientation conventions.
func (b *CritterBody) positionPartFlatAtFoot(node Node3D.Instance, footLocal critter.Vec3) {
	if b.mesh == MeshInstance3D.Nil {
		return
	}
	bodyNode := b.mesh.AsNode3D()
	worldPos := bodyNode.ToGlobal(Vector3.XYZ{
		X: Float.X(footLocal.X), Y: Float.X(footLocal.Y), Z: Float.X(footLocal.Z),
	})
	// Min-clamp Y to the ground plate (top surface at world Y = 0,
	// arranged by the editor's ground-lift in ensureLoaded). The
	// foot RIDES the leg — when swing lifts the foot off the
	// ground, the stepper goes up with it; what we want to prevent
	// is body-bob or aggressive joint drags shoving the foot below
	// the plate, which would read as the stepper sinking into the
	// dirt. Above-ground motion stays untouched.
	if worldPos.Y < 0 {
		worldPos.Y = 0
	}
	// Body's local +Z direction in world coords — Z column of its
	// global basis. Project to horizontal so the stepper doesn't
	// inherit any pitch from a body-nod or roll from a body-lean.
	bodyFwd := bodyNode.GlobalTransform().Basis.Z
	bodyFwd.Y = 0
	fwdMag := Float.X(math.Sqrt(float64(bodyFwd.X*bodyFwd.X + bodyFwd.Z*bodyFwd.Z)))
	var fwd Vector3.XYZ
	if fwdMag < 1e-6 {
		// Body somehow facing straight up/down (shouldn't happen in
		// normal use, but the math has to stay defined). Fall back
		// to world +Z so the stepper at least has a sane forward.
		fwd = Vector3.XYZ{Z: 1}
	} else {
		fwd = Vector3.XYZ{X: bodyFwd.X / fwdMag, Z: bodyFwd.Z / fwdMag}
	}
	up := Vector3.XYZ{Y: 1}
	right := Vector3.Cross(up, fwd)
	s := b.partScale
	basis := Basis.XYZ{
		Vector3.MulX(right, s),
		Vector3.MulX(up, s),
		Vector3.MulX(fwd, s),
	}
	node.AsNode3D().SetGlobalTransform(Transform3D.BasisOrigin{Basis: basis, Origin: worldPos})
}

// SetAnimatedLegFeet updates every OnLeg-anchored part to ride the
// given per-(leg, side) animated foot positions instead of the
// data model's rest pose. Called by the gait pipeline every
// PhysicsProcess tick during the control view; pass nil-equivalent
// (len mismatch) and the call short-circuits.
//
// footLocals is indexed as [legIndex][side] where side 0 is the
// canonical +X side and side 1 is the −X mirror. Positions are in
// body-local space, same coordinate system as critter.Leg.Foot.
func (b *CritterBody) SetAnimatedLegFeet(footLocals [][2]critter.Vec3) {
	if b.critter == nil {
		return
	}
	if len(footLocals) != b.critter.LegCount() {
		return
	}
	b.animatedFeet = footLocals
	for id, anchor := range b.partAnchors {
		if !anchor.OnLeg {
			continue
		}
		if anchor.LegFoot < 0 || anchor.LegFoot >= len(footLocals) {
			continue
		}
		if anchor.LegSide < 0 || anchor.LegSide > 1 {
			continue
		}
		node, ok := id.Instance()
		if !ok {
			continue
		}
		b.positionPartFlatAtFoot(node, footLocals[anchor.LegFoot][anchor.LegSide])
	}
}

// ClearAnimatedLegFeet drops the cached gait foot overrides so OnLeg
// parts fall back to the data-model rest position (leg.Foot) the
// next time they're repositioned. Call when exiting the control view
// so steppers settle back to the rest pose under the legs.
func (b *CritterBody) ClearAnimatedLegFeet() {
	b.animatedFeet = nil
}
