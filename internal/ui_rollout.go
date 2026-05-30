package internal

import (
	"graphics.gd/classdb/Control"
	"graphics.gd/classdb/PropertyTweener"
	"graphics.gd/variant/Float"
)

// Rollout drives a slide-in/out panel animation: the panel sits parked
// off-screen at its scene-authored position and tweens only its
// position.Y to 0 to reveal, then back to the parked Y to hide.
//
// Crucially it animates *only* the Y axis. The panels are anchored to the
// top-right of the screen, so their X is owned by Godot's anchor/offset
// layout; tweening the whole position (as this once did) pinned an
// absolute X captured at open-time, which went stale the moment the window
// resized — the panel drifted off its anchor and could slide off-screen.
// Leaving X untouched keeps it locked to the anchor at every window size.
//
// It backs both the editor switcher (EditorIndicator) and the Settings
// cog menu (UI). The parked Y is captured once on first open (not at
// construction) so it works regardless of when layout/scaling settles the
// panel; for a top-anchored panel that Y is its offset_top, constant
// across resizes.
//
// Rollouts that share screen space can be made mutually exclusive by
// listing each other in [Rollout.exclusive]; opening one then slides the
// others shut so only a single panel is ever revealed.
type Rollout struct {
	open      bool
	animating bool
	// parkedY is the panel's position.Y when closed, captured once on the
	// first open and reused thereafter; parkedSet guards that capture.
	parkedY   Float.X
	parkedSet bool
	// panel is captured on Toggle so CloseIfOpen can slide the same panel
	// shut when a mutually-exclusive peer opens.
	panel Control.Instance
	// exclusive lists peer rollouts to close when this one opens. The
	// Settings menu and the editor switcher roll out of the same top-right
	// corner, so only one should be open at a time.
	exclusive []*Rollout
	// icon, when set, spins one full turn each time the rollout opens or
	// closes (opposite directions) — the Settings cog and the editor
	// switcher's Arrows. iconSpin caches its resting rotation.
	icon     Control.Instance
	iconSpin spinState
}

// rolloutDuration is the slide tween length shared by every rollout.
const rolloutDuration = 0.2

// Toggle flips the rollout between open and closed, sliding panel's Y over
// rolloutDuration. Re-entrant calls while a tween is in flight are
// swallowed (returning false) so a rapid double-press can't desync the
// open/closed bookkeeping — and so the parked Y is only ever captured when
// the panel is fully settled. Opening first closes any mutually-exclusive
// peers. Returns true when a tween was started.
func (r *Rollout) Toggle(panel Control.Instance) bool {
	if r.animating || panel == Control.Nil {
		return false
	}
	r.panel = panel
	r.open = !r.open
	if r.open {
		// Reveal: close any mutually-exclusive peers first, capture the
		// parked Y once (the panel is settled here, guarded by !animating),
		// then slide the top edge to Y=0.
		for _, peer := range r.exclusive {
			peer.CloseIfOpen()
		}
		if !r.parkedSet {
			r.parkedY = panel.Position().Y
			r.parkedSet = true
		}
		r.spinIcon(1)
		r.animate(0)
	} else {
		r.spinIcon(-1)
		r.animate(r.parkedY)
	}
	return true
}

// spinIcon spins the rollout's icon (if any) one full turn about its center,
// screenDir +1 clockwise / -1 counter-clockwise, so the cog or arrows turn
// in step with the panel sliding out and back in.
func (r *Rollout) spinIcon(screenDir Float.X) {
	if r.icon != Control.Nil {
		spinFull(r.icon, &r.iconSpin, screenDir)
	}
}

// CloseIfOpen slides the rollout shut if it is currently open and settled.
// It enforces mutual exclusivity when a peer opens, and is a no-op when the
// rollout is already closed, mid-animation, or has never been opened.
func (r *Rollout) CloseIfOpen() {
	if !r.open || r.animating || r.panel == Control.Nil || !r.parkedSet {
		return
	}
	r.open = false
	r.spinIcon(-1)
	r.animate(r.parkedY)
}

// animate tweens only the captured panel's position.Y to targetY over
// rolloutDuration, clearing the animating flag once it settles. X is left
// to the anchor layout so it stays put across window resizes.
func (r *Rollout) animate(targetY Float.X) {
	r.animating = true
	PropertyTweener.Make(r.panel.AsNode().CreateTween(), r.panel.AsObject(), "position:y", targetY, rolloutDuration).
		AsTweener().OnFinished(func() {
		r.animating = false
	})
}

// Animating reports whether a slide tween is currently in flight, for
// callers that gate other interactions on the rollout settling.
func (r *Rollout) Animating() bool { return r.animating }

// Open reports whether the rollout is currently revealed.
func (r *Rollout) Open() bool { return r.open }
