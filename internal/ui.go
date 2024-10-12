package internal

import "grow.graphics/gd"

/*
UI for editing a space in Aviary.
*/
type UI struct {
	gd.Class[UI, gd.Control] `gd:"AviaryUI"`

	preview chan string
	texture chan string

	Toolkit struct {
		gd.PanelContainer

		Buttons struct {
			gd.VBoxContainer

			Foliage gd.TextureButton
			Mineral gd.TextureButton
			Shelter gd.TextureButton
			Terrain gd.TextureButton
			Texture gd.TextureButton
			Citizen gd.TextureButton
			Critter gd.TextureButton
		}
	}
}

func (ui *UI) Ready() {
	tmp := ui.Temporary
	ui.Toolkit.Buttons.Foliage.AsObject().Connect(tmp.StringName("pressed"), tmp.Callable(func() {
		select {
		case ui.preview <- "res://library/wildfire_games/foliage/acacia.glb":
		default:
		}
	}), 0)
	ui.Toolkit.Buttons.Texture.AsObject().Connect(tmp.StringName("pressed"), tmp.Callable(func() {
		select {
		case ui.texture <- "res://library/wildfire_games/texture/alpine_cliff.dds":
		default:
		}
	}), 0)
}
