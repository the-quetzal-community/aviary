package internal

import (
	"errors"

	"graphics.gd/classdb/ArrayMesh"
	"graphics.gd/classdb/CollisionShape3D"
	"graphics.gd/classdb/ConcavePolygonShape3D"
	"graphics.gd/classdb/Mesh"
	"graphics.gd/classdb/MeshInstance3D"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/PackedScene"
	"graphics.gd/classdb/StaticBody3D"
	"graphics.gd/variant/Basis"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Object"
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
}

// PartAnchor parameterises one attached part's position on the
// critter's surface. (T, Theta) match the convention from
// critter.AnchorPoint: T runs tail→head, Theta sweeps around the
// spine.
//
// Offset has two meanings depending on T (see critter.AnchorPoint):
// for body anchors it lifts the part above the surface along the
// outward normal; for cap anchors (T∈{0,1}) it's the radial
// distance from the spine endpoint within the cap disc plane, so a
// part can land anywhere on the cap rather than only at its centre.
type PartAnchor struct {
	T, Theta float32
	Offset   float32
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
	body := CritterBody{
		critter:        c,
		mesh:           mi,
		arrayMesh:      ArrayMesh.New(),
		collisionShape: collisionShape,
		concaveShape:   concave,
		parts:          parts,
		partAnchors:    make(map[Node3D.ID]PartAnchor),
		partScale:      2.0,
	}
	body.rebuild()
	mi.SetMesh(body.arrayMesh.AsMesh())
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
	if b.parts == Node3D.Nil {
		if b.mesh == MeshInstance3D.Nil {
			return Node3D.Nil
		}
		b.parts = Node3D.New()
		b.mesh.AsNode().AddChild(b.parts.AsNode())
	}
	var node Node3D.Instance
	if scene != PackedScene.Nil {
		inst := scene.Instantiate()
		node = Object.To[Node3D.Instance](inst)
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
func (b *CritterBody) rebuild() {
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
	b.indices = m.Indices
	b.arrayMesh.ClearSurfaces()
	var arrays [Mesh.ArrayMax]any
	arrays[Mesh.ArrayVertex] = b.vertexBuf
	arrays[Mesh.ArrayIndex] = b.indices
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
// and applies it (plus partScale) to the given Node3D. The part's
// local +Z faces outward from the body, +Y runs along the spine
// toward the head; the +X axis falls out of the cross product.
func (b *CritterBody) positionPart(node Node3D.Instance, anchor PartAnchor) {
	if b.critter == nil {
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
