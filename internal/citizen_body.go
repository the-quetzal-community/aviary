package internal

import (
	"errors"
	"fmt"
	"math"
	"strings"

	"graphics.gd/classdb/ArrayMesh"
	"graphics.gd/classdb/FileAccess"
	"graphics.gd/classdb/Mesh"
	"graphics.gd/classdb/MeshInstance3D"
	"graphics.gd/classdb/Resource"
	"graphics.gd/classdb/StandardMaterial3D"
	"graphics.gd/classdb/Texture2D"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Vector2"
	"graphics.gd/variant/Vector3"

	"the.quetzal.community/aviary/internal/citizen"
)

// CitizenBody bridges a pure-Go citizen.Citizen to a Godot MeshInstance3D.
// It owns a persistent ArrayMesh that gets re-surfaced in place on every
// slider tick, and a pool of per-slot CitizenDressing instances for
// equipped clothing — each kept fitted to the body via MakeHuman's
// .mhclo barycentric vertex anchors.
type CitizenBody struct {
	citizen *citizen.Citizen
	// mesh is the editor-owned MeshInstance3D rendering this citizen.
	// Stored as Instance (not ID) and unexported so graphics.gd's
	// keepalive walker visits it via CitizenEditor → CitizenBody → this
	// field and calls Object.Use to mark it alive each frame.
	mesh MeshInstance3D.Instance
	// arrayMesh is the Resource backing the displayed mesh. It's
	// updated in place across slider events via ClearSurfaces +
	// AddSurfaceFromArrays. Kept alive via the same keepalive path so
	// graphics.gd's GC doesn't drop our last Unreference, which would
	// take Godot's refcount to 0 and destroy the mesh.
	arrayMesh ArrayMesh.Instance
	// baseIndices is the unmodified triangle index buffer from the
	// parsed base.obj — never mutated. `indices` is derived from
	// it on dressing change by culling triangles whose all-three
	// corners are deep enough inside the covered region that we
	// trust they'll never need to render.
	baseIndices []int32
	// indices is what surface() actually uses. Equal to baseIndices
	// when nothing is equipped; otherwise a subset that drops the
	// deep-interior triangles, leaving a one-vertex fringe at every
	// clothing boundary so the shrink-based fallback can handle the
	// edge without a halo.
	indices   []int32
	vertexBuf []Vector3.XYZ
	// neighbours is per-vertex adjacency derived from baseIndices,
	// built lazily on first need and cached. Used to erode the
	// covered set inward by one vertex spacing before deciding which
	// triangles to drop.
	neighbours [][]int32
	// dressings tracks the equipped clothing per slot. Each entry is a
	// pointer so refitDressing can mutate the buffer in place; the
	// graphics.gd keepalive walker follows pointer values in maps.
	dressings map[string]*CitizenDressing
	// shrinkDirs[i] is the unit-length direction to push body vert i
	// inward when clothing is equipped, derived from the averaged
	// .mhclo anchor offsets that reference this vert (negated, since
	// offsets point body→clothing and we want clothing→body). This is
	// more reliable than per-vertex body normals: MakeHuman's base.obj
	// has regions with inverted face winding (limbs vs. torso), so
	// computed body normals flip sign and a uniform "push along
	// normal" produces inconsistent results. Anchor offsets, by
	// contrast, are authored data and point consistently outward
	// across the whole mesh. Zero vec at verts no clothing anchors
	// into; those are skipped during shrink.
	shrinkDirs []citizen.Vec3
	// shrinkDirty triggers a re-shrink + re-surface on the next
	// CommitVisibility: AttachDressing flips it, CommitVisibility
	// runs the rebuild. Coalesces N startup-replay AttachDressings
	// into one rebuild per frame.
	shrinkDirty bool
}

// bodyShrinkAmount is how far we push anchored body verts inward
// along their shrinkDir. Typical .mhclo anchor offsets are 0.05–0.15
// in body-space units (where the hm08 mesh spans ±8), so 0.05 keeps
// the body strictly inside every reasonable clothing surface without
// being visually noticeable in regions where no clothing is equipped
// — those verts aren't shrunk at all.
const bodyShrinkAmount = 0.05

// CitizenDressing is one equipped clothing item — its own
// MeshInstance3D in the scene tree, an ArrayMesh resource we re-surface
// when the body deforms, and the MHClo fit data that maps each clothing
// vertex onto three body vertices. material is a per-item
// StandardMaterial3D applied as a surface override so it survives the
// ArrayMesh.ClearSurfaces/AddSurfaceFromArrays cycle in refit().
type CitizenDressing struct {
	mi        MeshInstance3D.Instance
	arrayMesh ArrayMesh.Instance
	material  StandardMaterial3D.Instance
	mhclo     *citizen.MHClo
	indices   []int32
	buf       []Vector3.XYZ
	// uvs is nil when the source .obj had no `vt` references; otherwise
	// one UV per vertex in buf. Static across body deformations — refit
	// only moves positions.
	uvs []Vector2.XY
	// restNormals is the per-vertex outward normal computed from the
	// clothing's rest (pre-fit) geometry. We use these in the body
	// shrink direction calculation: clothing surface normals point
	// outward, so negating them gives a reliable inward direction
	// even for 1-field .mhclo anchors that have no offset. Static —
	// clothing topology doesn't change with body deformation.
	restNormals []citizen.Vec3
}

// AttachCitizenBody creates a fresh ArrayMesh from the parsed base mesh,
// sets it on the supplied MeshInstance3D, and returns a body that drives
// it from the runtime delta application.
func AttachCitizenBody(mi MeshInstance3D.Instance, base *citizen.BaseMesh, targets []*citizen.Target) (CitizenBody, error) {
	if mi == MeshInstance3D.Nil {
		return CitizenBody{}, errors.New("citizen: nil MeshInstance3D")
	}
	if base == nil || len(base.Verts) == 0 {
		return CitizenBody{}, errors.New("citizen: empty base mesh")
	}
	vbuf := make([]Vector3.XYZ, len(base.Verts))
	for i, v := range base.Verts {
		vbuf[i] = Vector3.XYZ{X: Float.X(v.X), Y: Float.X(v.Y), Z: Float.X(v.Z)}
	}
	baseCopy := make([]citizen.Vec3, len(base.Verts))
	copy(baseCopy, base.Verts)
	c := citizen.New(baseCopy)
	c.AddTargets(targets)
	body := CitizenBody{
		citizen:     c,
		mesh:        mi,
		arrayMesh:   ArrayMesh.New(),
		baseIndices: base.Indices,
		indices:     base.Indices,
		vertexBuf:   vbuf,
		dressings:   make(map[string]*CitizenDressing),
	}
	body.surface()
	mi.SetMesh(body.arrayMesh.AsMesh())
	return body, nil
}

// Citizen exposes the underlying pure-Go state for direct querying
// (current weights, target catalogue, etc.).
func (b *CitizenBody) Citizen() *citizen.Citizen { return b.citizen }

// SetWeight updates a slider and rebuilds the displayed mesh if the value
// actually changed. Pass 0 to clear.
func (b *CitizenBody) SetWeight(name string, weight float32) {
	if !b.citizen.SetWeight(name, weight) {
		return
	}
	b.rebuild()
}

func (b *CitizenBody) rebuild() {
	if b.arrayMesh == ArrayMesh.Nil {
		return
	}
	body := b.citizen.Recompute()
	b.writeShrunkVertexBuf(body)
	b.arrayMesh.ClearSurfaces()
	b.surface()
	for _, d := range b.dressings {
		d.refit(body)
	}
}

// writeShrunkVertexBuf copies the pure body positions into vertexBuf,
// pushing any vert with a non-zero shrinkDir inward by
// bodyShrinkAmount along that direction. Clothing items still fit
// against the unmodified body, so the gap between body surface and
// clothing surface in anchored regions becomes (authored offset +
// bodyShrinkAmount) along the same direction — strictly positive
// even on items with small authored offsets, so the depth test
// always renders clothing in front of body.
func (b *CitizenBody) writeShrunkVertexBuf(body []citizen.Vec3) {
	if len(b.shrinkDirs) == 0 {
		for i, v := range body {
			b.vertexBuf[i] = Vector3.XYZ{
				X: Float.X(v.X), Y: Float.X(v.Y), Z: Float.X(v.Z),
			}
		}
		return
	}
	for i, v := range body {
		x, y, z := v.X, v.Y, v.Z
		if i < len(b.shrinkDirs) {
			d := b.shrinkDirs[i]
			if d.X != 0 || d.Y != 0 || d.Z != 0 {
				x += d.X * bodyShrinkAmount
				y += d.Y * bodyShrinkAmount
				z += d.Z * bodyShrinkAmount
			}
		}
		b.vertexBuf[i] = Vector3.XYZ{X: Float.X(x), Y: Float.X(y), Z: Float.X(z)}
	}
}

// (updateShrinkDirs is now merged into updateCulledIndices — coverage,
// shrink direction, and the index cull all come from the same
// proximity scan so every covered body vert gets a direction.)

// clothRestNormals computes smooth per-vertex outward normals from a
// clothing item's rest-pose geometry (the .obj verts and the
// triangulated index buffer we parsed). Each triangle contributes its
// (b-a) × (c-a) face normal to its three vertices; we then renormalise.
// CCW winding from outside ⇒ outward normal — the citizen OBJ parser
// flips winding so this assumption holds for everything that goes
// through it. Computed once per item at load and reused; clothing
// topology doesn't change with body deformation, so the rest-pose
// direction stays a fair proxy for the deformed-pose direction even
// after refitting against a slider-modified body.
func clothRestNormals(verts []citizen.Vec3, indices []int32) []citizen.Vec3 {
	n := make([]citizen.Vec3, len(verts))
	for i := 0; i+2 < len(indices); i += 3 {
		ia, ib, ic := indices[i], indices[i+1], indices[i+2]
		if int(ia) >= len(verts) || int(ib) >= len(verts) || int(ic) >= len(verts) {
			continue
		}
		a, b, c := verts[ia], verts[ib], verts[ic]
		ex, ey, ez := b.X-a.X, b.Y-a.Y, b.Z-a.Z
		fx, fy, fz := c.X-a.X, c.Y-a.Y, c.Z-a.Z
		nx := ey*fz - ez*fy
		ny := ez*fx - ex*fz
		nz := ex*fy - ey*fx
		n[ia].X += nx
		n[ia].Y += ny
		n[ia].Z += nz
		n[ib].X += nx
		n[ib].Y += ny
		n[ib].Z += nz
		n[ic].X += nx
		n[ic].Y += ny
		n[ic].Z += nz
	}
	for i, v := range n {
		l := float32(math.Sqrt(float64(v.X*v.X + v.Y*v.Y + v.Z*v.Z)))
		if l > 0 {
			n[i] = citizen.Vec3{X: v.X / l, Y: v.Y / l, Z: v.Z / l}
		}
	}
	// Auto-detect normal orientation per item. Different clothing
	// authors export with different winding conventions; my fan-
	// triangulation flip assumes CW-from-outside (true for MakeHuman
	// base.obj and most MakeClothes exports), but some items end up
	// with CCW-from-outside originally and consequently inverted
	// normals after the flip. To detect, compute the mesh centroid
	// and check at each vert whether (vert - centroid) · normal is
	// positive: a correctly-outward normal points away from the
	// centroid, so the majority sign tells us the convention. If
	// negative dominates, flip every normal.
	if len(verts) > 0 {
		var cx, cy, cz float32
		for _, v := range verts {
			cx += v.X
			cy += v.Y
			cz += v.Z
		}
		inv := float32(1) / float32(len(verts))
		cx *= inv
		cy *= inv
		cz *= inv
		var pos, neg int
		for i, v := range verts {
			nrm := n[i]
			if nrm.X == 0 && nrm.Y == 0 && nrm.Z == 0 {
				continue
			}
			if (v.X-cx)*nrm.X+(v.Y-cy)*nrm.Y+(v.Z-cz)*nrm.Z > 0 {
				pos++
			} else {
				neg++
			}
		}
		if neg > pos {
			for i := range n {
				n[i] = citizen.Vec3{X: -n[i].X, Y: -n[i].Y, Z: -n[i].Z}
			}
		}
	}
	return n
}

// surface (re)constructs the arrayMesh's surface 0 from the current
// vertexBuf and the current (possibly culled) index buffer. Caller
// should ClearSurfaces() before calling this on a mesh that already
// has surfaces.
func (b *CitizenBody) surface() {
	var arrays [Mesh.ArrayMax]any
	arrays[Mesh.ArrayVertex] = b.vertexBuf
	arrays[Mesh.ArrayIndex] = b.indices
	b.arrayMesh.AddSurfaceFromArrays(Mesh.PrimitiveTriangles, arrays[:])
}

// AttachDressing equips or replaces clothing in a slot. The design is a
// res:// path to a .obj file (the design explorer points at this via
// the preview .obj.png convention). The sibling .mhclo file is loaded
// to drive runtime fitting against body deformations. Pass design ""
// to unequip the slot.
func (b *CitizenBody) AttachDressing(slot, design string) {
	if !b.citizen.SetDressing(slot, design) {
		return
	}
	if existing, ok := b.dressings[slot]; ok {
		existing.mi.AsNode().QueueFree()
		delete(b.dressings, slot)
	}
	if design != "" {
		d, err := loadCitizenDressing(design)
		if err != nil {
			fmt.Println("citizen: dressing load failed:", err)
			return
		}
		d.refit(b.citizen.Recompute())
		b.mesh.AsNode().AddChild(d.mi.AsNode())
		b.dressings[slot] = d
	}
	b.shrinkDirty = true
}

// CommitVisibility re-runs the dressing-derived state if any
// AttachDressing happened since the last commit: shrink directions
// for the boundary, culled index buffer for the deep interior, and
// a full rebuild to apply both. Called once per editor frame so a
// burst of AttachDressings (e.g. replaying scene history at startup)
// collapses to one update.
func (b *CitizenBody) CommitVisibility() {
	if !b.shrinkDirty {
		return
	}
	b.shrinkDirty = false
	b.updateCoverageAndShrink()
	b.rebuild()
}

// updateCoverageAndShrink walks every equipped item's fitted clothing
// and, for each body vert in any item's AABB, finds all clothing
// verts within a per-item threshold. Each hit marks the body vert
// covered AND accumulates the negated clothing surface normal at the
// hit clothing vert into shrinkDirs[bodyVert]. Result: every body
// vert covered by proximity also gets a non-zero shrink direction —
// including covered verts that no .mhclo anchor weights into (the
// heel-of-shoe case where 1-field anchors are sparse).
//
// We then erode the covered set by one vertex spacing and use the
// eroded interior to cull the body's index buffer: triangles whose
// all-three corners are deep-interior disappear, leaving a one-
// vertex fringe at the clothing boundary that the shrink moves
// inward.
func (b *CitizenBody) updateCoverageAndShrink() {
	dirs := make([]citizen.Vec3, len(b.vertexBuf))
	if len(b.dressings) == 0 {
		b.shrinkDirs = dirs
		b.indices = b.baseIndices
		return
	}
	body := b.citizen.Recompute()
	covered := make([]bool, len(body))
	var clothBuf []citizen.Vec3
	for _, d := range b.dressings {
		if d.mhclo == nil || len(d.restNormals) == 0 {
			continue
		}
		clothBuf = d.mhclo.Fit(body, clothBuf)
		markCoveredWithDirs(covered, dirs, body, clothBuf, d.restNormals)
	}
	for i, v := range dirs {
		l := float32(math.Sqrt(float64(v.X*v.X + v.Y*v.Y + v.Z*v.Z)))
		if l > 0 {
			dirs[i] = citizen.Vec3{X: v.X / l, Y: v.Y / l, Z: v.Z / l}
		}
	}
	b.shrinkDirs = dirs
	eroded := b.erodeCovered(covered)
	out := make([]int32, 0, len(b.baseIndices))
	for i := 0; i+2 < len(b.baseIndices); i += 3 {
		a, b1, c := b.baseIndices[i], b.baseIndices[i+1], b.baseIndices[i+2]
		if int(a) < len(eroded) && int(b1) < len(eroded) && int(c) < len(eroded) &&
			eroded[a] && eroded[b1] && eroded[c] {
			continue
		}
		out = append(out, a, b1, c)
	}
	b.indices = out
}

// erodeCovered returns covered with all boundary verts unset (any
// hidden vert with a non-hidden neighbour). Single pass — combined
// with the all-three-hidden triangle rule, that leaves a single body
// triangle fringe past the culled interior for the shrink to deal
// with.
func (b *CitizenBody) erodeCovered(covered []bool) []bool {
	neigh := b.vertexNeighbours()
	if neigh == nil {
		return covered
	}
	out := make([]bool, len(covered))
	for v, h := range covered {
		if !h {
			continue
		}
		interior := true
		for _, n := range neigh[v] {
			if !covered[n] {
				interior = false
				break
			}
		}
		out[v] = interior
	}
	return out
}

// vertexNeighbours builds and caches the per-vertex adjacency list.
// Each entry lists every other body vertex that shares a triangle
// with it, deduplicated. Derived from baseIndices (not the culled
// indices) so adjacency reflects the body's actual topology.
func (b *CitizenBody) vertexNeighbours() [][]int32 {
	if b.neighbours != nil {
		return b.neighbours
	}
	if len(b.baseIndices) == 0 || len(b.vertexBuf) == 0 {
		return nil
	}
	sets := make([]map[int32]struct{}, len(b.vertexBuf))
	add := func(a, c int32) {
		if int(a) >= len(sets) {
			return
		}
		if sets[a] == nil {
			sets[a] = map[int32]struct{}{}
		}
		sets[a][c] = struct{}{}
	}
	for i := 0; i+2 < len(b.baseIndices); i += 3 {
		a, b1, c := b.baseIndices[i], b.baseIndices[i+1], b.baseIndices[i+2]
		add(a, b1)
		add(a, c)
		add(b1, a)
		add(b1, c)
		add(c, a)
		add(c, b1)
	}
	out := make([][]int32, len(sets))
	for i, s := range sets {
		if len(s) == 0 {
			continue
		}
		ns := make([]int32, 0, len(s))
		for k := range s {
			ns = append(ns, k)
		}
		out[i] = ns
	}
	b.neighbours = out
	return out
}

// markCoveredWithDirs scans each body vert that falls in the
// clothing's AABB and accumulates `-clothNormal` for every clothing
// vert within the per-item threshold. Body verts with ≥1 hit get
// covered[bi] = true and a non-zero dirs[bi]. Threshold is
// bbox_diagonal/sqrt(N), so sparse hats and dense pants each get a
// sensible inclusion radius. Caller renormalises dirs after all
// items are processed.
//
// Crucially this does NOT early-exit on first hit: covered verts
// with a single nearby clothing vert get one normal added, ones
// surrounded by many get a smoothed average — which is exactly the
// right behaviour for boundary shrink direction.
func markCoveredWithDirs(covered []bool, dirs, body, cloth, clothNormals []citizen.Vec3) {
	if len(cloth) == 0 || len(clothNormals) != len(cloth) {
		return
	}
	minP, maxP := cloth[0], cloth[0]
	for _, v := range cloth[1:] {
		if v.X < minP.X {
			minP.X = v.X
		} else if v.X > maxP.X {
			maxP.X = v.X
		}
		if v.Y < minP.Y {
			minP.Y = v.Y
		} else if v.Y > maxP.Y {
			maxP.Y = v.Y
		}
		if v.Z < minP.Z {
			minP.Z = v.Z
		} else if v.Z > maxP.Z {
			maxP.Z = v.Z
		}
	}
	sx, sy, sz := maxP.X-minP.X, maxP.Y-minP.Y, maxP.Z-minP.Z
	diag := float32(math.Sqrt(float64(sx*sx + sy*sy + sz*sz)))
	threshold := diag / float32(math.Sqrt(float64(len(cloth))))
	thresholdSq := threshold * threshold
	bbMinX, bbMaxX := minP.X-threshold, maxP.X+threshold
	bbMinY, bbMaxY := minP.Y-threshold, maxP.Y+threshold
	bbMinZ, bbMaxZ := minP.Z-threshold, maxP.Z+threshold
	for bi, bv := range body {
		if bv.X < bbMinX || bv.X > bbMaxX ||
			bv.Y < bbMinY || bv.Y > bbMaxY ||
			bv.Z < bbMinZ || bv.Z > bbMaxZ {
			continue
		}
		for ci, cv := range cloth {
			dx := bv.X - cv.X
			dy := bv.Y - cv.Y
			dz := bv.Z - cv.Z
			if dx*dx+dy*dy+dz*dz < thresholdSq {
				covered[bi] = true
				n := clothNormals[ci]
				dirs[bi].X -= n.X
				dirs[bi].Y -= n.Y
				dirs[bi].Z -= n.Z
			}
		}
	}
}

// loadCitizenDressing loads a clothing item's .obj geometry and .mhclo
// fit data from the asset library, builds a fresh MeshInstance3D +
// ArrayMesh pair, and returns the runtime state needed to refit it on
// every body rebuild. The caller positions the MeshInstance3D in the
// scene tree.
func loadCitizenDressing(objPath string) (*CitizenDressing, error) {
	mhcloPath := strings.TrimSuffix(objPath, ".obj") + ".mhclo"
	objFile := FileAccess.Open(objPath, FileAccess.Read)
	if objFile == FileAccess.Nil {
		return nil, fmt.Errorf("citizen: cannot open %s", objPath)
	}
	base, err := citizen.ParseOBJ(objPath, strings.NewReader(objFile.GetAsText()))
	if err != nil {
		return nil, err
	}
	mhcloFile := FileAccess.Open(mhcloPath, FileAccess.Read)
	if mhcloFile == FileAccess.Nil {
		return nil, fmt.Errorf("citizen: cannot open %s", mhcloPath)
	}
	mhclo, err := citizen.ParseMHClo(mhcloPath, strings.NewReader(mhcloFile.GetAsText()))
	if err != nil {
		return nil, err
	}
	if len(mhclo.Anchors) != len(base.Verts) {
		return nil, fmt.Errorf("citizen: %s has %d verts but %s has %d anchors",
			objPath, len(base.Verts), mhcloPath, len(mhclo.Anchors))
	}
	d := &CitizenDressing{
		mi:          MeshInstance3D.New(),
		arrayMesh:   ArrayMesh.New(),
		mhclo:       mhclo,
		indices:     base.Indices,
		buf:         make([]Vector3.XYZ, len(base.Verts)),
		restNormals: clothRestNormals(base.Verts, base.Indices),
	}
	if len(base.UVs) == len(base.Verts) {
		d.uvs = make([]Vector2.XY, len(base.UVs))
		for i, uv := range base.UVs {
			d.uvs[i] = Vector2.XY{X: Float.X(uv.U), Y: Float.X(uv.V)}
		}
	}
	d.mi.SetMesh(d.arrayMesh.AsMesh())
	// Load the material but defer applying it as a surface override
	// until after refit() adds surface 0 — setting an override for a
	// surface index that doesn't yet exist gets dropped on the floor.
	d.material = loadDressingMaterial(objPath)
	return d, nil
}

// loadDressingMaterial looks for a sibling diffuse texture written by
// import_makehuman_clothes.sh as `<item>.diffuse.<ext>` and returns a
// StandardMaterial3D using it as albedo. Returns Nil if no texture is
// present — the import script only writes one when the source asset
// shipped an .mhmat with a diffuseTexture. We probe via `.import`
// sidecars rather than the .png itself: Godot's exporter strips the
// original .png from PCKs (keeping only the imported .ctex), so a
// FileAccess check on the .png would miss assets that load fine via
// Resource.Load.
func loadDressingMaterial(objPath string) StandardMaterial3D.Instance {
	base := strings.TrimSuffix(objPath, ".obj")
	for _, ext := range []string{"png", "jpg", "jpeg"} {
		path := base + ".diffuse." + ext
		if !FileAccess.FileExists(path + ".import") {
			continue
		}
		tex := Resource.Load[Texture2D.Instance](path)
		if tex == Texture2D.Nil {
			continue
		}
		mat := StandardMaterial3D.New()
		mat.AsBaseMaterial3D().SetAlbedoTexture(tex)
		return mat
	}
	return StandardMaterial3D.Nil
}

// refit recomputes this clothing's vertex positions from the current
// body vertices and rebuilds its surface in place.
func (d *CitizenDressing) refit(body []citizen.Vec3) {
	fitted := d.mhclo.Fit(body, nil)
	if len(d.buf) != len(fitted) {
		d.buf = make([]Vector3.XYZ, len(fitted))
	}
	for i, v := range fitted {
		d.buf[i] = Vector3.XYZ{
			X: Float.X(v.X), Y: Float.X(v.Y), Z: Float.X(v.Z),
		}
	}
	d.arrayMesh.ClearSurfaces()
	var arrays [Mesh.ArrayMax]any
	arrays[Mesh.ArrayVertex] = d.buf
	arrays[Mesh.ArrayIndex] = d.indices
	if d.uvs != nil {
		arrays[Mesh.ArrayTexUv] = d.uvs
	}
	d.arrayMesh.AddSurfaceFromArrays(Mesh.PrimitiveTriangles, arrays[:])
	// Re-apply the surface override every refit. Surface overrides are
	// stored on the MeshInstance3D (not the Mesh) so they survive
	// ClearSurfaces in principle, but only if the surface index exists
	// when set — by rebinding after AddSurfaceFromArrays we don't have
	// to think about whether overrides survived or were dropped.
	if d.material != StandardMaterial3D.Nil {
		d.mi.SetSurfaceOverrideMaterial(0, d.material.AsMaterial())
	}
}
