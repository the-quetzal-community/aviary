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

func (fe *FoliageEditor) Tabs() []string {
	return []string{"Planting", "Settings"}
}
