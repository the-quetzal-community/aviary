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
	gd.Register[internal.Seed](godot)
	gd.Register[internal.Tile](godot)
}
