package internal

import (
	"graphics.gd/classdb/Node3D"
	"graphics.gd/variant/Path"
	"graphics.gd/variant/String"
)

type SceneryEditor struct {
	Node3D.Extension[SceneryEditor]

	preview chan Path.ToResource
}

func (es *SceneryEditor) Tabs(mode Mode) []string {
	switch mode {
	case ModeGeometry:
		return []string{
			"foliage",
			"mineral",
			"housing",
			"village",
			"farming",
			"factory",
			"defense",
			"obelisk",
			"citizen",
			"trinket",
			"critter",
			"special",
			"pathway",
			"fencing",
			"vehicle",
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
	select {
	case fe.preview <- Path.ToResource(String.New(design)):
	default:
	}
}
func (fe *SceneryEditor) SliderHandle(mode Mode, editing string, value float64, commit bool) {

}

func (fe *SceneryEditor) SliderConfig(mode Mode, editing string) (init, min, max, step float64) {
	return 0, 0, 1, 0.01
}
