package internal

import (
	"math"

	"graphics.gd/classdb/ArrayMesh"
	"graphics.gd/classdb/Camera3D"
	"graphics.gd/classdb/HeightMapShape3D"
	"graphics.gd/classdb/Image"
	"graphics.gd/classdb/Input"
	"graphics.gd/classdb/InputEvent"
	"graphics.gd/classdb/InputEventMouseButton"
	"graphics.gd/classdb/Mesh"
	"graphics.gd/classdb/MeshInstance3D"
	"graphics.gd/classdb/Resource"
	"graphics.gd/classdb/ShaderMaterial"
	"graphics.gd/classdb/StaticBody3D"
	"graphics.gd/classdb/Texture2D"
	"graphics.gd/classdb/Texture2DArray"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Packed"
	"graphics.gd/variant/Vector2"
	"graphics.gd/variant/Vector3"
)

type TerrainTile struct {
	StaticBody3D.Extension[TerrainTile] `gd:"AviaryTerrainTile"`

	brushEvents chan<- terrainBrushEvent

	Mesh        MeshInstance3D.Instance
	shader      ShaderMaterial.Instance
	side_shader ShaderMaterial.Instance

	shape_owner int
}

func (tile *TerrainTile) Ready() {
	tile.shape_owner = -1
	tile.Reload()
}

func (tile *TerrainTile) Reload() {
	grass := Resource.Load[Texture2D.Instance]("res://terrain/alpine_grass.png")
	terrains := Texture2DArray.New()
	terrains.AsImageTextureLayered().CreateFromImages([]Image.Instance{
		grass.AsTexture2D().GetImage(),
	})

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

	//heightm := tile.heightMapping[tile.region]
	var sample [16 * 16][4]uint8

	add := func(index int, cell int, x, y int, w1, w2, w3, w4 Float.X) {
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
	for x := range 16 {
		for y := range 16 {
			cell := x + 16*y
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

	// Compute min height and base
	min_h := float32(math.MaxFloat32)
	for _, h := range heights {
		if h < min_h {
			min_h = h
		}
	}
	base_h := min_h - 2
	tile_size := float32(1.0) // Adjust for texture tiling scale

	// Sides mesh data
	var vertices_side = Packed.New[Vector3.XYZ]()
	vertices_side.Resize(4 * 16 * 6)
	var normals_side = Packed.New[Vector3.XYZ]()
	normals_side.Resize(4 * 16 * 6)
	var uvs_side = Packed.New[Vector2.XY]()
	uvs_side.Resize(4 * 16 * 6)

	index_base := 0

	type sideParam struct {
		isZFixed         bool
		fixed            float32
		fixedIndex       int
		stride           int
		flippedWinding   bool
		normalAxis       int // 0 for X, 2 for Z
		negateIfPositive bool
	}

	sides := []sideParam{
		{true, 0, 0, 1, true, 2, true},      // South
		{true, 16, 16, 1, false, 2, false},  // North
		{false, 0, 0, 17, false, 0, true},   // West
		{false, 16, 16, 17, true, 0, false}, // East
	}

	for _, sp := range sides {
		for i := 0; i < 16; i++ {
			coord := i
			var h_near, h_far float32
			if sp.isZFixed {
				h_near = heights[coord+sp.fixedIndex*17]
				h_far = heights[coord+1+sp.fixedIndex*17]
			} else {
				h_near = heights[sp.fixedIndex+coord*17]
				h_far = heights[sp.fixedIndex+(coord+1)*17]
			}

			pos_near := float32(i)
			pos_far := float32(i + 1)
			var tl, tr, bl, br Vector3.XYZ
			if sp.isZFixed {
				tl = Vector3.XYZ{pos_near, h_near, sp.fixed}
				tr = Vector3.XYZ{pos_far, h_far, sp.fixed}
				bl = Vector3.XYZ{pos_near, base_h, sp.fixed}
				br = Vector3.XYZ{pos_far, base_h, sp.fixed}
			} else {
				tl = Vector3.XYZ{sp.fixed, h_near, pos_near}
				tr = Vector3.XYZ{sp.fixed, h_far, pos_far}
				bl = Vector3.XYZ{sp.fixed, base_h, pos_near}
				br = Vector3.XYZ{sp.fixed, base_h, pos_far}
			}

			var v1, v2 Vector3.XYZ
			if sp.flippedWinding {
				v1 = Vector3.Sub(tr, bl)
				v2 = Vector3.Sub(tl, bl)
			} else {
				v1 = Vector3.Sub(tl, bl)
				v2 = Vector3.Sub(tr, bl)
			}
			n := Vector3.Normalized(Vector3.Cross(v1, v2))
			nComp := Vector3.Index(n, sp.normalAxis)
			if (nComp > 0) == sp.negateIfPositive {
				n = Vector3.Inverse(n)
			}

			// Triangle 1
			vertices_side.SetIndex(index_base+0, bl)
			normals_side.SetIndex(index_base+0, n)
			uvs_side.SetIndex(index_base+0, Vector2.XY{float32(i) / tile_size, (base_h - base_h) / tile_size})
			if sp.flippedWinding {
				vertices_side.SetIndex(index_base+1, tr)
				normals_side.SetIndex(index_base+1, n)
				uvs_side.SetIndex(index_base+1, Vector2.XY{float32(i+1) / tile_size, (h_far - base_h) / tile_size})
				vertices_side.SetIndex(index_base+2, tl)
				normals_side.SetIndex(index_base+2, n)
				uvs_side.SetIndex(index_base+2, Vector2.XY{float32(i) / tile_size, (h_near - base_h) / tile_size})
			} else {
				vertices_side.SetIndex(index_base+1, tl)
				normals_side.SetIndex(index_base+1, n)
				uvs_side.SetIndex(index_base+1, Vector2.XY{float32(i) / tile_size, (h_near - base_h) / tile_size})
				vertices_side.SetIndex(index_base+2, tr)
				normals_side.SetIndex(index_base+2, n)
				uvs_side.SetIndex(index_base+2, Vector2.XY{float32(i+1) / tile_size, (h_far - base_h) / tile_size})
			}

			// Triangle 2
			vertices_side.SetIndex(index_base+3, bl)
			normals_side.SetIndex(index_base+3, n)
			uvs_side.SetIndex(index_base+3, Vector2.XY{float32(i) / tile_size, (base_h - base_h) / tile_size})
			if sp.flippedWinding {
				vertices_side.SetIndex(index_base+4, br)
				normals_side.SetIndex(index_base+4, n)
				uvs_side.SetIndex(index_base+4, Vector2.XY{float32(i+1) / tile_size, (base_h - base_h) / tile_size})
				vertices_side.SetIndex(index_base+5, tr)
				normals_side.SetIndex(index_base+5, n)
				uvs_side.SetIndex(index_base+5, Vector2.XY{float32(i+1) / tile_size, (h_far - base_h) / tile_size})
			} else {
				vertices_side.SetIndex(index_base+4, tr)
				normals_side.SetIndex(index_base+4, n)
				uvs_side.SetIndex(index_base+4, Vector2.XY{float32(i+1) / tile_size, (h_far - base_h) / tile_size})
				vertices_side.SetIndex(index_base+5, br)
				normals_side.SetIndex(index_base+5, n)
				uvs_side.SetIndex(index_base+5, Vector2.XY{float32(i+1) / tile_size, (base_h - base_h) / tile_size})
			}

			index_base += 6
		}
	}

	var arrays_side = [Mesh.ArrayMax]any{
		Mesh.ArrayVertex: vertices_side,
		Mesh.ArrayNormal: normals_side,
		Mesh.ArrayTexUv:  uvs_side,
	}
	ArrayMesh.Expanded(mesh).AddSurfaceFromArrays(Mesh.PrimitiveTriangles, arrays_side[:], nil, nil,
		Mesh.ArrayFormatVertex|Mesh.ArrayFormatNormal|Mesh.ArrayFormatTexUv,
	)

	if tile.shape_owner != -1 {
		tile.AsCollisionObject3D().ShapeOwnerClearShapes(tile.shape_owner)
	} else {
		tile.shape_owner = tile.AsCollisionObject3D().CreateShapeOwner(tile.AsObject())
	}
	tile.AsCollisionObject3D().ShapeOwnerAddShape(tile.shape_owner, shape.AsShape3D())

	tile.Mesh.SetMesh(mesh.AsMesh())
	tile.Mesh.SetSurfaceOverrideMaterial(0, tile.shader.AsMaterial())
	tile.Mesh.SetSurfaceOverrideMaterial(1, tile.side_shader.AsMaterial())
	tile.Mesh.AsNode3D().SetPosition(Vector3.XYZ{
		-8, 0, -8,
	})
	/*tile.AsNode3D().SetPosition(Vector3.XYZ{
	  float32(tile.region[0])*16 + 8 - 0.5,
	  0,
	  float32(tile.region[1])*16 + 8 - 0.5,
	  })*/
}

func (tile *TerrainTile) InputEvent(camera Camera3D.Instance, event InputEvent.Instance, pos, normal Vector3.XYZ, shape int) {
	if event, ok := Object.As[InputEventMouseButton.Instance](event); ok && Input.IsKeyPressed(Input.KeyShift) {
		if event.ButtonIndex() == Input.MouseButtonLeft {
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
		if event.ButtonIndex() == Input.MouseButtonRight {
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
