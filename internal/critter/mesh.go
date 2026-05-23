package critter

import "math"

// Mesh is the procedural critter body — vertex positions in body
// space and triangle indices into Verts. Closed at both ends with
// a small fan so the body is a watertight blob.
type Mesh struct {
	Verts   []Vec3
	Indices []int32
}

// sample is one resolved point on the spine: position, unit-length
// head-ward tangent, and the local body radius. Shared by BuildMesh
// (ring construction) and AnchorPoint / ClosestAnchor (part
// placement), so any future change to the spine evaluation only
// needs to touch sampleSpineAt below.
type sample struct {
	pos    Vec3
	tan    Vec3
	radius float32
}

// sampleSpineAt evaluates the Catmull-Rom spine + linear radius
// interpolation at parameter t ∈ [0,1] running from tail tip (0)
// to head tip (1). Reflects the current slider state via
// ComputeShape.
func (c *Critter) sampleSpineAt(t float32) sample {
	controls, radii := c.ComputeShape()
	n := len(controls)
	if n == 0 {
		return sample{tan: Vec3{Z: 1}, radius: 1}
	}
	if n == 1 {
		return sample{pos: controls[0], tan: Vec3{Z: 1}, radius: radii[0]}
	}
	pre := extrapolate(controls[1], controls[0])
	post := extrapolate(controls[n-2], controls[n-1])
	idx, local := mapToSegment(t, n-1)
	var p0, p3 Vec3
	if idx == 0 {
		p0 = pre
	} else {
		p0 = controls[idx-1]
	}
	if idx+2 >= n {
		p3 = post
	} else {
		p3 = controls[idx+2]
	}
	p1 := controls[idx]
	p2 := controls[idx+1]
	pos := catmullRom(p0, p1, p2, p3, local)
	tan := normalise(catmullRomTangent(p0, p1, p2, p3, local))
	r := radii[idx] + (radii[idx+1]-radii[idx])*local
	return sample{pos: pos, tan: tan, radius: r}
}

// BuildMesh extrudes a tube around the current spine. The spine is
// sampled samplesAlong times via Catmull-Rom interpolation through
// the control points; at each sample a ring of segmentsAround
// vertices is laid out perpendicular to the local tangent.
// Adjacent rings are stitched with quads (two triangles); the
// first and last rings are closed with a triangle fan to a single
// cap vertex.
//
// CCW winding from outside (so Godot's default backface culling
// keeps the surface visible) and a smooth-shaded surface when the
// renderer derives normals from triangle data.
func (c *Critter) BuildMesh(samplesAlong, segmentsAround int) Mesh {
	if samplesAlong < 2 {
		samplesAlong = 2
	}
	if segmentsAround < 3 {
		segmentsAround = 3
	}
	controls, _ := c.ComputeShape()
	if len(controls) < 2 {
		return Mesh{}
	}

	// Sample positions, tangents and radii along the spine. All the
	// per-sample work now lives in sampleSpineAt so AnchorPoint /
	// ClosestAnchor (used by the part-placement code) see exactly the
	// same surface the renderer sees.
	samples := make([]sample, samplesAlong)
	for i := 0; i < samplesAlong; i++ {
		t := float32(i) / float32(samplesAlong-1)
		samples[i] = c.sampleSpineAt(t)
	}

	// Generate ring vertices. Use a stable reference up vector to
	// build the perpendicular frame — works as long as no spine
	// tangent points along world up, which our default poses don't.
	refUp := Vec3{X: 0, Y: 1, Z: 0}
	verts := make([]Vec3, 0, samplesAlong*segmentsAround+2)
	for _, s := range samples {
		n, b := frameFromTangent(s.tan, refUp)
		for j := 0; j < segmentsAround; j++ {
			a := 2 * math.Pi * float64(j) / float64(segmentsAround)
			cx := float32(math.Cos(a)) * s.radius
			cy := float32(math.Sin(a)) * s.radius
			verts = append(verts, Vec3{
				X: s.pos.X + n.X*cx + b.X*cy,
				Y: s.pos.Y + n.Y*cx + b.Y*cy,
				Z: s.pos.Z + n.Z*cx + b.Z*cy,
			})
		}
	}

	// Cap vertices at the tail and head — a single vert at each
	// endpoint, fanning into the first/last ring respectively.
	tailCap := int32(len(verts))
	verts = append(verts, samples[0].pos)
	headCap := int32(len(verts))
	verts = append(verts, samples[len(samples)-1].pos)

	indices := make([]int32, 0, samplesAlong*segmentsAround*6+segmentsAround*6)
	// Body quads, CCW from outside.
	for i := 0; i < samplesAlong-1; i++ {
		base0 := int32(i * segmentsAround)
		base1 := int32((i + 1) * segmentsAround)
		for j := 0; j < segmentsAround; j++ {
			jn := int32((j + 1) % segmentsAround)
			a := base0 + int32(j)
			b := base0 + jn
			cc := base1 + int32(j)
			d := base1 + jn
			indices = append(indices, a, cc, d, a, d, b)
		}
	}
	// Tail cap: fan from tailCap into ring 0. Godot's front-face
	// convention is clockwise from outside; for a fan sitting in a
	// plane perpendicular to the tube, that means we walk the ring
	// in the same rotational sense the body quads use. Earlier
	// drafts had this fan wound the other way, which left the cap
	// backface-culled — a visible hole at the tail tip.
	for j := 0; j < segmentsAround; j++ {
		jn := int32((j + 1) % segmentsAround)
		indices = append(indices, tailCap, int32(j), jn)
	}
	// Head cap: fan from headCap into the last ring. Wound the
	// opposite way around the ring because the head face points
	// the other direction; same logic as the tail.
	last := int32((samplesAlong - 1) * segmentsAround)
	for j := 0; j < segmentsAround; j++ {
		jn := int32((j + 1) % segmentsAround)
		indices = append(indices, headCap, last+jn, last+int32(j))
	}

	return Mesh{Verts: verts, Indices: indices}
}

// mapToSegment converts a global t ∈ [0,1] along a poly-curve of
// `segments` segments into (idx, local) where idx is the segment
// index in [0, segments-1] and local is t within that segment.
func mapToSegment(t float32, segments int) (idx int, local float32) {
	if t <= 0 {
		return 0, 0
	}
	if t >= 1 {
		return segments - 1, 1
	}
	scaled := t * float32(segments)
	idx = int(scaled)
	if idx >= segments {
		idx = segments - 1
	}
	local = scaled - float32(idx)
	return idx, local
}

// extrapolate returns p2 reflected through p1, used to invent the
// phantom control points needed at the ends of a Catmull-Rom chain
// so the curve still passes through and is tangent-continuous at
// the first and last real controls.
func extrapolate(p1, p2 Vec3) Vec3 {
	return Vec3{X: 2*p2.X - p1.X, Y: 2*p2.Y - p1.Y, Z: 2*p2.Z - p1.Z}
}

// catmullRom evaluates the Catmull-Rom spline through (p1, p2) at
// local parameter t ∈ [0,1], with p0 and p3 as the neighbouring
// controls providing the tangent constraints.
func catmullRom(p0, p1, p2, p3 Vec3, t float32) Vec3 {
	t2 := t * t
	t3 := t2 * t
	w0 := -0.5*t3 + t2 - 0.5*t
	w1 := 1.5*t3 - 2.5*t2 + 1.0
	w2 := -1.5*t3 + 2.0*t2 + 0.5*t
	w3 := 0.5*t3 - 0.5*t2
	return Vec3{
		X: w0*p0.X + w1*p1.X + w2*p2.X + w3*p3.X,
		Y: w0*p0.Y + w1*p1.Y + w2*p2.Y + w3*p3.Y,
		Z: w0*p0.Z + w1*p1.Z + w2*p2.Z + w3*p3.Z,
	}
}

// catmullRomTangent is the analytic derivative of catmullRom w.r.t.
// t, giving the spine direction at the sample point.
func catmullRomTangent(p0, p1, p2, p3 Vec3, t float32) Vec3 {
	t2 := t * t
	w0 := -1.5*t2 + 2*t - 0.5
	w1 := 4.5*t2 - 5*t
	w2 := -4.5*t2 + 4*t + 0.5
	w3 := 1.5*t2 - t
	return Vec3{
		X: w0*p0.X + w1*p1.X + w2*p2.X + w3*p3.X,
		Y: w0*p0.Y + w1*p1.Y + w2*p2.Y + w3*p3.Y,
		Z: w0*p0.Z + w1*p1.Z + w2*p2.Z + w3*p3.Z,
	}
}

// frameFromTangent builds an orthonormal (normal, binormal) pair
// perpendicular to the tangent, using refUp as a stable reference
// direction. The resulting frame is well-defined except when the
// tangent points exactly along refUp — the critter spine never
// runs vertically in our defaults, so we don't bother handling
// that degeneracy yet.
func frameFromTangent(tan, refUp Vec3) (normal, binormal Vec3) {
	binormal = normalise(cross(tan, refUp))
	normal = normalise(cross(binormal, tan))
	return normal, binormal
}

func cross(a, b Vec3) Vec3 {
	return Vec3{
		X: a.Y*b.Z - a.Z*b.Y,
		Y: a.Z*b.X - a.X*b.Z,
		Z: a.X*b.Y - a.Y*b.X,
	}
}

func normalise(v Vec3) Vec3 {
	l := float32(math.Sqrt(float64(v.X*v.X + v.Y*v.Y + v.Z*v.Z)))
	if l == 0 {
		return Vec3{}
	}
	return Vec3{X: v.X / l, Y: v.Y / l, Z: v.Z / l}
}
