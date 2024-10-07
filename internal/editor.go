package internal

import "grow.graphics/gd"

/*
Editor is the main UI for editing a space in Aviary.
*/
type Editor struct {
	gd.Class[Editor, gd.Control] `gd:"AviaryEditor"`

	Foliage gd.TextureButton
	Mineral gd.TextureButton
	Shelter gd.TextureButton
	Terrain gd.TextureButton
	Citizen gd.TextureButton
	Critter gd.TextureButton
}
