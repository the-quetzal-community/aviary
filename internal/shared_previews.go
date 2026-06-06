package internal

import (
	"strings"

	"graphics.gd/classdb/CollisionObject3D"
	"graphics.gd/classdb/Mesh"
	"graphics.gd/classdb/MeshInstance3D"
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/PackedScene"
	"graphics.gd/variant/AABB"
	"graphics.gd/variant/Euler"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Transform3D"
	"graphics.gd/variant/Vector3"
)

type PreviewRenderer struct {
	Node3D.Instance

	design string
	// attached is the design whose mesh is currently parented (set in attach),
	// lagging `design` by the async load. Geometry readers (the fence
	// footprint analysis) check it so they don't measure the previous design's
	// mesh in the frame after SetDesign, before the old child is freed.
	attached string
	// gen increments on every SetDesign/Remove so an async scene load
	// that finishes after the user has already moved on to another design
	// (or cleared the preview) can detect it's stale and drop its result
	// instead of attaching the wrong mesh.
	gen int

	// defaultScale is the editor-specific "library placement" factor
	// (0.1 for scenery, 0.2 for shelter, etc.) captured in Ready.
	// hasExplicitScale is true after DuplicateSelection copies a
	// user-modified scale; it prevents re-folding intrinsic scales
	// from the design and tells SetDesign to preserve the explicit value.
	defaultScale     Vector3.XYZ
	hasExplicitScale bool

	// normalizeRotation, when set, makes attach discard the design root's
	// intrinsic rotation so the preview shows the geometry the way the
	// placement path orients it. client_musical.Change overwrites a spawned
	// root's rotation with con.Angles, dropping any baked-in root rotation
	// (Kenney fence panels carry a 180° Y on their root node); a preview that
	// kept it would face the opposite way from what gets placed. Set by the
	// fence tool, which both relies on this match and reads Faces() the same
	// (root-transform-excluded) way.
	normalizeRotation bool
}

func (preview *PreviewRenderer) Design() string {
	return preview.design
}

func (preview *PreviewRenderer) SetDesign(design string) *PreviewRenderer {
	preview.design = design
	preview.attached = "" // the new design's mesh hasn't attached yet
	preview.gen++
	preview.hasExplicitScale = false
	if preview.defaultScale != (Vector3.XYZ{}) {
		preview.AsNode3D().SetScale(preview.defaultScale)
	}
	gen := preview.gen
	// Clear the previous preview immediately so the old design doesn't
	// linger while the new one loads.
	if preview.AsNode().GetChildCount() > 0 {
		Node.Instance(preview.AsNode().GetChild(0)).QueueFree()
	}
	// MakeHuman clothing items (and other raw-OBJ assets) don't ship
	// with .import companions, so Godot's resource loader spews
	// "Resource file not found" errors before returning Nil. Detect
	// them by extension and use the static-mesh loader directly — it
	// uses FileAccess.Open against the same res:// path, which works
	// because the library is mounted into the resource filesystem even
	// though no PackedScene importer ever ran. That path is local-only
	// (no network), so it stays synchronous.
	if strings.HasSuffix(design, ".obj") {
		preview.attach(loadStaticObjNode(design), gen)
		return preview
	}
	// Load the PackedScene on the dedicated loader thread so a not-yet-
	// downloaded design never blocks the main thread / VR compositor. The
	// scene geometry is usually local (preview.pck) and returns quickly;
	// the materials it references stream in afterwards (see
	// shared_materials.go). attach runs on the main thread and drops the
	// result if the user has since selected a different design (gen bump).
	LoadAsync(design, func(scene PackedScene.Is[Node3D.Instance]) {
		if scene == (PackedScene.Is[Node3D.Instance]{}) {
			return
		}
		preview.attach(scene.Instantiate(), gen)
	})
	return preview
}

// attach parents a freshly-loaded preview instance, unless a newer
// SetDesign/Remove has superseded it (gen mismatch), in which case the
// stale instance is discarded. Runs on the main thread.
func (preview *PreviewRenderer) attach(instance Node3D.Instance, gen int) {
	if instance == Node3D.Nil {
		return
	}
	if gen != preview.gen {
		instance.AsNode().QueueFree()
		return
	}
	// A previous attach for this same generation shouldn't happen, but if
	// any child slipped in, clear it so we never stack two previews.
	if preview.AsNode().GetChildCount() > 0 {
		Node.Instance(preview.AsNode().GetChild(0)).QueueFree()
	}
	preview.remove_collisions(instance.AsNode())
	preview.AsNode().AddChild(instance.AsNode())

	// Normalize the freshly instantiated design root to scale 1, folding
	// any non-1 root scale ("preset scale") that the design (e.g. a
	// Kenney .scn) carries into the scale tracked on the PreviewRenderer
	// node itself. This makes the value exposed via .Scale() (and later
	// passed as Change.Bounds or copied via DuplicateSelection) the
	// correct absolute scale to apply to a new root on placement, so the
	// placed model matches the size the user saw in the preview.
	//
	// Only for !hasExplicitScale (i.e. fresh picks from the library, not
	// duplicates of a user-scaled entity) do we multiply the editor's
	// default factor by the intrinsic. Explicit scales are already final.
	s := instance.Scale()
	instance.SetScale(Vector3.New(1, 1, 1))
	if !preview.hasExplicitScale {
		if pscale := preview.AsNode3D().Scale(); pscale != Vector3.One && pscale != (Vector3.XYZ{}) {
			preview.AsNode3D().SetScale(Vector3.Mul(pscale, s))
		}
		// Library-sizing debug mode: match the ghost to the sizes.txt
		// override that applyLibrarySizeOverride will apply on placement,
		// so the model doesn't visibly jump from stale-.glb size to
		// overridden size when dropped.
		if overrides := librarySizeOverrides(); len(overrides) > 0 {
			preview.applySizeOverride(instance, overrides)
		}
	}
	// Discard the design root's intrinsic rotation when asked (the fence tool),
	// so the preview faces the way the placement path will: client_musical.Change
	// overwrites the spawned root's rotation with con.Angles, dropping any baked-in
	// root rotation (Kenney fence panels carry a 180° Y). Only the root is zeroed —
	// descendant transforms are kept, matching the placement (which only sets the
	// root). See normalizeRotation.
	if preview.normalizeRotation {
		instance.SetRotation(Euler.Radians{})
	}
	// Record which design's mesh is now actually displayed, so geometry
	// readers (the fence tool's footprint analysis) don't measure a stale mesh
	// in the window between SetDesign and the async attach landing.
	preview.attached = preview.design
}

// AttachedDesign reports the design whose mesh is currently attached and
// displayed — which lags SetDesign's Design() by the async load. The fence tool
// only trusts Faces()/footprint analysis once this matches the selected design.
func (preview *PreviewRenderer) AttachedDesign() string { return preview.attached }

func (preview *PreviewRenderer) Remove() {
	preview.gen++ // invalidate any in-flight async load
	if preview.AsNode().GetChildCount() > 0 {
		Node.Instance(preview.AsNode().GetChild(0)).QueueFree()
	}
	preview.design = ""
	preview.attached = ""
	preview.hasExplicitScale = false
}

func (preview *PreviewRenderer) AABB() (bounds AABB.PositionSize) {
	return preview.aabb(preview.AsNode3D())
}

// Faces returns every triangle vertex of the loaded design expressed in the
// design root's OWN local frame — the root's own transform excluded but every
// descendant transform applied — then component-multiplied by the preview root
// scale so the points are in world units at identity rotation. That is exactly
// the frame the placement path produces: client_musical.Change overwrites the
// spawned root's rotation (con.Angles) and scale (con.Bounds) while leaving
// descendant transforms intact (Kenney fence panels bake a rotation into their
// root, so measuring the root rotated would mismatch what gets placed).
//
// The fence tool runs a PCA over these points to find the run axis — which is why
// it handles diagonal panels (a 45° strip), not just axis-aligned ones — and to
// tell a thin draggable strip from a blocky corner/blob. Returns nil until a mesh
// has attached.
func (preview *PreviewRenderer) Faces() []Vector3.XYZ {
	if preview.AsNode().GetChildCount() == 0 {
		return nil
	}
	child, ok := Object.As[Node3D.Instance](preview.AsNode().GetChild(0))
	if !ok {
		return nil
	}
	var pts []Vector3.XYZ
	collectFaces(child, &pts)
	s := preview.AsNode3D().Scale()
	for i := range pts {
		pts[i] = Vector3.Mul(pts[i], s)
	}
	return pts
}

// collectFaces appends node's triangle vertices in node's OWN local frame: node's
// own mesh contributes its raw GetFaces (node's own transform NOT applied) and
// each child's contribution is transformed up by that child's transform. So the
// top node's own transform is excluded while every descendant transform is
// applied — matching what the placement path keeps (see Faces).
func collectFaces(node Node3D.Instance, out *[]Vector3.XYZ) {
	if instance, ok := Object.As[MeshInstance3D.Instance](node); ok {
		if mesh := instance.Mesh(); mesh != Mesh.Nil {
			*out = append(*out, mesh.GetFaces()...)
		}
	}
	n := node.AsNode()
	for i := range n.GetChildCount() {
		child, ok := Object.As[Node3D.Instance](n.GetChild(i))
		if !ok {
			continue
		}
		base := len(*out)
		collectFaces(child, out)
		t := child.Transform()
		for j := base; j < len(*out); j++ {
			(*out)[j] = Transform3D.Transform((*out)[j], t)
		}
	}
}

func (preview *PreviewRenderer) aabb(node Node3D.Instance) (bounds AABB.PositionSize) {
	if instance, ok := Object.As[MeshInstance3D.Instance](node); ok {
		bounds = Transform3D.TransformAABB(instance.Mesh().GetAabb(), node.Transform())
	}
	for i := range node.AsNode().GetChildCount() {
		if node, ok := Object.As[Node3D.Instance](node.AsNode().GetChild(i)); ok {
			bounds = AABB.Merge(bounds, preview.aabb(node))
		}
	}
	return bounds
}

func (preview *PreviewRenderer) remove_collisions(node Node.Instance) {
	if body, ok := Object.As[CollisionObject3D.Instance](node); ok {
		body.SetCollisionLayer(0)
	}
	for _, child := range node.GetChildren() {
		preview.remove_collisions(child)
	}
}

// setPickableExceptTerrain walks node and its descendants, toggling
// input_ray_pickable on every CollisionObject3D. This gates Godot's
// viewport physics picking, which is what drives TerrainTile.InputEvent
// (the terrain brush): with placed objects made non-pickable the brush
// ray passes straight through scenery/shelters/etc. and lands on the
// ground underneath, so terrain can be painted below placed objects.
//
// TerrainTile (and the extend/retract arrows nested under it) are
// skipped — they are the only nodes that actually consume viewport
// picking, and must stay pickable so sculpting and world-extension keep
// working. Selection and preview placement use explicit intersect_ray
// queries, which ignore input_ray_pickable, so this never affects them.
func setPickableExceptTerrain(node Node.Instance, pickable bool) {
	if Object.Is[*TerrainTile](node) || Object.Is[*TerrainTileArrow](node) {
		return
	}
	if body, ok := Object.As[CollisionObject3D.Instance](node); ok {
		body.SetInputRayPickable(pickable)
	}
	for _, child := range node.GetChildren() {
		setPickableExceptTerrain(child, pickable)
	}
}
