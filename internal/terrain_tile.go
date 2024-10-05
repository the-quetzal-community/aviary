package internal

import (
	"grow.graphics/gd"
	"the.quetzal.community/aviary/protocol/vulture"
)

type TerrainTile struct {
	gd.Class[TerrainTile, gd.StaticBody3D] `gd:"AviaryTerrainTile"`

	vulture vulture.Terrain

	Mesh gd.MeshInstance3D

	shaders *TerrainShaderPool

	uplift    float64
	radius    float64
	uplifting float64
}

func (tile *TerrainTile) Ready() {
	tmp := tile.Temporary

	tile.radius = 1.0

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

	tile.Mesh.AsGeometryInstance3D().SetMaterialOverride(tile.shaders.GetShader().AsMaterial())
	tile.Mesh.SetMesh(plane.AsMesh())
	tile.Super().AsNode3D().SetPosition(gd.Vector3{
		float32(tile.vulture.Area[0])*16 + 8,
		0,
		float32(tile.vulture.Area[1])*16 + 8,
	})
}

func (tile *TerrainTile) Process(delta gd.Float) {
	tmp := tile.Temporary
	if tile.uplifting != 0 {
		tile.uplift += delta * tile.uplifting
		tile.shaders.GetShader().SetShaderParameter(tmp.StringName("height"), tmp.Variant(tile.uplift))
	}
}

func (tile *TerrainTile) InputEvent(camera gd.Camera3D, event gd.InputEvent, pos, normal gd.Vector3, shape gd.Int) {
	tmp := tile.Temporary
	Input := gd.Input(tmp)
	if event, ok := gd.As[gd.InputEventMouseButton](tmp, event); ok && Input.IsKeyPressed(gd.KeyShift) {
		if event.GetButtonIndex() == gd.MouseButtonLeft {
			if event.AsInputEvent().IsPressed() {
				tile.uplifting = 2
				tile.shaders.GetShader().SetShaderParameter(tmp.StringName("uplift"), tmp.Variant(pos.Round()))
			} else {
				tile.uplifting = 0
				tile.uplift = 0
			}
		}
		if event.GetButtonIndex() == gd.MouseButtonRight {
			if event.AsInputEvent().IsPressed() {
				tile.uplifting = -2
				tile.shaders.GetShader().SetShaderParameter(tmp.StringName("uplift"), tmp.Variant(pos.Round()))
			} else {
				tile.uplifting = 0
				tile.uplift = 0
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
