package internal

import (
	"strings"
	"time"

	"graphics.gd/classdb/AnimationPlayer"
	"graphics.gd/classdb/Input"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/variant/Angle"
	"graphics.gd/variant/Euler"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Vector3"

	"the.quetzal.community/aviary/internal/musical"
)

// possessState is the live state of a GizmoEnter possession: the player has
// "entered" a placed mobile entity (a critter/citizen/swimmer) and drives it
// directly with WASD (+ space to jump) from a third-person chase cam. While
// active the world's normal WASD camera handling is locked (controlLockMovement)
// and the entity's motion is broadcast to peers as Commit=false Changes; the
// final pose is committed (and recorded for undo) on exit. See enterPossess /
// updatePossess / exitPossess and the skip guard in musicalImpl.Change.
type possessState struct {
	active         bool
	entity         musical.Entity
	player         AnimationPlayer.Instance
	hasJump        bool   // a real jump clip OR a procedural rig (gates the leap)
	hasAttack      bool   // the model carries a real bite/attack clip (gates the strike)
	hasBark        bool   // the model carries a real bark/vocal clip (gates the bark)
	terrainWalking bool   // ground-walker (snap to terrain) vs air/water (keep Y)
	swimmer        bool   // fish: mouse-aimed 3D swim, clamped to the water column
	intent         string // current locomotion clip intent ("" until first set)

	jumpActive bool
	jumpTime   Float.X

	// One-shot gesture: a left-click bite/attack or a right-click bark. Plays a
	// non-looping clip for gestureLength and ROOTS the entity in place while it
	// plays (no skating), then re-picks locomotion. attackHeld/barkHeld debounce
	// each button so a held click fires once rather than every frame.
	gestureActive bool
	gestureTime   Float.X
	gestureLength Float.X
	attackHeld    bool
	barkHeld      bool

	lastSent time.Time // throttle for the Commit=false motion broadcast

	// Pre-possession camera rig transform, restored on exit.
	savedFocalPos Vector3.XYZ
	savedFocalRot Euler.Radians
	savedLensRot  Euler.Radians
	savedCamPos   Vector3.XYZ

	// Pre-possession entity pose, used as the undo target for the exit commit.
	startPos Vector3.XYZ
	startRot Euler.Radians
}

const (
	// possessWalkSpeed matches the critter control view (controlWalkSpeed) so the
	// on-foot feel carries across. Steering is mouse-look (FPS-style), so there's no
	// separate turn rate — A/D strafe relative to the view instead.
	possessWalkSpeed = float32(0.5)
	// possessRunMultiplier is the Shift-to-run boost. It scales BOTH the ground
	// speed (here) and the walk-clip cadence (critterClipSpeed, intent "run") by
	// the same factor, so the legs cycle in step with the faster travel and don't
	// skate. Tuning the run feel means changing this one number.
	possessRunMultiplier = float32(4.0)
	// possessSendInterval throttles the Commit=false motion broadcast to peers
	// (~10 Hz, like LookAt and the scenery placement preview).
	possessSendInterval = time.Second / 10

	// possessCam* frame the third-person chase cam while possessing: a tighter,
	// zoomed-in over-the-shoulder shot than the critter-control view (controlCam*),
	// since the mouse now steers the camera first-person style. The pre-possession
	// rig is snapshotted on enter and restored verbatim on exit (savedCam* below).
	possessCamHeight = float32(0.5)
	possessCamDist   = float32(2.0)
	possessCamPitch  = float32(-0.2)
)

// toggleEnter is the Enter key (and the GizmoEnter toolbar button): exit whatever
// control mode is active, else possess the selected mobile entity, else — with
// nothing selected in an fpsEditor — take off in third-person self-flight.
func (world *Client) toggleEnter() {
	if world.possess.active {
		world.exitPossess()
		return
	}
	if world.flight.active {
		world.exitFlight()
		return
	}
	if world.enterPossess() {
		return
	}
	world.enterFlight()
}

// enterPossess begins controlling the selected entity, if it is a placed mobile
// design (critter/citizen/swimmer/…) with an AnimationPlayer, while the scenery
// editor is active. Returns false (no-op) when there's no controllable selection.
func (world *Client) enterPossess() bool {
	if world.possess.active || world.xr {
		return false
	}
	if world.Editing != Editing.Scenery {
		return false
	}
	entity, node, _, ok := world.resolveSelection()
	if !ok || node == Node3D.Nil {
		return false
	}
	if !node.AsNode().HasNode("AnimationPlayer") {
		return false
	}
	// Only mobile entities may be possessed — static scenery (rocks, fences,
	// buildings) stays put. Mirrors the right-click "walk here" gate so the two
	// ways of moving a placed entity agree on what's mobile. User-design creations
	// count as mobile (isMobileDesign) even though they have no library category.
	design, has := world.findDesignForObject(Node3D.ID(node.ID()))
	if !has {
		return false
	}
	if !world.isMobileDesign(design) {
		return false
	}
	category := designCategory(world.design_to_string[design])
	player := Object.To[AnimationPlayer.Instance](node.AsNode().GetNode("AnimationPlayer"))

	world.possess = possessState{
		active: true,
		entity: entity,
		player: player,
		// A model jumps if it carries a real jump clip; a user-design
		// creation jumps procedurally instead — its CritterAnimator
		// reads the root's arc (crouch dip → leap) straight from the
		// altitude above the terrain, so peers reconstruct the same
		// animation from the broadcast motion (playCritterClip on its
		// empty AnimationPlayer is a harmless no-op).
		hasJump:        hasJumpClip(player) || node.AsNode().HasNode("CritterAnimator"),
		hasAttack:      hasAttackClip(player),
		hasBark:        hasCritterClip(player, "bark"),
		terrainWalking: world.designWalksTerrain(design),
		swimmer:        isSwimmerCategory(category),
		startPos:       node.AsNode3D().Position(),
		startRot:       node.AsNode3D().Rotation(),
	}

	// Snapshot the camera rig so exitPossess puts it back exactly.
	world.possess.savedFocalPos = world.FocalPoint.AsNode3D().Position()
	world.possess.savedFocalRot = world.FocalPoint.AsNode3D().Rotation()
	world.possess.savedLensRot = world.FocalPoint.Lens.AsNode3D().Rotation()
	world.possess.savedCamPos = world.FocalPoint.Lens.Camera.AsNode3D().Position()

	// Drop any walk-here path already running on this entity: we're taking direct
	// control now. Locally this stops the ActionRenderer fighting updatePossess's
	// per-frame SetPosition; on peers the future-stamped possession moves cancel
	// it too (see sendPossessChange).
	cancelEntityAction(node)

	// Hide the selection outline while driving — we're now controlling the entity,
	// not editing it, so the highlight just clutters the third-person view. It's
	// restored on exit (the entity stays selected underneath).
	Select(node.AsNode(), false)

	world.setMovementLocked(true)
	if world.ui != nil {
		world.ui.hideOverlay() // full-screen the third-person view while driving
	}
	// Capture the mouse so it steers the chase cam first-person style — for ground
	// walkers/citizens as well as look-to-swim fish (the camera heading is the move
	// direction; see updatePossess / updateSwimPossess and the possess case in
	// UnhandledInput). Released on exit.
	Input.SetMouseMode(Input.MouseModeCaptured)

	// Frame the chase cam zoomed in behind the model (which faces +Z, see
	// ActionRenderer.OrientModel): lens tilted down, camera lifted and pulled back a
	// short distance, focal yaw flipped so we start looking at the model's back. The
	// mouse takes over the view from here.
	world.FocalPoint.Lens.AsNode3D().SetRotation(Euler.Radians{X: Angle.Radians(possessCamPitch)})
	world.FocalPoint.Lens.Camera.AsNode3D().SetPosition(Vector3.New(0, possessCamHeight, possessCamDist))
	world.FocalPoint.AsNode3D().SetGlobalPosition(possessFocalCenter(node))
	world.FocalPoint.AsNode3D().SetRotation(Euler.Radians{Y: node.AsNode3D().Rotation().Y + Angle.Pi})

	playCritterClip(node, player, "idle")
	return true
}

// exitPossess leaves possession: commit the entity's final pose (and record an
// undo back to where the possession started), then restore the camera rig and
// release the keyboard lock.
func (world *Client) exitPossess() {
	if !world.possess.active {
		return
	}
	if raw, ok := world.entity_to_object[world.possess.entity].Instance(); ok {
		if node, ok := Object.As[Node3D.Instance](raw); ok {
			world.commitPossess(node)
			// Restore the selection outline hidden on enter: the entity is still
			// selected, so the editor view should show it highlighted again.
			if world.selection == Node3D.ID(node.ID()) {
				Select(node.AsNode(), true)
			}
		}
	}
	world.FocalPoint.AsNode3D().SetPosition(world.possess.savedFocalPos)
	world.FocalPoint.AsNode3D().SetRotation(world.possess.savedFocalRot)
	world.FocalPoint.Lens.AsNode3D().SetRotation(world.possess.savedLensRot)
	world.FocalPoint.Lens.Camera.AsNode3D().SetPosition(world.possess.savedCamPos)
	world.setMovementLocked(false)
	if world.ui != nil {
		world.ui.showOverlay()
	}
	Input.SetMouseMode(Input.MouseModeVisible) // release the mouse-look capture
	world.possess.active = false
}

// updatePossess runs each frame while possessing: read WASD to move the entity
// relative to the mouse-steered view heading (forward/back + strafe) and face it
// where the camera looks, snap it to the terrain (ground walkers), trigger/advance
// a jump, pick the locomotion clip, broadcast the motion to peers, and track the cam.
func (world *Client) updatePossess(dt Float.X) {
	// Re-resolve the node from the entity each frame: a remote Remove (or a
	// reload) could have freed it out from under us.
	raw, ok := world.entity_to_object[world.possess.entity].Instance()
	if !ok {
		world.exitPossess()
		return
	}
	node, ok := Object.As[Node3D.Instance](raw)
	if !ok {
		world.exitPossess()
		return
	}

	// Fish swim in 3D, mouse-aimed and clamped to the water column — a wholly
	// different control loop from the WASD ground walker below.
	if world.possess.swimmer {
		world.updateSwimPossess(node, dt)
		return
	}

	body := node.AsNode3D()

	// Mouse-look steers the camera (FPS-style, like self-flight); WASD moves the
	// entity relative to that view heading — W/S forward/back, A/D strafe — and the
	// entity is turned to face where the camera looks. The camera follows behind via
	// trackFlightCamera with no yaw recenter, so the player steers entirely by mouse.
	fwd := cameraForward(world)
	heading := horizontal(fwd)
	right := Vector3.New(-heading.Z, 0, heading.X) // screen-right for a +Z heading
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
	// Shift sprints: faster travel, and (when actually moving) the run-cadence clip.
	running := Input.IsKeyPressed(Input.KeyShift)
	speed := possessWalkSpeed
	if running {
		speed *= possessRunMultiplier
	}
	// One-shot gestures fired by the mouse buttons, resolved BEFORE movement so the
	// entity is rooted the instant one starts: left-click bites/attacks, right-click
	// barks. Each is gated on a real clip (hasAttack/hasBark), on the press EDGE (a
	// held button fires once, not every frame), and on nothing else one-shot already
	// playing. The intent is queued onto the next LookAt so peers play the same
	// gesture on the critter we're driving (see playObservedGesture).
	leftPressed := Input.IsMouseButtonPressed(Input.MouseButtonLeft)
	rightPressed := Input.IsMouseButtonPressed(Input.MouseButtonRight)
	if !world.possess.gestureActive && !world.possess.jumpActive {
		switch {
		case world.possess.hasAttack && leftPressed && !world.possess.attackHeld:
			world.startPossessGesture(node, "attack")
		case world.possess.hasBark && rightPressed && !world.possess.barkHeld:
			world.startPossessGesture(node, "bark")
		}
	}
	world.possess.attackHeld = leftPressed
	world.possess.barkHeld = rightPressed
	if world.possess.gestureActive {
		world.possess.gestureTime += dt
		if world.possess.gestureTime >= world.possess.gestureLength {
			world.possess.gestureActive = false
			world.possess.gestureTime = 0
			world.possess.intent = "" // force a re-pick of walk/idle below
		}
	}

	// Spacebar leaps — gated on a real jump clip, on no jump already in flight
	// (holding space can't bunny-hop or buffer a second jump), and on no gesture
	// playing.
	if world.possess.hasJump && Input.IsKeyPressed(Input.KeySpace) &&
		!world.possess.jumpActive && !world.possess.gestureActive {
		world.possess.jumpActive = true
		world.possess.jumpTime = 0
		playCritterClip(node, world.possess.player, "jump")
		world.possess.intent = "jump"
	}
	if world.possess.jumpActive {
		world.possess.jumpTime += dt
		if world.possess.jumpTime >= Float.X(jumpDuration) {
			world.possess.jumpActive = false
			world.possess.jumpTime = 0
			world.possess.intent = "" // force a re-pick of walk/idle below
		}
	}

	// Move relative to the view — unless a one-shot gesture roots the entity (an
	// attack/bark shouldn't slide it; the locomotion clip is suspended, so travel
	// would skate). The jump deliberately stays mobile (you leap forward).
	pos := body.Position()
	if moving && !world.possess.gestureActive {
		pos = Vector3.Add(pos, Vector3.MulX(Vector3.Normalized(move), Float.X(speed)*dt))
	}

	// Ground walkers ride the terrain surface; a jump arcs on top of it. Air /
	// water movers (airship/seaship/swimmer) keep whatever Y they walked to.
	if world.possess.terrainWalking {
		ground := world.TerrainEditor.HeightAt(Vector3.New(pos.X, 0, pos.Z))
		pos.Y = ground
		if world.possess.jumpActive {
			pos.Y += Float.X(jumpYOffset(float32(world.possess.jumpTime) / jumpDuration))
		}
	}
	body.SetPosition(pos)
	// Face where the camera looks (FPS-locked), but hold the heading while a gesture
	// roots the entity, and let middle-click free-orbit the camera WITHOUT turning
	// the entity (so you can look around it; release resumes locked facing).
	if !world.possess.gestureActive && !Input.IsMouseButtonPressed(Input.MouseButtonMiddle) {
		faceFlightDirection(body, heading)
	}

	// Locomotion clip (only while no one-shot — jump, attack, or bark — is playing):
	// run when sprinting and moving, walk when moving, else idle. Run is gated on
	// actual travel so a Shift held while standing still stays idle (no skating).
	if !world.possess.jumpActive && !world.possess.gestureActive {
		want := "idle"
		if moving {
			want = "walk"
		}
		if running && moving {
			want = "run"
		}
		if want != world.possess.intent {
			world.possess.intent = want
			playCritterClip(node, world.possess.player, want)
		}
	}

	// Broadcast the motion so peers see it move (Commit=false; the final pose is
	// committed on exit). Throttled like LookAt. Our own apply is skipped in
	// musicalImpl.Change — we already drove the node directly above.
	if time.Since(world.possess.lastSent) >= possessSendInterval {
		world.sendPossessChange(node, false)
		world.possess.lastSent = time.Now()
	}

	world.trackFlightCamera(possessFocalCenter(node))
}

// possessFocalCenter is the point the chase cam pins to and orbits: the critter's
// visual centre in the horizontal plane, so its body sits centred on screen rather
// than pivoting on the model ORIGIN — which on some rigs sits off to the rear (or
// front) of the mesh, making the camera appear centred slightly behind the critter.
// The Y is left at the origin so the vertical framing stays governed by
// possessCamHeight / possessCamPitch. Falls back to the origin if the model has no
// measurable visual bounds.
func possessFocalCenter(node Node3D.Instance) Vector3.XYZ {
	center := node.AsNode3D().GlobalPosition()
	if bmin, bmax, ok := worldVisualBounds(node.AsNode()); ok {
		center.X = (bmin.X + bmax.X) * 0.5
		center.Z = (bmin.Z + bmax.Z) * 0.5
	}
	return center
}

// startPossessGesture begins a one-shot gesture (an attack bite or a bark) on the
// possessed entity: play the non-looping clip, root the entity for its length
// (gestureActive suspends movement/turning above), and queue the intent onto the
// next LookAt so peers play the same gesture on the critter we're driving (see
// playObservedGesture / EntityAnimator.PlayGesture).
func (world *Client) startPossessGesture(node Node3D.Instance, intent string) {
	world.possess.gestureActive = true
	world.possess.gestureTime = 0
	world.possess.gestureLength = critterClipLength(world.possess.player, intent)
	playCritterClip(node, world.possess.player, intent)
	world.possess.intent = intent
	world.pendingGesture = intent
}

// sendPossessChange publishes the entity's current pose as a musical Change.
// Ground walkers store Y as a terrain-relative delta (Editor "float") so the
// move rides later terrain edits and reload, exactly like a scenery gizmo move;
// air/water movers store an absolute Y.
func (world *Client) sendPossessChange(node Node3D.Instance, commit bool) {
	pos := node.AsNode3D().Position()
	ch := musical.Change{
		Author: world.id,
		Entity: world.possess.entity,
		Offset: pos,
		Angles: node.AsNode3D().Rotation(),
		// Stamp FUTURE, not now: a walk-here Action sets the entity's positional
		// high-water mark to its own Future() timing, so a now-stamped move loses
		// the entity_move_timing gate on peers and never applies (nor cancels the
		// action) — the entity would keep being dragged by the stale path while we
		// drive it here. A future stamp makes each possession move the newest, so
		// it wins the gate AND triggers cancelEntityAction, taking control cleanly.
		Timing: world.time.Future(),
		Commit: commit,
	}
	// Both ground walkers and swimmers store Y terrain-relative (Editor "float"):
	// the walker rides the surface, the fish keeps its height above the seabed so
	// its depth survives terrain edits and reload — and so a dropping water level
	// can still strand it above the surface (the death case). Only true air movers
	// (airship/rockets) keep an absolute Y.
	if world.possess.terrainWalking || world.possess.swimmer {
		ground := world.TerrainEditor.HeightAt(Vector3.New(pos.X, 0, pos.Z))
		ch.Offset.Y = pos.Y - ground
		ch.Editor = "float"
	}
	_ = world.space.Change(ch)
}

// commitPossess writes the final Commit=true Change for the possessed entity and
// records the inverse (back to the pre-possession pose) for undo.
func (world *Client) commitPossess(node Node3D.Instance) {
	// End any in-flight jump cleanly so the committed Y is the resting surface,
	// not a mid-arc height.
	if world.possess.terrainWalking && world.possess.jumpActive {
		pos := node.AsNode3D().Position()
		pos.Y = world.TerrainEditor.HeightAt(Vector3.New(pos.X, 0, pos.Z))
		node.AsNode3D().SetPosition(pos)
	}
	world.possess.jumpActive = false

	pos := node.AsNode3D().Position()
	ch := musical.Change{
		Author: world.id,
		Entity: world.possess.entity,
		Offset: pos,
		Angles: node.AsNode3D().Rotation(),
		// Future stamp (as with the live moves) so the final pose is the newest
		// positional mutation — it wins the gate over any cancelled walk path on
		// every client and on reload.
		Timing: world.time.Future(),
		Commit: true,
	}
	undo := musical.Change{
		Author: world.id,
		Entity: world.possess.entity,
		Offset: world.possess.startPos,
		Angles: world.possess.startRot,
		Commit: true,
	}
	if world.possess.terrainWalking || world.possess.swimmer {
		ground := world.TerrainEditor.HeightAt(Vector3.New(pos.X, 0, pos.Z))
		ch.Offset.Y = pos.Y - ground
		ch.Editor = "float"
		startGround := world.TerrainEditor.HeightAt(Vector3.New(world.possess.startPos.X, 0, world.possess.startPos.Z))
		undo.Offset.Y = world.possess.startPos.Y - startGround
		undo.Editor = "float"
	}
	_ = world.space.Change(ch)
	world.RecordChange(ch, undo)
}

// hasCritterClip reports whether the model carries a clip matching intent's keyword
// set, so a gated one-shot (jump / attack / bark) is only offered when there's a
// real animation to play — never the idle fallback resolveCritterClip would
// otherwise substitute.
func hasCritterClip(player AnimationPlayer.Instance, intent string) bool {
	for _, name := range player.AsAnimationMixer().GetAnimationList() {
		lower := strings.ToLower(name)
		for _, kw := range critterClipKeywords[intent] {
			if strings.Contains(lower, kw) {
				return true
			}
		}
	}
	return false
}

// hasJumpClip / hasAttackClip gate the spacebar leap and the left-click strike on a
// real clip (see hasCritterClip). hasJumpClip is also used by EntityAnimator.
func hasJumpClip(player AnimationPlayer.Instance) bool   { return hasCritterClip(player, "jump") }
func hasAttackClip(player AnimationPlayer.Instance) bool { return hasCritterClip(player, "attack") }
