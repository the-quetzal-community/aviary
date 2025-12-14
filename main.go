package main

import (
	"log/slog"
	"runtime"

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
	classdb.Register[internal.Tree](internal.NewTree)
	classdb.Register[internal.Rock](internal.NewRock)
	classdb.Register[internal.TerrainTile]()
	classdb.Register[internal.Client]()
	classdb.Register[internal.UI]()
	classdb.Register[internal.Triangle](internal.NewTriangle)

	classdb.Register[internal.SceneryEditor]()
	classdb.Register[internal.TerrainEditor]()
	classdb.Register[internal.FoliageEditor]()
	classdb.Register[internal.BoulderEditor]()
	classdb.Register[internal.VehicleEditor]()
	classdb.Register[internal.CitizenEditor]()
	classdb.Register[internal.CritterEditor]()
	classdb.Register[internal.ShelterEditor]()

	classdb.Register[internal.GridFlowContainer]()
	classdb.Register[internal.ViewSelector]()
	classdb.Register[internal.CloudControl]()
	classdb.Register[internal.LibraryDownloader]()
	classdb.Register[internal.FlightPlanner]()
	classdb.Register[internal.AnimationSaving]()
	classdb.Register[internal.ActionRenderer]()
	classdb.Register[internal.EditorIndicator]()
	classdb.Register[internal.MaterialSharingMeshInstance3D]()
	classdb.Register[internal.MaterialSharingDecal]()
	classdb.Register[internal.DesignExplorer]()
	classdb.Register[internal.CommunityResourceLoader](internal.NewCommunityResourceLoader)
	startup.LoadingScene()
	if runtime.GOOS != "js" && !ProjectSettings.LoadResourcePack("user://preview.pck", 0) {
		SceneTree.Add(Resource.Load[PackedScene.Is[Node.Instance]]("res://ui/library_downloader.tscn").Instantiate())
		startup.Scene()
		return
	}
	ResourceLoader.AddResourceFormatLoader(internal.NewCommunityResourceLoader().AsResourceFormatLoader(), true)
	SceneTree.Add(internal.NewClient())
	startup.Scene()
	close(internal.ShuttingDown)
	internal.PendingSaves.Wait()
}
