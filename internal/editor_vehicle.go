package internal

import (
	"graphics.gd/classdb/Input"
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/PackedScene"
	"graphics.gd/classdb/Resource"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Vector3"
)

type VehicleEditor struct {
	Node3D.Extension[VehicleEditor]

	Preview       PreviewRenderer
	MirrorPreview PreviewRenderer
}

func (editor *VehicleEditor) Ready() {
	base := Resource.Load[PackedScene.Is[Node.Instance]]("res://base.obj")
	instance := base.Instantiate()
	editor.AsNode().AddChild(instance)
	editor.Preview.AsNode3D().SetScale(Vector3.MulX(editor.Preview.AsNode3D().Scale(), 0.4))
	editor.MirrorPreview.AsNode3D().SetScale(Vector3.MulX(editor.MirrorPreview.AsNode3D().Scale(), 0.4))
}

func (*VehicleEditor) Name() string { return "vehicle" }

func (*VehicleEditor) Tabs(mode Mode) []string {
	switch mode {
	case ModeGeometry:
		return []string{
			"polygon",
			"chassis",
			"cockpit",
			"spinner",
			"spoiler",
			"sailing",
			"gliding",
		}
	case ModeDressing:
		return []string{
			"citizen",
			"critter",
			"cannons",
			"aerials",
			"mirrors",
			"exhaust",
			"engines",
			"torches",
		}
	default:
		return TextureTabs
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
	case ModeGeometry:
		if editor.Preview.AsNode().GetChildCount() > 0 {
			Node.Instance(editor.Preview.AsNode().GetChild(0)).QueueFree()
		}
		if editor.MirrorPreview.AsNode().GetChildCount() > 0 {
			Node.Instance(editor.MirrorPreview.AsNode().GetChild(0)).QueueFree()
		}
		editor.Preview.SetDesign(design)
		editor.MirrorPreview.SetDesign(design)
		scale := editor.MirrorPreview.AsNode3D().Scale()
		scale.X = -scale.X
		editor.MirrorPreview.SetScale(scale)
	}
}

func (*VehicleEditor) SliderConfig(mode Mode, editing string) (init, min, max, step float64) {
	return 0, 0, 1, 0.01
}
func (*VehicleEditor) SliderHandle(mode Mode, editing string, value float64, commit bool) {}
