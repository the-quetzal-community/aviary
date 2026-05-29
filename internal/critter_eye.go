package internal

import (
	"math"
	"math/rand/v2"

	"graphics.gd/classdb/CollisionShape3D"
	"graphics.gd/classdb/MeshInstance3D"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/Shader"
	"graphics.gd/classdb/ShaderMaterial"
	"graphics.gd/classdb/SphereMesh"
	"graphics.gd/classdb/SphereShape3D"
	"graphics.gd/classdb/StaticBody3D"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Vector3"
)

// eyePart is a procedural critter eye: a small sphere with a
// shader-driven sclera/iris/pupil colouring and a "blink" lid.
// Holds all of its own animation state — the editor's Process tick
// is the only thing it borrows.
type eyePart struct {
	node     Node3D.Instance
	material ShaderMaterial.Instance

	// Per-eye random source so paired eyes don't blink in lockstep
	// and adjacent critters in a scene look like individuals
	// rather than copies of each other.
	rng *rand.Rand

	elapsed float32

	// Blink scheduling: animal blinks are bursty and irregular.
	// nextBlinkAt is the elapsed time at which to start the next
	// blink; blinkEndsAt is when the current blink finishes (zero
	// when no blink is in progress). When a blink ends we
	// sometimes schedule a quick second blink (pairs / triplets,
	// which most real animals do) before going back to a long
	// interval.
	nextBlinkAt float32
	blinkEndsAt float32

	// Saccade scheduling: real eyes don't drift continuously, they
	// fixate then dart. lookCurrent is the current pupil direction
	// the shader sees; lookTarget is the destination of the
	// current saccade; nextSaccadeAt is when to pick a new target.
	lookCurrent   Vector3.XYZ
	lookTarget    Vector3.XYZ
	nextSaccadeAt float32

	// hintLocal is a unit-vector look target supplied by the editor
	// (the cursor's screen-relative direction in this eye's local
	// frame). hintValid latches whether it's currently meaningful.
	// trackingUntil holds the elapsed time at which the eye exits
	// its current "lock onto cursor" burst — within that window
	// the pupil follows the hint continuously each frame, so the
	// motion looks live rather than snap-then-hold.
	hintLocal     Vector3.XYZ
	hintValid     bool
	trackingUntil float32
}

// newEyePart builds an unattached eye node and seeds its animation
// state. The phase argument is mixed into the random seed so
// previewing the same design twice still picks different schedules
// (otherwise paired eyes would lockstep).
func newEyePart(phase float32) *eyePart {
	const radius = float32(0.05)
	sphere := SphereMesh.New()
	sphere.SetRadius(radius)
	sphere.SetHeight(radius * 2)

	shader := LoadSync[Shader.Instance]("res://shader/critter_eye.gdshader")
	mat := ShaderMaterial.New()
	mat.SetShader(shader)
	mat.SetShaderParameter("blink", 0.0)
	mat.SetShaderParameter("look_dir", Vector3.XYZ{Z: 1})

	mi := MeshInstance3D.New()
	mi.SetMesh(sphere.AsMesh())
	mi.AsGeometryInstance3D().SetMaterialOverride(mat.AsMaterial())

	root := Node3D.New()
	root.AsNode().AddChild(mi.AsNode())

	body := StaticBody3D.New()
	shape := SphereShape3D.New()
	shape.SetRadius(radius)
	col := CollisionShape3D.New()
	col.SetShape(shape.AsShape3D())
	body.AsNode().AddChild(col.AsNode())
	root.AsNode().AddChild(body.AsNode())

	// Mix the phase bits into both halves of the PCG seed so every
	// new eye starts with a different schedule.
	seed1 := uint64(math.Float32bits(phase))*6364136223846793005 + 1442695040888963407
	seed2 := uint64(math.Float32bits(phase+1.234)) * 1234567891
	src := rand.NewPCG(seed1, seed2)
	e := &eyePart{
		node:        root,
		material:    mat,
		rng:         rand.New(src),
		lookCurrent: Vector3.XYZ{Z: 1},
		lookTarget:  Vector3.XYZ{Z: 1},
	}
	e.scheduleBlink(0)
	e.scheduleSaccade(0)
	return e
}

// Node returns the underlying Node3D so the caller can parent or
// position the eye.
func (e *eyePart) Node() Node3D.Instance { return e.node }

// HintFocus tells the eye where (in its own local frame) something
// interesting is — typically the mouse cursor's direction relative
// to the eye. The eye chooses, on its own saccade schedule, to
// occasionally fixate on this instead of a random drift target.
// Pass valid=false to clear.
func (e *eyePart) HintFocus(localDir Vector3.XYZ, valid bool) {
	e.hintLocal = localDir
	e.hintValid = valid
}

// Process drives blink + saccade idle behaviour, modelled after a
// resting animal: long fixations interrupted by quick darting
// saccades, and irregular bursty blinks (often paired) on a wide
// Poisson-ish interval.
func (e *eyePart) Process(delta float32) {
	e.elapsed += delta
	t := e.elapsed

	// --- Blink ---
	var blink float32
	switch {
	case t < e.blinkEndsAt:
		// Smooth open → closed → open arc over the blink window.
		blinkStart := e.blinkEndsAt - blinkDuration
		x := (t - blinkStart) / blinkDuration // 0..1
		// sin(π·x) gives a 0→1→0 hump.
		blink = float32(math.Sin(math.Pi * float64(x)))
	case t >= e.nextBlinkAt:
		// Start a new blink.
		e.blinkEndsAt = t + blinkDuration
		// 25% chance of pairing this blink with another shortly after
		// — animals often double-blink. Otherwise pick a longer
		// Poisson-like wait until the next one.
		if e.rng.Float32() < 0.25 {
			e.nextBlinkAt = t + blinkDuration + 0.15 + e.rng.Float32()*0.15
		} else {
			e.scheduleBlink(t + blinkDuration)
		}
	}
	e.material.SetShaderParameter("blink", float64(blink))

	// --- Saccade / tracking ---
	switch {
	case e.hintValid && t < e.trackingUntil:
		// Inside a tracking burst — the pupil follows the cursor
		// continuously instead of just snapping to it once. This
		// is what makes the eye feel alive when the user moves
		// the mouse: real animals fixate on novel motion for a
		// while before disengaging.
		e.lookTarget = clampLookSpread(e.hintLocal)
	case t >= e.nextSaccadeAt:
		// Pick a new saccade target. With a hint available, roll
		// for "lock onto cursor" — and if it hits, also start a
		// tracking burst so the eye keeps following while the
		// mouse moves rather than darting once and forgetting.
		if e.hintValid && e.rng.Float32() < 0.4 {
			e.lookTarget = clampLookSpread(e.hintLocal)
			e.trackingUntil = t + 1.5 + e.rng.Float32()*2.5
		} else {
			e.lookTarget = e.randomLookTarget()
			e.trackingUntil = 0
		}
		e.scheduleSaccade(t)
	}
	// Lerp toward target fast — real saccades complete in ~30-80ms.
	const saccadeSpeed = float32(18)
	a := saccadeSpeed * delta
	if a > 1 {
		a = 1
	}
	e.lookCurrent = Vector3.XYZ{
		X: e.lookCurrent.X + (e.lookTarget.X-e.lookCurrent.X)*Float.X(a),
		Y: e.lookCurrent.Y + (e.lookTarget.Y-e.lookCurrent.Y)*Float.X(a),
		Z: e.lookCurrent.Z + (e.lookTarget.Z-e.lookCurrent.Z)*Float.X(a),
	}
	e.material.SetShaderParameter("look_dir", e.lookCurrent)
}

// blinkDuration is the open→closed→open window in seconds. Real
// blinks are ~100-150 ms; this stretches a little for visual
// readability.
const blinkDuration = float32(0.18)

func (e *eyePart) scheduleBlink(now float32) {
	// Inter-blink interval drawn from a clamped exponential so most
	// gaps are 2-6 s and the occasional one is much longer (animals
	// can go ~10 s between blinks at rest).
	gap := -float32(math.Log(1-float64(e.rng.Float32()))) * 3
	if gap < 1.5 {
		gap = 1.5
	}
	if gap > 12 {
		gap = 12
	}
	e.nextBlinkAt = now + gap
}

func (e *eyePart) scheduleSaccade(now float32) {
	// Fixations last ~0.3-1.5 s in resting animals, with the
	// occasional longer hold.
	gap := 0.3 + e.rng.Float32()*1.2
	if e.rng.Float32() < 0.1 {
		gap += 1.5
	}
	e.nextSaccadeAt = now + gap
}

// clampLookSpread keeps a look target within the +Z hemisphere
// the shader's pupil math expects, and limits how far the pupil
// can drift from forward so a cursor far behind the critter
// doesn't drag the pupil into the iris/sclera edge.
func clampLookSpread(v Vector3.XYZ) Vector3.XYZ {
	const maxXY = float32(0.6)
	x := float32(v.X)
	y := float32(v.Y)
	z := float32(v.Z)
	l := float32(math.Sqrt(float64(x*x + y*y + z*z)))
	if l <= 1e-6 {
		return Vector3.XYZ{Z: 1}
	}
	x /= l
	y /= l
	z /= l
	if z < 0.1 {
		z = 0.1
	}
	if x > maxXY {
		x = maxXY
	}
	if x < -maxXY {
		x = -maxXY
	}
	if y > maxXY {
		y = maxXY
	}
	if y < -maxXY {
		y = -maxXY
	}
	// Re-normalise after clamping.
	l = float32(math.Sqrt(float64(x*x + y*y + z*z)))
	return Vector3.XYZ{X: Float.X(x / l), Y: Float.X(y / l), Z: Float.X(z / l)}
}

func (e *eyePart) randomLookTarget() Vector3.XYZ {
	// Pick a unit vector near +Z (forward) with bounded angular
	// spread so the eye keeps "looking ahead" but with realistic
	// jitter. Polar-ish sample: pick (x, y) uniformly in a disc,
	// then z = sqrt(1 - x² - y²) for the +Z hemisphere.
	const spread = float32(0.35)
	for i := 0; i < 8; i++ {
		x := (e.rng.Float32()*2 - 1) * spread
		y := (e.rng.Float32()*2 - 1) * spread
		if x*x+y*y > spread*spread {
			continue
		}
		z := float32(math.Sqrt(float64(1 - x*x - y*y)))
		return Vector3.XYZ{X: Float.X(x), Y: Float.X(y), Z: Float.X(z)}
	}
	return Vector3.XYZ{Z: 1}
}
