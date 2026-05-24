package internal

import (
	"fmt"
	"math"
	"strings"
	"sync"

	"graphics.gd/classdb/ArrayMesh"
	"graphics.gd/classdb/BaseMaterial3D"
	"graphics.gd/classdb/BoxShape3D"
	"graphics.gd/classdb/CollisionShape3D"
	"graphics.gd/classdb/FileAccess"
	"graphics.gd/classdb/Mesh"
	"graphics.gd/classdb/MeshInstance3D"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/StandardMaterial3D"
	"graphics.gd/classdb/StaticBody3D"
	"graphics.gd/variant/Color"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Vector2"
	"graphics.gd/variant/Vector3"

	"the.quetzal.community/aviary/internal/citizen"
)

// makeHumanBaseOnce + makeHumanBaseVerts cache the MakeHuman base
// mesh vertex array shared by every .mhclo we resolve — the file is
// ~10k verts and lives behind a FileAccess read, so loading it once
// per session is plenty.
var (
	makeHumanBaseOnce  sync.Once
	makeHumanBaseVerts []citizen.Vec3
)

func makeHumanBase() []citizen.Vec3 {
	makeHumanBaseOnce.Do(func() {
		const path = "res://library/makehuman/base.obj"
		f := FileAccess.Open(path, FileAccess.Read)
		if f == FileAccess.Nil {
			return
		}
		base, err := citizen.ParseOBJ(path, strings.NewReader(f.GetAsText()))
		if err != nil || base == nil {
			return
		}
		makeHumanBaseVerts = base.Verts
	})
	return makeHumanBaseVerts
}

// mhcloAnchorCentroid finds the body-relative attach point for a
// piece of MakeHuman clothing: the mean of the body vertices each
// clothing anchor references, weighted by the anchor's barycentric
// weights. For a hat this lands near the top of the head; for
// pants near the hips. Returns ok=false when either the base mesh
// hasn't been resolved yet or every anchor pointed past the end of
// the base array.
func mhcloAnchorCentroid(mhclo *citizen.MHClo) (citizen.Vec3, bool) {
	base := makeHumanBase()
	if len(base) == 0 || mhclo == nil || len(mhclo.Anchors) == 0 {
		return citizen.Vec3{}, false
	}
	var sum citizen.Vec3
	var totalWeight float32
	for _, a := range mhclo.Anchors {
		for k, vi := range a.Verts {
			if int(vi) < 0 || int(vi) >= len(base) {
				continue
			}
			w := a.Weights[k]
			v := base[vi]
			sum.X += v.X * w
			sum.Y += v.Y * w
			sum.Z += v.Z * w
			totalWeight += w
		}
	}
	if totalWeight <= 0 {
		return citizen.Vec3{}, false
	}
	inv := 1 / totalWeight
	return citizen.Vec3{X: sum.X * inv, Y: sum.Y * inv, Z: sum.Z * inv}, true
}

// loadStaticObjNode is the quick-and-dirty fallback for clothing
// items that ship as MakeHuman .obj + .mhclo pairs rather than as
// PackedScene-importable .glbs. It parses the .obj as a static
// mesh (no anchor weights, no body fitting) and returns a Node3D
// wrapping a MeshInstance3D with that mesh — usable as a preview
// or as the placed part.
//
// Path must end with ".obj". Returns Nil on failure (file missing,
// parse error, empty mesh) and prints a one-line diagnostic so the
// user can tell whether the fallback ran at all (since Godot's
// resource loader prints its own error for the PackedScene attempt
// just before this fires).
func loadStaticObjNode(objPath string) Node3D.Instance {
	if !strings.HasSuffix(objPath, ".obj") {
		return Node3D.Nil
	}
	f := FileAccess.Open(objPath, FileAccess.Read)
	if f == FileAccess.Nil {
		fmt.Println("critter dressing: FileAccess.Open failed for", objPath)
		return Node3D.Nil
	}
	base, err := citizen.ParseOBJ(objPath, strings.NewReader(f.GetAsText()))
	if err != nil || base == nil || len(base.Verts) == 0 || len(base.Indices) == 0 {
		fmt.Println("critter dressing: parse failed for", objPath, err)
		return Node3D.Nil
	}
	// Resolve the .mhclo (sibling file) and pull out the body-relative
	// attach point so the clothing's "wear-on" location lands at the
	// user's clicked anchor instead of at the .obj's bare world-space
	// origin. Without this, MakeHuman items render at human-world Y
	// (head ≈ 1.7 m) and float well above the much shorter critter.
	var shift citizen.Vec3
	mhcloPath := strings.TrimSuffix(objPath, ".obj") + ".mhclo"
	if mhcloFile := FileAccess.Open(mhcloPath, FileAccess.Read); mhcloFile != FileAccess.Nil {
		if mhclo, err := citizen.ParseMHClo(mhcloPath, strings.NewReader(mhcloFile.GetAsText())); err == nil {
			if c, ok := mhcloAnchorCentroid(mhclo); ok {
				shift = c
			}
		}
	}
	if shift == (citizen.Vec3{}) {
		// No .mhclo (or base mesh not yet loaded) — fall back to the
		// clothing mesh's own centroid so the item still sits at a
		// sensible reference point relative to the anchor.
		var cx, cy, cz float32
		for _, v := range base.Verts {
			cx += v.X
			cy += v.Y
			cz += v.Z
		}
		inv := 1 / float32(len(base.Verts))
		shift = citizen.Vec3{X: cx * inv, Y: cy * inv, Z: cz * inv}
	}
	// MakeHuman clothing items are authored at human scale (~1.8 m
	// figure), but the critter is roughly knee-high. Pre-scale the
	// vertex positions on top of the centroid shift so the rendered
	// item lands at a sensible size for the body it's worn on.
	const dressingScale = float32(0.125)

	// Orientation: derive the clothing's "outward" axis from the
	// vector between the body-anchor centroid (where the clothing
	// attaches to the body) and the clothing's own centroid (where
	// the bulk of the mesh sits). For a hat that vector points
	// straight up out of the head; for a shirt it's roughly
	// outward radially. The part-placement code (positionPart) aligns
	// each part's local +Z to the body-surface normal, so rotating
	// the clothing so that its outward direction becomes +Z lines
	// the item up with the surface it's about to attach to.
	clothingCentroid := citizen.Vec3{}
	for _, v := range base.Verts {
		clothingCentroid.X += v.X
		clothingCentroid.Y += v.Y
		clothingCentroid.Z += v.Z
	}
	inv := 1 / float32(len(base.Verts))
	clothingCentroid = citizen.Vec3{X: clothingCentroid.X * inv, Y: clothingCentroid.Y * inv, Z: clothingCentroid.Z * inv}
	outX := clothingCentroid.X - shift.X
	outY := clothingCentroid.Y - shift.Y
	outZ := clothingCentroid.Z - shift.Z
	outLen := float32(math.Sqrt(float64(outX*outX + outY*outY + outZ*outZ)))
	var rightX, rightY, rightZ, upX, upY, upZ float32
	rotate := outLen > 1e-4
	if rotate {
		outX /= outLen
		outY /= outLen
		outZ /= outLen
		// Pick a stable reference up — fall back to +Z when outward is
		// near-parallel with world up (rare for clothing but covers
		// pendants and similar designs whose anchor sits dead-centre
		// on the spine).
		refX, refY, refZ := float32(0), float32(1), float32(0)
		if outY > 0.99 || outY < -0.99 {
			refX, refY, refZ = 0, 0, 1
		}
		rightX = refY*outZ - refZ*outY
		rightY = refZ*outX - refX*outZ
		rightZ = refX*outY - refY*outX
		rLen := float32(math.Sqrt(float64(rightX*rightX + rightY*rightY + rightZ*rightZ)))
		if rLen > 0 {
			rightX /= rLen
			rightY /= rLen
			rightZ /= rLen
		}
		upX = outY*rightZ - outZ*rightY
		upY = outZ*rightX - outX*rightZ
		upZ = outX*rightY - outY*rightX
	}

	verts := make([]Vector3.XYZ, len(base.Verts))
	for i, v := range base.Verts {
		sx := v.X - shift.X
		sy := v.Y - shift.Y
		sz := v.Z - shift.Z
		if rotate {
			nx := sx*rightX + sy*rightY + sz*rightZ
			ny := sx*upX + sy*upY + sz*upZ
			nz := sx*outX + sy*outY + sz*outZ
			sx, sy, sz = nx, ny, nz
		}
		verts[i] = Vector3.XYZ{
			X: Float.X(sx * dressingScale),
			Y: Float.X(sy * dressingScale),
			Z: Float.X(sz * dressingScale),
		}
	}
	var uvs []Vector2.XY
	if len(base.UVs) == len(base.Verts) {
		uvs = make([]Vector2.XY, len(base.UVs))
		for i, uv := range base.UVs {
			uvs[i] = Vector2.XY{X: Float.X(uv.U), Y: Float.X(uv.V)}
		}
	}
	// Per-vertex normals are required for default lit materials —
	// without them the mesh ends up black-or-invisible. Accumulate
	// face normals into the verts the face references and normalise.
	normals := make([]Vector3.XYZ, len(verts))
	for i := 0; i+2 < len(base.Indices); i += 3 {
		ia, ib, ic := base.Indices[i], base.Indices[i+1], base.Indices[i+2]
		if int(ia) >= len(verts) || int(ib) >= len(verts) || int(ic) >= len(verts) {
			continue
		}
		a, b, c := verts[ia], verts[ib], verts[ic]
		ex, ey, ez := b.X-a.X, b.Y-a.Y, b.Z-a.Z
		fx, fy, fz := c.X-a.X, c.Y-a.Y, c.Z-a.Z
		nx := ey*fz - ez*fy
		ny := ez*fx - ex*fz
		nz := ex*fy - ey*fx
		normals[ia].X += nx
		normals[ia].Y += ny
		normals[ia].Z += nz
		normals[ib].X += nx
		normals[ib].Y += ny
		normals[ib].Z += nz
		normals[ic].X += nx
		normals[ic].Y += ny
		normals[ic].Z += nz
	}
	for i, n := range normals {
		l := Float.X(math.Sqrt(float64(n.X*n.X + n.Y*n.Y + n.Z*n.Z)))
		if l > 0 {
			normals[i] = Vector3.XYZ{X: n.X / l, Y: n.Y / l, Z: n.Z / l}
		} else {
			normals[i] = Vector3.XYZ{Y: 1}
		}
	}
	am := ArrayMesh.New()
	var arrays [Mesh.ArrayMax]any
	arrays[Mesh.ArrayVertex] = verts
	arrays[Mesh.ArrayNormal] = normals
	arrays[Mesh.ArrayIndex] = base.Indices
	if uvs != nil {
		arrays[Mesh.ArrayTexUv] = uvs
	}
	am.AddSurfaceFromArrays(Mesh.PrimitiveTriangles, arrays[:])

	root := Node3D.New()
	mi := MeshInstance3D.New()
	mi.SetMesh(am.AsMesh())
	// Prefer the MakeClothes-shipped diffuse texture (probed via
	// the same sibling-file convention the citizen editor uses) so
	// the clothing reads as intended; fall back to a flat beige if
	// the asset doesn't ship one.
	var mat StandardMaterial3D.Instance
	if textured := loadDressingMaterial(objPath); textured != StandardMaterial3D.Nil {
		mat = textured
	} else {
		mat = StandardMaterial3D.New()
		mat.AsBaseMaterial3D().SetAlbedoColor(Color.RGBA{R: 0.85, G: 0.78, B: 0.65, A: 1})
		mat.AsBaseMaterial3D().SetShadingMode(BaseMaterial3D.ShadingModeUnshaded)
	}
	mi.AsGeometryInstance3D().SetMaterialOverride(mat.AsMaterial())
	root.AsNode().AddChild(mi.AsNode())

	// AABB-based collider so a placed dressing item lands on the
	// global selection raycast (and the Delete handler can find it).
	// Box matches clothing shapes better than a sphere would and the
	// .mhclo + .obj metadata gives us all we need: the post-shift,
	// post-scale verts above are already in the local frame the
	// collider has to cover. Trimesh would be more accurate but
	// overkill for selection — clicking anywhere in the bounding
	// box is fine.
	if shape, ok := dressingBoxShape(verts); ok {
		body := StaticBody3D.New()
		col := CollisionShape3D.New()
		col.SetShape(shape.AsShape3D())
		body.AsNode().AddChild(col.AsNode())
		root.AsNode().AddChild(body.AsNode())
	}

	fmt.Println("critter dressing: loaded", objPath, "verts=", len(verts), "tris=", len(base.Indices)/3)
	return root
}

// dressingBoxShape returns a BoxShape3D sized to the AABB of the
// given vertices. ok=false when the input is empty (nothing to
// bound). The CollisionShape3D node it's wrapped in needs to be
// translated to the AABB centre by the caller — but since we leave
// the CollisionShape3D at its default origin, this routine also
// sizes the box so the natural (-extents, +extents) span hits the
// vertex range and returns the centre offset separately would be
// over-engineering; instead we set the box extents to fit the
// max(|min|, |max|) on each axis. Works fine when verts are
// roughly centred (true for our post-shift clothing).
func dressingBoxShape(verts []Vector3.XYZ) (BoxShape3D.Instance, bool) {
	if len(verts) == 0 {
		return BoxShape3D.Nil, false
	}
	minX, maxX := verts[0].X, verts[0].X
	minY, maxY := verts[0].Y, verts[0].Y
	minZ, maxZ := verts[0].Z, verts[0].Z
	for _, v := range verts[1:] {
		if v.X < minX {
			minX = v.X
		}
		if v.X > maxX {
			maxX = v.X
		}
		if v.Y < minY {
			minY = v.Y
		}
		if v.Y > maxY {
			maxY = v.Y
		}
		if v.Z < minZ {
			minZ = v.Z
		}
		if v.Z > maxZ {
			maxZ = v.Z
		}
	}
	// Pad each axis to max(|min|, |max|) so the box covers the verts
	// regardless of where the AABB centre is. Clamp to a minimum
	// size so degenerate (planar) meshes still get a clickable
	// volume.
	const minHalf = Float.X(0.005)
	hx := maxAbs(minX, maxX)
	hy := maxAbs(minY, maxY)
	hz := maxAbs(minZ, maxZ)
	if hx < minHalf {
		hx = minHalf
	}
	if hy < minHalf {
		hy = minHalf
	}
	if hz < minHalf {
		hz = minHalf
	}
	box := BoxShape3D.New()
	box.SetSize(Vector3.XYZ{X: hx * 2, Y: hy * 2, Z: hz * 2})
	return box, true
}

func maxAbs(a, b Float.X) Float.X {
	if a < 0 {
		a = -a
	}
	if b < 0 {
		b = -b
	}
	if a > b {
		return a
	}
	return b
}
