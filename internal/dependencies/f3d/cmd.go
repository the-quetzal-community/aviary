package f3d

import (
	"context"

	"runtime.link/api"
	"runtime.link/api/unix"
)

type API struct {
	api.Specification

	RenderToFile func(context.Context, unix.Path, Options) error `cmdl:"--output=%s %s"`
}

type Options struct {
	NoBackground bool   `cmdl:"--no-background"`
	Resolution   string `cmdl:"--resolution=%s"`
}
