package internal

import (
	"fmt"
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

func (v *Vulture) WorldSpaceToVultureCell(world gd.Vector3) gd.Vector2i {
	area := v.VultureSpaceToWorldSpace(v.WorldSpaceToVultureSpace(world))
	area = world.Sub(area).Abs()
	return gd.Vector2i{int32(area[0]), int32(area[2])}
}

func (v *Vulture) worldSpaceToVultureCell(world gd.Vector3) vulture.Cell {
	area := v.VultureSpaceToWorldSpace(v.WorldSpaceToVultureSpace(world))
	area = world.Sub(area).Abs()
	ivec := gd.Vector2i{int32(area[0]), int32(area[2])}
	fmt.Println("worldSpaceToVultureCell", world, ivec, vulture.Cell(ivec[0]+ivec[1]*16))
	return vulture.Cell(ivec[0] + ivec[1]*16)
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

func (v *Vulture) worldSpaceToVultureRegion(world gd.Vector3) vulture.Region {
	//world = world.Round().Addf(0.5)
	flat := gd.Vector2{world[0], world[2]}
	if flat[0] < 0 {
		flat[0] -= 16
	}
	if flat[1] < 0 {
		flat[1] -= 16
	}
	flat = flat.Divf(16)
	return vulture.Region{int8(flat[0]), int8(flat[1])}
}

func (v *Vulture) vultureCellToWorld(region vulture.Region, cell vulture.Cell) gd.Vector3 {
	world := v.VultureSpaceToWorldSpace(gd.Vector2i{int32(region[0]), int32(region[1])})
	return world.Add(gd.Vector3{float32(cell % 16), 0, float32(cell / 16)})
}

// bump is a x/y offset within a cell
func (v *Vulture) worldSpaceToVultureBump(world gd.Vector3) uint8 {
	region := v.worldSpaceToVultureRegion(world)
	area := v.vultureCellToWorld(region, v.worldSpaceToVultureCell(world))
	area = world.Sub(area).Abs()
	return uint8(area[0] + area[2]*16)
}

func (v *Vulture) vultureToWorld(region vulture.Region, cell vulture.Cell, bump uint8) gd.Vector3 {
	world := v.vultureCellToWorld(region, cell)
	return world.Add(gd.Vector3{float32(bump%16) / 16, 0, float32(bump/16) / 16})
}

func (v *Vulture) worldToVulture(world gd.Vector3) (region vulture.Region, cell vulture.Cell, bump uint8) {
	region = v.worldSpaceToVultureRegion(world)
	cell = v.worldSpaceToVultureCell(world)
	_, a := math.Modf(float64(world[0]))
	_, b := math.Modf(float64(world[2]))
	bump = uint8(a*16) + uint8(b*16)*16
	return
}
