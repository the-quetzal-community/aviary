package internal

import (
	"context"
	"encoding/gob"
	"math"
	"os"

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

func (v *Vulture) load() {
	tmp := v.Temporary

	var regions map[vulture.Region]vulture.Elements
	file, err := os.Open("save.vult")
	if err != nil {
		tmp.Printerr(tmp.Variant(tmp.String(err.Error())))
		return
	}
	defer file.Close()
	if err := gob.NewDecoder(file).Decode(&regions); err != nil {
		tmp.Printerr(tmp.Variant(tmp.String(err.Error())))
		return
	}
	var deltas []vulture.Deltas
	for region, packed := range regions {
		deltas = append(deltas, vulture.Deltas{
			Region: region,
			Packed: packed,
		})
	}
	if err := v.api.Reform(context.TODO(), deltas); err != nil {
		tmp.Printerr(tmp.Variant(tmp.String(err.Error())))
		return
	}
}

func (v *Vulture) vultureToWorld(region vulture.Region, cell vulture.Cell, bump uint8) gd.Vector3 {
	world := gd.Vector3{float32(region[0]), 0, float32(region[1])}
	world = world.Mulf(16)
	world = world.Add(gd.Vector3{float32(cell % 16), 0, float32(cell / 16)})
	bumps := gd.Vector3{float32(bump%16) / 16, 0, float32(bump/16) / 16}
	if region[0] < 0 {
		bumps[0] = 1 - bumps[0]
	}
	if region[1] < 0 {
		bumps[2] = 1 - bumps[2]
	}
	world = world.Add(bumps)
	return world
}

// worldToVulture is the inverse of [Vulture.vultureToWorld]
func (v *Vulture) worldToVulture(world gd.Vector3) (region vulture.Region, cell vulture.Cell, bump uint8) {
	region = vulture.Region{int8(world[0] / 16), int8(world[2] / 16)}
	if world[0] < 0 {
		region[0]--
	}
	if world[2] < 0 {
		region[1]--
	}
	local := world.Sub(gd.Vector3{float32(region[0]) * 16, 0, float32(region[1]) * 16}).Abs()
	cell = vulture.Cell(int32(local[0]) + int32(local[2])*16)
	_, a := math.Modf(float64(world[0]))
	if a < 0 {
		a = 1 - a
	}
	_, b := math.Modf(float64(world[2]))
	if b < 0 {
		b = 1 - b
	}
	bump = uint8(a*16) + uint8(b*16)*16
	return
}
