package internal

import (
	"math"
	"math/rand/v2"

	"graphics.gd/classdb/ArrayMesh"
	"graphics.gd/classdb/Mesh"
	"graphics.gd/classdb/MeshInstance3D"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Vector3"

	"the.quetzal.community/aviary/internal/critter"
)

// Gait tuning. Picked by eye against the editor's default critter
// (5-bone spine, 2 leg pairs at ~0.5 unit limb length). The ratios
// are length-relative so the same numbers read fine on a tiny
// hamster or a long-legged stilted thing.
const (
	// gaitCycleRate is the leg cycle frequency while moving (Hz).
	// 1.5 reads as a brisk walk rather than a sprint; with full
	// stride this puts foot ground-velocity in the right neighbourhood
	// of controlWalkSpeed (≈ 2 units/s) to keep foot-slip subtle.
	gaitCycleRate = float32(1.5)

	// gaitStrideRatio is the half-amplitude of the foot's Z swing as
	// a fraction of leg length. A foot moves ±strideRatio·legLen in
	// body-local Z over a cycle.
	gaitStrideRatio = float32(0.25)

	// gaitLiftRatio is the peak foot lift in +Y, as a fraction of leg
	// length. Too small and the swing looks like a shuffle; too large
	// and the critter looks like it's high-stepping over invisible
	// obstacles.
	gaitLiftRatio = float32(0.20)

	// gaitAttackRate / gaitReleaseRate are exponential-blend rates
	// (1/seconds) for easing gaitActive toward 1 when WASD is held
	// and toward 0 when released. Attack is faster than release so
	// the critter starts striding promptly but settles back to rest
	// gradually — matches the "they start running when you press,
	// then stop in a step or two" feel of platformer chase cams.
	gaitAttackRate  = float32(8.0)
	gaitReleaseRate = float32(4.0)

	// Body-motion amplitudes. The body itself isn't skinned to the
	// spine bones (Skeleton3D + Skin migration is still pending), so
	// we can't deform the spine cheaply per-frame. Instead the whole
	// body Node3D gets a small bob/roll/pitch oscillation that reads
	// as "the critter is alive while walking" without any mesh
	// rebuild cost.
	//
	// Frequencies relative to the gait cycle:
	//   bob   2×  (body drops each time a diagonal pair lands)
	//   roll  1×  (lateral lean alternates left/right per cycle)
	//   pitch 2×  (head dips on each push-off, twice per cycle)
	gaitBodyBob   = float32(0.025) // body-local Y units
	gaitBodyRoll  = float32(0.060) // radians (~3.4°)
	gaitBodyPitch = float32(0.040) // radians (~2.3°)

	// Jump tuning. The curve is in three pieces:
	//
	//   t ∈ [0, jumpCrouchFraction)        — crouch dip (preload).
	//   t ∈ [crouch, 1 − jumpLandFraction] — airborne arc (sin·π).
	//   t ∈ (1 − land, 1]                  — landing recoil dip.
	//
	// Continuity at the boundaries falls out of using sin·π for
	// every piece (sin(0)=sin(π)=0), so the body Y curve glues
	// together without explicit smoothing. Returns world-Y delta
	// to be added on top of the rest pose.
	// Beefier preload makes the leap read as a coiled spring rather
	// than a stiff hop: deeper crouch dip and a longer crouch window
	// so the eye has time to register the prep before the body
	// launches. Landing recoil mirrors the crouch.
	jumpDuration       = float32(0.85)
	jumpHeight         = float32(0.85)
	jumpCrouchDepth    = float32(0.22)
	jumpCrouchFraction = float32(0.22)
	jumpLandFraction   = float32(0.16)

	// Head-look tuning. The body mesh isn't skinned to its spine
	// bones (Skeleton3D + Skin migration is still pending), so we
	// can't bend the neck independently. Instead we yaw the whole
	// body around a pivot near the tail — the head end traces a
	// much larger arc than the tail does, so the result reads as
	// the critter craning its neck rather than swivelling its
	// whole stance.
	//
	// Event-based scheduling so the look isn't a metronome: short
	// random gap, sin·π curve over the event window, RNG picks the
	// peak yaw direction + magnitude.
	headLookMaxAngle    = float32(0.45) // radians (~26°)
	headLookEventMin    = float32(1.2)  // seconds
	headLookEventMax    = float32(2.5)
	headLookGapMin      = float32(4.0)
	headLookGapMax      = float32(12.0)
	headLookPivotLocalZ = float32(-0.8) // body-local Z, near tail
)

// gaitLegRender pairs one MeshInstance3D with its ArrayMesh for a
// single rendered leg side. controlVis carries one (right, left)
// pair per data leg in cv.legRenders.
type gaitLegRender struct {
	node MeshInstance3D.Instance
	mesh ArrayMesh.Instance
}

// setupGaitLegs spawns 2 MeshInstance3Ds per data leg (right, left)
// under a fresh container parented to body.mesh, then hides the
// body's own leg MeshInstance3Ds so we don't render two copies
// stacked on top of each other. Caches the spawned (node, mesh)
// pairs on cv so per-frame uploads can skip the scene-tree walk.
func (ce *CritterEditor) setupGaitLegs(cv *controlVis) {
	if ce.body.critter == nil || ce.body.mesh == MeshInstance3D.Nil {
		return
	}
	container := Node3D.New()
	ce.body.mesh.AsNode().AddChild(container.AsNode())
	cv.legContainer = container
	legs := ce.body.critter.Legs()
	cv.legRenders = make([][2]gaitLegRender, len(legs))
	for i := range legs {
		for s := 0; s < 2; s++ {
			mi := MeshInstance3D.New()
			am := ArrayMesh.New()
			mi.AsMeshInstance3D().SetMesh(am.AsMesh())
			container.AsNode().AddChild(mi.AsNode())
			cv.legRenders[i][s] = gaitLegRender{node: mi, mesh: am}
		}
	}
	// Hide the body's own leg renders so we own the leg pixels for
	// the duration of the view. controlExit (via teardownGaitLegs)
	// reverses this.
	for _, mi := range ce.body.legNodes {
		if mi != MeshInstance3D.Nil {
			mi.AsNode3D().SetVisible(false)
		}
	}
	// Push an initial rest pose so the first frame isn't a flash of
	// empty geometry while we wait for the first upload tick.
	ce.uploadGaitLegs(cv)
}

// teardownGaitLegs frees the gait container (which QueueFrees its
// child MeshInstance3Ds) and restores visibility on the body's own
// leg renders. Idempotent.
func (ce *CritterEditor) teardownGaitLegs(cv *controlVis) {
	if cv.legContainer != Node3D.Nil {
		cv.legContainer.AsNode().QueueFree()
		cv.legContainer = Node3D.Nil
	}
	cv.legRenders = nil
	for _, mi := range ce.body.legNodes {
		if mi != MeshInstance3D.Nil {
			mi.AsNode3D().SetVisible(true)
		}
	}
}

// uploadGaitLegs computes per-side leg poses at the current
// (gaitTime, gaitActive) and re-skins each MeshInstance3D. Called
// every PhysicsProcess while the control view is active — cheap
// because per-leg mesh is small (~50 verts) and BuildLegMesh is a
// straight CPU walk.
//
// If the leg count has changed since the last upload (a sculpt
// arrived while in control view), the gait nodes are torn down and
// respawned to match.
func (ce *CritterEditor) uploadGaitLegs(cv *controlVis) {
	if ce.body.critter == nil || cv.legContainer == Node3D.Nil {
		return
	}
	legs := ce.body.critter.Legs()
	if len(legs) != len(cv.legRenders) {
		ce.teardownGaitLegs(cv)
		ce.setupGaitLegs(cv)
		return
	}
	// Collect the animated foot positions per (data leg, side) as we
	// build the meshes — fed to CritterBody.SetAnimatedLegFeet below
	// so OnLeg-anchored parts (duck-foot steppers) ride the same
	// animated foot the rendered leg mesh terminates at. The slice
	// uses 2 entries per leg (right, left); side 1's pose has X
	// already negated by computeGaitPose so we can store the values
	// straight in.
	// Compute the same body-Y overlay applyBodyGait will apply this
	// frame (gait bob + jump curve) so the leg poses can subtract it
	// from foot Y. The body is about to move up/down by `bodyDY`
	// world-units; if we DON'T compensate, the feet ride that motion
	// and look like they're glued to the ankles — what the user
	// wants is feet planted on the ground while the body crouches
	// and leaps over them.
	bodyDY := bodyAnimationY(cv)
	feet := make([][2]critter.Vec3, len(legs))
	for i, leg := range legs {
		for s := 0; s < 2; s++ {
			phase := gaitPhase(cv.gaitTime, i, len(legs), s)
			posed := computeGaitPose(leg, phase, cv.gaitActive, s == 1, bodyDY)
			feet[i][s] = posed.Foot
			m := ce.body.critter.BuildLegMesh(posed, 6, 8, false)
			uploadCritterMesh(cv.legRenders[i][s].mesh, m)
		}
	}
	ce.body.SetAnimatedLegFeet(feet)
}

// gaitPhase returns the cycle phase ∈ [0, 1) for one rendered leg.
// Sides are 180° out of phase (0.5 offset between right and left of
// the same data leg), and successive data legs are scattered by
// 1/N so a 4-pair quadruped lands on a recognisable diagonal trot
// without having to encode the gait pattern as a separate table.
func gaitPhase(t float32, legIdx, legCount, side int) float32 {
	var base float32
	if legCount > 0 {
		base = float32(legIdx) / float32(legCount)
	}
	if side == 1 {
		base += 0.5
	}
	p := t + base
	p -= float32(math.Floor(float64(p)))
	if p < 0 {
		p += 1
	}
	return p
}

// computeGaitPose returns a Leg pose with foot/knee offset by the
// current gait phase, blended against the rest pose by `active`.
// When leftSide is true the +X positions are mirrored to the −X
// side so the caller can build the mesh one-sided (BuildLegMesh
// with mirror=false) and have the result land on the correct half
// of the body.
//
// Phase semantics:
//   - [0.0, 0.5] stance: foot stays on the ground (Y rest), moves
//     from +stride (in front) to −stride (behind) along body Z.
//   - [0.5, 1.0] swing: foot moves back to +stride and lifts in a
//     sin·π arc so it peaks at lift mid-swing then lands flat.
//
// `bodyDY` is the body's current vertical animation offset (jump +
// bob) in world units. Only the DOWNWARD half (bodyDY < 0) is
// compensated — that keeps the feet planted at world Y ≈ 0 while
// the body crouches and during landing recoil, so the IK bends
// the knees naturally. On the way UP (bodyDY > 0) we deliberately
// don't compensate: the feet tuck under the body and ride along
// instead of stretching into 1.5-metre poles dangling to the
// ground from the apex of the jump.
//
// The knee is solved by 2-bone analytic IK from the live foot, with
// the rest-knee position used to disambiguate the bend direction
// (so a foreleg keeps bending forward and a hind leg keeps bending
// backward even when the foot tracks far from rest).
func computeGaitPose(leg critter.Leg, phase, active float32, leftSide bool, bodyDY float32) critter.Leg {
	legLen := vecDist(leg.Hip, leg.Foot)
	stride := gaitStrideRatio * legLen
	lift := gaitLiftRatio * legLen
	var dz, dy float32
	if phase < 0.5 {
		// Stance: foot moves from +stride (front) to −stride (back).
		dz = stride * (1 - 4*phase)
	} else {
		// Swing: foot returns from −stride to +stride and lifts.
		t := (phase - 0.5) * 2 // 0..1
		dz = stride * (2*t - 1)
		dy = lift * float32(math.Sin(math.Pi*float64(t)))
	}
	// One-sided compensation: only plant-the-feet when the body is
	// dropping (crouch or landing recoil). Going up, leave the foot
	// at rest pose so it lifts with the body — see doc comment
	// above for the full rationale.
	planted := bodyDY
	if planted > 0 {
		planted = 0
	}
	foot := critter.Vec3{
		X: leg.Foot.X,
		Y: leg.Foot.Y + dy*active - planted,
		Z: leg.Foot.Z + dz*active,
	}
	lenFemur := vecDist(leg.Hip, leg.Knee)
	lenTibia := vecDist(leg.Knee, leg.Foot)
	// Cap how far the foot can stretch from the hip so a tall jump
	// doesn't blow the leg up to a four-foot-long noodle. Beyond
	// 1.6× the rest reach the foot pins to that radius along the
	// hip→foot direction — IK then locks the leg straight and the
	// extra body Y is absorbed by the part landing slightly off
	// the ground rather than by the limb mesh stretching.
	maxR := (lenFemur + lenTibia) * 1.6
	dx := foot.X - leg.Hip.X
	dy2 := foot.Y - leg.Hip.Y
	dz2 := foot.Z - leg.Hip.Z
	d := float32(math.Sqrt(float64(dx*dx + dy2*dy2 + dz2*dz2)))
	if d > maxR && d > 1e-6 {
		k := maxR / d
		foot.X = leg.Hip.X + dx*k
		foot.Y = leg.Hip.Y + dy2*k
		foot.Z = leg.Hip.Z + dz2*k
	}
	knee := twoBoneIK(leg.Hip, foot, lenFemur, lenTibia, leg.Knee)
	posed := critter.Leg{
		Attach:     leg.Attach,
		Hip:        leg.Hip,
		Knee:       knee,
		Foot:       foot,
		HipRadius:  leg.HipRadius,
		KneeRadius: leg.KneeRadius,
		FootRadius: leg.FootRadius,
	}
	if leftSide {
		posed.Hip.X = -posed.Hip.X
		posed.Knee.X = -posed.Knee.X
		posed.Foot.X = -posed.Foot.X
	}
	return posed
}

// twoBoneIK places the knee given a fixed hip, a target foot, the
// rest-pose femur/tibia lengths, and a rest-knee position used only
// as a hint for which side of the hip-foot line the knee bends to.
//
// Distances clamp into [|lF − lT|, lF + lT] so an unreachable target
// stretches the leg along the hip→foot line rather than producing
// NaNs from the law-of-cosines step. The bend direction is the
// rest-knee's projection onto the plane perpendicular to hip→foot,
// renormalised — collapse to a fallback (world-down then world-+X)
// when the rest knee happens to be collinear with the hip→foot line.
func twoBoneIK(hip, foot critter.Vec3, lenF, lenT float32, restKnee critter.Vec3) critter.Vec3 {
	d := critter.Vec3{X: foot.X - hip.X, Y: foot.Y - hip.Y, Z: foot.Z - hip.Z}
	D := vecLen(d)
	if D < 1e-6 {
		return restKnee
	}
	minR := absF(lenF - lenT)
	maxR := lenF + lenT
	if D > maxR {
		D = maxR
	}
	if D < minR {
		D = minR
	}
	cosA := (lenF*lenF + D*D - lenT*lenT) / (2 * lenF * D)
	if cosA > 1 {
		cosA = 1
	}
	if cosA < -1 {
		cosA = -1
	}
	sinA := float32(math.Sqrt(float64(1 - cosA*cosA)))
	inv := 1 / vecLen(d)
	axis := critter.Vec3{X: d.X * inv, Y: d.Y * inv, Z: d.Z * inv}
	restOff := critter.Vec3{
		X: restKnee.X - hip.X,
		Y: restKnee.Y - hip.Y,
		Z: restKnee.Z - hip.Z,
	}
	along := restOff.X*axis.X + restOff.Y*axis.Y + restOff.Z*axis.Z
	bend := critter.Vec3{
		X: restOff.X - along*axis.X,
		Y: restOff.Y - along*axis.Y,
		Z: restOff.Z - along*axis.Z,
	}
	bm := vecLen(bend)
	if bm < 1e-6 {
		// Rest knee collinear with hip→foot. Fall back to world
		// −Y (legs bend downward); if that's also collinear (a
		// vertical leg), pick +X.
		down := critter.Vec3{Y: -1}
		along = down.Y * axis.Y
		bend = critter.Vec3{X: -along * axis.X, Y: -1 - along*axis.Y, Z: -along * axis.Z}
		bm = vecLen(bend)
		if bm < 1e-6 {
			bend = critter.Vec3{X: 1}
			bm = 1
		}
	}
	binv := 1 / bm
	bend = critter.Vec3{X: bend.X * binv, Y: bend.Y * binv, Z: bend.Z * binv}
	return critter.Vec3{
		X: hip.X + lenF*(cosA*axis.X+sinA*bend.X),
		Y: hip.Y + lenF*(cosA*axis.Y+sinA*bend.Y),
		Z: hip.Z + lenF*(cosA*axis.Z+sinA*bend.Z),
	}
}

// headLookState is the per-critter scheduler for the idle "look
// left / right" head sway. Holds an RNG so multiple critters
// scheduled side-by-side don't fire events in lockstep, plus the
// current event's parameters (peak yaw + duration). Output is read
// from `angle` after each advance() call.
type headLookState struct {
	rng *rand.Rand

	elapsed   float32
	eventEnds float32 // elapsed time at which the current event finishes
	nextEvent float32 // elapsed time at which the next event starts
	duration  float32 // length of the current event
	target    float32 // peak yaw of the current event (signed)

	angle float32 // current yaw output; read by applyBodyGait
}

func newHeadLookState(seed uint64) *headLookState {
	h := &headLookState{
		rng: rand.New(rand.NewPCG(seed, seed*6364136223846793005+1442695040888963407)),
	}
	// Initial gap before the first event so a fresh critter doesn't
	// jerk its head the instant control view opens.
	h.scheduleNext(0)
	return h
}

func (h *headLookState) scheduleNext(from float32) {
	gap := headLookGapMin + h.rng.Float32()*(headLookGapMax-headLookGapMin)
	h.nextEvent = from + gap
}

// advance ticks the schedule by `delta` seconds and updates h.angle
// to the current sin·π yaw offset. Returns the same value for
// callers that want to chain inline; applyBodyGait reads h.angle
// directly so it doesn't need the return value.
func (h *headLookState) advance(delta float32) float32 {
	h.elapsed += delta
	switch {
	case h.elapsed < h.eventEnds:
		eventStart := h.eventEnds - h.duration
		t := (h.elapsed - eventStart) / h.duration
		h.angle = h.target * float32(math.Sin(math.Pi*float64(t)))
	case h.elapsed >= h.nextEvent:
		// Start a new event. Pick duration, magnitude (50%–100% of
		// max), and direction independently — the magnitude scale
		// keeps the look "interesting" (some sharp, some lazy).
		h.duration = headLookEventMin + h.rng.Float32()*(headLookEventMax-headLookEventMin)
		sign := float32(1)
		if h.rng.Float32() < 0.5 {
			sign = -1
		}
		mag := 0.5 + h.rng.Float32()*0.5
		h.target = sign * mag * headLookMaxAngle
		h.eventEnds = h.elapsed + h.duration
		h.scheduleNext(h.eventEnds)
		h.angle = 0
	default:
		h.angle = 0
	}
	return h.angle
}

// bodyAnimationY returns the total vertical offset (jump + gait
// bob) that applyBodyGait will impose on the body Node3D this
// frame. uploadGaitLegs reads it BEFORE applyBodyGait runs so the
// leg poses can pre-compensate — feet stay planted at world Y ≈ 0
// while the body crouches and leaps over them.
func bodyAnimationY(cv *controlVis) float32 {
	var jumpY float32
	if cv.jumpActive {
		jumpY = jumpYOffset(cv.jumpTime / jumpDuration)
	}
	phase := 2 * math.Pi * float64(cv.gaitTime)
	bobY := -gaitBodyBob * cv.gaitActive *
		float32(math.Cos(2*phase))
	return bobY + jumpY
}

// jumpYOffset returns the body-Y delta for the current jump phase.
// t is the normalised jump time in [0, 1]; outside that range
// callers should treat the jump as finished (return 0). Continuity
// is automatic — every sub-curve is sin·π, which is 0 at both
// endpoints, so the crouch, airborne, and landing pieces glue
// together without any extra smoothing.
//
//   crouch:    dip down to −jumpCrouchDepth, return to 0
//   airborne:  rise to +jumpHeight, return to 0
//   landing:   dip down to −jumpCrouchDepth, return to 0
func jumpYOffset(t float32) float32 {
	if t < 0 || t > 1 {
		return 0
	}
	switch {
	case t < jumpCrouchFraction:
		x := t / jumpCrouchFraction
		return -jumpCrouchDepth * float32(math.Sin(math.Pi*float64(x)))
	case t > 1-jumpLandFraction:
		x := (t - (1 - jumpLandFraction)) / jumpLandFraction
		return -jumpCrouchDepth * float32(math.Sin(math.Pi*float64(x)))
	default:
		span := 1 - jumpCrouchFraction - jumpLandFraction
		x := (t - jumpCrouchFraction) / span
		return jumpHeight * float32(math.Sin(math.Pi*float64(x)))
	}
}

// updateGaitState eases gaitActive toward 1 when the critter is
// moving and toward 0 when it isn't, using the same
// frame-rate-independent exponential blend pattern as the camera
// recenter. gaitTime advances regardless so a brief stop-and-start
// doesn't reset the cycle — phases stay continuous and a leg that
// was mid-swing keeps swinging when motion resumes.
func updateGaitState(cv *controlVis, moving bool, delta float32) {
	var rate float32
	target := float32(0)
	if moving {
		rate = gaitAttackRate
		target = 1
	} else {
		rate = gaitReleaseRate
	}
	t := 1 - float32(math.Exp(-float64(rate)*float64(delta)))
	cv.gaitActive += (target - cv.gaitActive) * t
	cv.gaitTime += delta * gaitCycleRate
	// Keep gaitTime bounded so float drift doesn't accumulate over a
	// very long session. Wrap at 1 — gaitPhase wraps again per-leg
	// after adding the per-leg offset, so this is purely numerical
	// hygiene.
	cv.gaitTime -= float32(math.Floor(float64(cv.gaitTime)))
}

// uploadCritterMesh copies a critter.Mesh (CPU-side verts/normals/
// indices) into the given ArrayMesh, replacing its single surface.
// Same conversion pattern as rebuildLegs in critter_body.go — kept
// local so the gait path doesn't have to reach into private body
// internals.
func uploadCritterMesh(am ArrayMesh.Instance, m critter.Mesh) {
	verts := make([]Vector3.XYZ, len(m.Verts))
	for j, v := range m.Verts {
		verts[j] = Vector3.XYZ{X: Float.X(v.X), Y: Float.X(v.Y), Z: Float.X(v.Z)}
	}
	normals := make([]Vector3.XYZ, len(m.Normals))
	for j, n := range m.Normals {
		normals[j] = Vector3.XYZ{X: Float.X(n.X), Y: Float.X(n.Y), Z: Float.X(n.Z)}
	}
	am.ClearSurfaces()
	var arrays [Mesh.ArrayMax]any
	arrays[Mesh.ArrayVertex] = verts
	arrays[Mesh.ArrayNormal] = normals
	arrays[Mesh.ArrayIndex] = m.Indices
	am.AddSurfaceFromArrays(Mesh.PrimitiveTriangles, arrays[:])
}

func vecDist(a, b critter.Vec3) float32 {
	return vecLen(critter.Vec3{X: b.X - a.X, Y: b.Y - a.Y, Z: b.Z - a.Z})
}

func vecLen(v critter.Vec3) float32 {
	return float32(math.Sqrt(float64(v.X*v.X + v.Y*v.Y + v.Z*v.Z)))
}

func absF(x float32) float32 {
	if x < 0 {
		return -x
	}
	return x
}
