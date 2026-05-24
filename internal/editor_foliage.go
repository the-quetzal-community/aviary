package internal

import (
	"reflect"
	"strings"
	"time"

	"graphics.gd/classdb/BaseMaterial3D"
	"graphics.gd/classdb/Engine"
	"graphics.gd/classdb/MeshInstance3D"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/Resource"
	"graphics.gd/classdb/StandardMaterial3D"
	"graphics.gd/classdb/Texture2D"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Object"
	"the.quetzal.community/aviary/internal/musical"
)

type FoliageEditor struct {
	Node3D.Extension[FoliageEditor]
	musical.Stubbed

	Mesh MeshInstance3D.Instance
	tree *Tree

	client *Client

	last_slider_sculpt time.Time
}

func (*FoliageEditor) Views() []string          { return nil }
func (*FoliageEditor) SwitchToView(view string) {}

func (fe *FoliageEditor) Name() string  { return "foliage" }
func (fe *FoliageEditor) EnableEditor() {}
func (fe *FoliageEditor) ChangeEditor() {}

func (fe *FoliageEditor) Ready() {
	fe.tree = Object.Leak(NewTree())
	fe.Mesh.SetMesh(fe.tree.AsMesh())

	leaflet := StandardMaterial3D.New().
		AsBaseMaterial3D().SetAlbedoTexture(Resource.Load[Texture2D.Instance]("res://default/leaflet.png")).
		AsBaseMaterial3D().SetTransparency(BaseMaterial3D.TransparencyAlphaScissor)
	fe.Mesh.Mesh().SurfaceSetMaterial(1, leaflet.AsMaterial())

	timbers := StandardMaterial3D.New().
		AsBaseMaterial3D().SetAlbedoTexture(Resource.Load[Texture2D.Instance]("res://default/timbers.png"))
	fe.Mesh.Mesh().SurfaceSetMaterial(0, timbers.AsMaterial())
}

func (fe *FoliageEditor) ExitTree() {
	Object.Free(fe.tree)
}

func (fe *FoliageEditor) Sculpt(brush musical.Sculpt) error {
	_, prop, _ := strings.Cut(brush.Slider, "/")
	applyReflectSlider(fe.tree, reflect.TypeFor[Tree](), prop, float64(brush.Amount), func() {
		fe.tree.recalculating = true
		fe.tree.recalculate()
	})
	return nil
}

func (fe *FoliageEditor) Tabs(mode Mode) []string {
	switch mode {
	case ModeGeometry:
		return []string{
			"editing/seed",
			"editing/levels",
			"editing/twig_scale",
			"editing/initial_branch_length",
			"editing/length_falloff_factor",
			"editing/length_falloff_power",
			"editing/clump_min",
			"editing/clump_max",
			"editing/branch_factor",
			"editing/drop_amount",
			"editing/grow_amount",
			"editing/sweep_amount",
			"editing/max_radius",
			"editing/climb_rate",
			"editing/trunk_kink",
			"editing/tree_steps",
			"editing/taper_rate",
			"editing/radius_falloff_rate",
			"editing/twist_rate",
			"editing/trunk_length",
			"editing/v_multiplier",
		}
	case ModeMaterial:
		return []string{
			"foliage/leaflet",
			"foliage/timbers",
		}
	default:
		return nil
	}
}

func (fe *FoliageEditor) SelectDesign(mode Mode, design string) {

}

func (fe *FoliageEditor) SliderConfig(mode Mode, editing string) (init, from, upto, step float64) {
	_, prop, _ := strings.Cut(editing, "/")
	if init, from, upto, step, ok := reflectSliderConfig(reflect.TypeFor[Tree](), prop); ok {
		return init, from, upto, step
	}
	return 0, 0, 1, 0.01
}

func (fe *FoliageEditor) SliderHandle(mode Mode, editing string, value float64, commit bool) {
	if !commit && time.Since(fe.last_slider_sculpt) < time.Second/10 {
		return
	}
	fe.last_slider_sculpt = time.Now()
	if err := fe.client.space.Sculpt(musical.Sculpt{
		Author: fe.client.id,
		Editor: "foliage",
		Slider: editing,
		Amount: Float.X(value),
		Commit: commit,
	}); err != nil {
		Engine.Raise(err)
	}
}
