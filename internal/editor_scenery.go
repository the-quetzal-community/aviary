package internal

import (
	"graphics.gd/classdb/Input"
	"graphics.gd/classdb/InputEvent"
	"graphics.gd/classdb/InputEventMouseButton"
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/variant/Angle"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Vector3"
	"the.quetzal.community/aviary/internal/musical"
)

type SceneryEditor struct {
	Node3D.Extension[SceneryEditor]
	musical.Stubbed

	Preview PreviewRenderer

	client *Client
}

func (editor *SceneryEditor) Ready() {
	editor.Preview.AsNode3D().SetScale(Vector3.MulX(editor.Preview.AsNode3D().Scale(), 0.1))
}

func (editor *SceneryEditor) Input(event InputEvent.Instance) {
	if event, ok := Object.As[InputEventMouseButton.Instance](event); ok {
		if Input.IsKeyPressed(Input.KeyShift) {
			if event.ButtonIndex() == Input.MouseButtonWheelUp {
				editor.Preview.AsNode3D().Rotate(Vector3.XYZ{0, 1, 0}, -Angle.Pi/64)
			}
			if event.ButtonIndex() == Input.MouseButtonWheelDown {
				editor.Preview.AsNode3D().Rotate(Vector3.XYZ{0, 1, 0}, Angle.Pi/64)
			}
		}
		if event.ButtonIndex() == Input.MouseButtonRight && event.AsInputEvent().IsPressed() {
			editor.Preview.Remove()
		}
		if event.ButtonIndex() == Input.MouseButtonLeft && event.AsInputEvent().IsPressed() {
			if editor.Preview.Design() != "" {
				design, ok := editor.client.loaded[editor.Preview.Design()]
				if !ok {
					editor.client.design_ids[editor.client.id]++
					design = musical.Design{
						Author: editor.client.id,
						Number: editor.client.design_ids[editor.client.id],
					}
					editor.client.space.Import(musical.Import{
						Design: design,
						Import: editor.Preview.Design(),
					})
				}
				editor.client.entity_ids[editor.client.id]++
				editor.client.space.Change(musical.Change{
					Author: editor.client.id,
					Entity: musical.Entity{
						Author: editor.client.id,
						Number: editor.client.entity_ids[editor.client.id],
					},
					Design: design,
					Offset: editor.Preview.AsNode3D().Position(),
					Angles: editor.Preview.AsNode3D().Rotation(),
					Commit: true,
				})
				if !Input.IsKeyPressed(Input.KeyShift) {
					editor.Preview.Remove()
				}
			}
		}
	}
}

func (editor *SceneryEditor) PhysicsProcess(delta Float.X) {
	if editor.Preview.Design() != "" {
		if hover := MousePicker(editor.AsNode3D()); Object.Is[*TerrainTile](hover.Collider) {
			editor.Preview.AsNode3D().SetGlobalPosition(hover.Position)
		}
	}
}

func (fe *SceneryEditor) Name() string { return "scenery" }

func (fe *SceneryEditor) EnableEditor() {
	fe.client.TerrainEditor.AsNode().SetProcessMode(Node.ProcessModeInherit)
}
func (fe *SceneryEditor) ChangeEditor() {
	fe.client.TerrainEditor.AsNode().SetProcessMode(Node.ProcessModeDisabled)
}

func (es *SceneryEditor) Tabs(mode Mode) []string {
	switch mode {
	case ModeGeometry:
		return []string{
			// natural
			"foliage",
			"mineral",

			// village
			"housing",
			"village",
			"farming",
			"factory",
			"defense",
			"obelisk",
			"utility",

			"fencing",
		}
	case ModeDressing:
		return []string{
			// vehicle
			"citizen",
			"critter",
			"swimmer",
			"scooter",
			"bicycle",
			"roadway",
			"railway",
			"seaship",
			"airship",
			"rockets",
		}
	case ModeMaterial:
		return []string{
			"colours",
			"posture",
		}
	default:
		return nil
	}
}

func (fe *SceneryEditor) SelectDesign(mode Mode, design string) {
	fe.Preview.SetDesign(design)
}

func (fe *SceneryEditor) SliderHandle(mode Mode, editing string, value float64, commit bool) {

}

func (fe *SceneryEditor) SliderConfig(mode Mode, editing string) (init, min, max, step float64) {
	return 0, 0, 1, 0.01
}
