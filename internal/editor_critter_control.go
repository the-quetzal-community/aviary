package internal

import (
	"math"

	"graphics.gd/classdb/Camera3D"
	"graphics.gd/classdb/Input"
	"graphics.gd/classdb/MeshInstance3D"
	"graphics.gd/variant/Angle"
	"graphics.gd/variant/Basis"
	"graphics.gd/variant/Euler"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Vector3"
)

// controlVis is the saved state we restore on exit from "control"
// view. We snapshot both the camera transform and the body
// transform so flipping back to another view drops the critter
// (and the camera) back where the user left them. Without this,
// the user could walk the critter halfway across the world and
// then find the next view trying to edit it from a stale origin.
type controlVis struct {
	savedFocalPos   Vector3.XYZ
	savedFocalRot   Euler.Radians
	savedLensRot    Euler.Radians
	savedCamPos     Vector3.XYZ
	savedProjection Camera3D.ProjectionType

	savedBodyPos   Vector3.XYZ
	savedBodyBasis Basis.XYZ

	// gaitState is the shared procedural leg-gait driver (legContainer/legRenders,
	// gaitTime/gaitActive, jump state, feetBuf). It's embedded so its fields and
	// methods promote — existing cv.gaitTime / cv.setupLegs() usage keeps working —
	// and the placed-creation animator (CritterAnimator) drives the same code.
	// controlEnter sets cv.body = &ce.body and cv.applyBodyBob = true (the editor
	// layers a body bob via applyBodyGait; a placed creation does not).
	//
	// Note: head-look state lives on CritterEditor.idleHeadLook and is applied by
	// Process() across every view, not just here.
	gaitState

	// walkPos / walkBasis snapshot the body Node3D's transform AFTER
	// each frame's WASD translate/rotate but BEFORE the gait bob/
	// roll/pitch overlay is applied. Next frame we restore from
	// these so the previous frame's animation offset doesn't get
	// folded into the new Translate (Translate operates in the
	// current local frame, so a rolled body would walk sideways).
	walkPos   Vector3.XYZ
	walkBasis Basis.XYZ
}

const (
	// Forward speed in body-local Z units / second. Tuned against
	// the editor's body lift (Y=0.3) so a held W key reads as a
	// brisk walk on the Kenney ground plate without overshooting it
	// in one keypress.
	controlWalkSpeed = float32(2.0)

	// Yaw rate in radians / second. Same scale as the world's QE
	// camera-yaw so users carry the same finger-feel between views.
	controlTurnRate = float32(2.5)

	// Chase-cam offsets, in body-local space. The lens hovers
	// `controlCamHeight` above the critter and sits `controlCamDist`
	// behind it; `controlCamPitch` tilts the lens down so the
	// critter is framed in the lower half of the screen with the
	// horizon visible above.
	//
	// Pitch is negative: in Godot's Lens-then-Camera convention a
	// positive X rotation drops the camera position below the lens
	// origin (Y rotates toward Z), so we need the opposite sign to
	// elevate the camera and aim it slightly downward at the critter.
	controlCamHeight = float32(1.2)
	controlCamDist   = float32(3.5)
	controlCamPitch  = float32(-0.3)

	// controlYawRecenterRate sets how fast the camera yaw drifts back
	// to "behind the critter" once movement begins. Frame-rate
	// independent via t = 1 − exp(−rate·dt) — rate=2 closes ~63% of
	// the gap in 0.5 s, which feels prompt but not snappy. Set to 0
	// to disable the drift (the camera would stay wherever the user
	// last dragged it, full free orbit even while walking).
	controlYawRecenterRate = float32(2.0)
)

// controlEnter swaps into the chase-cam control view: lock the
// world's WASD camera handling, restore perspective projection if
// we came in from ribcage view, position the lens behind the
// critter, and snapshot enough state that controlExit can put
// everything back exactly as it was.
func (ce *CritterEditor) controlEnter() {
	if ce.control != nil {
		return
	}
	cv := &controlVis{}
	if ce.body.mesh != MeshInstance3D.Nil {
		cv.savedBodyPos = ce.body.mesh.AsNode3D().Position()
		cv.savedBodyBasis = ce.body.mesh.AsNode3D().Basis()
		// walkPos / walkBasis start at the same transform: with
		// gaitActive=0 there's nothing to undo on the first tick.
		cv.walkPos = cv.savedBodyPos
		cv.walkBasis = cv.savedBodyBasis
	}
	if ce.rig != nil {
		cv.savedFocalPos = ce.rig.focalNode().Position()
		cv.savedFocalRot = ce.rig.focalNode().Rotation()
		cv.savedLensRot = ce.rig.lensNode().Rotation()
		cv.savedCamPos = ce.rig.viewportCamera().AsNode3D().Position()
		cv.savedProjection = ce.rig.viewportCamera().Projection()
		ce.rig.setMovementLocked(true)
		// Ribcage view may have flipped the camera to orthographic;
		// chase cam wants depth cues, so force perspective on enter.
		// Reasonable defaults for FOV / near / far — the existing
		// restore on exit puts the user's prior settings back.
		ce.rig.viewportCamera().SetPerspective(75, 0.05, 1000)
		ce.rig.lensNode().SetRotation(Euler.Radians{
			X: Angle.Radians(controlCamPitch),
		})
		ce.rig.viewportCamera().AsNode3D().SetPosition(Vector3.New(
			float32(0), controlCamHeight, controlCamDist,
		))
	}
	ce.control = cv
	if ce.rig != nil {
		ce.rig.setDriveMode(true) // touch overlay: joystick + Jump/Run/Exit
	}
	// Initial snap: place the FocalPoint at the critter with the
	// "behind-the-critter" yaw so the first frame already reads as
	// chase cam. After this, controlPhysicsProcess only re-pins the
	// position; the user's middle-mouse drag is free to orbit, and
	// the yaw lerps back toward "behind" only while walking.
	if ce.rig != nil && ce.body.mesh != MeshInstance3D.Nil {
		pos := ce.body.mesh.AsNode3D().GlobalPosition()
		yaw := ce.body.mesh.AsNode3D().Rotation().Y
		ce.rig.focalNode().SetGlobalPosition(pos)
		ce.rig.focalNode().SetRotation(Euler.Radians{
			Y: yaw + Angle.Pi,
		})
	}
	// Spawn the gait-driven leg renders. They sit at rest pose until
	// the first WASD press flips gaitActive above zero, but having
	// them visible from the first frame avoids a one-frame popless
	// where the body's own legs are hidden but ours haven't been
	// uploaded yet. Point the shared gait at the editor's body and enable the
	// body-bob feet-compensation (the editor layers a bob via applyBodyGait).
	cv.body = &ce.body
	cv.applyBodyBob = true
	cv.setupLegs()
}

// controlExit undoes controlEnter — restores camera + body
// transforms and releases the keyboard lock. No-op if we never
// entered control view.
func (ce *CritterEditor) controlExit() {
	if ce.control == nil {
		return
	}
	cv := ce.control
	// Tear down our custom leg renders FIRST so the body's own legs
	// (about to become visible again) aren't briefly stacked under
	// ours. Order matters here: SetVisible(true) flips inside
	// teardownGaitLegs.
	cv.teardownLegs()
	if ce.body.mesh != MeshInstance3D.Nil {
		ce.body.mesh.AsNode3D().SetPosition(cv.savedBodyPos)
		ce.body.mesh.AsNode3D().SetBasis(cv.savedBodyBasis)
		// Snap any leg-anchored parts (duck-foot steppers) from
		// their last animated mid-stride pose back to rest. Without
		// this, the next non-control view would inherit a frozen
		// half-swing foot.
		ce.body.ClearAnimatedLegFeet()
		ce.body.repositionParts()
	}
	if ce.rig != nil {
		ce.rig.focalNode().SetPosition(cv.savedFocalPos)
		ce.rig.focalNode().SetRotation(cv.savedFocalRot)
		ce.rig.lensNode().SetRotation(cv.savedLensRot)
		ce.rig.viewportCamera().AsNode3D().SetPosition(cv.savedCamPos)
		ce.rig.viewportCamera().SetProjection(cv.savedProjection)
		ce.rig.setMovementLocked(false)
		ce.rig.setDriveMode(false)
	}
	ce.control = nil
}

// controlPhysicsProcess reads WASD each fixed-step frame and drives
// the body Node3D: W/S translate along body-local Z (head/forward
// direction in the critter package); A/D yaw the body around world
// Y. The camera is re-snapped at the end so the chase cam tracks
// any rotation or translation that just happened.
func (ce *CritterEditor) controlPhysicsProcess(delta float32) {
	if ce.control == nil || ce.body.mesh == MeshInstance3D.Nil {
		return
	}
	cv := ce.control
	// Undo last frame's gait body-offset before applying this frame's
	// WASD. Translate operates in the body's current local frame, so
	// walking forward while rolled would drift sideways unless we
	// strip the roll first. Same reasoning for the bob in Y.
	bodyNode := ce.body.mesh.AsNode3D()
	bodyNode.SetPosition(cv.walkPos)
	bodyNode.SetBasis(cv.walkBasis)

	// The touch overlay's EXIT (or a VR exit) drops back to the explore
	// view — same as picking it in the view selector, which stays
	// clickable here for mouse users.
	if ce.rig != nil && ce.rig.consumeDriveExit() {
		ce.SwitchToView("explore")
		if ce.workbench != nil {
			ce.workbench.refreshViewSelector(0, ce.Views())
		}
		return
	}
	// Merged drive axes: keyboard, touch joystick, or VR stick.
	drive := ce.rig.driveInput()
	forward := float32(drive.Move.Y)
	turn := float32(-drive.Move.X)
	if turn != 0 {
		bodyNode.Rotate(
			Vector3.New(0, 1, 0),
			Angle.Radians(turn*controlTurnRate*delta),
		)
	}
	if forward != 0 {
		// Body +Z is the head/forward axis (see critter.go). Translate
		// on +Z so W moves the critter in the direction it's facing.
		bodyNode.Translate(Vector3.New(
			0, 0, Float.X(forward*controlWalkSpeed*delta),
		))
	}
	// Snapshot the WASD-only "rest" pose so next frame can undo this
	// frame's animation offset cleanly.
	cv.walkPos = bodyNode.Position()
	cv.walkBasis = bodyNode.Basis()

	moving := forward != 0 || turn != 0
	ce.controlTrackCamera(moving, delta)
	// Spacebar triggers a one-shot procedural jump. We trigger off a
	// hold-check gated by !jumpActive rather than tracking an edge,
	// because the active flag already prevents re-firing — holding
	// the key during a jump just makes the trigger wait until the
	// current jump finishes, which feels right (no key-repeat bunny
	// hops, no buffered second jump).
	if drive.Jump && !cv.jumpActive {
		cv.jumpActive = true
		cv.jumpTime = 0
	}
	if cv.jumpActive {
		cv.jumpTime += delta
		if cv.jumpTime >= jumpDuration {
			cv.jumpActive = false
			cv.jumpTime = 0
		}
	}
	// Drive the procedural gait. State advances even when stopped so
	// gaitActive can ease back to 0 (the leg meshes are blended
	// against rest pose by `gaitActive` inside computeGaitPose).
	// Speed feeds the speed-matched cycle rate; turning in place
	// passes 0 so the legs shuffle at the profile's base rate.
	// Speed scales with the analog deflection (keyboard gives ±1, so
	// desktop behaviour is unchanged) and feeds the speed-matched gait.
	speed := controlWalkSpeed * absF(forward)
	cv.update(moving, speed, delta)
	cv.uploadLegs()
	// Idle head-look ticks every frame, regardless of gaitActive —
	// Idle head-look ticks on CritterEditor.Process now (see
	// idleHeadLook); we don't double-advance it here.
	// Layer body bob/roll/pitch on top of the rest pose. Cheap —
	// just a few sin/cos and a SetPosition/Rotate; the body mesh
	// isn't rebuilt, the Node3D transform is what moves.
	ce.applyBodyGait(cv)
}

// applyBodyGait layers a small bob+roll+pitch oscillation over the
// rest pose snapshot in cv.walkPos / cv.walkBasis. Skipped when
// gaitActive is essentially zero to avoid burning per-frame SetX
// calls while the critter is idle. Amplitudes scale by gaitActive
// so the body eases into and out of the animation in sync with the
// legs.
func (ce *CritterEditor) applyBodyGait(cv *controlVis) {
	if ce.body.mesh == MeshInstance3D.Nil {
		return
	}
	// Shared with the placed CritterAnimator: bobY already folds in the jump Y.
	bobY, rollZ, pitchX := cv.bobOffset()
	// Skip the application entirely when there's nothing to apply (idle, no jump)
	// — saves a handful of SetX calls per idle frame.
	if cv.gaitActive < 0.001 && bobY == 0 {
		return
	}
	node := ce.body.mesh.AsNode3D()
	pos := node.Position()
	pos.Y += Float.X(bobY)
	node.SetPosition(pos)
	// Local-frame rotations: Z is body forward (roll axis), X is
	// body right (pitch axis). Applying Rotate in body-local
	// coords keeps the bob axes attached to the critter rather
	// than to world space — turning the critter doesn't change
	// what "roll" or "pitch" mean relative to the spine.
	node.Rotate(Vector3.New(0, 0, 1), Angle.Radians(rollZ))
	node.Rotate(Vector3.New(1, 0, 0), Angle.Radians(pitchX))
	// Head-look is applied from CritterEditor.Process via the
	// shared idleHeadLook scheduler — it runs in every view, not
	// just control, so there's nothing to do here.
}

// controlTrackCamera keeps the FocalPoint pinned to the critter's
// world position every frame so the camera follows. Yaw is left
// alone — the world's middle-mouse-drag handler is free to orbit
// the focal point around the critter, giving the user a side view
// (or any other angle) just by dragging.
//
// When `moving` is true (the user is holding WASD), yaw lerps back
// toward "behind the critter" with a frame-rate-independent blend.
// This is the third-person-game feel: the camera holds wherever the
// user dragged it while idle, then drifts back behind on movement.
//
// Lens pitch is never touched here — middle-mouse-drag-Y already
// adjusts it, and the user's tilt should survive across moves.
func (ce *CritterEditor) controlTrackCamera(moving bool, delta float32) {
	if ce.control == nil || ce.rig == nil || ce.body.mesh == MeshInstance3D.Nil {
		return
	}
	pos := ce.body.mesh.AsNode3D().GlobalPosition()
	ce.rig.focalNode().SetGlobalPosition(pos)
	if !moving || controlYawRecenterRate <= 0 {
		return
	}
	// While the user holds middle-mouse, they're actively orbiting
	// the camera — fighting them with the recenter pull feels like
	// the camera is dragging back against the cursor. Suspend the
	// drift entirely until they release. Position still pins so the
	// camera follows the critter; only the yaw is left alone.
	if Input.IsMouseButtonPressed(Input.MouseButtonMiddle) {
		return
	}
	bodyYaw := ce.body.mesh.AsNode3D().Rotation().Y
	target := bodyYaw + Angle.Pi
	rot := ce.rig.focalNode().Rotation()
	// Shortest-arc delta into (−π, π]. Without this, lerping across
	// the ±π seam (e.g. cur=−3.0, target=3.0) would walk the long
	// way around and look like a 360° spin.
	diff := target - rot.Y
	for diff > Angle.Pi {
		diff -= 2 * Angle.Pi
	}
	for diff < -Angle.Pi {
		diff += 2 * Angle.Pi
	}
	// Exponential ease toward target — t = 1 − exp(−rate·dt) is the
	// stable, frame-rate-independent form of "lerp 5% of remaining
	// each frame." Closes faster the further the camera is from
	// behind, which matches the "snap when way off, hold when close"
	// feel of typical chase cams.
	t := 1 - Angle.Radians(math.Exp(-float64(controlYawRecenterRate)*float64(delta)))
	rot.Y += diff * t
	ce.rig.focalNode().SetRotation(rot)
}
