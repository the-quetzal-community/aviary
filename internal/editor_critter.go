package internal

import "graphics.gd/classdb/Node3D"

type CritterEditor struct {
	Node3D.Extension[CritterEditor]
}

func (*CritterEditor) Name() string { return "critter" }
func (*CritterEditor) Tabs(mode Mode) []string {
	switch mode {
	case ModeGeometry:
		return []string{
			"sensory",
			"muzzles",
			"grabber",
			"forearm",
			"foreleg",
			"stepper",
			"antlers",
			"gliders",
		}
	case ModeDressing:
		return []string{
			"helmets",
			"sunnies",
			"pendant",
			"utensil",
			"daypack",
			"hipwear",
		}
	default:
		return nil
	}

}

func (*CritterEditor) EnableEditor() {}
func (*CritterEditor) ChangeEditor() {}

func (*CritterEditor) SelectDesign(mode Mode, design string) {}

func (*CritterEditor) SliderConfig(mode Mode, editing string) (init, min, max, step float64) {
	return 0, 0, 1, 0.01
}
func (*CritterEditor) SliderHandle(mode Mode, editing string, value float64, commit bool) {}
