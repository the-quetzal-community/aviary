package internal

import (
	"grow.graphics/gd"
	"the.quetzal.community/aviary/protocol/vulture"
)

// Vulture API client resource, connects to a remote server.
type Vulture struct {
	gd.Class[Vulture, gd.Resource]

	api vulture.API
}

func (v *Vulture) OnCreate() {
	v.api = vulture.New() // in-memory for now, will be replaced with a remote connection
}

func (v *Vulture) WorldSpaceToVultureCell(world gd.Vector3) gd.Vector2i {
	area := v.VultureSpaceToWorldSpace(v.WorldSpaceToVultureSpace(world))
	area = world.Sub(area).Abs()
	return gd.Vector2i{int32(area[0]), int32(area[2])}
}

func (v *Vulture) VultureSpaceToWorldSpace(vulture gd.Vector2i) gd.Vector3 {
	flat := vulture.Vector2().Mulf(16).Sub(vulture.Vector2()) // x*16-x = y
	return gd.Vector3{flat[0], 0, flat[1]}
}

func (v *Vulture) WorldSpaceToVultureSpace(world gd.Vector3) gd.Vector2i {
	return gd.Vector2i{int32(world[0]) / 15, int32(world[2]) / 15}
}
