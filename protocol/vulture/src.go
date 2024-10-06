package vulture

import (
	"context"
	"sync"
)

// New returns a reference in-memory implementation of the Vulture API.
func New() API {
	var I refImpl
	I.chunks = make(map[Area]Terrain)
	return API{
		Vision: I.vision,
		Uplift: I.uplift,
	}
}

type refImpl struct {
	mutex   sync.Mutex
	clients []refClient
	chunks  map[Area]Terrain
}

type refClient struct {
	vision chan<- Vision
}

func (I *refImpl) vision(ctx context.Context) (<-chan Vision, error) {
	vision := make(chan Vision)
	I.clients = append(I.clients, refClient{vision})
	return vision, nil
}

func (I *refImpl) uplift(ctx context.Context, uplift Uplift) (Terrain, error) {
	I.mutex.Lock()
	defer I.mutex.Unlock()

	terrain, ok := I.chunks[uplift.Area]
	if !ok {
		for x := 0; x < 16; x++ {
			for y := 0; y < 16; y++ {
				terrain.Vertices[y*16+x] = 0
			}
		}
	}
	// apply requested uplift to the cell, ie. a circular
	// terrain brush with a radius of uplift.Size
	if uplift.Lift != 0 {
		for x := 0; x < 16; x++ {
			for y := 0; y < 16; y++ {
				dx := float64(x) - float64(uplift.Cell%16)
				dy := float64(y) - float64(uplift.Cell/16)
				if dx*dx+dy*dy <= float64(uplift.Size*uplift.Size) { // uplift should smoothly decrease with distance
					height := int16(float64(uplift.Lift) * (1 - (dx*dx+dy*dy)/float64(uplift.Size*uplift.Size)))
					vertex := &terrain.Vertices[y*16+x]
					vertex.SetHeight(vertex.Height() + height)
				}
			}
		}
	}
	terrain.Area = uplift.Area
	I.chunks[uplift.Area] = terrain
	return terrain, nil
}
