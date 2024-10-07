package internal

import (
	"grow.graphics/gd"
)

var categories = []string{
	"foliage",
	"mineral",
	"shelter",
	"terrain",
	"citizen",
	"critter",
}

type Main struct {
	gd.Class[Main, gd.SceneTree] `gd:"AviaryMain"`
}

func (m *Main) OnCreate() {
	root := gd.Create(m.KeepAlive, new(World))
	m.Super().GetRoot(m.Temporary).AsNode().AddChild(root.Super().AsNode(), false, 0)
}
