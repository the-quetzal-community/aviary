package internal

import (
	"graphics.gd/classdb"
	"graphics.gd/classdb/Control"
	"graphics.gd/variant/Color"
	"graphics.gd/variant/Vector2"
)

type Triangle struct {
	Control.Extension[Triangle] `icon:"ui/triangle.svg"`
	classdb.Tool

	RightAngled bool
	Color       Color.RGBA
}

func NewTriangle() *Triangle {
	return &Triangle{
		Color: Color.X11.White,
	}
}

func (tri *Triangle) Ready() {
	tri.AsCanvasItem().QueueRedraw()
}

func (tri *Triangle) Draw() {
	canvas := tri.AsCanvasItem()
	size := tri.AsControl().Size()
	points := [3]Vector2.XY{
		{X: size.X / 2, Y: 0},
		{X: size.X, Y: size.Y},
		{X: 0, Y: size.Y},
	}
	if tri.RightAngled {
		points = [3]Vector2.XY{
			{X: 0, Y: 0},
			{X: size.X, Y: 0},
			{X: 0, Y: size.Y},
		}
	}
	colors := [3]Color.RGBA{
		tri.Color, tri.Color, tri.Color,
	}
	canvas.DrawPolygon(points[:], colors[:])
}
