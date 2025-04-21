package internal

import (
	"context"
	"encoding/gob"
	"math"
	"os"

	"graphics.gd/classdb"
	"graphics.gd/classdb/Engine"
	"graphics.gd/classdb/Resource"
	"graphics.gd/variant/Vector3"
	"the.quetzal.community/aviary/protocol/vulture"
)

// Vulture API client resource, connects to a remote server.
type Vulture struct {
	classdb.Extension[Vulture, Resource.Instance]

	api vulture.API

	uploads     vulture.Upload
	name2upload map[string]vulture.Upload
	upload2name map[vulture.Upload]string
}

func (v *Vulture) OnCreate() {
	v.api = vulture.New() // in-memory for now, will be replaced with a remote connection
}

func (v *Vulture) load() {
	v.name2upload = make(map[string]vulture.Upload)
	v.upload2name = make(map[vulture.Upload]string)

	var regions map[vulture.Region]vulture.Elements
	file, err := os.Open("save.vult")
	if err != nil {
		Engine.Raise(err)
		return
	}
	defer file.Close()
	if err := gob.NewDecoder(file).Decode(&regions); err != nil {
		Engine.Raise(err)
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
		Engine.Raise(err)
		return
	}
}

func (v *Vulture) vultureToWorld(region vulture.Region, cell vulture.Cell, bump uint8) Vector3.XYZ {
	world := Vector3.New(region[0], 0, region[1])
	world = Vector3.MulX(world, 16)
	world = Vector3.Add(world, Vector3.New(int(cell%16), 0, int(cell/16)))
	bumps := Vector3.New(float32(bump%16)/16, 0, float32(bump/16)/16)
	if region[0] < 0 {
		bumps.X = 1 - bumps.X
	}
	if region[1] < 0 {
		bumps.Z = 1 - bumps.Z
	}
	world = Vector3.Add(world, bumps)
	return world
}

// worldToVulture is the inverse of [Vulture.vultureToWorld]
func (v *Vulture) worldToVulture(world Vector3.XYZ) (region vulture.Region, cell vulture.Cell, bump uint8) {
	region = vulture.Region{int8(world.X / 16), int8(world.Z / 16)}
	if world.X < 0 {
		region[0]--
	}
	if world.Z < 0 {
		region[1]--
	}
	local := Vector3.Sub(world, Vector3.New(region[0]*16, 0, region[1]*16))
	cell = vulture.Cell(int32(local.X) + int32(local.Z)*16)
	_, a := math.Modf(float64(world.X))
	_, b := math.Modf(float64(world.Z))
	if a < 0 {
		a = -a
	}
	if b < 0 {
		b = -b
	}
	bump = uint8(a*16) + uint8(b*16)*16
	return
}
