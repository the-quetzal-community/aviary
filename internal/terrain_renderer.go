package internal

import (
	"fmt"

	"graphics.gd/classdb/Image"
	"graphics.gd/classdb/Input"
	"graphics.gd/classdb/InputEvent"
	"graphics.gd/classdb/InputEventKey"
	"graphics.gd/classdb/InputEventMouse"
	"graphics.gd/classdb/InputEventMouseButton"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/Resource"
	"graphics.gd/classdb/Shader"
	"graphics.gd/classdb/ShaderMaterial"
	"graphics.gd/classdb/Texture2D"
	"graphics.gd/classdb/Texture2DArray"
	"graphics.gd/variant/Color"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Path"
	"graphics.gd/variant/Vector2"
	"graphics.gd/variant/Vector3"
	"the.quetzal.community/aviary/internal/musical"
)

// TerrainRenderer is responsible for rendering and managing the terrain in the 3D environment.
type TerrainRenderer struct {
	Node3D.Extension[TerrainRenderer] `gd:"TerrainRenderer"`

	tile *TerrainTile

	mouseOver chan Vector3.XYZ

	shader        ShaderMaterial.Instance
	shader_buried ShaderMaterial.Instance

	texture chan Path.ToResource

	//
	// Terrain Brush parameters are used to represent modifications
	// to the terrain. Either for texturing or height map adjustments.
	//
	BrushDesign string
	BrushActive bool
	BrushTarget Vector3.XYZ
	BrushRadius Float.X
	BrushAmount Float.X
	BrushDeltaV Float.X
	brushEvents chan terrainBrushEvent

	PaintActive bool

	designs map[musical.Design]Texture2D.Instance

	client *Client
}

func (tr *TerrainRenderer) Ready() {
	shader := Resource.Load[Shader.Instance]("res://shader/terrain.gdshader")
	grass := Resource.Load[Texture2D.Instance]("res://terrain/alpine_grass.png")
	textures := Texture2DArray.New()
	textures.AsImageTextureLayered().CreateFromImages([]Image.Instance{
		grass.AsTexture2D().GetImage(),
	})
	tr.shader = ShaderMaterial.New()
	tr.shader.SetShader(shader)
	tr.shader.SetShaderParameter("albedo", Color.RGBA{1, 1, 1, 1})
	tr.shader.SetShaderParameter("uv1_scale", Vector2.New(8, 8))
	tr.shader.SetShaderParameter("texture_albedo", textures)
	tr.shader.SetShaderParameter("radius", 2.0)
	tr.shader.SetShaderParameter("height", 0.0)

	rock := Resource.Load[Texture2D.Instance]("res://terrain/rock.jpg")
	buried := Resource.Load[Shader.Instance]("res://shader/buried.gdshader")
	tr.shader_buried = ShaderMaterial.New()
	tr.shader_buried.SetShader(buried)
	tr.shader_buried.SetShaderParameter("texture_albedo", rock)

	tr.BrushRadius = 2.0

	tr.tile = new(TerrainTile)
	tr.tile.shader = tr.shader
	tr.tile.side_shader = tr.shader_buried
	tr.tile.brushEvents = tr.brushEvents
	tr.AsNode().AddChild(tr.tile.AsNode())
}

func (tr *TerrainRenderer) Paint(event InputEventMouse.Instance) {
	design, ok := tr.client.loaded[tr.BrushDesign]
	if !ok {
		tr.client.design_ids++
		tr.client.space.Import(musical.Import{
			Design: musical.Design{
				Author: tr.client.id,
				Number: tr.client.design_ids,
			},
			Import: tr.BrushDesign,
		})
	}
	tr.client.space.Sculpt(musical.Sculpt{
		Author: tr.client.id,
		Target: tr.BrushTarget,
		Radius: tr.BrushRadius,
		Amount: tr.BrushAmount,
		Design: design,
	})
	tr.shader.SetShaderParameter("paint_active", false)
	tr.PaintActive = false
}

func (vr *TerrainRenderer) Process(dt Float.X) {
	for {
		select {
		case res := <-vr.texture:
			texture := Resource.Load[Texture2D.Instance](res)
			vr.BrushDesign = res.String()
			vr.shader.SetShaderParameter("paint_texture", texture)
			vr.shader.SetShaderParameter("paint_active", true)
			vr.PaintActive = true
		case event := <-vr.brushEvents:
			vr.mouseOver <- event.BrushTarget
			vr.BrushTarget = Vector3.Round(event.BrushTarget)
			vr.shader.SetShaderParameter("uplift", event.BrushTarget)
			if vr.PaintActive && Input.IsMouseButtonPressed(Input.MouseButtonLeft) {
				vr.BrushTarget = Vector3.Round(event.BrushTarget)
				vr.shader.SetShaderParameter("uplift", event.BrushTarget)
				vr.client.space.Sculpt(musical.Sculpt{
					Author: vr.client.id,
					Target: event.BrushTarget,
					Radius: vr.BrushRadius,
					Amount: event.BrushDeltaV,
					Commit: true,
				})
			} else if !Input.IsKeyPressed(Input.KeyShift) {

			} else {
				event.BrushTarget = Vector3.Round(event.BrushTarget)
				vr.BrushTarget = event.BrushTarget
				vr.BrushDeltaV = event.BrushDeltaV
				if event.BrushDeltaV != 0 {
					vr.BrushActive = true
				}
				vr.shader.SetShaderParameter("uplift", Vector3.Sub(event.BrushTarget, Vector3.New(0.5, 0.5, 0.5)))
				fmt.Println("Brush at:", vr.BrushTarget, " delta:", vr.BrushDeltaV, Vector3.Sub(event.BrushTarget, Vector3.New(0.5, 0.5, 0.5)))
			}
			continue
		default:
		}
		break
	}
	if vr.BrushActive {
		vr.BrushAmount += dt * vr.BrushDeltaV
		vr.shader.SetShaderParameter("height", vr.BrushAmount)
	}
}

func (vr *TerrainRenderer) Sculpt(brush musical.Sculpt) {
	vr.tile.Sculpt(brush)
}

type terrainBrushEvent struct {
	BrushTarget Vector3.XYZ
	BrushDeltaV Float.X
}

func (tr *TerrainRenderer) OnCreate() {
	tr.brushEvents = make(chan terrainBrushEvent, 100)
}

func (tr *TerrainRenderer) UnhandledInput(event InputEvent.Instance) {
	if event, ok := Object.As[InputEventMouseButton.Instance](event); ok {
		if Input.IsKeyPressed(Input.KeyShift) {
			if event.ButtonIndex() == Input.MouseButtonWheelDown {
				tr.BrushRadius -= 0.5
				if tr.BrushRadius == 0 {
					tr.BrushRadius = 0.5
				}
				tr.shader.SetShaderParameter("radius", tr.BrushRadius)
			}
			if event.ButtonIndex() == Input.MouseButtonWheelUp {
				tr.BrushRadius += 0.5
				tr.shader.SetShaderParameter("radius", tr.BrushRadius)
			}
		}
		if tr.BrushActive && event.ButtonIndex() == Input.MouseButtonLeft || event.ButtonIndex() == Input.MouseButtonRight && event.AsInputEvent().IsReleased() {
			/*if err := tr.edits.LiftTerrain(tr.BrushTarget, tr.BrushRadius, tr.BrushAmount, 1); err != nil {
				Engine.Raise(err)
				return
			}*/
		}
		if event.ButtonIndex() == Input.MouseButtonLeft && tr.PaintActive {
			if event.AsInputEvent().IsReleased() {
				tr.PaintActive = false

			}
		}
	}
	if event, ok := Object.As[InputEventKey.Instance](event); ok {
		if event.Keycode() == Input.KeyShift && event.AsInputEvent().IsPressed() {
			tr.shader.SetShaderParameter("brush_active", true)
		}
		if event.Keycode() == Input.KeyShift && event.AsInputEvent().IsReleased() {
			tr.shader.SetShaderParameter("height", 0.0)
			tr.shader.SetShaderParameter("brush_active", false)
		}
	}
}

func (tr *TerrainRenderer) HeightAt(world Vector3.XYZ) Float.X {
	return 0
	/*
	   // Ensure x and z are within bounds
	   x := world.X
	   z := world.Z

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
	*/
}
