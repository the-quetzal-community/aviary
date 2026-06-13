package internal

import (
	"graphics.gd/classdb/AnimationPlayer"
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Vector3"
)

// AvatarFlight drives a flying avatar's animation from its own motion.
//
// Other players' avatars (the everything "avatar" birds/bats/insects) follow
// the player's camera, which can be high in the air or down near the terrain, so
// the avatar reads as a flying creature when airborne and a ground critter when
// low. The rigs carry full clip sets, so the intent can't be chosen once at
// spawn; instead this samples the avatar's per-frame displacement (driven by the
// LookAt position tweens) and its height above the terrain to pick:
//
//   - airborne: "flying" (flap) to hold station / climb / cruise level, and
//     "gliding" (glide) only while descending toward the ground;
//   - near the ground: "idle" while still and "walk" while moving, exactly like
//     a placed critter.
//
// Because it watches actual movement rather than incoming LookAts, it settles
// correctly when the peer stops moving and no further LookAts arrive.
type AvatarFlight struct {
	Node.Extension[AvatarFlight]

	body     Node3D.Instance
	player   AnimationPlayer.Instance
	heightAt func(Vector3.XYZ) Float.X // terrain height at a world XZ (Y ignored)

	last        Vector3.XYZ
	accum       Float.X
	flapPending Float.X // time a flap has been wanted while still gliding
	have        bool    // last position captured
	intent      string  // current clip intent ("" until first chosen)
}

const (
	// avatarFlightSample throttles the motion check (a few times a second).
	avatarFlightSample = Float.X(0.1)
	// avatarGroundAltitude is the height above the terrain below which the avatar
	// is treated as on the ground (idle/walk) rather than airborne (flap/glide).
	// Kept low so the avatar only grounds when the camera is right down by the
	// terrain; anything higher reads as flight.
	avatarGroundAltitude = Float.X(0.15)
	// avatarWalkSpeed is the horizontal world-units/second above which a grounded
	// avatar walks rather than idles. Kept well below the on-ground pace — which
	// is the (halved) first-person move speed, ~0.375 u/s when zoomed in — so
	// walking is detected, while a still avatar (~0) stays idle.
	avatarWalkSpeed = Float.X(0.1)
	// avatarGlideDescent is the downward world-units/second past which an airborne
	// avatar glides rather than flaps.
	avatarGlideDescent = Float.X(0.5)
	// glideToFlapDelay holds a glide for this long after the descent stops before
	// flapping, so easing the camera down slowly (descent dipping in and out under
	// the threshold) keeps the avatar gliding instead of flickering to a flap. Only
	// this transition is delayed — diving into a glide and landing stay immediate.
	glideToFlapDelay = Float.X(0.6)
)

func (a *AvatarFlight) Ready() {
	a.last = a.body.GlobalPosition()
	a.have = true
	a.setIntent(a.decide(0, 0, a.altitude(a.last))) // stationary until motion says otherwise
}

func (a *AvatarFlight) Process(delta Float.X) {
	a.accum += delta
	if a.accum < avatarFlightSample {
		return
	}
	pos := a.body.GlobalPosition()
	if !a.have {
		a.last, a.have = pos, true
		a.accum = 0
		return
	}
	// Horizontal speed governs walk/idle on the ground; vertical (descent) speed
	// governs flap/glide in the air.
	sample := a.accum
	horiz := Vector3.Distance(Vector3.XYZ{X: a.last.X, Z: a.last.Z}, Vector3.XYZ{X: pos.X, Z: pos.Z}) / sample
	descent := (a.last.Y - pos.Y) / sample // positive while moving down
	a.last = pos
	a.accum = 0
	a.applyIntent(a.decide(horiz, descent, a.altitude(pos)), sample)
}

// applyIntent commits the wanted intent, but delays only the glide→flap flip by
// glideToFlapDelay: while gliding, a flap must be wanted continuously for that
// long before it takes effect, so a slow descent that briefly dips under the
// threshold keeps gliding. Any other intent (or a renewed glide) applies at once
// and clears the pending timer.
func (a *AvatarFlight) applyIntent(want string, sample Float.X) {
	if a.intent == "gliding" && want == "flying" {
		a.flapPending += sample
		if a.flapPending < glideToFlapDelay {
			return
		}
	} else {
		a.flapPending = 0
	}
	a.setIntent(want)
}

// altitude is the avatar's height above the terrain at its XZ.
func (a *AvatarFlight) altitude(pos Vector3.XYZ) Float.X {
	return pos.Y - a.heightAt(Vector3.New(pos.X, 0, pos.Z))
}

// decide picks the clip intent from the avatar's motion and altitude: grounded
// → walk/idle, airborne → gliding while descending else flying.
func (a *AvatarFlight) decide(horiz, descent, altitude Float.X) string {
	if altitude <= avatarGroundAltitude {
		if horiz > avatarWalkSpeed {
			return "walk"
		}
		return "idle"
	}
	if descent > avatarGlideDescent {
		return "gliding"
	}
	return "flying"
}

// setIntent switches the clip when the intent changes. The 0.25s default blend
// in playCritterClip cross-fades the transition; gating on a change avoids
// re-asserting loop/speed every sample.
func (a *AvatarFlight) setIntent(intent string) {
	if a.intent == intent {
		return
	}
	a.intent = intent
	playCritterClip(a.body, a.player, intent)
}

// avatarGroundedOffset snaps an avatar target to the terrain surface when the
// camera is low enough to count as standing on the ground, so a grounded avatar
// sits on the surface instead of floating at the camera's eye height. Mirrors
// AvatarFlight's grounded test, and feeds the LookAt position tween (the single
// authority for the avatar's Y) so there is no per-frame fight over the height.
func avatarGroundedOffset(offset Vector3.XYZ, heightAt func(Vector3.XYZ) Float.X) Vector3.XYZ {
	ground := heightAt(Vector3.New(offset.X, 0, offset.Z))
	if offset.Y-ground <= avatarGroundAltitude {
		offset.Y = ground
	}
	return offset
}

// attachAvatarFlight wires an AvatarFlight onto a freshly instantiated avatar so
// it flaps/glides in the air and walks/idles on the ground. No-op if the model
// has no AnimationPlayer or already has a controller.
func attachAvatarFlight(avatar Node3D.Instance, heightAt func(Vector3.XYZ) Float.X) {
	if !avatar.AsNode().HasNode("AnimationPlayer") || avatar.AsNode().HasNode("AvatarFlight") {
		return
	}
	a := new(AvatarFlight)
	a.body = avatar
	a.player = Object.To[AnimationPlayer.Instance](avatar.AsNode().GetNode("AnimationPlayer"))
	a.heightAt = heightAt
	a.AsNode().SetName("AvatarFlight")
	avatar.AsNode().AddChild(a.AsNode())
}
