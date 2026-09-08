package internal

import (
	"math"

	"graphics.gd/classdb/AnimationPlayer"
	"graphics.gd/classdb/Camera3D"
	"graphics.gd/classdb/Input"
	"graphics.gd/variant/Angle"
	"graphics.gd/variant/Basis"
	"graphics.gd/variant/Euler"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Vector3"
)

// citizenControlVis is the saved state restored on exit from the "control"
// view (the WASD chase-cam walk-test), plus the live walk state. We snapshot
// the camera AND the rig root transform so leaving the view drops the citizen
// (and the camera) back where the user started rather than wherever they
// walked to.
type citizenControlVis struct {
	savedFocalPos   Vector3.XYZ
	savedFocalRot   Euler.Radians
	savedLensRot    Euler.Radians
	savedCamPos     Vector3.XYZ
	savedProjection Camera3D.ProjectionType

	savedBodyPos   Vector3.XYZ
	savedBodyBasis Basis.XYZ

	intent     string  // current clip intent ("" forces a re-pick)
	jumpTime   float32 // seconds into an in-flight jump (0 = grounded)
	escapeDown bool    // Escape edge-detection to leave control mode
}

// Walk-test tunables. Speeds and camera offsets are in editor units — the rig
// is rendered at its true ≈1.7 m size, so these are metre-scale (a brisk human
// walk, a chase cam a few metres back), matching the critter control view.
const (
	citizenWalkSpeed       = float32(1.5)
	citizenRunSpeed        = float32(3.0)
	citizenTurnRate        = float32(2.5)
	citizenCamHeight       = float32(1.2)
	citizenCamDist         = float32(3.5)
	citizenCamPitch        = float32(-0.3)
	citizenYawRecenterRate = float32(2.0)
	// jumpYOffset peaks at jumpHeight (0.85 m); a ~0.85 m hop reads fine on a
	// 1.7 m citizen, so no extra scaling.
	citizenJumpScale = float32(1.0)
	// The rigged human faces +Z (confirmed: the eye verts sit +Z of the body
	// centre), so "forward" walks along +Z and the chase cam sits behind it at
	// -Z (citizenChaseYaw adds π). This drives both the walk direction and the
	// camera-behind offset, so they stay consistent.
	citizenForwardSign = float32(1)
)

// controlEnter swaps into the chase-cam walk-test: lock the editor camera,
// force perspective, frame the lens behind the citizen, snapshot enough to
// restore on exit, and settle the body into its idle clip.
func (ce *CitizenEditor) controlEnter() {
	if ce.control != nil || ce.rigScene == nil {
		return
	}
	cv := &citizenControlVis{}
	root := ce.rigScene.root
	cv.savedBodyPos = root.Position()
	cv.savedBodyBasis = root.Basis()
	if ce.rig != nil {
		cv.savedFocalPos = ce.rig.focalNode().Position()
		cv.savedFocalRot = ce.rig.focalNode().Rotation()
		cv.savedLensRot = ce.rig.lensNode().Rotation()
		cv.savedCamPos = ce.rig.viewportCamera().AsNode3D().Position()
		cv.savedProjection = ce.rig.viewportCamera().Projection()
		ce.rig.setMovementLocked(true)
		ce.rig.setOverlayVisible(false) // clean chase-cam view; Escape restores it
		ce.rig.setDriveMode(true)       // touch overlay: joystick + Jump/Run/Exit
		ce.rig.viewportCamera().SetPerspective(75, 0.05, 2000)
		ce.rig.lensNode().SetRotation(Euler.Radians{X: Angle.Radians(citizenCamPitch)})
		ce.rig.viewportCamera().AsNode3D().SetPosition(Vector3.New(float32(0), citizenCamHeight, citizenCamDist))
		ce.rig.focalNode().SetGlobalPosition(root.GlobalPosition())
		ce.rig.focalNode().SetRotation(Euler.Radians{Y: citizenChaseYaw(root.Rotation().Y)})
	}
	ce.control = cv
	ce.setControlIntent("idle")
}

// controlExit restores the camera + body transform and releases the camera
// lock. No-op if control was never entered.
func (ce *CitizenEditor) controlExit() {
	if ce.control == nil {
		return
	}
	cv := ce.control
	if ce.rigScene != nil {
		root := ce.rigScene.root
		root.SetPosition(cv.savedBodyPos)
		root.SetBasis(cv.savedBodyBasis)
		if ce.rigScene.player != AnimationPlayer.Nil {
			playCritterClip(root, ce.rigScene.player, "idle")
		}
	}
	if ce.rig != nil {
		ce.rig.focalNode().SetPosition(cv.savedFocalPos)
		ce.rig.focalNode().SetRotation(cv.savedFocalRot)
		ce.rig.lensNode().SetRotation(cv.savedLensRot)
		ce.rig.viewportCamera().AsNode3D().SetPosition(cv.savedCamPos)
		ce.rig.viewportCamera().SetProjection(cv.savedProjection)
		ce.rig.setMovementLocked(false)
		ce.rig.setOverlayVisible(true)
		ce.rig.setDriveMode(false)
	}
	ce.control = nil
}

// controlPhysicsProcess reads WASD each fixed step and drives the rig root:
// W/S walk along the facing direction, A/D yaw, Shift runs, Space hops. The
// locomotion clip is reconstructed from the resulting motion exactly like the
// networked EntityAnimator, so what the user sees here is what peers will see.
func (ce *CitizenEditor) controlPhysicsProcess(delta float32) {
	if ce.control == nil || ce.rigScene == nil {
		return
	}
	cv := ce.control
	root := ce.rigScene.root

	// Escape (or the touch overlay's EXIT / a VR exit) leaves the
	// walk-test — the UI is hidden, so the view dropdown can't be
	// clicked. Edge-detected so a held key doesn't re-trigger.
	esc := Input.IsKeyPressed(Input.KeyEscape)
	if (esc && !cv.escapeDown) || ce.rig.consumeDriveExit() {
		cv.escapeDown = esc
		ce.SwitchToView("edit") // controlExit restores the camera/body + UI
		if ce.workbench != nil {
			ce.workbench.refreshViewSelector(0, ce.Views()) // dropdown → "edit"
		}
		return
	}
	cv.escapeDown = esc

	// Merged drive axes: keyboard, touch joystick, or VR stick.
	drive := ce.rig.driveInput()
	forward := float32(drive.Move.Y)
	turn := float32(-drive.Move.X)
	running := drive.Run

	if turn != 0 {
		root.Rotate(Vector3.New(0, 1, 0), Angle.Radians(turn*citizenTurnRate*delta))
	}

	// Arm a one-shot jump on Space / the touch JUMP button (gated by
	// jumpTime so a held press doesn't re-trigger mid-air); advance any
	// in-flight jump.
	if drive.Jump && cv.jumpTime <= 0 {
		cv.jumpTime = 1e-4
	}
	grounded := true
	if cv.jumpTime > 0 {
		cv.jumpTime += delta
		if cv.jumpTime >= jumpDuration {
			cv.jumpTime = 0
		} else {
			grounded = false
		}
	}

	speed := citizenWalkSpeed
	if running {
		speed = citizenRunSpeed
	}
	pos := root.Position()
	if forward != 0 {
		// Walk along the body's facing direction, normalised so the root's
		// editor up-scale doesn't inflate the step. The root only yaws, so the
		// facing is horizontal and X/Z move while Y stays put.
		fwd := Vector3.Normalized(root.Basis().Z)
		pos = Vector3.Add(pos, Vector3.MulX(fwd, Float.X(forward*speed*delta*citizenForwardSign)))
	}
	// Jump arc on top of the flat baseline Y (the walk never changes Y itself).
	var jumpY float32
	if cv.jumpTime > 0 {
		jumpY = jumpYOffset(cv.jumpTime/jumpDuration) * citizenJumpScale
	}
	pos.Y = cv.savedBodyPos.Y + Float.X(jumpY)
	root.SetPosition(pos)

	// Reconstruct the locomotion clip from the motion.
	want := "idle"
	switch {
	case !grounded:
		want = "jump"
	case forward != 0:
		if running {
			want = "run"
		} else {
			want = "walk"
		}
	}
	ce.setControlIntent(want)

	ce.controlTrackCamera(forward != 0 || turn != 0, delta)
}

// setControlIntent switches the clip when the intent changes (gating avoids
// re-asserting loop/speed every frame); the 0.25s default blend in
// playCritterClip cross-fades the transition.
func (ce *CitizenEditor) setControlIntent(intent string) {
	if ce.control == nil || ce.rigScene == nil {
		return
	}
	if ce.control.intent == intent {
		return
	}
	ce.control.intent = intent
	playCritterClip(ce.rigScene.root, ce.rigScene.player, intent)
}

// citizenChaseYaw maps the body yaw to the focal yaw that frames the camera
// behind the citizen, given which way it faces (citizenForwardSign).
func citizenChaseYaw(bodyYaw Angle.Radians) Angle.Radians {
	if citizenForwardSign > 0 {
		return bodyYaw + Angle.Pi
	}
	return bodyYaw
}

// controlTrackCamera pins the focal point to the citizen each frame so the
// camera follows, and (while walking and not actively orbiting) eases the yaw
// back to "behind the citizen" — the standard third-person feel. Mirrors the
// critter chase cam.
func (ce *CitizenEditor) controlTrackCamera(moving bool, delta float32) {
	if ce.control == nil || ce.rig == nil || ce.rigScene == nil {
		return
	}
	root := ce.rigScene.root
	ce.rig.focalNode().SetGlobalPosition(root.GlobalPosition())
	if !moving || citizenYawRecenterRate <= 0 {
		return
	}
	if Input.IsMouseButtonPressed(Input.MouseButtonMiddle) {
		return // user is orbiting; don't fight them
	}
	target := citizenChaseYaw(root.Rotation().Y)
	rot := ce.rig.focalNode().Rotation()
	diff := target - rot.Y
	for diff > Angle.Pi {
		diff -= 2 * Angle.Pi
	}
	for diff < -Angle.Pi {
		diff += 2 * Angle.Pi
	}
	t := 1 - Angle.Radians(math.Exp(-float64(citizenYawRecenterRate)*float64(delta)))
	rot.Y += diff * t
	ce.rig.focalNode().SetRotation(rot)
}
