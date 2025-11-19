package internal

import (
	"strings"

	"graphics.gd/classdb"
	"graphics.gd/classdb/Button"
	"graphics.gd/classdb/Control"
	"graphics.gd/classdb/DirAccess"
	"graphics.gd/classdb/DisplayServer"
	"graphics.gd/classdb/Input"
	"graphics.gd/classdb/InputEvent"
	"graphics.gd/classdb/InputEventKey"
	"graphics.gd/classdb/OS"
	"graphics.gd/classdb/TextureButton"
	"graphics.gd/classdb/Viewport"
	"graphics.gd/classdb/Window"
	"graphics.gd/variant/Callable"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Path"
	"graphics.gd/variant/Signal"
	"graphics.gd/variant/Vector2"
	"graphics.gd/variant/Vector2i"
)

/*
UI for editing a space in Aviary.
*/
type UI struct {
	Control.Extension[UI] `gd:"AviaryUI"`
	classdb.Tool

	preview chan Path.ToResource
	texture chan Path.ToResource

	Editor *DesignExplorer

	ExpansionIndicator Button.Instance
	EditorIndicator    *EditorIndicator

	ModeGeometry TextureButton.Instance `gd:"%ModeGeometry"`
	ModeMaterial TextureButton.Instance `gd:"%ModeMaterial"`

	CloudControl  *CloudControl
	ThemeSelector *ThemeSelector

	Cloudy *FlightPlanner

	themes      []string
	theme_index int

	client *Client

	lastDisplay Vector2i.XY

	mode Mode
}

func (ui *UI) Setup() {
	ui.Cloudy.client = ui.client
	ui.Cloudy.clientReady.Done()
	ui.CloudControl.client = ui.client
	ui.CloudControl.Setup()
	ui.EditorIndicator.client = ui.client
}

func (ui *UI) SetMode(mode Mode) {
	if ui.mode == mode {
		return
	}
	const half = 4
	switch mode {
	case ModeGeometry:
		pos := ui.ModeGeometry.AsControl().Position()
		ui.ModeGeometry.AsControl().SetPosition(Vector2.Add(pos, Vector2.New(-half, -half)))
		ui.ModeGeometry.AsControl().SetSize(Vector2.New(32, 32))

		ui.ModeMaterial.AsControl().SetSize(Vector2.New(24, 24))
		pos = ui.ModeMaterial.AsControl().Position()
		ui.ModeMaterial.AsControl().SetPosition(Vector2.Add(pos, Vector2.New(half, half)))
	case ModeMaterial:
		pos := ui.ModeMaterial.AsControl().Position()
		ui.ModeMaterial.AsControl().SetPosition(Vector2.Add(pos, Vector2.New(-half, -half)))
		ui.ModeMaterial.AsControl().SetSize(Vector2.New(32, 32))

		ui.ModeGeometry.AsControl().SetSize(Vector2.New(24, 24))
		pos = ui.ModeGeometry.AsControl().Position()
		ui.ModeGeometry.AsControl().SetPosition(Vector2.Add(pos, Vector2.New(half, half)))
	}
	ui.mode = mode
	ui.onThemeSelected(ui.theme_index)
}

func (ui *UI) Input(event InputEvent.Instance) {
	if event, ok := Object.As[InputEventKey.Instance](event); ok {
		if event.AsInputEvent().IsPressed() && !event.AsInputEvent().IsEcho() {
			if event.Keycode() == Input.KeyTab {
				ui.SetMode(!ui.mode) // toggle between [ModeGeometry] and [ModeMaterial]
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
	Callable.Defer(Callable.New(func() {
		pos := ui.ModeGeometry.AsControl().Position()
		ui.ModeGeometry.AsControl().SetPosition(Vector2.Add(pos, Vector2.New(-4, -4)))
		ui.ModeGeometry.AsControl().SetSize(Vector2.New(32, 32))
	}))

	ui.themes = append(ui.themes, "")
	Dir := DirAccess.Open("res://library")
	if Dir == (DirAccess.Instance{}) {
		return
	}
	var count int
	for name := range Dir.Iter() {
		if strings.Contains(name, ".") {
			continue
		}
		ui.themes = append(ui.themes, name)
		count++
	}
	ui.ThemeSelector.LoadThemes(ui.themes)
	ui.ThemeSelector.ThemeSelected.Call(ui.onThemeSelected)
	ui.ExpansionIndicator.AsControl().SetMouseFilter(Control.MouseFilterPass)
	ui.ExpansionIndicator.AsBaseButton().SetToggleMode(true)
	ui.ExpansionIndicator.AsBaseButton().AsControl().OnMouseEntered(ui.Editor.openDrawer)
	ui.CloudControl.HBoxContainer.Cloud.AsBaseButton().OnPressed(func() {
		if !ui.client.isOnline() {
			OS.ShellOpen("https://the.quetzal.community/aviary/together?authorise=" + UserState.Secret)
			Object.To[Window.Instance](Viewport.Get(ui.AsNode())).OnFocusEntered(func() {
				ui.Setup()
			}, Signal.OneShot)
		} else {
			ui.Cloudy.AsCanvasItem().SetVisible(!ui.Cloudy.AsCanvasItem().Visible())
			if ui.Cloudy.AsCanvasItem().Visible() {
				ui.Cloudy.Reload()
			}
		}
	})
	ui.scaling()
	ui.AsControl().OnResized(ui.scaling)
}

func (ui *UI) scaling() {
	display := DisplayServer.WindowGetSize(0)
	// If not set (first time), initialize it
	if ui.lastDisplay.X == 0 && ui.lastDisplay.Y == 0 {
		ui.lastDisplay = display
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
	ui.scale(ui.CloudControl.AsControl(), Float.X(3840), Float.X(2160), 0.5)
	ui.scale(ui.ThemeSelector.AsControl(), Float.X(3840), Float.X(2160), 0.5)
	ui.scale(ui.ExpansionIndicator.AsControl(), Float.X(3840), Float.X(2160), 0.5)
	ui.scale(ui.EditorIndicator.AsControl(), Float.X(3840), Float.X(2160), 0.5)

	// ThemeSelector needs to be centered to the top center
	theme_pos := ui.ThemeSelector.AsControl().Position()
	theme_scale := ui.ThemeSelector.AsControl().Scale()
	theme_size := ui.ThemeSelector.AsControl().Size()
	theme_pos.X = (Float.X(display.X)/2 - (theme_size.X * theme_scale.X * Float.X(len(ui.themes))))
	ui.ThemeSelector.AsControl().SetPosition(theme_pos)

	// Update last display for next resize
	ui.lastDisplay = display
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

// onThemeSelected regenerates the palette picker.
func (ui *UI) onThemeSelected(idx int) {
	ui.theme_index = idx
	ui.Editor.Refresh(ui.client.Editing, ui.themes[idx], ui.mode)
}
