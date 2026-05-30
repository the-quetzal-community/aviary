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
	"graphics.gd/classdb/HSlider"
	"graphics.gd/classdb/Image"
	"graphics.gd/classdb/ImageTexture"
	"graphics.gd/classdb/Input"
	"graphics.gd/classdb/InputEvent"
	"graphics.gd/classdb/InputEventKey"
	"graphics.gd/classdb/Label"
	"graphics.gd/classdb/Material"
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/OS"
	"graphics.gd/classdb/Panel"
	"graphics.gd/classdb/ProgressBar"
	"graphics.gd/classdb/PropertyTweener"
	"graphics.gd/classdb/Range"
	"graphics.gd/classdb/RichTextLabel"
	"graphics.gd/classdb/SceneTree"
	"graphics.gd/classdb/Shader"
	"graphics.gd/classdb/ShaderMaterial"
	"graphics.gd/classdb/TextEdit"
	"graphics.gd/classdb/Texture2D"
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

	GizmoTypes struct {
		VBoxContainer.Instance

		// The four gizmo buttons sit at scene indices 0..3 so
		// set_gizmo can address them with GetChild(int(gizmo)).
		// Duplicate / Delete live in the same column underneath an
		// HSeparator — Ready() limits the gizmo OnPressed wire-up to
		// the gizmo prefix so the action buttons don't get hooked as
		// extra gizmos.
		Duplicate TextureButton.Instance
		Delete    TextureButton.Instance
	}
	GizmoIndicator TextureRect.Instance

	UpdateProgress ProgressBar.Instance

	Gizmo       Gizmo
	gizmoBackup Gizmo

	sharing    bool
	client     *Client
	on_process chan func(*CloudControl)

	// sizeSlider is the terrain brush-size slider built in code and
	// parented to CloudControl (a sibling of GizmoTypes, like
	// GizmoIndicator). It only shows while the terrain editor is active
	// and is pinned next to the Shift gizmo button each frame.
	sizeSlider      HSlider.Instance
	sizeSliderReady bool

	// densitySlider is the terrain dressing-density slider, shown only
	// while the terrain editor is in ModeDressing. It's pinned just below
	// the size slider and feeds the "dressing/density" slider channel.
	densitySlider      HSlider.Instance
	densitySliderReady bool
}

type Gizmo int

const (
	GizmoPoint Gizmo = iota
	GizmoShift
	GizmoTwist
	GizmoFloat

	GizmoClone
	GizmoTrash

	GizmoBrush
	GizmoPower
	GizmoScale
	GizmoErase
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
	types := ui.GizmoTypes.AsControl()
	indicator := ui.GizmoIndicator.AsControl()
	childCenter := Vector2.Add(
		types.Position(),
		Vector2.Mul(Vector2.Add(child.Position(), Vector2.MulX(child.Size(), 0.5)), types.Scale()),
	)
	indicatorHalf := Vector2.MulX(Vector2.Mul(indicator.Size(), indicator.Scale()), 0.5)
	target := Vector2.Sub(childCenter, indicatorHalf)
	PropertyTweener.Make(indicator.AsNode().CreateTween(), indicator.AsObject(), "position", target, 0.1).SetEase(Tween.EaseOut)
}

func (ui *CloudControl) Ready() {
	// First four GizmoTypes children are the gizmo TextureButtons in
	// the enum order GizmoPoint/Shift/Twist/Scale. Children after
	// that are the action group (HSeparator, Duplicate, Delete) and
	// must not be hooked as extra gizmos — their OnPressed is wired
	// from UI.Ready instead so the client reference is available.
	const gizmoCount = 4
	for i := 0; i < gizmoCount; i++ {
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
			var spinner = LoadSync[Shader.Instance]("res://shader/spinner.gdshader")
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

	ui.buildSizeSlider()
	ui.buildDensitySlider()
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
	ui.positionSizeSlider()
	ui.positionDensitySlider()
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

// newToolbarSlider builds a hidden horizontal slider styled to match the
// gizmo toolbar (tall hit rect that stops mouse fall-through, downscaled
// themed grabber) and wires its value_changed to onChanged. Shared by the
// brush-size and dressing-density sliders.
func (ui *CloudControl) newToolbarSlider(min, max, step, init float64, onChanged func(value Float.X)) HSlider.Instance {
	slider := HSlider.Advanced(HSlider.New())
	slider.AsRange().SetMin(min)
	slider.AsRange().SetMax(max)
	slider.AsRange().SetStep(step)
	slider.AsRange().SetValue(init)
	// Make the hit rect as tall as the grabber (64) — not the slim 24 the
	// track needs — so pressing anywhere on the visible handle lands on the
	// control. With MouseFilterStop, Godot then consumes the whole drag and
	// it never falls through to terrain sculpt/paint or selection in the world.
	HSlider.Instance(slider).AsControl().SetCustomMinimumSize(Vector2.New(280, 64))
	HSlider.Instance(slider).AsControl().SetMouseFilter(Control.MouseFilterStop)
	HSlider.Instance(slider).AsCanvasItem().SetVisible(false)
	// Match the Settings menu slider's handle: the themed grabber
	// (res://ui/slider.png) is 128×128 and Godot draws it at its native
	// size, so downscale a copy and override it on just this slider.
	const grabberSize = 64
	if tex := LoadSync[Texture2D.Instance]("res://ui/slider.png"); tex != Texture2D.Nil {
		if img := tex.GetImage(); img != Image.Nil {
			img.Resize(grabberSize, grabberSize)
			small := ImageTexture.CreateFromImage(img).AsTexture2D()
			for _, name := range []string{"grabber", "grabber_highlight", "grabber_disabled"} {
				HSlider.Instance(slider).AsControl().AddThemeIconOverride(name, small)
			}
		}
	}
	Range.Instance(slider.AsRange()).OnValueChanged(onChanged)
	ui.AsNode().AddChild(Node.Instance(slider.AsNode()))
	return HSlider.Instance(slider)
}

// buildSizeSlider creates the terrain brush-size slider shown in the
// gizmo toolbar beside the Shift button. It's created hidden; the editor
// switch (setSizeSliderVisible) reveals it only while the terrain editor
// is active, and positionSizeSlider keeps it pinned next to Shift. The
// value is forwarded to the terrain editor through the same
// SliderHandle/SliderConfig contract the design explorer used.
func (ui *CloudControl) buildSizeSlider() {
	ui.sizeSlider = ui.newToolbarSlider(0, 10, 0.01, 2, func(value Float.X) {
		if ui.client == nil {
			return
		}
		ui.client.TerrainEditor.SliderHandle(ModeGeometry, "editing/radius", float64(value), false)
	})
	ui.sizeSliderReady = true
}

// buildDensitySlider creates the terrain dressing-density slider. Like the
// size slider it's created hidden and pinned in the toolbar; it's only
// revealed while the terrain editor is in ModeDressing.
func (ui *CloudControl) buildDensitySlider() {
	ui.densitySlider = ui.newToolbarSlider(0, 1, 0.01, 0.5, func(value Float.X) {
		if ui.client == nil {
			return
		}
		ui.client.TerrainEditor.SliderHandle(ModeDressing, "dressing/density", float64(value), false)
	})
	ui.densitySliderReady = true
}

// setSizeSliderVisible reveals or hides the terrain brush-size slider.
// When revealing, it syncs the slider's range and current value from the
// terrain editor so it reflects the live brush radius.
func (ui *CloudControl) setSizeSliderVisible(v bool) {
	if !ui.sizeSliderReady {
		return
	}
	if v && ui.client != nil {
		init, min, max, step := ui.client.TerrainEditor.SliderConfig(ModeGeometry, "editing/radius")
		r := Range.Advanced(ui.sizeSlider.AsRange())
		r.SetMin(min)
		r.SetMax(max)
		r.SetStep(step)
		ui.sizeSlider.AsRange().SetValueNoSignal(Float.X(init))
	}
	ui.sizeSlider.AsCanvasItem().SetVisible(v)
}

// setSizeSliderValue moves the size slider's handle to v without
// re-emitting value_changed — the caller has already applied the radius
// (e.g. Shift+scroll resizing the terrain brush).
func (ui *CloudControl) setSizeSliderValue(v float64) {
	if !ui.sizeSliderReady {
		return
	}
	ui.sizeSlider.AsRange().SetValueNoSignal(Float.X(v))
}

// setDensitySliderVisible reveals or hides the dressing-density slider,
// syncing its range and value from the terrain editor when revealing.
func (ui *CloudControl) setDensitySliderVisible(v bool) {
	if !ui.densitySliderReady {
		return
	}
	if v && ui.client != nil {
		init, min, max, step := ui.client.TerrainEditor.SliderConfig(ModeDressing, "dressing/density")
		r := Range.Advanced(ui.densitySlider.AsRange())
		r.SetMin(min)
		r.SetMax(max)
		r.SetStep(step)
		ui.densitySlider.AsRange().SetValueNoSignal(Float.X(init))
	}
	ui.densitySlider.AsCanvasItem().SetVisible(v)
}

// positionSizeSlider pins the size slider just to the right of the Shift
// gizmo button each frame while it's visible, mirroring the maths
// set_gizmo uses to place the gizmo indicator so the slider tracks the
// button across window/layout changes.
func (ui *CloudControl) positionSizeSlider() {
	if !ui.sizeSliderReady || !ui.sizeSlider.AsCanvasItem().Visible() {
		return
	}
	shift := Object.To[Control.Instance](ui.GizmoTypes.AsNode().GetChild(int(GizmoShift)))
	if shift == Control.Nil {
		return
	}
	types := ui.GizmoTypes.AsControl()
	// Right-edge vertical-centre of the Shift button in CloudControl space.
	edge := Vector2.Add(
		types.Position(),
		Vector2.Mul(
			Vector2.Add(shift.Position(), Vector2.New(shift.Size().X, shift.Size().Y*0.5)),
			types.Scale(),
		),
	)
	const gap = 12
	size := ui.sizeSlider.AsControl().Size()
	ui.sizeSlider.AsControl().SetPosition(Vector2.New(edge.X+gap, edge.Y-size.Y*0.5))
}

// positionDensitySlider pins the dressing-density slider just below the
// brush-size slider's slot (both sit to the right of the Shift button), so
// the two stack vertically in the toolbar.
func (ui *CloudControl) positionDensitySlider() {
	if !ui.densitySliderReady || !ui.densitySlider.AsCanvasItem().Visible() {
		return
	}
	shift := Object.To[Control.Instance](ui.GizmoTypes.AsNode().GetChild(int(GizmoShift)))
	if shift == Control.Nil {
		return
	}
	types := ui.GizmoTypes.AsControl()
	edge := Vector2.Add(
		types.Position(),
		Vector2.Mul(
			Vector2.Add(shift.Position(), Vector2.New(shift.Size().X, shift.Size().Y*0.5)),
			types.Scale(),
		),
	)
	const gap = 12
	const stack = 56 // vertical offset below the size slider's centre line
	size := ui.densitySlider.AsControl().Size()
	ui.densitySlider.AsControl().SetPosition(Vector2.New(edge.X+gap, edge.Y-size.Y*0.5+stack))
}
