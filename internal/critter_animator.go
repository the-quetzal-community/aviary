package internal

import (
	"time"

	"graphics.gd/classdb/MeshInstance3D"
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/variant/Angle"
	"graphics.gd/variant/Basis"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Vector3"
)

// critterAnimatorWalkSpeed is the horizontal world-units/second above which a
// placed creation is treated as walking (the gait eases in); below it the legs
// settle to the idle rest pose. Mirrors AvatarFlight's avatarWalkSpeed.
const critterAnimatorWalkSpeed = Float.X(0.1)

// critterAnimatorAltitudeDeadZone is the height band (world units, either side
// of the terrain surface) inside which a creation's root is treated as simply
// standing on the ground — snap residue and tween lag on gentle slopes land in
// here. Outside it, the excess altitude feeds gaitState.rootDY so the legs
// respond to the root's vertical motion: a possession jump dips the root below
// the terrain for its crouch (knees bend, feet stay planted) then arcs it
// upward (feet tuck and ride). Well below jumpCrouchDepth (0.22) so the crouch
// preload always reads.
const critterAnimatorAltitudeDeadZone = float32(0.05)

// critterAnimatorMaxCrouch caps how far below the terrain the root can sink
// while the legs still try to keep feet planted — past that (a cliff-edge
// height mismatch, a bad tween) the compensation would hyper-extend the pose,
// so it saturates instead.
const critterAnimatorMaxCrouch = float32(0.5)

// CritterAnimator drives a placed user-design creation's procedural animation
// from its own motion — using the SAME shared code the critter editor's control
// (walk-test) view uses: the leg gait (gaitState), the idle breathing + head-look
// (applyCritterIdle), and the walk body bob/roll/pitch (gaitState.bobOffset).
//
// It watches the creation root's per-frame horizontal displacement: moving → legs
// step and the body bobs; still → legs settle to rest while breathing + the
// occasional head glance keep it alive, exactly like the editor critter.
//
// The body bob is applied over the body MESH's base LOCAL transform (restored
// each frame), so it bobs in place under the possession/walk-driven wrapper
// rather than fighting the wrapper's world position.
type CritterAnimator struct {
	Node.Extension[CritterAnimator]

	root Node3D.Instance // the placed creation root, sampled for movement
	gait gaitState       // shared leg-gait driver; gait.body is &body below
	body CritterBody     // owns the live body so &body stays valid for gait

	// heightAt resolves the terrain surface under a point (the same
	// hook AvatarFlight takes). When set, the root's altitude above
	// the terrain drives gaitState.rootDY — the legs crouch/tuck in
	// response to vertical root motion such as the possession jump.
	// Nil for preview renders (no terrain to measure against).
	heightAt func(Vector3.XYZ) Float.X

	headLook    *headLookState
	breatheTime float32

	// bodyBase* is the body mesh's resting local transform, captured once; the
	// walk bob is layered over it each frame.
	bodyBasePos   Vector3.XYZ
	bodyBaseBasis Basis.XYZ

	last Vector3.XYZ
	have bool
}

func (a *CritterAnimator) Ready() {
	a.gait.body = &a.body
	// Bob the body AND pre-compensate the feet for that bob, exactly like the
	// editor's control view (the placed creature's wrapper is moved externally, so
	// the bob rides the body mesh's local transform — see Process).
	a.gait.applyBodyBob = true
	a.gait.setupLegs()
	a.headLook = newHeadLookState(uint64(time.Now().UnixNano()))
	if a.body.mesh != MeshInstance3D.Nil {
		a.bodyBasePos = a.body.mesh.AsNode3D().Position()
		a.bodyBaseBasis = a.body.mesh.AsNode3D().Basis()
	}
	if a.root != Node3D.Nil {
		a.last = a.root.GlobalPosition()
		a.have = true
	}
}

func (a *CritterAnimator) Process(delta Float.X) {
	if a.gait.body == nil || a.gait.body.critter == nil || a.root == Node3D.Nil {
		return
	}
	// Movement detection (horizontal speed only — vertical terrain-follow
	// shouldn't read as walking) drives the gait active/idle blend, and
	// the measured speed drives the speed-matched cycle rate so the legs
	// patter faster when the creation is pushed faster.
	pos := a.root.GlobalPosition()
	moving := false
	var speed Float.X
	if a.have && delta > 0 {
		speed = Vector3.Distance(
			Vector3.XYZ{X: a.last.X, Z: a.last.Z},
			Vector3.XYZ{X: pos.X, Z: pos.Z},
		) / delta
		moving = speed > critterAnimatorWalkSpeed
	}
	a.last = pos
	a.have = true

	// Idle aliveness on the skeleton (breathing puff + occasional head glance).
	a.breatheTime += float32(delta)
	applyCritterIdle(a.gait.body, a.headLook, a.breatheTime, float32(delta), true)

	// Vertical root motion → leg response. Altitude above the terrain
	// (outside a small stand-on-ground dead zone) feeds the gait as an
	// external body offset: the possession jump's crouch dip bends the
	// knees while the feet hold the ground, and its airborne arc lets
	// the feet tuck and ride. Derived purely from the synced position,
	// so every client reconstructs the same response.
	a.gait.rootDY = 0
	if a.heightAt != nil {
		alt := float32(pos.Y - a.heightAt(Vector3.XYZ{X: pos.X, Z: pos.Z}))
		switch {
		case alt > critterAnimatorAltitudeDeadZone:
			a.gait.rootDY = alt - critterAnimatorAltitudeDeadZone
		case alt < -critterAnimatorAltitudeDeadZone:
			a.gait.rootDY = alt + critterAnimatorAltitudeDeadZone
			if a.gait.rootDY < -critterAnimatorMaxCrouch {
				a.gait.rootDY = -critterAnimatorMaxCrouch
			}
		}
	}

	// Leg gait.
	a.gait.update(moving, float32(speed), float32(delta))
	a.gait.uploadLegs()

	// Walk body bob/roll/pitch over the mesh's base local transform.
	bobY, rollZ, pitchX := a.gait.bobOffset()
	mesh := a.gait.body.mesh.AsNode3D()
	p := a.bodyBasePos
	p.Y += Float.X(bobY)
	mesh.SetPosition(p)
	mesh.SetBasis(a.bodyBaseBasis)
	mesh.Rotate(Vector3.New(float32(0), float32(0), float32(1)), Angle.Radians(rollZ))
	mesh.Rotate(Vector3.New(float32(1), float32(0), float32(0)), Angle.Radians(pitchX))

	// Parts ride the current bone poses (breathing/head-look) + animated feet.
	a.gait.body.RepositionPartsAnimated()
}

// attachCritterAnimator hands the freshly-built body to a CritterAnimator and
// parents it under root, so the placed creation animates from its own motion. The
// animator's Ready (fired when root enters the tree) sets up the gait leg renders.
// heightAt (nil-able, see the field doc) lets the legs react to the root's
// altitude above the terrain — the possession jump's crouch and leap.
func attachCritterAnimator(root Node3D.Instance, body CritterBody, heightAt func(Vector3.XYZ) Float.X) {
	a := new(CritterAnimator)
	a.root = root
	a.body = body
	a.heightAt = heightAt
	a.AsNode().SetName("CritterAnimator")
	root.AsNode().AddChild(a.AsNode())
}
