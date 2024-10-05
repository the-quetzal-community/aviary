package internal

import (
	"grow.graphics/gd"
	"the.quetzal.community/aviary/protocol/vulture"
)

type TerrainTile struct {
	gd.Class[TerrainTile, gd.StaticBody3D] `gd:"AviaryTerrainTile"`

	vulture vulture.Terrain

	Mesh gd.MeshInstance3D

	shader gd.ShaderMaterial

	uplift    float64
	uplifting bool
}

func (tile *TerrainTile) Ready() {
	tmp := tile.Temporary

	shader, ok := gd.Load[gd.Shader](tmp, "res://shader/terrain.gdshader")
	if !ok {
		return
	}
	grass, ok := gd.Load[gd.Texture2D](tmp, "res://terrain/alpine_grass.png")
	if !ok {
		return
	}

	tile.shader = *gd.Create(tile.KeepAlive, new(gd.ShaderMaterial))
	tile.shader.SetShader(shader)
	tile.shader.SetShaderParameter(tmp.StringName("albedo"), tmp.Variant(gd.Color{1, 1, 1, 1}))
	tile.shader.SetShaderParameter(tmp.StringName("uv1_scale"), tmp.Variant(gd.Vector2{1, 1}))
	tile.shader.SetShaderParameter(tmp.StringName("texture_albedo"), tmp.Variant(grass))

	plane := gd.Create(tmp, new(gd.PlaneMesh))
	plane.SetSize(gd.Vector2{16, 16})
	plane.SetSubdivideDepth(14)
	plane.SetSubdivideWidth(14)

	shape := gd.Create(tmp, new(gd.HeightMapShape3D))

	data := tmp.PackedFloat32Array()
	for range tile.vulture.Vertices {
		data.PushBack(0)
	}
	shape.SetMapDepth(17)
	shape.SetMapWidth(17)
	shape.SetMapData(data)

	owner := tile.Super().AsCollisionObject3D().CreateShapeOwner(tile.AsObject())
	tile.Super().AsCollisionObject3D().ShapeOwnerAddShape(owner, shape.AsShape3D())

	tile.Mesh.AsGeometryInstance3D().SetMaterialOverride(tile.shader.AsMaterial())
	tile.Mesh.SetMesh(plane.AsMesh())
	tile.Super().AsNode3D().SetPosition(gd.Vector3{
		float32(tile.vulture.Area[0])*16 + 8,
		0,
		float32(tile.vulture.Area[1])*16 + 8,
	})
}

func (tile *TerrainTile) Process(delta gd.Float) {
	tmp := tile.Temporary
	if tile.uplifting {
		tile.uplift += delta
		tile.shader.SetShaderParameter(tmp.StringName("height"), tmp.Variant(tile.uplift))
	}
}

func (tile *TerrainTile) InputEvent(camera gd.Camera3D, event gd.InputEvent, pos, normal gd.Vector3, shape gd.Int) {
	tmp := tile.Temporary
	if event, ok := gd.As[gd.InputEventMouseButton](tmp, event); ok {
		if event.GetButtonIndex() == gd.MouseButtonLeft {
			tile.uplifting = event.AsInputEvent().IsPressed()
			if !tile.uplifting {
				tile.uplift = 0
			}
			tile.shader.SetShaderParameter(tmp.StringName("uplift"), tmp.Variant(pos))
		}
	}
}
