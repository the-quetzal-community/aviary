package internal

import (
	"time"

	"graphics.gd/classdb/BaseMaterial3D"
	"graphics.gd/classdb/Camera3D"
	"graphics.gd/classdb/Engine"
	"graphics.gd/classdb/Input"
	"graphics.gd/classdb/InputEvent"
	"graphics.gd/classdb/InputEventMouseButton"
	"graphics.gd/classdb/MeshInstance3D"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/SphereMesh"
	"graphics.gd/classdb/StandardMaterial3D"
	"graphics.gd/classdb/Viewport"
	"graphics.gd/variant/Color"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Vector3"

	"the.quetzal.community/aviary/internal/critter"
)

// Sculpting a parametric part (horn, fin): selecting one shows two
// handles on it. Dragging the TIP handle sets its length and curl in one
// motion; dragging the BASE handle sets its thickness. Twist, scale and
// position stay with the ordinary gizmo. Every drag is a critter part
// Change carrying the shape (Commit=false while dragging, so peers watch
// it happen; committed on release), and a part placed as a mirrored pair
// is sculpted together with its twin.

const (
	shapeHandleNone = iota
	shapeHandleTip
	shapeHandleGirth
)

const (
	shapeHandleRadius = 0.03
	shapeHandlePickPx = 18
)

type shapeSculpt struct {
	container  Node3D.Instance
	tip, girth Node3D.Instance

	target Node3D.ID // the part the handles are on
	hover  int       // handle under the cursor
	drag   int       // handle being dragged
	// dirty is set once a drag has actually changed the shape, so a
	// click that never moved doesn't record a no-op edit.
	dirty bool
}

func (ce *CritterEditor) ensureShapeHandles() {
	if ce.shape.container != Node3D.Nil {
		return
	}
	ce.shape.container = Node3D.New()
	ce.AsNode3D().AsNode().AddChild(ce.shape.container.AsNode())
	spawn := func(colour Color.RGBA) Node3D.Instance {
		sphere := SphereMesh.New()
		sphere.SetRadius(shapeHandleRadius)
		sphere.SetHeight(shapeHandleRadius * 2)
		mat := StandardMaterial3D.New()
		mat.AsBaseMaterial3D().SetAlbedoColor(colour)
		mat.AsBaseMaterial3D().SetShadingMode(BaseMaterial3D.ShadingModeUnshaded)
		mat.AsBaseMaterial3D().SetNoDepthTest(true)
		sphere.AsPrimitiveMesh().AsMesh().SurfaceSetMaterial(0, mat.AsMaterial())
		mi := MeshInstance3D.New()
		mi.SetMesh(sphere.AsMesh())
		ce.shape.container.AsNode().AddChild(mi.AsNode())
		return mi.AsNode3D()
	}
	// Same orange as the spine's bone handles: "drag me to reshape".
	ce.shape.tip = spawn(Color.RGBA{R: 0.95, G: 0.7, B: 0.2, A: 1})
	ce.shape.girth = spawn(Color.RGBA{R: 1, G: 1, B: 1, A: 1})
	ce.shape.container.SetVisible(false)
}

// sculptTarget resolves the parametric part the handles belong on: the
// one being dragged, else the selected one.
func (ce *CritterEditor) sculptTarget() (Node3D.Instance, *sweptPart, bool) {
	lookup := func(id Node3D.ID) (Node3D.Instance, *sweptPart, bool) {
		part, ok := ce.animatedParts[id].(*sweptPart)
		if !ok {
			return Node3D.Nil, nil, false
		}
		node, ok := id.Instance()
		return node, part, ok
	}
	if ce.shape.drag != shapeHandleNone {
		return lookup(ce.shape.target)
	}
	if ce.spineEdit || ce.control != nil || ce.placingLeg != "" || ce.Preview.Design() != "" || ce.workbench == nil {
		return Node3D.Nil, nil, false
	}
	selected, ok := ce.workbench.selectedNode()
	if !ok {
		return Node3D.Nil, nil, false
	}
	return lookup(Node3D.ID(selected.ID()))
}

// girthHandleLocal is where the base handle sits: on the rim of the base
// section, on the side away from the curl.
func girthHandleLocal(part *sweptPart) critter.Vec3 {
	_, _, girth := part.shape.Dims()
	return critter.Vec3{Y: -girth * part.profile.Depth}
}

// updateShapeHandles runs every frame: it shows, places and hover-tests
// the handles, and feeds an in-progress drag.
func (ce *CritterEditor) updateShapeHandles() {
	node, part, ok := ce.sculptTarget()
	if !ok {
		if ce.shape.container != Node3D.Nil {
			ce.shape.container.SetVisible(false)
		}
		ce.shape.hover, ce.shape.drag, ce.shape.target = shapeHandleNone, shapeHandleNone, 0
		return
	}
	ce.ensureShapeHandles()
	ce.shape.target = node.ID()
	cam := Viewport.Get(ce.AsNode()).GetCamera3d()
	if cam == Camera3D.Nil {
		return
	}
	mouse := Viewport.Get(ce.AsNode()).GetMousePosition()
	xf := node.GlobalTransform()
	toWorld := func(l critter.Vec3) Vector3.XYZ {
		return Vector3.Add(xf.Origin, Vector3.Add(Vector3.MulX(xf.Basis.Y, Float.X(l.Y)), Vector3.MulX(xf.Basis.Z, Float.X(l.Z))))
	}

	if ce.shape.drag != shapeHandleNone {
		// Both handles move in the part's bend plane (local YZ).
		from := cam.ProjectRayOrigin(mouse)
		dir := Vector3.Normalized(Vector3.Sub(cam.ProjectPosition(mouse, 1000), from))
		if hit, ok := IntersectRayPlane(from, dir, xf.Origin, Vector3.Normalized(xf.Basis.X)); ok {
			d := Vector3.Sub(hit, xf.Origin)
			y := float32(Vector3.Dot(d, xf.Basis.Y) / Vector3.Dot(xf.Basis.Y, xf.Basis.Y))
			z := float32(Vector3.Dot(d, xf.Basis.Z) / Vector3.Dot(xf.Basis.Z, xf.Basis.Z))
			next := part.shape
			switch ce.shape.drag {
			case shapeHandleTip:
				next = next.WithTip(y, z)
			case shapeHandleGirth:
				next = next.WithGirth(max(-y, 0) / part.profile.Depth)
			}
			if next != part.shape {
				ce.shape.dirty = true
				ce.publishShape(node.ID(), next, false)
			}
		}
	}

	tipWorld, girthWorld := toWorld(part.shape.Tip()), toWorld(girthHandleLocal(part))
	ce.shape.tip.SetGlobalPosition(tipWorld)
	ce.shape.girth.SetGlobalPosition(girthWorld)
	ce.shape.container.SetVisible(true)

	if ce.shape.drag == shapeHandleNone {
		ce.shape.hover = shapeHandleNone
		best := Float.X(shapeHandlePickPx)
		for handle, world := range map[int]Vector3.XYZ{shapeHandleTip: tipWorld, shapeHandleGirth: girthWorld} {
			if cam.IsPositionBehind(world) {
				continue
			}
			screen := cam.UnprojectPosition(world)
			dx, dy := screen.X-mouse.X, screen.Y-mouse.Y
			if d := Float.X(Float.Sqrt(dx*dx + dy*dy)); d < best {
				best, ce.shape.hover = d, handle
			}
		}
	}
	// The handle under the cursor (or in hand) swells: the wordless
	// "this is grabbable".
	grow := func(n Node3D.Instance, handle int) {
		s := Float.X(1)
		if ce.shape.hover == handle || ce.shape.drag == handle {
			s = 1.5
		}
		n.SetScale(Vector3.New(s, s, s))
	}
	grow(ce.shape.tip, shapeHandleTip)
	grow(ce.shape.girth, shapeHandleGirth)
}

// shapeUnhandledInput starts and ends handle drags. It reports whether
// it consumed the event (so the click neither changes the selection nor
// arms the gizmo).
func (ce *CritterEditor) shapeUnhandledInput(event InputEvent.Instance) bool {
	mev, ok := Object.As[InputEventMouseButton.Instance](event)
	if !ok || mev.ButtonIndex() != Input.MouseButtonLeft {
		return false
	}
	if mev.AsInputEvent().IsPressed() {
		if ce.shape.hover == shapeHandleNone || ce.shape.drag != shapeHandleNone {
			return false
		}
		ce.shape.drag, ce.shape.dirty = ce.shape.hover, false
		ce.lastEditAt = time.Now()
		Viewport.Get(ce.AsNode()).SetInputAsHandled()
		return true
	}
	if ce.shape.drag == shapeHandleNone {
		return false
	}
	if part, ok := ce.animatedParts[ce.shape.target].(*sweptPart); ok && ce.shape.dirty {
		ce.publishShape(ce.shape.target, part.shape, true)
	}
	ce.shape.drag = shapeHandleNone
	Viewport.Get(ce.AsNode()).SetInputAsHandled()
	return true
}

// publishShape records a new shape for a parametric part — and for its
// mirror twin, if it was placed as one of a pair — as critter part
// Changes. Without a recorder (single-user dev) it applies directly.
func (ce *CritterEditor) publishShape(id Node3D.ID, shape critter.PartShape, commit bool) {
	ids := []Node3D.ID{id}
	if twin, ok := ce.mirrorTwin(id); ok {
		ids = append(ids, twin)
	}
	for _, id := range ids {
		anchor, ok := ce.body.partAnchors[id]
		if !ok {
			continue
		}
		anchor.Shape = shape
		entity, recorded := ce.partToEntity[id]
		if ce.recorder == nil || !recorded {
			ce.body.partAnchors[id] = anchor
			if shaped, ok := ce.animatedParts[id].(shapedPart); ok {
				shaped.SetShape(shape)
			}
			continue
		}
		change := changeFromAnchor(anchor)
		change.Entity = entity
		change.Editor = "critter"
		change.Commit = commit
		if err := ce.recorder.publishChange(change); err != nil && commit {
			Engine.Raise(err)
		}
	}
}

// mirrorTwin finds the part placed as id's reflection across the body's
// midline: the same design, sitting at the mirrored position.
func (ce *CritterEditor) mirrorTwin(id Node3D.ID) (Node3D.ID, bool) {
	node, ok := id.Instance()
	if !ok {
		return 0, false
	}
	at := node.Position()
	if Float.Abs(at.X) < 0.02 {
		return 0, false // on the midline: it is its own reflection
	}
	want := Vector3.New(-at.X, at.Y, at.Z)
	for other := range ce.body.partAnchors {
		if other == id || ce.partURIs[other] != ce.partURIs[id] {
			continue
		}
		if _, parametric := ce.animatedParts[other].(*sweptPart); !parametric {
			continue
		}
		if twin, ok := other.Instance(); ok && Vector3.Distance(twin.Position(), want) < 0.03 {
			return other, true
		}
	}
	return 0, false
}
