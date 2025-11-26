package internal

import (
	"graphics.gd/classdb/Node3D"
	"the.quetzal.community/aviary/internal/musical"
)

type ShelterEditor struct {
	Node3D.Extension[ShelterEditor]
	musical.Stubbed
}

func (*ShelterEditor) Name() string { return "shelter" }
func (*ShelterEditor) Tabs(mode Mode) []string {
	switch mode {
	case ModeGeometry:
		return []string{
			"polygon",
			"surface",
			"divider",
			"doorway",
			"windows",
			"roofing",
			"balcony",
			"fencing",
			"columns",
			"ladders",
			"chimney",
		}
	case ModeDressing:
		return []string{
			"bedding",
			"kitchen",
			"bathing",
			"storage",
			"benches",
			"seating",
			"candles",
			"lesiure",
			"trinket",
		}
	default:
		return TextureTabs
	}
}

func (*ShelterEditor) EnableEditor() {}
func (*ShelterEditor) ChangeEditor() {}

func (*ShelterEditor) SelectDesign(mode Mode, design string) {}

func (*ShelterEditor) SliderConfig(mode Mode, editing string) (init, min, max, step float64) {
	return 0, 0, 1, 0.01
}
func (*ShelterEditor) SliderHandle(mode Mode, editing string, value float64, commit bool) {}
