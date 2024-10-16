package internal

import (
	"grow.graphics/gd"
	"the.quetzal.community/aviary/protocol/vulture"
)

type TerrainTile struct {
	gd.Class[TerrainTile, gd.StaticBody3D] `gd:"AviaryTerrainTile"`

	region vulture.Region
	buffer vulture.Elements

	heightMapping map[vulture.Region][16 * 16][4]vulture.Height

	brushEvents chan<- terrainBrushEvent

	Mesh    gd.MeshInstance3D
	Shader  gd.ShaderMaterial
	Vulture *Vulture

	shape_owner gd.Int
}

func (tile *TerrainTile) Ready() {
	tile.shape_owner = -1
	tile.Reload()
}

func (tile *TerrainTile) Reload() {
	tmp := tile.Temporary
	tile.Shader.SetShaderParameter(tmp.StringName("height"), tmp.Variant(0.0))
	tile.Shader.SetShaderParameter(tmp.StringName("paint_active"), tmp.Variant(false))

	var vertices = tmp.PackedVector3Array()
	vertices.Resize(16 * 16 * 6)
	var normals = tmp.PackedVector3Array()
	normals.Resize(16 * 16 * 6)
	var uvs = tmp.PackedVector2Array()
	uvs.Resize(16 * 16 * 6)

	var textures = tmp.PackedFloat32Array()
	textures.Resize(16 * 16 * 6 * 4)

	heights := tmp.PackedFloat32Array()
	heights.Resize(17 * 17)

	weights := tmp.PackedFloat32Array()
	weights.Resize(16 * 16 * 6 * 4)

	heightm := tile.heightMapping[tile.region]
	var sample [16 * 16][4]uint8
	for _, element := range tile.buffer.Iter(0) {
		if element.Type() == vulture.ElementIsPoints {
			points := element.Points()
			for i, height := range points.Height {
				index := gd.Int(points.Cell%16) + gd.Int(17*(points.Cell/16)) + gd.Int(i%2) + 17*gd.Int(i/2)
				heights.Set(gd.Int(index), gd.Float(height)/32)
			}
			sample[points.Cell] = points.Sample
			heightm[points.Cell] = points.Height
		}
	}
	tile.heightMapping[tile.region] = heightm

	add := func(index gd.Int, cell vulture.Cell, x, y gd.Int, w1, w2, w3, w4 gd.Float) {
		vertices.Set(index, gd.Vector3{float32(x), heights.Index(x + y*17), float32(y)})
		normals.Set(index, gd.Vector3{0, 1, 0})
		uvs.Set(index, gd.Vector2{float32(x) / 16, float32(y) / 16})

		// Need to blend these correctly.w
		textures.Set((index * 4), gd.Float(sample[cell][0])) // top left
		textures.Set((index*4)+1, gd.Float(sample[cell][1])) // top right
		textures.Set((index*4)+2, gd.Float(sample[cell][2])) // bottom left
		textures.Set((index*4)+3, gd.Float(sample[cell][3])) // bottom right

		weights.Set((index * 4), w1)
		weights.Set((index*4)+1, w2)
		weights.Set((index*4)+2, w3)
		weights.Set((index*4)+3, w4)
	}
	// generate the triangle pairs of the plane mesh
	for x := gd.Int(0); x < 16; x++ {
		for y := gd.Int(0); y < 16; y++ {
			cell := vulture.Cell(x + 16*y)
			add(6*(x+16*y)+0, cell, x, y, 1, 0, 0, 0)     // top left
			add(6*(x+16*y)+1, cell, x+1, y, 0, 1, 0, 0)   // top right
			add(6*(x+16*y)+2, cell, x, y+1, 0, 0, 1, 0)   // bottom left
			add(6*(x+16*y)+3, cell, x+1, y, 0, 1, 0, 0)   // top right
			add(6*(x+16*y)+4, cell, x+1, y+1, 0, 0, 0, 1) // bottom right
			add(6*(x+16*y)+5, cell, x, y+1, 0, 0, 1, 0)   // bottom left
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
	arrays.SetIndex(int64(gd.MeshArrayTexUv), tmp.Variant(uvs))
	arrays.SetIndex(int64(gd.MeshArrayNormal), tmp.Variant(normals))
	arrays.SetIndex(int64(gd.MeshArrayCustom0), tmp.Variant(textures))
	arrays.SetIndex(int64(gd.MeshArrayCustom1), tmp.Variant(weights))

	mesh.AddSurfaceFromArrays(gd.MeshPrimitiveTriangles, arrays, gd.NewArrayOf[gd.Array](tmp), tmp.Dictionary(),
		gd.MeshArrayFormatVertex|
			gd.MeshArrayFormat(gd.MeshArrayCustomRgbaFloat)<<gd.MeshArrayFormatCustom0Shift|
			gd.MeshArrayFormat(gd.MeshArrayCustomRgbaFloat)<<gd.MeshArrayFormatCustom1Shift,
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
	tile.Mesh.AsNode3D().SetPosition(gd.Vector3{
		-8, 0, -8,
	})
	tile.Super().AsNode3D().SetPosition(gd.Vector3{
		float32(tile.region[0])*16 + 8 - 0.5,
		0,
		float32(tile.region[1])*16 + 8 - 0.5,
	})
}

func (tile *TerrainTile) InputEvent(camera gd.Camera3D, event gd.InputEvent, pos, normal gd.Vector3, shape gd.Int) {
	tmp := tile.Temporary
	Input := gd.Input(tmp)
	if event, ok := gd.As[gd.InputEventMouseButton](tmp, event); ok && Input.IsKeyPressed(gd.KeyShift) {
		if event.GetButtonIndex() == gd.MouseButtonLeft {
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
		select {
		case tile.brushEvents <- terrainBrushEvent{
			BrushTarget: pos,
		}:
		default:
		}
	}
}
