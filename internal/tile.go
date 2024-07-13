package internal

import (
	"grow.graphics/gd"
	"grow.graphics/xy"
	"grow.graphics/xy/vector2"
	"grow.graphics/xy/vector3"
)

type Tile struct {
	gd.Class[Tile, gd.ArrayMesh] `gd:"AviaryTile"`

	Size xy.Vector2  `gd:"size"`
	Cell xy.Vector2i `gd:"cell"`

	Seed *Seed `gd:"seed"`

	generating bool
}

func (tile *Tile) OnSet(godot gd.Context, name gd.StringName, value gd.Variant) {
	if !tile.generating {
		godot.Callable(tile.generate).CallDeferred()
		tile.generating = true
	}
}

func (tile *Tile) OnFree(godot gd.Context) {
	tile.generating = false
}

func (tile *Tile) AsArrayMesh() gd.ArrayMesh { return *tile.Super() }

func (tile *Tile) generate(godot gd.Context) {
	if !tile.generating || tile.Seed == nil {
		return
	}
	tile.generating = false
	var (
		segmentWidth  = tile.Size.X()
		segmentHeight = tile.Size.Y()
	)
	const overscan = 4
	//Create the terrain slightly larger so that normals are correct on the edge.
	//width := w + segmentWidth*overscan*2
	//height := h + segmentHeight*overscan*2
	//widthHalf := width / 2
	//heightHalf := height / 2
	var (
		gridX  = overscan * 2
		gridY  = overscan * 2
		gridX1 = gridX + 1
		gridY1 = gridY + 1
	)
	// Create buffers
	var (
		positions []xy.Vector3
		normals   []xy.Vector3
		uvs       []xy.Vector2
		cells     []xy.Vector3i
	)
	// Generate plane vertices, vertices normals and vertices texture mappings.
	for iy := 0; iy < gridY1; iy++ {
		y := float64(iy)*segmentHeight - overscan*segmentHeight
		for ix := 0; ix < gridX1; ix++ {
			x := float64(ix)*segmentWidth - overscan*segmentWidth

			positions = append(positions, vector3.New(x, tile.Seed.HeightAt(vector2.New(x, y)), y))
			normals = append(normals, gd.Vector3{})
			uvs = append(uvs, vector2.New(float64(ix)/float64(gridX), (float64(iy)/float64(gridY))))
		}
	}
	// Generate plane vertices indices for the faces
	for iy := 0; iy < gridY; iy++ {
		for ix := 0; ix < gridX; ix++ {
			a := int64(ix + gridX1*iy)
			b := int64(ix + gridX1*(iy+1))
			c := int64((ix + 1) + gridX1*(iy+1))
			d := int64((ix + 1) + gridX1*iy)
			cells = append(cells, gd.NewVector3i(a, b, d))
			cells = append(cells, gd.NewVector3i(b, c, d))
		}
	}
	//calculate the normals
	for _, index := range cells {
		var va, vb, vc gd.Vector3 = positions[index[0]],
			positions[index[1]],
			positions[index[2]]
		e1 := vector3.Sub(vb, va)
		e2 := vector3.Sub(vc, va)
		no := vector3.Cross(e1, e2)
		normals[index[0]] = vector3.Add(normals[index[0]], no)
		normals[index[1]] = vector3.Add(normals[index[1]], no)
		normals[index[2]] = vector3.Add(normals[index[2]], no)
	}
	cells = nil
	// Generate plane vertices indices for the faces
	for iy := overscan; iy < gridY-overscan; iy++ {
		for ix := overscan; ix < gridX-overscan; ix++ {
			a := int64(ix + gridX1*iy)
			b := int64(ix + gridX1*(iy+1))
			c := int64((ix + 1) + gridX1*(iy+1))
			d := int64((ix + 1) + gridX1*iy)
			cells = append(cells, gd.NewVector3i(a, b, d))
			cells = append(cells, gd.NewVector3i(b, c, d))
		}
	}
	for i := range normals {
		normals[i] = normals[i].Normalized()
	}
	ArrayMesh := tile.AsArrayMesh()
	ArrayMesh.AsObject().SetBlockSignals(true)
	defer ArrayMesh.AsObject().SetBlockSignals(false)
	ArrayMesh.ClearSurfaces()
	{
		var vertices = godot.PackedVector3Array()
		for _, vertex := range positions {
			vertices.Append(vertex)
		}
		var indicies = godot.PackedInt32Array()
		for _, index := range cells {
			indicies.Append(int64(index[2]))
			indicies.Append(int64(index[1]))
			indicies.Append(int64(index[0]))
		}
		var norm = godot.PackedVector3Array()
		for _, normal := range normals {
			norm.Append(normal)
		}
		var uv = godot.PackedVector2Array()
		for _, u := range uvs {
			uv.Append(u)
		}
		var arrays = godot.Array()
		arrays.Resize(int64(gd.MeshArrayMax))
		arrays.SetIndex(int64(gd.MeshArrayVertex), godot.Variant(vertices))
		arrays.SetIndex(int64(gd.MeshArrayIndex), godot.Variant(indicies))
		arrays.SetIndex(int64(gd.MeshArrayNormal), godot.Variant(norm))
		arrays.SetIndex(int64(gd.MeshArrayTexUv), godot.Variant(uv))
		ArrayMesh.AddSurfaceFromArrays(gd.MeshPrimitiveTriangles, arrays, gd.NewArrayOf[gd.Array](godot), godot.Dictionary(), gd.MeshArrayFormatVertex)
	}
}
