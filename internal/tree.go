/*
    https://github.com/supereggbert/proctree.js/blob/master/proctree.js

	Copyright (c) 2012, Paul Brunt
	All rights reserved.

	Redistribution and use in source and binary forms, with or without
	modification, are permitted provided that the following conditions are met:
		* Redistributions of source code must retain the above copyright
		notice, this list of conditions and the following disclaimer.
		* Redistributions in binary form must reproduce the above copyright
		notice, this list of conditions and the following disclaimer in the
		documentation and/or other materials provided with the distribution.
		* Neither the name of tree.js nor the
		names of its contributors may be used to endorse or promote products
		derived from this software without specific prior written permission.

	THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS "AS IS" AND
	ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE IMPLIED
	WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE ARE
	DISCLAIMED. IN NO EVENT SHALL PAUL BRUNT BE LIABLE FOR ANY
	DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES
	(INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES;
	LOSS OF USE, DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND
	ON ANY THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
	(INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE OF THIS
	SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
*/

package internal

import (
	"fmt"
	"math"

	"graphics.gd/classdb/ArrayMesh"
	"graphics.gd/classdb/Material"
	"graphics.gd/classdb/Mesh"
	"graphics.gd/variant/Angle"
	"graphics.gd/variant/Callable"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Packed"
	"graphics.gd/variant/Vector2"
	"graphics.gd/variant/Vector3"
	"graphics.gd/variant/Vector3i"
)

// Tree is a procedurally generated tree.
type Tree struct {
	ArrayMesh.Extension[Tree] `gd:"AviaryTree"`

	Seed      Float.X `gd:"seed" default:"10" range:"0,1000,or_greater,or_less"`
	Levels    int     `gd:"levels" default:"3" range:"1,7,or_greater"`
	TwigScale Float.X `gd:"twig_scale" default:"2" range:"0,1,or_greater"`

	// Branching

	InitalBranchLength  Float.X `gd:"initial_branch_length" default:"0.85" range:"0.1,1,or_greater"`
	LengthFalloffFactor Float.X `gd:"length_falloff_factor" default:"0.85" range:"0.5,1,or_greater,or_less"`
	LengthFalloffPower  Float.X `gd:"length_falloff_power" default:"1" range:"0.1,1.5,or_greater"`
	ClumpMin            Float.X `gd:"clump_min" default:"0.8" range:"0,1"`
	ClumpMax            Float.X `gd:"clump_max" default:"0.5" range:"0,1"`
	BranchFactor        Float.X `gd:"branch_factor" default:"2" range:"2,4,or_greater"`
	DropAmount          Float.X `gd:"drop_amount" range:"-1,1"`
	GrowAmount          Float.X `gd:"grow_amount" range:"-0.5,1"`
	SweepAmount         Float.X `gd:"sweep_amount" range:"-1,1"`

	// Trunk

	MaxRadius         Float.X       `gd:"max_radius" default:"0.25" range:"0.05,1,or_greater"`
	ClimbRate         Float.X       `gd:"climb_rate" default:"1.5" range:"0.05,1,or_greater"`
	TrunkKink         Float.X       `gd:"trunk_kink" range:"0,0.5,or_greater"`
	TreeSteps         Float.X       `gd:"tree_steps" range:"0,35,or_greater"`
	TaperRate         Float.X       `gd:"taper_rate" default:"0.95" range:"0.7,1,or_greater"`
	RadiusFalloffRate Float.X       `gd:"radius_falloff_rate" default:"0.6" range:"0.5,0.8"`
	TwistRate         Angle.Radians `gd:"twist_rate" default:"13" range:"0,10"`
	TrunkLength       Float.X       `gd:"trunk_length" default:"2.5" range:"0.1,5"`

	// Material
	VMultiplier Float.X `gd:"v_multiplier" default:"0.2"`

	// Internal

	segments int `gd:"segments" default:"6"`

	recalculating bool

	root *branch

	random func(Float.X) Float.X

	mesh, twig buffer
}

func NewTree() *Tree {
	return &Tree{
		Seed:                10,
		Levels:              3,
		TwigScale:           2,
		InitalBranchLength:  0.85,
		LengthFalloffFactor: 0.85,
		LengthFalloffPower:  1,
		ClumpMin:            0.8,
		ClumpMax:            0.5,
		BranchFactor:        2,
		DropAmount:          0,
		GrowAmount:          0,
		SweepAmount:         0,
		MaxRadius:           0.25,
		ClimbRate:           1.5,
		TrunkKink:           0,
		TreeSteps:           0,
		TaperRate:           0.95,
		RadiusFalloffRate:   0.6,
		TwistRate:           10,
		TrunkLength:         2.5,
		VMultiplier:         0.2,
	}
}

type buffer struct {
	verts   []Vector3.XYZ
	faces   []Vector3i.XYZ
	normals []Vector3.XYZ
	uvs     []Vector2.XY
}

func (tree *Tree) OnCreate() {
	tree.random = func(a Float.X) Float.X {
		return Float.Abs(Angle.Cos(Angle.Radians(a + a*a)))
	}
	tree.segments = 6
	if !tree.recalculating {
		Callable.Defer(Callable.New(tree.recalculate))
		tree.recalculating = true
	}
}

func (tree *Tree) OnSet(name string, value any) {
	fmt.Println("Tree property changed:", name, value)
	if !tree.recalculating {
		Callable.Defer(Callable.New(tree.recalculate))
		tree.recalculating = true
	}
}

func (tree *Tree) OnFree() {
	tree.recalculating = false
}

func (tree *Tree) recalculate() {
	if !tree.recalculating {
		return
	}
	tree.Levels = max(tree.Levels, 1)
	tree.recalculating = false

	tree.mesh = buffer{}
	tree.twig = buffer{}
	tree.root = newBranch(Vector3.New(0, tree.TrunkLength, 0), nil)
	tree.root.length = tree.InitalBranchLength
	tree.root.split(tree.Levels, tree.TreeSteps, tree, 0, 0)
	tree.createForks(tree.root, tree.MaxRadius)
	tree.createTwigs(tree.root)
	tree.doFaces(tree.root)
	tree.calcNormals()

	ArrayMesh := tree.AsArrayMesh()

	var restoreMats = false
	var mat1 Material.Instance
	var mat2 Material.Instance
	if ArrayMesh.AsMesh().GetSurfaceCount() > 0 {
		restoreMats = true
		mat1 = ArrayMesh.AsMesh().SurfaceGetMaterial(0)
		mat2 = ArrayMesh.AsMesh().SurfaceGetMaterial(1)
	}

	Object.Instance(ArrayMesh.AsObject()).SetSignalsBlocked(true)
	defer Object.Instance(ArrayMesh.AsObject()).SetSignalsBlocked(false)

	ArrayMesh.ClearSurfaces()
	{
		var vertices = Packed.New[Vector3.XYZ]()
		for _, vertex := range tree.mesh.verts {
			vertices.Append(vertex)
		}
		var indicies = Packed.New[int32]()
		for _, index := range tree.mesh.faces {
			indicies.Append(index.Z)
			indicies.Append(index.Y)
			indicies.Append(index.X)
		}
		var normals = Packed.New[Vector3.XYZ]()
		for _, normal := range tree.mesh.normals {
			normals.Append(normal)
		}
		var arrays = [Mesh.ArrayMax]any{
			Mesh.ArrayVertex: vertices,
			Mesh.ArrayIndex:  indicies,
			Mesh.ArrayNormal: normals,
		}
		ArrayMesh.AddSurfaceFromArrays(Mesh.PrimitiveTriangles, arrays[:])
	}
	{
		var vertices = Packed.New[Vector3.XYZ]()
		for _, vertex := range tree.twig.verts {
			vertices.Append(vertex)
		}
		var indicies = Packed.New[int32]()
		for _, index := range tree.twig.faces {
			indicies.Append(index.Z)
			indicies.Append(index.Y)
			indicies.Append(index.X)
		}
		var normals = Packed.New[Vector3.XYZ]()
		for _, normal := range tree.twig.normals {
			normals.Append(normal)
		}
		var arrays = [Mesh.ArrayMax]any{
			Mesh.ArrayVertex: vertices,
			Mesh.ArrayIndex:  indicies,
			Mesh.ArrayNormal: normals,
		}
		ArrayMesh.AddSurfaceFromArrays(Mesh.PrimitiveTriangles, arrays[:])
	}
	if restoreMats {
		ArrayMesh.AsMesh().SurfaceSetMaterial(0, mat1)
		ArrayMesh.AsMesh().SurfaceSetMaterial(1, mat2)
	}
}

func scaleInDirection(vector, direction Vector3.XYZ, scale Float.X) Vector3.XYZ {
	var currentMag = Vector3.Dot(vector, direction)
	var change = Vector3.MulX(direction, currentMag*scale-currentMag)
	return Vector3.Add(vector, change)
}

func vecAxisAngle(vec, axis Vector3.XYZ, angle Angle.Radians) Vector3.XYZ {
	var cosr = Angle.Cos(angle)
	var sinr = Angle.Sin(angle)
	return Vector3.Add(
		Vector3.Add(
			Vector3.MulX(vec, cosr),
			Vector3.MulX(Vector3.Cross(axis, vec), sinr),
		),
		Vector3.MulX(axis, Vector3.Dot(axis, vec)*(1-cosr)),
	)
}

type Leaves Tree

func (leaves Leaves) Indicies() []uint32 {
	tree := Tree(leaves)
	var retArray = make([]uint32, 0, len(tree.twig.faces)*3)
	for _, face := range tree.twig.faces {
		retArray = append(retArray, uint32(face.X))
		retArray = append(retArray, uint32(face.Y))
		retArray = append(retArray, uint32(face.Z))
	}
	return retArray
}

func (leaves Leaves) UVs() []float32 {
	tree := Tree(leaves)
	var retArray = make([]float32, 0, len(tree.twig.uvs)*3)
	for _, uv := range tree.twig.uvs {
		retArray = append(retArray, float32(uv.X))
		retArray = append(retArray, float32(uv.Y))
	}
	return retArray
}

func (leaves Leaves) Vertices() []float32 {
	tree := Tree(leaves)
	var retArray = make([]float32, 0, len(tree.twig.verts)*3)
	for _, triangle := range tree.twig.verts {
		retArray = append(retArray, float32(triangle.X))
		retArray = append(retArray, float32(triangle.Y))
		retArray = append(retArray, float32(triangle.Z))
	}
	return retArray
}

func (leaves Leaves) Normals() []float32 {
	tree := Tree(leaves)
	var retArray = make([]float32, 0, len(tree.twig.normals)*3)
	for _, vector := range tree.twig.normals {
		retArray = append(retArray, float32(vector.X))
		retArray = append(retArray, float32(vector.Y))
		retArray = append(retArray, float32(vector.Z))
	}
	return retArray
}

func (tree Tree) Leaves() Leaves {
	return Leaves(tree)
}

func (tree *Tree) calcNormals() {
	var (
		allNormals = make([][]Vector3.XYZ, len(tree.mesh.verts))
	)
	for _, face := range tree.mesh.faces {
		var norm = Vector3.Normalized(
			Vector3.Cross(
				Vector3.Sub(tree.mesh.verts[face.Y], tree.mesh.verts[face.Z]),
				Vector3.Sub(tree.mesh.verts[face.Y], tree.mesh.verts[face.X]),
			),
		)
		allNormals[face.X] = append(allNormals[face.X], norm)
		allNormals[face.Y] = append(allNormals[face.Y], norm)
		allNormals[face.Z] = append(allNormals[face.Z], norm)
	}
	tree.mesh.normals = make([]Vector3.XYZ, len(allNormals))
	for i := range allNormals {
		var total = Vector3.Zero
		var l = len(allNormals[i])
		for j := range l {
			total = Vector3.Add(total, Vector3.MulX(allNormals[i][j], 1/float64(l)))
		}
		tree.mesh.normals[i] = total
	}
}

func (tree *Tree) doFaces(branch *branch) {
	var (
		segments = int32(tree.segments)
	)
	if branch.parent == nil {
		tree.mesh.uvs = make([]Vector2.XY, len(tree.mesh.verts))
		var tangent = Vector3.Normalized(
			Vector3.Cross(
				Vector3.Sub(branch.child[0].head, branch.head),
				Vector3.Sub(branch.child[1].head, branch.head),
			),
		)
		var normal = Vector3.Normalized(branch.head)
		var angle = Angle.Acos(Vector3.Dot(tangent, Vector3.New(-1, 0, 0)))
		if Vector3.Dot(Vector3.Cross(Vector3.New(-1, 0, 0), tangent), normal) > 0 {
			angle = 2*math.Pi - angle
		}
		var (
			segOffset = int32(Float.Round(Float.X(angle/Angle.Pi/2) * Float.X(segments)))
		)
		for i := int32(0); i < segments; i++ {
			var (
				v1 = branch.ring[0][i]
				v2 = branch.root[(i+segOffset+1)%segments]
				v3 = branch.root[(i+segOffset)%segments]
				v4 = branch.ring[0][(i+1)%segments]
			)
			tree.mesh.faces = append(tree.mesh.faces, Vector3i.New(v1, v4, v3))
			tree.mesh.faces = append(tree.mesh.faces, Vector3i.New(v1, v4, v3))
			tree.mesh.faces = append(tree.mesh.faces, Vector3i.New(v4, v2, v3))
			tree.mesh.uvs[(i+segOffset)%segments] = Vector2.New(math.Abs(float64(i)/float64(segments)-0.5)*2, 0)
			var (
				l = Vector3.Length(Vector3.Sub(tree.mesh.verts[branch.ring[0][i]], tree.mesh.verts[branch.root[(i+segOffset)%segments]])) * tree.VMultiplier
			)
			tree.mesh.uvs[branch.ring[0][i]] = Vector2.New(Float.Abs(Float.X(i)/Float.X(segments)-0.5)*2, l)
			tree.mesh.uvs[branch.ring[2][i]] = Vector2.New(Float.Abs(Float.X(i)/Float.X(segments)-0.5)*2, l)
		}
	}
	if branch.child[0].ring[0] != nil {
		var (
			segOffset0, segOffset1 int32
			match0, match1         Float.X
			first0, first1         bool = true, true
			v1                          = Vector3.Normalized(Vector3.Sub(tree.mesh.verts[branch.ring[1][0]], branch.head))
			v2                          = Vector3.Normalized(Vector3.Sub(tree.mesh.verts[branch.ring[2][0]], branch.head))
		)
		v1 = scaleInDirection(v1, Vector3.Normalized(Vector3.Sub(branch.child[0].head, branch.head)), 0)
		v2 = scaleInDirection(v2, Vector3.Normalized(Vector3.Sub(branch.child[1].head, branch.head)), 0)
		for i := int32(0); i < segments; i++ {
			var d = Vector3.Normalized(Vector3.Sub(tree.mesh.verts[branch.child[0].ring[0][i]], branch.child[0].head))
			var l = Vector3.Dot(d, v1)
			if first0 || l > match0 {
				match0 = l
				segOffset0 = segments - i
				first0 = false

			}
			d = Vector3.Normalized(Vector3.Sub(tree.mesh.verts[branch.child[1].ring[0][i]], branch.child[1].head))
			l = Vector3.Dot(d, v2)
			if first1 || l > match1 {
				match1 = l
				segOffset1 = segments - i
				first1 = false
			}
		}
		var (
			UVScale = tree.MaxRadius / branch.radius
		)
		for i := int32(0); i < segments; i++ {
			v1 := branch.child[0].ring[0][i]
			v2 := branch.ring[1][(i+segOffset0+1)%segments]
			v3 := branch.ring[1][(i+segOffset0)%segments]
			v4 := branch.child[0].ring[0][(i+1)%segments]
			tree.mesh.faces = append(tree.mesh.faces, Vector3i.New(v1, v4, v3))
			tree.mesh.faces = append(tree.mesh.faces, Vector3i.New(v4, v2, v3))
			v1 = branch.child[1].ring[0][i]
			v2 = branch.ring[2][(i+segOffset1+1)%segments]
			v3 = branch.ring[2][(i+segOffset1)%segments]
			v4 = branch.child[1].ring[0][(i+1)%segments]
			tree.mesh.faces = append(tree.mesh.faces, Vector3i.New(v1, v2, v3))
			tree.mesh.faces = append(tree.mesh.faces, Vector3i.New(v1, v4, v2))
			var (
				len1 = Vector3.Length(Vector3.Sub(tree.mesh.verts[branch.child[0].ring[0][i]], tree.mesh.verts[branch.ring[1][(i+segOffset0)%segments]])) * UVScale
				uv1  = tree.mesh.uvs[branch.ring[1][(i+segOffset0-1)%segments]]
			)
			tree.mesh.uvs[branch.child[0].ring[0][i]] = Vector2.New(uv1.X, uv1.Y+len1*tree.VMultiplier)
			tree.mesh.uvs[branch.child[0].ring[2][i]] = Vector2.New(uv1.X, uv1.Y+len1*tree.VMultiplier)
			var (
				len2 = Vector3.Length(Vector3.Sub(tree.mesh.verts[branch.child[1].ring[0][i]], tree.mesh.verts[branch.ring[2][(i+segOffset1)%segments]])) * UVScale
				uv2  = tree.mesh.uvs[branch.ring[2][(i+segOffset1-1)%segments]]
			)
			tree.mesh.uvs[branch.child[1].ring[0][i]] = Vector2.New(uv2.X, uv2.Y+len2*tree.VMultiplier)
			tree.mesh.uvs[branch.child[1].ring[2][i]] = Vector2.New(uv2.X, uv2.Y+len2*tree.VMultiplier)
		}
		tree.doFaces(branch.child[0])
		tree.doFaces(branch.child[1])
	} else {
		for i := int32(0); i < segments; i++ {
			tree.mesh.faces = append(tree.mesh.faces, Vector3i.New(branch.child[0].end, branch.ring[1][(i+1)%segments], branch.ring[1][i]))
			tree.mesh.faces = append(tree.mesh.faces, Vector3i.New(branch.child[1].end, branch.ring[2][(i+1)%segments], branch.ring[2][i]))
			var (
				len = Vector3.Length(Vector3.Sub(tree.mesh.verts[branch.child[0].end], tree.mesh.verts[branch.ring[1][i]]))
			)
			tree.mesh.uvs[branch.child[0].end] = Vector2.New(Float.Abs(Float.X(i)/Float.X(segments)-1-0.5)*2, len*tree.VMultiplier)
			len = Vector3.Length(Vector3.Sub(tree.mesh.verts[branch.child[1].end], tree.mesh.verts[branch.ring[2][i]]))
			tree.mesh.uvs[branch.child[1].end] = Vector2.New(Float.Abs(Float.X(i)/Float.X(segments)-0.5)*2, len*tree.VMultiplier)
		}
	}
}

func (tree *Tree) createTwigs(branch *branch) {
	if branch.child[0] == nil {
		var tangent = Vector3.Normalized(
			Vector3.Cross(
				Vector3.Sub(branch.parent.child[0].head, branch.parent.head),
				Vector3.Sub(branch.parent.child[1].head, branch.parent.head),
			),
		)
		var (
			binormal = Vector3.Normalized(Vector3.Sub(branch.head, branch.parent.head))
			normal   = Vector3.Cross(tangent, binormal)
		)
		//This can probably be factored into a loop.
		var vert1 = int32(len(tree.twig.verts))
		tree.twig.verts = append(tree.twig.verts,
			Vector3.Add(
				Vector3.Add(branch.head, Vector3.MulX(tangent, tree.TwigScale)),
				Vector3.MulX(binormal, tree.TwigScale*2-branch.length),
			),
		)
		var vert2 = int32(len(tree.twig.verts))
		tree.twig.verts = append(tree.twig.verts,
			Vector3.Add(
				Vector3.Add(branch.head, Vector3.MulX(tangent, -tree.TwigScale)),
				Vector3.MulX(binormal, tree.TwigScale*2-branch.length),
			),
		)
		var vert3 = int32(len(tree.twig.verts))
		tree.twig.verts = append(tree.twig.verts,
			Vector3.Add(
				Vector3.Add(branch.head, Vector3.MulX(tangent, -tree.TwigScale)),
				Vector3.MulX(binormal, -branch.length),
			),
		)
		var vert4 = int32(len(tree.twig.verts))
		tree.twig.verts = append(tree.twig.verts,
			Vector3.Add(
				Vector3.Add(branch.head, Vector3.MulX(tangent, tree.TwigScale)),
				Vector3.MulX(binormal, -branch.length),
			),
		)
		var vert8 = int32(len(tree.twig.verts))
		tree.twig.verts = append(tree.twig.verts,
			Vector3.Add(
				Vector3.Add(branch.head, Vector3.MulX(tangent, tree.TwigScale)),
				Vector3.MulX(binormal, tree.TwigScale*2-branch.length),
			),
		)
		var vert7 = int32(len(tree.twig.verts))
		tree.twig.verts = append(tree.twig.verts,
			Vector3.Add(
				Vector3.Add(branch.head, Vector3.MulX(tangent, -tree.TwigScale)),
				Vector3.MulX(binormal, tree.TwigScale*2-branch.length),
			),
		)
		var vert6 = int32(len(tree.twig.verts))
		tree.twig.verts = append(tree.twig.verts,
			Vector3.Add(
				Vector3.Add(branch.head, Vector3.MulX(tangent, -tree.TwigScale)),
				Vector3.MulX(binormal, -branch.length),
			),
		)
		var vert5 = int32(len(tree.twig.verts))
		tree.twig.verts = append(tree.twig.verts,
			Vector3.Add(
				Vector3.Add(branch.head, Vector3.MulX(tangent, tree.TwigScale)),
				Vector3.MulX(binormal, -branch.length),
			),
		)
		tree.twig.faces = append(tree.twig.faces, Vector3i.New(vert1, vert2, vert3))
		tree.twig.faces = append(tree.twig.faces, Vector3i.New(vert4, vert1, vert3))
		tree.twig.faces = append(tree.twig.faces, Vector3i.New(vert6, vert7, vert8))
		tree.twig.faces = append(tree.twig.faces, Vector3i.New(vert6, vert8, vert5))
		normal = Vector3.Normalized(
			Vector3.Cross(
				Vector3.Sub(tree.twig.verts[vert1], tree.twig.verts[vert3]),
				Vector3.Sub(tree.twig.verts[vert2], tree.twig.verts[vert3]),
			),
		)
		var normal2 = Vector3.Normalized(
			Vector3.Cross(
				Vector3.Sub(tree.twig.verts[vert7], tree.twig.verts[vert6]),
				Vector3.Sub(tree.twig.verts[vert8], tree.twig.verts[vert6]),
			),
		)
		tree.twig.normals = append(tree.twig.normals, normal)
		tree.twig.normals = append(tree.twig.normals, normal)
		tree.twig.normals = append(tree.twig.normals, normal)
		tree.twig.normals = append(tree.twig.normals, normal)
		tree.twig.normals = append(tree.twig.normals, normal2)
		tree.twig.normals = append(tree.twig.normals, normal2)
		tree.twig.normals = append(tree.twig.normals, normal2)
		tree.twig.normals = append(tree.twig.normals, normal2)
		tree.twig.uvs = append(tree.twig.uvs, Vector2.New(0, 1))
		tree.twig.uvs = append(tree.twig.uvs, Vector2.New(1, 1))
		tree.twig.uvs = append(tree.twig.uvs, Vector2.New(1, 0))
		tree.twig.uvs = append(tree.twig.uvs, Vector2.New(0, 0))
		tree.twig.uvs = append(tree.twig.uvs, Vector2.New(0, 1))
		tree.twig.uvs = append(tree.twig.uvs, Vector2.New(1, 1))
		tree.twig.uvs = append(tree.twig.uvs, Vector2.New(1, 0))
		tree.twig.uvs = append(tree.twig.uvs, Vector2.New(0, 0))
	} else {
		tree.createTwigs(branch.child[0])
		tree.createTwigs(branch.child[1])
	}
}

func (tree *Tree) createForks(branch *branch, radius Float.X) {
	branch.radius = radius
	if radius > branch.length {
		radius = branch.length
	}
	var (
		segments     = int32(tree.segments)
		segmentAngle = Angle.Pi * 2 / Angle.Radians(segments)
	)
	if branch.parent == nil {
		//create the root of the tree
		var axis = Vector3.New(0, 1, 0)
		for i := int32(0); i < segments; i++ {
			var vec = vecAxisAngle(Vector3.New(-1, 0, 0), axis, -segmentAngle*Angle.Radians(i))
			branch.root = append(branch.root, int32(len(tree.mesh.verts)))
			tree.mesh.verts = append(tree.mesh.verts, Vector3.MulX(vec, radius/tree.RadiusFalloffRate))
		}
	}
	//cross the branches to get the left
	//add the branches to get the up
	if branch.child[0] != nil {
		var axis Vector3.XYZ
		if branch.parent != nil {
			axis = Vector3.Normalized(Vector3.Sub(branch.head, branch.parent.head))
		} else {
			axis = Vector3.Normalized(branch.head)
		}
		var axis1 = Vector3.Normalized(Vector3.Sub(branch.head, branch.child[0].head))
		var axis2 = Vector3.Normalized(Vector3.Sub(branch.head, branch.child[1].head))
		var tangent = Vector3.Normalized(Vector3.Cross(axis1, axis2))
		branch.tangent = tangent
		var (
			axis3     = Vector3.Normalized(Vector3.Cross(tangent, Vector3.Normalized(Vector3.Add(Vector3.MulX(axis1, -1), Vector3.MulX(axis2, -1)))))
			dir       = Vector3.XYZ{axis2.X, 0, axis2.Z}
			centerloc = Vector3.Add(branch.head, Vector3.MulX(dir, -tree.MaxRadius/2))
			scale     = tree.RadiusFalloffRate
		)
		if branch.child[0].trunk || branch.trunk {
			scale = 1 / tree.TaperRate
		}
		//main segment ring
		var (
			linch0 = int32(len(tree.mesh.verts))
		)
		branch.ring[0] = append(branch.ring[0], linch0)
		branch.ring[2] = append(branch.ring[2], linch0)
		tree.mesh.verts = append(tree.mesh.verts,
			Vector3.Add(centerloc, Vector3.MulX(tangent, radius*scale)))
		var (
			start = int32(len(tree.mesh.verts) - 1)
			d1    = vecAxisAngle(tangent, axis2, 1.57)
			d2    = Vector3.Normalized(Vector3.Cross(tangent, axis))
			s     = 1 / Vector3.Dot(d1, d2)
		)
		for i := int32(1); i < segments/2; i++ {
			var vec = vecAxisAngle(tangent, axis2, segmentAngle*Angle.Radians(i))
			branch.ring[0] = append(branch.ring[0], start+i)
			branch.ring[2] = append(branch.ring[2], start+i)
			vec = scaleInDirection(vec, d2, s)
			tree.mesh.verts = append(tree.mesh.verts,
				Vector3.Add(centerloc, Vector3.MulX(vec, radius*scale)))
		}
		var linch1 = int32(len(tree.mesh.verts))
		branch.ring[0] = append(branch.ring[0], linch1)
		branch.ring[1] = append(branch.ring[1], linch1)
		tree.mesh.verts = append(tree.mesh.verts,
			Vector3.Add(centerloc, Vector3.MulX(tangent, -radius*scale)))
		for i := segments/2 + 1; i < segments; i++ {
			var vec = vecAxisAngle(tangent, axis1, segmentAngle*Angle.Radians(i))
			branch.ring[0] = append(branch.ring[0], int32(len(tree.mesh.verts)))
			branch.ring[1] = append(branch.ring[1], int32(len(tree.mesh.verts)))

			tree.mesh.verts = append(tree.mesh.verts,
				Vector3.Add(centerloc, Vector3.MulX(vec, radius*scale)))
		}
		branch.ring[1] = append(branch.ring[1], linch0)
		branch.ring[2] = append(branch.ring[2], linch1)
		start = int32(len(tree.mesh.verts)) - 1
		for i := int32(1); i < segments/2; i++ {
			var (
				vec = vecAxisAngle(tangent, axis3, segmentAngle*Angle.Radians(i))
			)
			branch.ring[1] = append(branch.ring[1], start+i)
			branch.ring[2] = append(branch.ring[2], start+(segments/2-i))
			var (
				v = Vector3.MulX(vec, radius*scale)
			)
			tree.mesh.verts = append(tree.mesh.verts,
				Vector3.Add(centerloc, v))
		}
		//child radius is related to the brans direction and the length of the branch
		//var length0 = length(vector3.Sub(branch.head, branch.child[0].head))
		//var length1 = length(vector3.Sub(branch.head, branch.child[1].head))
		var radius0 = 1 * radius * tree.RadiusFalloffRate
		var radius1 = 1 * radius * tree.RadiusFalloffRate
		if branch.trunk {
			radius0 = radius * tree.TaperRate
		}
		tree.createForks(branch.child[0], radius0)
		tree.createForks(branch.child[1], radius1)
	} else {
		//add points for the ends of braches
		branch.end = int32(len(tree.mesh.verts))
		//branch.head=vector3.Add(branch.head,vector3.Mulf([this.properties.xBias,this.properties.yBias,this.properties.zBias],branch.length*3));
		tree.mesh.verts = append(tree.mesh.verts,
			branch.head,
		)
	}
}

type branch struct {
	head, tangent  Vector3.XYZ
	length, radius Float.X

	end  int32
	root []int32
	ring [3][]int32

	//Is this branch the main trunk of the tree?
	trunk bool

	child  [2]*branch
	parent *branch
}

func newBranch(head Vector3.XYZ, parent *branch) *branch {
	return &branch{
		head:   head,
		parent: parent,
		length: 1,
	}
}

func (b *branch) mirrorBranch(vec, norm Vector3.XYZ, properties *Tree) Vector3.XYZ {
	var v = Vector3.Cross(norm, Vector3.Cross(vec, norm))
	var s = properties.BranchFactor * Vector3.Dot(v, vec)
	return Vector3.New(vec.X-v.X*s, vec.Y-v.Y*s, vec.Z-v.Z*s)
}

func (bra *branch) split(level int, steps Float.X, properties *Tree, l1, l2 Float.X) {
	if l1 == 0 {
		l1 = 1
	}
	if l2 == 0 {
		l2 = 1
	}
	var rLevel = properties.Levels - level
	var po Vector3.XYZ
	if bra.parent != nil {
		po = bra.parent.head
	} else {
		bra.trunk = true
	}
	var so = bra.head
	var dir = Vector3.Normalized(Vector3.Sub(so, po))
	var (
		normal  = Vector3.Cross(dir, Vector3.New(dir.Z, dir.X, dir.Y))
		tangent = Vector3.Cross(dir, normal)
		r       = properties.random(Float.X(rLevel*10) + l1*5 + l2 + properties.Seed)
		//r2       = properties.random(rLevel*10 + l1*5 + l2 + 1 + properties.Seed)
		clumpmax = properties.ClumpMax
		clumpmin = properties.ClumpMin
	)
	var adj = Vector3.Add(Vector3.MulX(normal, r), Vector3.MulX(tangent, 1-r))
	if r > 0.5 {
		adj = Vector3.MulX(adj, -1)
	}
	var (
		clump  = (clumpmax-clumpmin)*r + clumpmin
		newdir = Vector3.Normalized(Vector3.Add(Vector3.MulX(adj, 1-clump), Vector3.MulX(dir, clump)))
	)
	var newdir2 = bra.mirrorBranch(newdir, dir, properties)
	if r > 0.5 {
		var tmp = newdir
		newdir = newdir2
		newdir2 = tmp
	}
	if steps > 0 {
		var angle = Angle.Radians(steps/properties.TreeSteps*2) * Angle.Pi * properties.TwistRate
		newdir2 = Vector3.Normalized(Vector3.New(Angle.Sin(angle), r, Angle.Cos(angle)))
	}
	var growAmount = Float.X(level*level/(properties.Levels*properties.Levels)) * properties.GrowAmount
	var dropAmount = Float.X(rLevel) * properties.DropAmount
	var sweepAmount = Float.X(rLevel) * properties.SweepAmount
	newdir = Vector3.Normalized(Vector3.Add(newdir, Vector3.New(sweepAmount, dropAmount+growAmount, 0)))
	newdir2 = Vector3.Normalized(Vector3.Add(newdir2, Vector3.New(sweepAmount, dropAmount+growAmount, 0)))
	var (
		head0 = Vector3.Add(so, Vector3.MulX(newdir, bra.length))
		head1 = Vector3.Add(so, Vector3.MulX(newdir2, bra.length))
	)
	bra.child[0] = newBranch(head0, bra)
	bra.child[1] = newBranch(head1, bra)
	bra.child[0].length = Float.Pow(bra.length, properties.LengthFalloffPower) * properties.LengthFalloffFactor
	bra.child[1].length = Float.Pow(bra.length, properties.LengthFalloffPower) * properties.LengthFalloffFactor
	if level > 0 {
		if steps > 0 {
			bra.child[0].head = Vector3.Add(bra.head,
				Vector3.New((r-0.5)*2*properties.TrunkKink, properties.ClimbRate, (r-0.5)*2*properties.TrunkKink))
			bra.child[0].trunk = true
			bra.child[0].length = bra.length * properties.TaperRate
			bra.child[0].split(level, steps-1, properties, l1+1, l2)
		} else {
			bra.child[0].split(level-1, 0, properties, l1+1, l2)
		}
		bra.child[1].split(level-1, 0, properties, l1, l2+1)
	}
}
