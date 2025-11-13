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
	generated bool
	reloading bool
	sculpts   []musical.Sculpt

	heights []float32

	plain_normal Image.Instance

	// Store mesh and packed arrays for reuse
	mesh        ArrayMesh.Instance
	side_mesh   ArrayMesh.Instance
	arrays      [Mesh.ArrayMax]any
	arrays_side [Mesh.ArrayMax]any

	mapper map[musical.Design]int

	// cached geometry
	vertices []Vector3.XYZ
	normals  []Vector3.XYZ
	uvs      []Vector2.XY
	textures []float32
	weights  []float32

	albedos     []Image.Instance
	normal_maps []Image.Instance
}

func (tile *TerrainTile) Ready() {
	tile.shape_owner = -1
	tile.mapper = make(map[musical.Design]int)
	tile.Reload()
}

func (tile *TerrainTile) Sculpt(brush musical.Sculpt) {
	tile.sculpts = append(tile.sculpts, brush)
	tile.Reload()
}

// generateBase mesh, textures and the collision shape, these will change whenever a [musical.Sculpt] arrives.
func (tile *TerrainTile) generateBase() {
	tile.generated = true
	//
	// the actual mesh is a 16x16 plane grid, each cell has a texture associated with it that is known by each
	// neighboring vertex and blended together using the weights we setup here (the weights identify where in
	// the cell that the vertex is, and therefore which textures to take into consideration).
	//
	var mesh = ArrayMesh.New()
	tile.vertices = make([]Vector3.XYZ, 16*16*6)
	tile.normals = make([]Vector3.XYZ, 16*16*6)
	tile.uvs = make([]Vector2.XY, 16*16*6)
	tile.textures = make([]float32, 16*16*6*4)
	tile.weights = make([]float32, 16*16*6*4)
	add := func(index int, x, y int, w1, w2, w3, w4 Float.X) {
		tile.vertices[index] = Vector3.XYZ{Float.X(x), 0, Float.X(y)}
		tile.normals[index] = Vector3.XYZ{0, 1, 0}
		tile.uvs[index] = Vector2.XY{Float.X(x) / 16, Float.X(y) / 16}
		tile.weights[index*4] = float32(w1)
		tile.weights[index*4+1] = float32(w2)
		tile.weights[index*4+2] = float32(w3)
		tile.weights[index*4+3] = float32(w4)
	}
	for x := range 16 {
		for y := range 16 {
			add(6*(x+16*y)+0, x, y, 1, 0, 0, 0)     // top left
			add(6*(x+16*y)+1, x+1, y, 0, 1, 0, 0)   // top right
			add(6*(x+16*y)+2, x, y+1, 0, 0, 1, 0)   // bottom left
			add(6*(x+16*y)+3, x+1, y, 0, 1, 0, 0)   // top right
			add(6*(x+16*y)+4, x+1, y+1, 0, 0, 0, 1) // bottom right
			add(6*(x+16*y)+5, x, y+1, 0, 0, 1, 0)   // bottom left
		}
	}
	attributes := [Mesh.ArrayMax]any{
		Mesh.ArrayVertex:  tile.vertices,
		Mesh.ArrayTexUv:   tile.uvs,
		Mesh.ArrayNormal:  tile.normals,
		Mesh.ArrayCustom0: tile.textures,
		Mesh.ArrayCustom1: tile.weights,
	}
	mesh.MoreArgs().AddSurfaceFromArrays(Mesh.PrimitiveTriangles, attributes[:], nil, nil,
		Mesh.ArrayFormatVertex|
			Mesh.ArrayFormat(Mesh.ArrayCustomRgbaFloat)<<Mesh.ArrayFormatCustom0Shift|
			Mesh.ArrayFormat(Mesh.ArrayCustomRgbaFloat)<<Mesh.ArrayFormatCustom1Shift,
	)
	tile.Mesh.SetMesh(mesh.AsMesh())
	tile.Mesh.SetSurfaceOverrideMaterial(0, tile.shader.AsMaterial())
	tile.Mesh.AsNode3D().SetPosition(Vector3.XYZ{
		-8, 0, -8,
	})
	//
	// We set this up so that we can figure out which point on the terrain was clicked on input.
	//
	tile.heights = make([]float32, 17*17)
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
	shape := HeightMapShape3D.New()
	shape.SetMapDepth(17)
	shape.SetMapWidth(17)
	shape.SetMapData(tile.heights)
	collision_shape.SetShape(shape.AsShape3D())
	//
	// Whenever there is a new texture added to the tile, we need to recreate these texture arrays.
	//
	tile.albedos = []Image.Instance{Resource.Load[Texture2D.Instance]("res://terrain/alpine_grass.png").AsTexture2D().GetImage()}
	tile.normal_maps = []Image.Instance{Resource.Load[Texture2D.Instance]("res://terrain/normal.png").AsTexture2D().GetImage()}
	terrains := Texture2DArray.New()
	terrains.AsImageTextureLayered().CreateFromImages(tile.albedos)
	bumpmaps := Texture2DArray.New()
	bumpmaps.AsImageTextureLayered().CreateFromImages(tile.normal_maps)
	tile.shader.SetShaderParameter("texture_albedo", terrains)
	tile.shader.SetShaderParameter("texture_normal", bumpmaps)
	tile.reloadSides()
}

// Reload applies any pending sculpt operations to the terrain tile.
func (tile *TerrainTile) Reload() {
	if !tile.generated {
		tile.generateBase()
	}
	if tile.reloading {
		return // we only want to reload once per frame.
	}
	tile.reloading = true
	Callable.Defer(Callable.New(func() {
		tile.reloading = false
		//
		// we need to recreate the texture arrays if there are any new textures.
		//
		var old_count = len(tile.albedos)
		for _, sculpt := range tile.sculpts {
			if _, exists := tile.mapper[sculpt.Design]; exists || sculpt.Design == (musical.Design{}) {
				continue
			}
			texture, ok := tile.client.textures[sculpt.Design].Instance()
			if ok {
				tile.mapper[sculpt.Design] = len(tile.albedos)
				tile.albedos = append(tile.albedos, texture.GetImage())
				ext := path.Ext(texture.AsResource().ResourcePath())
				normal_path := strings.TrimSuffix(texture.AsResource().ResourcePath(), ext) + "_normal" + ext
				if FileAccess.FileExists(normal_path) {
					tile.normal_maps = append(tile.normal_maps, Resource.Load[Texture2D.Instance](normal_path).AsTexture2D().GetImage())
				} else {
					tile.normal_maps = append(tile.normal_maps, Resource.Load[Texture2D.Instance]("res://terrain/normal.png").AsTexture2D().GetImage())
				}
			}
		}
		if len(tile.albedos) != old_count {
			terrains := Texture2DArray.New()
			terrains.AsImageTextureLayered().CreateFromImages(tile.albedos)
			bumpmaps := Texture2DArray.New()
			bumpmaps.AsImageTextureLayered().CreateFromImages(tile.normal_maps)
			tile.shader.SetShaderParameter("texture_albedo", terrains)
			tile.shader.SetShaderParameter("texture_normal", bumpmaps)
		}
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
					return tile.mapper[sculpt.Design]
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
		update := func(index int, cell_x, cell_y int, x, y int) {
			tile.vertices[index].Y += sample_height(x, y)
			tile.uvs[index] = Vector2.XY{Float.X(x) / 16, Float.X(y) / 16}
			if sample := sample_texture(cell_x, cell_y); sample != 0 {
				tile.textures[index*4+0] = float32(sample) // top left
			}
			if sample := sample_texture(cell_x+1, cell_y); sample != 0 {
				tile.textures[index*4+1] = float32(sample) // top right
			}
			if sample := sample_texture(cell_x, cell_y+1); sample != 0 {
				tile.textures[index*4+2] = float32(sample) // bottom left
			}
			if sample := sample_texture(cell_x+1, cell_y+1); sample != 0 {
				tile.textures[index*4+3] = float32(sample) // bottom right
			}
		}
		for x := range 16 {
			for y := range 16 {
				update(6*(x+16*y)+0, x, y, x, y)     // top left
				update(6*(x+16*y)+1, x, y, x+1, y)   // top right
				update(6*(x+16*y)+2, x, y, x, y+1)   // bottom left
				update(6*(x+16*y)+3, x, y, x+1, y)   // top right
				update(6*(x+16*y)+4, x, y, x+1, y+1) // bottom right
				update(6*(x+16*y)+5, x, y, x, y+1)   // bottom left
			}
		}
		for i := range 17 * 17 {
			tile.heights[i] += float32(sample_height(i%17, i/17))
		}
		var collision_shape CollisionShape3D.Instance
		for i := 0; i < tile.AsNode().GetChildCount(); i++ {
			child := tile.AsNode().GetChild(i)
			if shape, ok := Object.As[CollisionShape3D.Instance](child); ok {
				collision_shape = shape
				break
			}
		}
		Object.To[HeightMapShape3D.Instance](collision_shape.Shape()).SetMapData(tile.heights)
		attributes := [Mesh.ArrayMax]any{
			Mesh.ArrayVertex:  tile.vertices,
			Mesh.ArrayTexUv:   tile.uvs,
			Mesh.ArrayNormal:  tile.normals,
			Mesh.ArrayCustom0: tile.textures,
			Mesh.ArrayCustom1: tile.weights,
		}
		mesh := Object.To[ArrayMesh.Instance](tile.Mesh.Mesh())
		mesh.ClearSurfaces()
		mesh.MoreArgs().AddSurfaceFromArrays(Mesh.PrimitiveTriangles, attributes[:], nil, nil,
			Mesh.ArrayFormatVertex|
				Mesh.ArrayFormat(Mesh.ArrayCustomRgbaFloat)<<Mesh.ArrayFormatCustom0Shift|
				Mesh.ArrayFormat(Mesh.ArrayCustomRgbaFloat)<<Mesh.ArrayFormatCustom1Shift,
		)
		mesh.RegenNormalMaps()
		for _, brush := range tile.sculpts {
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
		}
		tile.reloadSides()
		tile.sculpts = tile.sculpts[:0]
	}))
}

// reloadSides updates the side meshes to match the current terrain heights.
func (tile *TerrainTile) reloadSides() {
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
	var (
		vertices_side = make([]Vector3.XYZ, 4*16*6)
		normals_side  = make([]Vector3.XYZ, 4*16*6)
		uvs_side      = make([]Vector2.XY, 4*16*6)
	)
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
			vertices_side[index_base+0] = bl
			normals_side[index_base+0] = n
			uvs_side[index_base+0] = Vector2.XY{float32(i) / tile_size, 0 / tile_size}
			if sp.flippedWinding {
				vertices_side[index_base+1] = tr
				normals_side[index_base+1] = n
				uvs_side[index_base+1] = Vector2.XY{float32(i+1) / tile_size, h_far / tile_size}
				vertices_side[index_base+2] = tl
				normals_side[index_base+2] = n
				uvs_side[index_base+2] = Vector2.XY{float32(i) / tile_size, h_near / tile_size}
			} else {
				vertices_side[index_base+1] = tl
				normals_side[index_base+1] = n
				uvs_side[index_base+1] = Vector2.XY{float32(i) / tile_size, h_near / tile_size}
				vertices_side[index_base+2] = tr
				normals_side[index_base+2] = n
				uvs_side[index_base+2] = Vector2.XY{float32(i+1) / tile_size, h_far / tile_size}
			}
			// Triangle 2
			vertices_side[index_base+3] = bl
			normals_side[index_base+3] = n
			uvs_side[index_base+3] = Vector2.XY{float32(i) / tile_size, 0 / tile_size}
			if sp.flippedWinding {
				vertices_side[index_base+4] = br
				normals_side[index_base+4] = n
				uvs_side[index_base+4] = Vector2.XY{float32(i+1) / tile_size, 0 / tile_size}
				vertices_side[index_base+5] = tr
				normals_side[index_base+5] = n
				uvs_side[index_base+5] = Vector2.XY{float32(i+1) / tile_size, h_far / tile_size}
			} else {
				vertices_side[index_base+4] = tr
				normals_side[index_base+4] = n
				uvs_side[index_base+4] = Vector2.XY{float32(i+1) / tile_size, h_far / tile_size}
				vertices_side[index_base+5] = br
				normals_side[index_base+5] = n
				uvs_side[index_base+5] = Vector2.XY{float32(i+1) / tile_size, 0 / tile_size}
			}
			index_base += 6
		}
	}
	// Prepare mesh arrays for side surface
	tile.arrays_side = [Mesh.ArrayMax]any{
		Mesh.ArrayVertex: vertices_side,
		Mesh.ArrayNormal: normals_side,
		Mesh.ArrayTexUv:  uvs_side,
	}
	Object.To[ArrayMesh.Instance](tile.Mesh.Mesh()).MoreArgs().AddSurfaceFromArrays(Mesh.PrimitiveTriangles, tile.arrays_side[:], nil, nil,
		Mesh.ArrayFormatVertex|Mesh.ArrayFormatNormal|Mesh.ArrayFormatTexUv,
	)
	tile.Mesh.SetSurfaceOverrideMaterial(1, tile.side_shader.AsMaterial())
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
