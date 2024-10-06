package internal

import (
	"context"
	"time"

	"grow.graphics/gd"
	"the.quetzal.community/aviary/protocol/vulture"
)

type TerrainTile struct {
	gd.Class[TerrainTile, gd.StaticBody3D] `gd:"AviaryTerrainTile"`

	vulture vulture.API
	terrain vulture.Territory

	Mesh gd.MeshInstance3D

	shaders *TerrainShaderPool
	reloads chan vulture.Territory

	target    gd.Vector2
	uplift    float64
	radius    float64
	uplifting float64
}

func (tile *TerrainTile) Ready() {
	tile.reloads = make(chan vulture.Territory, 1)
	tile.radius = 2.0
	tile.reloads <- tile.terrain
}

func (tile *TerrainTile) onReload(terrain vulture.Territory) {
	tmp := tile.Temporary

	tile.terrain = terrain

	var vertices = tmp.PackedVector3Array()
	var indicies = tmp.PackedInt32Array()
	var normals = tmp.PackedVector3Array()
	var uvs = tmp.PackedVector2Array()

	heights := tmp.PackedFloat32Array()
	for i, vertex := range terrain.Vertices {
		heights.PushBack(float64(vertex.Height()) / 16)
		// calculate the position of the vertex in mesh-space
		x := float32(i%16 - 8)
		y := float32(i/16 - 8)
		vertices.PushBack(gd.Vector3{x, float32(vertex.Height()) / 32, y})
		uvs.PushBack(gd.Vector2{float32(i%16) / 16, float32(i/16) / 16})
		normals.PushBack(gd.Vector3{0, 1, 0})
	}
	// add indices for each triangle.
	for i := 0; i < 15; i++ {
		for j := 0; j < 15; j++ {
			// triangle 1
			indicies.PushBack(gd.Int(i*16 + j))
			indicies.PushBack(gd.Int(i*16 + j + 1))
			indicies.PushBack(gd.Int((i+1)*16 + j + 1))
			// triangle 2
			indicies.PushBack(gd.Int(i*16 + j))
			indicies.PushBack(gd.Int((i+1)*16 + j + 1))
			indicies.PushBack(gd.Int((i+1)*16 + j))
		}
	}
	shape := gd.Create(tmp, new(gd.HeightMapShape3D))
	shape.SetMapDepth(17)
	shape.SetMapWidth(17)
	shape.SetMapData(heights)

	var mesh = gd.Create(tmp, new(gd.ArrayMesh))
	var arrays = tmp.Array()
	arrays.Resize(int64(gd.MeshArrayMax))
	arrays.SetIndex(int64(gd.MeshArrayVertex), tmp.Variant(vertices))
	arrays.SetIndex(int64(gd.MeshArrayIndex), tmp.Variant(indicies))
	arrays.SetIndex(int64(gd.MeshArrayTexUv), tmp.Variant(uvs))
	arrays.SetIndex(int64(gd.MeshArrayNormal), tmp.Variant(normals))

	mesh.AddSurfaceFromArrays(gd.MeshPrimitiveTriangles, arrays, gd.NewArrayOf[gd.Array](tmp), tmp.Dictionary(), gd.MeshArrayFormatVertex)

	// generate mesh with pre-baked heights.

	owner := tile.Super().AsCollisionObject3D().CreateShapeOwner(tile.AsObject())
	tile.Super().AsCollisionObject3D().ShapeOwnerAddShape(owner, shape.AsShape3D())

	tile.Mesh.AsGeometryInstance3D().SetMaterialOverride(tile.shaders.GetShader().AsMaterial())
	tile.Mesh.SetMesh(mesh.AsMesh())
	tile.Super().AsNode3D().SetPosition(gd.Vector3{
		float32(tile.terrain.Area[0])*15 + 8,
		0,
		float32(tile.terrain.Area[1])*15 + 8,
	})
	tile.shaders.GetShader().SetShaderParameter(tmp.StringName("height"), tmp.Variant(0.0))
}

func (tile *TerrainTile) Process(delta gd.Float) {
	tmp := tile.Temporary
	if tile.uplifting != 0 {
		tile.uplift += delta * tile.uplifting
		tile.shaders.GetShader().SetShaderParameter(tmp.StringName("height"), tmp.Variant(tile.uplift))
	}
	select {
	case terrain := <-tile.reloads:
		tile.onReload(terrain)
	default:
	}
}

func (tile *TerrainTile) InputEvent(camera gd.Camera3D, event gd.InputEvent, pos, normal gd.Vector3, shape gd.Int) {
	tmp := tile.Temporary
	Input := gd.Input(tmp)
	if event, ok := gd.As[gd.InputEventMouseButton](tmp, event); ok && Input.IsKeyPressed(gd.KeyShift) {
		pos = pos.Round()
		if event.GetButtonIndex() == gd.MouseButtonLeft {
			if event.AsInputEvent().IsPressed() {
				tile.uplifting = 2
				tile.shaders.GetShader().SetShaderParameter(tmp.StringName("uplift"), tmp.Variant(pos))
				tile.target = gd.Vector2{pos[0], pos[2]}
			} else {
				tile.submit()
			}
		}
		if event.GetButtonIndex() == gd.MouseButtonRight {
			if event.AsInputEvent().IsPressed() {
				tile.uplifting = -2
				tile.shaders.GetShader().SetShaderParameter(tmp.StringName("uplift"), tmp.Variant(pos))
				tile.target = gd.Vector2{pos[0], pos[2]}
			} else {
				tile.submit()
			}
		}
		if event.GetButtonIndex() == gd.MouseButtonWheelUp {
			tile.radius += 1
			tile.shaders.GetShader().SetShaderParameter(tmp.StringName("radius"), tmp.Variant(tile.radius))
		}
		if event.GetButtonIndex() == gd.MouseButtonWheelDown {
			tile.radius -= 1
			tile.shaders.GetShader().SetShaderParameter(tmp.StringName("radius"), tmp.Variant(tile.radius))
		}
	}
}

// submit uplift via Vulture API, so that it is persisted.
func (tile *TerrainTile) submit() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	uplift := vulture.Uplift{
		Area: tile.terrain.Area,
		Cell: vulture.Cell((tile.target[1])*16 + tile.target[0]),
		Size: uint8(tile.radius),
		Lift: int8(tile.uplift * 32),
	}
	tile.uplifting = 0
	tile.uplift = 0
	go func() {
		tmp := gd.NewLifetime(tile.Temporary)
		defer tmp.End()
		terrain, err := tile.vulture.Uplift(ctx, uplift)
		if err != nil {
			tmp.Printerr(tmp.Variant(tmp.String(err.Error())))
			return
		}
		tile.reloads <- terrain
	}()
}
