package internal

import (
	"math"
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
	hasJump        bool   // the model carries a real jump clip (gates the leap)
	terrainWalking bool   // ground-walker (snap to terrain) vs air/water (keep Y)
	swimmer        bool   // fish: mouse-aimed 3D swim, clamped to the water column
	intent         string // current locomotion clip intent ("" until first set)

	jumpActive bool
	jumpTime   Float.X

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
	// possessWalkSpeed / possessTurnRate match the critter control view
	// (controlWalkSpeed / controlTurnRate) so the on-foot feel carries across.
	possessWalkSpeed = float32(2.0)
	possessTurnRate  = float32(2.5)
	// possessSendInterval throttles the Commit=false motion broadcast to peers
	// (~10 Hz, like LookAt and the scenery placement preview).
	possessSendInterval = time.Second / 10
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
	// Only the mobile dressing categories may be possessed — static scenery
	// (rocks, fences, buildings) stays put. Mirrors the right-click "walk here"
	// gate so the two ways of moving a placed entity agree on what's mobile.
	design, has := world.findDesignForObject(Node3D.ID(node.ID()))
	if !has {
		return false
	}
	category := designCategory(world.design_to_string[design])
	if !isMobileDesignCategory(category) {
		return false
	}
	player := Object.To[AnimationPlayer.Instance](node.AsNode().GetNode("AnimationPlayer"))

	world.possess = possessState{
		active:         true,
		entity:         entity,
		player:         player,
		hasJump:        hasJumpClip(player),
		terrainWalking: isTerrainWalkingCategory(category),
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

	world.setMovementLocked(true)
	if world.ui != nil {
		world.ui.hideOverlay() // full-screen the third-person view while driving
	}
	// A swimmer is steered look-to-swim (like self-flight): capture the mouse so it
	// aims the 3D heading. Ground walkers/citizens keep the cursor for middle-drag
	// orbit and WASD-turn control.
	if world.possess.swimmer {
		Input.SetMouseMode(Input.MouseModeCaptured)
	}

	// Frame the chase cam exactly like the critter control view: lens tilted
	// down, camera lifted and pulled back, focal yaw flipped behind the model
	// (which faces +Z, see ActionRenderer.OrientModel).
	world.FocalPoint.Lens.AsNode3D().SetRotation(Euler.Radians{X: Angle.Radians(controlCamPitch)})
	world.FocalPoint.Lens.Camera.AsNode3D().SetPosition(Vector3.New(0, controlCamHeight, controlCamDist))
	world.FocalPoint.AsNode3D().SetGlobalPosition(node.AsNode3D().GlobalPosition())
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
	if world.possess.swimmer {
		Input.SetMouseMode(Input.MouseModeVisible) // release the look-to-swim capture
	}
	world.possess.active = false
}

// updatePossess runs each frame while possessing: read WASD to walk/turn the
// entity, snap it to the terrain (ground walkers), trigger/advance a jump,
// pick the locomotion clip, broadcast the motion to peers, and track the cam.
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

	var forward, turn float32
	if Input.IsKeyPressed(Input.KeyW) || Input.IsKeyPressed(Input.KeyUp) {
		forward += 1
	}
	if Input.IsKeyPressed(Input.KeyS) || Input.IsKeyPressed(Input.KeyDown) {
		forward -= 1
	}
	if Input.IsKeyPressed(Input.KeyA) || Input.IsKeyPressed(Input.KeyLeft) {
		turn += 1
	}
	if Input.IsKeyPressed(Input.KeyD) || Input.IsKeyPressed(Input.KeyRight) {
		turn -= 1
	}
	if turn != 0 {
		body.Rotate(Vector3.New(0, 1, 0), Angle.Radians(turn*possessTurnRate*float32(dt)))
	}
	if forward != 0 {
		// Models face +Z (see ActionRenderer.OrientModel), so +Z is forward.
		body.Translate(Vector3.New(0, 0, Float.X(forward*possessWalkSpeed*float32(dt))))
	}

	// Spacebar leaps — gated on a real jump clip and on no jump already in
	// flight (holding space can't bunny-hop or buffer a second jump).
	if world.possess.hasJump && Input.IsKeyPressed(Input.KeySpace) && !world.possess.jumpActive {
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

	// Ground walkers ride the terrain surface; a jump arcs on top of it. Air /
	// water movers (airship/seaship/swimmer) keep whatever Y they walked to.
	if world.possess.terrainWalking {
		pos := body.Position()
		ground := world.TerrainEditor.HeightAt(Vector3.New(pos.X, 0, pos.Z))
		pos.Y = ground
		if world.possess.jumpActive {
			pos.Y += Float.X(jumpYOffset(float32(world.possess.jumpTime) / jumpDuration))
		}
		body.SetPosition(pos)
	}

	// Locomotion clip (only while not mid-jump): walk when moving, else idle.
	if !world.possess.jumpActive {
		want := "idle"
		if forward != 0 || turn != 0 {
			want = "walk"
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

	world.trackPossessCamera(body, forward != 0 || turn != 0, dt)
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

// trackPossessCamera pins the focal point onto the possessed entity each frame
// (so the camera follows) and, while moving, eases the yaw back to "behind" the
// entity — the third-person-game feel from the critter control view. Mid-mouse
// drag suspends the recenter so the user can orbit freely. After pinning, the
// rig is lifted if the camera would dip below the terrain behind the entity
// (the world's per-frame camera collision is suspended under controlLockMovement).
func (world *Client) trackPossessCamera(body Node3D.Instance, moving bool, dt Float.X) {
	focal := world.FocalPoint.AsNode3D()
	focal.SetGlobalPosition(body.GlobalPosition())

	if moving && !Input.IsMouseButtonPressed(Input.MouseButtonMiddle) {
		target := body.Rotation().Y + Angle.Pi
		rot := focal.Rotation()
		diff := target - rot.Y
		for diff > Angle.Pi {
			diff -= 2 * Angle.Pi
		}
		for diff < -Angle.Pi {
			diff += 2 * Angle.Pi
		}
		t := 1 - Angle.Radians(math.Exp(-float64(controlYawRecenterRate)*float64(dt)))
		rot.Y += diff * t
		focal.SetRotation(rot)
	}

	// Keep the camera above the ground behind the entity.
	if world.TerrainEditor != nil {
		camNode := world.FocalPoint.Lens.Camera.AsNode3D()
		camPos := camNode.GlobalPosition()
		floor := world.TerrainEditor.HeightAt(Vector3.New(camPos.X, 0, camPos.Z)) + cameraTerrainClearance
		if camPos.Y < floor {
			fpPos := focal.Position()
			fpPos.Y += floor - camPos.Y
			focal.SetPosition(fpPos)
		}
	}
}

// hasJumpClip reports whether the model carries a clip that reads as a jump, so
// possession only offers the leap when there's a real animation to play (never
// the idle fallback resolveCritterClip would otherwise substitute).
func hasJumpClip(player AnimationPlayer.Instance) bool {
	for _, name := range player.AsAnimationMixer().GetAnimationList() {
		lower := strings.ToLower(name)
		for _, kw := range critterClipKeywords["jump"] {
			if strings.Contains(lower, kw) {
				return true
			}
		}
	}
	return false
}
