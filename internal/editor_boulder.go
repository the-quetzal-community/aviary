package internal

import (
	"reflect"
	"strconv"
	"strings"
	"time"

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

	standard := StandardMaterial3D.New()
	standard.AsBaseMaterial3D().
		SetAlbedoTexture(Resource.Load[Texture2D.Instance]("res://default/mineral.jpg")).
		SetUv1Triplanar(true)
	fe.Mesh.Mesh().SurfaceSetMaterial(0, standard.AsMaterial())
}

func (fe *BoulderEditor) ExitTree() {
	Object.Free(fe.rock)
}

func (fe *BoulderEditor) Sculpt(brush musical.Sculpt) error {
	editing := brush.Slider
	value := float64(brush.Amount)
	switch editing {
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
	rtype := reflect.TypeFor[Rock]()
	for i := range rtype.NumField() {
		field := rtype.Field(i)
		if field.Tag.Get("gd") == prop {
			switch field.Type.Kind() {
			case reflect.Int:
				reflect.ValueOf(fe.rock).Elem().Field(i).SetInt(int64(value))
			case reflect.Float32, reflect.Float64:
				reflect.ValueOf(fe.rock).Elem().Field(i).SetFloat(value)
			}
			fe.rock.generating = true
			fe.rock.generate()
			return nil
		}
	}
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

}

func (fe *BoulderEditor) SliderConfig(mode Mode, editing string) (init, from, upto, step float64) {
	_, prop, _ := strings.Cut(editing, "/")
	rtype := reflect.TypeFor[Rock]()
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
