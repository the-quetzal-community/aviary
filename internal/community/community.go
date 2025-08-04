package community

import (
	"graphics.gd/variant/Color"
	"graphics.gd/variant/Euler"
	"graphics.gd/variant/Vector3"
	"runtime.link/api"
)

// Log represents the operations available within a creative community space.
type Log struct {
	api.Specification

	InsertObject func(design string, initial Object)
	UpdateObject func(design string, initial Object, deltas Object)
	RemoveObject func(design string, initial Object)
}

type Design struct{}

// Region within a habitat, representing a specific area with its own terrain and creations.
type Region struct {
	Extents [2]uint8 // width and height of the region, in cells.
	Terrain Terrain  // terrain of the region, including height and texture.
	Objects []Object // designs or creations within the region.
	Walkers []Walker // walkers that can traverse the region.
}

// Terrain represents the terrain of a region, including height and texture.
type Terrain struct {
	Height []float32 // height map of the terrain, representing elevation at each point.
}

// Object represents a static creation or object placed into a region, which can be a building, structure, or another creative design.
type Object struct {
	Offset Vector3.XYZ
	Bounds Vector3.XYZ
	Angles Euler.Radians
	Colour Color.RGBA
}

func (o *Object) apply(deltas Object) {
	o.Offset = Vector3.Add(o.Offset, deltas.Offset)
	o.Bounds = Vector3.Add(o.Bounds, deltas.Bounds)
	o.Angles = Vector3.EulerRadians(Vector3.Add(o.Angles.Vector3(), deltas.Angles.Vector3()))
}

// Walker represents a dynamic entity that represents movement between two objects.
type Walker struct {
	Design uint16 // design or type of the walker.
}
