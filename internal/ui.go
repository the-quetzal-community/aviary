package internal

import (
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"graphics.gd/classdb"
	"graphics.gd/classdb/BaseButton"
	"graphics.gd/classdb/Button"
	"graphics.gd/classdb/Control"
	"graphics.gd/classdb/DirAccess"
	"graphics.gd/classdb/DisplayServer"
	"graphics.gd/classdb/FileAccess"
	"graphics.gd/classdb/GradientTexture2D"
	"graphics.gd/classdb/GridContainer"
	"graphics.gd/classdb/HBoxContainer"
	"graphics.gd/classdb/Label"
	"graphics.gd/classdb/Material"
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/OS"
	"graphics.gd/classdb/OptionButton"
	"graphics.gd/classdb/Panel"
	"graphics.gd/classdb/PropertyTweener"
	"graphics.gd/classdb/Resource"
	"graphics.gd/classdb/SceneTree"
	"graphics.gd/classdb/Shader"
	"graphics.gd/classdb/ShaderMaterial"
	"graphics.gd/classdb/TabContainer"
	"graphics.gd/classdb/TextEdit"
	"graphics.gd/classdb/Texture2D"
	"graphics.gd/classdb/TextureButton"
	"graphics.gd/classdb/TextureRect"
	"graphics.gd/classdb/Tween"
	"graphics.gd/variant/Color"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Path"
	"graphics.gd/variant/String"
	"graphics.gd/variant/Vector2"
	"graphics.gd/variant/Vector2i"
	"the.quetzal.community/aviary/internal/ice/signalling"
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

	JoinCode struct {
		Panel.Instance

		Label       Label.Instance
		ShareButton TextureButton.Instance
	}
	sharing  bool
	joinCode chan signalling.Code

	Keypad struct {
		Panel.Instance

		TextEdit TextEdit.Instance

		Keys GridContainer.Instance
	}

	HBoxContainer struct {
		HBoxContainer.Instance

		Cloud struct {
			TextureButton.Instance

			OnlineIndicator TextureRect.Instance
		}
	}
	onlineStatus chan bool

	ExpansionIndicator Button.Instance

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

func (ui *UI) Ready() {
	ui.joinCode = make(chan signalling.Code)
	ui.onlineStatus = make(chan bool, 1)

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
	ui.JoinCode.ShareButton.AsBaseButton().OnPressed(func() {
		if !ui.sharing {
			ui.sharing = true
			var spinner = Resource.Load[Shader.Instance]("res://shader/spinner.gdshader")
			var material = ShaderMaterial.New()
			material.SetShader(spinner)
			ui.JoinCode.ShareButton.AsCanvasItem().SetMaterial(material.AsMaterial())
			go func() {
				fmt.Println("Generating join code...")
				code, err := ui.client.apiHost()
				if err != nil {
					fmt.Println("Error getting API host:", err)
					ui.joinCode <- ""
					return
				}
				ui.joinCode <- code
				time.Sleep(5 * time.Minute)
				ui.joinCode <- ""
			}()
		}
	})
	go func() {
		if err := ui.client.goOnline(); err != nil {
			fmt.Println("Error going online:", err)
			ui.onlineStatus <- false
			return
		}
		ui.onlineStatus <- true
	}()
	ui.HBoxContainer.Cloud.AsBaseButton().OnPressed(func() {
		if !ui.client.isOnline() {
			OS.ShellOpen("https://the.quetzal.community/aviary/account?connection=" + OneTimeUseCode)
		} else {
			ui.Keypad.AsCanvasItem().SetVisible(!ui.Keypad.AsCanvasItem().Visible())
		}
	})
	ui.Keypad.TextEdit.OnTextChanged(func() {
		text := ui.Keypad.TextEdit.Text()
		safe := ""
		for _, char := range text {
			if strings.ContainsRune("0123456789", char) {
				safe += string(char)
			}
		}
		if len(safe) > 6 {
			safe = safe[:6]
		}
		if text != safe {
			ui.Keypad.TextEdit.SetText(safe)
		}
	})
	keys := ui.Keypad.Keys.AsNode().GetChildren()
	for _, key := range keys {
		name := key.Name()
		switch name {
		case "X":
			Object.To[BaseButton.Instance](key).OnPressed(func() {
				text := ui.Keypad.TextEdit.Text()
				if len(text) > 0 {
					text = text[:len(text)-1]
				}
				ui.Keypad.TextEdit.SetText(text)
			})
		case ">":
			Object.To[BaseButton.Instance](key).OnPressed(func() {
				go ui.client.apiJoin(signalling.Code(ui.Keypad.TextEdit.Text()))
				ui.Keypad.AsCanvasItem().SetVisible(false)
			})
		default:
			Object.To[BaseButton.Instance](key).OnPressed(func() {
				text := ui.Keypad.TextEdit.Text()
				text += name
				ui.Keypad.TextEdit.SetText(text)
			})
		}
	}
}

func (ui *UI) Process(dt Float.X) {
	select {
	case online := <-ui.onlineStatus:
		var col = Color.X11.Green
		if !online {
			col = Color.X11.Red
		}
		tex := ui.HBoxContainer.Cloud.OnlineIndicator.Texture()
		grad := Object.To[GradientTexture2D.Instance](tex).Gradient()
		cols := grad.Colors()
		cols[0] = col
		grad.SetColors(cols)
	case code := <-ui.joinCode:
		ui.JoinCode.ShareButton.AsCanvasItem().SetMaterial(Material.Nil)
		size := ui.JoinCode.AsControl().Size()
		if code != "" {
			size.X = 184
		} else {
			size.X = 54
		}
		PropertyTweener.Make(SceneTree.Get(ui.AsNode()).CreateTween(), ui.JoinCode.AsControl().AsObject(), "size", size, 0.2).SetEase(Tween.EaseOut)
		ui.JoinCode.Label.SetText(string(code))
		ui.sharing = false
	default:
	}
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
