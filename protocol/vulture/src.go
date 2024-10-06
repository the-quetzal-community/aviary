package vulture

import (
	"context"
	"sync"
)

// New returns a reference in-memory implementation of the Vulture API.
func New() API {
	var I refImpl
	I.chunks = make(map[Area]Territory)
	return API{
		Vision: I.vision,
		Uplift: I.uplift,
	}
}

type refImpl struct {
	mutex   sync.Mutex
	clients []refClient
	chunks  map[Area]Territory
}

type refClient struct {
	vision chan<- Vision
}

func (I *refImpl) vision(ctx context.Context) (<-chan Vision, error) {
	vision := make(chan Vision)
	I.clients = append(I.clients, refClient{vision})
	return vision, nil
}

func (I *refImpl) uplift(ctx context.Context, uplift Uplift) ([]Territory, error) {
	I.mutex.Lock()
	defer I.mutex.Unlock()

	results := []Territory{}
	// apply requested uplift to the cell, ie. a circular
	// terrain brush with a radius of uplift.Size
	for X := -1; X <= 1; X++ {
		for Y := -1; Y <= 1; Y++ {
			area := Area{uplift.Area[0] + int16(X), uplift.Area[1] + int16(Y)}
			mods := false
			terrain, ok := I.chunks[area]
			if !ok {
				for x := 0; x < 16; x++ {
					for y := 0; y < 16; y++ {
						terrain.Vertices[y*16+x] = 0
					}
				}
			}
			if uplift.Lift != 0 {
				for x := 0; x < 16; x++ {
					for y := 0; y < 16; y++ {
						dx := (float64(x) + float64(X*16) - float64(X)) - (float64(uplift.Cell % 16))
						dy := (float64(y) + float64(Y*16) - float64(Y)) - (float64(uplift.Cell / 16))
						if dx*dx+dy*dy <= float64(uplift.Size*uplift.Size) { // uplift should smoothly decrease with distance
							height := int16(float64(uplift.Lift) * (1 - (dx*dx+dy*dy)/float64(uplift.Size*uplift.Size)))
							vertex := &terrain.Vertices[y*16+x]
							vertex.SetHeight(vertex.Height() + height)
							mods = true
						}
					}
				}
			}
			terrain.Area = area
			I.chunks[area] = terrain
			if mods || area == uplift.Area {
				results = append(results, terrain)
			}
		}
	}
	return results, nil
}
