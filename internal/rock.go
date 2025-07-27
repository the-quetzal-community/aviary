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

	"graphics.gd/classdb/ArrayMesh"
	"graphics.gd/classdb/FastNoiseLite"
	"graphics.gd/classdb/Mesh"
	"graphics.gd/variant/Angle"
	"graphics.gd/variant/Callable"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Packed"
	"graphics.gd/variant/Vector3"
	"graphics.gd/variant/Vector3i"
)

// Rock that is procedurally generated.
type Rock struct {
	ArrayMesh.Extension[Tree] `gd:"AviaryRock"`

	Seed int `gd:"seed" range:"0,1000,or_greater,or_less" default:"100"`

	NoiseScale     Float.X `gd:"noise_scale" range:"0.5,5,or_greater,or_less" default:"2"`
	NoiseStrength  Float.X `gd:"noise_strength" range:"0,0.5,or_greater,or_less" default:"0.2"`
	ScrapeCount    int     `gd:"scrape_count" range:"0,15,or_greater" default:"7"`
	ScrapeMinDist  Float.X `gd:"scrape_min_dist" range:"0.1,1,or_greater" default:"0.8"`
	ScrapeStrength Float.X `gd:"scrape_strength" range:"0.1,0.6,or_greater" default:"0.2"`
	ScrapeRadius   Float.X `gd:"scrape_radius" range:"0.1,0.5,or_greater" default:"0.3"`

	random     func() float32
	generating bool
}

func (rock *Rock) getNeighbours(positions []Vector3.XYZ, cells []Vector3i.XYZ) []map[uint32]struct{} {
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
				return 3 + i
			}
			return i % 3
		}
		// go through all the points of the face.
		for iPosition := range 3 {
			// the neighbours of this points are the previous and next points(in the array)
			var cur = Vector3i.Index(cellPositions, wrap(iPosition+0))
			var prev = Vector3i.Index(cellPositions, wrap(iPosition-1))
			var next = Vector3i.Index(cellPositions, wrap(iPosition+1))
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

func (rock *Rock) scrape(positionIndex int, positions []Vector3.XYZ, normals []Vector3.XYZ,
	adjacentVertices []map[uint32]struct{}, strength Float.X, radius Float.X) {
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
	Vector3.Add(r0, Vector3.MulX(n, -strength))
	var (
		stack []int
	)
	stack = append(stack, positionIndex)
	/*
		Projects the point `p` onto the plane defined by the normal `n` and the point `r0`
	*/
	project := func(n, r0, p Vector3.XYZ) Vector3.XYZ {
		// For an explanation of the math, see http://math.stackexchange.com/a/100766
		var (
			t          = Vector3.Dot(n, Vector3.Sub(r0, p)) / Vector3.Dot(n, n)
			projectedP = Vector3.Add(p, Vector3.MulX(n, t))
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
		if Vector3.Distance(projectedP, r0) < radius {
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

func (rock *Rock) OnSet(name string, value any) {
	if !rock.generating {
		Callable.Defer(Callable.New(rock.generate))
		rock.generating = true
	}
}

func (rock *Rock) OnFree() {
	rock.generating = false
}

func (rock *Rock) sphere(radius Float.X, precision int) (mesh struct {
	Vertices []Vector3.XYZ
	Indicies []Vector3i.XYZ
	Normals  []Vector3.XYZ
}) {
	var (
		stacks    = Angle.Radians(precision)
		slices    = Angle.Radians(precision)
		positions []Vector3.XYZ
		cells     []Vector3i.XYZ
		normals   []Vector3.XYZ
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
			u              = Angle.Radians(i) / stacks
			phi            = u * Angle.Pi
			stackBaseIndex = len(cells) / 2
		)
		// loop through the slices.
		for j := range int(slices) {
			var (
				v     = Angle.Radians(j) / slices
				theta = v * (Angle.Pi * 2)
			)
			var R = radius
			// use spherical coordinates to calculate the positions.
			var (
				x = Angle.Cos(theta) * Angle.Sin(phi)
				y = Angle.Cos(phi)
				z = Angle.Sin(theta) * Angle.Sin(phi)
			)
			positions = append(positions, Vector3.New(R*x, R*y, R*z))
			normals = append(normals, Vector3.New(x, y, z))
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
				cells = append(cells, Vector3i.XYZ{int32(i1), int32(i2), int32(i3)})
				cells = append(cells, Vector3i.XYZ{int32(i4), int32(i3), int32(i2)})
			}
			index++
		}
	}
	/*
	   Next, we finish the sphere by adding the faces that are adjacent to the top and bottom vertices.
	*/
	var topIndex = index
	index++
	positions = append(positions, Vector3.New(0.0, radius, 0.0))
	normals = append(normals, Vector3.New(0, 1, 0))
	var bottomIndex = index
	index++
	positions = append(positions, Vector3.New(0, -radius, 0))
	normals = append(normals, Vector3.New(0, -1, 0))
	for i := range int(slices) {
		var i1 = uint32(topIndex)
		var i2 = uint32(i + 0)
		var i3 = uint32(i+1) % uint32(slices)
		cells = append(cells, Vector3i.XYZ{int32(i3), int32(i2), int32(i1)})
		i1 = uint32(bottomIndex)
		i2 = uint32(bottomIndex-1) - uint32(slices) + uint32(i+0)
		i3 = uint32(bottomIndex-1) - uint32(slices) + uint32((i+1))%uint32(slices)
		cells = append(cells, Vector3i.XYZ{int32(i1), int32(i2), int32(i3)})
	}
	mesh.Vertices = positions
	mesh.Indicies = cells
	mesh.Normals = normals
	return
}

func (rock *Rock) calculateNormals(vertices []Vector3.XYZ, indicies []Vector3i.XYZ) (normals []Vector3.XYZ) {
	normals = make([]Vector3.XYZ, len(vertices))
	for _, index := range indicies {
		var va, vb, vc Vector3.XYZ = vertices[index.X],
			vertices[index.Y],
			vertices[index.Z]
		e1 := Vector3.Sub(vb, va)
		e2 := Vector3.Sub(vc, va)
		no := Vector3.Cross(e1, e2)
		normals[index.X] = Vector3.Add(normals[index.X], no)
		normals[index.Y] = Vector3.Add(normals[index.Y], no)
		normals[index.Z] = Vector3.Add(normals[index.Z], no)
	}
	return normals
}

func (rock *Rock) generate() {
	if !rock.generating {
		return
	}
	rock.generating = false
	rock.random = rand.New(rand.NewSource(int64(rock.Seed))).Float32
	var (
		noise     = FastNoiseLite.New()
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
	for range int(rock.ScrapeCount) {
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
			for j := range len(scrapeIndices) {
				var (
					q = positions[scrapeIndices[j]]
				)
				if Vector3.Distance(p, q) < rock.ScrapeMinDist {
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
	for i := range scrapeIndices {
		rock.scrape(
			scrapeIndices[i], positions, normals,
			adjacentVertices, rock.ScrapeStrength, rock.ScrapeRadius)
	}
	/*
	   Finally, we apply a Perlin noise to slighty distort the mesh,
	    and then we scale the mesh.
	*/
	for i := range positions {
		var p = positions[i]
		var noise = rock.NoiseStrength * noise.AsNoise().GetNoise3d(
			rock.NoiseScale*p.X,
			rock.NoiseScale*p.Y,
			rock.NoiseScale*p.Z)
		positions[i].X += Float.X(noise)
		positions[i].Y += Float.X(noise)
		positions[i].Z += Float.X(noise)
	}
	normals = rock.calculateNormals(positions, cells)
	ArrayMesh := rock.AsArrayMesh()
	Object.Instance(ArrayMesh.AsObject()).SetSignalsBlocked(true)
	defer Object.Instance(ArrayMesh.AsObject()).SetSignalsBlocked(false)
	ArrayMesh.ClearSurfaces()
	{
		var vertices = Packed.New(positions...)
		var indicies = Packed.New[int32]()
		for _, index := range cells {
			indicies.Append(index.Z)
			indicies.Append(index.Y)
			indicies.Append(index.Z)
		}
		var norm = Packed.New(normals...)
		var arrays = [Mesh.ArrayMax]any{
			Mesh.ArrayVertex: vertices,
			Mesh.ArrayIndex:  indicies,
			Mesh.ArrayNormal: norm,
		}
		ArrayMesh.AddSurfaceFromArrays(Mesh.PrimitiveTriangles, arrays[:])
	}
}
