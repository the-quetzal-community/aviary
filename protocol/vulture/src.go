package vulture

import (
	"context"
)

// New returns a reference in-memory implementation of the Vulture API.
func New() API {
	var I refImpl
	return API{
		Vision: I.vision,
		Uplift: I.uplift,
	}
}

type refImpl struct {
	clients []refClient
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
	var vertices [16 * 16]Vertex
	for x := 0; x < 16; x++ {
		for y := 0; y < 16; y++ {
			vertices[x*16+y] = 0
		}
	}
	return Terrain{
		Area:     uplift.Area,
		Vertices: vertices,
	}, nil
}
