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
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/OS"
	"graphics.gd/classdb/PropertyTweener"
	"graphics.gd/classdb/Resource"
	"graphics.gd/classdb/SceneTree"
	"graphics.gd/classdb/TabContainer"
	"graphics.gd/classdb/Texture2D"
	"graphics.gd/classdb/TextureButton"
	"graphics.gd/classdb/Tween"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Path"
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
	ui.Editor.GetTabBar().AsControl().SetMouseFilter(Control.MouseFilterPass)
	ui.Editor.AsControl().OnMouseExited(func() {
		height := DisplayServer.WindowGetSize(0).Y
		if ui.Editor.AsCanvasItem().GetGlobalMousePosition().Y < Float.X(height)*0.3 {
			ui.closeDrawer()
		}
	})
	ui.ExpansionIndicator.AsControl().SetMouseFilter(Control.MouseFilterPass)
	ui.ExpansionIndicator.AsBaseButton().SetToggleMode(true)
	ui.ExpansionIndicator.AsBaseButton().AsControl().OnMouseEntered(ui.openDrawer)
	ui.CloudControl.HBoxContainer.Cloud.AsBaseButton().OnPressed(func() {
		if !ui.client.isOnline() {
			OS.ShellOpen("https://the.quetzal.community/aviary/account?connection=" + OneTimeUseCode)
		} else {
			ui.Cloudy.AsCanvasItem().SetVisible(!ui.Cloudy.AsCanvasItem().Visible())
			if ui.Cloudy.AsCanvasItem().Visible() {
				ui.Cloudy.Reload()
			}
		}
	})
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
	// Expand close to the top of the screen.
	var amount Float.X = -(Float.X(window_size.Y) - 370) * 0.8
	move := Vector2.New(ui.Editor.AsControl().Position().X, ui.Editor.AsControl().Position().Y+amount)
	grow := Vector2.New(ui.Editor.AsControl().Size().X, ui.Editor.AsControl().Size().Y-amount)
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
	ui.drawExpansion = amount
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
	move := Vector2.New(ui.Editor.AsControl().Position().X, ui.Editor.AsControl().Position().Y-ui.drawExpansion)
	grow := Vector2.New(ui.Editor.AsControl().Size().X, ui.Editor.AsControl().Size().Y+ui.drawExpansion)
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
					ImageButton.AsControl().SetMouseFilter(Control.MouseFilterPass)
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
					ImageButton.AsControl().SetMouseFilter(Control.MouseFilterPass)
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
