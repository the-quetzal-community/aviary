package internal

import (
	"math"

	"graphics.gd/classdb"
	"graphics.gd/classdb/Button"
	"graphics.gd/classdb/Control"
	"graphics.gd/classdb/DisplayServer"
	"graphics.gd/classdb/HSlider"
	"graphics.gd/classdb/Image"
	"graphics.gd/classdb/ImageTexture"
	"graphics.gd/classdb/Input"
	"graphics.gd/classdb/InputEvent"
	"graphics.gd/classdb/InputEventKey"
	"graphics.gd/classdb/OS"
	"graphics.gd/classdb/Panel"
	"graphics.gd/classdb/PropertyTweener"
	"graphics.gd/classdb/Range"
	"graphics.gd/classdb/Texture2D"
	"graphics.gd/classdb/TextureButton"
	"graphics.gd/classdb/Tween"
	"graphics.gd/variant/Callable"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Path"
	"graphics.gd/variant/Vector2"
)

// guideURL is the online guide opened by the toolbar's Help button.
const guideURL = "https://the.quetzal.community/aviary/guide"

/*
UI for editing a space in Aviary.
*/
type UI struct {
	Control.Extension[UI] `gd:"AviaryUI"`
	classdb.Tool

	texture chan Path.ToResource

	Editor *DesignExplorer

	ExpansionIndicator Button.Instance
	EditorIndicator    *EditorIndicator

	Toolbar struct {
		*Triangle

		Settings TextureButton.Instance
		Undo     TextureButton.Instance
		Redo     TextureButton.Instance
		Export   TextureButton.Instance
		Help     TextureButton.Instance
	}

	// SettingsMenu is the panel that rolls out from the Toolbar triangle
	// when the Settings cog is pressed, mirroring the editor switcher's
	// EditorSelector rollout. It's a root-level sibling so it stays
	// axis-aligned (the Toolbar itself is rotated) and is sized to the
	// Toolbar triangle's width in the scene. Its contents — the
	// graphics-quality slider — are declared in editor.tscn and wired up
	// by buildSettingsMenu.
	SettingsMenu Panel.Instance

	ModeGeometry TextureButton.Instance `gd:"%ModeGeometry"`
	ModeMaterial TextureButton.Instance `gd:"%ModeMaterial"`
	ModeDressing TextureButton.Instance `gd:"%ModeDressing"`

	CloudControl *CloudControl
	ViewSelector *ViewSelector

	Cloudy *FlightPlanner

	client *Client

	mode Mode

	// settingsRollout drives the Settings cog menu's slide animation,
	// sharing the same Rollout helper as the editor switcher.
	settingsRollout Rollout

	// undoSpin/redoSpin remember each button's resting rotation so the
	// click spin can return to the designed tilt instead of snapping
	// upright (see spinControl).
	undoSpin, redoSpin spinState
}

// spinState caches a toolbar button's resting rotation. rest is captured
// lazily on the first spin (when the button is at rest) so subsequent
// spins triggered mid-animation still return to the designed orientation.
type spinState struct {
	rest Float.X
	got  bool
}

func (ui *UI) Setup() {
	ui.Cloudy.client = ui.client
	ui.Cloudy.clientReady.Done()
	ui.CloudControl.client = ui.client
	ui.CloudControl.Setup()
	ui.EditorIndicator.client = ui.client
	// The Toolbar struct field's embedded *Triangle defeats graphics.gd's
	// auto-binder for the four TextureButton siblings — it treats the
	// outer struct as a single node-implementing value and never
	// recurses into the buttons, so they stay zero / pointing at an
	// orphan and OnPressed silently goes nowhere. Look them up via the
	// scene path directly so the handlers attach to the in-scene
	// nodes. Ctrl+Z working via UI.Input confirmed Undo() itself was
	// fine; only the button wiring was broken.
	toolbar := ui.AsNode().GetNode("Toolbar")
	if btn, ok := Object.As[TextureButton.Instance](toolbar.GetNode("Settings")); ok {
		ui.Toolbar.Settings = btn
	}
	if btn, ok := Object.As[TextureButton.Instance](toolbar.GetNode("Undo")); ok {
		ui.Toolbar.Undo = btn
	}
	if btn, ok := Object.As[TextureButton.Instance](toolbar.GetNode("Redo")); ok {
		ui.Toolbar.Redo = btn
	}
	if btn, ok := Object.As[TextureButton.Instance](toolbar.GetNode("Export")); ok {
		ui.Toolbar.Export = btn
	}
	if btn, ok := Object.As[TextureButton.Instance](toolbar.GetNode("HelpMe")); ok {
		ui.Toolbar.Help = btn
	}
	ui.Toolbar.Settings.AsBaseButton().OnPressed(ui.toggleSettings)
	ui.Toolbar.Undo.AsBaseButton().OnPressed(ui.undo)
	ui.Toolbar.Redo.AsBaseButton().OnPressed(ui.redo)
	ui.Toolbar.Export.AsBaseButton().OnPressed(func() {
		ui.client.Export()
	})
	// The Help button opens the online guide in the user's browser.
	ui.Toolbar.Help.AsBaseButton().OnPressed(func() {
		OS.ShellOpen(guideURL)
	})
	ui.buildSettingsMenu()
	// The Settings menu and the editor switcher roll out of the same
	// top-right corner, so make them mutually exclusive: opening either
	// slides the other shut, leaving only one panel revealed at a time.
	ui.settingsRollout.exclusive = []*Rollout{&ui.EditorIndicator.rollout}
	ui.EditorIndicator.rollout.exclusive = []*Rollout{&ui.settingsRollout}
	// Spin the cog each time its menu rolls out or in.
	ui.settingsRollout.icon = ui.Toolbar.Settings.AsControl()
}

// buildSettingsMenu wires the graphics-quality slider that lives in the
// Settings rollout. The layout — a gray toaster icon, the HSlider, and a
// gray sports-car icon, pushed below the rotated Toolbar triangle by a
// spacer — is declared in editor.tscn under
// SettingsMenu/SettingsTypes/QualityRow. The icons are PNGs from The Noun
// Project, tinted gray to read on the light drawer (see graphics/License).
// Code only handles what the scene can't: shrinking the oversized themed
// grabber for this compact row, syncing the launch level into both the
// handle and the live renderer, and applying [GraphicsQuality] on each move.
func (ui *UI) buildSettingsMenu() {
	if ui.SettingsMenu.AsControl() == Control.Nil {
		return
	}
	sliderNode := ui.SettingsMenu.AsNode().GetNode("SettingsTypes/QualityRow/Quality")
	slider, ok := Object.As[HSlider.Instance](sliderNode)
	if !ok {
		return
	}

	// The themed grabber (res://ui/slider.png) is 128×128 — sized for the
	// full-size sliders elsewhere — and Godot draws the handle at the
	// texture's native size, so it dwarfs this compact row. Downscale a
	// copy to roughly the row height and override it on just this slider.
	const grabberSize = 64
	if tex := LoadSync[Texture2D.Instance]("res://ui/slider.png"); tex != Texture2D.Nil {
		if img := tex.GetImage(); img != Image.Nil {
			img.Resize(grabberSize, grabberSize)
			small := ImageTexture.CreateFromImage(img).AsTexture2D()
			for _, name := range []string{"grabber", "grabber_highlight", "grabber_disabled"} {
				slider.AsControl().AddThemeIconOverride(name, small)
			}
		}
	}

	// Pin the handle to the launch level (keeping it in lockstep with the
	// renderer regardless of the value baked into the scene), then react to
	// every move. Set the value before connecting so this seed doesn't fire
	// the handler — the explicit Apply below covers the initial render.
	slider.AsRange().SetValue(Float.X(defaultGraphicsQuality))
	Range.Instance(slider.AsRange()).OnValueChanged(func(value Float.X) {
		GraphicsQuality(int(value)).Apply(ui.AsNode())
	})

	// Apply the launch default so the renderer matches the slider's
	// initial position before the user ever opens the menu.
	defaultGraphicsQuality.Apply(ui.AsNode())
}

// toggleSettings rolls the Settings menu in and out from behind the
// Toolbar triangle, sharing the Rollout helper with the editor switcher.
func (ui *UI) toggleSettings() {
	ui.settingsRollout.Toggle(ui.SettingsMenu.AsControl())
}

func (ui *UI) SetMode(mode Mode) {
	if ui.mode == mode {
		return
	}
	if prev := ui.modeButton(ui.mode); prev != nil {
		resizeAroundCenter(prev.AsControl(), shrunkSize)
	}
	if next := ui.modeButton(mode); next != nil {
		resizeAroundCenter(next.AsControl(), grownSize(mode))
	}
	ui.mode = mode
	ui.Editor.Refresh(ui.client.Editing, "", ui.mode)
	if ui.CloudControl != nil {
		// The dressing-density slider is only relevant while dressing the
		// terrain; mirror the editor gate used in StartEditing.
		ui.CloudControl.setDensitySliderVisible(ui.client.Editing == Editing.Terrain && mode == ModeDressing)
	}
}

var shrunkSize = Vector2.New[float32](24, 24)

func grownSize(m Mode) Vector2.XY {
	if m == ModeDressing {
		return Vector2.New[float32](36, 36)
	}
	return Vector2.New[float32](32, 32)
}

// resizeAroundCenter sets a Control's size while shifting its position so the
// visible center stays put. The target is clamped to the combined minimum size
// so Godot's size enforcement doesn't desync from the position shift.
func resizeAroundCenter(c Control.Instance, target Vector2.XY) {
	target = Vector2.Max(target, c.GetCombinedMinimumSize())
	delta := Vector2.MulX(Vector2.Mul(Vector2.Sub(target, c.Size()), c.Scale()), 0.5)
	c.SetPosition(Vector2.Sub(c.Position(), delta))
	c.SetSize(target)
}

// modeButton maps a Mode to the corresponding TextureButton on the UI
// so SetMode's shrink/grow animation can iterate the three tabs
// without a switch-per-arm.
func (ui *UI) modeButton(m Mode) *TextureButton.Instance {
	switch m {
	case ModeGeometry:
		return &ui.ModeGeometry
	case ModeMaterial:
		return &ui.ModeMaterial
	case ModeDressing:
		return &ui.ModeDressing
	}
	return nil
}

func (ui *UI) Process(_ Float.X) {
	if ui.client == nil {
		return
	}
	// Duplicate and Delete sit at the bottom of the gizmo column and
	// only make sense when something is selectable. Hide them so the
	// column doesn't show greyed-out tiles for ineligible editors.
	// Their visibility is further gated by the active editor's
	// SetGizmos declaration (GizmoTrash / GizmoClone).
	if ui.CloudControl != nil {
		wantDelete := ui.client.CanDeleteSelection() && ui.CloudControl.isGizmoAllowed(GizmoTrash)
		wantDup := ui.client.CanDeleteSelection() && ui.CloudControl.isGizmoAllowed(GizmoClone)
		if ui.CloudControl.GizmoTypes.Delete.AsCanvasItem().Visible() != wantDelete {
			ui.CloudControl.GizmoTypes.Delete.AsCanvasItem().SetVisible(wantDelete)
		}
		if ui.CloudControl.GizmoTypes.Duplicate.AsCanvasItem().Visible() != wantDup {
			ui.CloudControl.GizmoTypes.Duplicate.AsCanvasItem().SetVisible(wantDup)
		}
	}
}

func (ui *UI) Input(event InputEvent.Instance) {
	if event, ok := Object.As[InputEventKey.Instance](event); ok {
		if event.AsInputEvent().IsPressed() && !event.AsInputEvent().IsEcho() {
			if event.Keycode() == Input.KeyTab {
				switch ui.mode {
				case ModeGeometry:
					ui.SetMode(ModeMaterial)
				case ModeMaterial:
					ui.SetMode(ModeDressing)
				case ModeDressing:
					ui.SetMode(ModeGeometry)
				}
			}
			// Ctrl+Z = undo, Ctrl+Shift+Z (or Ctrl+Y) = redo. We
			// intentionally don't gate on focus — there's no text
			// input that should swallow these in editor context,
			// and the design-explorer drawer treats Tab as its own
			// command above without checking focus either.
			if event.Keycode() == Input.KeyZ && Input.IsKeyPressed(Input.KeyCtrl) && ui.client != nil {
				if Input.IsKeyPressed(Input.KeyShift) {
					ui.redo()
				} else {
					ui.undo()
				}
			}
			if event.Keycode() == Input.KeyY && Input.IsKeyPressed(Input.KeyCtrl) && ui.client != nil {
				ui.redo()
			}
		}
	}
}

// undo runs the client's undo and spins the Undo button one full turn
// counter-clockwise (matching the icon's arrow direction). Shared by the
// toolbar button and the Ctrl+Z keyboard shortcut so both animate.
func (ui *UI) undo() {
	spinFull(ui.Toolbar.Undo.AsControl(), &ui.undoSpin, -1)
	if ui.client != nil {
		ui.client.Undo()
	}
}

// redo mirrors [UI.undo], spinning the Redo button one full turn clockwise.
func (ui *UI) redo() {
	spinFull(ui.Toolbar.Redo.AsControl(), &ui.redoSpin, 1)
	if ui.client != nil {
		ui.client.Redo()
	}
}

// spinDuration is the slide length of one full-turn icon spin, shared by the
// toolbar buttons and the rollout indicators.
const spinDuration = 0.4

// spinFull spins ctrl one full turn about its center over spinDuration.
// screenDir is the desired on-screen direction: +1 clockwise, -1
// counter-clockwise.
func spinFull(ctrl Control.Instance, st *spinState, screenDir Float.X) {
	spinControl(ctrl, st, screenDir*2*math.Pi, spinDuration)
}

// spinControl tweens ctrl by screenDelta radians (signed; + = clockwise on
// screen) about its own center over duration, leaving it at restingTilt +
// screenDelta.
//
// Several of these controls rest at a designed tilt in the scene (e.g. the
// Redo button at ~-75°), so the resting angle is cached in st on first use
// and every spin starts from it — a full turn then lands on rest±2π, which
// is visually identical to rest, instead of snapping the icon upright. The
// cache also means a spin retriggered mid-animation still resolves to the
// designed orientation rather than drifting.
//
// Pinning the pivot to the center moves an already-rotated/scaled rect, so
// the position is compensated by the displacement (I - R·S)·Δpivot — R the
// resting rotation, S the scale — to keep the control visually put (zero
// for an axis-aligned, unscaled control). A mirrored (negative-determinant)
// control additionally has its delta flipped so the visible spin still goes
// the requested way.
func spinControl(ctrl Control.Instance, st *spinState, screenDelta, duration Float.X) {
	if ctrl == Control.Nil {
		return
	}
	if !st.got {
		st.rest = ctrl.Rotation()
		st.got = true
	}
	scale := ctrl.Scale()
	pivot0 := ctrl.PivotOffset()
	center := Vector2.MulX(ctrl.Size(), 0.5)
	// Displacement from re-pivoting: d - R·S·d, where d = center - pivot0
	// and R·S is Godot's Control linear transform (scale then rotate).
	dx := float64(center.X - pivot0.X)
	dy := float64(center.Y - pivot0.Y)
	sin, cos := math.Sincos(float64(st.rest))
	sx, sy := float64(scale.X), float64(scale.Y)
	shift := Vector2.New(
		Float.X(dx-(sx*cos*dx-sy*sin*dy)),
		Float.X(dy-(sx*sin*dx+sy*cos*dy)),
	)
	ctrl.SetPosition(Vector2.Sub(ctrl.Position(), shift))
	ctrl.SetPivotOffset(center)
	ctrl.SetRotation(st.rest)
	if scale.X*scale.Y < 0 {
		screenDelta = -screenDelta
	}
	PropertyTweener.Make(
		ctrl.AsNode().CreateTween(), ctrl.AsObject(),
		"rotation", st.rest+screenDelta, duration,
	).SetEase(Tween.EaseOut)
}

func (ui *UI) Ready() {
	ui.Editor.ExpansionIndicator = ui.ExpansionIndicator.ID()
	ui.Editor.client = ui.client

	ui.ModeGeometry.AsBaseButton().OnPressed(func() {
		ui.SetMode(ModeGeometry)
	})
	ui.ModeMaterial.AsBaseButton().OnPressed(func() {
		ui.SetMode(ModeMaterial)
	})
	ui.ModeDressing.AsBaseButton().OnPressed(func() {
		ui.SetMode(ModeDressing)
	})
	Callable.Defer(Callable.New(func() {
		for _, m := range []Mode{ModeGeometry, ModeMaterial, ModeDressing} {
			b := ui.modeButton(m)
			if b == nil {
				continue
			}
			target := shrunkSize
			if m == ui.mode {
				target = grownSize(m)
			}
			resizeAroundCenter(b.AsControl(), target)
		}
	}))

	ui.ExpansionIndicator.
		AsBaseButton().SetToggleMode(true).
		AsBaseButton().AsControl().OnMouseEntered(ui.Editor.openDrawer).
		AsControl().SetMouseFilter(Control.MouseFilterPass)
	ui.CloudControl.GizmoTypes.Delete.AsBaseButton().OnPressed(func() {
		if ui.client != nil {
			ui.client.DeleteSelection()
		}
	})
	ui.CloudControl.GizmoTypes.Duplicate.AsBaseButton().OnPressed(func() {
		if ui.client != nil {
			ui.client.DuplicateSelection()
		}
	})
	ui.CloudControl.HBoxContainer.Cloud.AsBaseButton().OnPressed(func() {
		ui.Cloudy.AsCanvasItem().SetVisible(!ui.Cloudy.AsCanvasItem().Visible())
		if ui.Cloudy.AsCanvasItem().Visible() {
			ui.Cloudy.Reload()
		}
	})
	ui.scaling()
	ui.AsControl().OnResized(ui.scaling)
	ui.ViewSelector.ViewSelected.Call(func(view string) {
		ui.Editor.editor.SwitchToView(view)
	})
}

func (ui *UI) scaling() {
	display := DisplayServer.WindowGetSize(0)
	// In VR the UI doesn't live in the main window — it's painted
	// into a 1920×1080 SubViewport per wrist panel. Pretend the
	// "display" is 1920×1080 so the drawer-position-by-bottom-of-
	// screen math doesn't push the drawer off the visible quad.
	if ui.client != nil && ui.client.xr {
		display.X = 1920
		display.Y = 1080
	}

	// Calculate uniform scale factor based on height ratio (360 base height at 2160 screen height)
	var scale_factor Float.X = Float.X(display.Y) / 2160.0

	if scale_factor < 0.5 {
		scale_factor = 0.5
	}

	// Set uniform scale for both X and Y (to scale contents like tab icons without distortion)
	scale := Vector2.XY{X: scale_factor, Y: scale_factor}
	ui.Editor.AsControl().SetScale(scale)

	// Adjust logical size.X to fill the full display width after scaling
	// (Do not change size.Y; assume it's fixed at base, e.g., 360)
	size := ui.Editor.AsControl().Size()
	size.X = Float.X(display.X) / scale_factor
	ui.Editor.AsControl().SetSize(size)

	// Pin to the bottom: Set position.Y so the bottom aligns with screen bottom
	// (Assuming position.X is 0 or left-aligned; adjust if needed)
	pos := ui.Editor.AsControl().Position()
	pos.Y = Float.X(display.Y) - (size.Y * scale_factor)
	ui.Editor.AsControl().SetPosition(pos)

	// scale root UI elements based on display size
	ui.scaleDefault(ui.CloudControl.AsControl())
	ui.scaleDefault(ui.ViewSelector.AsControl())
	ui.scaleDefault(ui.ExpansionIndicator.AsControl())
	ui.scaleDefault(ui.EditorIndicator.AsControl())
	ui.scaleDefault(ui.Toolbar.AsControl())
	// Scale the Settings rollout with the same factor as the Toolbar so it
	// keeps the triangle's width across display sizes. Pin the pivot to the
	// panel's actual right edge first: it's anchored left/right at 1.0, so
	// its width is the authored offset span, and scaling around the right
	// edge keeps it flush to the top-right corner. A hard-coded pivot would
	// drift the moment the panel is resized in the editor, scaling it
	// inward and opening a right-side gap that grows as the window shrinks.
	if sm := ui.SettingsMenu.AsControl(); sm != Control.Nil {
		sm.SetPivotOffset(Vector2.New(sm.Size().X, 0))
		ui.scaleDefault(sm)
	}

	// ViewSelector needs to be centered to the top center
	theme_pos := ui.ViewSelector.AsControl().Position()
	theme_scale := ui.ViewSelector.AsControl().Scale()
	theme_size := ui.ViewSelector.AsControl().Size()
	theme_pos.X = (Float.X(display.X)/2 - (theme_size.X * theme_scale.X * Float.X(len(ui.ViewSelector.views))))
	ui.ViewSelector.AsControl().SetPosition(theme_pos)
}

// Reference display the root UI controls are authored against, and the
// floor scale factor below which they stop shrinking. Shared by every
// scaleDefault call so the magic numbers live in one place.
const (
	baseScreenWidth  Float.X = 3840
	baseScreenHeight Float.X = 2160
	minUIScale       Float.X = 0.5
)

// scaleDefault scales a root UI control against the reference display
// (baseScreenWidth × baseScreenHeight, floored at minUIScale). Wraps the
// previously-repeated ui.scale(c, 3840, 2160, 0.5) call.
func (ui *UI) scaleDefault(control Control.Instance) {
	ui.scale(control, baseScreenWidth, baseScreenHeight, minUIScale)
}

func (ui *UI) scale(control Control.Instance, base_screen_width, base_screen_height, min_scale Float.X) {
	display := DisplayServer.WindowGetSize(0)

	// Determine which change is more significant
	var scale_factor Float.X
	if Float.X(display.Y)/base_screen_height > Float.X(display.X)/base_screen_width {
		// Height change is larger: scale based on height
		scale_factor = Float.X(display.Y) / base_screen_height
	} else {
		// Width change is larger (or equal): scale based on width, preserving aspect
		scale_factor = Float.X(display.X) / base_screen_width
	}

	if scale_factor < min_scale {
		scale_factor = min_scale
	}

	// Set uniform scale for both X and Y (preserves aspect, scales icons etc.)
	scale := Vector2.XY{X: scale_factor, Y: scale_factor}
	control.SetScale(scale)
}
