package internal

import (
	"graphics.gd/classdb/Input"
	"graphics.gd/classdb/InputEventKey"
	"graphics.gd/classdb/InputEventMouseButton"
	"graphics.gd/classdb/Viewport"
	"graphics.gd/variant/Angle"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Vector2"
)

// input_pad.go is the gamepad half of the platform-parity input layer
// (see input_drive.go for the movement side). It covers an Xbox/
// PlayStation pad on the desktop and the Steam Deck's built-in
// controls, which is what "usable on Deck/controller" means for every
// editor: the pad has to do the two things a mouse does — point at
// something and click it — as well as move the camera.
//
// Rather than teach each editor a second input vocabulary, the pad
// drives the ones they already speak:
//
//	the right stick moves the real mouse cursor (Input.WarpMouse), so
//	every hover, preview, snap and drag keeps working untouched;
//	the face buttons are parsed back in as mouse/key events
//	(Input.ParseInputEvent), so a click is a click — in the 3D scene
//	and on the design explorer's tiles alike.
//
// The mapping, in editor context:
//
//	left stick      pan the camera (through driveInput)
//	right stick     move the cursor
//	L2 / R2         zoom out / in
//	d-pad           turn (left/right) and tilt (up/down) the camera
//	A               left click        B  right click
//	X               Delete (the coaster's pop-last-piece, deselect)
//	Y               open/close the design drawer (pick the next piece)
//	L1 / R1         undo / redo       Back  next editor   Start  Escape
//
// and in a drive mode (possession, self-flight, first-person ground,
// the editor walk-tests), where there is nothing to point at:
//
//	left stick      move             right stick  look
//	A               jump             left stick click  run
//	B               leave the mode (the touch overlay's Exit button)
//
// The coaster editor is the case that drove this: with a track open it
// is already pointer-free — every piece previews at the track's own
// cursor — so building a track on a pad is A to lay the next piece, X
// to pop it back off, B to close the track, Y to pick a different
// piece. Only the station that starts a track needs the pointer.
const (
	// padStickDeadzone filters thumbstick drift. Matches
	// vrDriveStickDeadzone so a stick feels the same in either hand.
	padStickDeadzone = Float.X(0.2)
	// padTriggerPress is how far an analogue trigger must be pulled to
	// count as held.
	padTriggerPress = Float.X(0.5)
	// padCursorSpeed is the virtual cursor's speed in pixels per second
	// at full stick deflection. The response is squared below, so small
	// deflections stay slow enough to pick a single grid cell.
	padCursorSpeed = Float.X(1400)
	// padLookSpeed / padTurnSpeed are radians per second at full
	// deflection for the right stick (drive-mode look) and the d-pad
	// (editor camera). The d-pad is a digital press, so it turns at the
	// same rate as holding Q/E/R/F (1 radian per second).
	padLookSpeed = Float.X(2.5)
	padTurnSpeed = Float.X(1)
	// padPollSeconds is how often the connected-joypad list is
	// re-checked, so plugging a pad in mid-session is picked up without
	// polling the list every frame.
	padPollSeconds = Float.X(2)
)

// padState is the gamepad's per-frame bookkeeping: which device we
// read, and the previous frame's button states so presses can be
// edge-detected (Godot's Input singleton only reports held-ness for
// joypad buttons).
type padState struct {
	device  Input.Device
	present bool
	// poll counts down to the next GetConnectedJoypads call.
	poll Float.X
	// held is last frame's state for the buttons we act on.
	held map[Input.JoyButton]bool
	// exitRequest is set by B in a drive mode and consumed by
	// consumeDriveExit, exactly like the touch overlay's Exit button.
	exitRequest bool
	// zoomIn / zoomOut are this frame's trigger states, read by the
	// camera block in Process.
	zoomIn  bool
	zoomOut bool
	// mouseLeft / mouseRight track the mouse buttons the pad is
	// currently holding down, so a held A that is interrupted (a drive
	// mode starts, the pad is unplugged) still gets its release: a
	// mouse button left logically down would keep a terrain brush
	// stroking.
	mouseLeft  bool
	mouseRight bool
	// keys are the keyboard keys the pad is holding down, for the same
	// reason: some editors poll Input.IsKeyPressed rather than reading
	// the event, so a press and release in one frame would be missed.
	keys map[Input.Key]bool
}

// padDevice reports the joypad the pad layer reads, if one is
// connected. Polled rather than signal-driven so a pad connected (or
// unplugged) mid-session is picked up without wiring a signal through
// the client.
func (world *Client) padDevice() (Input.Device, bool) {
	return world.pad.device, world.pad.present
}

// padPressed reports whether a pad button went down this frame, and
// records the new state. Called once per button per frame from
// processPad — the stored state is the edge detector.
func (world *Client) padPressed(button Input.JoyButton) bool {
	down := Input.IsJoyButtonPressed(world.pad.device, button)
	if world.pad.held == nil {
		world.pad.held = make(map[Input.JoyButton]bool)
	}
	was := world.pad.held[button]
	world.pad.held[button] = down
	return down && !was
}

// padReleased reports whether a pad button came up this frame. Pairs
// with padPressed for the buttons that synthesize a held mouse button
// (A drags a terrain brush the way a held left button does), so it
// must be called before padPressed updates the stored state.
func (world *Client) padReleased(button Input.JoyButton) bool {
	down := Input.IsJoyButtonPressed(world.pad.device, button)
	return !down && world.pad.held[button]
}

// padStick samples one thumbstick with the deadzone removed and the
// remainder rescaled to 0..1, so movement starts from zero just
// outside the deadzone instead of jumping to a fifth of full speed.
// Y is Godot's screen-style axis: +1 is down/towards the player.
func padStick(device Input.Device, axisX, axisY Input.JoyAxis) Vector2.XY {
	return padStickShape(Vector2.XY{
		X: Input.GetJoyAxis(device, axisX),
		Y: Input.GetJoyAxis(device, axisY),
	})
}

// padStickShape is padStick's arithmetic on an already-sampled stick,
// kept separate so it can be tested without a device: radial deadzone
// (so a diagonal isn't easier to trip than an axis), the remainder
// rescaled to 0..1, and the length clamped — a stick that reads past
// full deflection at the corners can't outrun the keyboard.
func padStickShape(raw Vector2.XY) Vector2.XY {
	length := Vector2.Length(raw)
	if length < padStickDeadzone {
		return Vector2.XY{}
	}
	scale := (length - padStickDeadzone) / (1 - padStickDeadzone)
	if scale > 1 {
		scale = 1
	}
	return Vector2.MulX(Vector2.DivX(raw, length), scale)
}

// padTrigger reports whether an analogue trigger is pulled past
// padTriggerPress. Godot rests triggers at 0 and pulls to 1.
func padTrigger(device Input.Device, axis Input.JoyAxis) bool {
	return Input.GetJoyAxis(device, axis) > padTriggerPress
}

// padZoomIn / padZoomOut are the trigger states sampled by processPad,
// consumed by the camera block in Process alongside the +/− keys.
func (world *Client) padZoomIn() bool  { return world.pad.zoomIn }
func (world *Client) padZoomOut() bool { return world.pad.zoomOut }

// processPad runs once per frame, before the camera block in Process.
// It refreshes the connected device, moves the virtual cursor, steers
// the camera and turns button edges into the mouse and key events the
// editors already handle.
func (world *Client) processPad(dt Float.X) {
	// In XR the controllers are the input surface (see xr_ui.go) and
	// there is no desktop cursor to warp.
	if world.xr {
		return
	}
	world.pad.poll -= dt
	if world.pad.poll <= 0 {
		world.pad.poll = padPollSeconds
		pads := Input.GetConnectedJoypads()
		if len(pads) == 0 {
			if world.pad.present {
				world.padReleaseMouse()
				world.padReleaseKeys()
			}
			world.pad.present = false
			clear(world.pad.held)
		} else {
			world.pad.device = Input.Device(pads[0])
			world.pad.present = true
		}
	}
	if !world.pad.present {
		world.pad.zoomIn, world.pad.zoomOut = false, false
		return
	}
	device := world.pad.device

	world.pad.zoomIn = padTrigger(device, Input.JoyAxisTriggerRight)
	world.pad.zoomOut = padTrigger(device, Input.JoyAxisTriggerLeft)

	drive := world.driveModeActive()
	look := padStick(device, Input.JoyAxisRightX, Input.JoyAxisRightY)
	if drive {
		// Nothing to point at while driving: the right stick looks,
		// with the same clamped pitch as the captured mouse.
		if look.X != 0 {
			world.turnYaw(-Angle.Radians(look.X * padLookSpeed * dt))
		}
		if look.Y != 0 {
			world.lookPitch(-Angle.Radians(look.Y*padLookSpeed*dt), true)
		}
	} else if look.X != 0 || look.Y != 0 {
		world.padMoveCursor(Vector2.MulX(look, padCursorSpeed*dt))
	}

	// The d-pad turns and tilts the editor camera (Q/E and R/F). The
	// critter/citizen control views own the camera themselves while
	// controlLockMovement is set, so leave it alone there.
	if !drive && !world.controlLockMovement {
		if Input.IsJoyButtonPressed(device, Input.JoyButtonDpadLeft) {
			world.turnYaw(-Angle.Radians(padTurnSpeed * dt))
		}
		if Input.IsJoyButtonPressed(device, Input.JoyButtonDpadRight) {
			world.turnYaw(Angle.Radians(padTurnSpeed * dt))
		}
		grounded := world.cameraGrounded()
		if Input.IsJoyButtonPressed(device, Input.JoyButtonDpadUp) {
			world.lookPitch(-Angle.Radians(padTurnSpeed*dt), grounded)
		}
		if Input.IsJoyButtonPressed(device, Input.JoyButtonDpadDown) {
			world.lookPitch(Angle.Radians(padTurnSpeed*dt), grounded)
		}
	}

	// A is jump while driving (merged in driveInput) and the left mouse
	// button otherwise — pressed and released rather than clicked, so a
	// held A drags a terrain brush or a fence run like a held button.
	// Edges are read every frame either way, so leaving a drive mode
	// with a button held doesn't fire a click on the next frame.
	upA, upB := world.padReleased(Input.JoyButtonA), world.padReleased(Input.JoyButtonB)
	upX, upStart := world.padReleased(Input.JoyButtonX), world.padReleased(Input.JoyButtonStart)
	downA, downB := world.padPressed(Input.JoyButtonA), world.padPressed(Input.JoyButtonB)
	downX, downStart := world.padPressed(Input.JoyButtonX), world.padPressed(Input.JoyButtonStart)
	switch {
	case drive:
		// Nothing to click; drop any button the pad was holding and
		// let B ask the mode to end.
		world.padReleaseMouse()
		if downB {
			world.pad.exitRequest = true
		}
	default:
		if upA {
			world.padMouse(Input.MouseButtonLeft, false)
		}
		if downA {
			world.padMouse(Input.MouseButtonLeft, true)
		}
		if upB {
			world.padMouse(Input.MouseButtonRight, false)
		}
		if downB {
			world.padMouse(Input.MouseButtonRight, true)
		}
	}
	// Delete and Escape are held for as long as their button is, in
	// either mode: an editor that polls the key (the critter/citizen
	// control views read Escape that way) would never see a press and
	// release delivered in the same frame.
	if upX {
		world.padKey(Input.KeyDelete, false)
	}
	if downX {
		world.padKey(Input.KeyDelete, true)
	}
	if upStart {
		world.padKey(Input.KeyEscape, false)
	}
	if downStart {
		world.padKey(Input.KeyEscape, true)
	}
	if world.padPressed(Input.JoyButtonY) {
		world.padToggleDrawer()
	}
	if world.padPressed(Input.JoyButtonLeftShoulder) && world.ui != nil {
		world.ui.undo()
	}
	if world.padPressed(Input.JoyButtonRightShoulder) && world.ui != nil {
		world.ui.redo()
	}
	if world.padPressed(Input.JoyButtonBack) && world.ui != nil {
		world.ui.cycleMode()
	}
}

// padMoveCursor nudges the real mouse cursor by a pixel delta, clamped
// to the window. Warping rather than tracking a private cursor keeps
// the pad and the mouse the same pointer: every editor's hover,
// preview and snap code reads the mouse position as it always has.
func (world *Client) padMoveCursor(delta Vector2.XY) {
	size := uiDisplaySize(world)
	at := Viewport.Get(world.AsNode()).GetMousePosition()
	at.X = min(max(at.X+delta.X, 0), Float.X(size.X))
	at.Y = min(max(at.Y+delta.Y, 0), Float.X(size.Y))
	Input.WarpMouse(at)
}

// padMouse parses a mouse button press or release in at the cursor.
// Input.ParseInputEvent feeds the event through the same path as a real
// click — the Control tree first (so design-explorer tiles respond),
// then UnhandledInput on the editors — and updates the Input
// singleton's button mask, so held-button checks see it too.
func (world *Client) padMouse(button Input.MouseButton, pressed bool) {
	switch button {
	case Input.MouseButtonLeft:
		if world.pad.mouseLeft == pressed {
			return
		}
		world.pad.mouseLeft = pressed
	case Input.MouseButtonRight:
		if world.pad.mouseRight == pressed {
			return
		}
		world.pad.mouseRight = pressed
	}
	event := InputEventMouseButton.New()
	event.SetButtonIndex(button)
	event.SetPressed(pressed)
	event.AsInputEventMouse().SetPosition(Viewport.Get(world.AsNode()).GetMousePosition())
	Input.ParseInputEvent(event.AsInputEvent())
}

// padReleaseMouse lets go of whatever mouse buttons the pad is holding.
// Called when a drive mode takes over (and when the pad disappears), so
// a stroke that started with A can't be left half-open.
func (world *Client) padReleaseMouse() {
	if world.pad.mouseLeft {
		world.padMouse(Input.MouseButtonLeft, false)
	}
	if world.pad.mouseRight {
		world.padMouse(Input.MouseButtonRight, false)
	}
}

// padKey parses a key press or release in, for the editor commands that
// are keyboard-only: Delete pops the coaster's last piece (and removes
// a selected object elsewhere), Escape leaves a menu or a control view.
// Held-state is tracked so a key can't be pressed or released twice.
func (world *Client) padKey(key Input.Key, pressed bool) {
	if world.pad.keys == nil {
		world.pad.keys = make(map[Input.Key]bool)
	}
	if world.pad.keys[key] == pressed {
		return
	}
	world.pad.keys[key] = pressed
	event := InputEventKey.New()
	event.SetKeycode(key)
	event.SetPressed(pressed)
	Input.ParseInputEvent(event.AsInputEvent())
}

// padReleaseKeys lets go of whatever keys the pad is holding, for the
// same reason as padReleaseMouse: an unplugged pad must not leave a key
// logically down.
func (world *Client) padReleaseKeys() {
	for key, held := range world.pad.keys {
		if held {
			world.padKey(key, false)
		}
	}
}

// padToggleDrawer opens or closes the design drawer. On a pad the
// drawer is the only way to reach the palette: it opens on pointer
// hover over the expansion indicator, which a stick-driven cursor can
// do but only awkwardly, and closing it is otherwise a mouse move up
// the screen.
func (world *Client) padToggleDrawer() {
	if world.ui == nil || world.ui.Editor == nil {
		return
	}
	if world.ui.Editor.drawExpanded.Load() {
		world.ui.Editor.closeDrawer()
	} else {
		world.ui.Editor.openDrawer()
	}
}
