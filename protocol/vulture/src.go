package vulture

import (
	"context"
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

// deltas used to prepare changes to make to the space.
type deltas []Deltas

func (d *deltas) forRegion(region Region) *Deltas {
	var dt *Deltas
	for i := range *d {
		if (*d)[i].Region == region {
			dt = &(*d)[i]
			break
		}
	}
	if dt == nil {
		*d = append(*d, Deltas{Region: region})
		dt = &(*d)[len(*d)-1]
	}
	return dt
}

func (d *deltas) Write(region Region, offset Offset, element Element) {
	dt := d.forRegion(region)
	if offset == dt.Packed.Len() {
		dt.Packed.Add(element)
	} else {
		if dt.Sparse == nil {
			dt.Sparse = make(map[Offset]Element)
		}
		dt.Sparse[offset] = element
	}
}

func (d *deltas) Append(region Region, element Element) {
	dt := d.forRegion(region)
	dt.Append.Add(element)
}

func (I *refImpl) uplift(ctx context.Context, uplift Uplift) error {
	var dt deltas
	// apply requested uplift to the cell, ie. a circular
	// terrain brush with a radius of uplift.Size
	for X := -1; X <= 1; X++ {
		for Y := -1; Y <= 1; Y++ {
			var modified [16 * 16]bool
			if uplift.Lift != 0 {
				affected := func(x, y int) (Height, bool) {
					dx := x + X*16 - int(uplift.Cell%16)
					dy := y + Y*16 - int(uplift.Cell/16)
					height := Height(float64(uplift.Lift) * (1 - float64(dx*dx+dy*dy)/float64(int(uplift.Size)*int(uplift.Size))))
					return height, dx*dx+dy*dy <= int(uplift.Size)*int(uplift.Size) // uplift should smoothly decrease with distance
				}
				region := Region{uplift.Area[0] + int8(X), uplift.Area[1] + int8(Y)}
				regionData, ok := I.regions[region]
				if !ok {
					regionData = new(refRegion)
					I.regions[region] = regionData
				}
				for offset, el := range regionData.packed.Iter(0) {
					if el.Type() == ElementIsPoints {
						sample := *el.Points()
						cell := sample.Cell
						x := int(cell % 16)
						y := int(cell / 16)
						var edited bool
						for i := range sample.Height {
							height, ok := affected(x+i%2, y+i/2)
							if ok {
								edited = true
								sample.Height[i] += height
							}
						}
						if edited {
							dt.Write(region, offset, sample.Element())
							modified[y*16+x] = true
						}
					}
				}
				for x := 0; x < 16; x += 1 {
					for y := 0; y < 16; y += 1 {
						if !modified[y*16+x] {
							var heights [4]Height
							for i := range heights {
								if height, ok := affected(x+i%2, y+i/2); ok {
									heights[i] = height
								}
							}
							sample := ElementPoints{
								Cell:   Cell(y*16 + x),
								Height: heights,
							}
							if heights != [4]Height{} {
								dt.Append(region, sample.Element())
							}
						}
					}
				}
			}
		}
	}
	return I.reform(ctx, dt)
}

func (I *refImpl) reform(ctx context.Context, changes []Deltas) error {
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
		if change.Append != nil && change.Packed == nil {
			// convert to packed, so that we can broadcast change out-of-order.
			changes[i].Offset = region.packed.Len()
			changes[i].Packed = change.Append
			changes[i].Append = nil
		}
		region.packed.Apply(change)
	}
	//json.NewEncoder(os.Stdout).Encode(changes)
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
