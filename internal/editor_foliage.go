package internal

import (
	"graphics.gd/classdb/MeshInstance3D"
	"graphics.gd/classdb/Node3D"
)

type FoliageEditor struct {
	Node3D.Extension[FoliageEditor]

	Mesh MeshInstance3D.Instance
	tree *Tree
}

func (fe *FoliageEditor) Ready() {
	fe.tree = NewTree()
	fe.Mesh.SetMesh(fe.tree.AsMesh())
}

func (fe *FoliageEditor) Tabs() []string {
	return []string{"Planting", "Settings"}
}
