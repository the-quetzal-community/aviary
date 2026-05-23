package critter

// SliderSpec describes one slider exposed by the critter editor.
// Unlike the citizen catalog (which maps to MakeHuman shape-key
// pairs) each spec here drives a single numeric weight that
// ComputeShape interprets directly — so Decr/Incr aren't needed.
type SliderSpec struct {
	// Tab is the editor key, e.g. "shape/length".
	Tab string
	// Min, Max bound the slider value; Init is the neutral position.
	Min, Max, Init float64
}

// Specs is the v1 critter slider catalog. Order is the order the
// UI displays them under ModeGeometry. Add to this to expose more
// shape knobs; ComputeShape just reads weights by name and ignores
// anything it doesn't recognise, so adding an entry here and then
// teaching ComputeShape to consume it is the whole change.
func Specs() []SliderSpec {
	return []SliderSpec{
		{Tab: "shape/length", Min: -1, Max: 1, Init: 0},
		{Tab: "shape/arch", Min: -1, Max: 1, Init: 0},
		{Tab: "shape/neck_lift", Min: -1, Max: 1, Init: 0},
		{Tab: "shape/head_size", Min: -1, Max: 1, Init: 0},
		{Tab: "shape/body_size", Min: -1, Max: 1, Init: 0},
		{Tab: "shape/tail_size", Min: -1, Max: 1, Init: 0},
	}
}
