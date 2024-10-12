package main

import (
	"grow.graphics/gd"
	"grow.graphics/gd/gdextension"

	"the.quetzal.community/aviary/internal"
)

func main() {
	godot, ok := gdextension.Link()
	if !ok {
		return
	}
	gd.Register[internal.Tree](godot)
	gd.Register[internal.Rock](godot)
	gd.Register[internal.TerrainTile](godot)
	gd.Register[internal.Vulture](godot)
	gd.Register[internal.World](godot)
	gd.Register[internal.UI](godot)
	gd.Register[internal.PreviewRenderer](godot)
	gd.Register[internal.Renderer](godot)
	gd.Register[internal.Main](godot)
}
