package internal

import (
	"context"
	"fmt"
	"net/http"
	"runtime/debug"
	"sync/atomic"
	"time"

	"github.com/quaadgras/velopack-go/velopack"
	"graphics.gd/classdb/Control"
	"graphics.gd/classdb/DirAccess"
	"graphics.gd/classdb/Engine"
	"graphics.gd/classdb/FileAccess"
	"graphics.gd/classdb/GradientTexture2D"
	"graphics.gd/classdb/GridContainer"
	"graphics.gd/classdb/HBoxContainer"
	"graphics.gd/classdb/Label"
	"graphics.gd/classdb/Material"
	"graphics.gd/classdb/OS"
	"graphics.gd/classdb/Panel"
	"graphics.gd/classdb/ProgressBar"
	"graphics.gd/classdb/PropertyTweener"
	"graphics.gd/classdb/Resource"
	"graphics.gd/classdb/RichTextLabel"
	"graphics.gd/classdb/SceneTree"
	"graphics.gd/classdb/Shader"
	"graphics.gd/classdb/ShaderMaterial"
	"graphics.gd/classdb/TextEdit"
	"graphics.gd/classdb/TextureButton"
	"graphics.gd/classdb/TextureRect"
	"graphics.gd/classdb/Tween"
	"graphics.gd/classdb/Viewport"
	"graphics.gd/classdb/Window"
	"graphics.gd/variant/Color"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Signal"
	"the.quetzal.community/aviary/internal/networking"
)

type CloudControl struct {
	Control.Extension[CloudControl]

	JoinCode struct {
		Panel.Instance

		Label       Label.Instance
		ShareButton TextureButton.Instance
		Versioning  struct {
			HBoxContainer.Instance

			Version RichTextLabel.Instance
			Restart TextureButton.Instance
			Updates TextureRect.Instance
		}
	}
	HBoxContainer struct {
		HBoxContainer.Instance

		Cloud struct {
			TextureButton.Instance

			OnlineIndicator TextureRect.Instance
		}
	}
	Keypad struct {
		Panel.Instance

		TextEdit TextEdit.Instance

		Keys GridContainer.Instance
	}

	UpdateProgress ProgressBar.Instance

	sharing    bool
	client     *Client
	on_process chan func(*CloudControl)
}

var setting_up atomic.Bool

func (ui *CloudControl) Setup() {
	ui.on_process = make(chan func(*CloudControl), 10)
	if !setting_up.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer func() {
			setting_up.Store(false)
			if r := recover(); r != nil {
				Engine.Raise(fmt.Errorf("panic during automatic update: %v", r))
				debug.PrintStack()
			}
		}()
		user, err := ui.client.signalling.LookupUser(context.Background())
		if err != nil {
			Engine.Raise(err)
			ui.on_process <- func(cc *CloudControl) { cc.set_online_status_indicator(false) }
			return
		}
		ui.on_process <- func(cc *CloudControl) {
			ui.client.user = user
			cc.set_online_status_indicator(true)
		}
		if time.Now().After(user.TogetherUntil) {
			return
		}

		manager, err := velopack.NewUpdateManager("https://vpk.quetzal.community")
		if err != nil {
			Engine.Raise(err)
			return
		}
		version := manager.CurrentlyInstalledVersion()
		ui.on_process <- func(cc *CloudControl) {
			ui.JoinCode.Versioning.Version.SetText("v" + version)
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
				req, err := http.NewRequest("HEAD", "https://vpk.quetzal.community/library.pck", nil)
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
				if remote_time.Unix() > int64(FileAccess.GetModifiedTime("res://library.pck")) && remote_time.Unix() > int64(FileAccess.GetModifiedTime("user://library.pck")) {
					if FileAccess.FileExists("res://library.pck") {
						if err := DirAccess.RenameAbsolute("res://library.pck", "res://library.pck.backup"); err != nil {
							Engine.Raise(err)
							return
						}
					}
					if FileAccess.FileExists("user://library.pck") {
						if err := DirAccess.RenameAbsolute("user://library.pck", "user://library.pck.backup"); err != nil {
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
	}()
}

func (ui *CloudControl) Ready() {
	ui.JoinCode.ShareButton.AsBaseButton().OnPressed(func() {
		if time.Now().After(ui.client.user.TogetherUntil) {
			OS.ShellOpen("https://the.quetzal.community/aviary/connection?id=" + UserState.Device)
			Object.To[Window.Instance](Viewport.Get(ui.AsNode())).OnFocusEntered(func() {
				ui.Setup()
			}, Signal.OneShot)
			return
		}
		if !ui.sharing {
			ui.sharing = true
			var spinner = Resource.Load[Shader.Instance]("res://shader/spinner.gdshader")
			var material = ShaderMaterial.New()
			material.SetShader(spinner)
			ui.JoinCode.ShareButton.AsCanvasItem().SetMaterial(material.AsMaterial())
			go func() {
				code, err := ui.client.apiHost()
				if err != nil {
					Engine.Raise(err)
					ui.on_process <- func(cc *CloudControl) { cc.set_join_code("") }
					return
				}
				ui.on_process <- func(cc *CloudControl) { cc.set_join_code(code) }
				time.Sleep(5 * time.Minute)
				ui.on_process <- func(cc *CloudControl) { cc.set_join_code("") }
			}()
		}
	})

}

func (ui *CloudControl) set_update_available(restart func(), available bool) {
	if available {
		ui.UpdateProgress.AsCanvasItem().SetVisible(true)
		ui.JoinCode.Versioning.Updates.AsCanvasItem().SetVisible(true)
	} else {
		ui.JoinCode.Versioning.Version.SetText("[s]" + ui.JoinCode.Versioning.Version.Text() + "[/s]")
		ui.JoinCode.Versioning.Updates.AsCanvasItem().SetVisible(false)
		ui.JoinCode.Versioning.Restart.AsCanvasItem().SetVisible(true)
		ui.JoinCode.Versioning.Restart.AsBaseButton().OnPressed(restart)
		ui.UpdateProgress.AsCanvasItem().SetVisible(false)
	}
}

func (ui *CloudControl) set_online_status_indicator(online bool) {
	var col = Color.X11.Green
	if !online {
		col = Color.X11.Red
	}
	tex := ui.HBoxContainer.Cloud.OnlineIndicator.Texture()
	grad := Object.To[GradientTexture2D.Instance](tex).Gradient()
	cols := grad.Colors()
	cols[0] = col
	grad.SetColors(cols)
}

func (ui *CloudControl) set_join_code(code networking.Code) {
	ui.JoinCode.ShareButton.AsCanvasItem().SetMaterial(Material.Nil)
	size := ui.JoinCode.AsControl().Size()
	if code != "" {
		size.X = 184
	} else {
		size.X = 54
	}
	PropertyTweener.Make(SceneTree.Get(ui.AsNode()).CreateTween(), ui.JoinCode.AsControl().AsObject(), "size", size, 0.2).SetEase(Tween.EaseOut)
	ui.JoinCode.Label.SetText(string(code))
	ui.sharing = false
}

func (ui *CloudControl) Process(dt Float.X) {
	for {
		select {
		case fn := <-ui.on_process:
			if fn != nil {
				fn(ui)
			}
		default:
			return
		}
	}
}
