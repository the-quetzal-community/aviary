package internal

import (
	"fmt"
	"slices"
	"strings"
	"sync/atomic"

	"graphics.gd/classdb"
	"graphics.gd/classdb/Button"
	"graphics.gd/classdb/Control"
	"graphics.gd/classdb/DirAccess"
	"graphics.gd/classdb/DisplayServer"
	"graphics.gd/classdb/FileAccess"
	"graphics.gd/classdb/InputEvent"
	"graphics.gd/classdb/InputEventMouseMotion"
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/OS"
	"graphics.gd/classdb/PropertyTweener"
	"graphics.gd/classdb/Resource"
	"graphics.gd/classdb/SceneTree"
	"graphics.gd/classdb/TabContainer"
	"graphics.gd/classdb/Texture2D"
	"graphics.gd/classdb/TextureButton"
	"graphics.gd/classdb/Tween"
	"graphics.gd/classdb/Viewport"
	"graphics.gd/classdb/Window"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Path"
	"graphics.gd/variant/Signal"
	"graphics.gd/variant/String"
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

	Editor TabContainer.Instance

	ExpansionIndicator Button.Instance

	CloudControl  *CloudControl
	ThemeSelector *ThemeSelector

	Cloudy *FlightPlanner

	themes         []string
	gridContainers []*GridFlowContainer

	drawExpanded  atomic.Bool
	drawExpansion Float.X

	locked bool
	queued func()

	client *Client

	lastDisplay Vector2i.XY
}

var categories = []string{
	"terrain",
	"texture",
	"foliage",
	"mineral",
	"shelter",
	"citizen",
	"trinket",
	"critter",
	"special",
	// "pathway"
	// "fencing"
	// "vehicle"
	// "polygon"
}

func (ui *UI) Setup() {
	ui.Cloudy.client = ui.client
	ui.CloudControl.client = ui.client
	ui.CloudControl.Setup()
}

func (ui *UI) UnhandledInput(event InputEvent.Instance) {
	if ui.drawExpanded.Load() && Object.Is[InputEventMouseMotion.Instance](event) {
		height := DisplayServer.WindowGetSize(0).Y
		if ui.Editor.AsCanvasItem().GetGlobalMousePosition().Y < Float.X(height)*0.3 {
			ui.closeDrawer()
		}
	}
}

func (ui *UI) Ready() {
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
	ui.onThemeSelected(0)
	ui.ThemeSelector.ThemeSelected.Call(ui.onThemeSelected)
	ui.Editor.GetTabBar().AsControl().SetMouseFilter(Control.MouseFilterStop)
	ui.ExpansionIndicator.AsControl().SetMouseFilter(Control.MouseFilterPass)
	ui.ExpansionIndicator.AsBaseButton().SetToggleMode(true)
	ui.ExpansionIndicator.AsBaseButton().AsControl().OnMouseEntered(ui.openDrawer)
	ui.CloudControl.HBoxContainer.Cloud.AsBaseButton().OnPressed(func() {
		if !ui.client.isOnline() {
			OS.ShellOpen("https://the.quetzal.community/aviary/connection?id=" + UserState.Device)
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

func (ui *UI) Close() {
	ui.closeDrawer()
}

func (ui *UI) openDrawer() {
	if ui.locked {
		ui.queued = ui.openDrawer
		return
	}
	if !ui.drawExpanded.CompareAndSwap(false, true) {
		return
	}
	ui.locked = true
	for _, container := range ui.gridContainers {
		container.scroll_lock = false
	}
	ui.client.scroll_lock = true
	window_size := DisplayServer.WindowGetSize(0)
	scale_factor := ui.Editor.AsControl().Scale().Y
	current_eff_height := ui.Editor.AsControl().Size().Y * scale_factor
	var amount Float.X = -(Float.X(window_size.Y) - current_eff_height) * 0.8
	move := Vector2.New(ui.Editor.AsControl().Position().X, ui.Editor.AsControl().Position().Y+amount)
	grow := Vector2.New(ui.Editor.AsControl().Size().X, ui.Editor.AsControl().Size().Y-(amount/scale_factor))
	tween := SceneTree.Get(ui.Editor.AsNode()).CreateTween()
	PropertyTweener.Make(tween, ui.Editor.AsControl().AsObject(), "size", grow, 0.1).SetEase(Tween.EaseOut)
	PropertyTweener.Make(SceneTree.Get(ui.Editor.AsNode()).CreateTween(), ui.Editor.AsControl().AsObject(), "position", move, 0.1).SetEase(Tween.EaseOut)
	tween.OnFinished(func() {
		ui.locked = false
		if ui.queued != nil {
			queued := ui.queued
			ui.queued = nil
			queued()
		}
	})
	ui.ExpansionIndicator.AsCanvasItem().SetVisible(false)
	// Remove ui.drawExpansion = amount (no longer needed)
}

func (ui *UI) closeDrawer() {
	if ui.locked {
		ui.queued = ui.closeDrawer
		return
	}
	if !ui.drawExpanded.CompareAndSwap(true, false) {
		return
	}
	ui.locked = true
	for _, container := range ui.gridContainers {
		container.scroll_lock = true
	}
	ui.client.scroll_lock = false
	window_size := DisplayServer.WindowGetSize(0)
	scale_factor := ui.Editor.AsControl().Scale().Y
	const base_logical_height = 360.0 // Your base collapsed logical height (adjust to 370.0 if that's intended)
	grow := Vector2.New(ui.Editor.AsControl().Size().X, base_logical_height)
	move := Vector2.New(ui.Editor.AsControl().Position().X, Float.X(window_size.Y)-(base_logical_height*scale_factor))
	tween := SceneTree.Get(ui.Editor.AsNode()).CreateTween()
	PropertyTweener.Make(tween, ui.Editor.AsControl().AsObject(), "size", grow, 0.1).SetEase(Tween.EaseOut)
	PropertyTweener.Make(SceneTree.Get(ui.Editor.AsNode()).CreateTween(), ui.Editor.AsControl().AsObject(), "position", move, 0.1).SetEase(Tween.EaseOut)
	tween.OnFinished(func() {
		ui.locked = false
		if ui.queued != nil {
			queued := ui.queued
			ui.queued = nil
			queued()
		}
	})
	ui.ExpansionIndicator.AsCanvasItem().SetVisible(true)
}

func (ui *UI) generatePreview(res Resource.Instance, size Vector2i.XY) Texture2D.Instance {
	return Texture2D.Instance{}
}

// onThemeSelected regenerates the palette picker.
func (ui *UI) onThemeSelected(idx int) {
	themes := DirAccess.Open("res://library/" + ui.themes[idx])
	if themes == DirAccess.Nil {
		return
	}
	for _, node := range ui.Editor.AsNode().GetChildren() {
		container, ok := Object.As[*GridFlowContainer](Node.Instance(node))
		if ok {
			container.AsObject()[0].Free()
		}
	}
	ui.gridContainers = ui.gridContainers[:0]
	var glb = ".glb"
	var png = ".png"
	var i int
	for name := range themes.Iter() {
		if slices.Contains(categories, name) {
			gridflow := new(GridFlowContainer)
			gridflow.AsControl().SetMouseFilter(Control.MouseFilterStop)
			gridflow.scroll_lock = true
			gridflow.AsNode().SetName(name)
			ui.Editor.AsNode().AddChild(gridflow.AsNode())
			gridflow.Scrollable.GetHScrollBar().AsControl().SetMouseFilter(Control.MouseFilterPass)
			gridflow.Scrollable.GetVScrollBar().AsControl().SetMouseFilter(Control.MouseFilterPass)
			ui.gridContainers = append(ui.gridContainers, gridflow)
			elements := gridflow.Scrollable.GridContainer
			resources := DirAccess.Open("res://library/" + ui.themes[idx] + "/" + name)
			if resources == DirAccess.Nil {
				continue
			}
			var ext = glb
			if name == "texture" {
				ext = png
			}
			for resource := range resources.Iter() {
				resource = String.TrimSuffix(resource, ".import")
				if !String.HasSuffix(resource, ext) {
					continue
				}
				var path = Path.ToResource(String.New("res://library/" + ui.themes[idx] + "/" + name + "/" + resource))
				switch ext {
				case glb:
					renamed := Path.ToResource(String.New("res://library/" + ui.themes[idx] + "/" + name + "/" + String.TrimSuffix(resource, glb) + ".png"))
					preview := Resource.Load[Texture2D.Instance](Path.ToResource(renamed))
					if preview == Texture2D.Nil {
						continue
					}
					tscn := "res://library/" + ui.themes[idx] + "/" + name + "/" + String.TrimSuffix(resource, glb) + ".tscn"
					if FileAccess.FileExists(tscn) {
						path = Path.ToResource(String.New(tscn))
					}
					ImageButton := TextureButton.New()
					ImageButton.SetTextureNormal(preview)
					ImageButton.SetIgnoreTextureSize(true)
					ImageButton.SetStretchMode(TextureButton.StretchKeepAspectCentered)
					ImageButton.AsControl().SetCustomMinimumSize(Vector2.New(256, 256))
					ImageButton.AsControl().SetMouseFilter(Control.MouseFilterStop)
					ImageButton.AsBaseButton().OnPressed(func() {
						select {
						case ui.preview <- path:
							fmt.Println(path)
							ui.closeDrawer()
						default:
						}
					})
					elements.AsNode().AddChild(ImageButton.AsNode())
				case png:
					texture := Resource.Load[Texture2D.Instance](path)
					ImageButton := TextureButton.New()
					ImageButton.SetTextureNormal(texture)
					ImageButton.SetIgnoreTextureSize(true)
					ImageButton.SetStretchMode(TextureButton.StretchKeepAspectCentered)
					ImageButton.AsControl().SetCustomMinimumSize(Vector2.New(256, 256))
					ImageButton.AsControl().SetMouseFilter(Control.MouseFilterStop)
					ImageButton.AsBaseButton().OnPressed(func() {
						select {
						case ui.texture <- path:
							ui.closeDrawer()
						default:
						}
					})
					elements.AsNode().AddChild(ImageButton.AsNode())
				}
			}
			texture := Resource.Load[Texture2D.Instance]("res://ui/" + name + ".svg")
			gridflow.Update()
			ui.Editor.SetTabIcon(i, texture)
			ui.Editor.SetTabTitle(i, "")
			i++
		}
	}
	ui.Editor.AsCanvasItem().SetVisible(i > 0)
	ui.ExpansionIndicator.AsCanvasItem().SetVisible(i > 0)
}
