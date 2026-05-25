package internal

import (
	"encoding/json"
	"fmt"
	"path"
	"reflect"
	"strings"
	"time"

	"graphics.gd/classdb/AtlasTexture"
	"graphics.gd/classdb/BaseMaterial3D"
	"graphics.gd/classdb/Engine"
	"graphics.gd/classdb/FileAccess"
	"graphics.gd/classdb/MeshInstance3D"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/Resource"
	"graphics.gd/classdb/StandardMaterial3D"
	"graphics.gd/classdb/Texture2D"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Rect2"
	"the.quetzal.community/aviary/internal/musical"
)

type FoliageEditor struct {
	Node3D.Extension[FoliageEditor]
	musical.Stubbed

	Mesh MeshInstance3D.Instance
	tree *Tree

	leafletMaterial BaseMaterial3D.Instance
	timbersMaterial BaseMaterial3D.Instance

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

	fe.leafletMaterial = StandardMaterial3D.New().
		AsBaseMaterial3D().SetAlbedoTexture(Resource.Load[Texture2D.Instance]("res://default/leaflet.png")).
		AsBaseMaterial3D().SetTransparency(BaseMaterial3D.TransparencyAlphaScissor)
	fe.Mesh.Mesh().SurfaceSetMaterial(1, fe.leafletMaterial.AsMaterial())

	fe.timbersMaterial = StandardMaterial3D.New().
		AsBaseMaterial3D().SetAlbedoTexture(Resource.Load[Texture2D.Instance]("res://default/timbers.png"))
	fe.Mesh.Mesh().SurfaceSetMaterial(0, fe.timbersMaterial.AsMaterial())
}

func (fe *FoliageEditor) ExitTree() {
	Object.Free(fe.tree)
}

func (fe *FoliageEditor) Sculpt(brush musical.Sculpt) error {
	switch brush.Slider {
	case "leaflet", "timbers":
		var target BaseMaterial3D.Instance
		switch brush.Slider {
		case "leaflet":
			target = fe.leafletMaterial
		case "timbers":
			target = fe.timbersMaterial
		}
		texture := fe.resolveMaterialTexture(brush.Design)
		if texture == Texture2D.Nil {
			return nil
		}
		target.SetAlbedoTexture(texture)
		return nil
	}
	_, prop, _ := strings.Cut(brush.Slider, "/")
	applyReflectSlider(fe.tree, reflect.TypeFor[Tree](), prop, float64(brush.Amount), func() {
		fe.tree.recalculating = true
		fe.tree.recalculate()
	})
	return nil
}

// resolveMaterialTexture turns a foliage material Design into a usable
// Texture2D. For legacy .png paths it just loads the file (or returns
// the cached import). For .region sidecars it reads the JSON, loads the
// referenced shared material from <author>/texture/<hash>.tres, and
// wraps its albedo in an AtlasTexture with the recorded region.
func (fe *FoliageEditor) resolveMaterialTexture(design musical.Design) Texture2D.Instance {
	uri, ok := fe.client.design_to_string[design]
	if !ok {
		return Texture2D.Nil
	}
	if strings.HasSuffix(uri, ".region") {
		return loadRegionTexture(uri)
	}
	if tex, ok := fe.client.textures[design].Instance(); ok {
		return tex
	}
	return Resource.Load[Texture2D.Instance](uri)
}

type regionSidecar struct {
	Material string     `json:"material"`
	Region   [4]float64 `json:"region"`
}

func loadRegionTexture(uri string) Texture2D.Instance {
	f := FileAccess.Open(uri, FileAccess.Read)
	if f == FileAccess.Nil {
		return Texture2D.Nil
	}
	var sidecar regionSidecar
	if err := json.Unmarshal([]byte(f.GetAsText()), &sidecar); err != nil {
		return Texture2D.Nil
	}
	mat := Resource.Load[BaseMaterial3D.Instance](sidecar.Material)
	if mat == BaseMaterial3D.Nil {
		return Texture2D.Nil
	}
	src := mat.AlbedoTexture()
	if src == Texture2D.Nil {
		return Texture2D.Nil
	}
	atlas := AtlasTexture.New()
	atlas.SetAtlas(src)
	atlas.SetRegion(Rect2.New(sidecar.Region[0], sidecar.Region[1], sidecar.Region[2], sidecar.Region[3]))
	return atlas.AsTexture2D()
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
			"leaflet",
			"timbers",
		}
	default:
		return nil
	}
}

func (fe *FoliageEditor) SelectDesign(mode Mode, design string) {
	if mode != ModeMaterial {
		return
	}
	var slider string
	switch path.Base(path.Dir(design)) {
	case "leaflet":
		slider = "leaflet"
	case "timbers":
		slider = "timbers"
	default:
		return
	}
	fmt.Println("foliage select:", slider, design)
	if strings.HasSuffix(design, ".region") {
		if f := FileAccess.Open(design, FileAccess.Read); f != FileAccess.Nil {
			fmt.Println("  region body:", f.GetAsText())
		}
	}
	if err := fe.client.space.Sculpt(musical.Sculpt{
		Author: fe.client.id,
		Editor: "foliage",
		Slider: slider,
		Design: fe.client.MusicalDesign(design),
		Commit: true,
	}); err != nil {
		Engine.Raise(err)
	}
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
