package vulture

import (
	"context"
	"errors"
	"math"
	"sync"
	"time"
)

// New returns a reference in-memory implementation of the Vulture API.
func New() API {
	var I refImpl
	I.chunks = make(map[Area]Territory)
	I.views = make(map[Area][]refView)
	I.regions = make(map[Region]*refRegion)
	I.time = time.Now
	return API{
		//Vision: I.vision,
		//Vision: I.vision,
		Uplift: I.uplift,
		Reform: I.reform,
		Events: I.events,
		//Render: I.render,
	}
}

type refImpl struct {
	mutex   sync.Mutex
	time    func() time.Time
	clients []refClient
	chunks  map[Area]Territory
	views   map[Area][]refView

	globals Global
	regions map[Region]*refRegion
}

type refRegion struct {
	period int64
	bounds [6]float32
	packed Elements
}

type refClient struct {
	events chan<- []Deltas
}

type refView struct {
	Cell Cell
	Mesh Upload
	Time Time
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

func (I *refImpl) reform(ctx context.Context, changes []Deltas) error {
	I.mutex.Lock()
	defer I.mutex.Unlock()

	for _, change := range changes {
		sum := 0
		if change.Packed != nil {
			sum++
		}
		if change.Sparse != nil {
			sum++
		}
		if change.Append != nil {
			sum++
		}
		if sum != 1 {
			return errors.New("please provide exactly one of packed, sparse or append")
		}
	}
	now := I.time()
	for i, change := range changes {
		region, ok := I.regions[change.Region]
		if !ok {
			region = &refRegion{}
			I.regions[change.Region] = region
		}
		if change.Global != nil {
			I.globals = *change.Global
		}
		future := int64(now.Sub(time.Unix(0, int64(region.period))))
		if future > math.MaxUint16 {
			I.rebase(region, future)
		}
		if change.Packed != nil {
			copy(region.packed[change.Offset:], change.Packed)
		}
		if change.Sparse != nil {
			for i, el := range change.Sparse {
				copy(region.packed[change.Offset+i:], el[:])
			}
		}
		if change.Append != nil {
			// convert to packed, so that we can broadcast change out-of-order.
			changes[i].Offset = region.packed.Len()
			region.packed = append(region.packed, change.Append...)
			changes[i].Packed = change.Append
			changes[i].Append = nil
		}
	}
	for _, client := range I.clients {
		select {
		case client.events <- changes:
		default:
			// FIXME kick
		}
	}
	return nil
}

func (I *refImpl) rebase(region *refRegion, future int64) {
	for _, el := range region.packed.Iter(0) {
		switch el.Type() {
		case ElementIsMarker:
			marker := el.Marker()
			if int64(marker.Time) > future {
				marker.Time -= Ticks(future)
			} else {
				marker.Time = 0
			}
		}
	}
	region.period += future
}

func (I *refImpl) events(ctx context.Context) (<-chan []Deltas, error) {
	I.mutex.Lock()
	defer I.mutex.Unlock()

	events := make(chan []Deltas, 10)
	client := refClient{events: events}
	I.clients = append(I.clients, client)
	return events, nil
}
