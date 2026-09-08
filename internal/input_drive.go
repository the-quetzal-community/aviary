package internal

import (
	"graphics.gd/classdb/Input"
	"graphics.gd/classdb/XRController3D"
	"graphics.gd/variant/Vector2"
)

// input_drive.go is the platform-parity input layer for "drive" modes —
// possession, self-flight, first-person ground walking, and the editor
// walk-test views. Each mode used to read the keyboard directly
// (IsKeyPressed W/A/S/D/Space/Shift), which made every one of them
// desktop-only. driveInput() merges the same intent from all three
// input surfaces:
//
//	desktop — WASD/arrows, Space, Shift (unchanged behaviour);
//	touch   — the on-screen TouchControls overlay (virtual joystick +
//	          Jump/Run buttons, see ui_touch_controls.go);
//	VR      — the left controller thumbstick, with A/X (or the off-UI
//	          trigger while possessing) as jump.
//
// Modes consume DriveInput instead of the keyboard, so adding an input
// surface here upgrades every drive mode at once.

// DriveInput is one frame's merged movement intent. Move is a unit-ish
// vector: X is the A/D axis (+1 = D/right — strafe or turn depending on
// the mode), Y is the W/S axis (+1 = W/forward). Length is clamped to 1
// so stacked sources can't exceed keyboard speed. Jump and Run are
// held-states; callers keep their existing edge detection.
type DriveInput struct {
	Move Vector2.XY
	Jump bool
	Run  bool
}

// vrDriveStickDeadzone filters thumbstick drift; matches the locomotion
// deadzone in processVRLocomotion.
const vrDriveStickDeadzone = float32(0.2)

// driveInput samples the merged movement intent for this frame. Pure
// sampling — no latches are consumed (see consumeDriveExit for the exit
// affordance), so a mode may call it more than once per frame safely.
func (world *Client) driveInput() DriveInput {
	var d DriveInput
	if Input.IsKeyPressed(Input.KeyW) || Input.IsKeyPressed(Input.KeyUp) {
		d.Move.Y += 1
	}
	if Input.IsKeyPressed(Input.KeyS) || Input.IsKeyPressed(Input.KeyDown) {
		d.Move.Y -= 1
	}
	if Input.IsKeyPressed(Input.KeyD) || Input.IsKeyPressed(Input.KeyRight) {
		d.Move.X += 1
	}
	if Input.IsKeyPressed(Input.KeyA) || Input.IsKeyPressed(Input.KeyLeft) {
		d.Move.X -= 1
	}
	if Input.IsKeyPressed(Input.KeySpace) {
		d.Jump = true
	}
	if Input.IsKeyPressed(Input.KeyShift) {
		d.Run = true
	}
	if world.touchControls != nil {
		move := world.touchControls.MoveVector()
		d.Move.X += move.X
		d.Move.Y += move.Y
		d.Jump = d.Jump || world.touchControls.JumpHeld()
		d.Run = d.Run || world.touchControls.RunActive()
	}
	if world.xr && world.xrLeft != XRController3D.Nil {
		stick := world.xrLeft.GetVector2("primary")
		if Vector2.Length(stick) > vrDriveStickDeadzone {
			d.Move.X += stick.X
			d.Move.Y += stick.Y
		}
		d.Jump = d.Jump || world.vrJumpHeld
	}
	if l := Vector2.Length(d.Move); l > 1 {
		d.Move = Vector2.DivX(d.Move, l)
	}
	return d
}

// consumeDriveExit reports (and clears) a pending exit request from a
// non-keyboard affordance: the touch overlay's Exit button or the VR
// B/Y button. Keyboard Escape/Enter stay handled by each mode directly,
// so nothing double-fires. Exactly one mode is active at a time — that
// mode is the single consumer per frame.
func (world *Client) consumeDriveExit() bool {
	fired := false
	if world.touchControls != nil && world.touchControls.ConsumeExit() {
		fired = true
	}
	if world.vrExitRequest {
		world.vrExitRequest = false
		fired = true
	}
	return fired
}

// touchPointerClaimed reports whether the touch overlay currently owns
// finger 0 — the finger Godot mirrors as the emulated mouse. Gesture
// and look code polls this to ignore that synthetic mouse while the
// finger is on the joystick or a button.
func (world *Client) touchPointerClaimed() bool {
	return world.touchControls != nil && world.touchControls.PointerClaimed()
}

// driveModeActive reports whether any drive mode currently owns
// movement input — used to show/hide the touch overlay.
func (world *Client) driveModeActive() bool {
	return world.possess.active || world.flight.active || world.fpsMode || world.editorDrive
}

// setDriveMode is the CameraRig port hook for editor-owned drive views
// (the critter/citizen walk-tests): flags the mode active so the touch
// overlay appears alongside it.
func (world *Client) setDriveMode(active bool) {
	world.editorDrive = active
}
