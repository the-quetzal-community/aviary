package internal

import (
	"context"
	"math"
	"time"

	"graphics.gd/classdb"
	"graphics.gd/classdb/Engine"
	"graphics.gd/classdb/Input"
	"graphics.gd/classdb/InputEvent"
	"graphics.gd/classdb/InputEventKey"
	"graphics.gd/classdb/InputEventMouseButton"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Vector3"
	"the.quetzal.community/aviary/protocol/vulture"
)

type terrainBrushEvent struct {
	BrushTarget Vector3.XYZ
	BrushDeltaV Float.X
}

func (tr *Renderer) OnCreate() {
	tr.heightMapping = make(map[vulture.Region][16 * 16][4]vulture.Height)
	tr.brushEvents = make(chan terrainBrushEvent, 100)
}

func (tr *Renderer) UnhandledInput(event InputEvent.Instance) {
	if event, ok := classdb.As[InputEventMouseButton.Instance](event); ok {
		if Input.IsKeyPressed(Input.KeyShift) {
			if event.ButtonIndex() == InputEventMouseButton.MouseButtonWheelDown {
				tr.BrushRadius -= 0.5
				if tr.BrushRadius == 0 {
					tr.BrushRadius = 0.5
				}
				tr.shader.SetShaderParameter("radius", tr.BrushRadius)
			}
			if event.ButtonIndex() == InputEventMouseButton.MouseButtonWheelUp {
				tr.BrushRadius += 0.5
				tr.shader.SetShaderParameter("radius", tr.BrushRadius)
			}
		}
		if tr.BrushActive && event.ButtonIndex() == InputEventMouseButton.MouseButtonLeft || event.ButtonIndex() == InputEventMouseButton.MouseButtonRight && event.AsInputEvent().IsReleased() {
			tr.uploadEdits(vulture.Uplift{
				Lift: int8(tr.BrushAmount * 32),
			})
		}
		if event.ButtonIndex() == InputEventMouseButton.MouseButtonLeft && tr.PaintActive {
			if event.AsInputEvent().IsReleased() {
				tr.PaintActive = false

			}
		}
	}
	if event, ok := classdb.As[InputEventKey.Instance](event); ok {
		if event.Keycode() == InputEventKey.KeyShift && event.AsInputEvent().IsPressed() {
			tr.shader.SetShaderParameter("brush_active", true)
		}
		if event.Keycode() == InputEventKey.KeyShift && event.AsInputEvent().IsReleased() {
			tr.shader.SetShaderParameter("height", 0.0)
			tr.shader.SetShaderParameter("brush_active", false)
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
		if err := tr.Vulture.api.Uplift(ctx, uplift); err != nil {
			Engine.Raise(err)
			return
		}
	}()
}

func (tr *Renderer) HeightAt(world Vector3.XYZ) Float.X {
	region, cell, _ := tr.Vulture.worldToVulture(world)
	data := tr.heightMapping[region]

	// Ensure x and z are within bounds
	x := math.Min(math.Max(float64(cell%16), 0), 15.0)
	z := math.Min(math.Max(float64(cell/16), 0), 15.0)

	// Calculate grid cell coordinates
	x0, z0 := int(x), int(z)
	x1, z1 := x0+1, z0+1

	// Clamp to avoid out-of-bounds access
	if x1 >= 16 {
		x1 = 15
	}
	if z1 >= 16 {
		z1 = 15
	}

	// Get the four corner heights
	y00 := float64(data[z0*16+x0][0])
	y10 := float64(data[z0*16+x1][0])
	y01 := float64(data[z1*16+x0][0])
	y11 := float64(data[z1*16+x1][0])

	// Fractional components for interpolation
	fx := x - float64(x0)
	fz := z - float64(z0)

	// Bilinear interpolation
	// Interpolate along x for z0 and z1
	y0 := y00*(1-fx) + y10*fx
	y1 := y01*(1-fx) + y11*fx
	// Interpolate along z
	y := y0*(1-fz) + y1*fz

	return Float.X(y / 32)
}
