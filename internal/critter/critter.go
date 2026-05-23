// Package critter is the pure-Go data model for the critter editor —
// a procedural creature where a body shape is sculpted
// by sliders that drive a parametric spine + radius profile. Mesh
// generation is in mesh.go; the slider catalog is in sliders.go;
// graphics.gd glue lives in internal/critter_body.go.
//
// Layout: index 0 is the tail tip, len(Controls)-1 is the head tip.
// The body axis is +Z forward, +Y up. Sliders are stored as a flat
// map so they can be serialised through the musical interface the
// same way the citizen editor's sliders are.
package critter

import "math"

// Vec3 is a 3D vector of float32. Local to this package so the data
// model stays pure-Go and testable; the graphics.gd bridge converts
// at the boundary.
type Vec3 struct{ X, Y, Z float32 }

// Bone is one vertebra in the spine chain — a rest-pose position
// in body space plus the local body radius at that bone. Bones are
// stored in order tail (index 0) → head (last index); the implicit
// parent of bone i is bone i-1, so the chain is always linear (no
// branching) in v1. Branched skeletons (legs, arms) will need an
// explicit parent index here later.
type Bone struct {
	Pos    Vec3
	Radius float32
}

// Critter holds the live editing state for one creature: an
// explicit chain of spine bones (variable length, ≥2) plus a flat
// map of macro sliders that apply on top of the bone rest pose.
// Shape is recomputed on demand from these via ComputeShape; the
// sliders are still the same names the citizen editor uses so
// existing UI / replication keeps working while the
// per-bone editing layers on top.
type Critter struct {
	bones   []Bone
	weights map[string]float32
}

// New returns a default critter — a 5-bone tail-to-head chain with
// the same rest pose previous versions used (so existing tests +
// scenes line up unchanged).
func New() *Critter {
	return &Critter{
		bones:   defaultBones(),
		weights: map[string]float32{},
	}
}

// defaultBones is the neutral 5-bone spine. Tail tip at index 0,
// head tip at index 4. Y-lift toward the head gives a quadrupedal
// resting pose rather than a flat sausage. Centralised here so
// New() and AppendHead/AppendTail share the same neutral shape.
func defaultBones() []Bone {
	return []Bone{
		{Pos: Vec3{X: 0, Y: 0.0, Z: -1.5}, Radius: 0.08}, // tail tip
		{Pos: Vec3{X: 0, Y: 0.0, Z: -0.7}, Radius: 0.30}, // hip
		{Pos: Vec3{X: 0, Y: 0.1, Z: 0.0}, Radius: 0.40},  // body
		{Pos: Vec3{X: 0, Y: 0.2, Z: 0.7}, Radius: 0.25},  // neck base
		{Pos: Vec3{X: 0, Y: 0.4, Z: 1.3}, Radius: 0.30},  // head tip
	}
}

// Bones returns a copy of the current bone chain — safe to walk or
// hand to a renderer without worrying about subsequent edits
// mutating the slice underneath.
func (c *Critter) Bones() []Bone {
	out := make([]Bone, len(c.bones))
	copy(out, c.bones)
	return out
}

// BoneCount is the current number of bones in the spine chain.
func (c *Critter) BoneCount() int { return len(c.bones) }

// MoveBone sets bone i's rest position. Returns true if the bone
// existed (and the value actually changed). Out-of-range indices
// are no-ops returning false.
func (c *Critter) MoveBone(i int, pos Vec3) bool {
	if i < 0 || i >= len(c.bones) {
		return false
	}
	if c.bones[i].Pos == pos {
		return false
	}
	c.bones[i].Pos = pos
	return true
}

// SetBoneRadius sets bone i's body radius. Returns true on a real
// change.
func (c *Critter) SetBoneRadius(i int, r float32) bool {
	if i < 0 || i >= len(c.bones) {
		return false
	}
	if c.bones[i].Radius == r {
		return false
	}
	c.bones[i].Radius = r
	return true
}

// AppendHead grows the spine by one bone past the current head
// tip, extrapolating the head-end direction so the new tip sits
// the same distance further out as the previous segment length.
// Returns the new bone's index.
func (c *Critter) AppendHead() int {
	if len(c.bones) < 2 {
		// Degenerate; just stamp another copy of the only bone we
		// have so we don't divide by zero downstream.
		if len(c.bones) == 1 {
			c.bones = append(c.bones, c.bones[0])
		}
		return len(c.bones) - 1
	}
	last := c.bones[len(c.bones)-1]
	prev := c.bones[len(c.bones)-2]
	step := sub(last.Pos, prev.Pos)
	c.bones = append(c.bones, Bone{
		Pos:    add(last.Pos, step),
		Radius: last.Radius * 0.85, // taper toward the new tip
	})
	return len(c.bones) - 1
}

// AppendTail grows the spine by one bone past the current tail
// tip, mirroring AppendHead. The new bone is inserted at index 0,
// so all existing bone indices shift up by one — callers tracking
// bones by index need to compensate.
func (c *Critter) AppendTail() int {
	if len(c.bones) < 2 {
		if len(c.bones) == 1 {
			c.bones = append([]Bone{c.bones[0]}, c.bones...)
		}
		return 0
	}
	first := c.bones[0]
	next := c.bones[1]
	step := sub(first.Pos, next.Pos)
	c.bones = append([]Bone{{
		Pos:    add(first.Pos, step),
		Radius: first.Radius * 0.85,
	}}, c.bones...)
	return 0
}

// RemoveHead removes the head-end bone. Refuses to drop below two
// bones (a chain needs at least one segment). Returns true on a
// real removal.
func (c *Critter) RemoveHead() bool {
	if len(c.bones) <= 2 {
		return false
	}
	c.bones = c.bones[:len(c.bones)-1]
	return true
}

// RemoveTail removes the tail-end bone. Refuses to drop below two
// bones. Returns true on a real removal. Existing bone indices
// shift down by one — callers tracking bones by index need to
// compensate.
func (c *Critter) RemoveTail() bool {
	if len(c.bones) <= 2 {
		return false
	}
	c.bones = c.bones[1:]
	return true
}

// SetWeight updates one macro slider. Returns true when the value
// changed, so callers can early-exit on no-op slider drags
// (mirrors citizen.Citizen.SetWeight).
func (c *Critter) SetWeight(name string, weight float32) bool {
	if c.weights[name] == weight {
		return false
	}
	if weight == 0 {
		delete(c.weights, name)
	} else {
		c.weights[name] = weight
	}
	return true
}

// Weight reads back a slider value. Unset sliders return 0.
func (c *Critter) Weight(name string) float32 { return c.weights[name] }

// Weights returns a copy of the slider state, for save/load and
// network replication.
func (c *Critter) Weights() map[string]float32 {
	out := make(map[string]float32, len(c.weights))
	for k, v := range c.weights {
		out[k] = v
	}
	return out
}

// DefaultControls is a backwards-compatible view returning the
// rest positions of the default bone chain. Kept around for any
// callers that still want the old "5-control" shape; new code
// should use Bones() instead.
func DefaultControls() []Vec3 {
	bs := defaultBones()
	out := make([]Vec3, len(bs))
	for i, b := range bs {
		out[i] = b.Pos
	}
	return out
}

// DefaultRadii is a backwards-compatible view of the default bone
// chain's radii.
func DefaultRadii() []float32 {
	bs := defaultBones()
	out := make([]float32, len(bs))
	for i, b := range bs {
		out[i] = b.Radius
	}
	return out
}

// ComputeShape resolves the current bone chain plus macro slider
// state into concrete spine + radius slices the mesh generator
// (and AnchorPoint / ClosestAnchor) consume. The returned slices
// are freshly allocated each call.
//
// Macro slider interpretation, applied on top of the live bone
// positions and radii:
//   - shape/length      ∈ [-1,1] → all bones' Z scaled ×0.5 to ×1.5
//   - shape/arch        ∈ [-1,1] → mid-chain Y offset for back arch
//   - shape/neck_lift   ∈ [-1,1] → Y offset on head-half bones
//   - shape/head_size   ∈ [-1,1] → head-tip radius ×0.5 to ×1.5
//   - shape/body_size   ∈ [-1,1] → middle radii ×0.5 to ×1.5
//   - shape/tail_size   ∈ [-1,1] → tail-tip radius ×0.5 to ×1.5
//
// With a variable-length chain the macros use proportional indices
// (mid bone is bones[n/2], etc.) so growing the spine doesn't
// stop the sliders working. Anything else in weights is ignored
// (forwards-compatible with future macros).
func (c *Critter) ComputeShape() (controls []Vec3, radii []float32) {
	n := len(c.bones)
	controls = make([]Vec3, n)
	radii = make([]float32, n)
	for i, b := range c.bones {
		controls[i] = b.Pos
		radii[i] = b.Radius
	}
	if n == 0 {
		return controls, radii
	}
	length := 1 + 0.5*c.weights["shape/length"]
	arch := c.weights["shape/arch"]
	neck := c.weights["shape/neck_lift"]
	head := 1 + 0.5*c.weights["shape/head_size"]
	body := 1 + 0.5*c.weights["shape/body_size"]
	tail := 1 + 0.5*c.weights["shape/tail_size"]
	for i := range controls {
		controls[i].Z *= length
	}
	if n >= 3 {
		mid := n / 2
		controls[mid].Y += 0.2 * arch
	}
	if n >= 2 {
		// Apply neck lift to the head-half of the chain, ramping
		// from 0 at the midpoint to full lift at the tip.
		for i := n / 2; i < n; i++ {
			f := float32(i-n/2) / float32(n-n/2-1+1)
			controls[i].Y += 0.2 * neck * f
		}
	}
	radii[0] *= tail
	radii[n-1] *= head
	for i := 1; i < n-1; i++ {
		// Taper body multiplier toward the head: full body scaling
		// at the hip, easing to 0.8× near the neck (mirrors the
		// original 5-bone behaviour where bones[3] had a 0.8
		// multiplier).
		f := float32(i) / float32(n-1)
		mul := body * (1 - 0.2*f)
		radii[i] *= mul
	}
	return controls, radii
}

func add(a, b Vec3) Vec3 { return Vec3{X: a.X + b.X, Y: a.Y + b.Y, Z: a.Z + b.Z} }
func sub(a, b Vec3) Vec3 { return Vec3{X: a.X - b.X, Y: a.Y - b.Y, Z: a.Z - b.Z} }

// AnchorPoint returns the body-space position and orientation of a
// point on the critter's surface parameterised by (t, theta, off).
//
//	t     ∈ [0,1] runs from tail tip (0) to head tip (1) along the
//	             spine.
//	theta ∈ [0, 2π] sweeps around the body at that t; 0 is the
//	             local "normal" axis of the perpendicular frame
//	             (corresponds to ring vert 0 in BuildMesh).
//	off   has two meanings depending on the band:
//	        - body surface (CapSnap < t < 1-CapSnap): a lift
//	          above the surface along the outward normal. 0 sits
//	          flush; positive floats the part outwards.
//	        - cap (t ≤ CapSnap or t ≥ 1-CapSnap): radial distance
//	          from the spine endpoint within the cap disc plane,
//	          so a part can land anywhere on the cap rather than
//	          only at the centre tip.
//
// Inside the body band, outward is the radial surface normal and
// along is the head-ward spine tangent. In the cap band, outward
// becomes ±tangent (pointing out of the tip) and along becomes
// the radial direction at angle theta — theta then controls the
// roll of the attached part around the snout axis.
func (c *Critter) AnchorPoint(t, theta, off float32) (pos, outward, along Vec3) {
	s := c.sampleSpineAt(t)
	n, b := frameFromTangent(s.tan, Vec3{Y: 1})
	cs := float32(math.Cos(float64(theta)))
	sn := float32(math.Sin(float64(theta)))
	radial := Vec3{
		X: n.X*cs + b.X*sn,
		Y: n.Y*cs + b.Y*sn,
		Z: n.Z*cs + b.Z*sn,
	}
	if t < CapSnap {
		// Tail cap: tip points away from the head; `off` is the
		// radial distance on the cap disc.
		return Vec3{
			X: s.pos.X + radial.X*off,
			Y: s.pos.Y + radial.Y*off,
			Z: s.pos.Z + radial.Z*off,
		}, Vec3{X: -s.tan.X, Y: -s.tan.Y, Z: -s.tan.Z}, radial
	}
	if t > 1-CapSnap {
		// Head cap: tip points away from the tail; `off` is the
		// radial distance on the cap disc.
		return Vec3{
			X: s.pos.X + radial.X*off,
			Y: s.pos.Y + radial.Y*off,
			Z: s.pos.Z + radial.Z*off,
		}, s.tan, radial
	}
	r := s.radius + off
	return Vec3{
		X: s.pos.X + radial.X*r,
		Y: s.pos.Y + radial.Y*r,
		Z: s.pos.Z + radial.Z*r,
	}, radial, s.tan
}

// CapSnap is the width of the snap zone at each end of the spine
// inside which a hover/click is treated as landing on the tip cap
// rather than on the lateral tube surface. Picked small enough
// not to interfere with side-of-head placement but wide enough
// that a click near the head tip reliably lands on the cap.
const CapSnap = float32(0.05)

// ClosestAnchor inverts AnchorPoint: given an arbitrary body-space
// point (typically a mouse-picker raycast hit on the surface), it
// returns the (t, theta, off) triple whose AnchorPoint lands
// closest. A coarse spine scan picks the nearest sample; theta is
// read off as the angle of the perpendicular projection in that
// sample's local frame; off is 0 for body hits and the cap-disc
// radial distance for cap hits (so the caller can place a part
// anywhere on the cap, not just at the tip centre).
//
// When the closest sample is one of the spine endpoints we snap t
// to an exact 0 (tail cap) or 1 (head cap) so AnchorPoint's cap
// branch fires reliably — without that, hits on the visible cap
// disc end up landing on the side of the tip ring rather than on
// the cap itself.
func (c *Critter) ClosestAnchor(p Vec3) (t, theta, off float32) {
	const samples = 64
	if samples < 2 {
		return 0, 0, 0
	}
	bestI := 0
	bestD := float32(-1)
	for i := 0; i < samples; i++ {
		ti := float32(i) / float32(samples-1)
		s := c.sampleSpineAt(ti)
		dx := p.X - s.pos.X
		dy := p.Y - s.pos.Y
		dz := p.Z - s.pos.Z
		d := dx*dx + dy*dy + dz*dz
		if bestD < 0 || d < bestD {
			bestD = d
			bestI = i
		}
	}
	switch bestI {
	case 0:
		t = 0
	case samples - 1:
		t = 1
	default:
		t = float32(bestI) / float32(samples-1)
	}
	s := c.sampleSpineAt(t)
	n, b := frameFromTangent(s.tan, Vec3{Y: 1})
	dp := Vec3{X: p.X - s.pos.X, Y: p.Y - s.pos.Y, Z: p.Z - s.pos.Z}
	dn := dotV(dp, n)
	db := dotV(dp, b)
	theta = float32(math.Atan2(float64(db), float64(dn)))
	if t == 0 || t == 1 {
		// On a cap: the meaningful offset is the radial distance
		// from the spine endpoint within the cap's perpendicular
		// plane. Body hits keep off=0 since the body-band branch
		// of AnchorPoint already adds s.radius itself.
		off = float32(math.Sqrt(float64(dn*dn + db*db)))
	}
	return t, theta, off
}

func dotV(a, b Vec3) float32 { return a.X*b.X + a.Y*b.Y + a.Z*b.Z }
