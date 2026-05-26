//go:build !musl && !android

package internal

import (
	"fmt"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/quaadgras/velopack-go/velopack"
	"graphics.gd/classdb/DirAccess"
	"graphics.gd/classdb/Engine"
	"graphics.gd/classdb/FileAccess"
	"graphics.gd/variant/Float"
)

func (ui *CloudControl) automaticallyUpdate() {
	defer func() {
		setting_up.Store(false)
		if r := recover(); r != nil {
			Engine.Raise(fmt.Errorf("panic during automatic update: %v", r))
			debug.PrintStack()
		}
	}()
	manager, err := velopack.NewUpdateManager("https://vpk.quetzal.community")
	if err != nil {
		Engine.Raise(err)
	}
	if manager != nil {
		version = "v" + manager.CurrentlyInstalledVersion()
		ui.on_process <- func(cc *CloudControl) {
			ui.JoinCode.Versioning.Version.SetText(version)
		}
	}
	user, ok := ui.loginUpdate()
	if !ok {
		return
	}
	// Always check whether preview.pck has been re-uploaded on R2 —
	// this is independent of velopack binary releases (the .pck can
	// be refreshed on its own), so gating it behind UpdateAvailable
	// would leave the running app pinned to a stale library forever
	// whenever no velopack release is pending.
	ui.on_process <- func(cc *CloudControl) {
		checkPreviewPckFreshness()
	}
	if manager == nil || time.Now().After(user.TogetherUntil) {
		return
	}
	latest, update, err := manager.CheckForUpdates()
	if err != nil {
		Engine.Raise(err)
		return
	}
	if update == velopack.UpdateAvailable {
		ui.on_process <- func(cc *CloudControl) { cc.set_update_available(nil, true) }
		if err := manager.DownloadUpdates(latest, func(progress uint) {
			ui.on_process <- func(cc *CloudControl) {
				cc.UpdateProgress.AsRange().SetValue(Float.X(progress))
			}
		}); err != nil {
			Engine.Raise(err)
			return
		}
		restart := func() {
			if err := manager.ApplyUpdatesAndRestart(latest); err != nil {
				Engine.Raise(err)
				return
			}
		}
		ui.on_process <- func(cc *CloudControl) { cc.set_update_available(restart, false) }
	}
}

// checkPreviewPckFreshness HEADs preview.pck on R2 and, if its
// Last-Modified is newer than the local copy at user://preview.pck,
// renames the stale .pck out of the way so the next startup falls
// into the library_downloader flow (main.go:52) and pulls the fresh
// file. Caller is responsible for running on the Godot main thread
// (FileAccess / DirAccess are not goroutine-safe).
func checkPreviewPckFreshness() {
	req, err := http.NewRequest("HEAD", "https://vpk.quetzal.community/preview.pck", nil)
	if err != nil {
		Engine.Raise(err)
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		Engine.Raise(err)
		return
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		Engine.Raise(fmt.Errorf("failed to HEAD preview.pck: %s", resp.Status))
		return
	}
	remote_time, err := time.Parse(time.RFC1123, resp.Header.Get("Last-Modified"))
	if err != nil {
		Engine.Raise(err)
		return
	}
	// Only user://preview.pck is the active copy after first
	// download; res://preview.pck either doesn't exist or has the
	// build's bundled mtime which we don't want to compare against
	// (it would pin freshness to the binary's age, not the file's).
	localMTime := int64(FileAccess.GetModifiedTime("user://preview.pck"))
	if remote_time.Unix() <= localMTime {
		return
	}
	if FileAccess.FileExists("user://preview.pck") {
		if err := DirAccess.RenameAbsolute("user://preview.pck", "user://preview.pck.backup"); err != nil {
			Engine.Raise(err)
			return
		}
	}
	if FileAccess.FileExists("res://preview.pck") {
		if err := DirAccess.RenameAbsolute("res://preview.pck", "res://preview.pck.backup"); err != nil {
			Engine.Raise(err)
			return
		}
	}
}
