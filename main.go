package main

import (
	"graphics.gd/classdb"
	"graphics.gd/classdb/SceneTree"
	"graphics.gd/startup"
	"the.quetzal.community/aviary/internal"
)

func main() {
	classdb.Register[internal.Tree]()
	classdb.Register[internal.Rock]()
	classdb.Register[internal.TerrainTile]()
	classdb.Register[internal.Client]()
	classdb.Register[internal.UI]()
	classdb.Register[internal.PreviewRenderer]()
	classdb.Register[internal.Renderer]()
	classdb.Register[internal.EditorPlugin]()
	classdb.Register[internal.ModelLoader]()
	classdb.Register[internal.GridFlowContainer]()
	startup.LoadingScene()
	SceneTree.Add(new(internal.Client))
	startup.Scene()
}
