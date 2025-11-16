package main

import (
	"log/slog"

	"graphics.gd/classdb"
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/PackedScene"
	"graphics.gd/classdb/ProjectSettings"
	"graphics.gd/classdb/Resource"
	"graphics.gd/classdb/ResourceLoader"
	"graphics.gd/classdb/SceneTree"
	"graphics.gd/startup"
	"the.quetzal.community/aviary/internal"

	"github.com/quaadgras/velopack-go/velopack"
)

func init() {
	velopack.Run(velopack.App{
		AutoApplyOnStartup: true,
		Logger: func(level, message string) {
			switch level {
			case "error":
				slog.Error(message)
			case "trace":
				slog.Debug(message)
			case "info":
				slog.Info(message)
			}
		},
	})
}

func main() {
	classdb.Register[internal.Tree]()
	classdb.Register[internal.Rock]()
	classdb.Register[internal.TerrainTile]()
	classdb.Register[internal.Client]()
	classdb.Register[internal.UI]()
	classdb.Register[internal.PreviewRenderer]()
	classdb.Register[internal.TerrainRenderer]()
	classdb.Register[internal.GridFlowContainer]()
	classdb.Register[internal.ThemeSelector]()
	classdb.Register[internal.CloudControl]()
	classdb.Register[internal.LibraryDownloader]()
	classdb.Register[internal.FlightPlanner]()
	classdb.Register[internal.AnimationSaving]()
	classdb.Register[internal.ActionRenderer]()
	classdb.Register[internal.MaterialSharingMeshInstance3D]()
	classdb.Register[internal.CommunityResourceLoader](internal.NewCommunityResourceLoader)
	if !ProjectSettings.LoadResourcePack("user://preview.pck", 0) {
		startup.LoadingScene()
		SceneTree.Add(Resource.Load[PackedScene.Is[Node.Instance]]("res://ui/library_downloader.tscn").Instantiate())
		startup.Scene()
		return
	}
	startup.LoadingScene()
	ResourceLoader.AddResourceFormatLoader(internal.NewCommunityResourceLoader().AsResourceFormatLoader(), true)
	SceneTree.Add(internal.NewClient())
	startup.Scene()
	close(internal.ShuttingDown)
	internal.PendingSaves.Wait()
}
