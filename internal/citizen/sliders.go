// Package citizen catalogues the body-shape sliders driving the citizen
// editor's character customisation. Slider names refer to MakeHuman
// .target shape-key names; the runtime applies them as sparse vertex
// deltas against the base mesh.
//
// The catalogue mirrors the layout of MakeHuman's own modeling_sliders.json
// (CC0) but is pruned to a video-game scope:
//   - Left/right symmetric pairs are collapsed into single sliders (the
//     runtime applies the matching r- and l- shape keys together).
//   - Fine-grained measurement and asymmetry sliders are omitted.
//
// CitizenEditor calls [EditorSpecs] to project this catalogue into the
// editor's tab names; the catalogue is the single source of truth.
package citizen

import "strings"

// Slider drives one or two shape keys on the citizen mesh.
//
// For a two-way slider Decrease and Increase are both set; the slider value
// is in [-1, 1] and the runtime applies Decrease at -1 and Increase at +1,
// linearly interpolated (mutually exclusive at any positive/negative side).
//
// For a one-way slider Decrease is empty and the value range is [0, 1].
type Slider struct {
	Label    string
	Decrease string
	Increase string
}

// SymmetricSlider drives a left/right pair of shape keys together with a
// single value. The runtime applies both keys at the same weight.
type SymmetricSlider struct {
	Label    string
	Decrease [2]string // {left, right}; empty pair means one-way
	Increase [2]string
}

// Group bundles related sliders into a single editor sub-panel.
type Group struct {
	Name             string
	Sliders          []Slider
	SymmetricSliders []SymmetricSlider
}

// Geometry is the full catalogue of body-shape sliders, organised by the
// citizen editor's geometry sub-panels. CitizenEditor.Tabs(ModeGeometry)
// chooses which of these Groups to expose.
var Geometry = []Group{
	{
		Name: "head",
		Sliders: []Slider{
			{"Age", "head/head-age-decr", "head/head-age-incr"},
			{"Fat", "head/head-fat-decr", "head/head-fat-incr"},
			{"Angle", "head/head-angle-in", "head/head-angle-out"},
			{"Scale depth", "head/head-scale-depth-decr", "head/head-scale-depth-incr"},
			{"Scale horizontal", "head/head-scale-horiz-decr", "head/head-scale-horiz-incr"},
			{"Scale vertical", "head/head-scale-vert-decr", "head/head-scale-vert-incr"},
			{"Back scale depth", "head/head-back-scale-depth-decr", "head/head-back-scale-depth-incr"},
			// Discrete head shapes (one-way, mutually exclusive in UI).
			{"Shape: oval", "", "head/head-oval"},
			{"Shape: round", "", "head/head-round"},
			{"Shape: square", "", "head/head-square"},
			{"Shape: rectangular", "", "head/head-rectangular"},
			{"Shape: triangular", "", "head/head-triangular"},
			{"Shape: inverted triangle", "", "head/head-invertedtriangular"},
			{"Shape: diamond", "", "head/head-diamond"},
		},
	},
	{
		Name: "forehead",
		Sliders: []Slider{
			{"Bulge", "forehead/forehead-trans-backward", "forehead/forehead-trans-forward"},
			{"Scale vertical", "forehead/forehead-scale-vert-decr", "forehead/forehead-scale-vert-incr"},
			{"Cranic shape", "forehead/forehead-nubian-decr", "forehead/forehead-nubian-incr"},
			{"Temple bulge", "forehead/forehead-temple-decr", "forehead/forehead-temple-incr"},
		},
	},
	{
		Name: "eyebrows",
		Sliders: []Slider{
			{"Bulge", "eyebrows/eyebrows-trans-backward", "eyebrows/eyebrows-trans-forward"},
			{"Angle", "eyebrows/eyebrows-angle-down", "eyebrows/eyebrows-angle-up"},
			{"Height", "eyebrows/eyebrows-trans-down", "eyebrows/eyebrows-trans-up"},
		},
	},
	{
		Name: "eyes",
		SymmetricSliders: []SymmetricSlider{
			{"Size", [2]string{"eyes/l-eye-scale-decr", "eyes/r-eye-scale-decr"}, [2]string{"eyes/l-eye-scale-incr", "eyes/r-eye-scale-incr"}},
			{"Horizontal position", [2]string{"eyes/l-eye-trans-in", "eyes/r-eye-trans-in"}, [2]string{"eyes/l-eye-trans-out", "eyes/r-eye-trans-out"}},
			{"Vertical position", [2]string{"eyes/l-eye-trans-down", "eyes/r-eye-trans-down"}, [2]string{"eyes/l-eye-trans-up", "eyes/r-eye-trans-up"}},
			{"Epicanthus", [2]string{"eyes/l-eye-epicanthus-in", "eyes/r-eye-epicanthus-in"}, [2]string{"eyes/l-eye-epicanthus-out", "eyes/r-eye-epicanthus-out"}},
			{"Eyefold angle", [2]string{"eyes/l-eye-eyefold-angle-down", "eyes/r-eye-eyefold-angle-down"}, [2]string{"eyes/l-eye-eyefold-angle-up", "eyes/r-eye-eyefold-angle-up"}},
			{"Eyefold volume", [2]string{"eyes/l-eye-eyefold-concave", "eyes/r-eye-eyefold-concave"}, [2]string{"eyes/l-eye-eyefold-convex", "eyes/r-eye-eyefold-convex"}},
			{"Eye bag", [2]string{"eyes/l-eye-bag-decr", "eyes/r-eye-bag-decr"}, [2]string{"eyes/l-eye-bag-incr", "eyes/r-eye-bag-incr"}},
		},
	},
	{
		Name: "nose",
		Sliders: []Slider{
			{"Scale vertical", "nose/nose-scale-vert-decr", "nose/nose-scale-vert-incr"},
			{"Scale horizontal", "nose/nose-scale-horiz-decr", "nose/nose-scale-horiz-incr"},
			{"Scale depth", "nose/nose-scale-depth-decr", "nose/nose-scale-depth-incr"},
			{"Volume", "nose/nose-volume-decr", "nose/nose-volume-incr"},
			{"Base height", "nose/nose-base-down", "nose/nose-base-up"},
			{"Bridge curve", "nose/nose-curve-concave", "nose/nose-curve-convex"},
			{"Bridge hump", "nose/nose-hump-decr", "nose/nose-hump-incr"},
			{"Tip height", "nose/nose-point-down", "nose/nose-point-up"},
			{"Tip width", "nose/nose-point-width-decr", "nose/nose-point-width-incr"},
			{"Nostrils width", "nose/nose-nostrils-width-decr", "nose/nose-nostrils-width-incr"},
			{"Nostrils flare", "nose/nose-flaring-decr", "nose/nose-flaring-incr"},
			{"Septum angle", "nose/nose-septumangle-decr", "nose/nose-septumangle-incr"},
		},
	},
	{
		Name: "mouth",
		Sliders: []Slider{
			{"Scale horizontal", "mouth/mouth-scale-horiz-decr", "mouth/mouth-scale-horiz-incr"},
			{"Scale vertical", "mouth/mouth-scale-vert-decr", "mouth/mouth-scale-vert-incr"},
			{"Upper lip height", "mouth/mouth-upperlip-height-decr", "mouth/mouth-upperlip-height-incr"},
			{"Upper lip volume", "mouth/mouth-upperlip-volume-decr", "mouth/mouth-upperlip-volume-incr"},
			{"Lower lip height", "mouth/mouth-lowerlip-height-decr", "mouth/mouth-lowerlip-height-incr"},
			{"Lower lip volume", "mouth/mouth-lowerlip-volume-decr", "mouth/mouth-lowerlip-volume-incr"},
			{"Cupids bow", "mouth/mouth-cupidsbow-decr", "mouth/mouth-cupidsbow-incr"},
			{"Philtrum volume", "mouth/mouth-philtrum-volume-decr", "mouth/mouth-philtrum-volume-incr"},
			{"Mouth angles", "mouth/mouth-angles-down", "mouth/mouth-angles-up"},
			{"Dimples", "mouth/mouth-dimples-in", "mouth/mouth-dimples-out"},
		},
	},
	{
		Name: "ears",
		SymmetricSliders: []SymmetricSlider{
			{"Size", [2]string{"ears/l-ear-scale-decr", "ears/r-ear-scale-decr"}, [2]string{"ears/l-ear-scale-incr", "ears/r-ear-scale-incr"}},
			{"Lobe", [2]string{"ears/l-ear-lobe-decr", "ears/r-ear-lobe-decr"}, [2]string{"ears/l-ear-lobe-incr", "ears/r-ear-lobe-incr"}},
			{"Flap", [2]string{"ears/l-ear-flap-decr", "ears/r-ear-flap-decr"}, [2]string{"ears/l-ear-flap-incr", "ears/r-ear-flap-incr"}},
			{"Rotation", [2]string{"ears/l-ear-rot-backward", "ears/r-ear-rot-backward"}, [2]string{"ears/l-ear-rot-forward", "ears/r-ear-rot-forward"}},
			{"Pointed", [2]string{"", ""}, [2]string{"ears/l-ear-shape-pointed", "ears/r-ear-shape-pointed"}},
		},
	},
	{
		Name: "chin",
		Sliders: []Slider{
			{"Height", "chin/chin-height-decr", "chin/chin-height-incr"},
			{"Bones", "chin/chin-bones-decr", "chin/chin-bones-incr"},
			{"Cleft", "chin/chin-cleft-decr", "chin/chin-cleft-incr"},
			{"Jaw drop", "chin/chin-jaw-drop-decr", "chin/chin-jaw-drop-incr"},
		},
	},
	{
		Name: "cheek",
		SymmetricSliders: []SymmetricSlider{
			{"Bones", [2]string{"cheek/l-cheek-bones-decr", "cheek/r-cheek-bones-decr"}, [2]string{"cheek/l-cheek-bones-incr", "cheek/r-cheek-bones-incr"}},
			{"Volume", [2]string{"cheek/l-cheek-volume-decr", "cheek/r-cheek-volume-decr"}, [2]string{"cheek/l-cheek-volume-incr", "cheek/r-cheek-volume-incr"}},
			{"Inner", [2]string{"cheek/l-cheek-inner-decr", "cheek/r-cheek-inner-decr"}, [2]string{"cheek/l-cheek-inner-incr", "cheek/r-cheek-inner-incr"}},
			{"Position", [2]string{"cheek/l-cheek-trans-down", "cheek/r-cheek-trans-down"}, [2]string{"cheek/l-cheek-trans-up", "cheek/r-cheek-trans-up"}},
		},
	},
	{
		Name: "neck",
		Sliders: []Slider{
			{"Scale depth", "neck/neck-scale-depth-decr", "neck/neck-scale-depth-incr"},
			{"Scale horizontal", "neck/neck-scale-horiz-decr", "neck/neck-scale-horiz-incr"},
			{"Scale vertical", "neck/neck-scale-vert-decr", "neck/neck-scale-vert-incr"},
			{"Double chin", "neck/neck-double-decr", "neck/neck-double-incr"},
		},
	},
	{
		Name: "torso",
		Sliders: []Slider{
			{"Scale depth", "torso/torso-scale-depth-decr", "torso/torso-scale-depth-incr"},
			{"Scale horizontal", "torso/torso-scale-horiz-decr", "torso/torso-scale-horiz-incr"},
			{"Pectoral", "torso/torso-muscle-pectoral-decr", "torso/torso-muscle-pectoral-incr"},
			{"Dorsi", "torso/torso-muscle-dorsi-decr", "torso/torso-muscle-dorsi-incr"},
		},
	},
	{
		Name: "body",
		Sliders: []Slider{
			{"Hip scale horizontal", "hip/hip-scale-horiz-decr", "hip/hip-scale-horiz-incr"},
			{"Hip scale vertical", "hip/hip-scale-vert-decr", "hip/hip-scale-vert-incr"},
			{"Stomach tone", "stomach/stomach-tone-decr", "stomach/stomach-tone-incr"},
			{"Buttocks volume", "buttocks/buttocks-volume-decr", "buttocks/buttocks-volume-incr"},
		},
	},
}

// EditorSpec is the runtime shape of one editor slider: the tab name it
// shows up under, the shape-key list to apply at negative weight (Decr)
// and the list at positive weight (Incr). A one-way slider has empty
// Decr and value range [0,1]; a two-way has both sides and range [-1,1].
type EditorSpec struct {
	Tab  string
	Decr []string
	Incr []string
}

// EditorSpecs flattens the [Geometry] catalogue into the form the
// CitizenEditor consumes. Tab names are built from the group name plus
// the slider's slugged label: group "nose" + label "Bridge curve" →
// `editing/nose_bridge_curve`. SymmetricSliders bundle both left and
// right shape keys into a single tab so the user moves them together.
func EditorSpecs() []EditorSpec {
	var out []EditorSpec
	for _, g := range Geometry {
		for _, s := range g.Sliders {
			spec := EditorSpec{Tab: "editing/" + g.Name + "_" + slug(s.Label)}
			if s.Decrease != "" {
				spec.Decr = []string{s.Decrease}
			}
			if s.Increase != "" {
				spec.Incr = []string{s.Increase}
			}
			out = append(out, spec)
		}
		for _, s := range g.SymmetricSliders {
			spec := EditorSpec{Tab: "editing/" + g.Name + "_" + slug(s.Label)}
			for _, k := range s.Decrease {
				if k != "" {
					spec.Decr = append(spec.Decr, k)
				}
			}
			for _, k := range s.Increase {
				if k != "" {
					spec.Incr = append(spec.Incr, k)
				}
			}
			out = append(out, spec)
		}
	}
	return out
}

// slug converts a human label ("Bridge curve", "Shape: oval") into a
// snake_case tab fragment ("bridge_curve", "shape_oval").
func slug(label string) string {
	s := strings.ToLower(label)
	s = strings.ReplaceAll(s, ": ", "_")
	s = strings.ReplaceAll(s, " ", "_")
	s = strings.ReplaceAll(s, ":", "")
	return s
}
