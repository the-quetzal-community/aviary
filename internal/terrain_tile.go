package internal

import (
	"grow.graphics/gd"
	"the.quetzal.community/aviary/protocol/vulture"
)

type TerrainTile struct {
	gd.Class[TerrainTile, gd.StaticBody3D] `gd:"AviaryTerrainTile"`

	territory   vulture.Territory
	brushEvents chan<- terrainBrushEvent

	Mesh   gd.MeshInstance3D
	Shader gd.ShaderMaterial
}

func (tile *TerrainTile) Ready() { tile.Reload() }

func (tile *TerrainTile) Reload() {
	tmp := tile.Temporary

	var vertices = tmp.PackedVector3Array()
	var indicies = tmp.PackedInt32Array()
	var normals = tmp.PackedVector3Array()
	var uvs = tmp.PackedVector2Array()

	heights := tmp.PackedFloat32Array()
	for i, vertex := range tile.territory.Vertices {
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

	tile.Mesh.AsGeometryInstance3D().SetMaterialOverride(tile.Shader.AsMaterial())
	tile.Mesh.SetMesh(mesh.AsMesh())
	tile.Super().AsNode3D().SetPosition(gd.Vector3{
		float32(tile.territory.Area[0])*15 + 8,
		0,
		float32(tile.territory.Area[1])*15 + 8,
	})
}

func (tile *TerrainTile) InputEvent(camera gd.Camera3D, event gd.InputEvent, pos, normal gd.Vector3, shape gd.Int) {
	tmp := tile.Temporary
	Input := gd.Input(tmp)
	if event, ok := gd.As[gd.InputEventMouseButton](tmp, event); ok && Input.IsKeyPressed(gd.KeyShift) {
		pos = pos.Round()
		if event.GetButtonIndex() == gd.MouseButtonLeft {
			if event.AsInputEvent().IsPressed() {
				tile.brushEvents <- terrainBrushEvent{
					BrushTarget: pos,
					BrushDeltaV: 2,
				}
			}
		}
		if event.GetButtonIndex() == gd.MouseButtonRight {
			if event.AsInputEvent().IsPressed() {
				select {
				case tile.brushEvents <- terrainBrushEvent{
					BrushTarget: pos,
					BrushDeltaV: -2,
				}:
				default:
				}
			}
		}
	} else {
		pos = pos.Round()
		select {
		case tile.brushEvents <- terrainBrushEvent{
			BrushTarget: pos,
		}:
		default:
		}
	}
}
