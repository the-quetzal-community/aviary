package internal

import (
	"math"

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

func (v *Vulture) vultureToWorld(region vulture.Region, cell vulture.Cell, bump uint8) gd.Vector3 {
	world := gd.Vector3{float32(region[0]), 0, float32(region[1])}
	world = world.Mulf(15)
	world = world.Add(gd.Vector3{float32(cell % 16), 0, float32(cell / 16)})
	world = world.Add(gd.Vector3{float32(bump%16) / 16, 0, float32(bump/16) / 16})
	return world
}

// worldToVulture is the inverse of [Vulture.vultureToWorld]
func (v *Vulture) worldToVulture(world gd.Vector3) (region vulture.Region, cell vulture.Cell, bump uint8) {
	region = vulture.Region{int8(world[0] / 15), int8(world[2] / 15)}
	if world[0] < 0 {
		region[0]--
	}
	if world[2] < 0 {
		region[1]--
	}
	local := world.Sub(gd.Vector3{float32(region[0]) * 15, 0, float32(region[1]) * 15}).Abs()
	cell = vulture.Cell(int32(local[0]) + int32(local[2])*16)
	_, a := math.Modf(float64(world[0]))
	_, b := math.Modf(float64(world[2]))
	bump = uint8(a*16) + uint8(b*16)*16
	return
}
