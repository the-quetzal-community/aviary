package internal

import (
	"fmt"
	"strings"
	"time"

	"github.com/quaadgras/velopack-go/velopack"
	"graphics.gd/classdb/BaseButton"
	"graphics.gd/classdb/Control"
	"graphics.gd/classdb/Engine"
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
	"graphics.gd/variant/Color"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Object"
	"the.quetzal.community/aviary/internal/networking"
)

type CloudControl struct {
	Control.Extension[CloudControl]

	JoinCode struct {
		Panel.Instance

		Label       Label.Instance
		ShareButton TextureButton.Instance
		Version     RichTextLabel.Instance
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

func (ui *CloudControl) Setup() {
	go func() {
		if err := ui.client.goOnline(); err != nil {
			fmt.Println("Error going online:", err)
			ui.on_process <- func(cc *CloudControl) { cc.set_online_status_indicator(false) }
			return
		}
		ui.on_process <- func(cc *CloudControl) { cc.set_online_status_indicator(true) }

		manager, err := velopack.NewUpdateManager("https://vpk.quetzal.community/aviary")
		if err != nil {
			Engine.Raise(err)
			return
		}
		latest, update, err := manager.CheckForUpdates()
		if err != nil {
			Engine.Raise(err)
			return
		}
		if update == velopack.UpdateAvailable {
			ui.on_process <- func(cc *CloudControl) { cc.set_update_available(true) }
			if err := manager.DownloadUpdates(latest, func(progress uint) {
				ui.on_process <- func(cc *CloudControl) {
					cc.UpdateProgress.AsRange().SetValue(Float.X(progress))
				}
			}); err != nil {
				Engine.Raise(err)
				return
			}
			ui.on_process <- func(cc *CloudControl) { cc.set_update_available(false) }
		}
	}()
}

func (ui *CloudControl) Ready() {
	ui.on_process = make(chan func(*CloudControl), 10)
	up, err := velopack.NewUpdateManager("https://vpk.quetzal.community/aviary")
	if err == nil {
		ui.JoinCode.Version.SetText("v" + up.CurrentlyInstalledVersion())
	}
	ui.JoinCode.ShareButton.AsBaseButton().OnPressed(func() {
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
	ui.HBoxContainer.Cloud.AsBaseButton().OnPressed(func() {
		if !ui.client.isOnline() {
			OS.ShellOpen("https://the.quetzal.community/aviary/account?connection=" + OneTimeUseCode)
		} else {
			ui.Keypad.AsCanvasItem().SetVisible(!ui.Keypad.AsCanvasItem().Visible())
		}
	})
	ui.Keypad.TextEdit.OnTextChanged(func() {
		text := ui.Keypad.TextEdit.Text()
		safe := ""
		for _, char := range text {
			if strings.ContainsRune("0123456789", char) {
				safe += string(char)
			}
		}
		if len(safe) > 6 {
			safe = safe[:6]
		}
		if text != safe {
			ui.Keypad.TextEdit.SetText(safe)
		}
	})
	keys := ui.Keypad.Keys.AsNode().GetChildren()
	for _, key := range keys {
		name := key.Name()
		switch name {
		case "X":
			Object.To[BaseButton.Instance](key).OnPressed(func() {
				text := ui.Keypad.TextEdit.Text()
				if len(text) > 0 {
					text = text[:len(text)-1]
				}
				ui.Keypad.TextEdit.SetText(text)
			})
		case ">":
			Object.To[BaseButton.Instance](key).OnPressed(func() {
				go ui.client.apiJoin(networking.Code(ui.Keypad.TextEdit.Text()))
				ui.Keypad.AsCanvasItem().SetVisible(false)
			})
		default:
			Object.To[BaseButton.Instance](key).OnPressed(func() {
				text := ui.Keypad.TextEdit.Text()
				text += name
				ui.Keypad.TextEdit.SetText(text)
			})
		}
	}
}

func (ui *CloudControl) set_update_available(available bool) {
	if available {
		ui.JoinCode.Version.SetText(ui.JoinCode.Version.Text() + "⬆️")
		ui.UpdateProgress.AsCanvasItem().SetVisible(true)
	} else {
		ui.JoinCode.Version.SetText("[s]" + strings.TrimSuffix(ui.JoinCode.Version.Text(), "⬆️") + "[/s]🔃")
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
