package internal

import (
	"grow.graphics/gd"

	"the.quetzal.community/aviary/protocol/vulture"
)

type Main struct {
	gd.Class[Main, gd.SceneTree] `gd:"AviaryMain"`
}

func (m *Main) OnCreate() {
	root := gd.Create(m.KeepAlive, new(Root))
	root.vulture = vulture.New()
	m.Super().GetRoot(m.Temporary).AsNode().AddChild(root.Super().AsNode(), false, 0)
}
