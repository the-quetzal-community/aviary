package internal

import (
	"grow.graphics/gd"
)

type Area struct {
	gd.Class[Area, gd.MeshInstance2D] `gd:"AviaryArea"`
}

func (area *Area) OnCreate() {

}
