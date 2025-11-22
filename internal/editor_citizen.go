package internal

import "graphics.gd/classdb/Node3D"

type CitizenEditor struct {
	Node3D.Extension[CitizenEditor]
}

func (*CitizenEditor) Name() string { return "citizen" }
func (*CitizenEditor) Tabs(mode Mode) []string {
	switch mode {
	case ModeGeometry:
		return []string{
			"editing/head_size",
			"haircut",
			"stubble",
			//...
		}
	case ModeDressing:
		return []string{
			"helmets",
			"sunnies",
			"pendant",
			"utensil",
			"mittens",
			"daypack",
			"hipwear",
			"jackets",
			"legwear",
			"sandals",
		}
	case ModeMaterial:
		return []string{
			"hairdye",
			"eyetint",
			"pigment",
			"tattoos",
			"posture",
		}
	default:
		return nil
	}
}

func (*CitizenEditor) EnableEditor() {}
func (*CitizenEditor) ChangeEditor() {}

func (*CitizenEditor) SelectDesign(mode Mode, design string) {}

func (*CitizenEditor) SliderConfig(mode Mode, editing string) (init, min, max, step float64) {
	return 0, 0, 1, 0.01
}
func (*CitizenEditor) SliderHandle(mode Mode, editing string, value float64, commit bool) {}
