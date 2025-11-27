package internal

import (
	"slices"

	"graphics.gd/classdb/Input"
	"graphics.gd/classdb/InputEvent"
	"graphics.gd/classdb/InputEventKey"
	"graphics.gd/classdb/InputEventMouseButton"
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/PackedScene"
	"graphics.gd/classdb/Resource"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Vector3"
	"the.quetzal.community/aviary/internal/musical"
)

type VehicleEditor struct {
	Node3D.Extension[VehicleEditor]
	musical.Stubbed

	Objects Node3D.Instance

	Preview       PreviewRenderer
	MirrorPreview PreviewRenderer

	client *Client

	design_to_entity map[musical.Design][]Node3D.ID
	entity_to_object map[musical.Entity]Node3D.ID
	entity_to_mirror map[musical.Entity]Node3D.ID
	object_to_entity map[Node3D.ID]musical.Entity
}

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
			"details",
		}
	case ModeDressing:
		return []string{
			"cannons",
			"aerials",
			"mirrors",
			"exhaust",
			"engines",
			"torches",
			"spinner",
			"walkers",
		}
	default:
		return TextureTabs
	}
}

func (editor *VehicleEditor) Input(event InputEvent.Instance) {
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
		editor.Objects.AsNode().AddChild(node.AsNode())
	case ok && change.Mirror == (Vector3.XYZ{}):
		node.AsNode().QueueFree()
		delete(editor.entity_to_mirror, change.Entity)
		return
	case !ok && change.Mirror == (Vector3.XYZ{}):
		return
	}
	node.AsNode3D().SetPosition(Vector3.Add(parent.Position(), change.Mirror))
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
		exists.SetPosition(change.Offset)
		exists.SetRotation(change.Angles)
		exists.SetScale(Vector3.New(0.3, 0.3, 0.3))
		return nil
	}
	var node Node3D.Instance
	scene, ok := editor.client.packed_scenes[change.Design].Instance()
	if ok {
		node = Object.To[Node3D.Instance](scene.Instantiate())
	} else {
		node = Node3D.New()
	}
	node.SetPosition(change.Offset)
	node.SetRotation(change.Angles)
	node.SetScale(Vector3.Mul(node.Scale(), Vector3.New(0.3, 0.3, 0.3)))
	editor.entity_to_object[change.Entity] = node.ID()
	editor.object_to_entity[node.ID()] = change.Entity
	editor.design_to_entity[change.Design] = append(editor.design_to_entity[change.Design], node.ID())
	editor.remirror(node, change)
	container.AddChild(node.AsNode())
	return nil
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
				editor.MirrorPreview.AsNode3D().SetGlobalPosition(pos)
				editor.MirrorPreview.AsNode3D().SetVisible(true)
			} else {
				editor.MirrorPreview.AsNode3D().SetVisible(false)
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
		editor.Preview.SetDesign(design)
		editor.MirrorPreview.SetDesign(design)
	}
}

func (*VehicleEditor) SliderConfig(mode Mode, editing string) (init, min, max, step float64) {
	return 0, 0, 1, 0.01
}
func (*VehicleEditor) SliderHandle(mode Mode, editing string, value float64, commit bool) {}
