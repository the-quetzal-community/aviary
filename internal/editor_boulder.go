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

type BoulderEditor struct {
	Node3D.Extension[BoulderEditor]
	musical.Stubbed

	Mesh MeshInstance3D.Instance
	rock *Rock

	mineralMaterial BaseMaterial3D.Instance

	client *Client

	last_slider_sculpt time.Time
}

func (fe *BoulderEditor) Name() string { return "boulder" }
func (fe *BoulderEditor) EnableEditor() {

}
func (fe *BoulderEditor) ChangeEditor() {

}

func (fe *BoulderEditor) Views() []string       { return nil }
func (*BoulderEditor) SwitchToView(view string) {}

func (fe *BoulderEditor) Ready() {
	fe.rock = Object.Leak(NewRock())
	fe.Mesh.SetMesh(fe.rock.AsMesh())

	fe.mineralMaterial = StandardMaterial3D.New().
		AsBaseMaterial3D().SetAlbedoTexture(Resource.Load[Texture2D.Instance]("res://default/mineral.jpg")).
		AsBaseMaterial3D().SetUv1Triplanar(true)
	fe.Mesh.Mesh().SurfaceSetMaterial(0, fe.mineralMaterial.AsMaterial())
}

func (fe *BoulderEditor) ExitTree() {
	Object.Free(fe.rock)
}

func (fe *BoulderEditor) Sculpt(brush musical.Sculpt) error {
	editing := brush.Slider
	value := float64(brush.Amount)
	switch editing {
	case "mineral":
		texture := fe.client.resolveMaterialTexture(brush.Design)
		if texture == Texture2D.Nil {
			return nil
		}
		fe.mineralMaterial.SetAlbedoTexture(texture)
		return nil
	case "editing/width":
		scale := fe.Mesh.AsNode3D().Scale()
		scale.X = Float.X(value)
		fe.Mesh.AsNode3D().SetScale(scale)
		return nil
	case "editing/height":
		scale := fe.Mesh.AsNode3D().Scale()
		scale.Y = Float.X(value)
		fe.Mesh.AsNode3D().SetScale(scale)
		return nil
	case "editing/depth":
		scale := fe.Mesh.AsNode3D().Scale()
		scale.Z = Float.X(value)
		fe.Mesh.AsNode3D().SetScale(scale)
		return nil
	}
	_, prop, _ := strings.Cut(editing, "/")
	applyReflectSlider(fe.rock, reflect.TypeFor[Rock](), prop, value, func() {
		fe.rock.generating = true
		fe.rock.generate()
	})
	return nil
}

func (fe *BoulderEditor) Tabs(mode Mode) []string {
	switch mode {
	case ModeGeometry:
		return []string{
			"editing/seed",
			"editing/noise_scale",
			"editing/noise_strength",
			"editing/scrape_count",
			"editing/scrape_min_dist",
			"editing/scrape_strength",
			"editing/scrape_radius",
			"editing/width",
			"editing/height",
			"editing/depth",
		}
	case ModeMaterial:
		return []string{
			"mineral",
		}
	default:
		return nil
	}
}

func (fe *BoulderEditor) SelectDesign(mode Mode, design string) {
	if mode != ModeMaterial {
		return
	}
	// Mineral has only one material slot, so the slider name is fixed
	// — unlike foliage which keys on leaflet/timbers.
	if err := fe.client.space.Sculpt(musical.Sculpt{
		Author: fe.client.id,
		Editor: "mineral",
		Slider: "mineral",
		Design: fe.client.MusicalDesign(design),
		Commit: true,
	}); err != nil {
		Engine.Raise(err)
	}
}

func (fe *BoulderEditor) SliderConfig(mode Mode, editing string) (init, from, upto, step float64) {
	_, prop, _ := strings.Cut(editing, "/")
	if init, from, upto, step, ok := reflectSliderConfig(reflect.TypeFor[Rock](), prop); ok {
		return init, from, upto, step
	}
	return 1, 0, 5, 0.01
}

func (fe *BoulderEditor) SliderHandle(mode Mode, editing string, value float64, commit bool) {
	if !commit && time.Since(fe.last_slider_sculpt) < time.Second/10 {
		return
	}
	fe.last_slider_sculpt = time.Now()
	if err := fe.client.space.Sculpt(musical.Sculpt{
		Author: fe.client.id,
		Editor: "mineral",
		Slider: editing,
		Amount: Float.X(value),
		Commit: commit,
	}); err != nil {
		Engine.Raise(err)
	}
}
