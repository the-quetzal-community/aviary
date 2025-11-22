package internal

import (
	"reflect"
	"strconv"
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

	Mesh MeshInstance3D.Instance
	tree *Tree

	client *Client

	last_slider_sculpt time.Time
}

func (fe *FoliageEditor) Name() string  { return "foliage" }
func (fe *FoliageEditor) EnableEditor() {}
func (fe *FoliageEditor) ChangeEditor() {}

func (fe *FoliageEditor) Ready() {
	fe.tree = Object.Leak(NewTree())
	fe.Mesh.SetMesh(fe.tree.AsMesh())

	leaflet := StandardMaterial3D.New()
	leaflet.AsBaseMaterial3D().SetAlbedoTexture(Resource.Load[Texture2D.Instance]("res://default/leaflet.png"))
	leaflet.AsBaseMaterial3D().SetTransparency(BaseMaterial3D.TransparencyAlphaScissor)
	fe.Mesh.Mesh().SurfaceSetMaterial(1, leaflet.AsMaterial())

	timbers := StandardMaterial3D.New()
	timbers.AsBaseMaterial3D().SetAlbedoTexture(Resource.Load[Texture2D.Instance]("res://default/timbers.png"))
	fe.Mesh.Mesh().SurfaceSetMaterial(0, timbers.AsMaterial())
}

func (fe *FoliageEditor) ExitTree() {
	Object.Free(fe.tree)
}

func (fe *FoliageEditor) Sculpt(brush musical.Sculpt) {
	if brush.Editor != "foliage" {
		return
	}
	editing := brush.Slider
	value := float64(brush.Amount)
	_, prop, _ := strings.Cut(editing, "/")
	rtype := reflect.TypeFor[Tree]()
	for i := range rtype.NumField() {
		field := rtype.Field(i)
		if field.Tag.Get("gd") == prop {
			switch field.Type.Kind() {
			case reflect.Int:
				reflect.ValueOf(fe.tree).Elem().Field(i).SetInt(int64(value))
			case reflect.Float32, reflect.Float64:
				reflect.ValueOf(fe.tree).Elem().Field(i).SetFloat(value)
			}
			fe.tree.recalculating = true
			fe.tree.recalculate()
			return
		}
	}
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
	rtype := reflect.TypeFor[Tree]()
	for i := range rtype.NumField() {
		field := rtype.Field(i)
		if field.Tag.Get("gd") == prop {
			init, _ = strconv.ParseFloat(field.Tag.Get("default"), 64)
			ranges := strings.Split(field.Tag.Get("range"), ",")
			from, _ = strconv.ParseFloat(ranges[0], 64)
			upto, _ = strconv.ParseFloat(ranges[1], 64)
			step := 0.001
			if field.Type.Kind() == reflect.Int {
				step = 1
			}
			return init, from, upto, step
		}
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
