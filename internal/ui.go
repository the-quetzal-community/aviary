package internal

import (
	"context"
	"slices"
	"strings"

	"graphics.gd/classdb"
	"graphics.gd/classdb/Control"
	"graphics.gd/classdb/DirAccess"
	"graphics.gd/classdb/HBoxContainer"
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/OptionButton"
	"graphics.gd/classdb/Resource"
	"graphics.gd/classdb/TabContainer"
	"graphics.gd/classdb/Texture2D"
	"graphics.gd/classdb/TextureButton"
	"graphics.gd/variant/Path"
	"graphics.gd/variant/String"
	"graphics.gd/variant/Vector2"
	"graphics.gd/variant/Vector2i"
	"runtime.link/api/unix"
	"the.quetzal.community/aviary/internal/dependencies/f3d"
)

/*
UI for editing a space in Aviary.
*/
type UI struct {
	classdb.Extension[UI, Control.Instance] `gd:"AviaryUI"`
	classdb.Tool

	preview chan Path.ToResource
	texture chan Path.ToResource

	Editor TabContainer.Instance
	Theme  OptionButton.Instance
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
	ui.Theme.Clear()
	ui.themes = append(ui.themes, "")
	ui.Theme.AddItem("select a theme")

	Dir := DirAccess.Instance(DirAccess.Open("res://library"))
	if Dir == (DirAccess.Instance{}) {
		return
	}
	for name := range Dir.Iter() {
		if strings.Contains(name, ".") {
			continue
		}
		ui.themes = append(ui.themes, name)
		ui.Theme.AddItem(String.ToPascalCase(name))
	}
	ui.onThemeSelected(0)
	ui.Theme.OnItemSelected(ui.onThemeSelected)

	/*ui.Toolkit.Buttons.Foliage.AsObject().Connect(tmp.StringName("pressed"), tmp.Callable(func() {
	select {
	case ui.preview <- "res://library/wildfire_games/foliage/acacia.glb":
	default:
	}
	}), 0)*/
}

func (ui *UI) generatePreview(res Resource.Instance, size Vector2i.XY) Texture2D.Instance {
	return Texture2D.Instance{}
}

// onThemeSelected regenerates the palette picker.
func (ui *UI) onThemeSelected(idx int) {
	themes := DirAccess.Instance(DirAccess.Open("res://library/" + ui.themes[idx]))
	if themes == (DirAccess.Instance{}) {
		return
	}
	for _, node := range ui.Editor.AsNode().GetChildren() {
		container, ok := classdb.As[HBoxContainer.Instance](Node.Instance(node))
		if ok {
			HBoxContainer.Instance(container).AsObject()[0].Free()
		}
	}
	var glb = ".glb"
	var png = ".png"
	var i int
	for name := range themes.Iter() {
		if slices.Contains(categories, name) {
			hlayout := HBoxContainer.New()
			hlayout.AsNode().SetName(name)
			resources := DirAccess.Instance(DirAccess.Open("res://library/" + ui.themes[idx] + "/" + name))
			if resources == (DirAccess.Instance{}) {
				continue
			}
			var ext = glb
			if name == "texture" {
				ext = png
			}
			for resource := range resources.Iter() {
				if !String.HasSuffix(resource, ext) {
					continue
				}
				var path = Path.ToResource(String.New("res://library/" + ui.themes[idx] + "/" + name + "/" + resource))
				switch ext {
				case glb:
					renamed := Path.ToResource(String.New("res://library/" + ui.themes[idx] + "/" + name + "/" + String.TrimSuffix(resource, glb) + ".png"))
					preview := Resource.Load[Texture2D.Instance](Path.ToResource(renamed))
					if preview == Texture2D.Nil {
						f3d.Command.Run(context.Background(), unix.Path(strings.TrimPrefix(path.String(), "res://")), f3d.Options{
							Output:       unix.Path(strings.TrimPrefix(renamed.String(), "res://")),
							NoBackground: true,
							Resolution:   "128,128",
						})
						continue
					}
					ImageButton := TextureButton.New()
					ImageButton.SetTextureNormal(preview)
					ImageButton.SetIgnoreTextureSize(true)
					ImageButton.SetStretchMode(TextureButton.StretchKeepAspectCentered)
					ImageButton.AsControl().SetCustomMinimumSize(Vector2.New(128, 128))
					ImageButton.AsBaseButton().OnPressed(func() {
						select {
						case ui.preview <- path:
						default:
						}
					})
					hlayout.AsNode().AddChild(ImageButton.AsNode())
				case png:
					texture := Resource.Load[Texture2D.Instance](path)
					ImageButton := TextureButton.New()
					ImageButton.SetTextureNormal(texture)
					ImageButton.SetIgnoreTextureSize(true)
					ImageButton.SetStretchMode(TextureButton.StretchKeepAspectCentered)
					ImageButton.AsControl().SetCustomMinimumSize(Vector2.New(128, 128))
					ImageButton.AsBaseButton().OnPressed(func() {
						select {
						case ui.texture <- path:
						default:
						}
					})
					hlayout.AsNode().AddChild(ImageButton.AsNode())
				}
			}
			texture := Resource.Load[Texture2D.Instance]("res://ui/" + name + ".svg")
			ui.Editor.AsNode().AddChild(hlayout.AsNode())
			ui.Editor.SetTabIcon(i, texture)
			ui.Editor.SetTabTitle(i, "")
			i++
		}
	}
	ui.Editor.AsCanvasItem().SetVisible(i > 0)
}
