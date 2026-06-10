package internal

import (
	"math"

	"graphics.gd/classdb/Animation"
	"graphics.gd/classdb/AnimationPlayer"
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Vector3"
	"the.quetzal.community/aviary/internal/musical"
)

type ActionRenderer struct {
	Node.Extension[ActionRenderer]

	Initial Vector3.XYZ
	// pathStart is the first waypoint of the current path — captured when its first
	// segment is queued, BEFORE the linear walk mutates Initial. processRepeat needs
	// it so a ping-pong loop built over several clicks (while the critter is already
	// walking) turns around at its true start, not at a mid-path point.
	pathStart Vector3.XYZ

	playing string
	current int
	actions []musical.Action

	client *Client

	CurrentUp      Vector3.XYZ
	CurrentForward Vector3.XYZ
}

func (ar *ActionRenderer) Ready() {
	ar.playing = "Idle"
	ar.AsNode().SetProcess(false)
}

func (ar *ActionRenderer) Add(action musical.Action) {
	if len(ar.actions) > 0 {
		if action.Timing < ar.actions[len(ar.actions)-1].Timing {
			return
		}
		if action.Cancel {
			previous := ar.actions[ar.current]
			ar.Initial = Vector3.Lerp(ar.Initial, previous.Target, ar.progress(previous))
			ar.actions = ar.actions[0:0:cap(ar.actions)]
			ar.current = 0
		}
	}
	if len(ar.actions) == 0 {
		// First segment of a new path (queue empty, or a Cancel just cleared it):
		// remember where it starts. The linear walk advances ar.Initial segment by
		// segment, so this is the only record of the path's true first waypoint.
		ar.pathStart = ar.Initial
	}
	ar.actions = append(ar.actions, action)
	ar.AsNode().SetProcess(true)
}

// progress is how far the synced clock has advanced through action's walk,
// CLAMPED to [0,1]. The clamp is load-bearing on replay/join: Timing and Period
// are absolute, so a critter with a long edit history folds dozens of
// long-completed actions into Initial (the Lerp in Add) whose raw
// (Now-Timing)/Period is far above 1, and a just-joined client whose clock hasn't
// synced reads it below 0. Unclamped, both this and the Process interpolation
// extrapolate — compounding to astronomical/NaN positions, so the model vanishes
// off-world and look_at() fails with "origin and target in the same position"
// until time finally passes Timing+Period and it snaps to the destination.
// Period 0 is a zero-length move (right-clicking the critter's own spot); treat
// it as already complete to avoid the divide-by-zero.
func (ar *ActionRenderer) progress(action musical.Action) Float.X {
	if action.Period <= 0 {
		return 1
	}
	t := Float.X(ar.client.time.Now()-action.Timing) / Float.X(action.Period)
	if t < 0 {
		return 0
	}
	if t > 1 {
		return 1
	}
	return t
}

// cancel drops any queued/active walk and returns the model to Idle. Called when
// a newer manual move (a gizmo/placement Change with a later timestamp) supersedes
// the walk, so Process stops dragging the entity toward a now-stale target.
func (ar *ActionRenderer) cancel() {
	ar.actions = ar.actions[0:0:cap(ar.actions)]
	ar.current = 0
	ar.AsNode().SetProcess(false)
	ar.play("Idle")
}

func (ar *ActionRenderer) play(name string) {
	parent := Object.To[Node3D.Instance](ar.AsNode().GetParent())
	if !parent.AsNode().HasNode("AnimationPlayer") {
		ar.playing = name // procedural critter (no clip) — nothing to drive
		return
	}
	player := Object.To[AnimationPlayer.Instance](parent.AsNode().GetNode("AnimationPlayer"))
	// Key off the player's REAL state, not a cached flag. On reload the renderer's
	// first play("Walk") can land while the model is still streaming in during the
	// load drain, so the clip never actually starts; a cached `playing == name`
	// would then latch forever and the bear slides to its target un-animated. Re-
	// asserting whenever the player isn't already looping this clip self-heals once
	// the AnimationPlayer is live, and is a no-op while it is (looped → IsPlaying).
	if player.IsPlaying() && player.CurrentAnimation() == name {
		ar.playing = name
		return
	}
	mixer := player.AsAnimationMixer()
	if !mixer.HasAnimation(name) {
		return
	}
	mixer.GetAnimation(name).SetLoopMode(Animation.LoopLinear)
	player.PlayNamed(name)
	ar.playing = name
}

// actionRendererFor returns the entity object's ActionRenderer if one is
// attached (i.e. the entity has been commanded to move at least once).
func actionRendererFor(object Node3D.Instance) (*ActionRenderer, bool) {
	if object.AsNode().HasNode("ActionRenderer") {
		return Object.To[*ActionRenderer](object.AsNode().GetNode("ActionRenderer")), true
	}
	return nil, false
}

// PathTail reports where and when the entity's currently queued walk path ends —
// the last segment's Target and its finish time — so a Shift/Ctrl-click can chain
// a new segment onto the END of the path instead of restarting from the model's
// current position. active is false when no path is queued (renderer idle/cleared).
func (ar *ActionRenderer) PathTail() (pos Vector3.XYZ, endTime musical.Timing, active bool) {
	if len(ar.actions) == 0 {
		return Vector3.Zero, 0, false
	}
	last := ar.actions[len(ar.actions)-1]
	return last.Target, last.Timing + musical.Timing(last.Period), true
}

// swivelPause is how long the critter stops and turns on the spot at each
// ping-pong turnaround (Ctrl-click loop) before walking back — 0.5s in ns. A walk
// period is distance*5s, so this reads as a brief deliberate about-face.
const swivelPause = musical.Timing(500_000_000)

// processRepeat drives a ping-pong loop path (Ctrl-click): the queued segments are
// walked forward, a swivel-in-place at the far end, back, a swivel at the near end,
// forever. Position/heading are derived purely from elapsed synced time modulo the
// round-trip duration, so every client — and any reload — reconstructs the same
// state with no per-frame memory. ar.Initial is the loop's first waypoint (folded
// from any pre-loop actions by Add's Cancel branch).
func (ar *ActionRenderer) processRepeat(delta Float.X) {
	n := len(ar.actions)
	// Waypoints W[0..n] = the path's true start then each segment Target; periods[i]
	// is leg i. pathStart (not Initial) is W[0]: Initial drifts along the path as the
	// linear walk advances, which would collapse W[0] onto W[1] and kill the near-end
	// swivel (zero-length turn). See ActionRenderer.pathStart.
	way := make([]Vector3.XYZ, n+1)
	way[0] = ar.pathStart
	periods := make([]musical.Timing, n)
	var total musical.Timing
	for i := 0; i < n; i++ {
		way[i+1] = ar.actions[i].Target
		p := musical.Timing(ar.actions[i].Period)
		if p < 0 {
			p = 0
		}
		periods[i] = p
		total += p
	}
	if total <= 0 {
		return
	}
	// One round trip: forward legs, swivel at the far end, reversed legs, swivel at
	// the near end. A turn stage holds its waypoint and rotates the heading 180°.
	type stage struct {
		turn     bool
		from, to Vector3.XYZ // move endpoints; the held spot for a turn
		inDir    Vector3.XYZ // turn only: incoming heading to swivel away from
		period   musical.Timing
	}
	stages := make([]stage, 0, 2*n+2)
	for i := 0; i < n; i++ {
		stages = append(stages, stage{from: way[i], to: way[i+1], period: periods[i]})
	}
	stages = append(stages, stage{turn: true, from: way[n], to: way[n], inDir: flatDir(way[n-1], way[n]), period: swivelPause})
	for i := n - 1; i >= 0; i-- {
		stages = append(stages, stage{from: way[i+1], to: way[i], period: periods[i]})
	}
	stages = append(stages, stage{turn: true, from: way[0], to: way[0], inDir: flatDir(way[1], way[0]), period: swivelPause})

	cycleDur := 2*total + 2*swivelPause
	elapsed := ar.client.time.Now() - ar.actions[0].Timing
	if elapsed < 0 {
		elapsed = 0
	}
	phase := elapsed % cycleDur
	if phase < 0 {
		phase += cycleDur
	}
	// Walk the stages accumulating durations until phase lands inside one.
	chosen := stages[len(stages)-1]
	var base musical.Timing
	for _, s := range stages {
		if phase < base+s.period {
			chosen = s
			break
		}
		base += s.period
	}
	frac := Float.X(0)
	if chosen.period > 0 {
		frac = Float.X(phase-base) / Float.X(chosen.period)
		if frac < 0 {
			frac = 0
		} else if frac > 1 {
			frac = 1
		}
	}
	parent := Object.To[Node3D.Instance](ar.AsNode().GetParent())
	var pos, dir Vector3.XYZ
	if chosen.turn {
		// Keep the walk clip running through the swivel: the model is still
		// "stepping" as it rotates on the spot — dropping to Idle made it
		// glide around the turn with frozen legs.
		ar.play("Walk")
		pos = chosen.from
		// Rotate the incoming heading 180° around world-up across the pause: starts
		// matching the arriving walk, ends matching the departing one, so OrientModel
		// sweeps the model around instead of jumping.
		dir = rotateY(chosen.inDir, math.Pi*float64(frac))
	} else {
		ar.play("Walk")
		pos = Vector3.Lerp(chosen.from, chosen.to, frac)
		dir = Vector3.Sub(chosen.to, chosen.from)
	}
	dir.Y = 0
	pos.Y = ar.client.TerrainEditor.HeightAt(pos)
	parent.SetPosition(pos)
	ar.OrientModel(parent, pos, dir, ar.client.TerrainEditor.NormalAt(pos), delta)
}

// flatDir is the horizontal (Y-zeroed) vector from a to b.
func flatDir(a, b Vector3.XYZ) Vector3.XYZ {
	d := Vector3.Sub(b, a)
	d.Y = 0
	return d
}

// rotateY rotates a vector by ang radians around the world-up (Y) axis.
func rotateY(v Vector3.XYZ, ang float64) Vector3.XYZ {
	c, s := Float.X(math.Cos(ang)), Float.X(math.Sin(ang))
	return Vector3.XYZ{X: v.X*c + v.Z*s, Y: v.Y, Z: -v.X*s + v.Z*c}
}

func (ar *ActionRenderer) Process(delta Float.X) {
	// A path whose final segment is flagged Repeat (Ctrl-click) loops back and
	// forth forever rather than completing — handled separately so the linear
	// queue walk below doesn't run off the end and stop.
	if n := len(ar.actions); n > 0 && ar.actions[n-1].Repeat {
		ar.processRepeat(delta)
		return
	}
	action := ar.actions[ar.current]
	parent := Object.To[Node3D.Instance](ar.AsNode().GetParent())
	for ar.client.time.Now()-action.Timing >= musical.Timing(action.Period) {
		pos := action.Target
		pos.Y = ar.client.TerrainEditor.HeightAt(pos)
		parent.SetPosition(pos)
		ar.Initial = action.Target
		ar.current++
		if ar.current >= len(ar.actions) {
			ar.AsNode().SetProcess(false)
			ar.play("Idle")
			ar.actions = ar.actions[0:0:cap(ar.actions)]
			ar.current = 0
			return
		}
		action = ar.actions[ar.current]
	}
	ar.play("Walk")
	dir := Vector3.Sub(action.Target, ar.Initial)
	dir.Y = 0
	pos := Vector3.Lerp(ar.Initial, action.Target, ar.progress(action))
	pos.Y = ar.client.TerrainEditor.HeightAt(pos)
	parent.SetPosition(pos)
	ar.OrientModel(parent, pos, dir, ar.client.TerrainEditor.NormalAt(pos), delta)
}

// OrientModel aligns the model's up direction with the terrain normal while preserving the facing direction based on movement.
func (ar *ActionRenderer) OrientModel(model Node3D.Instance, pos Vector3.XYZ, movementDir Vector3.XYZ, normal Vector3.XYZ, delta Float.X) {
	// Normalize the normal to get the target up direction
	targetUp := Vector3.Normalized(normal)
	if Vector3.LengthSquared(targetUp) == 0 {
		targetUp = Vector3.XYZ{Y: 1}
	}

	// Smoothly interpolate the current up towards the target up
	if Vector3.LengthSquared(ar.CurrentUp) == 0 {
		ar.CurrentUp = Vector3.XYZ{Y: 1}
	}
	ar.CurrentUp = Vector3.Lerp(ar.CurrentUp, targetUp, Float.X(12)*delta)
	ar.CurrentUp = Vector3.Normalized(ar.CurrentUp)

	// Drop the movement direction onto the tangent plane for target forward.
	// Shear it vertically (solve f.Y so f·up = 0) rather than projecting
	// orthogonally: both land in the plane, but the shear preserves the
	// horizontal heading exactly, while orthogonal projection skews the yaw
	// toward the slope's contour line — on steep terrain the model banks
	// correctly but visibly faces away from its direction of travel. On
	// near-vertical terrain (up.Y ~ 0) the shear blows up, so fall back to
	// the orthogonal projection there.
	proj := Vector3.Dot(movementDir, targetUp)
	var targetProjectedForward Vector3.XYZ
	if targetUp.Y > 0.2 {
		targetProjectedForward = Vector3.Sub(movementDir, Vector3.XYZ{Y: proj / targetUp.Y})
	} else {
		targetProjectedForward = Vector3.Sub(movementDir, Vector3.MulX(targetUp, proj))
	}
	targetProjectedForwardLengthSq := Vector3.LengthSquared(targetProjectedForward)

	if targetProjectedForwardLengthSq > 0 {
		targetProjectedForward = Vector3.Normalized(targetProjectedForward)
	} else {
		// Fallback: Use an arbitrary direction in the tangent plane
		var arbitrary Vector3.XYZ
		ux := math.Abs(float64(targetUp.X))
		uy := math.Abs(float64(targetUp.Y))
		uz := math.Abs(float64(targetUp.Z))
		min := math.Min(ux, math.Min(uy, uz))
		if ux == min {
			arbitrary = Vector3.XYZ{X: 1}
		} else if uy == min {
			arbitrary = Vector3.XYZ{Y: 1}
		} else {
			arbitrary = Vector3.XYZ{Z: 1}
		}
		perp := Vector3.Cross(targetUp, arbitrary)
		perpLengthSq := Vector3.LengthSquared(perp)
		if perpLengthSq > 0 {
			targetProjectedForward = Vector3.Normalized(perp)
		} else {
			// Extremely rare fallback
			targetProjectedForward = Vector3.XYZ{X: 1}
		}
	}

	// Smoothly interpolate the current forward towards the target forward
	if Vector3.LengthSquared(ar.CurrentForward) == 0 {
		ar.CurrentForward = Vector3.XYZ{Z: 1} // Assume default forward is +Z
	}
	if Vector3.Dot(Vector3.Normalized(ar.CurrentForward), targetProjectedForward) < -0.7 {
		// Near-180° reversal — e.g. a ping-pong loop's turnaround. Lerping the
		// forward vector toward its near-opposite collapses it through ~zero, so the
		// model moonwalks (faces the old way while moving the new) and then flips
		// over a horizontal axis as it crosses. Snap to the new facing instead: an
		// instant about-face. Gentler turns still interpolate smoothly below.
		ar.CurrentForward = targetProjectedForward
	} else {
		ar.CurrentForward = Vector3.Lerp(ar.CurrentForward, targetProjectedForward, Float.X(12)*delta)
		ar.CurrentForward = Vector3.Normalized(ar.CurrentForward)
	}

	// CurrentForward can normalise to zero (an antiparallel lerp passing through
	// the midpoint, or a non-finite position slipping through upstream); look_at()
	// would then see target == position and log "origin and target are in the same
	// position". Skip the reorient this frame and keep the prior facing — clamped
	// progress should keep positions finite, but this guards the final call too.
	if Vector3.LengthSquared(ar.CurrentForward) == 0 {
		return
	}
	// Use LookAt to set the orientation (assumes model faces +Z locally to fix backwards walking)
	globalPos := model.GlobalPosition()
	target := Vector3.Add(globalPos, ar.CurrentForward)
	model.MoreArgs().LookAt(target, ar.CurrentUp, true)
}
