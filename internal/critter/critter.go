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
	legs    []Leg
	weights map[string]float32
}

// Leg is a mirrored limb attached to one spine bone. The struct
// stores the +X-side rest-pose joint positions in body-local
// space; the renderer draws the limb on both sides by flipping X
// at draw time, so legs are always bilaterally symmetric.
//
// Multiple legs can share the same Attach (e.g. front-pair both
// socketed to the shoulder bone); they're stored independently so
// each pair can be edited in isolation. Procedural walking is a
// runtime concern — the data model only carries the rest pose
// (Hip, Knee, Foot) and a per-joint tube radius. Per-side phase
// for the gait controller lives outside this package.
//
// HipRadius / KneeRadius / FootRadius let the user thin the limb
// to a tapered claw or fatten it to a barrel — the mesh
// interpolates linearly between them along the femur and tibia.
type Leg struct {
	Attach     int
	Hip        Vec3
	Knee       Vec3
	Foot       Vec3
	HipRadius  float32
	KneeRadius float32
	FootRadius float32
}

// LegJoint names the three editable joints on a leg. The integer
// values match the order in which the editor lays handles out
// along the chain so external code (sculpt protocol) can encode
// "which joint" as a small int.
type LegJoint int

const (
	LegHip  LegJoint = 0
	LegKnee LegJoint = 1
	LegFoot LegJoint = 2
)

// GroundY is the body-local Y floor for leg joints. The editor
// lifts the body's MeshInstance3D by +0.3 above the world ground
// plane (see ensureLoaded in editor_critter.go), so body-local
// Y=−0.3 is exactly world Y=0. Clamping joint Y to ≥ GroundY here
// in the data model keeps locally-driven drags and replayed
// sculpts from poking control points underground. Must agree with
// the lift used by the editor — if that lift changes, change this
// too.
const GroundY = float32(-0.3)

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
// mutating the slice underneath. Use BonesView for read-only loops
// that don't need to outlive the next mutation.
func (c *Critter) Bones() []Bone {
	out := make([]Bone, len(c.bones))
	copy(out, c.bones)
	return out
}

// BonesView returns the underlying bone slice for read-only access.
// Caller must NOT mutate or retain across mutations — the slice is
// invalidated by any AppendHead/AppendTail/RemoveHead/RemoveTail/
// MoveBone/SetBoneRadius. Hot-path callers (per-frame renders, IK)
// use this to avoid the per-call alloc+copy of Bones().
func (c *Critter) BonesView() []Bone { return c.bones }

// BoneCount is the current number of bones in the spine chain.
func (c *Critter) BoneCount() int { return len(c.bones) }

// BoneAt returns one bone by index (zero-copy). ok=false on
// out-of-range. Hot-path read access without allocating the full
// bone slice.
func (c *Critter) BoneAt(i int) (Bone, bool) {
	if i < 0 || i >= len(c.bones) {
		return Bone{}, false
	}
	return c.bones[i], true
}

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

// Legs returns a copy of the current leg list — safe to walk or
// hand to a renderer without subsequent edits mutating the slice
// underneath. Use LegsView for read-only loops that don't need to
// outlive the next mutation.
func (c *Critter) Legs() []Leg {
	out := make([]Leg, len(c.legs))
	copy(out, c.legs)
	return out
}

// LegsView returns the underlying leg slice for read-only access.
// Caller must NOT mutate or retain across mutations — invalidated
// by AppendLeg/RemoveLeg/SetLegJoint/SetLegRadius/etc.
func (c *Critter) LegsView() []Leg { return c.legs }

// LegCount is the current number of legs.
func (c *Critter) LegCount() int { return len(c.legs) }

// AppendLeg adds a new leg with a deterministic default rest pose
// derived from the spine — used by both the local editor click and
// the replicated "leg/grow" sculpt so every client computes the
// same new entry without having to ship the joint coordinates.
// Returns the new leg's index (or -1 if the spine is empty).
func (c *Critter) AppendLeg() int {
	return c.AppendLegAt(c.defaultLegAttach())
}

// AppendLegAt adds a leg socketed to the given spine bone index,
// with the default rest pose derived from that bone's position and
// radius. Out-of-range indices clamp into the valid range.
func (c *Critter) AppendLegAt(attach int) int {
	n := len(c.bones)
	if n == 0 {
		return -1
	}
	if attach < 0 {
		attach = 0
	}
	if attach >= n {
		attach = n - 1
	}
	c.legs = append(c.legs, defaultLegPose(c.bones[attach], attach))
	return len(c.legs) - 1
}

// defaultLegAttach picks a sensible spine bone for a new leg —
// roughly the hip position on a quadruped (front quarter of the
// chain). Falls back to the head end on very short spines.
func (c *Critter) defaultLegAttach() int {
	if len(c.bones) < 2 {
		return 0
	}
	idx := len(c.bones) / 4
	if idx == 0 {
		idx = 1
	}
	return idx
}

// LegRestPoseAtPos returns the default leg rest pose for a leg
// whose hip lands at the given body-local position. Knee bends
// forward and downward, foot lands on the ground plane (GroundY).
// Attach is set to the nearest spine bone so downstream gait/IK
// code still has a chain reference. Used by both the editor's
// placement preview (ghost) and the free-attach commit path so
// they produce the exact same rest pose. ok=false on an empty
// spine.
func (c *Critter) LegRestPoseAtPos(hip Vec3) (Leg, bool) {
	if len(c.bones) == 0 {
		return Leg{}, false
	}
	if hip.X < 0 {
		hip.X = -hip.X
	}
	if hip.Y < GroundY {
		hip.Y = GroundY
	}
	attach := c.NearestBone(hip)
	footY := GroundY
	kneeY := (hip.Y + footY) * 0.5
	if kneeY < GroundY {
		kneeY = GroundY
	}
	return Leg{
		Attach:     attach,
		Hip:        hip,
		Knee:       Vec3{X: hip.X + 0.05, Y: kneeY, Z: hip.Z + 0.05},
		Foot:       Vec3{X: hip.X + 0.05, Y: footY, Z: hip.Z},
		HipRadius:  0.06,
		KneeRadius: 0.048,
		FootRadius: 0.036,
	}, true
}

// AppendLegAtPos adds a leg whose hip lands at the given body-local
// position with the default rest pose from LegRestPoseAtPos.
// Returns the new leg's index, or -1 if the spine is empty.
func (c *Critter) AppendLegAtPos(hip Vec3) int {
	leg, ok := c.LegRestPoseAtPos(hip)
	if !ok {
		return -1
	}
	c.legs = append(c.legs, leg)
	return len(c.legs) - 1
}

// NearestBone returns the index of the spine bone closest to the
// given body-local point in 3D. Used by the editor to map a
// body-surface raycast hit to an attach bone for new legs.
func (c *Critter) NearestBone(p Vec3) int {
	if len(c.bones) == 0 {
		return -1
	}
	best := 0
	var bestD float32
	for i, b := range c.bones {
		dx := p.X - b.Pos.X
		dy := p.Y - b.Pos.Y
		dz := p.Z - b.Pos.Z
		d := dx*dx + dy*dy + dz*dz
		if i == 0 || d < bestD {
			best = i
			bestD = d
		}
	}
	return best
}

// defaultLegPose returns rest joint positions for a leg socketed
// to the given bone. Hip sits on the +X body surface; knee bends
// outward and forward; foot lands on the ground plane (GroundY).
// All joints are clamped to ≥ GroundY so even a low-slung attach
// bone won't push the rest pose underground. Default per-joint
// radii reproduce the old tapered look (0.06 → 0.048 → 0.036) so
// existing scenes keep the same silhouette.
func defaultLegPose(b Bone, attach int) Leg {
	r := b.Radius
	hipY := b.Pos.Y
	footY := GroundY
	kneeY := (hipY + footY) * 0.5
	if hipY < GroundY {
		hipY = GroundY
	}
	if kneeY < GroundY {
		kneeY = GroundY
	}
	return Leg{
		Attach:     attach,
		Hip:        Vec3{X: r, Y: hipY, Z: b.Pos.Z},
		Knee:       Vec3{X: r + 0.05, Y: kneeY, Z: b.Pos.Z + 0.05},
		Foot:       Vec3{X: r + 0.05, Y: footY, Z: b.Pos.Z},
		HipRadius:  0.06,
		KneeRadius: 0.048,
		FootRadius: 0.036,
	}
}

// RemoveLeg drops the leg at index i. Returns true on a real
// removal; out-of-range indices are no-ops.
func (c *Critter) RemoveLeg(i int) bool {
	if i < 0 || i >= len(c.legs) {
		return false
	}
	c.legs = append(c.legs[:i], c.legs[i+1:]...)
	return true
}

// SetLegAttach re-sockets leg i to a different spine bone.
// Returns true on a real change.
func (c *Critter) SetLegAttach(i, bone int) bool {
	if i < 0 || i >= len(c.legs) {
		return false
	}
	if bone < 0 {
		bone = 0
	}
	if bone >= len(c.bones) {
		bone = len(c.bones) - 1
	}
	if c.legs[i].Attach == bone {
		return false
	}
	c.legs[i].Attach = bone
	return true
}

// SetLegJoint sets one joint on leg i to the given position. X is
// normalised to ≥0 since legs are mirrored across X=0 — passing a
// negative X would just flip-flop which side renders "primary".
// Y is clamped to ≥ GroundY so joints can't sink under the ground
// plane. Returns true on a real change.
func (c *Critter) SetLegJoint(i int, joint LegJoint, pos Vec3) bool {
	if i < 0 || i >= len(c.legs) {
		return false
	}
	if pos.X < 0 {
		pos.X = -pos.X
	}
	if pos.Y < GroundY {
		pos.Y = GroundY
	}
	cur := c.legPtr(i, joint)
	if *cur == pos {
		return false
	}
	*cur = pos
	return true
}

// SetLegJointAxis sets a single axis (0=X, 1=Y, 2=Z) of one joint
// on leg i. Matches the sculpt protocol where each axis ships as
// its own message (so a drag emitting Y+Z doesn't have to bundle
// the unchanged X). Returns true on a real change.
func (c *Critter) SetLegJointAxis(i int, joint LegJoint, axis int, v float32) bool {
	if i < 0 || i >= len(c.legs) {
		return false
	}
	cur := c.legPtr(i, joint)
	p := *cur
	switch axis {
	case 0:
		if v < 0 {
			v = -v
		}
		if p.X == v {
			return false
		}
		p.X = v
	case 1:
		if v < GroundY {
			v = GroundY
		}
		if p.Y == v {
			return false
		}
		p.Y = v
	case 2:
		if p.Z == v {
			return false
		}
		p.Z = v
	default:
		return false
	}
	*cur = p
	return true
}

// SetLegRadius sets all three joint radii on leg i to the given
// value — convenience for the "uniform thickness" case. Per-joint
// edits go through SetLegJointRadius. Returns true on a real
// change.
func (c *Critter) SetLegRadius(i int, r float32) bool {
	if i < 0 || i >= len(c.legs) {
		return false
	}
	if r < 0 {
		r = 0
	}
	leg := &c.legs[i]
	if leg.HipRadius == r && leg.KneeRadius == r && leg.FootRadius == r {
		return false
	}
	leg.HipRadius = r
	leg.KneeRadius = r
	leg.FootRadius = r
	return true
}

// SetLegJointRadius sets the tube radius at one joint of leg i.
// Returns true on a real change.
func (c *Critter) SetLegJointRadius(i int, joint LegJoint, r float32) bool {
	if i < 0 || i >= len(c.legs) {
		return false
	}
	if r < 0 {
		r = 0
	}
	cur := c.legRadiusPtr(i, joint)
	if *cur == r {
		return false
	}
	*cur = r
	return true
}

func (c *Critter) legRadiusPtr(i int, joint LegJoint) *float32 {
	switch joint {
	case LegHip:
		return &c.legs[i].HipRadius
	case LegKnee:
		return &c.legs[i].KneeRadius
	default:
		return &c.legs[i].FootRadius
	}
}

func (c *Critter) legPtr(i int, joint LegJoint) *Vec3 {
	switch joint {
	case LegHip:
		return &c.legs[i].Hip
	case LegKnee:
		return &c.legs[i].Knee
	default:
		return &c.legs[i].Foot
	}
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
// bones by index need to compensate. Legs that were attached to
// pre-existing bones have their Attach index bumped to match.
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
	for i := range c.legs {
		c.legs[i].Attach++
	}
	return 0
}

// RemoveHead removes the head-end bone. Refuses to drop below two
// bones (a chain needs at least one segment). Returns true on a
// real removal. Legs that were attached to the dropped bone clamp
// to the new head tip.
func (c *Critter) RemoveHead() bool {
	if len(c.bones) <= 2 {
		return false
	}
	c.bones = c.bones[:len(c.bones)-1]
	newMax := len(c.bones) - 1
	for i := range c.legs {
		if c.legs[i].Attach > newMax {
			c.legs[i].Attach = newMax
		}
	}
	return true
}

// RemoveTail removes the tail-end bone. Refuses to drop below two
// bones. Returns true on a real removal. Existing bone indices
// shift down by one — callers tracking bones by index need to
// compensate. Legs that were attached to bone 0 clamp to the new
// tail (now bone 0).
func (c *Critter) RemoveTail() bool {
	if len(c.bones) <= 2 {
		return false
	}
	c.bones = c.bones[1:]
	for i := range c.legs {
		if c.legs[i].Attach > 0 {
			c.legs[i].Attach--
		}
	}
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
	s, n, b := c.rmfAt(t)
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
	// Pre-sample the spine once + walk the RMF over the same
	// samples so theta is read off the same frame the renderer
	// draws (otherwise parts placed on a corkscrewing surface
	// would jump to a different angle after the RMF fix).
	sList := make([]sample, samples)
	for i := 0; i < samples; i++ {
		ti := float32(i) / float32(samples-1)
		sList[i] = c.sampleSpineAt(ti)
	}
	normals, binormals := sampleFrames(sList, Vec3{Y: 1})
	bestI := 0
	bestD := float32(-1)
	for i := 0; i < samples; i++ {
		s := sList[i]
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
	s := sList[bestI]
	n, b := normals[bestI], binormals[bestI]
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

// rmfAt walks the rotation-minimising frame from t=0 to the
// requested t and returns the spine sample + (normal, binormal)
// there. Resolution matches what BuildMesh uses by default
// (samplesAlong=24) so AnchorPoint and the rendered tube agree on
// the frame's rotation at any given t — without that, fixing the
// corkscrew in BuildMesh would just move it into the part
// placement, with anchored parts drifting off the surface.
func (c *Critter) rmfAt(t float32) (s sample, normal, binormal Vec3) {
	const steps = 24
	n := steps
	if t < 0 {
		t = 0
	}
	if t > 1 {
		t = 1
	}
	sList := make([]sample, n)
	for i := 0; i < n; i++ {
		ti := t * float32(i) / float32(n-1)
		sList[i] = c.sampleSpineAt(ti)
	}
	normals, binormals := sampleFrames(sList, Vec3{Y: 1})
	return sList[n-1], normals[n-1], binormals[n-1]
}

func dotV(a, b Vec3) float32 { return a.X*b.X + a.Y*b.Y + a.Z*b.Z }
