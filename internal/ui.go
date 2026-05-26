package internal

import (
	"fmt"

	"graphics.gd/classdb"
	"graphics.gd/classdb/Button"
	"graphics.gd/classdb/Control"
	"graphics.gd/classdb/DisplayServer"
	"graphics.gd/classdb/Input"
	"graphics.gd/classdb/InputEvent"
	"graphics.gd/classdb/InputEventKey"
	"graphics.gd/classdb/TextureButton"
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

	ModeGeometry TextureButton.Instance `gd:"%ModeGeometry"`
	ModeMaterial TextureButton.Instance `gd:"%ModeMaterial"`
	ModeDressing TextureButton.Instance `gd:"%ModeDressing"`

	CloudControl *CloudControl
	ViewSelector *ViewSelector

	Cloudy *FlightPlanner

	// TrashButton is a touch-friendly entry point for deleting the
	// current selection. Hidden whenever DeleteSelection would be a
	// no-op (no selection, or editor that does not support per-
	// selection delete).
	TrashButton TextureButton.Instance

	client *Client

	mode Mode
}

func (ui *UI) Setup() {
	ui.Cloudy.client = ui.client
	ui.Cloudy.clientReady.Done()
	ui.CloudControl.client = ui.client
	ui.CloudControl.Setup()
	ui.EditorIndicator.client = ui.client
	ui.Toolbar.Settings.AsBaseButton().OnPressed(func() {
		fmt.Println("toolbar: settings (TODO)")
	})
	ui.Toolbar.Undo.AsBaseButton().OnPressed(func() {
		ui.client.Undo()
	})
	ui.Toolbar.Redo.AsBaseButton().OnPressed(func() {
		ui.client.Redo()
	})
	ui.Toolbar.Export.AsBaseButton().OnPressed(func() {
		ui.client.Export()
	})
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
	want := ui.client.CanDeleteSelection()
	if ui.TrashButton.AsCanvasItem().Visible() != want {
		ui.TrashButton.AsCanvasItem().SetVisible(want)
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
	ui.TrashButton.AsBaseButton().OnPressed(func() {
		if ui.client != nil {
			ui.client.DeleteSelection()
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
	ui.scale(ui.ViewSelector.AsControl(), Float.X(3840), Float.X(2160), 0.5)
	ui.scale(ui.ExpansionIndicator.AsControl(), Float.X(3840), Float.X(2160), 0.5)
	ui.scale(ui.EditorIndicator.AsControl(), Float.X(3840), Float.X(2160), 0.5)
	ui.scale(ui.TrashButton.AsControl(), Float.X(3840), Float.X(2160), 0.5)
	ui.scale(ui.Toolbar.AsControl(), Float.X(3840), Float.X(2160), 0.5)

	// ViewSelector needs to be centered to the top center
	theme_pos := ui.ViewSelector.AsControl().Position()
	theme_scale := ui.ViewSelector.AsControl().Scale()
	theme_size := ui.ViewSelector.AsControl().Size()
	theme_pos.X = (Float.X(display.X)/2 - (theme_size.X * theme_scale.X * Float.X(len(ui.ViewSelector.views))))
	ui.ViewSelector.AsControl().SetPosition(theme_pos)
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
