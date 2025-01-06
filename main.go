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
	classdb.Register[internal.Vulture]()
	classdb.Register[internal.World]()
	classdb.Register[internal.UI]()
	classdb.Register[internal.PreviewRenderer]()
	classdb.Register[internal.Renderer]()
	startup.Wait()
	SceneTree.Add(new(internal.World))
}
