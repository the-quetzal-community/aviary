/*
	https://github.com/Erkaman/gl-rock

	Permission is hereby granted, free of charge, to any person obtaining a copy of
	this software and associated documentation files (the "Software"), to deal in
	the Software without restriction, including without limitation the rights to
	use, copy, modify, merge, publish, distribute, sublicense, and/or sell copies of
	the Software, and to permit persons to whom the Software is furnished to do so,
	subject to the following conditions:

	The above copyright notice and this permission notice shall be included in all
	copies or substantial portions of the Software.

	THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
	IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY, FITNESS
	FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS OR
	COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER
	IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN
	CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
*/

package internal

import (
	"math"
	"math/rand"

	"grow.graphics/gd"
	"grow.graphics/xy"
	"grow.graphics/xy/vector3"
)

// Rock that is procedurally generated.
type Rock struct {
	gd.Class[Tree, gd.ArrayMesh] `gd:"AviaryRock"`

	Seed gd.Int `gd:"seed" range:"0,1000,or_greater,or_less" default:"100"`

	NoiseScale     gd.Float `gd:"noise_scale" range:"0.5,5,or_greater,or_less" default:"2"`
	NoiseStrength  gd.Float `gd:"noise_strength" range:"0,0.5,or_greater,or_less" default:"0.2"`
	ScrapeCount    gd.Int   `gd:"scrape_count" range:"0,15,or_greater" default:"7"`
	ScrapeMinDist  gd.Float `gd:"scrape_min_dist" range:"0.1,1,or_greater" default:"0.8"`
	ScrapeStrength gd.Float `gd:"scrape_strength" range:"0.1,0.6,or_greater" default:"0.2"`
	ScrapeRadius   gd.Float `gd:"scrape_radius" range:"0.1,0.5,or_greater" default:"0.3"`

	random     func() float32
	generating bool
}

func (rock *Rock) getNeighbours(positions []xy.Vector3, cells []xy.Vector3i) []map[uint32]struct{} {
	/*
	   adjacentVertices[i] contains a set containing all the indices of the neighbours of the vertex with
	   index i.
	   A set is used because it makes the algorithm more convenient.
	*/
	var adjacentVertices = make([]map[uint32]struct{}, len(positions))
	// go through all faces.
	for _, cellPositions := range cells {
		wrap := func(i int) int {
			if i < 0 {
				return len(cellPositions) + i
			}
			return i % len(cellPositions)
		}
		// go through all the points of the face.
		for iPosition := 0; iPosition < len(cellPositions); iPosition++ {
			// the neighbours of this points are the previous and next points(in the array)
			var cur = cellPositions[wrap(iPosition+0)]
			var prev = cellPositions[wrap(iPosition-1)]
			var next = cellPositions[wrap(iPosition+1)]
			// create set on the fly if necessary.
			if adjacentVertices[cur] == nil {
				adjacentVertices[cur] = make(map[uint32]struct{})
			}
			// add adjacent vertices.
			adjacentVertices[cur][uint32(prev)] = struct{}{}
			adjacentVertices[cur][uint32(next)] = struct{}{}
		}
	}
	return adjacentVertices
}

func (rock *Rock) scrape(positionIndex int, positions []xy.Vector3, normals []xy.Vector3,
	adjacentVertices []map[uint32]struct{}, strength float64, radius float64) {
	var (
		traversed      = make([]bool, len(positions))
		centerPosition = positions[positionIndex]
	)
	// to scrape, we simply project all vertices that are close to `centerPosition`
	// onto a plane. The equation of this plane is given by dot(n, r-r0) = 0,
	// where n is the plane normal, r0 is a point on the plane(in our case we set this to be the projected center),
	// and r is some arbitrary point on the plane.
	var (
		n  = normals[positionIndex]
		r0 = centerPosition
	)
	vector3.Add(r0, vector3.Mulf(n, -strength))
	var (
		stack []int
	)
	stack = append(stack, positionIndex)
	/*
		Projects the point `p` onto the plane defined by the normal `n` and the point `r0`
	*/
	project := func(n, r0, p gd.Vector3) gd.Vector3 {
		// For an explanation of the math, see http://math.stackexchange.com/a/100766
		var (
			t          = vector3.Dot(n, vector3.Sub(r0, p)) / vector3.Dot(n, n)
			projectedP = vector3.Add(p, vector3.Mulf(n, t))
		)
		return projectedP
	}
	/*
	 We use a simple flood-fill algorithm to make sure that we scrape all vertices around the center.
	 This will be fast, since all vertices have knowledge about their neighbours.
	*/
	for len(stack) > 0 {
		var (
			topIndex = stack[len(stack)-1]
		)
		stack = stack[:len(stack)-1]
		if traversed[topIndex] {
			continue // already traversed; look at next element in stack.
		}
		traversed[topIndex] = true
		var (
			topPosition = positions[topIndex]
			// project onto plane.
			p          = topPosition
			projectedP = project(n, r0, p)
		)
		if vector3.Distance(projectedP, r0) < radius {
			positions[topIndex] = projectedP
			normals[topIndex] = n
		} else {
			continue
		}
		var neighbourIndices = adjacentVertices[topIndex]
		for i := range neighbourIndices {
			stack = append(stack, int(i))
		}
	}
}

func (rock *Rock) OnSet(godot gd.Context, name gd.StringName, value gd.Variant) {
	if !rock.generating {
		godot.Callable(rock.generate).CallDeferred()
		rock.generating = true
	}
}

func (rock *Rock) OnFree(godot gd.Context) {
	rock.generating = false
}

func (rock *Rock) AsArrayMesh() gd.ArrayMesh { return *rock.Super() }

func (rock *Rock) sphere(radius float64, precision int) (mesh struct {
	Vertices []xy.Vector3
	Indicies []xy.Vector3i
	Normals  []xy.Vector3
}) {
	var (
		stacks    = float64(precision)
		slices    = float64(precision)
		positions []xy.Vector3
		cells     []xy.Vector3i
		normals   []xy.Vector3
	)
	// keeps track of the index of the next vertex that we create.
	var index = 0
	/*
	   First of all, we create all the faces that are NOT adjacent to the
	   bottom(0,-R,0) and top(0,+R,0) vertices of the sphere.
	   (it's easier this way, because for the bottom and top vertices, we need to add triangle faces.
	   But for the faces between, we need to add quad faces. )
	*/
	// loop through the stacks.
	for i := 1; i < int(stacks); i++ {
		var (
			u              = float64(i) / stacks
			phi            = u * math.Pi
			stackBaseIndex = len(cells) / 2
		)
		// loop through the slices.
		for j := 0; j < int(slices); j++ {
			var (
				v     = float64(j) / slices
				theta = v * (math.Pi * 2)
			)
			var R = radius
			// use spherical coordinates to calculate the positions.
			var (
				x = xy.Cos(theta) * xy.Sin(phi)
				y = xy.Cos(phi)
				z = xy.Sin(theta) * xy.Sin(phi)
			)
			positions = append(positions, vector3.New(R*x, R*y, R*z))
			normals = append(normals, vector3.New(x, y, z))
			if (i + 1) != int(stacks) { // for the last stack, we don't need to add faces.
				var (
					i1, i2, i3, i4 uint32
				)
				if (j + 1) == int(slices) {
					// for the last vertex in the slice, we need to wrap around to create the face.
					i1 = uint32(index)
					i2 = uint32(stackBaseIndex)
					i3 = uint32(index + int(slices))
					i4 = uint32(stackBaseIndex + int(slices))
				} else {
					// use the indices from the current slice, and indices from the next slice, to create the face.
					i1 = uint32(index)
					i2 = uint32(index + 1)
					i3 = uint32(index + int(slices))
					i4 = uint32(index + int(slices) + 1)
				}
				// add quad face
				cells = append(cells, xy.Vector3i{int32(i1), int32(i2), int32(i3)})
				cells = append(cells, xy.Vector3i{int32(i4), int32(i3), int32(i2)})
			}
			index++
		}
	}
	/*
	   Next, we finish the sphere by adding the faces that are adjacent to the top and bottom vertices.
	*/
	var topIndex = index
	index++
	positions = append(positions, vector3.New(0.0, radius, 0.0))
	normals = append(normals, vector3.New(0, 1, 0))
	var bottomIndex = index
	index++
	positions = append(positions, vector3.New(0, -radius, 0))
	normals = append(normals, vector3.New(0, -1, 0))
	for i := 0; i < int(slices); i++ {
		var i1 = uint32(topIndex)
		var i2 = uint32(i + 0)
		var i3 = uint32(i+1) % uint32(slices)
		cells = append(cells, xy.Vector3i{int32(i3), int32(i2), int32(i1)})
		i1 = uint32(bottomIndex)
		i2 = uint32(bottomIndex-1) - uint32(slices) + uint32(i+0)
		i3 = uint32(bottomIndex-1) - uint32(slices) + uint32((i+1))%uint32(slices)
		cells = append(cells, xy.Vector3i{int32(i1), int32(i2), int32(i3)})
	}
	mesh.Vertices = positions
	mesh.Indicies = cells
	mesh.Normals = normals
	return
}

func (rock *Rock) calculateNormals(vertices []xy.Vector3, indicies []xy.Vector3i) (normals []xy.Vector3) {
	normals = make([]xy.Vector3, len(vertices))
	for _, index := range indicies {
		var va, vb, vc xy.Vector3 = vertices[index[0]],
			vertices[index[1]],
			vertices[index[2]]
		e1 := vector3.Sub(vb, va)
		e2 := vector3.Sub(vc, va)
		no := vector3.Cross(e1, e2)
		normals[index[0]] = vector3.Add(normals[index[0]], no)
		normals[index[1]] = vector3.Add(normals[index[1]], no)
		normals[index[2]] = vector3.Add(normals[index[2]], no)
	}
	return normals
}

func (rock *Rock) generate(godot gd.Context) {
	if !rock.generating {
		return
	}
	rock.generating = false
	rock.random = rand.New(rand.NewSource(int64(rock.Seed))).Float32
	var (
		noise     = gd.Create(godot, new(gd.FastNoiseLite))
		sphere    = rock.sphere(1, 20)
		positions = sphere.Vertices
		cells     = sphere.Indicies
		normals   = sphere.Normals
	)
	noise.SetSeed(rock.Seed)
	adjacentVertices := rock.getNeighbours(positions, cells)
	/*
	   randomly generate positions at which to scrape.
	*/
	var (
		scrapeIndices []int
	)
	for i := 0; i < int(rock.ScrapeCount); i++ {
		var (
			attempts = 0
		)
		// find random position which is not too close to the other positions.
		for {
			var (
				randIndex = int(math.Floor(float64(len(positions)) * float64(rock.random())))
				p         = positions[randIndex]
				tooClose  = false
			)
			// check that it is not too close to the other vertices.
			for j := 0; j < len(scrapeIndices); j++ {
				var (
					q = positions[scrapeIndices[j]]
				)
				if vector3.Distance(p, q) < rock.ScrapeMinDist {
					tooClose = true
					break
				}
			}
			attempts++
			// if we have done too many attempts, we let it pass regardless.
			// otherwise, we risk an endless loop.
			if tooClose && attempts < 100 {
				continue
			} else {
				scrapeIndices = append(scrapeIndices, randIndex)
				break
			}
		}
	}
	// now we scrape at all the selected positions.
	for i := 0; i < len(scrapeIndices); i++ {
		rock.scrape(
			scrapeIndices[i], positions, normals,
			adjacentVertices, rock.ScrapeStrength, rock.ScrapeRadius)
	}
	/*
	   Finally, we apply a Perlin noise to slighty distort the mesh,
	    and then we scale the mesh.
	*/
	for i := 0; i < len(positions); i++ {
		var p = positions[i]
		var noise = rock.NoiseStrength * noise.AsNoise().GetNoise3d(
			rock.NoiseScale*float64(p[0]),
			rock.NoiseScale*float64(p[1]),
			rock.NoiseScale*float64(p[2]))
		positions[i][0] += xy.Float(noise)
		positions[i][1] += xy.Float(noise)
		positions[i][2] += xy.Float(noise)
	}
	normals = rock.calculateNormals(positions, cells)
	ArrayMesh := rock.AsArrayMesh()
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
		var arrays = godot.Array()
		arrays.Resize(int64(gd.MeshArrayMax))
		arrays.SetIndex(int64(gd.MeshArrayVertex), godot.Variant(vertices))
		arrays.SetIndex(int64(gd.MeshArrayIndex), godot.Variant(indicies))
		arrays.SetIndex(int64(gd.MeshArrayNormal), godot.Variant(norm))
		ArrayMesh.AddSurfaceFromArrays(gd.MeshPrimitiveTriangles, arrays, gd.NewArrayOf[gd.Array](godot), godot.Dictionary(), gd.MeshArrayFormatVertex)
	}
}
