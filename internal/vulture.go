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
	flat := gd.Vector2{world[0], world[2]}
	if flat[0] < 0 {
		flat[0] -= 16
	}
	if flat[1] < 0 {
		flat[1] -= 16
	}
	return flat.Divf(16).Vector2i()
}

func (v *Vulture) vultureCellToWorld(region vulture.Region, cell vulture.Cell) gd.Vector3 {
	world := v.VultureSpaceToWorldSpace(gd.Vector2i{int32(region[0]), int32(region[1])})
	return world.Add(gd.Vector3{float32(cell % 16), 0, float32(cell / 16)})
}
