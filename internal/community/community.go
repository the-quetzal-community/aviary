package community

import "runtime.link/api"

// Log represents the operations available within a creative community space.
type Log struct {
	api.Specification

	InsertRegion func(extents [2]uint8)
	InsertDesign func(Design)
	InsertObject func(region int, object Object)
	InsertWalker func(region int, walker Walker)

	ResizeRegion func(region int, id int, extents [2]uint8)
	UpdateObject func(region int, id int, object Object)
	UpdateWalker func(region int, id int, walker Walker)

	PrintMessage func(string)
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
	Design uint16
	Offset [2]uint8
	Jitter uint8
	Angles [3]uint8
}

// Walker represents a dynamic entity that represents movement between two objects.
type Walker struct {
	Design uint16 // design or type of the walker.
}
