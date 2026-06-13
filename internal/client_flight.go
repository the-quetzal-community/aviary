package internal

import (
	"graphics.gd/classdb/AnimationPlayer"
	"graphics.gd/classdb/Input"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/PackedScene"
	"graphics.gd/variant/Angle"
	"graphics.gd/variant/Euler"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Vector3"
)

// flightState drives the third-person self-flight mode: pressing Enter in an
// fpsEditor with NOTHING selected hands control to the player's own avatar — a
// flappy-bird glider. Space flaps (each press is one wing-beat: it adds airspeed
// + lift and restarts the flap clip), gravity bleeds your airspeed as you climb,
// and you glide in the direction you look. Pull up and you trade speed for height
// until you run out of energy and stall/fall; dive to win it back. Land on the
// ground and it becomes a walk-around (WASD); Space takes off again; Enter exits
// to the normal first-person/editing view.
//
// The controlled avatar is a LOCAL node we spawn and drive directly — including
// its animation clip, since here we know the exact state (no AvatarFlight, which
// would fight us). Its pose is broadcast on the LookAt channel (Process's LookAt
// block uses it while active) so peers render what we fly; peers attach their own
// AvatarFlight and approximate the clip from the broadcast motion.
type flightState struct {
	active   bool
	avatar   Node3D.Instance
	player   AnimationPlayer.Instance
	grounded bool

	speed Float.X // airspeed along the view forward (the glider's energy)
	flapV Float.X // decaying extra vertical velocity from the last flap(s)

	intent    string  // current animation clip intent
	flapHold  Float.X // remaining time to keep the flap clip before reverting to glide
	spaceHeld bool    // edge-detect Space for the flap / take-off
}

const (
	// flightCruise is the launch / typical airspeed (u/s).
	flightCruise = Float.X(7)
	// flightGravity exchanges airspeed for height: climbing (nose up) bleeds
	// speed at this rate, diving (nose down) gains it — the core glide energy.
	flightGravity = Float.X(9)
	// flightDrag bleeds airspeed each second even in level flight, so an un-flapped
	// glide gradually slows toward a stall.
	flightDrag = Float.X(0.2)
	// flightFlapBoost is the small airspeed each flap adds — kept low so DIVING
	// (which gains airspeed via the flightGravity exchange) is the main way to
	// build energy, not flap-spam. flightFlapUp is the upward velocity a flap
	// forces (decaying at flightFlapDecay): it's applied as a FLOOR on the
	// vertical velocity, so a flap always lifts — even mid-dive — rather than
	// merely adding to a steep downward glide it can't overcome.
	flightFlapBoost = Float.X(0.8)
	flightFlapUp    = Float.X(5)
	flightFlapDecay = Float.X(9)
	// flightGlideSink is the baseline downward drift while not flapping — a real
	// glider always loses a little height to the air. Without it level flight
	// hovers forever (vel.Y=0) and you can never settle to the ground; with it,
	// stop flapping and you sink, descend, and land (gravity "brings you down").
	flightGlideSink = Float.X(1.5)
	// flightStallSpeed is the airspeed below which lift fails and the glider sinks
	// at up to (stall−speed)·flightStallSink u/s ON TOP of the baseline — you "run
	// out of energy and fall".
	flightStallSpeed = Float.X(3.5)
	flightStallSink  = Float.X(1.5)
	// flightMaxSpeed caps a dive's airspeed; flightMaxFall caps the downward
	// velocity so a steep nose-down doesn't plummet.
	flightMaxSpeed = Float.X(16)
	flightMaxFall  = Float.X(12)
	// flapClipHold is how long the flap clip plays after a Space press before the
	// glide clip resumes.
	flapClipHold = Float.X(0.45)
	// flightWalkSpeed is the on-foot pace once landed — matched to first-person
	// ground walking (fpsMoveSpeed) so landing and walking feels the same as the
	// normal grounded camera.
	flightWalkSpeed = Float.X(fpsMoveSpeed)
	// flightLandEpsilon is how close to the terrain (u) counts as landed, so a
	// model whose origin rides slightly above its feet still grounds.
	flightLandEpsilon = Float.X(0.2)
)

// enterFlight spawns the player's avatar at the camera and hands it the flappy
// glider with a chase cam. Gated to fpsEditors with no selection (the caller,
// toggleEnter, only reaches here when possession declined). Returns false (no-op)
// if it can't engage.
func (world *Client) enterFlight() bool {
	if world.flight.active || world.xr || !world.fpsEditor() || world.selection != 0 {
		return false
	}
	if world.TerrainEditor == nil {
		return false
	}
	resource := defaultAvatarURI
	if world.avatarResource != "" {
		resource = world.avatarResource
	}

	cam := world.FocalPoint.Lens.Camera.AsNode3D()
	eye := cam.GlobalPosition()
	fwd := cameraForward(world)
	// Launch point: at the camera eye, nudged forward so the avatar spawns in
	// view rather than on top of the lens, and never below the ground.
	spawn := Vector3.Add(eye, Vector3.MulX(horizontal(fwd), 1.5))
	if ground := world.TerrainEditor.HeightAt(Vector3.New(spawn.X, 0, spawn.Z)); spawn.Y < ground {
		spawn.Y = ground
	}

	avatar := LoadSync[PackedScene.Is[Node3D.Instance]](resource).Instantiate().
		SetPosition(spawn).
		SetScale(Vector3.New(0.1, 0.1, 0.1))
	world.AsNode().AddChild(avatar.AsNode())
	faceFlightDirection(avatar, fwd)

	world.flight = flightState{
		active:    true,
		avatar:    avatar,
		speed:     flightCruise, // launch into a glide, not a drop
		spaceHeld: Input.IsKeyPressed(Input.KeySpace),
	}
	if avatar.AsNode().HasNode("AnimationPlayer") {
		world.flight.player = Object.To[AnimationPlayer.Instance](avatar.AsNode().GetNode("AnimationPlayer"))
		world.setFlightClip("gliding", false)
	}

	// If we came straight from first-person ground mode, clear its flag: flight
	// now owns the camera (mouse already captured, UI already hidden), and a
	// dangling fpsMode would re-grab look handling once flight ends.
	world.fpsMode = false
	world.setMovementLocked(true)
	if world.ui != nil {
		world.ui.hideOverlay()
	}
	Input.SetMouseMode(Input.MouseModeCaptured)

	// Chase-cam framing (same constants as the critter control / possession view).
	world.FocalPoint.Lens.AsNode3D().SetRotation(Euler.Radians{X: Angle.Radians(controlCamPitch)})
	cam.SetPosition(Vector3.New(0, controlCamHeight, controlCamDist))
	world.FocalPoint.AsNode3D().SetGlobalPosition(spawn)
	return true
}

// exitFlight ends flight: free the avatar and leave the editing camera where the
// avatar ended up (you continue building wherever you flew/landed), reset to a
// normal third-person framing, and hand control back. The next Process LookAt
// resumes broadcasting the camera pose (its Offset will differ from the flight
// avatar's, so it re-sends without any extra nudge).
func (world *Client) exitFlight() {
	if !world.flight.active {
		return
	}
	landing := world.FocalPoint.AsNode3D().Position()
	if world.flight.avatar != Node3D.Nil {
		landing = world.flight.avatar.GlobalPosition()
		world.flight.avatar.AsNode().QueueFree()
	}
	world.flight = flightState{}

	world.FocalPoint.AsNode3D().SetGlobalPosition(landing)
	world.FocalPoint.Lens.Camera.AsNode3D().SetPosition(Vector3.New(0, 1, 3))
	world.FocalPoint.Lens.AsNode3D().SetRotation(Euler.Radians{})
	// Allow first-person to re-engage if we exited standing on the ground (the
	// normal editing view there); a prior exitFPS may have left this suppressed.
	world.fpsSuppressed = false
	world.setMovementLocked(false)
	if world.ui != nil {
		world.ui.showOverlay()
	}
	Input.SetMouseMode(Input.MouseModeVisible)
}

// updateFlight steps the glider energy model (or the on-foot walk once landed),
// keeps the chase cam on the avatar, drives the clip, and faces the avatar where
// it's going. The pose is broadcast by Process's LookAt block while active.
func (world *Client) updateFlight(dt Float.X) {
	if world.flight.avatar == Node3D.Nil {
		world.exitFlight()
		return
	}
	body := world.flight.avatar.AsNode3D()
	pos := body.GlobalPosition()
	fwd := cameraForward(world)
	ground := world.TerrainEditor.HeightAt(Vector3.New(pos.X, 0, pos.Z))

	space := Input.IsKeyPressed(Input.KeySpace)
	flap := space && !world.flight.spaceHeld
	world.flight.spaceHeld = space

	if world.flight.grounded {
		world.flightWalk(body, &pos, fwd, flap, dt)
	} else {
		world.flightGlide(body, &pos, fwd, ground, flap, dt)
	}

	body.SetGlobalPosition(pos)
	world.trackFlightCamera(pos)
}

// flightWalk is the on-foot state: walk relative to the view heading (W/S
// forward/back, A/D strafe), snap to the terrain, face the heading. Space flaps
// to take off again.
func (world *Client) flightWalk(body Node3D.Instance, pos *Vector3.XYZ, fwd Vector3.XYZ, flap bool, dt Float.X) {
	heading := horizontal(fwd)
	// Right-hand strafe vector for a +Z-forward heading (x,z): rotating the
	// heading −90° about Y gives (−z, 0, x), so D/Right strafes to screen-right.
	right := Vector3.New(-heading.Z, 0, heading.X)
	move := Vector3.Zero
	if Input.IsKeyPressed(Input.KeyW) || Input.IsKeyPressed(Input.KeyUp) {
		move = Vector3.Add(move, heading)
	}
	if Input.IsKeyPressed(Input.KeyS) || Input.IsKeyPressed(Input.KeyDown) {
		move = Vector3.Sub(move, heading)
	}
	if Input.IsKeyPressed(Input.KeyD) || Input.IsKeyPressed(Input.KeyRight) {
		move = Vector3.Add(move, right)
	}
	if Input.IsKeyPressed(Input.KeyA) || Input.IsKeyPressed(Input.KeyLeft) {
		move = Vector3.Sub(move, right)
	}
	moving := Vector3.Length(move) > 0.001
	if moving {
		*pos = Vector3.Add(*pos, Vector3.MulX(Vector3.Normalized(move), flightWalkSpeed*dt))
	}
	pos.Y = world.TerrainEditor.HeightAt(Vector3.New(pos.X, 0, pos.Z))
	faceFlightDirection(body, heading)

	if flap {
		// Take off: relaunch into a glide with an upward flap kick.
		world.flight.grounded = false
		world.flight.speed = flightCruise
		world.flight.flapV = flightFlapUp
		world.flight.flapHold = flapClipHold
		world.setFlightClip("flying", true)
		return
	}
	if moving {
		world.setFlightClip("walk", false)
	} else {
		world.setFlightClip("idle", false)
	}
}

// flightGlide is the airborne energy model: climbing (nose up) bleeds airspeed,
// diving gains it, drag trims it; below the stall airspeed lift fails and you
// sink. A flap adds airspeed + an immediate (decaying) lift, and restarts the
// flap clip so every Space press is a visible wing-beat.
func (world *Client) flightGlide(body Node3D.Instance, pos *Vector3.XYZ, fwd Vector3.XYZ, ground Float.X, flap bool, dt Float.X) {
	v := world.flight.speed
	v -= flightGravity * fwd.Y * dt // climb costs speed, dive gains it
	v -= flightDrag * v * dt
	if flap {
		v += flightFlapBoost
		world.flight.flapV = flightFlapUp // arm the upward floor (see below)
	}
	if v < 0 {
		v = 0
	} else if v > flightMaxSpeed {
		v = flightMaxSpeed
	}
	world.flight.speed = v
	if world.flight.flapV > 0 {
		world.flight.flapV = max(world.flight.flapV-flightFlapDecay*dt, 0)
	}

	vel := Vector3.MulX(fwd, v)
	vel.Y -= flightGlideSink // baseline glider sink, so you settle/land when not flapping
	if v < flightStallSpeed {
		vel.Y -= (flightStallSpeed - v) * flightStallSink // out of energy → extra sink
	}
	// A flap ALWAYS lifts, even mid-dive: while the flap kick is live, force the
	// vertical UP to its decaying floor rather than just adding to a steep
	// downward glide it couldn't overcome. MUST gate on flapV > 0 — otherwise a
	// decayed floor of 0 would clamp every descent up to zero (you could never go
	// down). Cap the dive/sink so a steep nose-down doesn't plummet.
	if world.flight.flapV > 0 && world.flight.flapV > vel.Y {
		vel.Y = world.flight.flapV
	}
	if vel.Y < -flightMaxFall {
		vel.Y = -flightMaxFall
	}
	*pos = Vector3.Add(*pos, Vector3.MulX(vel, dt))

	// Land once we settle onto the surface. The epsilon catches models whose
	// origin sits slightly above the feet (so pos.Y never quite reaches terrain)
	// and only triggers while actually descending, so a low skim doesn't snag.
	if pos.Y <= ground+flightLandEpsilon && vel.Y <= 0 {
		pos.Y = ground
		world.flight.speed = 0
		world.flight.flapV = 0
		world.flight.grounded = true
		world.setFlightClip("idle", false)
		faceFlightDirection(body, horizontal(fwd))
		return
	}

	faceFlightDirection(body, fwd)
	if flap {
		world.flight.flapHold = flapClipHold
		world.setFlightClip("flying", true) // restart the wing-beat per press
		return
	}
	if world.flight.flapHold > 0 {
		world.flight.flapHold -= dt
		return // let the flap beat play out
	}
	world.setFlightClip("gliding", false)
}

// setFlightClip drives the avatar's clip for intent. It is called every frame by
// the walk/glide loops and SELF-HEALS: it re-asserts the clip whenever the player
// isn't actually playing the one intent resolves to — keying off the player's
// real state rather than a cached intent flag. Without this, a single play() that
// didn't "take" (a clip stomped mid-blend by the preceding flap, or the model
// still streaming) latched the wrong animation forever, which is why a landed
// glider stayed stuck in its flying clip. restart forces a fresh play from frame
// 0 even if already on the clip, so every Space press is a distinct wing-beat.
// (Same re-assert pattern as ActionRenderer.play.)
func (world *Client) setFlightClip(intent string, restart bool) {
	if world.flight.player == (AnimationPlayer.Instance{}) {
		return
	}
	clip, ok := resolveCritterClip(world.flight.player, intent)
	if !ok {
		return
	}
	onClip := world.flight.player.IsPlaying() && world.flight.player.CurrentAnimation() == clip
	if onClip && !restart {
		world.flight.intent = intent
		return
	}
	world.flight.intent = intent
	playCritterClip(world.flight.avatar, world.flight.player, intent)
	if restart {
		world.flight.player.MoreArgs().SeekTo(0, true, false) // restart the wing-beat now
	}
}

// trackFlightCamera pins the chase cam onto the avatar each frame (mouse look
// orbits it freely — no recenter, the player steers with the view) and lifts the
// rig if the camera would dip below the terrain behind the avatar (the world's
// per-frame camera collision is suspended under controlLockMovement).
func (world *Client) trackFlightCamera(pos Vector3.XYZ) {
	focal := world.FocalPoint.AsNode3D()
	focal.SetGlobalPosition(pos)
	if world.TerrainEditor == nil {
		return
	}
	camNode := world.FocalPoint.Lens.Camera.AsNode3D()
	camPos := camNode.GlobalPosition()
	floor := world.TerrainEditor.HeightAt(Vector3.New(camPos.X, 0, camPos.Z)) + cameraTerrainClearance
	if camPos.Y < floor {
		fpPos := focal.Position()
		fpPos.Y += floor - camPos.Y
		focal.SetPosition(fpPos)
	}
}

// cameraForward is the unit view direction (−Z of the camera basis) — where the
// player is looking, which the glide follows.
func cameraForward(world *Client) Vector3.XYZ {
	t := world.FocalPoint.Lens.Camera.AsNode3D().GlobalTransform()
	fwd := Vector3.New(-t.Basis.Z.X, -t.Basis.Z.Y, -t.Basis.Z.Z)
	if Vector3.Length(fwd) < 1e-4 {
		return Vector3.New(0, 0, -1)
	}
	return Vector3.Normalized(fwd)
}

// horizontal drops a direction's vertical component and renormalises (the ground
// heading). Falls back to +Z if the input is near-vertical.
func horizontal(v Vector3.XYZ) Vector3.XYZ {
	h := Vector3.New(v.X, 0, v.Z)
	if Vector3.Length(h) < 1e-4 {
		return Vector3.New(0, 0, 1)
	}
	return Vector3.Normalized(h)
}

// faceFlightDirection points the avatar (which faces +Z locally, like the
// walk-here models) along dir. The look target's vertical is clamped so a steep
// dive/climb doesn't hit LookAt's degenerate up-vector case.
func faceFlightDirection(node Node3D.Instance, dir Vector3.XYZ) {
	look := dir
	if look.Y > 0.9 {
		look.Y = 0.9
	} else if look.Y < -0.9 {
		look.Y = -0.9
	}
	if Vector3.Length(Vector3.New(look.X, 0, look.Z)) < 1e-4 {
		return
	}
	node.MoreArgs().LookAt(Vector3.Add(node.GlobalPosition(), look), Vector3.New(0, 1, 0), true)
}
