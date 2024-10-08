package internal

import (
	"context"
	"fmt"
	"math"
	"time"

	"grow.graphics/gd"
	"the.quetzal.community/aviary/protocol/vulture"
)

// TerrainRenderer is responsible for rendering [vulture.Territory]
// around a specified focal point.
type TerrainRenderer struct {
	gd.Class[TerrainTile, gd.Node3D] `gd:"AviaryTerrainRenderer"`

	// at the moment we just have a single shared shader for terrain,
	// in future, we will need to shuffle between new shaders when
	// we run out of texture samplers.
	shader gd.ShaderMaterial

	ActiveTerritory gd.Node // the territory that is currently being rendered.
	CachedTerritory gd.Node // the territory that is out of focus.
	loadedTerritory map[vulture.Area]vulture.Territory
	updateTerritory chan []vulture.Territory

	mouseOver chan gd.Vector3

	//
	// Terrain Brush parameters are used to represent modifications
	// to the terrain. Either for texturing or height map adjustments.
	//
	BrushActive bool
	BrushTarget gd.Vector3
	BrushRadius gd.Float
	BrushAmount gd.Float
	BrushDeltaV gd.Float
	brushEvents chan terrainBrushEvent

	// Vulture resource, required for the TerrainRenderer to
	// function.
	Vulture *Vulture
}

type terrainBrushEvent struct {
	BrushTarget gd.Vector3
	BrushDeltaV gd.Float
}

func (tr *TerrainRenderer) AsNode() gd.Node { return tr.Super().AsNode() }

func (tr *TerrainRenderer) OnCreate() {
	tr.loadedTerritory = make(map[vulture.Area]vulture.Territory)
	tr.updateTerritory = make(chan []vulture.Territory)
	tr.brushEvents = make(chan terrainBrushEvent, 100)
}

func (tr *TerrainRenderer) Ready() {
	tmp := tr.Temporary
	shader, ok := gd.Load[gd.Shader](tmp, "res://shader/terrain.gdshader")
	if !ok {
		return
	}
	grass, ok := gd.Load[gd.Texture2D](tmp, "res://terrain/alpine_grass.png")
	if !ok {
		return
	}
	tr.shader = *gd.Create(tr.KeepAlive, new(gd.ShaderMaterial))
	tr.shader.SetShader(shader)
	tr.shader.SetShaderParameter(tmp.StringName("albedo"), tmp.Variant(gd.Color{1, 1, 1, 1}))
	tr.shader.SetShaderParameter(tmp.StringName("uv1_scale"), tmp.Variant(gd.Vector2{1, 1}))
	tr.shader.SetShaderParameter(tmp.StringName("texture_albedo"), tmp.Variant(grass))
	tr.shader.SetShaderParameter(tmp.StringName("radius"), tmp.Variant(2.0))
	tr.shader.SetShaderParameter(tmp.StringName("height"), tmp.Variant(0.0))
	tr.BrushRadius = 2.0
}

// SetFocalPoint3D sets the focal point of the terrain renderer, this should be
// where the camera is focused on. The [TerrainRenderer] will then fetch all
// nearby [vulture.Territory] enabling it to be rendered. The point should
// be in world space.
func (tr *TerrainRenderer) SetFocalPoint3D(world gd.Vector3) {
	focal_point := tr.Vulture.WorldSpaceToVultureSpace(world)
	// we need to load all 9 neighboring areas
	for x := int32(-1); x <= 1; x++ {
		for y := int32(-1); y <= 1; y++ {
			area := vulture.Area{int16(focal_point[0] + x), int16(focal_point[1] + y)}
			if _, ok := tr.loadedTerritory[area]; ok {
				continue
			}
			tr.loadedTerritory[area] = vulture.Territory{Area: area}
			go tr.downloadArea(area)
		}
	}
}

func (tr *TerrainRenderer) downloadArea(area vulture.Area) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	updates, err := tr.Vulture.api.Uplift(ctx, vulture.Uplift{
		Area: area,
	})
	if err != nil {
		fmt.Println(err) // TODO gd should have a way to report errors to the engine.
		return
	}
	tr.updateTerritory <- updates
}

func (tr *TerrainRenderer) Process(dt gd.Float) {
	tmp := tr.Temporary
	Input := gd.Input(tmp)
	for {
		select {
		case updates := <-tr.updateTerritory:
			for _, territory := range updates {
				name := fmt.Sprintf("%dx%dy", territory.Area[0], territory.Area[1])
				existing := tr.ActiveTerritory.AsNode().GetNodeOrNull(tmp, tmp.String(name).NodePath(tmp))
				if existing == (gd.Node{}) {
					area := gd.Create(tr.KeepAlive, new(TerrainTile))
					area.territory = territory
					area.brushEvents = tr.brushEvents
					area.Shader = tr.shader
					area.Super().AsNode().SetName(tmp.String(name))
					tr.ActiveTerritory.AsNode().AddChild(area.Super().AsNode(), false, 0)
				}
				tile, ok := gd.As[*TerrainTile](tmp, existing)
				if ok {
					tile.territory = territory
					tile.Reload()
				}
				tr.loadedTerritory[territory.Area] = territory
			}
		case event := <-tr.brushEvents:
			if !Input.IsKeyPressed(gd.KeyShift) {
				tr.mouseOver <- event.BrushTarget
			} else {
				tr.BrushTarget = event.BrushTarget
				tr.BrushDeltaV = event.BrushDeltaV
				if event.BrushDeltaV != 0 {
					tr.BrushActive = true
				}
				tr.shader.SetShaderParameter(tmp.StringName("uplift"), tmp.Variant(event.BrushTarget))
			}
			continue
		default:
		}
		break
	}
	if tr.BrushActive {
		tr.BrushAmount += dt * tr.BrushDeltaV
		tr.shader.SetShaderParameter(tmp.StringName("height"), tmp.Variant(tr.BrushAmount))
	}
}

func (tr *TerrainRenderer) Input(event gd.InputEvent) {
	tmp := tr.Temporary
	Input := gd.Input(tmp)
	if event, ok := gd.As[gd.InputEventMouseButton](tmp, event); ok {
		if Input.IsKeyPressed(gd.KeyShift) {
			if event.GetButtonIndex() == gd.MouseButtonWheelUp {
				tr.BrushRadius -= 1
				tr.shader.SetShaderParameter(tmp.StringName("radius"), tmp.Variant(tr.BrushRadius))
			}
			if event.GetButtonIndex() == gd.MouseButtonWheelDown {
				tr.BrushRadius += 1
				tr.shader.SetShaderParameter(tmp.StringName("radius"), tmp.Variant(tr.BrushRadius))
			}
		}
		if tr.BrushActive && event.GetButtonIndex() == gd.MouseButtonLeft || event.GetButtonIndex() == gd.MouseButtonRight && event.AsInputEvent().IsReleased() {
			tr.uploadEdits()
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
func (tr *TerrainRenderer) uploadEdits() {
	tmp := tr.Temporary
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	area := tr.Vulture.WorldSpaceToVultureSpace(tr.BrushTarget)
	cell := tr.Vulture.WorldSpaceToVultureCell(tr.BrushTarget)
	uplift := vulture.Uplift{
		Area: vulture.Area{int16(area[0]), int16(area[1])},
		Cell: vulture.Cell(cell[1]*16 + cell[0]),
		Size: uint8(tr.BrushRadius),
		Lift: int8(tr.BrushAmount * 32),
	}
	tr.BrushActive = false
	tr.BrushAmount = 0
	tr.shader.SetShaderParameter(tmp.StringName("height"), tmp.Variant(0.0))
	go func() {
		tmp := gd.NewLifetime(tr.Temporary)
		defer tmp.End()
		updates, err := tr.Vulture.api.Uplift(ctx, uplift)
		if err != nil {
			tmp.Printerr(tmp.Variant(tmp.String(err.Error())))
			return
		}
		tr.updateTerritory <- updates
	}()
}

func (tr *TerrainRenderer) HeightAt(world gd.Vector3) gd.Float {
	space := tr.Vulture.WorldSpaceToVultureSpace(world)
	area := vulture.Area{int16(space[0]), int16(space[1])}
	cell := tr.Vulture.WorldSpaceToVultureCell(world)
	data := tr.loadedTerritory[area].Vertices

	// Ensure x and z are within bounds
	x := math.Min(math.Max(float64(cell[0]), 0), float64(16-1))
	z := math.Min(math.Max(float64(cell[1]), 0), float64(16-1))

	// Calculate grid cell coordinates
	x0, z0 := int(x), int(z)
	x1, z1 := x0+1, z0+1

	// Ensure we don't go out of bounds due to float precision
	if x1 >= 16 {
		x1 = 16 - 1
	}
	if z1 >= 16 {
		z1 = 16 - 1
	}

	// Determine which triangle we're in within the cell (assuming we're using a grid where each square is split into two triangles)
	insideTriangle := (x-float64(x0))+(z-float64(z0)) < 1.0

	var y float64

	if insideTriangle {
		// We're in the triangle that includes (x0,z0), (x1,z0), and (x0,z1)
		y00 := float64(data[z0*16+x0].Height())
		y10 := float64(data[z0*16+x1].Height())
		y01 := float64(data[z1*16+x0].Height())

		// Barycentric interpolation within the triangle
		alpha := float64(x - float64(x0))
		beta := float64(z - float64(z0))
		gamma := 1 - alpha - beta
		y = y00*gamma + y10*alpha + y01*beta

	} else {
		// We're in the other triangle that includes (x1,z1), (x1,z0), and (x0,z1)
		y11 := float64(data[z1*16+x1].Height())
		y10 := float64(data[z0*16+x1].Height())
		y01 := float64(data[z1*16+x0].Height())

		// Barycentric interpolation within this triangle
		alpha := float64(1 - (x - float64(x0)))
		beta := float64(1 - (z - float64(z0)))
		gamma := 1 - alpha - beta
		y = y11*gamma + y10*alpha + y01*beta
	}
	return y / 32
}
