package vulture

import (
	"context"
	"errors"
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
	lookat <-chan LookAt
	vision chan<- Vision
}

func (I *refImpl) vision(ctx context.Context) (chan<- LookAt, <-chan Vision, error) {
	lookat := make(chan LookAt)
	vision := make(chan Vision)
	I.clients = append(I.clients, refClient{lookat, vision})
	return lookat, vision, nil
}

func (I *refImpl) uplift(ctx context.Context, uplift Uplift) ([16 * 16]Vertex, error) {
	if uplift.Area != (Area{0, 0}) && uplift.Cell != 1 {
		return [16 * 16]Vertex{}, errors.New("not implemented")
	}
	var vertices [16 * 16]Vertex
	for x := 0; x < 16; x++ {
		for y := 0; y < 16; y++ {
			vertices[x*16+y] = 0
		}
	}
	return vertices, nil
}
