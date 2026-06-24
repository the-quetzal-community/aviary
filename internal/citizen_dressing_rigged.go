package internal

import (
	"fmt"
	"sort"
	"strings"

	"graphics.gd/classdb/StandardMaterial3D"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Vector2"
	"graphics.gd/variant/Vector3"

	"the.quetzal.community/aviary/internal/citizen"
)

// riggedDressing is one clothing item merged into the rigged body's ArrayMesh
// as a skinned surface. Geometry topology (indices + UVs) comes from the
// item's .obj; positions are reconstructed every rebuild from the body's
// current T-pose GLB verts via the .mhclo barycentric anchors; and each
// clothing vertex's bone binding is the (static) barycentric blend of its
// three body anchors' GLB skin weights, so it deforms with the animation.
type riggedDressing struct {
	mhclo    *citizen.MHClo
	indices  []int32
	uvs      []Vector2.XY
	material StandardMaterial3D.Instance
	// bones/weights: static 4-influence skinning per clothing vertex
	// (ArrayBones / ArrayWeights), blended from the body anchors at load.
	bones   []int32
	weights []float32
	// verts: per-rebuild GLB-space positions, one per clothing vertex.
	verts []Vector3.XYZ
	// normals: per-rebuild bind-pose normals computed from verts (the GPU
	// skins them along with the positions). Without these the clothing
	// surface renders unlit/flat.
	normals []Vector3.XYZ
	// restNormals: outward normals from the clothing's rest .obj geometry, in
	// base.obj space — drives the body-shrink direction during coverage
	// computation (negated: clothing→body). Static.
	restNormals []citizen.Vec3
}

// newRiggedDressing loads a clothing item (.obj + sibling .mhclo) and computes
// its skinning blend against the rigged body. The caller stores it in
// b.riggedDressings and triggers a rebuild to surface it.
func newRiggedDressing(objPath string, b *CitizenBody) (*riggedDressing, error) {
	objText, err := openCitizenText(objPath)
	if err != nil {
		return nil, err
	}
	obj, err := citizen.ParseOBJ(objPath, strings.NewReader(objText))
	if err != nil {
		return nil, err
	}
	mhcloPath := mhcloSidecarPath(objPath)
	mhcloText, err := openCitizenText(mhcloPath)
	if err != nil {
		return nil, err
	}
	mhclo, err := citizen.ParseMHClo(mhcloPath, strings.NewReader(mhcloText))
	if err != nil {
		return nil, err
	}
	if len(mhclo.Anchors) != len(obj.Verts) {
		return nil, fmt.Errorf("citizen: %s has %d verts but %s has %d anchors",
			objPath, len(obj.Verts), mhcloPath, len(mhclo.Anchors))
	}
	d := &riggedDressing{
		mhclo:       mhclo,
		indices:     obj.Indices,
		material:    loadDressingMaterial(objPath),
		restNormals: clothRestNormals(obj.Verts, obj.Indices),
	}
	if len(obj.UVs) == len(obj.Verts) {
		d.uvs = make([]Vector2.XY, len(obj.UVs))
		for i, uv := range obj.UVs {
			d.uvs[i] = Vector2.XY{X: Float.X(uv.U), Y: Float.X(uv.V)}
		}
	}
	d.blendWeights(b.rig)
	d.refit(b)
	return d, nil
}

// blendWeights computes each clothing vertex's 4-influence skin binding as the
// barycentric blend of its three body anchors' GLB bone weights. Static —
// anchors and body skin weights don't change with body morphs.
func (d *riggedDressing) blendWeights(rig *citizen.CitizenRig) {
	n := len(d.mhclo.Anchors)
	d.bones = make([]int32, 4*n)
	d.weights = make([]float32, 4*n)
	for i, a := range d.mhclo.Anchors {
		acc := map[int32]float32{}
		for k := 0; k < 3; k++ {
			c := a.Verts[k]
			if int(c) < 0 || int(c) >= len(rig.CanonicalToGLB) {
				continue
			}
			g := rig.CanonicalToGLB[c]
			if g < 0 {
				continue
			}
			bw := a.Weights[k]
			for inf := 0; inf < 4; inf++ {
				jw := rig.Weights[g][inf]
				if jw <= 0 {
					continue
				}
				acc[int32(rig.Joints[g][inf])] += bw * jw
			}
		}
		top := topInfluences(acc)
		for k := 0; k < 4; k++ {
			d.bones[i*4+k] = top[k].bone
			d.weights[i*4+k] = top[k].weight
		}
	}
}

type boneWeight struct {
	bone   int32
	weight float32
}

// topInfluences keeps the 4 highest-weight bones from acc and normalises them
// to sum to 1. An empty/zero acc pins the vertex to bone 0 (the root) so it at
// least rides the whole skeleton rather than collapsing to the origin.
func topInfluences(acc map[int32]float32) [4]boneWeight {
	var out [4]boneWeight
	if len(acc) == 0 {
		out[0] = boneWeight{bone: 0, weight: 1}
		return out
	}
	for b, w := range acc {
		for k := 0; k < 4; k++ {
			if w > out[k].weight {
				copy(out[k+1:], out[k:3])
				out[k] = boneWeight{bone: b, weight: w}
				break
			}
		}
	}
	var sum float32
	for k := 0; k < 4; k++ {
		sum += out[k].weight
	}
	if sum > 0 {
		for k := 0; k < 4; k++ {
			out[k].weight /= sum
		}
	} else {
		out = [4]boneWeight{{bone: 0, weight: 1}}
	}
	return out
}

// refit recomputes the clothing's GLB-space positions from the body's current
// (morphed, straightened) T-pose verts: each clothing vertex is the
// barycentric blend of its three anchor body verts' GLB positions plus the
// .mhclo offset rotated into the T-pose (rig.Disp folds in the straighten
// rotation and the base→GLB scale).
func (d *riggedDressing) refit(b *CitizenBody) {
	rig := b.rig
	n := len(d.mhclo.Anchors)
	if len(d.verts) != n {
		d.verts = make([]Vector3.XYZ, n)
	}
	for i, a := range d.mhclo.Anchors {
		var px, py, pz float32
		repG := int32(-1)
		for k := 0; k < 3; k++ {
			c := a.Verts[k]
			if int(c) < 0 || int(c) >= len(rig.CanonicalToGLB) {
				continue
			}
			g := rig.CanonicalToGLB[c]
			if g < 0 {
				continue
			}
			if repG < 0 {
				repG = g
			}
			w := a.Weights[k]
			v := b.glbVerts[g]
			px += w * float32(v.X)
			py += w * float32(v.Y)
			pz += w * float32(v.Z)
		}
		var off citizen.Vec3
		if repG >= 0 {
			off = rig.Disp[repG].Mul3Vec(a.Offset)
		}
		d.verts[i] = Vector3.XYZ{X: Float.X(px + off.X), Y: Float.X(py + off.Y), Z: Float.X(pz + off.Z)}
	}
	d.normals = clothNormalsXYZ(d.verts, d.indices)
}

// clothNormalsXYZ computes smooth per-vertex normals from the deformed
// clothing geometry, reusing the citizen rest-normal helper (centroid auto-
// orient handles a hat/shell's winding). Reused every refit; clothing is
// re-surfaced only on morph/equip changes, not per animation frame.
func clothNormalsXYZ(verts []Vector3.XYZ, indices []int32) []Vector3.XYZ {
	cv := make([]citizen.Vec3, len(verts))
	for i, v := range verts {
		cv[i] = citizen.Vec3{X: float32(v.X), Y: float32(v.Y), Z: float32(v.Z)}
	}
	cn := clothRestNormals(cv, indices)
	out := make([]Vector3.XYZ, len(cn))
	for i, v := range cn {
		out[i] = Vector3.XYZ{X: Float.X(v.X), Y: Float.X(v.Y), Z: Float.X(v.Z)}
	}
	return out
}

// sortedDressingSlots returns the slot keys in a stable order so clothing
// surfaces (and their materials) keep consistent indices across re-surfaces.
func sortedDressingSlots(m map[string]*riggedDressing) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
