package internal

import (
	"graphics.gd/classdb/AnimationPlayer"
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Vector3"

	"the.quetzal.community/aviary/internal/musical"
)

// EntityAnimator reconstructs a placed mobile entity's locomotion clip from its
// observed motion, so a critter/citizen/swimmer another client is driving — via
// the Commit=false Changes a GizmoEnter possession broadcasts, eased in by
// tweenPose — animates on every client instead of sliding frozen in its idle
// pose. It is the ground-mover counterpart to AvatarFlight (which does the same
// for flying avatars): walk while moving horizontally, idle when still, and a
// one-shot jump while airborne above the terrain (ground walkers only, and only
// if the model carries a jump clip — see hasJumpClip).
//
// Attached to every mobile placed entity (attachEntityAnimator), it stands down
// while another system already owns the AnimationPlayer:
// the LOCAL possessor drives the clip directly in updatePossess, so the animator
// stands down there (blanking its intent so it re-picks cleanly on release).
//
// It does NOT defer to a walk-here ActionRenderer: that renderer drives the
// entity's *motion*, which the animator reads anyway, so both independently
// resolve "walk" while it moves and "idle" when it stops — they agree, and
// playCritterClip is idempotent on an unchanged intent. Because it watches
// actual movement (not the update cadence) it settles to idle when the motion
// stops and no more Changes arrive.
type EntityAnimator struct {
	Node.Extension[EntityAnimator]

	body           Node3D.Instance
	player         AnimationPlayer.Instance
	client         *Client
	entity         musical.Entity
	hasJump        bool
	terrainWalking bool
	swimmer        bool // fish: reconstruct swim clips from 3D motion, die out of water

	last     Vector3.XYZ
	accum    Float.X
	have     bool
	intent   string  // current clip intent ("" forces a re-pick)
	jumpHold Float.X // remaining time to let a reconstructed jump play out
}

func (a *EntityAnimator) Ready() {
	a.last = a.body.GlobalPosition()
	a.have = true
}

func (a *EntityAnimator) Process(delta Float.X) {
	a.accum += delta
	if a.accum < avatarFlightSample {
		return
	}
	sample := a.accum
	a.accum = 0
	pos := a.body.GlobalPosition()
	if !a.have {
		a.last, a.have = pos, true
		return
	}
	horiz := Vector3.Distance(Vector3.XYZ{X: a.last.X, Z: a.last.Z}, Vector3.XYZ{X: pos.X, Z: pos.Z}) / sample
	vert := pos.Y - a.last.Y
	if vert < 0 {
		vert = -vert
	}
	vert /= sample
	a.last = pos
	if a.jumpHold > 0 {
		a.jumpHold -= sample
	}

	// Defer to whoever already owns the clip; blank the intent so the resumed
	// pick isn't suppressed as a no-op change.
	if a.client != nil && a.client.possess.active && a.client.possess.entity == a.entity {
		a.intent = ""
		return
	}

	// Fish reconstruct from 3D motion (and die out of water) on their own path —
	// they never jump, and their swim clips are picked by the dominant axis.
	if a.swimmer {
		a.animateSwimmer(pos, horiz, vert)
		return
	}

	// Reconstruct a leap from the entity's height above the terrain: a possessed
	// ground walker is snapped to the surface except while jumping, when the
	// broadcast carries its arc (jumpYOffset) as a positive terrain-relative Y.
	// Hold the jump clip for its nominal duration so a brief rise reads as a full
	// hop rather than a flicker.
	if a.terrainWalking && a.hasJump && a.jumpHold <= 0 && a.client != nil && a.client.TerrainEditor != nil {
		altitude := pos.Y - a.client.TerrainEditor.HeightAt(Vector3.New(pos.X, 0, pos.Z))
		if altitude > entityJumpAltitude {
			a.jumpHold = Float.X(jumpDuration)
			a.setIntent("jump")
		}
	}
	if a.jumpHold > 0 {
		return // let the jump play out before reasserting walk/idle
	}

	want := "idle"
	if horiz > avatarWalkSpeed {
		want = "walk"
	}
	a.setIntent(want)
}

// animateSwimmer reconstructs a fish's clip from its observed 3D motion. It
// floats belly-up (a frozen "Dead Floating") the moment its origin rises above
// the water surface — so a draining lake or a dropped water level strands every
// fish at once — swims vertically when its motion is mostly up/down, horizontally
// when mostly sideways, and idles (hovering in place) when still. Everything is
// derived from the synced position and the synced water level, so all clients see
// the same state with no extra mutation. Swimmers never jump.
func (a *EntityAnimator) animateSwimmer(pos Vector3.XYZ, horiz, vert Float.X) {
	if a.client != nil && a.client.TerrainEditor != nil &&
		pos.Y > a.client.TerrainEditor.WaterSurfaceAt(pos) {
		a.setIntent(swimClipDeath)
		return
	}
	switch {
	case horiz < avatarWalkSpeed && vert < avatarWalkSpeed:
		a.setIntent("idle")
	case vert > horiz:
		a.setIntent(swimClipVertical)
	default:
		a.setIntent(swimClipHorizontal)
	}
}

func (a *EntityAnimator) setIntent(intent string) {
	if a.intent == intent {
		return
	}
	a.intent = intent
	playCritterClip(a.body, a.player, intent)
}

// entityJumpAltitude is the height above the terrain (world units) past which a
// ground walker is taken to be mid-jump. Comfortably above the snap-to-terrain
// residual yet below the possession jump's peak (jumpHeight).
const entityJumpAltitude = Float.X(0.1)

// attachEntityAnimator wires an EntityAnimator onto a freshly created/streamed
// mobile entity so its locomotion animates on every client. No-op without an
// AnimationPlayer, if one is already attached, or for non-mobile designs (caller
// gates on isMobileDesignCategory).
func attachEntityAnimator(client *Client, entity musical.Entity, node Node3D.Instance, terrainWalking, swimmer bool) {
	if !node.AsNode().HasNode("AnimationPlayer") || node.AsNode().HasNode("EntityAnimator") {
		return
	}
	a := new(EntityAnimator)
	a.client = client
	a.entity = entity
	a.body = node
	a.player = Object.To[AnimationPlayer.Instance](node.AsNode().GetNode("AnimationPlayer"))
	a.hasJump = hasJumpClip(a.player)
	a.terrainWalking = terrainWalking
	a.swimmer = swimmer
	a.AsNode().SetName("EntityAnimator")
	node.AsNode().AddChild(a.AsNode())
}

// maybeAttachEntityAnimator attaches an EntityAnimator to node when its design is
// a mobile dressing category (critter/citizen/swimmer/…), so any networked motion
// of it (a peer's possession, future Change-driven movement) reconstructs the
// locomotion clip. Static scenery is left alone.
func (world *Client) maybeAttachEntityAnimator(entity musical.Entity, design musical.Design, node Node3D.Instance) {
	category := designCategory(world.design_to_string[design])
	if !isMobileDesignCategory(category) {
		return
	}
	// Mobile entities are children of the TerrainEditor node, whose whole
	// subtree the scenery/coaster editors flip to ProcessModeDisabled when
	// they're not the active editor. That would freeze a possessed entity's
	// AnimationPlayer, its EntityAnimator, and the node-bound tweenPose tween
	// for any observer not currently in the scenery editor — so a peer watching
	// from another view would see it neither move nor animate. Decouple mobile
	// entities from that gate (Pausable still honours a real SceneTree pause) so
	// their networked motion is observable everywhere, like avatars.
	node.AsNode().SetProcessMode(Node.ProcessModePausable)
	attachEntityAnimator(world, entity, node, isTerrainWalkingCategory(category), isSwimmerCategory(category))
}
