package f3d

import (
	"context"

	"runtime.link/api"
	"runtime.link/api/cmdl"
	"runtime.link/api/unix"
)

type API struct {
	api.Specification

	Run func(context.Context, unix.Path, Options) error `cmdl:"%[2]v %[1]v"`
}

var Command = api.Import[API](cmdl.API, "f3d", nil)

type Options struct {
	Output       unix.Path `cmdl:"--output=%s"`
	NoBackground bool      `cmdl:"--no-background"`
	Resolution   string    `cmdl:"--resolution=%s"`
}
