package main

import (
	"log/slog"

	"graphics.gd/classdb"
	"graphics.gd/classdb/ProjectSettings"
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
	classdb.Register[internal.Renderer]()
	classdb.Register[internal.GridFlowContainer]()
	classdb.Register[internal.ThemeSelector]()
	classdb.Register[internal.CloudControl]()
	startup.LoadingScene()
	SceneTree.Add(new(internal.Client))
	ProjectSettings.LoadResourcePack("res://library.pck", 0)
	startup.Scene()
}
