package internal

import (
	"graphics.gd/classdb"
	"graphics.gd/classdb/Button"
	"graphics.gd/classdb/Control"
	"graphics.gd/classdb/DisplayServer"
	"graphics.gd/classdb/HBoxContainer"
	"graphics.gd/classdb/HSlider"
	"graphics.gd/classdb/Input"
	"graphics.gd/classdb/InputEvent"
	"graphics.gd/classdb/InputEventKey"
	"graphics.gd/classdb/Panel"
	"graphics.gd/classdb/Range"
	"graphics.gd/classdb/Texture2D"
	"graphics.gd/classdb/TextureButton"
	"graphics.gd/classdb/TextureRect"
	"graphics.gd/classdb/VBoxContainer"
	"graphics.gd/variant/Callable"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Path"
	"graphics.gd/variant/Vector2"
)

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
	}

	// SettingsMenu is the (initially empty) panel that rolls out from
	// the Toolbar triangle when the Settings cog is pressed, mirroring
	// the editor switcher's EditorSelector rollout. It's a root-level
	// sibling so it stays axis-aligned (the Toolbar itself is rotated)
	// and is sized to the Toolbar triangle's width in the scene.
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
	ui.Toolbar.Settings.AsBaseButton().OnPressed(ui.toggleSettings)
	ui.Toolbar.Undo.AsBaseButton().OnPressed(func() {
		ui.client.Undo()
	})
	ui.Toolbar.Redo.AsBaseButton().OnPressed(func() {
		ui.client.Redo()
	})
	ui.Toolbar.Export.AsBaseButton().OnPressed(func() {
		ui.client.Export()
	})
	ui.buildSettingsMenu()
}

// buildSettingsMenu populates the (otherwise empty) Settings rollout
// with the graphics-quality slider: a gray toaster icon on the low end,
// a gray sports-car icon on the high end, and an HSlider between them
// that drives [GraphicsQuality] from QualityToaster..QualityFerrari.
// Built in code (rather than the .tscn) so the icons load via
// Resource.Load without needing committed .import sidecars, matching how
// the design explorer loads its library thumbnails. The icons are PNGs
// from The Noun Project, tinted gray to read on the light drawer (see
// graphics/License for attribution).
func (ui *UI) buildSettingsMenu() {
	if ui.SettingsMenu.AsControl() == Control.Nil {
		return
	}
	typesNode := ui.SettingsMenu.AsNode().GetNode("SettingsTypes")
	types, ok := Object.As[VBoxContainer.Instance](typesNode)
	if !ok {
		return
	}

	row := HBoxContainer.New()
	row.AsControl().SetMouseFilter(Control.MouseFilterPass)

	const iconSize = 28
	makeIcon := func(path string) TextureRect.Instance {
		rect := TextureRect.New()
		if tex := LoadSync[Texture2D.Instance](path); tex != Texture2D.Nil {
			rect.SetTexture(tex)
		}
		rect.SetExpandMode(TextureRect.ExpandIgnoreSize)
		rect.SetStretchMode(TextureRect.StretchKeepAspectCentered)
		rect.AsControl().SetCustomMinimumSize(Vector2.New(iconSize, iconSize))
		rect.AsControl().SetMouseFilter(Control.MouseFilterIgnore)
		return rect
	}

	low := makeIcon("res://ui/quality_low.png")   // toaster
	high := makeIcon("res://ui/quality_high.png") // sports car

	slider := HSlider.New()
	slider.AsRange().SetMinValue(0)
	slider.AsRange().SetMaxValue(graphicsQualitySteps - 1)
	slider.AsRange().SetStep(1)
	slider.AsRange().SetValue(Float.X(defaultGraphicsQuality))
	slider.AsControl().SetSizeFlagsHorizontal(Control.SizeExpandFill)
	slider.AsControl().SetCustomMinimumSize(Vector2.New(160, iconSize))
	Range.Instance(slider.AsRange()).OnValueChanged(func(value Float.X) {
		GraphicsQuality(int(value)).Apply(ui.AsNode())
	})

	row.AsNode().AddChild(low.AsNode())
	row.AsNode().AddChild(slider.AsNode())
	row.AsNode().AddChild(high.AsNode())
	types.AsNode().AddChild(row.AsNode())

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
	want := ui.client.CanDeleteSelection()
	if ui.CloudControl != nil {
		if ui.CloudControl.GizmoTypes.Delete.AsCanvasItem().Visible() != want {
			ui.CloudControl.GizmoTypes.Delete.AsCanvasItem().SetVisible(want)
		}
		if ui.CloudControl.GizmoTypes.Duplicate.AsCanvasItem().Visible() != want {
			ui.CloudControl.GizmoTypes.Duplicate.AsCanvasItem().SetVisible(want)
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
					ui.client.Redo()
				} else {
					ui.client.Undo()
				}
			}
			if event.Keycode() == Input.KeyY && Input.IsKeyPressed(Input.KeyCtrl) && ui.client != nil {
				ui.client.Redo()
			}
		}
	}
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
	// Scale the Settings rollout with the same factor as the Toolbar so
	// it keeps the triangle's width across display sizes.
	if ui.SettingsMenu.AsControl() != Control.Nil {
		ui.scaleDefault(ui.SettingsMenu.AsControl())
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
