//go:build !musl

package internal

import (
	"context"
	"fmt"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/quaadgras/velopack-go/velopack"
	"graphics.gd/classdb/DirAccess"
	"graphics.gd/classdb/Engine"
	"graphics.gd/classdb/FileAccess"
	"graphics.gd/classdb/SceneTree"
	"graphics.gd/variant/Float"
	"the.quetzal.community/aviary/internal/ice/signalling"
	"the.quetzal.community/aviary/internal/musical"
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
	user, err := ui.client.signalling.LookupUser(context.Background())
	if err != nil {
		Engine.Raise(err)
		ui.on_process <- func(cc *CloudControl) {
			if err.Error() == "Unauthorized" {
				UserState.Aviary = signalling.User{}
			}
			cc.set_online_status_indicator(false)
		}
		return
	}
	ui.on_process <- func(cc *CloudControl) {
		if UserState.Aviary.ID != user.ID {
			fresh := NewClientLoading(musical.WorkID(ui.client.record))
			for _, child := range SceneTree.Get(ui.AsNode()).Root().AsNode().GetChildren() {
				child.QueueFree()
			}
			SceneTree.Add(fresh)
		}
		UserState.Aviary = user
		cc.client.saveUserState()
		cc.set_online_status_indicator(true)
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

		ui.on_process <- func(cc *CloudControl) {
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
			if resp.StatusCode != http.StatusOK {
				Engine.Raise(fmt.Errorf("failed to fetch library.pck: %s", resp.Status))
				return
			}
			remote_time, err := time.Parse(time.RFC1123, resp.Header.Get("Last-Modified"))
			if err != nil {
				Engine.Raise(err)
				return
			}
			if remote_time.Unix() > int64(FileAccess.GetModifiedTime("res://preview.pck")) && remote_time.Unix() > int64(FileAccess.GetModifiedTime("user://preview.pck")) {
				if FileAccess.FileExists("res://preview.pck") {
					if err := DirAccess.RenameAbsolute("res://preview.pck", "res://preview.pck.backup"); err != nil {
						Engine.Raise(err)
						return
					}
				}
				if FileAccess.FileExists("user://preview.pck") {
					if err := DirAccess.RenameAbsolute("user://preview.pck", "user://preview.pck.backup"); err != nil {
						Engine.Raise(err)
						return
					}
				}
			}
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
