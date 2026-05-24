package critter

import "math"

// Mesh is the procedural critter body — vertex positions in body
// space, per-vertex normals (radial on the tube body, ±tangent on
// the caps) so the renderer can do smooth shading, and triangle
// indices into Verts. Closed at both ends with a small fan so the
// body is a watertight blob.
//
// Bones / Weights carry the GPU-skin bindings produced by BuildMesh
// — four bone indices and four weights per vertex, in flat-array
// layout (so Bones has 4·len(Verts) entries and Weights the same).
// For body BuildMesh, only the first two slots per vertex are ever
// non-zero (linear blend between the two flanking spine bones);
// the second pair is padded with bone 0 / weight 0 so the surface
// uploads in the standard 4-influences format without setting the
// 8-bone flag. BuildLegMesh (procedural-leg path) leaves both
// slices nil — leg meshes aren't skinned today; they're rebuilt
// every frame by the gait pipeline from animated joint positions.
type Mesh struct {
	Verts   []Vec3
	Normals []Vec3
	Indices []int32
	Bones   []int32
	Weights []float32
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

	// Generate ring vertices. The perpendicular (normal, binormal)
	// frame at each sample comes from a rotation-minimising walk
	// (sampleFrames), not an independent cross(tan, +Y) at each
	// sample — independently-aligned frames "corkscrew" between
	// adjacent samples when the local tangent rotates fast, which
	// happens when bones cluster (the Catmull-Rom curve through
	// closely-spaced bones bends sharply) and when the tangent
	// passes near world up (the cross product becomes numerically
	// unstable). RMF carries the frame from the tail along the
	// curve by the minimum rotation that keeps it perpendicular,
	// so adjacent rings stay rotationally aligned.
	refUp := Vec3{X: 0, Y: 1, Z: 0}
	normals, binormals := sampleFrames(samples, refUp)
	verts := make([]Vec3, 0, samplesAlong*segmentsAround+2)
	vertNormals := make([]Vec3, 0, samplesAlong*segmentsAround+2)
	// Pre-compute the (boneA, boneB, weightA, weightB) tuple per
	// sample so every vert in that ring shares it. boneCount falls
	// out of the data model — same as len(controls) since
	// ComputeShape doesn't insert or remove control points.
	boneCount := len(controls)
	ringBones := make([][2]int32, samplesAlong)
	ringWeights := make([][2]float32, samplesAlong)
	for i := 0; i < samplesAlong; i++ {
		t := float32(i) / float32(samplesAlong-1)
		if boneCount <= 1 {
			ringBones[i] = [2]int32{0, 0}
			ringWeights[i] = [2]float32{1, 0}
			continue
		}
		idx, local := mapToSegment(t, boneCount-1)
		ringBones[i] = [2]int32{int32(idx), int32(idx + 1)}
		ringWeights[i] = [2]float32{1 - local, local}
	}
	// Flat layout: 4 bone indices + 4 weights per vertex. We only
	// populate slot 0 and 1; slot 2 / 3 stay (bone 0, weight 0) so
	// the surface format reads as standard 4-influences. Reserve
	// capacity for the eventual ring count + 2 caps so we don't
	// thrash on append during the inner loop.
	bones := make([]int32, 0, (samplesAlong*segmentsAround+2)*4)
	weights := make([]float32, 0, (samplesAlong*segmentsAround+2)*4)
	pushBoneWeights := func(b [2]int32, w [2]float32) {
		bones = append(bones, b[0], b[1], 0, 0)
		weights = append(weights, w[0], w[1], 0, 0)
	}
	for i, s := range samples {
		n, b := normals[i], binormals[i]
		for j := 0; j < segmentsAround; j++ {
			a := 2 * math.Pi * float64(j) / float64(segmentsAround)
			cs := float32(math.Cos(a))
			sn := float32(math.Sin(a))
			// Radial direction at this ring vertex — already unit
			// length since n and b are orthonormal.
			radial := Vec3{
				X: n.X*cs + b.X*sn,
				Y: n.Y*cs + b.Y*sn,
				Z: n.Z*cs + b.Z*sn,
			}
			verts = append(verts, Vec3{
				X: s.pos.X + radial.X*s.radius,
				Y: s.pos.Y + radial.Y*s.radius,
				Z: s.pos.Z + radial.Z*s.radius,
			})
			vertNormals = append(vertNormals, radial)
			pushBoneWeights(ringBones[i], ringWeights[i])
		}
	}

	// Cap vertices at the tail and head — a single vert at each
	// endpoint, fanning into the first/last ring respectively. The
	// cap-centre normal points along ±tangent (out of the snout / out
	// of the tail tip), so when smooth shading interpolates from
	// "ring vert with radial normal" → "cap vert with tangent
	// normal", you get a softly rounded dome instead of a hard
	// flat-shaded disc.
	tailCap := int32(len(verts))
	verts = append(verts, samples[0].pos)
	vertNormals = append(vertNormals, Vec3{
		X: -samples[0].tan.X, Y: -samples[0].tan.Y, Z: -samples[0].tan.Z,
	})
	// Tail cap rides bone 0 fully — t = 0 lands exactly there.
	pushBoneWeights([2]int32{0, 0}, [2]float32{1, 0})
	headCap := int32(len(verts))
	headSample := samples[len(samples)-1]
	verts = append(verts, headSample.pos)
	vertNormals = append(vertNormals, headSample.tan)
	// Head cap rides the last bone fully — t = 1 lands exactly
	// there. Falls back to bone 0 when there's only one control
	// point so the weighting still sums to 1.
	lastBone := int32(boneCount - 1)
	if lastBone < 0 {
		lastBone = 0
	}
	pushBoneWeights([2]int32{lastBone, 0}, [2]float32{1, 0})

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

	return Mesh{
		Verts:   verts,
		Normals: vertNormals,
		Indices: indices,
		Bones:   bones,
		Weights: weights,
	}
}

// BuildLegMesh extrudes a procedural leg as a single continuous
// tapered tube running Hip → Knee → Foot, with a fan cap at the
// foot. Both sides (left and right of X=0) are baked into the same
// mesh so the renderer can attach a single MeshInstance3D per leg;
// the mirrored side has reversed triangle winding so backface
// culling keeps both sides visible.
//
// Earlier drafts built the femur and tibia as two independent tubes
// that shared a knee position but used different perpendicular
// frames, leaving a visible hole at the knee where the cross
// sections didn't connect. The single-tube version uses one
// rotation-minimising frame walked over the whole polyline (with an
// averaged tangent at the knee so the bend stays smooth) so the
// rings stitch continuously around the bend.
//
// Tapering is per-joint (leg.HipRadius / KneeRadius / FootRadius);
// the tube radius is linearly interpolated along each segment.
//
// When mirror is true (normal world rendering) the −X copy is
// appended with reversed winding. When false (e.g. ribcage side
// view, where the back-of-body leg would overlap and double the
// alpha against the camera-facing one) only the +X side is built.
func (c *Critter) BuildLegMesh(leg Leg, ringsPerSegment, segmentsAround int, mirror bool) Mesh {
	if ringsPerSegment < 2 {
		ringsPerSegment = 2
	}
	if segmentsAround < 3 {
		segmentsAround = 3
	}
	hipR := leg.HipRadius
	kneeR := leg.KneeRadius
	footR := leg.FootRadius

	femurDir := normalise(sub(leg.Knee, leg.Hip))
	tibiaDir := normalise(sub(leg.Foot, leg.Knee))
	// Tangent at the shared knee sample: bisector of the incoming
	// (femur) and outgoing (tibia) directions, so the RMF walk hits a
	// halfway orientation between the two segments at the knee. If
	// either segment is degenerate (zero length), fall back to the
	// other.
	kneeDir := Vec3{
		X: femurDir.X + tibiaDir.X,
		Y: femurDir.Y + tibiaDir.Y,
		Z: femurDir.Z + tibiaDir.Z,
	}
	if kneeDir.X*kneeDir.X+kneeDir.Y*kneeDir.Y+kneeDir.Z*kneeDir.Z < 1e-8 {
		kneeDir = tibiaDir
	}
	kneeDir = normalise(kneeDir)

	// Polyline samples: ringsPerSegment along the femur, then
	// ringsPerSegment-1 more along the tibia (the knee sample is
	// shared between the two segments).
	n := ringsPerSegment*2 - 1
	samples := make([]sample, n)
	for i := 0; i < ringsPerSegment; i++ {
		t := float32(i) / float32(ringsPerSegment-1)
		samples[i] = sample{
			pos: Vec3{
				X: leg.Hip.X + (leg.Knee.X-leg.Hip.X)*t,
				Y: leg.Hip.Y + (leg.Knee.Y-leg.Hip.Y)*t,
				Z: leg.Hip.Z + (leg.Knee.Z-leg.Hip.Z)*t,
			},
			tan:    femurDir,
			radius: hipR + (kneeR-hipR)*t,
		}
	}
	// Knee shares index ringsPerSegment-1; swap its tangent to the
	// averaged direction so the frame bisects the bend.
	samples[ringsPerSegment-1].tan = kneeDir
	for i := 1; i < ringsPerSegment; i++ {
		t := float32(i) / float32(ringsPerSegment-1)
		samples[ringsPerSegment-1+i] = sample{
			pos: Vec3{
				X: leg.Knee.X + (leg.Foot.X-leg.Knee.X)*t,
				Y: leg.Knee.Y + (leg.Foot.Y-leg.Knee.Y)*t,
				Z: leg.Knee.Z + (leg.Foot.Z-leg.Knee.Z)*t,
			},
			tan:    tibiaDir,
			radius: kneeR + (footR-kneeR)*t,
		}
	}

	frameNorms, frameBins := sampleFrames(samples, Vec3{Y: 1})

	verts := make([]Vec3, 0, n*segmentsAround+1)
	meshNormals := make([]Vec3, 0, n*segmentsAround+1)
	for i, s := range samples {
		nrm, bn := frameNorms[i], frameBins[i]
		for j := 0; j < segmentsAround; j++ {
			a := 2 * math.Pi * float64(j) / float64(segmentsAround)
			cs := float32(math.Cos(a))
			sn := float32(math.Sin(a))
			radial := Vec3{
				X: nrm.X*cs + bn.X*sn,
				Y: nrm.Y*cs + bn.Y*sn,
				Z: nrm.Z*cs + bn.Z*sn,
			}
			verts = append(verts, Vec3{
				X: s.pos.X + radial.X*s.radius,
				Y: s.pos.Y + radial.Y*s.radius,
				Z: s.pos.Z + radial.Z*s.radius,
			})
			meshNormals = append(meshNormals, radial)
		}
	}

	indices := make([]int32, 0, (n-1)*segmentsAround*6+segmentsAround*3)
	for i := 0; i < n-1; i++ {
		base0 := int32(i * segmentsAround)
		base1 := int32((i + 1) * segmentsAround)
		for j := 0; j < segmentsAround; j++ {
			jn := int32((j + 1) % segmentsAround)
			ia := base0 + int32(j)
			ib := base0 + jn
			ic := base1 + int32(j)
			id := base1 + jn
			indices = append(indices, ia, ic, id, ia, id, ib)
		}
	}

	lastRing := int32((n - 1) * segmentsAround)
	footCap := int32(len(verts))
	verts = append(verts, leg.Foot)
	meshNormals = append(meshNormals, tibiaDir)
	for j := 0; j < segmentsAround; j++ {
		jn := int32((j + 1) % segmentsAround)
		indices = append(indices, footCap, lastRing+jn, lastRing+int32(j))
	}

	if mirror {
		mirrorBase := int32(len(verts))
		for i := int32(0); i < mirrorBase; i++ {
			v := verts[i]
			nrm := meshNormals[i]
			verts = append(verts, Vec3{X: -v.X, Y: v.Y, Z: v.Z})
			meshNormals = append(meshNormals, Vec3{X: -nrm.X, Y: nrm.Y, Z: nrm.Z})
		}
		origIndexCount := len(indices)
		for i := 0; i < origIndexCount; i += 3 {
			a := indices[i] + mirrorBase
			b := indices[i+1] + mirrorBase
			cv := indices[i+2] + mirrorBase
			// Flip winding so backface culling keeps the mirrored side
			// visible from outside (negating X reverses the handedness).
			indices = append(indices, a, cv, b)
		}
	}

	return Mesh{Verts: verts, Normals: meshNormals, Indices: indices}
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
// direction. When tan is (nearly) parallel to refUp the cross
// product collapses; we then fall back to +X, and if tan is also
// parallel to +X we fall back to +Z. One of the three axes is
// always at least 45° off the tangent, so this terminates.
func frameFromTangent(tan, refUp Vec3) (normal, binormal Vec3) {
	binormal = cross(tan, refUp)
	if binormal.X*binormal.X+binormal.Y*binormal.Y+binormal.Z*binormal.Z < 1e-4 {
		binormal = cross(tan, Vec3{X: 1})
		if binormal.X*binormal.X+binormal.Y*binormal.Y+binormal.Z*binormal.Z < 1e-4 {
			binormal = cross(tan, Vec3{Z: 1})
		}
	}
	binormal = normalise(binormal)
	normal = normalise(cross(binormal, tan))
	return normal, binormal
}

// sampleFrames computes a rotation-minimising (parallel transport)
// frame at each sample. The first sample's frame comes from
// frameFromTangent(refUp); each subsequent sample's frame is the
// previous one rotated by the smallest rotation that takes the
// previous tangent onto the new one (Rodrigues formula). Because
// the frame is never spun around the tangent — only rotated
// perpendicular to it — adjacent rings stay rotationally aligned
// and the rendered tube doesn't corkscrew where the spine bends
// sharply (e.g. between two bones that have been moved close
// together).
//
// For a perfectly bilateral-symmetric spine (every bone at X=0)
// this returns the same frames as the old per-sample
// frameFromTangent: the rotation axis between adjacent tangents is
// always along ±X, and the resulting normals stay in the Y-Z
// plane. The benefit shows up when the spine acquires X
// components, or when adjacent tangents differ enough that the
// independent world-up cross is no longer numerically stable.
func sampleFrames(samples []sample, refUp Vec3) (normals, binormals []Vec3) {
	n := len(samples)
	if n == 0 {
		return nil, nil
	}
	normals = make([]Vec3, n)
	binormals = make([]Vec3, n)
	normals[0], binormals[0] = frameFromTangent(samples[0].tan, refUp)
	for i := 1; i < n; i++ {
		prev := samples[i-1].tan
		cur := samples[i].tan
		// For unit tangents, |prev × cur| = |sin θ| and prev · cur =
		// cos θ — both come out of the same cross/dot, no trig
		// needed.
		axis := cross(prev, cur)
		sinA := float32(math.Sqrt(float64(axis.X*axis.X + axis.Y*axis.Y + axis.Z*axis.Z)))
		if sinA < 1e-6 {
			normals[i] = normals[i-1]
			binormals[i] = binormals[i-1]
			continue
		}
		axis = Vec3{X: axis.X / sinA, Y: axis.Y / sinA, Z: axis.Z / sinA}
		cosA := dotV(prev, cur)
		if cosA > 1 {
			cosA = 1
		} else if cosA < -1 {
			cosA = -1
		}
		normals[i] = normalise(rotateAxisAngle(normals[i-1], axis, cosA, sinA))
		binormals[i] = normalise(rotateAxisAngle(binormals[i-1], axis, cosA, sinA))
	}
	return normals, binormals
}

// rotateAxisAngle applies Rodrigues' rotation formula to v around
// the supplied unit axis by the angle whose cosine and sine are
// pre-computed. Returns v unchanged when the rotation is identity.
func rotateAxisAngle(v, axis Vec3, cosA, sinA float32) Vec3 {
	if cosA >= 1 && sinA <= 0 {
		return v
	}
	cAV := cross(axis, v)
	k := (1 - cosA) * dotV(axis, v)
	return Vec3{
		X: v.X*cosA + cAV.X*sinA + axis.X*k,
		Y: v.Y*cosA + cAV.Y*sinA + axis.Y*k,
		Z: v.Z*cosA + cAV.Z*sinA + axis.Z*k,
	}
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
