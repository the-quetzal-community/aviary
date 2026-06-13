package internal

import (
	"math"
	"strings"

	"graphics.gd/classdb/Animation"
	"graphics.gd/classdb/AnimationPlayer"
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/variant/Float"
)

// The "everything" library critters don't share a clip vocabulary. The black
// bear (our baseline) has "Walk"/"Idle"; the snake, worm and rattlesnake move
// with "Side winding" and have no "Walk" at all; the standing bear uses
// "Walk_Formal_Loop"/"Idle_Loop"; the lion ships both "Walk" and "Run". So a
// hard-coded play("Walk") drives the bear but leaves the snake sliding
// un-animated. resolveCritterClip maps a movement intent ("walk"/"idle") onto
// whichever clip a given model actually carries, by ordered keyword preference
// (earlier keywords win, so a lion picks "Walk" over "Run").
var critterClipKeywords = map[string][]string{
	"walk": {"walk", "wind", "slither", "trot", "run", "swim", "fly", "flap", "glide", "crawl", "move"},
	// "glide" is the idle for marine mammals (seal/sea lion/walrus), whose only
	// clips are "Fly Flap"/"Fly Glide": flapping is their movement, gliding their
	// rest. Without it idle falls through to the first clip ("Fly Flap") and they
	// flap on the spot forever. It's last so any real "Idle" still wins.
	"idle": {"idle", "coiled", "rest", "stand", "breath", "glide"},
	// "place" is the placement-preview pose: a critter dropped into the world
	// falls in (or sits), so prefer "Fall" then "Sit"; anything without them
	// falls back to idle below.
	"place": {"fall", "sit"},
	// Flying avatars (the everything "avatar" birds/bats/insects) use these wing
	// clips while airborne: "flying" flaps to hold station/climb/cruise level,
	// "gliding" coasts while descending. Kept distinct from walk/idle (and not
	// matching either) so the resolver can't pick a ground clip for an airborne
	// avatar; when the avatar is down near the terrain it uses the shared
	// walk/idle instead. See AvatarFlight, which drives the switch from the
	// avatar's motion and height above the ground.
	"flying":  {"flap", "fly", "flutter", "wing"},
	"gliding": {"glide", "soar"},
	// "jump" is a one-shot triggered while possessing an entity (GizmoEnter): a
	// model only offers a jump when it actually carries one of these clips —
	// hasJumpClip gates the spacebar leap on a real match (never the idle
	// fallback), so a critter with no jump animation simply can't jump.
	"jump": {"jump", "leap", "hop"},
	// Swimmers (the everything "swimmer" fish/marine rig) carry a distinct
	// vocabulary — "Swim Horizontal", "Swim Vertical", "Idle", "Dead Floating"
	// (plus "Bite"/"Hit"). The two swim clips are picked by the dominant motion
	// axis (see EntityAnimator / updateSwimPossess); "death" is the frozen
	// belly-up pose shown when a swimmer ends up out of water. Kept off the
	// generic walk/idle intents so a fish never resolves a swim clip for a
	// ground "walk" by accident (and vice-versa). "horizontal"/"vertical" come
	// before the bare "swim" fallback so the right clip wins for models that name
	// both, while a model with only a generic "Swim" still resolves.
	swimClipHorizontal: {"swim horizontal", "horizontal", "swim"},
	swimClipVertical:   {"swim vertical", "vertical"},
	swimClipDeath:      {"dead floating", "dead", "death", "belly"},
}

// Swimmer clip intents (resolved via critterClipKeywords above). Named constants
// because they're referenced from several files (the animator, the renderer and
// possession) and a typo'd intent silently falls back to idle.
const (
	swimClipHorizontal = "swim_horizontal"
	swimClipVertical   = "swim_vertical"
	swimClipDeath      = "death"
)

func resolveCritterClip(player AnimationPlayer.Instance, intent string) (string, bool) {
	names := player.AsAnimationMixer().GetAnimationList()
	// Fast path: a model already using the canonical name.
	for _, n := range names {
		if strings.EqualFold(n, intent) {
			return n, true
		}
	}
	for _, kw := range critterClipKeywords[intent] {
		for _, n := range names {
			if strings.Contains(strings.ToLower(n), kw) {
				return n, true
			}
		}
	}
	// A non-idle intent with no matching clip falls back to idle (a snake with no
	// "Walk" still breathes; a placement preview with no "Fall"/"Sit" stands);
	// idle itself then falls back to the first clip so the model isn't frozen.
	if intent != "idle" {
		if n, ok := resolveCritterClip(player, "idle"); ok {
			return n, ok
		}
	}
	if len(names) > 0 {
		return names[0], true
	}
	return "", false
}

// Every critter walks the world at the same metres-per-second, so a model
// smaller than the size its walk clip was authored for must cycle its legs
// faster (and a larger one slower) or its feet skate. critterAnimSpeed scales
// the player's speed by the model's height relative to that authored size.
//
// But the clips come from Mesh2Motion's fixed rig set, and each rig authors its
// stride for a DIFFERENT body — the fox/quadruped rig around the black bear, the
// bird rig around a duck — so there is no single baseline: scaling a duck
// against the bear made it walk wrong. The reference height is therefore
// per-rig, keyed by Skeleton3D bone count (which uniquely identifies the
// Mesh2Motion rig). A rig with no calibrated entry gets no scaling, i.e. its
// clip plays at the authored speed — add its reference height once validated.
//
// Mesh2Motion rig bone counts: fox 48, bird 55, dragon 99, spider 56, snake 28,
// kaiju 57, human 66, shark/fish 33.
var rigReferenceHeight = map[int]Float.X{
	48: 0.42, // fox / quadruped rig — calibrated to the black bear
	55: 0.05, // bird rig — calibrated to the peking duck
	57: 10,
	99: 0.5,
}

// snakeRigBoneCount identifies the Mesh2Motion snake rig (snake / worm /
// rattlesnake). Their "Side winding" locomotion travels at an angle to the body,
// so their movement facing is offset (see snakeMovementYaw / OrientModel).
const snakeRigBoneCount = 28

// isSnakeRig reports whether node carries the snake rig, used to apply the
// side-winding facing offset only to snakes.
func isSnakeRig(node Node.Instance) bool {
	skel, ok := findSkeleton(node)
	return ok && skel.GetBoneCount() == snakeRigBoneCount
}

// isSwimmerCategory reports whether a library category is the swimmer (fish /
// marine) dressing category — the authoritative source of truth used by the
// placement, move-command and possession paths, which all have the design
// category in hand.
func isSwimmerCategory(category string) bool { return category == "swimmer" }

// isSwimmerModel reports whether an AnimationPlayer carries the swimmer clip
// vocabulary (both a horizontal and a vertical swim clip). The per-entity
// systems (ActionRenderer, EntityAnimator) detect a swimmer from category at
// attach time; this is the model-level fallback for paths that only have the
// player to hand.
func isSwimmerModel(player AnimationPlayer.Instance) bool {
	var horizontal, vertical bool
	for _, name := range player.AsAnimationMixer().GetAnimationList() {
		lower := strings.ToLower(name)
		if strings.Contains(lower, "swim horizontal") || strings.Contains(lower, "horizontal") {
			horizontal = true
		}
		if strings.Contains(lower, "swim vertical") || strings.Contains(lower, "vertical") {
			vertical = true
		}
	}
	return horizontal && vertical
}

const (
	critterAnimSpeedExp = 0.5 // sqrt of the size ratio (1.0 = linear)
	// Wide guard rails, not a tuning knob: they only catch a degenerate height
	// (≈0) running speed to infinity. Keep them loose enough that the per-rig
	// reference height is what actually controls the speed — a tight clamp here
	// silently pins every "extreme" reference to the ceiling, so changing the
	// reference appears to do nothing.
	critterAnimSpeedMin = Float.X(0.1)
	critterAnimSpeedMax = Float.X(8.0)

	// Cross-fade time (seconds) between clips. PlayNamed passes custom_blend=-1,
	// which means "use the player's default blend time", so setting this makes
	// every Idle↔Walk switch blend instead of hard-cutting. 0 restores the cut.
	critterAnimBlend = Float.X(0.25)

	// Idle slows for large creatures so an elephant or giraffe reads as
	// ponderous rather than twitchy. Models up to idleRefHeight idle at the
	// authored rate; taller ones slow toward idleSpeedMin. Idle is never sped up
	// (small critters keep 1.0). Size-based, not per-rig — idle breathing doesn't
	// depend on the rig's authored stride the way walk cadence does.
	idleRefHeight = Float.X(0.5) // in-world height (m) below which idle stays full speed
	idleSpeedExp  = 1.0          // 1.0 = linear in the size ratio
	idleSpeedMin  = Float.X(0.4) // floor so the largest creatures don't freeze
)

// critterClipSpeed is the playback speed for a clip of the given intent: the
// size-corrected walk speed for "walk", and 1 (authored speed) for idle or
// anything else. Only locomotion needs its cadence matched to the ground speed;
// scaling idle made small critters fidget too fast and large ones too slow.
func critterClipSpeed(node Node.Instance, intent string) Float.X {
	switch intent {
	case "walk":
		return critterAnimSpeed(node)
	case "idle":
		return critterIdleSpeed(node)
	default:
		return 1
	}
}

// critterIdleSpeed slows the idle clip for creatures taller than idleRefHeight
// (elephant/giraffe/mammoth), clamped to [idleSpeedMin, 1.0] so smaller critters
// idle at the authored rate and the largest don't freeze.
func critterIdleSpeed(node Node.Instance) Float.X {
	lo, hi, ok := worldVisualYRange(node)
	h := hi - lo
	if !ok || h <= idleRefHeight {
		return 1
	}
	return max(Float.X(math.Pow(float64(idleRefHeight/h), idleSpeedExp)), idleSpeedMin)
}

func critterAnimSpeed(node Node.Instance) Float.X {
	skel, ok := findSkeleton(node)
	if !ok {
		return 1
	}
	ref, ok := rigReferenceHeight[skel.GetBoneCount()]
	if !ok {
		return 1 // uncalibrated rig — leave the clip at its authored speed
	}
	lo, hi, ok := worldVisualYRange(node)
	h := hi - lo
	if !ok || h <= 0 {
		return 1
	}
	s := Float.X(math.Pow(float64(ref/h), critterAnimSpeedExp))
	return min(max(s, critterAnimSpeedMin), critterAnimSpeedMax)
}

// playCritterClip resolves intent to a real clip for this model, loops it, sets
// the size-appropriate playback speed, and plays it. No-op if the model has no
// matching clip. Shared by the placement/reload paths; ActionRenderer.play adds
// its own re-assert-on-reload guard but uses the same resolver and speed.
func playCritterClip(node Node3D.Instance, player AnimationPlayer.Instance, intent string) {
	clip, ok := resolveCritterClip(player, intent)
	if !ok {
		return
	}
	// The death pose (a swimmer stranded out of water) plays once and HOLDS its
	// last frame — a frozen belly-up "Dead Floating" — rather than looping. The
	// callers that drive it (EntityAnimator) only re-issue a clip on an intent
	// CHANGE, so a non-looping death clip stays settled on the dead pose. Every
	// other clip loops.
	loop := Animation.LoopLinear
	if intent == swimClipDeath {
		loop = Animation.LoopNone
	}
	player.AsAnimationMixer().GetAnimation(clip).SetLoopMode(loop)
	player.SetSpeedScale(critterClipSpeed(node.AsNode(), intent))
	player.SetPlaybackDefaultBlendTime(critterAnimBlend)
	player.PlayNamed(clip)
}
