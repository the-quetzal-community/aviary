package internal

import (
	"slices"

	"grow.graphics/gd"
)

/*
UI for editing a space in Aviary.
*/
type UI struct {
	gd.Class[UI, gd.Control] `gd:"AviaryUI"`
	gd.Tool

	preview chan string
	texture chan string

	Editor gd.TabContainer
	Theme  gd.OptionButton
	themes []string
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

func (ui *UI) Ready() {
	tmp := ui.Temporary

	ui.Theme.Clear()
	ui.themes = append(ui.themes, "")
	ui.Theme.AddItem(tmp.String("select a theme"), 0)

	DirAccess := gd.DirAccess.Open(gd.DirAccess{}, tmp, tmp.String("res://library"))
	if DirAccess == (gd.DirAccess{}) {
		return
	}
	for name := range DirAccess.Iter() {
		ui.themes = append(ui.themes, name.String())
		ui.Theme.AddItem(name.ToPascalCase(tmp), 0)
	}
	ui.onThemeSelected(0)
	ui.Theme.AsObject().Connect(tmp.StringName("item_selected"), tmp.Callable(ui.onThemeSelected), 0)

	/*ui.Toolkit.Buttons.Foliage.AsObject().Connect(tmp.StringName("pressed"), tmp.Callable(func() {
	select {
	case ui.preview <- "res://library/wildfire_games/foliage/acacia.glb":
	default:
	}
	}), 0)*/
}

func (ui *UI) generatePreview(span gd.Lifetime, res gd.Resource, size gd.Vector2i) gd.Texture2D {
	return gd.Texture2D{}
}

// onThemeSelected regenerates the palette picker.
func (ui *UI) onThemeSelected(idx gd.Int) {
	tmp := ui.Temporary
	themes := gd.DirAccess.Open(gd.DirAccess{}, tmp, tmp.String("res://library/"+ui.themes[idx]))
	if themes == (gd.DirAccess{}) {
		return
	}
	for _, node := range ui.Editor.AsNode().GetChildren(tmp, false).Iter() {
		node.AsObject().Free()
	}
	var glb = tmp.String(".glb")
	var png = tmp.String(".png")
	var i gd.Int
	for name := range themes.Iter() {
		sname := name.String()
		if slices.Contains(categories, sname) {
			hlayout := gd.Create(tmp, new(gd.HBoxContainer))
			hlayout.AsNode().SetName(name)
			resources := gd.DirAccess.Open(gd.DirAccess{}, tmp, tmp.String("res://library/"+ui.themes[idx]+"/"+sname))
			if resources == (gd.DirAccess{}) {
				continue
			}
			var ext = glb
			if sname == "texture" {
				ext = png
			}
			for resource := range resources.Iter() {
				if !resource.EndsWith(ext) {
					continue
				}
				var path = "res://library/" + ui.themes[idx] + "/" + sname + "/" + resource.String()
				switch ext {
				case glb:
					mesh, ok := gd.Load[gd.PackedScene](tmp, path)
					if ok {

					}
				case png:
					texture, ok := gd.Load[gd.Texture2D](tmp, path)
					if ok {
						ImageButton := gd.Create(tmp, new(gd.TextureButton))
						ImageButton.AsTextureButton().SetTextureNormal(texture)
						ImageButton.AsTextureButton().SetIgnoreTextureSize(true)
						ImageButton.AsTextureButton().SetStretchMode(gd.TextureButtonStretchKeepAspectCentered)
						ImageButton.AsControl().SetCustomMinimumSize(gd.Vector2{128, 128})
						ImageButton.AsObject().Connect(tmp.StringName("pressed"), tmp.Callable(func() {
							select {
							case ui.texture <- path:
							default:
							}
						}), 0)
						hlayout.AsNode().AddChild(ImageButton.AsNode(), false, 0)
					}
				}
			}
			texture, ok := gd.Load[gd.Texture2D](tmp, "res://ui/"+sname+".svg")
			if ok {
				ui.Editor.AsNode().AddChild(hlayout.AsNode(), false, 0)
				ui.Editor.SetTabIcon(i, texture)
				ui.Editor.SetTabTitle(i, tmp.String(""))
				i++
			}
		}
	}
	ui.Editor.AsCanvasItem().SetVisible(i > 0)
}
