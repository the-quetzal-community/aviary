package internal

import (
	"sync/atomic"
	"time"

	"graphics.gd/classdb/BaseButton"
	"graphics.gd/classdb/Control"
	"graphics.gd/classdb/Engine"
	"graphics.gd/classdb/GradientTexture2D"
	"graphics.gd/classdb/GridContainer"
	"graphics.gd/classdb/HBoxContainer"
	"graphics.gd/classdb/Input"
	"graphics.gd/classdb/InputEvent"
	"graphics.gd/classdb/InputEventKey"
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
	"graphics.gd/classdb/VBoxContainer"
	"graphics.gd/classdb/Viewport"
	"graphics.gd/classdb/Window"
	"graphics.gd/variant/Color"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Signal"
	"graphics.gd/variant/Vector2"
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

	GizmoTypes     VBoxContainer.Instance
	GizmoIndicator TextureRect.Instance

	UpdateProgress ProgressBar.Instance

	Gizmo       Gizmo
	gizmoBackup Gizmo

	sharing    bool
	client     *Client
	on_process chan func(*CloudControl)
}

type Gizmo int

const (
	GizmoPoint Gizmo = iota
	GizmoShift
	GizmoTwist
	GizmoScale
)

var setting_up atomic.Bool
var version string

func (ui *CloudControl) Setup() {
	ui.on_process = make(chan func(*CloudControl), 10)
	if !setting_up.CompareAndSwap(false, true) {
		return
	}
	go ui.automaticallyUpdate()
}

func (ui *CloudControl) Input(event InputEvent.Instance) {
	if event, ok := Object.As[InputEventKey.Instance](event); ok {
		if event.AsInputEvent().IsPressed() {
			switch event.Keycode() {
			case Input.KeyShift:
				if Input.IsKeyPressed(Input.KeyCtrl) {
					ui.set_gizmo(GizmoScale)
				} else {
					ui.gizmoBackup = ui.Gizmo
					ui.set_gizmo(GizmoShift)
				}
			case Input.KeyCtrl:
				if Input.IsKeyPressed(Input.KeyShift) {
					ui.set_gizmo(GizmoScale)
				} else {
					ui.gizmoBackup = ui.Gizmo
					ui.set_gizmo(GizmoTwist)
				}
			}
		} else {
			switch event.Keycode() {
			case Input.KeyShift, Input.KeyCtrl:
				if Input.IsKeyPressed(Input.KeyShift) && Input.IsKeyPressed(Input.KeyCtrl) {
					return
				}
				ui.set_gizmo(ui.gizmoBackup)
			}
		}
	}
}

func (ui *CloudControl) set_gizmo(gizmo Gizmo) {
	ui.Gizmo = gizmo
	child := Object.To[Control.Instance](ui.GizmoTypes.AsNode().GetChild(int(gizmo)))
	PropertyTweener.Make(ui.GizmoIndicator.AsNode().CreateTween(), ui.GizmoIndicator.AsObject(), "position", Vector2.Sub(
		Vector2.Add(ui.GizmoTypes.AsControl().Position(), child.Position()),
		Vector2.New(3, 3),
	), 0.1).SetEase(Tween.EaseOut)
}

func (ui *CloudControl) Ready() {
	for i := 0; i < ui.GizmoTypes.AsNode().GetChildCount(); i++ {
		child := ui.GizmoTypes.AsNode().GetChild(i)
		Object.To[BaseButton.Instance](child).OnPressed(func() {
			ui.set_gizmo(Gizmo(i))
		})
	}
	ui.JoinCode.ShareButton.AsBaseButton().OnPressed(func() {
		if time.Now().After(UserState.Aviary.TogetherUntil) {
			OS.ShellOpen("https://the.quetzal.community/aviary/together?authorise=" + UserState.Secret)
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
