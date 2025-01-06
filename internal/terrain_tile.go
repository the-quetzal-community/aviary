package internal

import (
	"graphics.gd/classdb"
	"graphics.gd/classdb/ArrayMesh"
	"graphics.gd/classdb/Camera3D"
	"graphics.gd/classdb/Engine"
	"graphics.gd/classdb/HeightMapShape3D"
	"graphics.gd/classdb/Input"
	"graphics.gd/classdb/InputEvent"
	"graphics.gd/classdb/InputEventMouseButton"
	"graphics.gd/classdb/Mesh"
	"graphics.gd/classdb/MeshInstance3D"
	"graphics.gd/classdb/ShaderMaterial"
	"graphics.gd/classdb/StaticBody3D"
	"graphics.gd/variant"
	"graphics.gd/variant/Array"
	"graphics.gd/variant/Dictionary"
	"graphics.gd/variant/Float"
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
	Vulture *Vulture

	shape_owner int
}

func (tile *TerrainTile) Ready() {
	tile.shape_owner = -1
	tile.Reload()
}

func (tile *TerrainTile) Reload() {
	tile.Shader.SetShaderParameter("height", 0.0)
	tile.Shader.SetShaderParameter("paint_active", false)

	var vertices = Packed.NewVector3Array()
	vertices.Resize(16 * 16 * 6)
	var normals = Packed.NewVector3Array()
	normals.Resize(16 * 16 * 6)
	var uvs = Packed.NewVector2Array()
	uvs.Resize(16 * 16 * 6)

	var textures = Packed.NewFloat32Array()
	textures.Resize(16 * 16 * 6 * 4)

	heights := make([]float32, 17*17)

	weights := Packed.NewFloat32Array()
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
		vertices.Set(Engine.Int(index), Vector3.XYZ{float32(x), Float.X(heights[x+y*17]), float32(y)})
		normals.Set(Engine.Int(index), Vector3.XYZ{0, 1, 0})
		uvs.Set(Engine.Int(index), Vector2.XY{Float.X(x) / 16, Float.X(y) / 16})

		// Need to blend these correctly.w
		textures.Set(Engine.Int(index*4), Engine.FloatX(sample[cell][0]))   // top left
		textures.Set(Engine.Int(index*4+1), Engine.FloatX(sample[cell][1])) // top right
		textures.Set(Engine.Int(index*4+2), Engine.FloatX(sample[cell][2])) // bottom left
		textures.Set(Engine.Int(index*4+3), Engine.FloatX(sample[cell][3])) // bottom right

		weights.Set(Engine.Int(index*4), Engine.FloatX(w1))
		weights.Set(Engine.Int(index*4+1), Engine.FloatX(w2))
		weights.Set(Engine.Int(index*4+2), Engine.FloatX(w3))
		weights.Set(Engine.Int(index*4+3), Engine.FloatX(w4))
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
	var arrays = Array.Empty()
	arrays.Resize(Engine.Int(Mesh.ArrayMax))
	arrays.SetIndex(Engine.Int(Mesh.ArrayVertex), variant.New(vertices))
	arrays.SetIndex(Engine.Int(Mesh.ArrayTexUv), variant.New(uvs))
	arrays.SetIndex(Engine.Int(Mesh.ArrayNormal), variant.New(normals))
	arrays.SetIndex(Engine.Int(Mesh.ArrayCustom0), variant.New(textures))
	arrays.SetIndex(Engine.Int(Mesh.ArrayCustom1), variant.New(weights))

	ArrayMesh.Advanced(mesh).AddSurfaceFromArrays(Mesh.PrimitiveTriangles, arrays, Array.Empty(), Dictionary.Empty(),
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
	if event, ok := classdb.As[InputEventMouseButton.Instance](event); ok && Input.IsKeyPressed(Input.KeyShift) {
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
