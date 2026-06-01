package internal

import (
	"math"

	"graphics.gd/classdb"
	"graphics.gd/classdb/Button"
	"graphics.gd/classdb/Control"
	"graphics.gd/classdb/DisplayServer"
	"graphics.gd/classdb/HBoxContainer"
	"graphics.gd/classdb/HSlider"
	"graphics.gd/classdb/Image"
	"graphics.gd/classdb/ImageTexture"
	"graphics.gd/classdb/Input"
	"graphics.gd/classdb/InputEvent"
	"graphics.gd/classdb/InputEventKey"
	"graphics.gd/classdb/InputEventMouseButton"
	"graphics.gd/classdb/Label"
	"graphics.gd/classdb/OS"
	"graphics.gd/classdb/Panel"
	"graphics.gd/classdb/PropertyTweener"
	"graphics.gd/classdb/Range"
	"graphics.gd/classdb/Texture2D"
	"graphics.gd/classdb/TextureButton"
	"graphics.gd/classdb/TextureRect"
	"graphics.gd/classdb/Tween"
	"graphics.gd/classdb/VBoxContainer"
	"graphics.gd/variant/Callable"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Path"
	"graphics.gd/variant/Vector2"

	"the.quetzal.community/aviary/internal/musical"
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

	// Pin the handle to the persisted (or default) launch level, then react to
	// every move. Set the value before connecting so this seed doesn't fire the
	// handler — the explicit Apply below covers the initial render.
	launchQ := UserState.GraphicsQuality
	slider.AsRange().SetValue(Float.X(launchQ))
	Range.Instance(slider.AsRange()).OnValueChanged(func(value Float.X) {
		q := GraphicsQuality(int(value))
		q.Apply(ui.AsNode())
		// SSAO's on/off flag lives on the world Environment, and the cloud
		// state spans the sky material + the volumetric-fog FogVolume — none of
		// which Apply's viewport/global state reaches, so apply them alongside.
		if ui.client != nil {
			q.ApplyAmbientOcclusion(ui.client.Environment)
			ui.client.applyCloudQuality(q)
			ui.client.applyWaterQuality(q)
			// Persist the choice so it survives across runs.
			UserState.GraphicsQuality = q
			UserState.GraphicsQualitySet = true
			ui.client.saveUserState()
		}
	})

	// Apply the launch quality (persisted or default) so the renderer matches
	// the slider's initial position before the user ever opens the menu.
	launchQ.Apply(ui.AsNode())
}

// environmentTopSpacer is the height (in EnvironmentMenu-local units) of the
// blank Control inserted before the first slider, reserving room for the
// EditorIndicator's white triangle the panel rolls out from. Tweak this to
// move the slider stack up or down.
const environmentTopSpacer Float.X = 372

// buildEnvironmentMenu creates the slider content for the lighting rolldown
// (Time of Day, Sun Angle, Fog, Clouds). The outer Panel lives in editor.tscn
// (EnvironmentMenu) so it participates in the same Rollout + scaling machinery
// as SettingsMenu. Content is built in code to keep the .tscn diff tiny and
// the layout easy to adjust.
func (ui *UI) buildEnvironmentMenu() {
	if ui.EnvironmentMenu.AsControl() == Control.Nil {
		return
	}
	ui.environmentSliders = make(map[string]HSlider.Instance)
	// Ensure we have a container to host the rows.
	containerNode := ui.EnvironmentMenu.AsNode().GetNode("Types")
	var container VBoxContainer.Instance
	if c, ok := Object.As[VBoxContainer.Instance](containerNode); ok {
		container = c
	} else {
		container = VBoxContainer.New()
		container.AsControl().SetAnchorsPreset(Control.PresetFullRect)
		ui.EnvironmentMenu.AsNode().AddChild(container.AsNode())
	}

	// Push the slider rows below the EditorIndicator's white triangle (the
	// toolbar this panel rolls out from) with a leading spacer, so they only
	// appear past its height. This is the single source of truth for that
	// gap — adjust environmentTopSpacer to move the first slider up/down.
	spacer := Control.New()
	spacer.SetCustomMinimumSize(Vector2.New(0, environmentTopSpacer))
	spacer.AsControl().SetMouseFilter(Control.MouseFilterIgnore)
	container.AsNode().AddChild(spacer.AsNode())

	// Downscale grabber exactly like the quality slider.
	const grabberSize = 64
	var smallGrabber Texture2D.Instance
	if tex := LoadSync[Texture2D.Instance]("res://ui/slider.png"); tex != Texture2D.Nil {
		if img := tex.GetImage(); img != Image.Nil {
			img.Resize(grabberSize, grabberSize)
			smallGrabber = ImageTexture.CreateFromImage(img).AsTexture2D()
		}
	}

	// Helper to make one labeled row. The caption is an icon loaded from
	// res://ui/<icon>.svg; if that texture isn't present yet (the icons are
	// still being sourced) it falls back to the text label so the row is
	// never blank.
	makeRow := func(sliderKey, icon, labelText string, minV, maxV, step, initV Float.X, onChange func(Float.X)) {
		row := HBoxContainer.New()
		row.AsControl().SetCustomMinimumSize(Vector2.New(0, 28))

		if tex := LoadSync[Texture2D.Instance]("res://ui/" + icon + ".svg"); tex != Texture2D.Nil {
			ico := TextureRect.New()
			ico.SetTexture(tex)
			ico.SetExpandMode(TextureRect.ExpandIgnoreSize)
			ico.SetStretchMode(TextureRect.StretchKeepAspectCentered)
			ico.AsControl().SetCustomMinimumSize(Vector2.New(72, 72))
			ico.AsControl().SetMouseFilter(Control.MouseFilterIgnore)
			row.AsNode().AddChild(ico.AsNode())
		} else {
			lbl := Label.New()
			lbl.SetText(labelText)
			lbl.AsControl().SetCustomMinimumSize(Vector2.New(90, 0))
			// Small readable size for the compact rolldown.
			lbl.AsControl().AddThemeFontSizeOverride("font_size", 14)
			row.AsNode().AddChild(lbl.AsNode())
		}

		sld := HSlider.Advanced(HSlider.New())
		ui.environmentSliders[sliderKey] = HSlider.Instance(sld)
		// Let the slider grab all the horizontal space the fixed-width label
		// leaves, so it spans the full panel width regardless of how wide the
		// triangle (and thus the panel) ends up.
		sldControl := HSlider.Instance(sld).AsControl()
		sldControl.SetCustomMinimumSize(Vector2.New(0, 24))
		sldControl.SetSizeFlagsHorizontal(Control.SizeExpandFill)
		sld.AsRange().SetMin(float64(minV))
		sld.AsRange().SetMax(float64(maxV))
		sld.AsRange().SetStep(float64(step))
		sld.AsRange().SetValue(float64(initV))

		if smallGrabber != Texture2D.Nil {
			h := HSlider.Instance(sld)
			for _, nm := range []string{"grabber", "grabber_highlight", "grabber_disabled"} {
				h.AsControl().AddThemeIconOverride(nm, smallGrabber)
			}
		}

		Range.Instance(sld.AsRange()).OnValueChanged(onChange)

		row.AsNode().AddChild(HSlider.Instance(sld).AsNode())
		container.AsNode().AddChild(row.AsNode())
	}

	// Seed the friendly controls from the authoritative lighting state so the
	// menu opens on the real current values (each axis stays independent
	// because the state stores them directly, not a lossy re-derivation from
	// the light's rotation).
	tod := Float.X(0.38) // sensible daytime default before the world exists
	sunAng := Float.X(0.08)
	fg := Float.X(0.0)
	cl := Float.X(0.0)
	rn := Float.X(0.0)
	sn := Float.X(0.0)
	wn := Float.X(0.0)
	mn := Float.X(0.5) // half moon by default
	if ui.client != nil {
		tod, sunAng, fg, cl, rn, sn, wn, mn = ui.client.GetLightingMenuState()
	}

	// Friendly, non-technical controls. We drive them through
	// ApplyLightingMenuState so each axis stays completely independent.
	makeRow("environment/time_of_day", "daytime", "Time of Day", 0, 1, 0.005, tod, func(v Float.X) {
		if ui.client != nil {
			_, angle, fog, clouds, rain, snow, wind, moon := ui.client.GetLightingMenuState()
			ui.client.ApplyLightingMenuState(v, angle, fog, clouds, rain, snow, wind, moon)
		}
		// Still send the Sculpt for networking + persistence
		ui.sendEnvironmentSlider("environment/time_of_day", v)
	})
	makeRow("environment/sun_angle", "sunside", "Sun Angle", 0, 1, 0.005, sunAng, func(v Float.X) {
		if ui.client != nil {
			tod, _, fog, clouds, rain, snow, wind, moon := ui.client.GetLightingMenuState()
			ui.client.ApplyLightingMenuState(tod, v, fog, clouds, rain, snow, wind, moon)
		}
		ui.sendEnvironmentSlider("environment/sun_angle", v)
	})
	makeRow("environment/fog", "fogmist", "Fog / Atmosphere", 0, 1, 0.01, fg, func(v Float.X) {
		if ui.client != nil {
			tod, angle, _, clouds, rain, snow, wind, moon := ui.client.GetLightingMenuState()
			ui.client.ApplyLightingMenuState(tod, angle, v, clouds, rain, snow, wind, moon)
		}
		ui.sendEnvironmentSlider("environment/fog", v)
	})
	makeRow("environment/clouds", "cumulus", "Clouds", 0, 1, 0.01, cl, func(v Float.X) {
		if ui.client != nil {
			tod, angle, fog, _, rain, snow, wind, moon := ui.client.GetLightingMenuState()
			ui.client.ApplyLightingMenuState(tod, angle, fog, v, rain, snow, wind, moon)
		}
		ui.sendEnvironmentSlider("environment/clouds", v)
	})
	makeRow("environment/rain", "raining", "Rain", 0, 1, 0.01, rn, func(v Float.X) {
		if ui.client != nil {
			tod, angle, fog, clouds, _, snow, wind, moon := ui.client.GetLightingMenuState()
			ui.client.ApplyLightingMenuState(tod, angle, fog, clouds, v, snow, wind, moon)
		}
		ui.sendEnvironmentSlider("environment/rain", v)
	})
	makeRow("environment/snow", "snowing", "Snow", 0, 1, 0.01, sn, func(v Float.X) {
		if ui.client != nil {
			tod, angle, fog, clouds, rain, _, wind, moon := ui.client.GetLightingMenuState()
			ui.client.ApplyLightingMenuState(tod, angle, fog, clouds, rain, v, wind, moon)
		}
		ui.sendEnvironmentSlider("environment/snow", v)
	})
	makeRow("environment/wind", "cyclone", "Wind", 0, 1, 0.01, wn, func(v Float.X) {
		if ui.client != nil {
			tod, angle, fog, clouds, rain, snow, _, moon := ui.client.GetLightingMenuState()
			ui.client.ApplyLightingMenuState(tod, angle, fog, clouds, rain, snow, v, moon)
		}
		ui.sendEnvironmentSlider("environment/wind", v)
	})
	makeRow("environment/moon", "moonlit", "Moon Phase", 0, 1, 0.01, mn, func(v Float.X) {
		if ui.client != nil {
			tod, angle, fog, clouds, rain, snow, wind, _ := ui.client.GetLightingMenuState()
			ui.client.ApplyLightingMenuState(tod, angle, fog, clouds, rain, snow, wind, v)
		}
		ui.sendEnvironmentSlider("environment/moon", v)
	})
}

// sendEnvironmentSlider builds a Sculpt with the correct Editor routing key
// for the active editor (so it lands in the right editor's Sculpt handler)
// and also applies immediately for snappy local feedback.
func (ui *UI) sendEnvironmentSlider(slider string, value Float.X) {
	if ui.client == nil || ui.client.space == nil {
		// Fallback: still push to renderer so the menu works even before join.
		ui.client.applyLightingStateFromSlider(slider, value)
		return
	}
	key := ui.client.activeLightingEditorKey()
	ui.client.space.Sculpt(musical.Sculpt{
		Editor: key,
		Slider: slider,
		Amount: value,
		Commit: true,
	})
	// Immediate local apply (the round-trip will re-apply the same value).
	ui.client.applyLightingStateFromSlider(slider, value)
}

// toggleSettings rolls the Settings menu in and out from behind the
// Toolbar triangle, sharing the Rollout helper with the editor switcher.
func (ui *UI) toggleSettings() {
	ui.settingsRollout.Toggle(ui.SettingsMenu.AsControl())
}

// toggleEnvironment rolls the lighting/fog/weather menu (Shading button).
func (ui *UI) toggleEnvironment() {
	// Re-sync the handles to the live lighting state before revealing them:
	// buildEnvironmentMenu seeds the sliders once during Setup, but the
	// persisted environment/* sculpts that carry the real values replay only
	// after the world loads, so the seed is stale by the time the user opens
	// the menu. By now the world is loaded, so GetLightingMenuState is right.
	ui.syncEnvironmentSliders()
	ui.environmentRollout.Toggle(ui.EnvironmentMenu.AsControl())
}

// syncEnvironmentSliders sets each lighting slider's handle to the current
// authoritative value from the Client's lightingMenuState. It uses
// SetValueNoSignal so refreshing the handle doesn't re-fire OnValueChanged
// (which would echo a redundant Sculpt back out). Safe to call repeatedly.
func (ui *UI) syncEnvironmentSliders() {
	if ui.client == nil || ui.environmentSliders == nil {
		return
	}
	tod, angle, fog, clouds, rain, snow, wind, moon := ui.client.GetLightingMenuState()
	for key, v := range map[string]Float.X{
		"environment/time_of_day": tod,
		"environment/sun_angle":   angle,
		"environment/fog":         fog,
		"environment/clouds":      clouds,
		"environment/rain":        rain,
		"environment/snow":        snow,
		"environment/wind":        wind,
		"environment/moon":        moon,
	} {
		if sld, ok := ui.environmentSliders[key]; ok {
			sld.AsRange().SetValueNoSignal(v)
		}
	}
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
