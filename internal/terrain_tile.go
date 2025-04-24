package internal

import (
	"graphics.gd/classdb"
	"graphics.gd/classdb/ArrayMesh"
	"graphics.gd/classdb/Camera3D"
	"graphics.gd/classdb/HeightMapShape3D"
	"graphics.gd/classdb/Input"
	"graphics.gd/classdb/InputEvent"
	"graphics.gd/classdb/InputEventMouseButton"
	"graphics.gd/classdb/Mesh"
	"graphics.gd/classdb/MeshInstance3D"
	"graphics.gd/classdb/ShaderMaterial"
	"graphics.gd/classdb/StaticBody3D"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Packed"
	"graphics.gd/variant/Vector2"
	"graphics.gd/variant/Vector3"
	"the.quetzal.community/aviary/protocol/vulture"
)

type TerrainTile struct {
	classdb.Extension[TerrainTile, StaticBody3D.Instance] `gd:"AviaryTerrainTile"`

	region vulture.Region
	buffer vulture.Elements

	heightMapping map[vulture.Region][16 * 16][4]vulture.Height

	brushEvents chan<- terrainBrushEvent

	Mesh    MeshInstance3D.Instance
	Shader  ShaderMaterial.Instance
	vulture *Vulture

	shape_owner int
}

func (tile *TerrainTile) Ready() {
	tile.shape_owner = -1
	tile.Reload()
}

func (tile *TerrainTile) Reload() {
	tile.Shader.SetShaderParameter("height", 0.0)
	tile.Shader.SetShaderParameter("paint_active", false)

	var vertices = Packed.New[Vector3.XYZ]()
	vertices.Resize(16 * 16 * 6)
	var normals = Packed.New[Vector3.XYZ]()
	normals.Resize(16 * 16 * 6)
	var uvs = Packed.New[Vector2.XY]()
	uvs.Resize(16 * 16 * 6)

	var textures = Packed.New[float32]()
	textures.Resize(16 * 16 * 6 * 4)

	heights := make([]float32, 17*17)

	weights := Packed.New[float32]()
	weights.Resize(16 * 16 * 6 * 4)

	heightm := tile.heightMapping[tile.region]
	var sample [16 * 16][4]uint8
	for _, element := range tile.buffer.Iter(0) {
		if element.Type() == vulture.ElementIsPoints {
			points := element.Points()
			for i, height := range points.Height {
				index := int(points.Cell%16) + int(17*(points.Cell/16)) + int(i%2) + 17*int(i/2)
				heights[index] = float32(height) / 32
			}
			sample[points.Cell] = points.Sample
			heightm[points.Cell] = points.Height
		}
	}
	tile.heightMapping[tile.region] = heightm

	add := func(index int, cell vulture.Cell, x, y int, w1, w2, w3, w4 Float.X) {
		vertices.SetIndex(index, Vector3.XYZ{float32(x), Float.X(heights[x+y*17]), float32(y)})
		normals.SetIndex(index, Vector3.XYZ{0, 1, 0})
		uvs.SetIndex(index, Vector2.XY{Float.X(x) / 16, Float.X(y) / 16})

		// Need to blend these correctly.w
		textures.SetIndex(index*4, float32(sample[cell][0]))   // top left
		textures.SetIndex(index*4+1, float32(sample[cell][1])) // top right
		textures.SetIndex(index*4+2, float32(sample[cell][2])) // bottom left
		textures.SetIndex(index*4+3, float32(sample[cell][3])) // bottom right

		weights.SetIndex(index*4, float32(w1))
		weights.SetIndex(index*4+1, float32(w2))
		weights.SetIndex(index*4+2, float32(w3))
		weights.SetIndex(index*4+3, float32(w4))
	}
	// generate the triangle pairs of the plane mesh
	for x := 0; x < 16; x++ {
		for y := 0; y < 16; y++ {
			cell := vulture.Cell(x + 16*y)
			add(6*(x+16*y)+0, cell, x, y, 1, 0, 0, 0)     // top left
			add(6*(x+16*y)+1, cell, x+1, y, 0, 1, 0, 0)   // top right
			add(6*(x+16*y)+2, cell, x, y+1, 0, 0, 1, 0)   // bottom left
			add(6*(x+16*y)+3, cell, x+1, y, 0, 1, 0, 0)   // top right
			add(6*(x+16*y)+4, cell, x+1, y+1, 0, 0, 0, 1) // bottom right
			add(6*(x+16*y)+5, cell, x, y+1, 0, 0, 1, 0)   // bottom left
		}
	}

	shape := HeightMapShape3D.New()
	shape.SetMapDepth(17)
	shape.SetMapWidth(17)
	shape.SetMapData(heights)

	var mesh = ArrayMesh.New()
	var arrays = [Mesh.ArrayMax]any{
		Mesh.ArrayVertex:  vertices,
		Mesh.ArrayTexUv:   uvs,
		Mesh.ArrayNormal:  normals,
		Mesh.ArrayCustom0: textures,
		Mesh.ArrayCustom1: weights,
	}
	ArrayMesh.Expanded(mesh).AddSurfaceFromArrays(Mesh.PrimitiveTriangles, arrays[:], nil, nil,
		Mesh.ArrayFormatVertex|
			Mesh.ArrayFormat(Mesh.ArrayCustomRgbaFloat)<<Mesh.ArrayFormatCustom0Shift|
			Mesh.ArrayFormat(Mesh.ArrayCustomRgbaFloat)<<Mesh.ArrayFormatCustom1Shift,
	)

	// generate mesh with pre-baked heights.

	if tile.shape_owner != -1 {
		tile.Super().AsCollisionObject3D().ShapeOwnerClearShapes(tile.shape_owner)
	} else {
		tile.shape_owner = tile.Super().AsCollisionObject3D().CreateShapeOwner(tile.AsObject())
	}
	tile.Super().AsCollisionObject3D().ShapeOwnerAddShape(tile.shape_owner, shape.AsShape3D())

	tile.Mesh.AsGeometryInstance3D().SetMaterialOverride(tile.Shader.AsMaterial())
	tile.Mesh.SetMesh(mesh.AsMesh())
	tile.Mesh.AsNode3D().SetPosition(Vector3.XYZ{
		-8, 0, -8,
	})
	tile.Super().AsNode3D().SetPosition(Vector3.XYZ{
		float32(tile.region[0])*16 + 8 - 0.5,
		0,
		float32(tile.region[1])*16 + 8 - 0.5,
	})
}

func (tile *TerrainTile) InputEvent(camera Camera3D.Instance, event InputEvent.Instance, pos, normal Vector3.XYZ, shape int) {
	if event, ok := Object.As[InputEventMouseButton.Instance](event); ok && Input.IsKeyPressed(Input.KeyShift) {
		if event.ButtonIndex() == InputEventMouseButton.MouseButtonLeft {
			if event.AsInputEvent().IsPressed() {
				select {
				case tile.brushEvents <- terrainBrushEvent{
					BrushTarget: pos,
					BrushDeltaV: 2,
				}:
				default:
				}
			}
		}
		if event.ButtonIndex() == InputEventMouseButton.MouseButtonRight {
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
		select {
		case tile.brushEvents <- terrainBrushEvent{
			BrushTarget: pos,
		}:
		default:
		}
	}
}
