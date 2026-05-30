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
	"graphics.gd/classdb/Material"
	"graphics.gd/classdb/Mesh"
	"graphics.gd/classdb/MeshInstance3D"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/Shader"
	"graphics.gd/classdb/ShaderMaterial"
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

	leafletMaterial ShaderMaterial.Instance
	timbersMaterial BaseMaterial3D.Instance

	client *Client

	last_slider_sculpt time.Time
}

func (*FoliageEditor) Views() []string          { return nil }
func (*FoliageEditor) SwitchToView(view string) {}

func (fe *FoliageEditor) Name() string { return "foliage" }

// ExportSubtree implements the Exporter interface (see export.go).
// We duplicate the procedural tree's MeshInstance3D onto a fresh root
// and PROMOTE its surface materials from the Mesh resource onto the
// MeshInstance3D itself as surface overrides. Godot's glTF exporter
// walks `MeshInstance3D.get_active_material()` and, while that does
// fall through to `Mesh.surface_get_material()` in principle, the
// path is unreliable for procedural ArrayMeshes whose surfaces are
// rebuilt at runtime — the materials end up missing from the .glb.
// Hoisting them to the instance makes them durably exported.
//
// The live leaflet material is a ShaderMaterial (foliage_wind.gdshader)
// for editor preview animation. For export we substitute a plain
// StandardMaterial3D carrying the current leaf texture so that glTF
// exporters embed the albedo (and alpha scissor) reliably for the leaves.
func (fe *FoliageEditor) ExportSubtree() Node3D.Instance {
	root := Node3D.New()
	root.AsNode().SetName("foliage")
	if fe.Mesh == MeshInstance3D.Nil {
		return root
	}
	dup, ok := Object.As[MeshInstance3D.Instance](fe.Mesh.AsNode().Duplicate())
	if !ok {
		return root
	}
	mesh := dup.Mesh()
	if mesh != Mesh.Nil {
		for i := range mesh.GetSurfaceCount() {
			var matToUse Material.Instance
			if i == 1 {
				// Bake a normal material for the leaves so the texture
				// is properly exported (the wind shader is preview-only).
				if texAny := fe.leafletMaterial.GetShaderParameter("albedo_texture"); texAny != nil {
					if tex, ok := texAny.(Texture2D.Instance); ok && tex != Texture2D.Nil {
						matToUse = StandardMaterial3D.New().
							AsBaseMaterial3D().SetAlbedoTexture(tex).
							AsBaseMaterial3D().SetTransparency(BaseMaterial3D.TransparencyAlphaScissor).
							AsMaterial()
					}
				}
			}
			if matToUse == Material.Nil {
				matToUse = mesh.SurfaceGetMaterial(i)
			}
			if matToUse != Material.Nil {
				dup.SetSurfaceOverrideMaterial(i, matToUse)
			}
		}
	}
	root.AsNode().AddChild(dup.AsNode())
	return root
}
func (fe *FoliageEditor) EnableEditor() {
	fe.client.SetGizmos(nil)
}
func (fe *FoliageEditor) ChangeEditor() {}

func (fe *FoliageEditor) Ready() {
	fe.tree = Object.Leak(NewTree())
	fe.Mesh.SetMesh(fe.tree.AsMesh())

	leafletShader := LoadSync[Shader.Instance]("res://shader/foliage_wind.gdshader")
	fe.leafletMaterial = ShaderMaterial.New().
		SetShader(leafletShader).
		SetShaderParameter("albedo_texture", LoadSync[Texture2D.Instance]("res://default/leaflet.png"))

	fe.timbersMaterial = StandardMaterial3D.New().
		AsBaseMaterial3D().SetAlbedoTexture(LoadSync[Texture2D.Instance]("res://default/timbers.png"))

	fe.applyMaterials()
}

func (fe *FoliageEditor) ExitTree() {
	Object.Free(fe.tree)
}

// applyMaterials pushes the current materials both onto the ArrayMesh
// resource (for the Tree's save/restore logic across recalculate and for
// ExportSubtree) and as surface overrides on the MeshInstance3D.
//
// Procedural ArrayMeshes that repeatedly do ClearSurfaces + AddSurfaceFromArrays
// lose reliable material binding unless you also set overrides on the
// MeshInstance3D (and use the SetMesh(nil)/SetMesh dance to resize the
// override array). Without this the leaves often fall back to Godot's
// default grey material → flat grey squares with no texture or wind.
func (fe *FoliageEditor) applyMaterials() {
	if fe.Mesh == MeshInstance3D.Nil {
		return
	}

	// 1. Keep the materials on the ArrayMesh resource itself.
	m := fe.Mesh.Mesh()
	if m != Mesh.Nil {
		if fe.timbersMaterial != (BaseMaterial3D.Instance{}) {
			m.SurfaceSetMaterial(0, fe.timbersMaterial.AsMaterial())
		}
		if fe.leafletMaterial != (ShaderMaterial.Instance{}) {
			m.SurfaceSetMaterial(1, fe.leafletMaterial.AsMaterial())
		}
	}

	// 2. Force the MeshInstance3D to resize its surface_override_materials
	//    array to match the current surface count, then install the overrides.
	//    This is the key step that makes the materials actually render.
	mi := fe.Mesh
	mm := mi.Mesh()
	mi.SetMesh(Mesh.Nil)
	mi.SetMesh(mm)

	if mi.GetSurfaceOverrideMaterialCount() > 0 {
		mi.SetSurfaceOverrideMaterial(0, fe.timbersMaterial.AsMaterial())
	}
	if mi.GetSurfaceOverrideMaterialCount() > 1 {
		mi.SetSurfaceOverrideMaterial(1, fe.leafletMaterial.AsMaterial())
	}
}

func (fe *FoliageEditor) Sculpt(brush musical.Sculpt) error {
	switch brush.Slider {
	case "leaflet", "timbers":
		texture := fe.client.resolveMaterialTexture(brush.Design)
		if texture == Texture2D.Nil {
			return nil
		}
		switch brush.Slider {
		case "leaflet":
			fe.leafletMaterial.SetShaderParameter("albedo_texture", texture)
		case "timbers":
			fe.timbersMaterial.SetAlbedoTexture(texture)
		}
		return nil
	}
	_, prop, _ := strings.Cut(brush.Slider, "/")
	applyReflectSlider(fe.tree, reflect.TypeFor[Tree](), prop, float64(brush.Amount), func() {
		fe.tree.recalculating = true
		fe.tree.recalculate()
		fe.applyMaterials() // re-apply overrides after the ArrayMesh surfaces were rebuilt
	})
	return nil
}

// resolveMaterialTexture turns a material Design into a usable
// Texture2D. For legacy .png paths it just loads the file (or returns
// the cached import). For .region sidecars it reads the JSON, loads
// the referenced shared material from <author>/texture/<hash>.tres,
// and wraps its albedo in an AtlasTexture with the recorded region.
//
// Lives on Client because it consults the shared design caches
// (design_to_string, textures) — every editor that supports material
// selection (foliage, mineral, …) calls it the same way.
func (client *Client) resolveMaterialTexture(design musical.Design) Texture2D.Instance {
	uri, ok := client.design_to_string[design]
	if !ok {
		return Texture2D.Nil
	}
	if strings.HasSuffix(uri, ".region") {
		return loadRegionTexture(uri)
	}
	if tex, ok := client.textures[design].Instance(); ok {
		return tex
	}
	return LoadSync[Texture2D.Instance](uri)
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
	mat := LoadSync[BaseMaterial3D.Instance](sidecar.Material)
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
