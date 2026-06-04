package main

import (
	"runtime"

	"graphics.gd/classdb"
	"graphics.gd/classdb/Engine"
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/OS"
	"graphics.gd/classdb/PackedScene"
	"graphics.gd/classdb/ProjectSettings"
	"graphics.gd/classdb/ResourceLoader"
	"graphics.gd/classdb/SceneTree"
	"graphics.gd/startup"
	"the.quetzal.community/aviary/internal"
)

func main() {
	classdb.Register[internal.Tree](internal.NewTree)
	classdb.Register[internal.Rock](internal.NewRock)
	classdb.Register[internal.TerrainTile]()
	classdb.Register[internal.TerrainTileArrow]()
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
	classdb.Register[internal.CoasterEditor]()

	classdb.Register[internal.GridFlowContainer]()
	classdb.Register[internal.ViewSelector]()
	classdb.Register[internal.CloudControl]()
	classdb.Register[internal.LibraryDownloader]()
	classdb.Register[internal.SceneLoader]()
	classdb.Register[internal.FlightPlanner]()
	classdb.Register[internal.AnimationSaving]()
	classdb.Register[internal.ActionRenderer]()
	classdb.Register[internal.EditorIndicator]()
	classdb.Register[internal.MaterialSharingMeshInstance3D]()
	classdb.Register[internal.MaterialSharingDecal]()
	classdb.Register[internal.DesignExplorer]()
	classdb.Register[internal.CommunityResourceLoader](internal.NewCommunityResourceLoader)
	internal.ProfMark("main: classes registered")
	startup.LoadingScene()
	internal.UserDataDir = OS.GetUserDataDir()
	if Engine.IsEditorHint() {
		startup.Scene()
		return
	}
	community_resource_loader := internal.NewCommunityResourceLoader().AsResourceFormatLoader()
	ResourceLoader.AddResourceFormatLoader(community_resource_loader, true)
	defer ResourceLoader.RemoveResourceFormatLoader(community_resource_loader)
	// Start the dedicated resource-loading thread before any scene is
	// added, so every load triggered by a node's Ready() funnels through
	// it (see resource_thread.go) and the community loader's maps are only
	// ever touched by that one thread.
	internal.StartResourceThread()
	internal.ProfMark("main: resource thread started")
	if runtime.GOOS != "js" && !ProjectSettings.LoadResourcePack("user://preview.pck", 0) {
		SceneTree.Add(internal.LoadSync[PackedScene.Is[Node.Instance]]("res://ui/library_downloader.tscn").Instantiate())
	} else {
		internal.ProfMark("main: preview.pck mounted")
		SceneTree.Add(internal.NewClient())
	}
	internal.ProfMark("main: client added, starting engine scene")
	startup.Scene()
	// The main loop has ended; release session-lifetime caches (shared materials,
	// etc.) while the engine is still up and the scene not yet torn down, so they
	// don't report as leaks at exit. Free only decrements refcounts, so anything
	// still bound to a live node is freed for real when that node is finalized.
	internal.RunShutdownCleanups()
	close(internal.ShuttingDown)
	internal.PendingSaves.Wait()
}
