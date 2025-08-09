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
	"graphics.gd/classdb/HBoxContainer"
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/OptionButton"
	"graphics.gd/classdb/Resource"
	"graphics.gd/classdb/TabContainer"
	"graphics.gd/classdb/Texture2D"
	"graphics.gd/classdb/TextureButton"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Path"
	"graphics.gd/variant/String"
	"graphics.gd/variant/Vector2"
	"graphics.gd/variant/Vector2i"
)

var DrawExpanded atomic.Bool
var DrawExpansion Float.X

/*
UI for editing a space in Aviary.
*/
type UI struct {
	Control.Extension[UI] `gd:"AviaryUI"`
	classdb.Tool

	preview chan Path.ToResource
	texture chan Path.ToResource

	Editor TabContainer.Instance
	Theme  OptionButton.Instance

	ExpansionIndicator Button.Instance

	CloudControl *CloudControl

	themes []string

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
	ui.CloudControl.client = ui.client
	ui.CloudControl.Setup()
}

func (ui *UI) Ready() {
	ui.Theme.Clear()
	ui.themes = append(ui.themes, "")
	ui.Theme.AddItem("select a theme")
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
		ui.Theme.AddItem(String.ToPascalCase(name))
		count++
	}
	ui.onThemeSelected(0)
	ui.Theme.OnItemSelected(ui.onThemeSelected)
	if count > 0 {
		ui.Theme.Select(count)
		ui.onThemeSelected(count)
	}
	ui.Editor.GetTabBar().AsControl().SetMouseFilter(Control.MouseFilterPass)
	ui.Editor.AsControl().OnMouseExited(func() {
		height := DisplayServer.WindowGetSize(0).Y
		if ui.Editor.AsCanvasItem().GetGlobalMousePosition().Y < Float.X(height)*0.3 {
			ui.closeDrawer()
		}
	})
	ui.ExpansionIndicator.AsControl().SetMouseFilter(Control.MouseFilterPass)
	ui.ExpansionIndicator.AsBaseButton().SetToggleMode(true)
	ui.ExpansionIndicator.AsBaseButton().AsControl().OnMouseEntered(func() {
		if !DrawExpanded.CompareAndSwap(false, true) {
			return
		}
		window_size := DisplayServer.WindowGetSize(0)
		// Expand close to the top of the screen.
		var amount Float.X = -(Float.X(window_size.Y) - 370) * 0.8
		ui.Editor.AsControl().SetPosition(Vector2.New(ui.Editor.AsControl().Position().X, ui.Editor.AsControl().Position().Y+amount))
		ui.Editor.AsControl().SetSize(Vector2.New(ui.Editor.AsControl().Size().X, ui.Editor.AsControl().Size().Y-amount))
		ui.ExpansionIndicator.AsCanvasItem().SetVisible(false)
		DrawExpansion = amount
	})
}

func (ui *UI) closeDrawer() {
	if !DrawExpanded.CompareAndSwap(true, false) {
		return
	}
	ui.Editor.AsControl().SetPosition(Vector2.New(ui.Editor.AsControl().Position().X, ui.Editor.AsControl().Position().Y-DrawExpansion))
	ui.Editor.AsControl().SetSize(Vector2.New(ui.Editor.AsControl().Size().X, ui.Editor.AsControl().Size().Y+DrawExpansion))
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
		container, ok := Object.As[HBoxContainer.Instance](Node.Instance(node))
		if ok {
			HBoxContainer.Instance(container).AsObject()[0].Free()
		}
	}
	var glb = ".glb"
	var png = ".png"
	var i int
	for name := range themes.Iter() {
		if slices.Contains(categories, name) {
			gridflow := new(GridFlowContainer)
			gridflow.AsNode().SetName(name)
			ui.Editor.AsNode().AddChild(gridflow.AsNode())
			gridflow.Scrollable.GetHScrollBar().AsControl().SetMouseFilter(Control.MouseFilterPass)
			gridflow.Scrollable.GetVScrollBar().AsControl().SetMouseFilter(Control.MouseFilterPass)
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
