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

// gaitProfile is one animation style's tuning: how a limb of that
// kind steps (stride/lift/duty/splay, as leg-length-relative ratios
// so the same numbers read fine on a tiny hamster or a stilted
// wader), how the legs phase against each other, and how much the
// body sways along. Ratios picked by eye against the editor's
// default critter (5-bone spine, 2 leg pairs at ~0.5 unit limbs).
type gaitProfile struct {
	// cycleRate is the fallback leg cycle frequency (Hz) when the
	// body speed is unknown (turning in place, easing to rest).
	// While moving, update() overrides it with a speed-matched rate
	// so foot ground-velocity tracks the body and slip stays subtle.
	cycleRate float32
	// stride is the half-amplitude of the foot's body-local Z sweep,
	// as a fraction of leg length.
	stride float32
	// lift is the peak swing-phase foot lift (+Y), as a fraction of
	// leg length. Small reads as a shuffle, large as high-stepping.
	lift float32
	// duty is the stance fraction of the cycle — the slice the foot
	// spends planted. Ground speed during stance = 2·stride·rate/duty.
	duty float32
	// splay bows the foot outward (+X, mirrored per side) during the
	// swing, as a fraction of lift. Gives insects their skittery
	// wide-track scuttle; zero for everything else.
	splay float32
	// bob / roll / pitch scale the shared body-motion amplitudes
	// (gaitBodyBob etc.) for this style: insects barely sway, birds
	// exaggerate the strut pitch into a head-bob.
	bob, roll, pitch float32
	// tripod switches the phase pattern to strict alternation —
	// adjacent legs and opposite sides always move in anti-phase
	// (the insect tripod). Otherwise legs trot (diagonal pairs) with
	// a slight travelling wave down the body.
	tripod bool
	// wave is the per-pair phase lag when !tripod, so long bodies
	// with many pairs ripple slightly instead of pogoing in lockstep.
	wave float32
	// kneeFallback is the IK bend direction used when a leg's rest
	// knee is collinear with hip→foot and can't disambiguate the
	// bend: mammals/birds drop the knee, insects raise it, arms
	// trail the elbow backward.
	kneeFallback critter.Vec3
}

// gaitProfiles is indexed by critter.LegKind. The arm entry only
// matters for a body whose EVERY limb is an arm (profile selection
// ignores arms otherwise) — it reuses the mammal body timing so a
// torso dragging itself around on arms still animates coherently.
var gaitProfiles = [...]gaitProfile{
	critter.LegKindMammal: {
		cycleRate: 1.6, stride: 0.30, lift: 0.22, duty: 0.55,
		bob: 1, roll: 1, pitch: 1, wave: 0.08,
		kneeFallback: critter.Vec3{Y: -1},
	},
	critter.LegKindArm: {
		cycleRate: 1.6, stride: 0.30, lift: 0.22, duty: 0.55,
		bob: 1, roll: 1, pitch: 1, wave: 0.08,
		kneeFallback: critter.Vec3{Z: -1},
	},
	critter.LegKindInsect: {
		cycleRate: 2.8, stride: 0.35, lift: 0.12, duty: 0.60, splay: 0.5,
		bob: 0.25, roll: 0.35, pitch: 0.3, tripod: true,
		kneeFallback: critter.Vec3{Y: 1},
	},
	critter.LegKindBird: {
		cycleRate: 1.9, stride: 0.38, lift: 0.32, duty: 0.60,
		bob: 1.25, roll: 0.5, pitch: 2.2, wave: 0.1,
		kneeFallback: critter.Vec3{Y: -1},
	},
}

// legKindProfile looks up the profile for one leg's kind, clamping
// unknown kinds (from a newer peer) to mammal.
func legKindProfile(kind critter.LegKind) gaitProfile {
	if kind < 0 || int(kind) >= len(gaitProfiles) {
		kind = critter.LegKindMammal
	}
	return gaitProfiles[kind]
}

const (
	// gaitArmSwingRatio is the fore-aft half-amplitude of an arm's
	// pendulum swing (fraction of arm length), and gaitArmLiftRatio
	// the slight rise of the hand at each swing extreme relative to
	// that amplitude.
	gaitArmSwingRatio = float32(0.35)
	gaitArmLiftRatio  = float32(0.25)

	// gaitArmReachSlack: a limb whose rest foot hangs more than this
	// fraction of its length above the ground plane can't plausibly
	// walk on it — it animates as an arm regardless of its kind.
	// This is what stops a shoulder-mounted "leg" from pawing at
	// air it can never reach (the old wonky-arms look).
	gaitArmReachSlack = float32(0.35)

	// Speed-matched cycle rate clamps, as multiples of the profile's
	// base rate: below the floor the gait reads as slow-motion, above
	// the ceiling as vibration.
	gaitRateFloor   = float32(0.6)
	gaitRateCeiling = float32(2.4)

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
	// Per-side scratch buffers reused across uploadCritterMesh calls
	// so the 60 Hz upload path doesn't allocate fresh slices each
	// PhysicsProcess tick.
	vertsBuf   []Vector3.XYZ
	normalsBuf []Vector3.XYZ
}

// upload copies a critter.Mesh into the cached ArrayMesh, reusing
// the per-side scratch slices for vert/normal conversion.
func (r *gaitLegRender) upload(m critter.Mesh) {
	r.vertsBuf = resizeXYZ(&r.vertsBuf, len(m.Verts))
	for j, v := range m.Verts {
		r.vertsBuf[j] = Vector3.XYZ{X: Float.X(v.X), Y: Float.X(v.Y), Z: Float.X(v.Z)}
	}
	r.normalsBuf = resizeXYZ(&r.normalsBuf, len(m.Normals))
	for j, n := range m.Normals {
		r.normalsBuf[j] = Vector3.XYZ{X: Float.X(n.X), Y: Float.X(n.Y), Z: Float.X(n.Z)}
	}
	r.mesh.ClearSurfaces()
	var arrays [Mesh.ArrayMax]any
	arrays[Mesh.ArrayVertex] = r.vertsBuf
	arrays[Mesh.ArrayNormal] = r.normalsBuf
	arrays[Mesh.ArrayIndex] = m.Indices
	r.mesh.AddSurfaceFromArrays(Mesh.PrimitiveTriangles, arrays[:])
}

// gaitState is the procedural leg-gait driver, shared by the critter editor's
// control (walk-test) view and by placed user-design creations (CritterAnimator).
// It owns the per-side animated leg meshes and the cycle state, and re-skins the
// legs each frame from the body's spine + leg data. The editor additionally
// layers a body bob/roll/pitch on top (applyBodyGait, editor-only); a placed
// creation leaves applyBodyBob false so its feet aren't pre-compensated for a bob
// that isn't applied (its body node is positioned by possession/walk, not here).
type gaitState struct {
	body         *CritterBody
	legContainer Node3D.Instance
	legRenders   [][2]gaitLegRender
	gaitTime     float32
	gaitActive   float32
	jumpActive   bool
	jumpTime     float32
	feetBuf      [][2]critter.Vec3
	applyBodyBob bool

	// rootDY is an EXTERNAL vertical animation offset (world units)
	// imposed on the critter by whoever moves its root node — e.g.
	// the possession jump arcs the placed creation's wrapper through
	// jumpYOffset, dipping it below the terrain for the crouch and
	// lifting it for the leap. It folds into the bodyDY handed to
	// computeGaitPose, so the legs respond exactly as they do to the
	// gait's own jump: knees bend to keep feet planted while the
	// root dips, feet tuck and ride while it rises. Derived from
	// observed motion (CritterAnimator altitude-above-terrain), so
	// peers reproduce it with no extra mutation. Zero when unused
	// (the editor's control view drives jumps internally instead).
	rootDY float32

	// profile is the body-level gait style — phase pattern, cycle
	// rate, body sway — picked from the dominant kind among the
	// ground-walking legs (arms don't vote). Per-limb pose shape
	// (stride/lift/splay/arm-swing) still follows each leg's OWN
	// kind, so a chimera with insect forelegs and mammal hindlegs
	// steps each limb in its own style over a shared rhythm.
	// avgLegLen (ground legs only) feeds the speed-matched cycle
	// rate in update(). Both refresh at the top of uploadLegs.
	profile   gaitProfile
	avgLegLen float32
}

// refreshProfile re-derives the body-level profile + average leg
// length from the current legs. O(legs) — cheap enough to run per
// frame, which keeps it correct when kinds/joints change mid-view
// (a peer's sculpt, an editor drag).
func (g *gaitState) refreshProfile() {
	g.profile = gaitProfiles[critter.LegKindMammal]
	g.avgLegLen = 0
	if g.body == nil || g.body.critter == nil {
		return
	}
	var counts [len(gaitProfiles)]int
	var sum float32
	ground := 0
	for _, leg := range g.body.critter.LegsView() {
		legLen := vecDist(leg.Hip, leg.Foot)
		if legActsAsArm(leg, legLen) {
			continue
		}
		kind := leg.Kind
		if kind < 0 || int(kind) >= len(gaitProfiles) {
			kind = critter.LegKindMammal
		}
		counts[kind]++
		sum += legLen
		ground++
	}
	if ground == 0 {
		return
	}
	best := critter.LegKindMammal
	for k := range counts {
		if counts[k] > counts[best] {
			best = critter.LegKind(k)
		}
	}
	g.profile = gaitProfiles[best]
	g.avgLegLen = sum / float32(ground)
}

// legActsAsArm reports whether a limb should animate as an arm: an
// explicit arm kind, or any limb whose rest foot hangs too far above
// the ground plane to plausibly walk on it (see gaitArmReachSlack).
func legActsAsArm(leg critter.Leg, legLen float32) bool {
	if leg.Kind == critter.LegKindArm {
		return true
	}
	return leg.Foot.Y-critter.GroundY > gaitArmReachSlack*legLen
}

// setupLegs spawns 2 MeshInstance3Ds per data leg (right, left) under a fresh
// container parented to body.mesh, then hides the body's own leg MeshInstance3Ds
// so we don't render two copies stacked on top of each other. Caches the spawned
// (node, mesh) pairs so per-frame uploads can skip the scene-tree walk.
func (g *gaitState) setupLegs() {
	if g.body == nil || g.body.critter == nil || g.body.mesh == MeshInstance3D.Nil {
		return
	}
	container := Node3D.New()
	g.body.mesh.AsNode().AddChild(container.AsNode())
	g.legContainer = container
	legCount := g.body.critter.LegCount()
	g.legRenders = make([][2]gaitLegRender, legCount)
	for i := 0; i < legCount; i++ {
		for s := 0; s < 2; s++ {
			mi := MeshInstance3D.New()
			am := ArrayMesh.New()
			mi.AsMeshInstance3D().SetMesh(am.AsMesh())
			container.AsNode().AddChild(mi.AsNode())
			g.legRenders[i][s] = gaitLegRender{node: mi, mesh: am}
		}
	}
	// Hide the body's own leg renders so we own the leg pixels while the gait is
	// active (the editor's controlExit / teardownLegs reverses this).
	for _, mi := range g.body.legNodes {
		if mi != MeshInstance3D.Nil {
			mi.AsNode3D().SetVisible(false)
		}
	}
	// Push an initial rest pose so the first frame isn't a flash of empty
	// geometry while we wait for the first upload tick.
	g.uploadLegs()
}

// teardownGaitLegs frees the gait container (which QueueFrees its
// child MeshInstance3Ds) and restores visibility on the body's own
// leg renders. Idempotent.
func (g *gaitState) teardownLegs() {
	if g.legContainer != Node3D.Nil {
		g.legContainer.AsNode().QueueFree()
		g.legContainer = Node3D.Nil
	}
	g.legRenders = nil
	if g.body == nil {
		return
	}
	for _, mi := range g.body.legNodes {
		if mi != MeshInstance3D.Nil {
			mi.AsNode3D().SetVisible(true)
		}
	}
}

// uploadLegs computes per-side leg poses at the current (gaitTime, gaitActive)
// and re-skins each MeshInstance3D. Called every frame while the gait is active —
// cheap because per-leg mesh is small (~50 verts) and BuildLegMesh is a straight
// CPU walk.
//
// If the leg count has changed since the last upload (a sculpt arrived while in
// control view), the gait nodes are torn down and respawned to match.
func (g *gaitState) uploadLegs() {
	if g.body == nil || g.body.critter == nil || g.legContainer == Node3D.Nil {
		return
	}
	legs := g.body.critter.LegsView()
	if len(legs) != len(g.legRenders) {
		g.teardownLegs()
		g.setupLegs()
		return
	}
	// Track kind/joint edits (peer sculpts, editor drags) before the
	// body sway below reads the profile.
	g.refreshProfile()
	// Collect the animated foot positions per (data leg, side) as we build the
	// meshes — fed to CritterBody.SetAnimatedLegFeet below so OnLeg-anchored parts
	// (duck-foot steppers) ride the same animated foot the rendered leg mesh
	// terminates at. Side 1's pose has X already negated by computeGaitPose.
	//
	// bodyDY is the body-Y overlay applyBodyGait imposes this frame (bob + jump);
	// the leg poses subtract it so feet stay planted while the body bobs/leaps.
	// Only meaningful when this gait drives the body bob (the editor); a placed
	// creation leaves applyBodyBob false, so its feet aren't compensated for a bob
	// that isn't applied.
	var bodyDY float32
	if g.applyBodyBob {
		bodyDY = g.bodyAnimationY()
	}
	// External root motion (possession jump / terrain lag) composes
	// with the gait's own body animation — see the rootDY field doc.
	bodyDY += g.rootDY
	if cap(g.feetBuf) < len(legs) {
		g.feetBuf = make([][2]critter.Vec3, len(legs))
	} else {
		g.feetBuf = g.feetBuf[:len(legs)]
	}
	for i, leg := range legs {
		for s := 0; s < 2; s++ {
			phase := gaitPhase(g.profile, g.gaitTime, i, s)
			posed := computeGaitPose(leg, phase, g.gaitActive, s == 1, bodyDY)
			g.feetBuf[i][s] = posed.Foot
			g.legRenders[i][s].upload(g.body.critter.BuildLegMesh(posed, 6, 8, false))
		}
	}
	g.body.SetAnimatedLegFeet(g.feetBuf)
}

// gaitPhase returns the cycle phase ∈ [0, 1) for one rendered leg.
// Sides are 180° out of phase, and (legIdx+side) parity puts
// diagonal limbs in phase with each other:
//
//	quadruped  → a trot (front-right steps with hind-left);
//	hexapod    → the insect tripod (with profile.tripod, exact);
//	biped/bird → simple alternating steps.
//
// Non-tripod bodies additionally lag each successive pair by
// profile.wave so long many-legged bodies ripple down the spine
// instead of pogoing in two rigid blocks.
func gaitPhase(prof gaitProfile, t float32, legIdx, side int) float32 {
	base := 0.5 * float32((legIdx+side)%2)
	if !prof.tripod {
		base += prof.wave * float32(legIdx)
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
// The pose shape follows the LEG's own kind (its gaitProfile), so a
// mixed body steps each limb in its own style:
//
//   - Ground limbs: [0, duty) stance — foot planted (Y rest),
//     sweeping from +stride (in front) to −stride (behind) along
//     body Z; [duty, 1) swing — foot returns forward, lifting in a
//     sin·π arc (insects also splay the foot outward mid-swing).
//   - Arms (explicit kind, or any limb whose rest foot can't reach
//     the ground — see legActsAsArm): a shoulder pendulum. The hand
//     sweeps fore-aft sinusoidally over the full cycle and rises
//     slightly at each extreme; no ground logic at all, which is
//     what stops high-mounted limbs air-walking against a floor
//     they'll never touch.
//
// `bodyDY` is the body's current vertical animation offset (jump +
// bob) in world units. Only the DOWNWARD half (bodyDY < 0) is
// compensated — that keeps the feet planted at world Y ≈ 0 while
// the body crouches and during landing recoil, so the IK bends
// the knees naturally. On the way UP (bodyDY > 0) we deliberately
// don't compensate: the feet tuck under the body and ride along
// instead of stretching into 1.5-metre poles dangling to the
// ground from the apex of the jump. Arms skip the compensation
// entirely — they hang from the body and ride every bob with it.
//
// The knee is solved by 2-bone analytic IK from the live foot, with
// the rest-knee position used to disambiguate the bend direction
// (so a foreleg keeps bending forward, a hind leg backward, and an
// insect knee stays peaked above the hip even when the foot tracks
// far from rest).
func computeGaitPose(leg critter.Leg, phase, active float32, leftSide bool, bodyDY float32) critter.Leg {
	prof := legKindProfile(leg.Kind)
	legLen := vecDist(leg.Hip, leg.Foot)
	arm := legActsAsArm(leg, legLen)
	var dx, dy, dz float32
	if arm {
		// Shoulder pendulum: full-cycle sinusoid fore-aft, hands
		// rising a little at both extremes (1−cos(4πt) peaks at the
		// quarter phases where sin(2πt) = ±1).
		amp := gaitArmSwingRatio * legLen
		dz = amp * float32(math.Sin(2*math.Pi*float64(phase)))
		dy = gaitArmLiftRatio * amp * 0.5 *
			(1 - float32(math.Cos(4*math.Pi*float64(phase))))
	} else {
		stride := prof.stride * legLen
		lift := prof.lift * legLen
		if phase < prof.duty {
			// Stance: foot planted, sweeping front → back.
			t := phase / prof.duty
			dz = stride * (1 - 2*t)
		} else {
			// Swing: foot returns to the front and lifts.
			t := (phase - prof.duty) / (1 - prof.duty)
			dz = stride * (2*t - 1)
			s := float32(math.Sin(math.Pi * float64(t)))
			dy = lift * s
			dx = prof.splay * lift * s
		}
	}
	// One-sided compensation: only plant-the-feet when the body is
	// dropping (crouch or landing recoil). Going up, leave the foot
	// at rest pose so it lifts with the body — see doc comment
	// above for the full rationale. Arms ride the body instead.
	planted := bodyDY
	if planted > 0 || arm {
		planted = 0
	}
	foot := critter.Vec3{
		X: leg.Foot.X + dx*active,
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
	dx2 := foot.X - leg.Hip.X
	dy2 := foot.Y - leg.Hip.Y
	dz2 := foot.Z - leg.Hip.Z
	d := float32(math.Sqrt(float64(dx2*dx2 + dy2*dy2 + dz2*dz2)))
	if d > maxR && d > 1e-6 {
		k := maxR / d
		foot.X = leg.Hip.X + dx2*k
		foot.Y = leg.Hip.Y + dy2*k
		foot.Z = leg.Hip.Z + dz2*k
	}
	knee := twoBoneIK(leg.Hip, foot, lenFemur, lenTibia, leg.Knee, prof.kneeFallback)
	posed := critter.Leg{
		Attach:     leg.Attach,
		Hip:        leg.Hip,
		Knee:       knee,
		Foot:       foot,
		HipRadius:  leg.HipRadius,
		KneeRadius: leg.KneeRadius,
		FootRadius: leg.FootRadius,
		Kind:       leg.Kind,
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
// renormalised — collapse to `fallback` (a per-kind bend hint:
// down for mammals, up for insect knees, backward for elbows; then
// world-+X as the last resort) when the rest knee happens to be
// collinear with the hip→foot line.
func twoBoneIK(hip, foot critter.Vec3, lenF, lenT float32, restKnee, fallback critter.Vec3) critter.Vec3 {
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
		// Rest knee collinear with hip→foot. Fall back to the
		// kind's bend hint; if that's also collinear (e.g. a
		// vertical leg with a downward hint), pick +X.
		if fallback == (critter.Vec3{}) {
			fallback = critter.Vec3{Y: -1}
		}
		along = fallback.X*axis.X + fallback.Y*axis.Y + fallback.Z*axis.Z
		bend = critter.Vec3{
			X: fallback.X - along*axis.X,
			Y: fallback.Y - along*axis.Y,
			Z: fallback.Z - along*axis.Z,
		}
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
func (g *gaitState) bodyAnimationY() float32 {
	var jumpY float32
	if g.jumpActive {
		jumpY = jumpYOffset(g.jumpTime / jumpDuration)
	}
	phase := 2 * math.Pi * float64(g.gaitTime)
	bobY := -gaitBodyBob * g.profile.bob * g.gaitActive *
		float32(math.Cos(2*phase))
	return bobY + jumpY
}

// bobOffset returns the body's local-Y bob (gait + jump), roll, and pitch for the
// current cycle, scaled by gaitActive. Shared by the editor's applyBodyGait and
// the placed CritterAnimator so both derive the body sway identically; each
// applies it to its own body node (the editor adds to the WASD-restored
// transform; the animator restores the body mesh's base then applies).
func (g *gaitState) bobOffset() (bobY, rollZ, pitchX float32) {
	bobY = g.bodyAnimationY()
	phase := 2 * math.Pi * float64(g.gaitTime)
	rollZ = gaitBodyRoll * g.profile.roll * g.gaitActive * float32(math.Sin(phase))
	pitchX = gaitBodyPitch * g.profile.pitch * g.gaitActive * float32(math.Sin(2*phase))
	return
}

const (
	critterBreathePeriod    = float32(4.0)  // seconds per breath
	critterBreatheAmplitude = float32(0.03) // ±3 % chest puff
)

// applyCritterIdle advances the head-look scheduler and applies the idle
// breathing puff + head-look glance to body's skeleton (composing with the leg
// gait on the same bones). Shared by the critter editor (CritterEditor.Process)
// and placed creations (CritterAnimator); the caller owns breatheTime and the
// headLookState so each critter keeps independent phase/scheduling. apply=false
// (the editor while spine-editing / placing) advances the scheduler but holds the
// body at rest.
func applyCritterIdle(body *CritterBody, headLook *headLookState, breatheTime, delta float32, apply bool) {
	if headLook != nil {
		headLook.advance(delta)
	}
	if body == nil {
		return
	}
	if !apply {
		body.SetBreathe(0)
		body.SetHeadLookYaw(0)
		return
	}
	phase := breatheTime * (2 * float32(math.Pi) / critterBreathePeriod)
	body.SetBreathe(critterBreatheAmplitude * float32(math.Sin(float64(phase))))
	if headLook != nil {
		body.SetHeadLookYaw(headLook.angle)
	}
}

// jumpYOffset returns the body-Y delta for the current jump phase.
// t is the normalised jump time in [0, 1]; outside that range
// callers should treat the jump as finished (return 0). Continuity
// is automatic — every sub-curve is sin·π, which is 0 at both
// endpoints, so the crouch, airborne, and landing pieces glue
// together without any extra smoothing.
//
//	crouch:    dip down to −jumpCrouchDepth, return to 0
//	airborne:  rise to +jumpHeight, return to 0
//	landing:   dip down to −jumpCrouchDepth, return to 0
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

// update eases gaitActive toward 1 when the critter is moving and
// toward 0 when it isn't, using the same frame-rate-independent
// exponential blend pattern as the camera recenter. gaitTime
// advances regardless so a brief stop-and-start doesn't reset the
// cycle — phases stay continuous and a leg that was mid-swing keeps
// swinging when motion resumes.
//
// `speed` is the body's current horizontal speed in world units/s
// (0 when unknown, e.g. turning in place). When moving with a known
// speed the cycle rate is matched so the feet's ground velocity
// during stance tracks the body — 2·stride·legLen per duty·cycle —
// which is what kills the moonwalk foot-slip: short-legged critters
// patter, long-legged ones lope, at the same body speed. Clamped to
// [gaitRateFloor, gaitRateCeiling]× the profile's base rate.
func (g *gaitState) update(moving bool, speed, delta float32) {
	var rate float32
	target := float32(0)
	if moving {
		rate = gaitAttackRate
		target = 1
	} else {
		rate = gaitReleaseRate
	}
	t := 1 - float32(math.Exp(-float64(rate)*float64(delta)))
	g.gaitActive += (target - g.gaitActive) * t
	cycleRate := g.profile.cycleRate
	if moving && speed > 0 && g.avgLegLen > 1e-4 && g.profile.stride > 0 {
		matched := speed * g.profile.duty / (2 * g.profile.stride * g.avgLegLen)
		lo := gaitRateFloor * g.profile.cycleRate
		hi := gaitRateCeiling * g.profile.cycleRate
		if matched < lo {
			matched = lo
		}
		if matched > hi {
			matched = hi
		}
		cycleRate = matched
	}
	g.gaitTime += delta * cycleRate
	// Keep gaitTime bounded so float drift doesn't accumulate over a
	// very long session. Wrap at 1 — gaitPhase wraps again per-leg
	// after adding the per-leg offset, so this is purely numerical
	// hygiene.
	g.gaitTime -= float32(math.Floor(float64(g.gaitTime)))
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
