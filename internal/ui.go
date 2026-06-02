package internal

import (
	"math"

	"graphics.gd/classdb"
	"graphics.gd/classdb/Button"
	"graphics.gd/classdb/Control"
	"graphics.gd/classdb/DisplayServer"
	"graphics.gd/classdb/HSlider"
	"graphics.gd/classdb/Input"
	"graphics.gd/classdb/InputEvent"
	"graphics.gd/classdb/InputEventKey"
	"graphics.gd/classdb/InputEventMouseButton"
	"graphics.gd/classdb/OS"
	"graphics.gd/classdb/Panel"
	"graphics.gd/classdb/PropertyTweener"
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

	// EnvironmentMenu is the lighting/fog/weather rolldown (sun azimuth/
	// elevation, energy, fog density). Triggered from the Shading button
	// on the EditorIndicator. Uses the same Rollout mechanism as SettingsMenu.
	// Its 4 sliders are created dynamically so the .tscn change stays tiny.
	EnvironmentMenu Panel.Instance

	ModeGeometry TextureButton.Instance `gd:"%ModeGeometry"`
	ModeMaterial TextureButton.Instance `gd:"%ModeMaterial"`
	ModeDressing TextureButton.Instance `gd:"%ModeDressing"`
	ModeTriangle *Triangle              `gd:"%ModeTriangle"`

	CloudControl *CloudControl
	ViewSelector *ViewSelector

	Cloudy *FlightPlanner

	client *Client

	mode Mode

	// settingsRollout drives the Settings cog menu's slide animation,
	// sharing the same Rollout helper as the editor switcher.
	settingsRollout Rollout

	// environmentRollout drives the new Shading/lighting rolldown.
	// Mutually exclusive with the other two top-right rollouts.
	environmentRollout Rollout

	// environmentSliders maps each environment/* slider key to its handle so
	// the rolldown can be re-synced to the authoritative lighting state when
	// it opens (the persisted state loads after the menu is first built).
	environmentSliders map[string]HSlider.Instance

	// undoSpin/redoSpin remember each button's resting rotation so the
	// click spin can return to the designed tilt instead of snapping
	// upright (see spinControl).
	undoSpin, redoSpin spinState

	// photoMode is true while the camera view (in the ViewSelector) has
	// hidden the whole editor overlay for a clean, UI-free screenshot. The
	// next key/mouse press restores it; see enterPhotoMode / UI.Input.
	photoMode bool
	// photoArrowsRestore remembers whether the terrain extend/reveal arrows
	// (3D, but functionally UI) were showing when photo mode hid them, so
	// exitPhotoMode only brings them back if they were up to begin with.
	photoArrowsRestore bool
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
	ui.EditorIndicator.Shading.AsBaseButton().OnPressed(ui.toggleEnvironment)

	ui.buildSettingsMenu()
	ui.buildEnvironmentMenu()

	// The three top-right rollouts (editor switcher, settings, lighting)
	// are mutually exclusive — only one may be open at a time.
	ui.settingsRollout.exclusive = []*Rollout{&ui.EditorIndicator.rollout, &ui.environmentRollout}
	ui.EditorIndicator.rollout.exclusive = []*Rollout{&ui.settingsRollout, &ui.environmentRollout}
	ui.environmentRollout.exclusive = []*Rollout{&ui.settingsRollout, &ui.EditorIndicator.rollout}

	// Spin the cog / shading icon each time its menu rolls out or in.
	ui.settingsRollout.icon = ui.Toolbar.Settings.AsControl()
	ui.environmentRollout.icon = ui.EditorIndicator.Shading.AsControl()
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
	if ui.client != nil && ui.client.Editing == Editing.Terrain {
		ui.client.TerrainEditor.syncBrushSliders()
	}
}

// cycleMode advances to the next editing mode (Geometry → Material → Dressing → Geometry),
// exactly as the Tab key does. Both Tab and clicks on the background of the mode switcher
// triangle (the ModeTriangle behind the three mode icons) invoke this.
func (ui *UI) cycleMode() {
	switch ui.mode {
	case ModeGeometry:
		ui.SetMode(ModeMaterial)
	case ModeMaterial:
		ui.SetMode(ModeDressing)
	case ModeDressing:
		ui.SetMode(ModeGeometry)
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
	// Photo mode is a free-look framing view: camera navigation passes
	// straight through so the shot can still be framed (WASD/arrows/Q/E/R/F/
	// +-, middle-drag to orbit, wheel to dolly). Only a deliberate left/right
	// click — or a non-navigation key — brings the overlay back, and that
	// dismissing press is swallowed (AcceptEvent) so it doesn't also paint
	// terrain or select an object underneath. _input is delivered even though
	// the Control is hidden, which is what lets this fire at all.
	if ui.photoMode {
		if key, ok := Object.As[InputEventKey.Instance](event); ok {
			if key.AsInputEvent().IsPressed() && !key.AsInputEvent().IsEcho() && !isPhotoNavKey(key.Keycode()) {
				ui.exitPhotoMode()
				ui.AsControl().AcceptEvent()
			}
			return
		}
		if mb, ok := Object.As[InputEventMouseButton.Instance](event); ok {
			btn := mb.ButtonIndex()
			if mb.AsInputEvent().IsPressed() && (btn == Input.MouseButtonLeft || btn == Input.MouseButtonRight) {
				ui.exitPhotoMode()
				ui.AsControl().AcceptEvent()
			}
			return
		}
		return
	}
	if event, ok := Object.As[InputEventKey.Instance](event); ok {
		if event.AsInputEvent().IsPressed() && !event.AsInputEvent().IsEcho() {
			if event.Keycode() == Input.KeyTab {
				ui.cycleMode()
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

// undo attempts the client's undo. If an entry was reversed it spins the
// Undo button one full counter-clockwise turn (matching the circular arrow
// icon). If the undo stack was empty, it instead performs a short forward
// nudge then back animation so the button always reacts to presses while
// visually signalling that nothing further can be undone. The same handler
// is used for both the toolbar button and the Ctrl+Z shortcut.
func (ui *UI) undo() {
	did := false
	if ui.client != nil {
		did = ui.client.Undo()
	}
	if did {
		spinFull(ui.Toolbar.Undo.AsControl(), &ui.undoSpin, -1)
	} else {
		spinNudge(ui.Toolbar.Undo.AsControl(), &ui.undoSpin, -1)
	}
}

// redo mirrors [UI.undo] for the opposite direction and Ctrl+Y / Ctrl+Shift+Z.
func (ui *UI) redo() {
	did := false
	if ui.client != nil {
		did = ui.client.Redo()
	}
	if did {
		spinFull(ui.Toolbar.Redo.AsControl(), &ui.redoSpin, 1)
	} else {
		spinNudge(ui.Toolbar.Redo.AsControl(), &ui.redoSpin, 1)
	}
}

// spinDuration is the slide length of one full-turn icon spin, shared by the
// toolbar buttons and the rollout indicators.
const spinDuration = 0.4

// nudge* control the "nothing left to undo/redo" affordance: the button
// rotates a fraction of a turn in the natural direction then returns to
// rest, over a total of ~0.3s. The angle is large enough to read as an
// intentional "try" but clearly not a completed action.
const (
	nudgeAngle   = 0.75 // radians (~43°)
	nudgeOutDur  = 0.12
	nudgeBackDur = 0.18
)

// spinFull spins ctrl one full turn about its center over spinDuration.
// screenDir is the desired on-screen direction: +1 clockwise, -1
// counter-clockwise.
func spinFull(ctrl Control.Instance, st *spinState, screenDir Float.X) {
	spinControl(ctrl, st, screenDir*2*math.Pi, spinDuration)
}

// spinPrepare captures the control's authored rest rotation (if not already
// known) and re-pivots it about its center while translating to keep the
// visual position unchanged. All spin animations call this first so that
// the subsequent rotation tweens orbit the icon's own centre rather than
// its top-left (or authored pivot). After prepare the control sits at its
// rest angle, ready for a delta tween.
func spinPrepare(ctrl Control.Instance, st *spinState) {
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
}

// spinControl tweens ctrl by screenDelta radians (signed; + = clockwise on
// screen) about its own center over duration, leaving it at rest + delta.
// See spinPrepare for the pivot math and why we cache rest. A mirrored
// (negative-determinant) control has its delta flipped so the visible spin
// still goes the requested way.
func spinControl(ctrl Control.Instance, st *spinState, screenDelta, duration Float.X) {
	if ctrl == Control.Nil {
		return
	}
	spinPrepare(ctrl, st)
	scale := ctrl.Scale()
	if scale.X*scale.Y < 0 {
		screenDelta = -screenDelta
	}
	PropertyTweener.Make(
		ctrl.AsNode().CreateTween(), ctrl.AsObject(),
		"rotation", st.rest+screenDelta, duration,
	).SetEase(Tween.EaseOut)
}

// spinNudge implements the failure affordance for Undo/Redo: the control
// is nudged a short distance in the requested screenDir then tweened back
// to its exact rest orientation using two sequential tweens on the same
// Tween instance. This produces the "tries to spin but spins back" motion
// without leaving the button rotated.
func spinNudge(ctrl Control.Instance, st *spinState, screenDir Float.X) {
	if ctrl == Control.Nil {
		return
	}
	spinPrepare(ctrl, st)
	scale := ctrl.Scale()
	if scale.X*scale.Y < 0 {
		screenDir = -screenDir
	}
	nudge := screenDir * nudgeAngle
	tw := ctrl.AsNode().CreateTween()
	PropertyTweener.Make(tw, ctrl.AsObject(), "rotation", st.rest+nudge, nudgeOutDur).
		SetEase(Tween.EaseOut)
	PropertyTweener.Make(tw, ctrl.AsObject(), "rotation", st.rest, nudgeBackDur).
		SetEase(Tween.EaseIn)
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

	if ui.ModeTriangle != nil {
		ui.ModeTriangle.AsControl().SetMouseFilter(Control.MouseFilterStop)
		ui.ModeTriangle.AsControl().OnGuiInput(func(event InputEvent.Instance) {
			if mb, ok := Object.As[InputEventMouseButton.Instance](event); ok {
				if mb.ButtonIndex() == Input.MouseButtonLeft && mb.AsInputEvent().IsPressed() {
					ui.cycleMode()
					ui.ModeTriangle.AsControl().AcceptEvent()
				}
			}
		})
	}

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
		// The camera view is a UI-level action (hide the overlay), not an
		// editor view, so it never reaches the active editor's SwitchToView.
		if view == cameraView {
			ui.enterPhotoMode()
			return
		}
		ui.Editor.editor.SwitchToView(view)
	})
}

// enterPhotoMode hides the entire editor overlay for a clean, UI-free view
// ("photo mode"), triggered by the camera view in the top-of-screen
// ViewSelector. Any subsequent key or mouse-button press restores it (see
// UI.Input) — UI.Input keeps firing while the Control is hidden because
// _input is delivered regardless of Control visibility. No-op in XR, where
// the overlay lives on wrist panels and there is no key/mouse to bring it
// back.
func (ui *UI) enterPhotoMode() {
	if ui.photoMode {
		return
	}
	if ui.client != nil && ui.client.xr {
		return
	}
	ui.photoMode = true
	ui.AsCanvasItem().SetVisible(false)
	// The terrain extend/reveal arrows live in the 3D world rather than the
	// overlay, so hiding the Control alone leaves them floating in shot. Hide
	// them too, remembering whether they were up so exitPhotoMode can restore
	// exactly that (they only show while the terrain editor is active).
	if ui.client != nil && ui.client.TerrainEditor != nil {
		ui.photoArrowsRestore = ui.client.TerrainEditor.arrowsVisible
		ui.client.TerrainEditor.setArrowsVisible(false)
	}
}

// exitPhotoMode restores the overlay hidden by enterPhotoMode.
func (ui *UI) exitPhotoMode() {
	if !ui.photoMode {
		return
	}
	ui.photoMode = false
	ui.AsCanvasItem().SetVisible(true)
	if ui.photoArrowsRestore && ui.client != nil && ui.client.TerrainEditor != nil {
		ui.client.TerrainEditor.setArrowsVisible(true)
	}
	ui.photoArrowsRestore = false
}

// isPhotoNavKey reports whether a key drives the camera (WASD/arrows to move,
// Q/E to turn, R/F to tilt, +/- to dolly) or is a bare modifier. These never
// dismiss photo mode, so the shot can be framed from the keyboard; any other
// key does. The set mirrors the camera keys polled in Client.Process, plus
// the modifiers so e.g. Shift+wheel doesn't pop the overlay back.
func isPhotoNavKey(keycode Input.Key) bool {
	switch keycode {
	case Input.KeyW, Input.KeyA, Input.KeyS, Input.KeyD,
		Input.KeyUp, Input.KeyDown, Input.KeyLeft, Input.KeyRight,
		Input.KeyQ, Input.KeyE, Input.KeyR, Input.KeyF,
		Input.KeyEqual, Input.KeyMinus,
		Input.KeyShift, Input.KeyCtrl, Input.KeyAlt, Input.KeyMeta:
		return true
	}
	return false
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
	// EnvironmentMenu rolls out from the EditorIndicator's white triangle, so
	// scale it with the same factor as that triangle to keep their widths in
	// lock-step across display sizes (the panel is authored to the triangle's
	// width). Pin the pivot to the panel's right edge first so it stays flush
	// to the top-right corner, exactly as SettingsMenu does above.
	if em := ui.EnvironmentMenu.AsControl(); em != Control.Nil {
		em.SetPivotOffset(Vector2.New(em.Size().X, 0))
		ui.scaleDefault(em)
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
