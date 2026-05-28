package internal

import (
	"graphics.gd/classdb/Control"
	"graphics.gd/classdb/PropertyTweener"
	"graphics.gd/variant/Vector2"
)

// Rollout drives a slide-in/out panel animation: the panel sits parked
// off-screen at its scene-authored position and tweens its position.Y
// to 0 to reveal, then back to the parked position to hide. It captures
// the parked position on first open (rather than at construction) so it
// works regardless of when layout/scaling settles the panel.
//
// It backs both the editor switcher (EditorIndicator) and the Settings
// cog menu (UI), which previously carried three parallel state fields
// and a copy of the open/close tween each.
type Rollout struct {
	open      bool
	animating bool
	closedPos Vector2.XY
}

// rolloutDuration is the slide tween length shared by every rollout.
const rolloutDuration = 0.2

// Toggle flips the rollout between open and closed, tweening panel over
// rolloutDuration. Re-entrant calls while a tween is in flight are
// swallowed (returning false) so a rapid double-press can't desync the
// open/closed bookkeeping. Returns true when a tween was started.
func (r *Rollout) Toggle(panel Control.Instance) bool {
	if r.animating || panel == Control.Nil {
		return false
	}
	r.animating = true
	r.open = !r.open
	target := r.closedPos
	if r.open {
		// Capture the parked position on the way out so we can return to
		// it on close; reveal by sliding the top edge to Y=0.
		r.closedPos = panel.Position()
		target = r.closedPos
		target.Y = 0
	}
	PropertyTweener.Make(panel.AsNode().CreateTween(), panel.AsObject(), "position", target, rolloutDuration).
		AsTweener().OnFinished(func() {
		r.animating = false
	})
	return true
}

// Animating reports whether a slide tween is currently in flight, for
// callers that gate other interactions on the rollout settling.
func (r *Rollout) Animating() bool { return r.animating }

// Open reports whether the rollout is currently revealed.
func (r *Rollout) Open() bool { return r.open }
