package internal

import (
	"context"
	"math"
	"time"

	"grow.graphics/gd"
	"the.quetzal.community/aviary/protocol/vulture"
)

type terrainBrushEvent struct {
	BrushTarget gd.Vector3
	BrushDeltaV gd.Float
}

func (tr *Renderer) OnCreate() {
	tr.heightMapping = make(map[vulture.Region][16 * 16][4]vulture.Height)
	tr.brushEvents = make(chan terrainBrushEvent, 100)
}

func (tr *Renderer) Input(event gd.InputEvent) {
	tmp := tr.Temporary
	Input := gd.Input(tmp)
	if event, ok := gd.As[gd.InputEventMouseButton](tmp, event); ok {
		if Input.IsKeyPressed(gd.KeyShift) {
			if event.GetButtonIndex() == gd.MouseButtonWheelDown {
				tr.BrushRadius -= 0.5
				if tr.BrushRadius == 0 {
					tr.BrushRadius = 0.5
				}
				tr.shader.SetShaderParameter(tmp.StringName("radius"), tmp.Variant(tr.BrushRadius))

			}
			if event.GetButtonIndex() == gd.MouseButtonWheelUp {
				tr.BrushRadius += 0.5
				tr.shader.SetShaderParameter(tmp.StringName("radius"), tmp.Variant(tr.BrushRadius))
			}
		}
		if tr.BrushActive && event.GetButtonIndex() == gd.MouseButtonLeft || event.GetButtonIndex() == gd.MouseButtonRight && event.AsInputEvent().IsReleased() {
			tr.uploadEdits(vulture.Uplift{
				Lift: int8(tr.BrushAmount * 32),
			})
		}
		if event.GetButtonIndex() == gd.MouseButtonLeft && tr.PaintActive {
			if event.AsInputEvent().IsReleased() {
				tr.PaintActive = false

			}
		}
	}
	if event, ok := gd.As[gd.InputEventKey](tmp, event); ok {
		if event.GetKeycode() == gd.KeyShift && event.AsInputEvent().IsPressed() {
			tr.shader.SetShaderParameter(tmp.StringName("brush_active"), tmp.Variant(true))
		}
		if event.GetKeycode() == gd.KeyShift && event.AsInputEvent().IsReleased() {
			tr.shader.SetShaderParameter(tmp.StringName("height"), tmp.Variant(0.0))
			tr.shader.SetShaderParameter(tmp.StringName("brush_active"), tmp.Variant(false))
		}
	}
}

// submit uplift via Vulture API, so that it is persisted.
func (tr *Renderer) uploadEdits(uplift vulture.Uplift) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	area, cell, _ := tr.Vulture.worldToVulture(tr.BrushTarget)
	uplift.Area = area
	uplift.Cell = cell
	uplift.Size = uint8(tr.BrushRadius)
	tr.BrushActive = false
	tr.BrushAmount = 0
	go func() {
		tmp := gd.NewLifetime(tr.Temporary)
		defer tmp.End()
		if err := tr.Vulture.api.Uplift(ctx, uplift); err != nil {
			tmp.Printerr(tmp.Variant(tmp.String(err.Error())))
			return
		}
	}()
}

func (tr *Renderer) HeightAt(world gd.Vector3) gd.Float {
	return 0
	region, cell, _ := tr.Vulture.worldToVulture(world)
	data := tr.heightMapping[region]

	// Ensure x and z are within bounds
	x := math.Min(math.Max(float64(cell%16), 0), float64(17))
	z := math.Min(math.Max(float64(cell/16), 0), float64(17))

	// Calculate grid cell coordinates
	x0, z0 := int(x), int(z)
	x1, z1 := x0+1, z0+1

	// Ensure we don't go out of bounds due to float precision
	if x1 >= 17 {
		x1 = 17 - 1
	}
	if z1 >= 17 {
		z1 = 17 - 1
	}

	// Determine which triangle we're in within the cell (assuming we're using a grid where each square is split into two triangles)
	insideTriangle := (x-float64(x0))+(z-float64(z0)) < 1.0

	var y float64

	if insideTriangle {
		// We're in the triangle that includes (x0,z0), (x1,z0), and (x0,z1)
		y00 := float64(data[z0*17+x0][0])
		y10 := float64(data[z0*17+x1][0])
		y01 := float64(data[z1*17+x0][0])

		// Barycentric interpolation within the triangle
		alpha := float64(x - float64(x0))
		beta := float64(z - float64(z0))
		gamma := 1 - alpha - beta
		y = y00*gamma + y10*alpha + y01*beta

	} else {
		// We're in the other triangle that includes (x1,z1), (x1,z0), and (x0,z1)
		y11 := float64(data[z1*17+x1][0])
		y10 := float64(data[z0*17+x1][0])
		y01 := float64(data[z1*17+x0][0])

		// Barycentric interpolation within this triangle
		alpha := float64(1 - (x - float64(x0)))
		beta := float64(1 - (z - float64(z0)))
		gamma := 1 - alpha - beta
		y = y11*gamma + y10*alpha + y01*beta
	}
	return y / 32
}
