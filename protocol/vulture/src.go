package vulture

import (
	"context"
	"sync"
	"time"

	"runtime.link/nix"
)

// New returns a reference in-memory implementation of the Vulture API.
func New() API {
	var I refImpl
	I.chunks = make(map[Area]Territory)
	I.views = make(map[Area][]refView)
	I.time = time.Now
	return API{
		//Vision: I.vision,
		Uplift: I.uplift,
		//Render: I.render,
	}
}

type refImpl struct {
	mutex   sync.Mutex
	time    func() time.Time
	clients []refClient
	chunks  map[Area]Territory
	views   map[Area][]refView
}

type refClient struct {
	vision chan<- Vision
}

type refView struct {
	Cell Cell
	Mesh Upload
	Time nix.Nanos
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

/*func (I *refImpl) render(ctx context.Context, render Render) error {
I.mutex.Lock()
defer I.mutex.Unlock()
views := I.views[render.Area]
view := refView{
	Cell: render.Cell,
	Mesh: render.Mesh,
	Time: nix.Nanos(I.time().UnixNano()),
}
views = append(views, view)
I.views[render.Area] = views
for _, clients := range I.clients {
	select {
	case clients.vision <- Vision{
		Period: nix.Nanos(time.Now().UnixNano()),
		View: []View{
			{
				Cell: view.Cell,
				Mesh: view.Mesh,
				Show: Ticks(0),
			},
		},
	}:
	default:
	}
}
return nil
}*/
