package internal

import (
	"graphics.gd/classdb/BaseMaterial3D"
	"graphics.gd/classdb/MeshInstance3D"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/Resource"
	"graphics.gd/classdb/StandardMaterial3D"
	"graphics.gd/classdb/Texture2D"
)

type FoliageEditor struct {
	Node3D.Extension[FoliageEditor]

	Mesh MeshInstance3D.Instance
	tree *Tree
}

func (fe *FoliageEditor) Ready() {
	fe.tree = NewTree()
	fe.Mesh.SetMesh(fe.tree.AsMesh())

	leaflet := StandardMaterial3D.New()
	leaflet.AsBaseMaterial3D().SetAlbedoTexture(Resource.Load[Texture2D.Instance]("res://default/leaflet.png"))
	leaflet.AsBaseMaterial3D().SetTransparency(BaseMaterial3D.TransparencyAlphaScissor)
	fe.Mesh.Mesh().SurfaceSetMaterial(1, leaflet.AsMaterial())

	timbers := StandardMaterial3D.New()
	timbers.AsBaseMaterial3D().SetAlbedoTexture(Resource.Load[Texture2D.Instance]("res://default/timbers.png"))
	fe.Mesh.Mesh().SurfaceSetMaterial(0, timbers.AsMaterial())
}

func (fe *FoliageEditor) Tabs(mode Mode) []string {
	switch mode {
	case ModeGeometry:
		return []string{
			"editing/seed",
			"editing/tree_levels",
			"editing/twig_scale",
			"editing/initial_branch_length",
			"editing/length_falloff_factor",
			"editing/length_falloff_power",
			"editing/twig_clump_min",
			"editing/twig_clump_max",
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
func (fe *FoliageEditor) AdjustSlider(mode Mode, editing string, value float64, commit bool) {

}
