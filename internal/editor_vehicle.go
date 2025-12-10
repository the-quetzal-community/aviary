package internal

import (
	"path"
	"slices"

	"graphics.gd/classdb/Input"
	"graphics.gd/classdb/InputEvent"
	"graphics.gd/classdb/InputEventKey"
	"graphics.gd/classdb/InputEventMouseButton"
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/PackedScene"
	"graphics.gd/classdb/Resource"
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

	base := Resource.Load[PackedScene.Is[Node.Instance]]("res://base.obj")
	instance := base.Instantiate()
	editor.AsNode().AddChild(instance)
	editor.Preview.AsNode3D().SetScale(Vector3.MulX(editor.Preview.AsNode3D().Scale(), 0.3))
	editor.MirrorPreview.AsNode3D().SetScale(Vector3.MulX(editor.MirrorPreview.AsNode3D().Scale(), 0.3))
	scale := editor.MirrorPreview.AsNode3D().Scale()
	scale.X = -scale.X
	editor.MirrorPreview.SetScale(scale)
}

func (*VehicleEditor) Name() string { return "vehicle" }

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
	if event, ok := Object.As[InputEventMouseButton.Instance](event); ok && event.ButtonIndex() == Input.MouseButtonLeft && event.AsInputEvent().IsPressed() {
		editor.client.entity_ids[editor.client.id]++
		var mirror Vector3.XYZ
		if editor.MirrorPreview.Visible() {
			mirror = Vector3.Sub(editor.MirrorPreview.AsNode3D().Position(), editor.Preview.AsNode3D().Position())
		}
		editor.client.space.Change(musical.Change{
			Author: editor.client.id,
			Entity: musical.Entity{
				Author: editor.client.id,
				Number: editor.client.entity_ids[editor.client.id],
			},
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
		if event.AsInputEvent().IsPressed() && (event.Keycode() == Input.KeyDelete || event.Keycode() == Input.KeyBackspace) && !event.AsInputEvent().IsEcho() {
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
		scene, ok := editor.client.packed_scenes[change.Design].Instance()
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
}

func (editor *VehicleEditor) Change(change musical.Change) error {
	container := editor.Objects.AsNode()
	exists, ok := editor.entity_to_object[change.Entity].Instance()
	if ok {
		defer editor.remirror(exists, change)
		if change.Remove {
			idx := slices.Index(editor.design_to_entity[change.Design], exists.ID())
			if idx >= 0 {
				editor.design_to_entity[change.Design] = slices.Delete(editor.design_to_entity[change.Design], idx, idx)
			}
			exists.AsNode().QueueFree()
			return nil
		}
		exists.
			SetPosition(change.Offset).
			SetRotation(change.Angles).
			SetScale(Vector3.New(0.3, 0.3, 0.3))
		return nil
	}
	var node Node3D.Instance
	scene, ok := editor.client.packed_scenes[change.Design].Instance()
	if ok {
		node = Object.To[Node3D.Instance](scene.Instantiate())
	} else {
		node = Node3D.New()
	}
	node.
		SetPosition(change.Offset).
		SetRotation(change.Angles).
		SetScale(Vector3.Mul(node.Scale(), Vector3.New(0.3, 0.3, 0.3)))
	editor.entity_to_object[change.Entity] = node.ID()
	editor.object_to_entity[node.ID()] = change.Entity
	editor.design_to_entity[change.Design] = append(editor.design_to_entity[change.Design], node.ID())
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

func (editor *VehicleEditor) PhysicsProcess(delta Float.X) {
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

				up := Vector3.Normalized(hover.Normal) // Ensure unit length

				// Original preview basis construction
				forward := Vector3.XYZ{0, 0, 1} // Adjust based on your needs
				if Float.Abs(Vector3.Dot(up, forward)) > 0.99 {
					forward = Vector3.XYZ{1, 0, 0}
				}
				right := Vector3.Normalized(Vector3.Cross(up, forward))
				new_forward := Vector3.Normalized(Vector3.Cross(right, up))
				basis := Basis.XYZ{right, up, new_forward}
				editor.Preview.AsNode3D().SetGlobalTransform(Transform3D.BasisOrigin{basis, editor.Preview.AsNode3D().GlobalPosition()})

				// Mirrored preview basis construction
				up_mirror := Vector3.Normalized(Vector3.XYZ{-up.X, up.Y, up.Z})
				forward_mirror := Vector3.XYZ{-forward.X, forward.Y, forward.Z}
				if Float.Abs(Vector3.Dot(up_mirror, forward_mirror)) > 0.99 {
					forward_mirror = Vector3.XYZ{-1, 0, 0} // Mirrored arbitrary fallback
				}
				right_mirror := Vector3.Normalized(Vector3.Cross(up_mirror, forward_mirror))
				new_forward_mirror := Vector3.Normalized(Vector3.Cross(right_mirror, up_mirror))
				basis_mirror := Basis.XYZ{right_mirror, up_mirror, new_forward_mirror}
				editor.MirrorPreview.AsNode3D().SetGlobalTransform(Transform3D.BasisOrigin{basis_mirror, editor.MirrorPreview.AsNode3D().GlobalPosition()})

				editor.Preview.AsNode3D().SetScale(scale)       // Restore scale
				scale.X = -scale.X                              // Mirror scale on X axis
				editor.MirrorPreview.AsNode3D().SetScale(scale) // Restore scale
			}
		}
	}
}

func (*VehicleEditor) EnableEditor() {}
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
