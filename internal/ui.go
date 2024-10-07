package internal

import "grow.graphics/gd"

/*
UI for editing a space in Aviary.
*/
type UI struct {
	gd.Class[UI, gd.Control] `gd:"AviaryUI"`

	Toolkit struct {
		gd.PanelContainer

		Buttons struct {
			gd.VBoxContainer

			Foliage gd.TextureButton
			Mineral gd.TextureButton
			Shelter gd.TextureButton
			Terrain gd.TextureButton
			Citizen gd.TextureButton
			Critter gd.TextureButton
		}
	}
}
