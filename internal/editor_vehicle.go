package internal

import (
	"path"

	"graphics.gd/classdb/Input"
	"graphics.gd/classdb/InputEvent"
	"graphics.gd/classdb/InputEventKey"
	"graphics.gd/classdb/InputEventMouseButton"
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/PackedScene"
	"graphics.gd/variant/Angle"
	"graphics.gd/variant/Basis"
	"graphics.gd/variant/Euler"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Transform3D"
	"graphics.gd/variant/Vector3"
	"the.quetzal.community/aviary/internal/musical"
)

type VehicleEditor struct {
	Node3D.Extension[VehicleEditor]
	musical.Stubbed

	Objects Node3D.Instance
	Spinner Node3D.Instance

	Preview       PreviewRenderer
	MirrorPreview PreviewRenderer

	client *Client

	design_to_entity map[musical.Design][]Node3D.ID
	entity_to_object map[musical.Entity]Node3D.ID
	entity_to_mirror map[musical.Entity]Node3D.ID
	object_to_entity map[Node3D.ID]musical.Entity
}

func (*VehicleEditor) Views() []string          { return nil }
func (*VehicleEditor) SwitchToView(view string) {}

func (editor *VehicleEditor) Ready() {
	editor.design_to_entity = make(map[musical.Design][]Node3D.ID)
	editor.entity_to_object = make(map[musical.Entity]Node3D.ID)
	editor.object_to_entity = make(map[Node3D.ID]musical.Entity)
	editor.entity_to_mirror = make(map[musical.Entity]Node3D.ID)

	base := LoadSync[PackedScene.Is[Node.Instance]]("res://base.obj")
	instance := base.Instantiate()
	editor.AsNode().AddChild(instance)
	editor.Preview.defaultScale = Vector3.New(0.3, 0.3, 0.3)
	editor.Preview.AsNode3D().SetScale(editor.Preview.defaultScale)
	editor.MirrorPreview.defaultScale = Vector3.New(0.3, 0.3, 0.3)
	editor.MirrorPreview.AsNode3D().SetScale(editor.MirrorPreview.defaultScale)
	scale := editor.MirrorPreview.AsNode3D().Scale()
	scale.X = -scale.X
	editor.MirrorPreview.SetScale(scale)
}

func (*VehicleEditor) Name() string { return "vehicle" }

var _ ClickableEditor = (*VehicleEditor)(nil)

func (*VehicleEditor) EditorID() string { return "vehicle" }

// GizmoManipulable implements [ClickableEditor]. Vehicle has no modal
// sub-views, so gizmos are always available.
func (*VehicleEditor) GizmoManipulable() bool { return true }

// EntityForNode implements [ClickableEditor]. Vehicle parts are tracked
// directly by their instantiated node, so no ancestor walk is needed.
func (editor *VehicleEditor) EntityForNode(node Node3D.Instance) (musical.Entity, Node3D.Instance, bool) {
	if e, has := editor.object_to_entity[Node3D.ID(node.ID())]; has {
		return e, node, true
	}
	return musical.Entity{}, Node3D.Nil, false
}

// DesignForNode implements [ClickableEditor].
func (editor *VehicleEditor) DesignForNode(node Node3D.Instance) (musical.Design, bool) {
	if _, has := editor.object_to_entity[Node3D.ID(node.ID())]; !has {
		return musical.Design{}, false
	}
	return findDesignInMap(editor.design_to_entity, Node3D.ID(node.ID()))
}

// ExportSubtree implements the Exporter interface (see export.go).
// We duplicate the Objects and Spinner containers — these hold every
// committed vehicle part — onto a fresh root so the .glb captures the
// car/ship without the base.obj ground plate or preview ghost.
func (editor *VehicleEditor) ExportSubtree() Node3D.Instance {
	root := Node3D.New()
	root.AsNode().SetName("vehicle")
	if editor.Objects != Node3D.Nil {
		if dup, ok := Object.As[Node3D.Instance](editor.Objects.AsNode().Duplicate()); ok {
			root.AsNode().AddChild(dup.AsNode())
		}
	}
	if editor.Spinner != Node3D.Nil {
		if dup, ok := Object.As[Node3D.Instance](editor.Spinner.AsNode().Duplicate()); ok {
			root.AsNode().AddChild(dup.AsNode())
		}
	}
	return root
}

func (*VehicleEditor) Tabs(mode Mode) []string {
	switch mode {
	case ModeGeometry:
		return []string{
			"polygon",
			"chassis",
			"cockpit",
			"spoiler",
			"sailing",
			"gliding",
			"sliding",
			"booster",
		}
	case ModeDressing:
		return []string{
			"cannons",
			"aerials",
			"mirrors",
			"exhaust",
			"details",
			"torches",
			"spinner",
			"walkers",
		}
	default:
		return TextureTabs
	}
}

func (editor *VehicleEditor) UnhandledInput(event InputEvent.Instance) {
	if !editor.AsNode3D().Visible() {
		return
	}
	if event, ok := Object.As[InputEventMouseButton.Instance](event); ok && event.ButtonIndex() == Input.MouseButtonLeft && event.AsInputEvent().IsPressed() {
		var mirror Vector3.XYZ
		if editor.MirrorPreview.Visible() {
			mirror = Vector3.Sub(editor.MirrorPreview.AsNode3D().Position(), editor.Preview.AsNode3D().Position())
		}
		editor.client.space.Change(musical.Change{
			Author: editor.client.id,
			Entity: editor.client.NextEntity(),
			Design: editor.client.MusicalDesign(editor.Preview.Design()),
			Offset: editor.Preview.AsNode3D().Position(),
			Angles: editor.Preview.AsNode3D().Rotation(),
			Editor: "vehicle",
			Mirror: mirror,
			Commit: true,
		})
		if !Input.IsKeyPressed(Input.KeyShift) {
			editor.Preview.Remove()
			editor.MirrorPreview.Remove()
		}
	}
	if event, ok := Object.As[InputEventKey.Instance](event); ok {
		if isDeletePress(event) {
			node, ok := editor.client.selection.Instance()
			if ok {
				if entity, ok := editor.object_to_entity[Node3D.ID(node.ID())]; ok {
					editor.client.space.Change(musical.Change{
						Author: editor.client.id,
						Entity: entity,
						Editor: "vehicle",
						Remove: true,
						Commit: true,
					})
				}
			}
		}
	}
}

func (editor *VehicleEditor) remirror(parent Node3D.Instance, change musical.Change) {
	node, ok := editor.entity_to_mirror[change.Entity].Instance()
	switch {
	case !ok && change.Mirror != (Vector3.XYZ{}):
		scene, ok := editor.client.sceneFor(change.Design)
		if ok {
			node = Object.To[Node3D.Instance](scene.Instantiate())
		} else {
			node = Node3D.New()
		}
		switch {
		case change.Mirror.X != 0:
			node.AsNode3D().SetScale(Vector3.Mul(node.Scale(), Vector3.New(-0.3, 0.3, 0.3)))
		case change.Mirror.Y != 0:
			node.AsNode3D().SetScale(Vector3.Mul(node.Scale(), Vector3.New(0.3, -0.3, 0.3)))
		case change.Mirror.Z != 0:
			node.AsNode3D().SetScale(Vector3.Mul(node.Scale(), Vector3.New(0.3, 0.3, -0.3)))
		}
		editor.entity_to_mirror[change.Entity] = node.ID()
		if path.Base(path.Dir(editor.client.design_to_string[change.Design])) == "spinner" {
			editor.Spinner.AsNode().AddChild(node.AsNode())
		} else {
			editor.Objects.AsNode().AddChild(node.AsNode())
		}
	case ok && change.Mirror == (Vector3.XYZ{}):
		node.AsNode().QueueFree()
		delete(editor.entity_to_mirror, change.Entity)
		return
	case !ok && change.Mirror == (Vector3.XYZ{}):
		return
	}
	node.
		SetPosition(Vector3.Add(parent.Position(), change.Mirror)).
		SetRotation(Euler.Radians{X: change.Angles.X, Y: -change.Angles.Y, Z: -change.Angles.Z})
	// Mirror scale tracks the parent's current scale with the
	// appropriate axis sign-flipped, so the scale gizmo on the main
	// part propagates uniformly to its mirror twin.
	mainScale := parent.Scale()
	switch {
	case change.Mirror.X != 0:
		node.SetScale(Vector3.Mul(mainScale, Vector3.New(-1, 1, 1)))
	case change.Mirror.Y != 0:
		node.SetScale(Vector3.Mul(mainScale, Vector3.New(1, -1, 1)))
	case change.Mirror.Z != 0:
		node.SetScale(Vector3.Mul(mainScale, Vector3.New(1, 1, -1)))
	}
}

func (editor *VehicleEditor) Change(change musical.Change) error {
	if change.Editor != "vehicle" {
		return nil
	}
	container := editor.Objects.AsNode()
	exists, ok := editor.entity_to_object[change.Entity].Instance()
	if ok {
		defer editor.remirror(exists, change)
		if change.Remove {
			removeEntity(editor.design_to_entity, editor.entity_to_object, editor.object_to_entity, change.Design, change.Entity, exists)
			return nil
		}
		exists.
			SetPosition(change.Offset).
			SetRotation(change.Angles)
		// Apply explicit Bounds (scale gizmo) when present; otherwise
		// keep the creation-time 0.3 factor (or the mirror's sign-
		// flipped variant). Mirror parts get their own scale through
		// remirror(); the scale gizmo only edits the main entity.
		// The factor path includes any intrinsic root scale from the design.
		if change.Bounds != Vector3.Zero {
			exists.SetScale(change.Bounds)
		}
		return nil
	}
	var node Node3D.Instance
	scene, ok := editor.client.sceneFor(change.Design)
	if ok {
		node = Object.To[Node3D.Instance](scene.Instantiate())
	} else {
		node = Node3D.New()
	}
	node.
		SetPosition(change.Offset).
		SetRotation(change.Angles)
	if change.Bounds != Vector3.Zero {
		node.SetScale(change.Bounds)
	} else {
		node.SetScale(Vector3.Mul(node.Scale(), Vector3.New(0.3, 0.3, 0.3)))
	}
	registerEntity(editor.design_to_entity, editor.entity_to_object, editor.object_to_entity, change.Design, change.Entity, node)
	editor.remirror(node, change)
	if path.Base(path.Dir(editor.client.design_to_string[change.Design])) == "spinner" {
		editor.Spinner.AsNode().AddChild(node.AsNode())
	} else {
		container.AddChild(node.AsNode())
	}
	return nil
}

func (editor *VehicleEditor) Process(delta Float.X) {
	if editor.Preview.Design() != "" {
		return
	}
	for i := range editor.Spinner.AsNode().GetChildCount() {
		child := Object.To[Node3D.Instance](editor.Spinner.AsNode().GetChild(i))
		child.RotateObjectLocal(Vector3.New(0, 1, 0), 5*Angle.Radians(delta))
	}
}

func (editor *VehicleEditor) PhysicsProcess(_ Float.X) {
	if editor.Preview.Design() != "" {

		if Input.IsMouseButtonPressed(Input.MouseButtonRight) {
			editor.Preview.Remove()
			editor.MirrorPreview.Remove()
			return
		}
		if hover := MousePicker(editor.AsNode3D()); hover.Collider != Object.Nil {
			pos := hover.Position
			if pos.X < 0.2 && pos.X > -0.2 {
				pos.X = 0
			}
			editor.Preview.AsNode3D().SetGlobalPosition(pos)
			if pos.X != 0 {
				pos.X = -pos.X
				editor.MirrorPreview.AsNode3D().
					SetGlobalPosition(pos).
					SetVisible(true)
			} else {
				editor.MirrorPreview.AsNode3D().SetVisible(false)
			}
			if editor.client.ui.mode == ModeDressing {
				scale := editor.Preview.AsNode3D().Scale() // Capture existing scale

				up := Vector3.Normalized(hover.Normal)

				forward := Vector3.XYZ{0, 0, 1}
				if Float.Abs(Vector3.Dot(up, forward)) > 0.99 {
					forward = Vector3.XYZ{1, 0, 0}
				}
				right := Vector3.Normalized(Vector3.Cross(up, forward))
				new_forward := Vector3.Normalized(Vector3.Cross(right, up))
				basis := Basis.XYZ{right, up, new_forward}
				editor.Preview.AsNode3D().SetGlobalTransform(Transform3D.BasisOrigin{basis, editor.Preview.AsNode3D().GlobalPosition()})

				up_mirror := Vector3.Normalized(Vector3.XYZ{-up.X, up.Y, up.Z})
				forward_mirror := Vector3.XYZ{-forward.X, forward.Y, forward.Z}
				if Float.Abs(Vector3.Dot(up_mirror, forward_mirror)) > 0.99 {
					forward_mirror = Vector3.XYZ{-1, 0, 0}
				}
				right_mirror := Vector3.Normalized(Vector3.Cross(up_mirror, forward_mirror))
				new_forward_mirror := Vector3.Normalized(Vector3.Cross(right_mirror, up_mirror))
				basis_mirror := Basis.XYZ{right_mirror, up_mirror, new_forward_mirror}
				editor.MirrorPreview.AsNode3D().SetGlobalTransform(Transform3D.BasisOrigin{basis_mirror, editor.MirrorPreview.AsNode3D().GlobalPosition()})

				editor.Preview.AsNode3D().SetScale(scale)
				scale.X = -scale.X
				editor.MirrorPreview.AsNode3D().SetScale(scale)
			}
		}
	}
}

func (editor *VehicleEditor) EnableEditor() {
	editor.client.SetGizmos([]Gizmo{
		GizmoPoint, GizmoShift, GizmoTwist, GizmoFloat,
		GizmoSpace, GizmoClone, GizmoTrash,
	})
}
func (*VehicleEditor) ChangeEditor() {}

func (editor *VehicleEditor) SelectDesign(mode Mode, design string) {
	switch mode {
	case ModeGeometry, ModeDressing:
		if editor.Preview.AsNode().GetChildCount() > 0 {
			Node.Instance(editor.Preview.AsNode().GetChild(0)).QueueFree()
		}
		if editor.MirrorPreview.AsNode().GetChildCount() > 0 {
			Node.Instance(editor.MirrorPreview.AsNode().GetChild(0)).QueueFree()
		}
		editor.Preview.
			SetDesign(design).
			SetRotation(Euler.Radians{})
		editor.MirrorPreview.SetDesign(design)
	}
}

func (*VehicleEditor) SliderConfig(mode Mode, editing string) (init, min, max, step float64) {
	return 0, 0, 1, 0.01
}
func (*VehicleEditor) SliderHandle(mode Mode, editing string, value float64, commit bool) {}
