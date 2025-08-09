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
	"graphics.gd/classdb/PropertyTweener"
	"graphics.gd/classdb/Resource"
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
		Version     Label.Instance
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

	sharing      bool
	joinCode     chan networking.Code
	onlineStatus chan bool
	client       *Client
}

func (ui *CloudControl) Setup() {
	go func() {
		if err := ui.client.goOnline(); err != nil {
			fmt.Println("Error going online:", err)
			ui.onlineStatus <- false
			return
		}
		ui.onlineStatus <- true
	}()
}

func (ui *CloudControl) Ready() {
	ui.joinCode = make(chan networking.Code)
	ui.onlineStatus = make(chan bool, 1)
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
					ui.joinCode <- ""
					return
				}
				ui.joinCode <- code
				time.Sleep(5 * time.Minute)
				ui.joinCode <- ""
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

func (ui *CloudControl) Process(dt Float.X) {
	select {
	case online := <-ui.onlineStatus:
		var col = Color.X11.Green
		if !online {
			col = Color.X11.Red
		}
		tex := ui.HBoxContainer.Cloud.OnlineIndicator.Texture()
		grad := Object.To[GradientTexture2D.Instance](tex).Gradient()
		cols := grad.Colors()
		cols[0] = col
		grad.SetColors(cols)
	case code := <-ui.joinCode:
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
	default:
	}
}
