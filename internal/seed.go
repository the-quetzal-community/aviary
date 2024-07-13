package internal

import (
	"grow.graphics/gd"
	"the.quetzal.community/aviary/toroidal"
)

type Seed struct {
	gd.Class[Seed, gd.Resource] `gd:"AviarySeed"`

	ID   gd.Int   `gd:"id"`
	Size gd.Float `gd:"size"`
	Wrap gd.Bool  `gd:"wrap"`

	Octaves     gd.Int   `gd:"octaves"`
	Scale       gd.Float `gd:"scale"`
	Persistence gd.Float `gd:"persistence"`
	Lacunarity  gd.Float `gd:"lacunarity"`

	WaterLevel gd.Float `gd:"water_level"`
}

func (seed *Seed) AsResource() gd.Resource { return *seed.Super() }

func (seed *Seed) HeightAt(pos gd.Vector2) gd.Float {
	var space = toroidal.Space{
		Seed:        int64(seed.ID),
		Size:        float64(seed.Size),
		Octaves:     int64(seed.Octaves),
		Scale:       float64(seed.Scale),
		Persistence: float64(seed.Persistence),
		Lacunarity:  float64(seed.Lacunarity),
	}
	height, _, _ := space.NoiseAt(pos.X(), pos.Y())
	return gd.Float(height)
}
