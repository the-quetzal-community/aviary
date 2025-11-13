package internal

import (
	"path"
	"strings"

	"graphics.gd/classdb/ArrayMesh"
	"graphics.gd/classdb/Camera3D"
	"graphics.gd/classdb/CollisionShape3D"
	"graphics.gd/classdb/FileAccess"
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
	"graphics.gd/variant/Callable"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Vector2"
	"graphics.gd/variant/Vector3"
	"the.quetzal.community/aviary/internal/musical"
)

type TerrainTile struct {
	StaticBody3D.Extension[TerrainTile] `gd:"AviaryTerrainTile"`

	brushEvents chan<- terrainBrushEvent

	Mesh        MeshInstance3D.Instance
	shader      ShaderMaterial.Instance
	side_shader ShaderMaterial.Instance

	shape_owner int

	client    *Client
	reloading bool
	sculpts   []musical.Sculpt

	heights []float32

	plain_normal Image.Instance

	// Store mesh and packed arrays for reuse
	mesh        ArrayMesh.Instance
	side_mesh   ArrayMesh.Instance
	arrays      [Mesh.ArrayMax]any
	arrays_side [Mesh.ArrayMax]any

	vertices      []Vector3.XYZ
	normals       []Vector3.XYZ
	uvs           []Vector2.XY
	textures      []float32
	weights       []float32
	vertices_side []Vector3.XYZ
	normals_side  []Vector3.XYZ
	uvs_side      []Vector2.XY
}

func (tile *TerrainTile) Ready() {
	tile.shape_owner = -1

	// Allocate mesh and arrays if not already done
	if tile.mesh == ArrayMesh.Nil {
		tile.mesh = ArrayMesh.New()
	}
	if tile.side_mesh == ArrayMesh.Nil {
		tile.side_mesh = ArrayMesh.New()
	}
	if tile.vertices == nil || len(tile.vertices) != 16*16*6 {
		tile.vertices = make([]Vector3.XYZ, 16*16*6)
	}
	if tile.normals == nil || len(tile.normals) != 16*16*6 {
		tile.normals = make([]Vector3.XYZ, 16*16*6)
	}
	if tile.uvs == nil || len(tile.uvs) != 16*16*6 {
		tile.uvs = make([]Vector2.XY, 16*16*6)
	}
	if tile.textures == nil || len(tile.textures) != 16*16*6*4 {
		tile.textures = make([]float32, 16*16*6*4)
	}
	if tile.weights == nil || len(tile.weights) != 16*16*6*4 {
		tile.weights = make([]float32, 16*16*6*4)
	}
	if tile.vertices_side == nil || len(tile.vertices_side) != 4*16*6 {
		tile.vertices_side = make([]Vector3.XYZ, 4*16*6)
	}
	if tile.normals_side == nil || len(tile.normals_side) != 4*16*6 {
		tile.normals_side = make([]Vector3.XYZ, 4*16*6)
	}
	if tile.uvs_side == nil || len(tile.uvs_side) != 4*16*6 {
		tile.uvs_side = make([]Vector2.XY, 4*16*6)
	}
	tile.Reload()
}

func (tile *TerrainTile) Sculpt(brush musical.Sculpt) {
	tile.sculpts = append(tile.sculpts, brush)
	if tile.reloading {
		return
	}
	tile.reloading = true
	Callable.Defer(Callable.New(func() {
		tile.Reload()

		if brush.Design == (musical.Design{}) {
			// raise any existing assets affected by the sculpt
			for id := range tile.client.object_to_entity {
				object, ok := id.Instance()
				if !ok {
					continue
				}
				pos := object.AsNode3D().GlobalPosition()
				pos.Y = tile.HeightAt(pos)
				object.AsNode3D().SetGlobalPosition(pos)
			}
		}
	}))
}

func (tile *TerrainTile) Reload() {
	tile.reloading = false

	terrains := Texture2DArray.New()
	images := []Image.Instance{Resource.Load[Texture2D.Instance]("res://terrain/alpine_grass.png").AsTexture2D().GetImage()}
	bumpmaps := []Image.Instance{Resource.Load[Texture2D.Instance]("res://terrain/normal.png").AsTexture2D().GetImage()}
	mapper := make(map[musical.Design]int)
	for _, sculpt := range tile.sculpts {
		if _, exists := mapper[sculpt.Design]; exists || sculpt.Design == (musical.Design{}) {
			continue
		}
		texture, ok := tile.client.textures[sculpt.Design].Instance()
		if ok {
			mapper[sculpt.Design] = len(images)
			images = append(images, texture.GetImage())

			ext := path.Ext(texture.AsResource().ResourcePath())
			normal_path := strings.TrimSuffix(texture.AsResource().ResourcePath(), ext) + "_normal" + ext
			if FileAccess.FileExists(normal_path) {
				bumpmaps = append(bumpmaps, Resource.Load[Texture2D.Instance](normal_path).AsTexture2D().GetImage())
			} else {
				bumpmaps = append(bumpmaps, Resource.Load[Texture2D.Instance]("res://terrain/normal.png").AsTexture2D().GetImage())
			}
		}
	}
	terrains.AsImageTextureLayered().CreateFromImages(images)
	tile.shader.SetShaderParameter("texture_albedo", terrains)
	normals_array := Texture2DArray.New()
	normals_array.AsImageTextureLayered().CreateFromImages(bumpmaps)
	tile.shader.SetShaderParameter("texture_normal", normals_array)

	offset := Vector3.XYZ{
		-8, 0, -8,
	}

	var sample_texture = func(x, y int) int {
		pos := Vector3.Add(Vector3.XYZ{Float.X(x), 0, Float.X(y)}, offset)
		for i := len(tile.sculpts) - 1; i >= 0; i-- {
			sculpt := tile.sculpts[i]
			if sculpt.Design == (musical.Design{}) {
				continue
			}
			dx := pos.X - sculpt.Target.X
			dy := pos.Z - sculpt.Target.Z
			dist := Float.Sqrt(dx*dx + dy*dy)
			if dist <= sculpt.Radius {
				return mapper[sculpt.Design]
			}
		}
		return 0
	}
	var sample_height = func(x, y int) Float.X {
		pos := Vector3.Add(Vector3.XYZ{Float.X(x), 0, Float.X(y)}, offset)
		height := Float.X(0)
		for i := range tile.sculpts {
			sculpt := tile.sculpts[i]
			if sculpt.Design != (musical.Design{}) {
				continue
			}
			dx := pos.X - sculpt.Target.X
			dy := pos.Z - sculpt.Target.Z
			if dx*dx+dy*dy <= sculpt.Radius*sculpt.Radius {
				height += sculpt.Amount * (1 - (dx*dx+dy*dy)/(sculpt.Radius*sculpt.Radius))
			}
		}
		return max(-2, height)
	}
	// Update heights
	if tile.heights == nil || len(tile.heights) != 17*17 {
		tile.heights = make([]float32, 17*17)
	}
	for x := 0; x < 17; x++ {
		for y := 0; y < 17; y++ {
			tile.heights[x+y*17] = float32(sample_height(x, y))
		}
	}

	// Update mesh arrays in-place
	add := func(index int, cell_x, cell_y int, x, y int, w1, w2, w3, w4 Float.X) {
		tile.vertices[index] = Vector3.XYZ{Float.X(x), sample_height(x, y), Float.X(y)}
		tile.normals[index] = Vector3.XYZ{0, 1, 0}
		tile.uvs[index] = Vector2.XY{Float.X(x) / 16, Float.X(y) / 16}

		tile.textures[index*4+0] = float32(sample_texture(cell_x, cell_y))     // top left
		tile.textures[index*4+1] = float32(sample_texture(cell_x+1, cell_y))   // top right
		tile.textures[index*4+2] = float32(sample_texture(cell_x, cell_y+1))   // bottom left
		tile.textures[index*4+3] = float32(sample_texture(cell_x+1, cell_y+1)) // bottom right

		tile.weights[index*4] = float32(w1)
		tile.weights[index*4+1] = float32(w2)
		tile.weights[index*4+2] = float32(w3)
		tile.weights[index*4+3] = float32(w4)
	}
	for x := 0; x < 16; x++ {
		for y := 0; y < 16; y++ {
			add(6*(x+16*y)+0, x, y, x, y, 1, 0, 0, 0)     // top left
			add(6*(x+16*y)+1, x, y, x+1, y, 0, 1, 0, 0)   // top right
			add(6*(x+16*y)+2, x, y, x, y+1, 0, 0, 1, 0)   // bottom left
			add(6*(x+16*y)+3, x, y, x+1, y, 0, 1, 0, 0)   // top right
			add(6*(x+16*y)+4, x, y, x+1, y+1, 0, 0, 0, 1) // bottom right
			add(6*(x+16*y)+5, x, y, x, y+1, 0, 0, 1, 0)   // bottom left
		}
	}

	shape := HeightMapShape3D.New()
	shape.SetMapDepth(17)
	shape.SetMapWidth(17)
	shape.SetMapData(tile.heights)

	// Prepare mesh arrays for main surface
	tile.arrays = [Mesh.ArrayMax]any{
		Mesh.ArrayVertex:  tile.vertices,
		Mesh.ArrayTexUv:   tile.uvs,
		Mesh.ArrayNormal:  tile.normals,
		Mesh.ArrayCustom0: tile.textures,
		Mesh.ArrayCustom1: tile.weights,
	}

	// Remove previous surfaces if any
	for tile.mesh.AsMesh().GetSurfaceCount() > 0 {
		tile.mesh.SurfaceRemove(0)
	}

	ArrayMesh.Expanded(tile.mesh).AddSurfaceFromArrays(Mesh.PrimitiveTriangles, tile.arrays[:], nil, nil,
		Mesh.ArrayFormatVertex|
			Mesh.ArrayFormat(Mesh.ArrayCustomRgbaFloat)<<Mesh.ArrayFormatCustom0Shift|
			Mesh.ArrayFormat(Mesh.ArrayCustomRgbaFloat)<<Mesh.ArrayFormatCustom1Shift,
	)
	tile_size := float32(1.0) // Adjust for texture tiling scale

	// Sides mesh data
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
				h_near = tile.heights[coord+sp.fixedIndex*17]
				h_far = tile.heights[coord+1+sp.fixedIndex*17]
			} else {
				h_near = tile.heights[sp.fixedIndex+coord*17]
				h_far = tile.heights[sp.fixedIndex+(coord+1)*17]
			}
			h_near += 2.2
			h_far += 2.2

			pos_near := float32(i)
			pos_far := float32(i + 1)
			var tl, tr, bl, br Vector3.XYZ
			if sp.isZFixed {
				tl = Vector3.XYZ{pos_near, h_near, sp.fixed}
				tr = Vector3.XYZ{pos_far, h_far, sp.fixed}
				bl = Vector3.XYZ{pos_near, 0, sp.fixed}
				br = Vector3.XYZ{pos_far, 0, sp.fixed}
			} else {
				tl = Vector3.XYZ{sp.fixed, h_near, pos_near}
				tr = Vector3.XYZ{sp.fixed, h_far, pos_far}
				bl = Vector3.XYZ{sp.fixed, 0, pos_near}
				br = Vector3.XYZ{sp.fixed, 0, pos_far}
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

			// Triangle 1
			tile.vertices_side[index_base+0] = bl
			tile.normals_side[index_base+0] = n
			tile.uvs_side[index_base+0] = Vector2.XY{float32(i) / tile_size, 0 / tile_size}
			if sp.flippedWinding {
				tile.vertices_side[index_base+1] = tr
				tile.normals_side[index_base+1] = n
				tile.uvs_side[index_base+1] = Vector2.XY{float32(i+1) / tile_size, h_far / tile_size}
				tile.vertices_side[index_base+2] = tl
				tile.normals_side[index_base+2] = n
				tile.uvs_side[index_base+2] = Vector2.XY{float32(i) / tile_size, h_near / tile_size}
			} else {
				tile.vertices_side[index_base+1] = tl
				tile.normals_side[index_base+1] = n
				tile.uvs_side[index_base+1] = Vector2.XY{float32(i) / tile_size, h_near / tile_size}
				tile.vertices_side[index_base+2] = tr
				tile.normals_side[index_base+2] = n
				tile.uvs_side[index_base+2] = Vector2.XY{float32(i+1) / tile_size, h_far / tile_size}
			}

			// Triangle 2
			tile.vertices_side[index_base+3] = bl
			tile.normals_side[index_base+3] = n
			tile.uvs_side[index_base+3] = Vector2.XY{float32(i) / tile_size, 0 / tile_size}
			if sp.flippedWinding {
				tile.vertices_side[index_base+4] = br
				tile.normals_side[index_base+4] = n
				tile.uvs_side[index_base+4] = Vector2.XY{float32(i+1) / tile_size, 0 / tile_size}
				tile.vertices_side[index_base+5] = tr
				tile.normals_side[index_base+5] = n
				tile.uvs_side[index_base+5] = Vector2.XY{float32(i+1) / tile_size, h_far / tile_size}
			} else {
				tile.vertices_side[index_base+4] = tr
				tile.normals_side[index_base+4] = n
				tile.uvs_side[index_base+4] = Vector2.XY{float32(i+1) / tile_size, h_far / tile_size}
				tile.vertices_side[index_base+5] = br
				tile.normals_side[index_base+5] = n
				tile.uvs_side[index_base+5] = Vector2.XY{float32(i+1) / tile_size, 0 / tile_size}
			}

			index_base += 6
		}
	}

	// Prepare mesh arrays for side surface
	tile.arrays_side = [Mesh.ArrayMax]any{
		Mesh.ArrayVertex: tile.vertices_side,
		Mesh.ArrayNormal: tile.normals_side,
		Mesh.ArrayTexUv:  tile.uvs_side,
	}

	// Remove previous side surfaces if any
	for tile.mesh.AsMesh().GetSurfaceCount() > 1 {
		tile.mesh.SurfaceRemove(1)
	}

	ArrayMesh.Expanded(tile.mesh).AddSurfaceFromArrays(Mesh.PrimitiveTriangles, tile.arrays_side[:], nil, nil,
		Mesh.ArrayFormatVertex|Mesh.ArrayFormatNormal|Mesh.ArrayFormatTexUv,
	)

	var collision_shape CollisionShape3D.Instance
	for i := 0; i < tile.AsNode().GetChildCount(); i++ {
		child := tile.AsNode().GetChild(i)
		if shape, ok := Object.As[CollisionShape3D.Instance](child); ok {
			collision_shape = shape
			break
		}
	}
	if collision_shape == CollisionShape3D.Nil {
		collision_shape = CollisionShape3D.New()
		tile.AsNode().AddChild(collision_shape.AsNode())
	}
	collision_shape.SetShape(shape.AsShape3D())

	tile.Mesh.SetMesh(tile.mesh.AsMesh())
	tile.Mesh.SetSurfaceOverrideMaterial(0, tile.shader.AsMaterial())
	tile.Mesh.SetSurfaceOverrideMaterial(1, tile.side_shader.AsMaterial())
	tile.Mesh.AsNode3D().SetPosition(Vector3.XYZ{
		-8, 0, -8,
	})
}

// HeightAt returns the height of the terrain mesh at the given position, taking into account the mesh.
func (tile *TerrainTile) HeightAt(pos Vector3.XYZ) Float.X {
	local := Vector3.Sub(pos, tile.Mesh.AsNode3D().GlobalPosition())
	x := local.X
	z := local.Z
	x = max(0.0, min(16.0, x))
	z = max(0.0, min(16.0, z))
	x0 := int(x)
	z0 := int(z)
	x1 := x0 + 1
	z1 := z0 + 1
	if x1 > 16 {
		x1 = 16
	}
	if z1 > 16 {
		z1 = 16
	}
	h00 := Float.X(tile.heights[x0+z0*17])
	h10 := Float.X(tile.heights[x1+z0*17])
	h01 := Float.X(tile.heights[x0+z1*17])
	h11 := Float.X(tile.heights[x1+z1*17])
	sx := x - Float.X(x0)
	sz := z - Float.X(z0)
	h0 := h00*(1-sx) + h10*sx
	h1 := h01*(1-sx) + h11*sx
	return (h0*(1-sz) + h1*sz)
}

// NormalAt returns the surface normal of the terrain mesh at the given position.
func (tile *TerrainTile) NormalAt(pos Vector3.XYZ) Vector3.XYZ {
	local := Vector3.Sub(pos, tile.Mesh.AsNode3D().GlobalPosition())
	x := local.X + 8
	z := local.Z + 8
	x = max(0.0, min(16.0, x))
	z = max(0.0, min(16.0, z))
	x0 := int(x)
	z0 := int(z)
	x1 := x0 + 1
	z1 := z0 + 1
	if x1 > 16 {
		x1 = 16
	}
	if z1 > 16 {
		z1 = 16
	}
	h00 := Float.X(tile.heights[x0+z0*17])
	h10 := Float.X(tile.heights[x1+z0*17])
	h01 := Float.X(tile.heights[x0+z1*17])
	h11 := Float.X(tile.heights[x1+z1*17])
	sx := x - Float.X(x0)
	sz := z - Float.X(z0)
	fx := (1-sz)*(h10-h00) + sz*(h11-h01)
	fz := (1-sx)*(h01-h00) + sx*(h11-h10)
	n := Vector3.XYZ{
		X: -fx,
		Y: 1,
		Z: -fz,
	}
	length := Float.Sqrt(n.X*n.X + n.Y*n.Y + n.Z*n.Z)
	if length == 0 {
		length = 1
	}
	n.X /= Float.X(length)
	n.Y /= Float.X(length)
	n.Z /= Float.X(length)
	return n
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
