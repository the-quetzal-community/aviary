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
	"math"

	"grow.graphics/gd"
	"grow.graphics/xy/vector2"
	"grow.graphics/xy/vector3"
)

// Tree is a procedurally generated tree.
type Tree struct {
	gd.Class[Tree, gd.ArrayMesh] `gd:"AviaryTree"`

	Seed      gd.Float `gd:"seed" default:"10" range:"0,1000,or_greater,or_less"`
	Levels    gd.Int   `gd:"levels" default:"3" range:"1,7,or_greater"`
	TwigScale gd.Float `gd:"twig_scale" default:"2" range:"0,1,or_greater"`

	// Branching

	InitalBranchLength  gd.Float `gd:"initial_branch_length" default:"0.85" range:"0.1,1,or_greater"`
	LengthFalloffFactor gd.Float `gd:"length_falloff_factor" default:"0.85" range:"0.5,1,or_greater,or_less"`
	LengthFalloffPower  gd.Float `gd:"length_falloff_power" default:"1" range:"0.1,1.5,or_greater"`
	ClumpMin            gd.Float `gd:"clump_min" default:"0.8" range:"0,1"`
	ClumpMax            gd.Float `gd:"clump_max" default:"0.5" range:"0,1"`
	BranchFactor        gd.Float `gd:"branch_factor" default:"2" range:"2,4,or_greater"`
	DropAmount          gd.Float `gd:"drop_amount" range:"-1,1"`
	GrowAmount          gd.Float `gd:"grow_amount" range:"-0.5,1"`
	SweepAmount         gd.Float `gd:"sweep_amount" range:"-1,1"`

	// Trunk

	MaxRadius         gd.Float `gd:"max_radius" default:"0.25" range:"0.05,1,or_greater"`
	ClimbRate         gd.Float `gd:"climb_rate" default:"1.5" range:"0.05,1,or_greater"`
	TrunkKink         gd.Float `gd:"trunk_kink" range:"0,0.5,or_greater"`
	TreeSteps         gd.Float `gd:"tree_steps" range:"0,35,or_greater"`
	TaperRate         gd.Float `gd:"taper_rate" default:"0.95" range:"0.7,1,or_greater"`
	RadiusFalloffRate gd.Float `gd:"radius_falloff_rate" default:"0.6" range:"0.5,0.8"`
	TwistRate         gd.Float `gd:"twist_rate" default:"13" range:"0,10"`
	TrunkLength       gd.Float `gd:"trunk_length" default:"2.5" range:"0.1,5"`

	// Material
	VMultiplier gd.Float `gd:"v_multiplier" default:"0.2"`

	// Internal

	segments gd.Int `gd:"segments" default:"6"`

	recalculating bool

	root *branch

	random func(float64) float64

	mesh, twig buffer
}

type buffer struct {
	verts   []gd.Vector3
	faces   []gd.Vector3i
	normals []gd.Vector3
	uvs     []gd.Vector2
}

func (tree *Tree) OnCreate(godot gd.Context) {
	tree.random = func(a float64) float64 {
		return math.Abs(math.Cos(a + a*a))
	}
	tree.segments = 6
}

func (tree *Tree) OnSet(godot gd.Context, name gd.StringName, value gd.Variant) {
	if !tree.recalculating {
		godot.Callable(tree.recalculate).CallDeferred()
		tree.recalculating = true
	}
}

func (tree *Tree) OnFree(godot gd.Context) {
	tree.recalculating = false
}

func (tree *Tree) AsArrayMesh() gd.ArrayMesh { return *tree.Super() }

func (tree *Tree) recalculate(godot gd.Context) {
	if !tree.recalculating {
		return
	}
	tree.recalculating = false

	tree.mesh = buffer{}
	tree.twig = buffer{}
	tree.root = newBranch(vector3.New(0, tree.TrunkLength, 0), nil)
	tree.root.length = tree.InitalBranchLength
	tree.root.split(tree.Levels, tree.TreeSteps, tree, 0, 0)
	tree.createForks(tree.root, tree.MaxRadius)
	tree.createTwigs(tree.root)
	tree.doFaces(tree.root)
	tree.calcNormals()

	ArrayMesh := tree.AsArrayMesh()

	var restoreMats = false
	var mat1 gd.Material
	var mat2 gd.Material
	if ArrayMesh.AsMesh().GetSurfaceCount() > 0 {
		restoreMats = true
		mat1 = ArrayMesh.AsMesh().SurfaceGetMaterial(godot, 0)
		mat2 = ArrayMesh.AsMesh().SurfaceGetMaterial(godot, 1)
	}

	ArrayMesh.AsObject().SetBlockSignals(true)
	defer ArrayMesh.AsObject().SetBlockSignals(false)

	ArrayMesh.ClearSurfaces()
	{
		var vertices = godot.PackedVector3Array()
		for _, vertex := range tree.mesh.verts {
			vertices.Append(vertex)
		}
		var indicies = godot.PackedInt32Array()
		for _, index := range tree.mesh.faces {
			indicies.Append(int64(index[2]))
			indicies.Append(int64(index[1]))
			indicies.Append(int64(index[0]))
		}
		var normals = godot.PackedVector3Array()
		for _, normal := range tree.mesh.normals {
			normals.Append(normal)
		}

		var arrays = godot.Array()
		arrays.Resize(int64(gd.MeshArrayMax))
		arrays.SetIndex(int64(gd.MeshArrayVertex), godot.Variant(vertices))
		arrays.SetIndex(int64(gd.MeshArrayIndex), godot.Variant(indicies))
		arrays.SetIndex(int64(gd.MeshArrayNormal), godot.Variant(normals))

		ArrayMesh.AddSurfaceFromArrays(gd.MeshPrimitiveTriangles, arrays, gd.NewArrayOf[gd.Array](godot), godot.Dictionary(), gd.MeshArrayFormatVertex)
	}
	{
		var vertices = godot.PackedVector3Array()
		for _, vertex := range tree.twig.verts {
			vertices.Append(vertex)
		}
		var indicies = godot.PackedInt32Array()
		for _, index := range tree.twig.faces {
			indicies.Append(int64(index[2]))
			indicies.Append(int64(index[1]))
			indicies.Append(int64(index[0]))
		}
		var normals = godot.PackedVector3Array()
		for _, normal := range tree.twig.normals {
			normals.Append(normal)
		}

		var arrays = godot.Array()
		arrays.Resize(int64(gd.MeshArrayMax))
		arrays.SetIndex(int64(gd.MeshArrayVertex), godot.Variant(vertices))
		arrays.SetIndex(int64(gd.MeshArrayIndex), godot.Variant(indicies))
		arrays.SetIndex(int64(gd.MeshArrayNormal), godot.Variant(normals))

		ArrayMesh.AddSurfaceFromArrays(gd.MeshPrimitiveTriangles, arrays, gd.NewArrayOf[gd.Array](godot), godot.Dictionary(), gd.MeshArrayFormatVertex)
	}
	if restoreMats {
		ArrayMesh.AsMesh().SurfaceSetMaterial(0, mat1)
		ArrayMesh.AsMesh().SurfaceSetMaterial(1, mat2)
	}
}

func scaleInDirection(vector, direction gd.Vector3, scale float64) gd.Vector3 {
	var currentMag = vector3.Dot(vector, direction)
	var change = vector3.Mulf(direction, currentMag*scale-currentMag)
	return vector3.Add(vector, change)
}

func vecAxisAngle(vec, axis gd.Vector3, angle float64) gd.Vector3 {
	var cosr = math.Cos(angle)
	var sinr = math.Sin(angle)
	return vector3.Add(
		vector3.Add(
			vector3.Mulf(vec, cosr),
			vector3.Mulf(vector3.Cross(axis, vec), sinr),
		),
		vector3.Mulf(axis, vector3.Dot(axis, vec)*(1-cosr)),
	)
}

type Leaves Tree

func (leaves Leaves) Indicies() []uint32 {
	tree := Tree(leaves)
	var retArray = make([]uint32, 0, len(tree.twig.faces)*3)
	for _, face := range tree.twig.faces {
		for _, index := range face {
			retArray = append(retArray, uint32(index))
		}
	}
	return retArray
}

func (leaves Leaves) UVs() []float32 {
	tree := Tree(leaves)
	var retArray = make([]float32, 0, len(tree.twig.uvs)*3)
	for _, uv := range tree.twig.uvs {
		for _, v := range uv {
			retArray = append(retArray, float32(v))
		}
	}
	return retArray
}

func (leaves Leaves) Vertices() []float32 {
	tree := Tree(leaves)
	var retArray = make([]float32, 0, len(tree.twig.verts)*3)
	for _, triangle := range tree.twig.verts {
		for _, vertex := range triangle {
			retArray = append(retArray, float32(vertex))
		}
	}
	return retArray
}

func (leaves Leaves) Normals() []float32 {
	tree := Tree(leaves)
	var retArray = make([]float32, 0, len(tree.twig.normals)*3)
	for _, vector := range tree.twig.normals {
		for _, v := range vector {
			retArray = append(retArray, float32(v))
		}
	}
	return retArray
}

func (tree Tree) Leaves() Leaves {
	return Leaves(tree)
}

func (tree Tree) Indicies() []uint32 {
	var retArray = make([]uint32, 0, len(tree.mesh.faces)*3)
	for _, face := range tree.mesh.faces {
		for _, index := range face {
			retArray = append(retArray, uint32(index))
		}
	}
	return retArray
}

func (tree Tree) UVs() []float32 {
	var retArray = make([]float32, 0, len(tree.mesh.uvs)*3)
	for _, uv := range tree.mesh.uvs {
		for _, v := range uv {
			retArray = append(retArray, float32(v))
		}
	}
	return retArray
}

func (tree Tree) Normals() []float32 {
	var retArray = make([]float32, 0, len(tree.mesh.normals)*3)
	for _, vector := range tree.mesh.normals {
		for _, v := range vector {
			retArray = append(retArray, float32(v))
		}
	}
	return retArray
}

func (tree *Tree) calcNormals() {
	var (
		allNormals = make([][]gd.Vector3, len(tree.mesh.verts))
	)
	for _, face := range tree.mesh.faces {
		var norm = vector3.Normalize(
			vector3.Cross(
				vector3.Sub(tree.mesh.verts[face[1]], tree.mesh.verts[face[2]]),
				vector3.Sub(tree.mesh.verts[face[1]], tree.mesh.verts[face[0]]),
			),
		)
		allNormals[face[0]] = append(allNormals[face[0]], norm)
		allNormals[face[1]] = append(allNormals[face[1]], norm)
		allNormals[face[2]] = append(allNormals[face[2]], norm)
	}
	tree.mesh.normals = make([]gd.Vector3, len(allNormals))
	for i := range allNormals {
		var total = gd.Vector3{0, 0, 0}
		var l = len(allNormals[i])
		for j := 0; j < l; j++ {
			total = vector3.Add(total, vector3.Mulf(allNormals[i][j], 1/float64(l)))
		}
		tree.mesh.normals[i] = total
	}
}

func (tree *Tree) doFaces(branch *branch) {
	var (
		segments = int32(tree.segments)
	)
	if branch.parent == nil {
		tree.mesh.uvs = make([]gd.Vector2, len(tree.mesh.verts))
		var tangent = vector3.Normalize(
			vector3.Cross(
				vector3.Sub(branch.child[0].head, branch.head),
				vector3.Sub(branch.child[1].head, branch.head),
			),
		)
		var normal = branch.head.Normalized()
		var angle = math.Acos(vector3.Dot(tangent, gd.Vector3{-1, 0, 0}))
		if vector3.Dot(vector3.Cross(gd.Vector3{-1, 0, 0}, tangent), normal) > 0 {
			angle = 2*math.Pi - angle
		}
		var (
			segOffset = int32(math.Round(angle / math.Pi / 2 * float64(segments)))
		)
		for i := int32(0); i < segments; i++ {
			var (
				v1 = branch.ring[0][i]
				v2 = branch.root[(i+segOffset+1)%segments]
				v3 = branch.root[(i+segOffset)%segments]
				v4 = branch.ring[0][(i+1)%segments]
			)
			tree.mesh.faces = append(tree.mesh.faces, gd.Vector3i{v1, v4, v3})
			tree.mesh.faces = append(tree.mesh.faces, gd.Vector3i{v1, v4, v3})
			tree.mesh.faces = append(tree.mesh.faces, gd.Vector3i{v4, v2, v3})
			tree.mesh.uvs[(i+segOffset)%segments] = vector2.New(math.Abs(float64(i)/float64(segments)-0.5)*2, 0)
			var (
				l = vector3.Length(vector3.Sub(tree.mesh.verts[branch.ring[0][i]], tree.mesh.verts[branch.root[(i+segOffset)%segments]])) * tree.VMultiplier
			)
			tree.mesh.uvs[branch.ring[0][i]] = vector2.New(math.Abs(float64(i)/float64(segments)-0.5)*2, l)
			tree.mesh.uvs[branch.ring[2][i]] = vector2.New(math.Abs(float64(i)/float64(segments)-0.5)*2, l)
		}
	}
	if branch.child[0].ring[0] != nil {
		var (
			segOffset0, segOffset1 int32
			match0, match1         float64
			first0, first1         bool = true, true
			v1                          = vector3.Normalize(vector3.Sub(tree.mesh.verts[branch.ring[1][0]], branch.head))
			v2                          = vector3.Normalize(vector3.Sub(tree.mesh.verts[branch.ring[2][0]], branch.head))
		)
		v1 = scaleInDirection(v1, vector3.Normalize(vector3.Sub(branch.child[0].head, branch.head)), 0)
		v2 = scaleInDirection(v2, vector3.Normalize(vector3.Sub(branch.child[1].head, branch.head)), 0)
		for i := int32(0); i < segments; i++ {
			var d = vector3.Normalize(vector3.Sub(tree.mesh.verts[branch.child[0].ring[0][i]], branch.child[0].head))
			var l = vector3.Dot(d, v1)
			if first0 || l > match0 {
				match0 = l
				segOffset0 = segments - i
				first0 = false

			}
			d = vector3.Normalize(vector3.Sub(tree.mesh.verts[branch.child[1].ring[0][i]], branch.child[1].head))
			l = vector3.Dot(d, v2)
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
			tree.mesh.faces = append(tree.mesh.faces, gd.Vector3i{v1, v4, v3})
			tree.mesh.faces = append(tree.mesh.faces, gd.Vector3i{v4, v2, v3})
			v1 = branch.child[1].ring[0][i]
			v2 = branch.ring[2][(i+segOffset1+1)%segments]
			v3 = branch.ring[2][(i+segOffset1)%segments]
			v4 = branch.child[1].ring[0][(i+1)%segments]
			tree.mesh.faces = append(tree.mesh.faces, gd.Vector3i{v1, v2, v3})
			tree.mesh.faces = append(tree.mesh.faces, gd.Vector3i{v1, v4, v2})
			var (
				len1 = vector3.Length(vector3.Sub(tree.mesh.verts[branch.child[0].ring[0][i]], tree.mesh.verts[branch.ring[1][(i+segOffset0)%segments]])) * UVScale
				uv1  = tree.mesh.uvs[branch.ring[1][(i+segOffset0-1)%segments]]
			)
			tree.mesh.uvs[branch.child[0].ring[0][i]] = vector2.New(float64(uv1[0]), float64(uv1[1])+len1*tree.VMultiplier)
			tree.mesh.uvs[branch.child[0].ring[2][i]] = vector2.New(float64(uv1[0]), float64(uv1[1])+len1*tree.VMultiplier)
			var (
				len2 = vector3.Length(vector3.Sub(tree.mesh.verts[branch.child[1].ring[0][i]], tree.mesh.verts[branch.ring[2][(i+segOffset1)%segments]])) * UVScale
				uv2  = tree.mesh.uvs[branch.ring[2][(i+segOffset1-1)%segments]]
			)
			tree.mesh.uvs[branch.child[1].ring[0][i]] = vector2.New(float64(uv2[0]), float64(uv2[1])+len2*tree.VMultiplier)
			tree.mesh.uvs[branch.child[1].ring[2][i]] = vector2.New(float64(uv2[0]), float64(uv2[1])+len2*tree.VMultiplier)
		}
		tree.doFaces(branch.child[0])
		tree.doFaces(branch.child[1])
	} else {
		for i := int32(0); i < segments; i++ {
			tree.mesh.faces = append(tree.mesh.faces, gd.Vector3i{branch.child[0].end, branch.ring[1][(i+1)%segments], branch.ring[1][i]})
			tree.mesh.faces = append(tree.mesh.faces, gd.Vector3i{branch.child[1].end, branch.ring[2][(i+1)%segments], branch.ring[2][i]})
			var (
				len = vector3.Length(vector3.Sub(tree.mesh.verts[branch.child[0].end], tree.mesh.verts[branch.ring[1][i]]))
			)
			tree.mesh.uvs[branch.child[0].end] = vector2.New(math.Abs(float64(i)/float64(segments)-1-0.5)*2, len*tree.VMultiplier)
			len = vector3.Length(vector3.Sub(tree.mesh.verts[branch.child[1].end], tree.mesh.verts[branch.ring[2][i]]))
			tree.mesh.uvs[branch.child[1].end] = vector2.New(math.Abs(float64(i)/float64(segments)-0.5)*2, len*tree.VMultiplier)
		}
	}
}

func (tree *Tree) createTwigs(branch *branch) {
	if branch.child[0] == nil {
		var tangent = vector3.Normalize(
			vector3.Cross(
				vector3.Sub(branch.parent.child[0].head, branch.parent.head),
				vector3.Sub(branch.parent.child[1].head, branch.parent.head),
			),
		)
		var (
			binormal = vector3.Normalize(vector3.Sub(branch.head, branch.parent.head))
			normal   = vector3.Cross(tangent, binormal)
		)
		//This can probably be factored into a loop.
		var vert1 = int32(len(tree.twig.verts))
		tree.twig.verts = append(tree.twig.verts,
			vector3.Add(
				vector3.Add(branch.head, vector3.Mulf(tangent, tree.TwigScale)),
				vector3.Mulf(binormal, tree.TwigScale*2-branch.length),
			),
		)
		var vert2 = int32(len(tree.twig.verts))
		tree.twig.verts = append(tree.twig.verts,
			vector3.Add(
				vector3.Add(branch.head, vector3.Mulf(tangent, -tree.TwigScale)),
				vector3.Mulf(binormal, tree.TwigScale*2-branch.length),
			),
		)
		var vert3 = int32(len(tree.twig.verts))
		tree.twig.verts = append(tree.twig.verts,
			vector3.Add(
				vector3.Add(branch.head, vector3.Mulf(tangent, -tree.TwigScale)),
				vector3.Mulf(binormal, -branch.length),
			),
		)
		var vert4 = int32(len(tree.twig.verts))
		tree.twig.verts = append(tree.twig.verts,
			vector3.Add(
				vector3.Add(branch.head, vector3.Mulf(tangent, tree.TwigScale)),
				vector3.Mulf(binormal, -branch.length),
			),
		)
		var vert8 = int32(len(tree.twig.verts))
		tree.twig.verts = append(tree.twig.verts,
			vector3.Add(
				vector3.Add(branch.head, vector3.Mulf(tangent, tree.TwigScale)),
				vector3.Mulf(binormal, tree.TwigScale*2-branch.length),
			),
		)
		var vert7 = int32(len(tree.twig.verts))
		tree.twig.verts = append(tree.twig.verts,
			vector3.Add(
				vector3.Add(branch.head, vector3.Mulf(tangent, -tree.TwigScale)),
				vector3.Mulf(binormal, tree.TwigScale*2-branch.length),
			),
		)
		var vert6 = int32(len(tree.twig.verts))
		tree.twig.verts = append(tree.twig.verts,
			vector3.Add(
				vector3.Add(branch.head, vector3.Mulf(tangent, -tree.TwigScale)),
				vector3.Mulf(binormal, -branch.length),
			),
		)
		var vert5 = int32(len(tree.twig.verts))
		tree.twig.verts = append(tree.twig.verts,
			vector3.Add(
				vector3.Add(branch.head, vector3.Mulf(tangent, tree.TwigScale)),
				vector3.Mulf(binormal, -branch.length),
			),
		)
		tree.twig.faces = append(tree.twig.faces, gd.Vector3i{vert1, vert2, vert3})
		tree.twig.faces = append(tree.twig.faces, gd.Vector3i{vert4, vert1, vert3})
		tree.twig.faces = append(tree.twig.faces, gd.Vector3i{vert6, vert7, vert8})
		tree.twig.faces = append(tree.twig.faces, gd.Vector3i{vert6, vert8, vert5})
		normal = vector3.Normalize(
			vector3.Cross(
				vector3.Sub(tree.twig.verts[vert1], tree.twig.verts[vert3]),
				vector3.Sub(tree.twig.verts[vert2], tree.twig.verts[vert3]),
			),
		)
		var normal2 = vector3.Normalize(
			vector3.Cross(
				vector3.Sub(tree.twig.verts[vert7], tree.twig.verts[vert6]),
				vector3.Sub(tree.twig.verts[vert8], tree.twig.verts[vert6]),
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
		tree.twig.uvs = append(tree.twig.uvs, gd.Vector2{0, 1})
		tree.twig.uvs = append(tree.twig.uvs, gd.Vector2{1, 1})
		tree.twig.uvs = append(tree.twig.uvs, gd.Vector2{1, 0})
		tree.twig.uvs = append(tree.twig.uvs, gd.Vector2{0, 0})
		tree.twig.uvs = append(tree.twig.uvs, gd.Vector2{0, 1})
		tree.twig.uvs = append(tree.twig.uvs, gd.Vector2{1, 1})
		tree.twig.uvs = append(tree.twig.uvs, gd.Vector2{1, 0})
		tree.twig.uvs = append(tree.twig.uvs, gd.Vector2{0, 0})
	} else {
		tree.createTwigs(branch.child[0])
		tree.createTwigs(branch.child[1])
	}
}

func (tree *Tree) createForks(branch *branch, radius float64) {
	branch.radius = radius
	if radius > branch.length {
		radius = branch.length
	}
	var (
		segments     = int32(tree.segments)
		segmentAngle = math.Pi * 2 / float64(segments)
	)
	if branch.parent == nil {
		//create the root of the tree
		var axis = gd.Vector3{0, 1, 0}
		for i := int32(0); i < segments; i++ {
			var vec = vecAxisAngle(gd.Vector3{-1, 0, 0}, axis, -segmentAngle*float64(i))
			branch.root = append(branch.root, int32(len(tree.mesh.verts)))
			tree.mesh.verts = append(tree.mesh.verts, vector3.Mulf(vec, radius/tree.RadiusFalloffRate))
		}
	}
	//cross the branches to get the left
	//add the branches to get the up
	if branch.child[0] != nil {
		var axis gd.Vector3
		if branch.parent != nil {
			axis = vector3.Normalize(vector3.Sub(branch.head, branch.parent.head))
		} else {
			axis = vector3.Normalize(branch.head)
		}
		var axis1 = vector3.Normalize(vector3.Sub(branch.head, branch.child[0].head))
		var axis2 = vector3.Normalize(vector3.Sub(branch.head, branch.child[1].head))
		var tangent = vector3.Normalize(vector3.Cross(axis1, axis2))
		branch.tangent = tangent
		var (
			axis3     = vector3.Normalize(vector3.Cross(tangent, vector3.Normalize(vector3.Add(vector3.Mulf(axis1, -1), vector3.Mulf(axis2, -1)))))
			dir       = gd.Vector3{axis2[0], 0, axis2[2]}
			centerloc = vector3.Add(branch.head, vector3.Mulf(dir, -tree.MaxRadius/2))
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
			vector3.Add(centerloc, vector3.Mulf(tangent, radius*scale)))
		var (
			start = int32(len(tree.mesh.verts) - 1)
			d1    = vecAxisAngle(tangent, axis2, 1.57)
			d2    = vector3.Normalize(vector3.Cross(tangent, axis))
			s     = 1 / vector3.Dot(d1, d2)
		)
		for i := int32(1); i < segments/2; i++ {
			var vec = vecAxisAngle(tangent, axis2, segmentAngle*float64(i))
			branch.ring[0] = append(branch.ring[0], start+i)
			branch.ring[2] = append(branch.ring[2], start+i)
			vec = scaleInDirection(vec, d2, s)
			tree.mesh.verts = append(tree.mesh.verts,
				vector3.Add(centerloc, vector3.Mulf(vec, radius*scale)))
		}
		var linch1 = int32(len(tree.mesh.verts))
		branch.ring[0] = append(branch.ring[0], linch1)
		branch.ring[1] = append(branch.ring[1], linch1)
		tree.mesh.verts = append(tree.mesh.verts,
			vector3.Add(centerloc, vector3.Mulf(tangent, -radius*scale)))
		for i := segments/2 + 1; i < segments; i++ {
			var vec = vecAxisAngle(tangent, axis1, segmentAngle*float64(i))
			branch.ring[0] = append(branch.ring[0], int32(len(tree.mesh.verts)))
			branch.ring[1] = append(branch.ring[1], int32(len(tree.mesh.verts)))

			tree.mesh.verts = append(tree.mesh.verts,
				vector3.Add(centerloc, vector3.Mulf(vec, radius*scale)))
		}
		branch.ring[1] = append(branch.ring[1], linch0)
		branch.ring[2] = append(branch.ring[2], linch1)
		start = int32(len(tree.mesh.verts)) - 1
		for i := int32(1); i < segments/2; i++ {
			var (
				vec = vecAxisAngle(tangent, axis3, segmentAngle*float64(i))
			)
			branch.ring[1] = append(branch.ring[1], start+i)
			branch.ring[2] = append(branch.ring[2], start+(segments/2-i))
			var (
				v = vector3.Mulf(vec, radius*scale)
			)
			tree.mesh.verts = append(tree.mesh.verts,
				vector3.Add(centerloc, v))
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
	head, tangent  gd.Vector3
	length, radius float64

	end  int32
	root []int32
	ring [3][]int32

	//Is this branch the main trunk of the tree?
	trunk bool

	child  [2]*branch
	parent *branch
}

func newBranch(head gd.Vector3, parent *branch) *branch {
	return &branch{
		head:   head,
		parent: parent,
		length: 1,
	}
}

func (b *branch) mirrorBranch(vec, norm gd.Vector3, properties *Tree) gd.Vector3 {
	var v = vector3.Cross(norm, vector3.Cross(vec, norm))
	var s = properties.BranchFactor * vector3.Dot(v, vec)
	return vector3.New(float64(vec[0])-float64(v[0])*s, float64(vec[1])-float64(v[1])*s, float64(vec[2])-float64(v[2])*s)
}

func (bra *branch) split(level gd.Int, steps float64, properties *Tree, l1, l2 float64) {
	if l1 == 0 {
		l1 = 1
	}
	if l2 == 0 {
		l2 = 1
	}
	var rLevel = properties.Levels - level
	var po gd.Vector3
	if bra.parent != nil {
		po = bra.parent.head
	} else {
		bra.trunk = true
	}
	var so = bra.head
	var dir = vector3.Normalize(vector3.Sub(so, po))
	var (
		normal  = vector3.Cross(dir, gd.Vector3{dir[2], dir[0], dir[1]})
		tangent = vector3.Cross(dir, normal)
		r       = properties.random(float64(rLevel*10) + l1*5 + l2 + properties.Seed)
		//r2       = properties.random(rLevel*10 + l1*5 + l2 + 1 + properties.Seed)
		clumpmax = properties.ClumpMax
		clumpmin = properties.ClumpMin
	)
	var adj = vector3.Add(vector3.Mulf(normal, r), vector3.Mulf(tangent, 1-r))
	if r > 0.5 {
		adj = vector3.Mulf(adj, -1)
	}
	var (
		clump  = (clumpmax-clumpmin)*r + clumpmin
		newdir = vector3.Normalize(vector3.Add(vector3.Mulf(adj, 1-clump), vector3.Mulf(dir, clump)))
	)
	var newdir2 = bra.mirrorBranch(newdir, dir, properties)
	if r > 0.5 {
		var tmp = newdir
		newdir = newdir2
		newdir2 = tmp
	}
	if steps > 0 {
		var angle = steps / properties.TreeSteps * 2 * math.Pi * properties.TwistRate
		newdir2 = vector3.Normalize(vector3.New(math.Sin(angle), r, math.Cos(angle)))
	}
	var growAmount = float64(level*level/(properties.Levels*properties.Levels)) * properties.GrowAmount
	var dropAmount = float64(rLevel) * properties.DropAmount
	var sweepAmount = float64(rLevel) * properties.SweepAmount
	newdir = vector3.Normalize(vector3.Add(newdir, gd.NewVector3(sweepAmount, dropAmount+growAmount, 0)))
	newdir2 = vector3.Normalize(vector3.Add(newdir2, gd.NewVector3(sweepAmount, dropAmount+growAmount, 0)))
	var (
		head0 = vector3.Add(so, vector3.Mulf(newdir, bra.length))
		head1 = vector3.Add(so, vector3.Mulf(newdir2, bra.length))
	)
	bra.child[0] = newBranch(head0, bra)
	bra.child[1] = newBranch(head1, bra)
	bra.child[0].length = math.Pow(bra.length, properties.LengthFalloffPower) * properties.LengthFalloffFactor
	bra.child[1].length = math.Pow(bra.length, properties.LengthFalloffPower) * properties.LengthFalloffFactor
	if level > 0 {
		if steps > 0 {
			bra.child[0].head = vector3.Add(bra.head,
				gd.NewVector3((r-0.5)*2*properties.TrunkKink, properties.ClimbRate, (r-0.5)*2*properties.TrunkKink))
			bra.child[0].trunk = true
			bra.child[0].length = bra.length * properties.TaperRate
			bra.child[0].split(level, steps-1, properties, l1+1, l2)
		} else {
			bra.child[0].split(level-1, 0, properties, l1+1, l2)
		}
		bra.child[1].split(level-1, 0, properties, l1, l2+1)
	}
}
