package citizen

import (
	"fmt"
	"math"
)

// Mat3 is a row-major 3×3 matrix. Mul3Vec applies it as M·v.
type Mat3 [3][3]float32

// CitizenRig is the animated GLB's bind-pose mesh plus the maps that let the
// canonical (base.obj) morph + clothing system drive the straightened,
// skinned mesh.
//
// The animated GLB's geometry is the mesh2motion-straightened T-pose that
// matches the skeleton's bind — so it skins correctly under the imported
// clips. But customisation (morph .target deltas, .mhclo clothing anchors)
// is authored against base.obj's canonical vertex order, in the A-pose. The
// maps here bridge the two: every GLB vertex knows which canonical vertex it
// came from (Canonical), and how to rotate an A-pose displacement at that
// canonical vertex into the straightened T-pose at this GLB vertex (Disp).
type CitizenRig struct {
	// Bind/Normals/UVs/Joints/Weights/Indices come straight from the
	// animated GLB's first primitive: the T-pose bind geometry in GLB
	// (≈1.7 m) space, the per-vertex bone bindings (JOINTS_0 indexing the
	// skin's joint list), and the triangle index buffer.
	Bind    []Vec3
	Normals []Vec3
	UVs     []Vec2
	Joints  [][4]uint16
	Weights [][4]float32
	Indices []int32

	// Canonical[i] is the base.obj canonical vertex GLB vert i was split
	// from, or -1 if no base vertex matched within tolerance.
	Canonical []int32

	// CanonicalToGLB[c] is a representative GLB vert with Canonical==c, or
	// -1 if canonical vert c is absent from the GLB (e.g. the helper groups
	// dropped during conversion). Lets clothing read a canonical body
	// vertex's T-pose position and skin binding.
	CanonicalToGLB []int32

	// Disp[i] maps an A-pose (base.obj-space) morph displacement at
	// Canonical[i] into the straightened T-pose at GLB vert i, including the
	// base.obj→GLB uniform scale, so:
	//   bindPos[i] = Bind[i] + Disp[i]·(recompute[c] - base[c])
	Disp []Mat3

	// Scale/Translate are the base.obj→GLB similarity (p_glb = s·p_obj + t),
	// exposed so clothing (fit in base.obj space) maps to GLB space the
	// same way the body does.
	Scale     float32
	Translate Vec3
}

// maxUVMatchHeightJump caps how far (in base.obj units, where the hm08 mesh
// spans ±8) a UV vertex match may move a vertex in height before it's rejected
// as a UV-seam collision rather than a real correspondence. The straighten
// raised arm verts by ≲4 units, so 4.5 keeps the arms while dropping the
// handful of cross-body jumps the non-injective UV layout produces.
const maxUVMatchHeightJump = 4.5

func absf(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}

// BuildCitizenRig assembles the rig from the animated (T-pose, rigged) GLB and
// the canonical base mesh. scale/translate are the base.obj→GLB similarity from
// the conversion sidecar.
//
// Each animated vertex is mapped to its base.obj canonical vertex by UV (UVs
// are preserved by the straighten and the importer, so this is pose- and
// import-order-independent — unlike matching the animated mesh against a
// separately-imported A-pose mesh, which the importer can reorder out of step).
// The match targets are restricted to the body+eye canonical verts (those the
// rendered faces reference) so helper-group UVs can't capture a match.
func BuildCitizenRig(animated *GLB, base *BaseMesh, scale float32, translate Vec3) (*CitizenRig, error) {
	if animated == nil || base == nil {
		return nil, fmt.Errorf("citizen: nil input to BuildCitizenRig")
	}
	n := len(animated.Positions)
	if n == 0 {
		return nil, fmt.Errorf("citizen: animated GLB has no vertices")
	}
	if len(animated.Joints) != n || len(animated.Weights) != n {
		return nil, fmt.Errorf("citizen: animated GLB missing skin (joints=%d weights=%d, want %d)",
			len(animated.Joints), len(animated.Weights), n)
	}
	if len(animated.UVs) != n {
		return nil, fmt.Errorf("citizen: animated GLB has no UVs (%d, want %d) — UV vertex mapping needs them",
			len(animated.UVs), n)
	}
	if len(base.UVs) != len(base.Verts) {
		return nil, fmt.Errorf("citizen: base mesh has no per-vertex UVs — UV vertex mapping needs them")
	}
	if scale == 0 {
		return nil, fmt.Errorf("citizen: zero base→GLB scale")
	}

	rig := &CitizenRig{
		Bind:      animated.Positions,
		Normals:   animated.Normals,
		UVs:       animated.UVs,
		Joints:    animated.Joints,
		Weights:   animated.Weights,
		Indices:   animated.Indices,
		Scale:     scale,
		Translate: translate,
		Canonical: make([]int32, n),
		Disp:      make([]Mat3, n),
	}
	rig.CanonicalToGLB = make([]int32, len(base.Verts))
	for c := range rig.CanonicalToGLB {
		rig.CanonicalToGLB[c] = -1
	}

	// 1. Canonical map by UV. Build a 2D nearest-neighbour grid over the
	// body+eye canonical verts' UVs (embedded as Vec3{u,v,0}) and match each
	// animated vert's UV into it.
	bodyEye := make([]bool, len(base.Verts))
	for _, idx := range base.Indices {
		if int(idx) < len(bodyEye) {
			bodyEye[idx] = true
		}
	}
	for _, idx := range base.EyeIndices {
		if int(idx) < len(bodyEye) {
			bodyEye[idx] = true
		}
	}
	var uvPts []Vec3
	var uvCanon []int32
	for c, on := range bodyEye {
		if !on {
			continue
		}
		uvPts = append(uvPts, Vec3{X: base.UVs[c].U, Y: base.UVs[c].V})
		uvCanon = append(uvCanon, int32(c))
	}
	if len(uvPts) == 0 {
		return nil, fmt.Errorf("citizen: no body/eye UV targets")
	}
	uvGrid := newVertGrid(uvPts)
	invScale := 1 / scale
	for i := 0; i < n; i++ {
		uv := animated.UVs[i]
		gi, _ := uvGrid.nearest(Vec3{X: uv.U, Y: uv.V})
		c := int32(-1)
		if gi >= 0 {
			cand := uvCanon[gi]
			// Reject a UV match that jumped to a wildly different body height.
			// base.obj's UV islands aren't injective (a head vert's UV can sit
			// next to a thigh vert's), so a few verts UV-match across the body.
			// The straighten only raised arm verts by ≲4 units, so a larger gap
			// is a UV-seam collision, not a real correspondence — leave it
			// unmatched (no morph there, but no stray either) rather than wire a
			// neck vert to the legs.
			objY := (animated.Positions[i].Y - translate.Y) * invScale
			if absf(objY-base.Verts[cand].Y) <= maxUVMatchHeightJump {
				c = cand
			}
		}
		rig.Canonical[i] = c
		if c >= 0 && rig.CanonicalToGLB[c] < 0 {
			rig.CanonicalToGLB[c] = int32(i)
		}
	}

	// 2. Per-bone straighten rotation. Grouping verts by dominant joint and
	// recovering the best-fit rotation from A-pose (base.obj canonical) →
	// T-pose (animated, inverse-transformed) offsets (Kabsch via polar) yields
	// one rotation per bone, so a morph delta authored A-pose ends up correct
	// on the straightened limb.
	numJoints := 0
	dom := make([]int32, n)
	for i := 0; i < n; i++ {
		d := dominantJoint(animated.Joints[i], animated.Weights[i])
		dom[i] = d
		if int(d)+1 > numJoints {
			numJoints = int(d) + 1
		}
	}
	rot := perBoneRotations(animated.Positions, base.Verts, rig.Canonical, dom, numJoints, 1/scale, translate)
	for i := 0; i < n; i++ {
		r := identity3()
		if d := dom[i]; d >= 0 && int(d) < len(rot) {
			r = rot[d]
		}
		rig.Disp[i] = scaleMat3(r, scale)
	}
	return rig, nil
}

// dominantJoint returns the joint index carrying the largest weight, or -1 if
// the vertex has no weight at all.
func dominantJoint(j [4]uint16, w [4]float32) int32 {
	best := int32(-1)
	var bestW float32
	for c := 0; c < 4; c++ {
		if w[c] > bestW {
			bestW = w[c]
			best = int32(j[c])
		}
	}
	return best
}

// perBoneRotations recovers, for each joint, the rotation that best maps the
// A-pose vertex cloud of that joint's dominant verts (base.obj canonical
// positions) onto their T-pose (animated, inverse-transformed to base.obj
// space) positions. Verts whose canonical map missed, and joints with too few
// verts, fall back to identity.
func perBoneRotations(animPos, baseVerts []Vec3, canonical, dom []int32, numJoints int, invScale float32, translate Vec3) []Mat3 {
	aPose := func(i int) (Vec3, bool) {
		c := canonical[i]
		if c < 0 || int(c) >= len(baseVerts) {
			return Vec3{}, false
		}
		return baseVerts[c], true
	}
	tPose := func(i int) Vec3 {
		p := animPos[i]
		return Vec3{
			X: (p.X - translate.X) * invScale,
			Y: (p.Y - translate.Y) * invScale,
			Z: (p.Z - translate.Z) * invScale,
		}
	}
	type acc struct {
		preC, postC Vec3
		count       int
	}
	cent := make([]acc, numJoints)
	for i := range dom {
		d := dom[i]
		if d < 0 {
			continue
		}
		p, ok := aPose(i)
		if !ok {
			continue
		}
		cent[d].preC = addV(cent[d].preC, p)
		cent[d].postC = addV(cent[d].postC, tPose(i))
		cent[d].count++
	}
	for j := range cent {
		if cent[j].count > 0 {
			inv := 1 / float32(cent[j].count)
			cent[j].preC = scaleV(cent[j].preC, inv)
			cent[j].postC = scaleV(cent[j].postC, inv)
		}
	}
	// Cross-covariance H[j] = Σ (post-postC)(pre-preC)ᵀ, so polar(H) maps
	// A-pose offsets onto T-pose offsets.
	cov := make([]Mat3, numJoints)
	for i := range dom {
		d := dom[i]
		if d < 0 || cent[d].count < 4 {
			continue
		}
		pp, ok := aPose(i)
		if !ok {
			continue
		}
		p := subV(pp, cent[d].preC)
		q := subV(tPose(i), cent[d].postC)
		cov[d][0][0] += q.X * p.X
		cov[d][0][1] += q.X * p.Y
		cov[d][0][2] += q.X * p.Z
		cov[d][1][0] += q.Y * p.X
		cov[d][1][1] += q.Y * p.Y
		cov[d][1][2] += q.Y * p.Z
		cov[d][2][0] += q.Z * p.X
		cov[d][2][1] += q.Z * p.Y
		cov[d][2][2] += q.Z * p.Z
	}
	out := make([]Mat3, numJoints)
	for j := 0; j < numJoints; j++ {
		if cent[j].count < 4 {
			out[j] = identity3()
			continue
		}
		out[j] = polar(cov[j])
	}
	return out
}

// ---- small linear-algebra helpers (3×3, no external deps) ----

func identity3() Mat3 { return Mat3{{1, 0, 0}, {0, 1, 0}, {0, 0, 1}} }

// Mul3Vec applies m as m·v.
func (m Mat3) Mul3Vec(v Vec3) Vec3 {
	return Vec3{
		X: m[0][0]*v.X + m[0][1]*v.Y + m[0][2]*v.Z,
		Y: m[1][0]*v.X + m[1][1]*v.Y + m[1][2]*v.Z,
		Z: m[2][0]*v.X + m[2][1]*v.Y + m[2][2]*v.Z,
	}
}

func scaleMat3(m Mat3, s float32) Mat3 {
	for r := 0; r < 3; r++ {
		for c := 0; c < 3; c++ {
			m[r][c] *= s
		}
	}
	return m
}

func det3(m Mat3) float32 {
	return m[0][0]*(m[1][1]*m[2][2]-m[1][2]*m[2][1]) -
		m[0][1]*(m[1][0]*m[2][2]-m[1][2]*m[2][0]) +
		m[0][2]*(m[1][0]*m[2][1]-m[1][1]*m[2][0])
}

func transpose3(m Mat3) Mat3 {
	return Mat3{
		{m[0][0], m[1][0], m[2][0]},
		{m[0][1], m[1][1], m[2][1]},
		{m[0][2], m[1][2], m[2][2]},
	}
}

func invert3(m Mat3) (Mat3, bool) {
	d := det3(m)
	if d > -1e-12 && d < 1e-12 {
		return Mat3{}, false
	}
	id := 1 / d
	return Mat3{
		{
			(m[1][1]*m[2][2] - m[1][2]*m[2][1]) * id,
			(m[0][2]*m[2][1] - m[0][1]*m[2][2]) * id,
			(m[0][1]*m[1][2] - m[0][2]*m[1][1]) * id,
		},
		{
			(m[1][2]*m[2][0] - m[1][0]*m[2][2]) * id,
			(m[0][0]*m[2][2] - m[0][2]*m[2][0]) * id,
			(m[0][2]*m[1][0] - m[0][0]*m[1][2]) * id,
		},
		{
			(m[1][0]*m[2][1] - m[1][1]*m[2][0]) * id,
			(m[0][1]*m[2][0] - m[0][0]*m[2][1]) * id,
			(m[0][0]*m[1][1] - m[0][1]*m[1][0]) * id,
		},
	}, true
}

func addMat3(a, b Mat3) Mat3 {
	for r := 0; r < 3; r++ {
		for c := 0; c < 3; c++ {
			a[r][c] += b[r][c]
		}
	}
	return a
}

func maxDiff3(a, b Mat3) float32 {
	var m float32
	for r := 0; r < 3; r++ {
		for c := 0; c < 3; c++ {
			d := a[r][c] - b[r][c]
			if d < 0 {
				d = -d
			}
			if d > m {
				m = d
			}
		}
	}
	return m
}

// polar returns the orthogonal polar factor of m (its closest rotation),
// computed by the Higham Newton iteration R ← ½(R + R⁻ᵀ). For a
// cross-covariance H = Σ q·pᵀ this is the Kabsch optimal rotation mapping
// p→q. Falls back to identity on a degenerate / reflective input.
func polar(m Mat3) Mat3 {
	r := m
	for it := 0; it < 64; it++ {
		inv, ok := invert3(r)
		if !ok {
			return identity3()
		}
		next := scaleMat3(addMat3(r, transpose3(inv)), 0.5)
		if maxDiff3(next, r) < 1e-7 {
			r = next
			break
		}
		r = next
	}
	if det3(r) < 0.5 { // reflection or collapse — not a usable rotation
		return identity3()
	}
	return r
}

func addV(a, b Vec3) Vec3           { return Vec3{a.X + b.X, a.Y + b.Y, a.Z + b.Z} }
func subV(a, b Vec3) Vec3           { return Vec3{a.X - b.X, a.Y - b.Y, a.Z - b.Z} }
func scaleV(a Vec3, s float32) Vec3 { return Vec3{a.X * s, a.Y * s, a.Z * s} }

func dist2(a, b Vec3) float32 {
	dx, dy, dz := a.X-b.X, a.Y-b.Y, a.Z-b.Z
	return dx*dx + dy*dy + dz*dz
}

// ---- spatial hash grid for nearest-neighbour over the base mesh ----

type vertGrid struct {
	cell  float32
	verts []Vec3
	cells map[[3]int32][]int32
}

// newVertGrid buckets verts into a uniform grid sized so most cells hold a
// handful of points (cell = bbox diagonal / 128, with a floor).
func newVertGrid(verts []Vec3) *vertGrid {
	g := &vertGrid{verts: verts, cells: make(map[[3]int32][]int32)}
	if len(verts) == 0 {
		g.cell = 1
		return g
	}
	lo, hi := verts[0], verts[0]
	for _, v := range verts[1:] {
		lo.X = min32(lo.X, v.X)
		lo.Y = min32(lo.Y, v.Y)
		lo.Z = min32(lo.Z, v.Z)
		hi.X = max32(hi.X, v.X)
		hi.Y = max32(hi.Y, v.Y)
		hi.Z = max32(hi.Z, v.Z)
	}
	diag := float32(math.Sqrt(float64(dist2(lo, hi))))
	g.cell = diag / 128
	if g.cell <= 0 {
		g.cell = 1
	}
	for i, v := range verts {
		k := g.key(v)
		g.cells[k] = append(g.cells[k], int32(i))
	}
	return g
}

func (g *vertGrid) key(p Vec3) [3]int32 {
	return [3]int32{
		int32(math.Floor(float64(p.X / g.cell))),
		int32(math.Floor(float64(p.Y / g.cell))),
		int32(math.Floor(float64(p.Z / g.cell))),
	}
}

// nearest returns the index of the closest grid vertex to p (and its squared
// distance), expanding the search shell until no nearer point can exist.
func (g *vertGrid) nearest(p Vec3) (int32, float32) {
	kc := g.key(p)
	best := int32(-1)
	bestD := float32(math.MaxFloat32)
	for r := int32(0); r < 256; r++ {
		for dx := -r; dx <= r; dx++ {
			for dy := -r; dy <= r; dy++ {
				for dz := -r; dz <= r; dz++ {
					if absI(dx) != r && absI(dy) != r && absI(dz) != r {
						continue // interior of the shell — already scanned
					}
					for _, vi := range g.cells[[3]int32{kc[0] + dx, kc[1] + dy, kc[2] + dz}] {
						if d := dist2(p, g.verts[vi]); d < bestD {
							bestD = d
							best = vi
						}
					}
				}
			}
		}
		// Anything outside shell r is ≥ r·cell away; stop once that
		// exceeds the best found so far.
		if best >= 0 && float32(r)*g.cell >= float32(math.Sqrt(float64(bestD))) {
			break
		}
	}
	return best, bestD
}

func absI(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}
func min32(a, b float32) float32 {
	if a < b {
		return a
	}
	return b
}
func max32(a, b float32) float32 {
	if a > b {
		return a
	}
	return b
}
