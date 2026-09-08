package internal

import (
	"os"

	"graphics.gd/classdb/CanvasLayer"
	"graphics.gd/classdb/Control"
	"graphics.gd/classdb/DisplayServer"
	"graphics.gd/classdb/GUI"
	"graphics.gd/classdb/InputEvent"
	"graphics.gd/classdb/InputEventScreenDrag"
	"graphics.gd/classdb/InputEventScreenTouch"
	"graphics.gd/classdb/Label"
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/Panel"
	"graphics.gd/classdb/StyleBoxFlat"
	"graphics.gd/classdb/Viewport"
	"graphics.gd/variant/Color"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Vector2"
)

// TouchControls is the on-screen drive overlay for touchscreen devices:
// a floating virtual joystick on the left (movement), JUMP + RUN
// buttons bottom-right, and an EXIT button top-right. It appears only
// while a drive mode is active (possession, self-flight, first-person
// walking, the editor walk-tests) and feeds Client.driveInput() /
// consumeDriveExit() — the same merged intent the keyboard and VR
// sticks feed, so every drive mode gains touch support at once.
//
// Input is handled from RAW InputEventScreenTouch/ScreenDrag (not
// Control gui_input) because Godot's mouse-from-touch emulation only
// tracks the FIRST finger — a joystick thumb plus a jump press is
// two-finger territory. Claimed fingers are consumed
// (SetInputAsHandled) so they never reach the Client's camera-orbit
// ScreenDrag handler; unclaimed fingers pass through and keep steering
// the look (see Client.UnhandledInput). The visuals are all
// MouseFilterIgnore so the emulated mouse can't get stuck on them.
type TouchControls struct {
	Node.Extension[TouchControls] `gd:"AviaryTouchControls"`

	canvas  CanvasLayer.Instance
	joyBase Panel.Instance
	joyKnob Panel.Instance
	jumpBtn Panel.Instance
	runBtn  Panel.Instance
	exitBtn Panel.Instance

	active bool

	// Finger claims: -1 = unclaimed. move tracks the joystick finger's
	// deflection as a -1..1 vector (+Y = forward / screen-up).
	moveFinger int
	moveOrigin Vector2.XY
	move       Vector2.XY
	jumpFinger int
	exitFinger int
	runActive  bool // toggle, flipped on RUN tap
	exitFired  bool // latched until ConsumeExit

	screen Vector2.XY // last laid-out viewport size
}

// touchControlsWanted reports whether this device should get the
// overlay at all: a real touchscreen, or AVIARY_TOUCH=1 for desktop
// debugging (pair with Godot's emulate_touch_from_mouse to exercise
// it with a mouse).
func touchControlsWanted() bool {
	return DisplayServer.IsTouchscreenAvailable() || os.Getenv("AVIARY_TOUCH") == "1"
}

// attachTouchControls creates the overlay under the client node.
// Called once at startup on touch-capable devices; the overlay stays
// hidden until SetActive(true).
func attachTouchControls(parent Node.Instance) *TouchControls {
	t := new(TouchControls)
	t.moveFinger, t.jumpFinger, t.exitFinger = -1, -1, -1
	t.AsNode().SetName("TouchControls")
	parent.AddChild(t.AsNode())
	return t
}

func (t *TouchControls) Ready() {
	// A high canvas layer so the overlay draws above the editor UI —
	// drive modes hide that UI anyway, but the walk-tests keep parts
	// of it visible.
	t.canvas = CanvasLayer.New()
	t.canvas.SetLayer(64)
	t.AsNode().AddChild(t.canvas.AsNode())

	dim := Color.RGBA{R: 1, G: 1, B: 1, A: 0.18}
	bold := Color.RGBA{R: 1, G: 1, B: 1, A: 0.38}
	warn := Color.RGBA{R: 1, G: 0.45, B: 0.35, A: 0.42}
	t.joyBase = t.circle(dim, "")
	t.joyKnob = t.circle(bold, "")
	t.jumpBtn = t.circle(bold, "JUMP")
	t.runBtn = t.circle(dim, "RUN")
	t.exitBtn = t.circle(warn, "EXIT")
	t.canvas.SetVisible(false)
}

// circle builds one round translucent panel (a StyleBoxFlat with a
// huge corner radius) with an optional centred caption. All visuals
// ignore the mouse — hit-testing happens in UnhandledInput.
func (t *TouchControls) circle(col Color.RGBA, caption string) Panel.Instance {
	p := Panel.New()
	sb := StyleBoxFlat.New()
	sb.SetBgColor(col)
	sb.SetCornerRadiusAll(4096)
	p.AsControl().AddThemeStyleboxOverride("panel", sb.AsStyleBox())
	p.AsControl().SetMouseFilter(Control.MouseFilterIgnore)
	if caption != "" {
		lbl := Label.New()
		lbl.SetText(caption)
		lbl.AsControl().SetMouseFilter(Control.MouseFilterIgnore)
		lbl.AsControl().SetAnchorsPreset(Control.PresetFullRect)
		lbl.SetHorizontalAlignment(GUI.HorizontalAlignmentCenter)
		lbl.SetVerticalAlignment(GUI.VerticalAlignmentCenter)
		p.AsNode().AddChild(lbl.AsNode())
	}
	t.canvas.AsNode().AddChild(p.AsNode())
	return p
}

// SetActive shows/hides the overlay. Deactivating releases every claim
// so a mode change mid-touch can't wedge a stale joystick vector.
func (t *TouchControls) SetActive(active bool) {
	if t.active == active {
		return
	}
	t.active = active
	if t.canvas != CanvasLayer.Nil {
		t.canvas.SetVisible(active)
	}
	t.releaseAll()
	if active {
		t.layout()
	}
}

func (t *TouchControls) releaseAll() {
	t.moveFinger, t.jumpFinger, t.exitFinger = -1, -1, -1
	t.move = Vector2.XY{}
	// runActive (a toggle) and exitFired (a latch) survive until read.
}

// MoveVector is the joystick deflection: X right, Y forward, length ≤ 1.
func (t *TouchControls) MoveVector() Vector2.XY { return t.move }

// JumpHeld reports whether a finger is holding the JUMP button.
func (t *TouchControls) JumpHeld() bool { return t.jumpFinger >= 0 }

// RunActive reports the RUN toggle.
func (t *TouchControls) RunActive() bool { return t.active && t.runActive }

// ConsumeExit reports (and clears) a tap on the EXIT button.
func (t *TouchControls) ConsumeExit() bool {
	fired := t.exitFired
	t.exitFired = false
	return fired
}

// PointerClaimed reports whether finger 0 — the one Godot mirrors as
// the emulated mouse — is currently on one of the overlay's controls.
// The Client uses it to keep that finger's synthetic mouse events from
// steering the look or firing possession gestures.
func (t *TouchControls) PointerClaimed() bool {
	return t.moveFinger == 0 || t.jumpFinger == 0 || t.exitFinger == 0
}

// Geometry, all relative to the shorter viewport axis so the layout
// reads the same on a phone and a tablet.
const (
	touchJoyRadius  = 0.13 // joystick base radius
	touchKnobRadius = 0.055
	touchJumpRadius = 0.085
	touchRunRadius  = 0.06
	touchExitRadius = 0.05
	// touchSlop widens each button's hit circle so imprecise taps land.
	touchSlop = 1.35
)

func (t *TouchControls) unit() Float.X {
	if t.screen.X < t.screen.Y {
		return t.screen.X
	}
	return t.screen.Y
}

func (t *TouchControls) joyHome() Vector2.XY {
	return Vector2.XY{X: t.screen.X * 0.14, Y: t.screen.Y * 0.74}
}
func (t *TouchControls) jumpCenter() Vector2.XY {
	return Vector2.XY{X: t.screen.X * 0.88, Y: t.screen.Y * 0.78}
}
func (t *TouchControls) runCenter() Vector2.XY {
	return Vector2.XY{X: t.screen.X * 0.74, Y: t.screen.Y * 0.87}
}
func (t *TouchControls) exitCenter() Vector2.XY {
	return Vector2.XY{X: t.screen.X * 0.94, Y: t.screen.Y * 0.08}
}

// layout sizes/positions every control against the current viewport.
// Cheap; re-run whenever the viewport size changes (Process) and on
// activation.
func (t *TouchControls) layout() {
	t.screen = Viewport.Get(t.AsNode()).GetVisibleRect().Size
	if t.screen.X <= 0 || t.screen.Y <= 0 {
		return
	}
	u := t.unit()
	place := func(p Panel.Instance, center Vector2.XY, r Float.X) {
		p.AsControl().SetSize(Vector2.XY{X: 2 * r, Y: 2 * r})
		p.AsControl().SetPosition(Vector2.XY{X: center.X - r, Y: center.Y - r})
	}
	base := t.joyHome()
	if t.moveFinger >= 0 {
		base = t.moveOrigin
	}
	place(t.joyBase, base, u*touchJoyRadius)
	knob := Vector2.Add(base, Vector2.MulX(
		Vector2.XY{X: t.move.X, Y: -t.move.Y}, u*touchJoyRadius))
	place(t.joyKnob, knob, u*touchKnobRadius)
	place(t.jumpBtn, t.jumpCenter(), u*touchJumpRadius)
	place(t.runBtn, t.runCenter(), u*touchRunRadius)
	place(t.exitBtn, t.exitCenter(), u*touchExitRadius)
	// RUN doubles as its own state light: brighten while toggled on.
	if sb, ok := Object.As[StyleBoxFlat.Instance](t.runBtn.AsControl().GetThemeStylebox("panel")); ok {
		a := Float.X(0.18)
		if t.runActive {
			a = 0.45
		}
		c := sb.BgColor()
		c.A = float32(a)
		sb.SetBgColor(c)
	}
}

func (t *TouchControls) Process(delta Float.X) {
	if !t.active {
		return
	}
	t.layout()
}

func within(p, center Vector2.XY, r Float.X) bool {
	return Vector2.Length(Vector2.Sub(p, center)) <= r
}

// UnhandledInput claims touches that land on the overlay's controls and
// consumes them so they don't leak into the world (camera orbit,
// selection). TouchControls is a child of the Client node, so it sees
// unhandled input before the Client does.
func (t *TouchControls) UnhandledInput(event InputEvent.Instance) {
	if !t.active {
		return
	}
	u := t.unit()
	if touch, ok := Object.As[InputEventScreenTouch.Instance](event); ok {
		idx := touch.Index()
		if touch.AsInputEvent().IsPressed() {
			pos := touch.Position()
			switch {
			case within(pos, t.exitCenter(), u*touchExitRadius*touchSlop):
				t.exitFinger = idx
			case within(pos, t.jumpCenter(), u*touchJumpRadius*touchSlop) && t.jumpFinger < 0:
				t.jumpFinger = idx
			case within(pos, t.runCenter(), u*touchRunRadius*touchSlop):
				t.runActive = !t.runActive
			case pos.X < t.screen.X*0.45 && pos.Y > t.screen.Y*0.30 && t.moveFinger < 0:
				// Floating joystick: the base re-centres where the
				// thumb lands anywhere in the lower-left region.
				t.moveFinger = idx
				t.moveOrigin = pos
				t.move = Vector2.XY{}
			default:
				return // unclaimed — leave it for the look/orbit path
			}
			Viewport.Get(t.AsNode()).SetInputAsHandled()
			return
		}
		// Release: clear whichever claim this finger held. The EXIT
		// latch fires on release ON the button, tap-style, so a slid-off
		// finger can bail out.
		claimed := false
		if idx == t.moveFinger {
			t.moveFinger = -1
			t.move = Vector2.XY{}
			claimed = true
		}
		if idx == t.jumpFinger {
			t.jumpFinger = -1
			claimed = true
		}
		if idx == t.exitFinger {
			t.exitFinger = -1
			if within(touch.Position(), t.exitCenter(), u*touchExitRadius*touchSlop*1.2) {
				t.exitFired = true
			}
			claimed = true
		}
		if claimed {
			Viewport.Get(t.AsNode()).SetInputAsHandled()
		}
		return
	}
	if drag, ok := Object.As[InputEventScreenDrag.Instance](event); ok {
		idx := drag.Index()
		if idx == t.moveFinger {
			// Deflection from the touch-down origin, clamped to the
			// base radius. Screen Y grows downward; forward is up.
			delta := Vector2.Sub(drag.Position(), t.moveOrigin)
			r := u * touchJoyRadius
			v := Vector2.XY{X: delta.X / r, Y: -delta.Y / r}
			if l := Vector2.Length(v); l > 1 {
				v = Vector2.DivX(v, l)
			}
			t.move = v
			Viewport.Get(t.AsNode()).SetInputAsHandled()
			return
		}
		if idx == t.jumpFinger || idx == t.exitFinger {
			Viewport.Get(t.AsNode()).SetInputAsHandled()
		}
	}
}
