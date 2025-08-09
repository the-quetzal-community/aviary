package main

import (
	"log"
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
		Logger: func(level, msg string) {
			switch level {
			case "info":
				slog.Info(msg)
			case "warn":
				slog.Warn(msg)
			case "error":
				slog.Error(msg)
			case "trace":
				slog.Debug(msg)
			default:
				log.Print(level, ": ", msg)
			}
		},
	})
}

func main() {
	go velopack.DownloadUpdatesInTheBackground("https://vpk.quetzal.community/aviary")
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
	ProjectSettings.LoadResourcePack("res://library.pck", 0)
	startup.Scene()
}
