package internal

import (
	"math"
	"strings"

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

	// snake caches whether this entity uses the snake rig (lazily, on first
	// orient), so its side-winding movement facing gets the snakeMovementYaw
	// offset without re-walking the skeleton every frame.
	snakeChecked bool
	snake        bool

	// swimmer is set once when the renderer is created (from the design category):
	// a fish moves through the full 3D water column — it lerps straight to the
	// action target (Y included, no terrain snap), pitches toward the 3D heading,
	// and leaves the swim clip to its EntityAnimator rather than driving Walk/Idle.
	swimmer bool
}

// snakeMovementYaw rotates a snake's facing while it moves: the "Side winding"
// clip travels at ~45° to the body, so the body is angled off the straight-line
// path. Negative = clockwise viewed from above (flip the sign to turn the other
// way). Applied in OrientModel for snake-rig models only.
const snakeMovementYaw = Float.X(-math.Pi / 4)

// swimTargetAbs reconstructs a swimmer action target's absolute world position
// from its stored, terrain-relative form: a swimmer's Action.Target.Y is the
// depth ABOVE the seabed at Target.XZ (not an absolute Y), so the fish's resting
// depth rides terrain edits and reload, and a dropping water level can strand it
// above the surface. Ground-walker targets aren't stored this way (their Y is
// re-snapped to the surface every frame), so this is only used on the swimmer
// path.
func swimTargetAbs(te *TerrainEditor, t Vector3.XYZ) Vector3.XYZ {
	t.Y = te.HeightAt(Vector3.New(t.X, 0, t.Z)) + t.Y
	return t
}

// recordSwimmerRest stores a rested swimmer's seabed-relative depth as its
// float delta (Client.entity_float_delta) so it re-seats against the FINAL
// terrain on reload — the Action counterpart to the float-Change bookkeeping in
// musicalImpl.Change. depth is the segment's Target.Y, already the height ABOVE
// the seabed at the resting XZ. Runs on every client (the ActionRenderer replays
// the same action everywhere) and during the bulk reload, so the rested depth is
// observable with no extra mutation. No-op if the entity isn't registered.
func (ar *ActionRenderer) recordSwimmerRest(parent Node3D.Instance, depth Float.X) {
	if ar.client == nil {
		return
	}
	if entity, ok := ar.client.object_to_entity[Node3D.ID(parent.ID())]; ok {
		ar.client.entity_float_delta[entity] = depth
	}
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
			// Fold the current interpolated spot into Initial. A swimmer's target is
			// seabed-relative, so reconstruct its absolute Y first — Initial is kept in
			// absolute world space (the lerp endpoints and pathStart all are).
			prevTarget := previous.Target
			if ar.swimmer {
				prevTarget = swimTargetAbs(ar.client.TerrainEditor, previous.Target)
			}
			ar.Initial = Vector3.Lerp(ar.Initial, prevTarget, ar.progress(previous))
			ar.actions = ar.actions[0:0:cap(ar.actions)]
			ar.current = 0
		}
	}
	if len(ar.actions) == 0 {
		// First segment of a new path (queue empty, or a Cancel just cleared it):
		// remember where it starts. The linear walk advances ar.Initial segment by
		// segment, so this is the only record of the path's true first waypoint.
		ar.pathStart = ar.Initial
		if ar.swimmer {
			// Store a swimmer's loop start TERRAIN-RELATIVE (depth above the seabed),
			// like the segment Targets, so processRepeat reconstructs it against the
			// CURRENT terrain via swimTargetAbs. Captured from the live position alone
			// it's unreliable on reload — the bulk replay seeds Initial against the
			// flat deferred heightfield (HeightAt==0), so an absolute pathStart left the
			// start half of a ping-pong loop floating above the water. Subtracting
			// HeightAt here cancels with the add in swimTargetAbs when the terrain is
			// unchanged and rides the seabed when it isn't.
			ar.pathStart.Y = ar.Initial.Y - ar.client.TerrainEditor.HeightAt(Vector3.New(ar.Initial.X, 0, ar.Initial.Z))
		}
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
	// Leave the locomotion clip to an EntityAnimator when one is attached: it
	// reconstructs walk/idle from the entity's motion, so forcing "Idle" here
	// would fight it. During a GizmoEnter possession every move calls cancel()
	// (the future-stamped moves supersede this path), which otherwise stomped the
	// player back to Idle ~10×/s — the animator, having already latched "walk",
	// never re-asserted, so the possessed critter looked frozen on peers. Without
	// an animator (legacy / non-mobile) keep the old "finished walking → Idle".
	if parent := Object.To[Node3D.Instance](ar.AsNode().GetParent()); parent != Node3D.Nil &&
		parent.AsNode().HasNode("EntityAnimator") {
		return
	}
	ar.play("Idle")
}

func (ar *ActionRenderer) play(name string) {
	parent := Object.To[Node3D.Instance](ar.AsNode().GetParent())
	if !parent.AsNode().HasNode("AnimationPlayer") {
		ar.playing = name // procedural critter (no clip) — nothing to drive
		return
	}
	player := Object.To[AnimationPlayer.Instance](parent.AsNode().GetNode("AnimationPlayer"))
	// name is an intent ("Walk"/"Idle"); resolve it to whatever clip THIS model
	// actually ships (a snake's "Walk" is "Side winding"). No clip → nothing to do.
	intent := strings.ToLower(name)
	clip, ok := resolveCritterClip(player, intent)
	if !ok {
		return
	}
	// Key off the player's REAL state, not a cached flag. On reload the renderer's
	// first play("Walk") can land while the model is still streaming in during the
	// load drain, so the clip never actually starts; a cached `playing == name`
	// would then latch forever and the bear slides to its target un-animated. Re-
	// asserting whenever the player isn't already looping this clip self-heals once
	// the AnimationPlayer is live, and is a no-op while it is (looped → IsPlaying).
	if player.IsPlaying() && player.CurrentAnimation() == clip {
		ar.playing = name
		return
	}
	mixer := player.AsAnimationMixer()
	mixer.GetAnimation(clip).SetLoopMode(Animation.LoopLinear)
	player.SetSpeedScale(critterClipSpeed(parent.AsNode(), intent))
	player.SetPlaybackDefaultBlendTime(critterAnimBlend)
	player.PlayNamed(clip)
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
	if ar.swimmer {
		// pathStart was stored seabed-relative (see Add) so it rides terrain like the
		// targets — reconstruct its absolute Y against the current seabed.
		way[0] = swimTargetAbs(ar.client.TerrainEditor, ar.pathStart)
	}
	periods := make([]musical.Timing, n)
	var total musical.Timing
	for i := 0; i < n; i++ {
		// Waypoints are absolute world positions; a swimmer's target stores a
		// seabed-relative depth, so reconstruct it (pathStart handled above).
		if ar.swimmer {
			way[i+1] = swimTargetAbs(ar.client.TerrainEditor, ar.actions[i].Target)
		} else {
			way[i+1] = ar.actions[i].Target
		}
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
		// glide around the turn with frozen legs. (Swimmers leave the clip to
		// their EntityAnimator, so skip play for them.)
		if !ar.swimmer {
			ar.play("Walk")
		}
		pos = chosen.from
		// Rotate the incoming heading 180° around world-up across the pause: starts
		// matching the arriving walk, ends matching the departing one, so OrientModel
		// sweeps the model around instead of jumping.
		dir = rotateY(chosen.inDir, math.Pi*float64(frac))
	} else {
		if !ar.swimmer {
			ar.play("Walk")
		}
		pos = Vector3.Lerp(chosen.from, chosen.to, frac)
		dir = Vector3.Sub(chosen.to, chosen.from)
	}
	if ar.swimmer {
		// 3D loop: waypoints carry their own Y, so keep the lerped 3D position and
		// pitch toward the 3D heading instead of flattening onto the terrain.
		parent.SetPosition(pos)
		if Vector3.LengthSquared(dir) > 0 {
			faceFlightDirection(parent, Vector3.Normalized(dir))
		}
		return
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

// critterTurnRate is how fast a walking critter/citizen pivots toward its
// heading, in radians/second. A 90° turn takes ~0.22s and a 180° about-face
// ~0.45s — fast enough to feel responsive, slow enough to read as a deliberate
// turn in place rather than an instant snap.
const critterTurnRate = Float.X(7.0)

// signedAngleAround returns the signed angle (radians) that rotates `from` onto
// `to` about `axis`, in [-π, π]. Both vectors are expected in the plane normal
// to axis (the terrain tangent plane here); the sign picks the short way round.
func signedAngleAround(from, to, axis Vector3.XYZ) Float.X {
	cross := Vector3.Cross(from, to)
	return Float.X(math.Atan2(
		float64(Vector3.Dot(cross, Vector3.Normalized(axis))),
		float64(Vector3.Dot(from, to)),
	))
}

// rotateAround rotates v by ang radians about axis (Rodrigues' formula), so the
// turn follows the terrain normal on slopes instead of only world-up.
func rotateAround(v, axis Vector3.XYZ, ang Float.X) Vector3.XYZ {
	axis = Vector3.Normalized(axis)
	c, s := Float.X(math.Cos(float64(ang))), Float.X(math.Sin(float64(ang)))
	return Vector3.Add(
		Vector3.Add(Vector3.MulX(v, c), Vector3.MulX(Vector3.Cross(axis, v), s)),
		Vector3.MulX(axis, Vector3.Dot(axis, v)*(1-c)),
	)
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
		var pos Vector3.XYZ
		if ar.swimmer {
			pos = swimTargetAbs(ar.client.TerrainEditor, action.Target) // seabed-relative depth → absolute
		} else {
			pos = action.Target
			pos.Y = ar.client.TerrainEditor.HeightAt(pos) // ground walkers ride the surface
		}
		parent.SetPosition(pos)
		if ar.swimmer {
			ar.Initial = pos // keep Initial in absolute world space for the next leg's lerp
		} else {
			ar.Initial = action.Target
		}
		ar.current++
		if ar.current >= len(ar.actions) {
			ar.AsNode().SetProcess(false)
			if ar.swimmer {
				// A rested swimmer's depth must keep riding terrain edits and reload,
				// like its placement did. The swim Action stores Target.Y as the depth
				// ABOVE the seabed at the resting XZ, so record it as the entity's float
				// delta. Without this, entity_float_delta still holds the stale
				// PLACEMENT depth, and reseatFloats re-applies it at the new resting XZ
				// on reload (HeightAt(newXZ)+placementDepth) — which strands a fish that
				// swam into shallower water above the surface (the "swimmer flies above
				// the water after reload" bug).
				ar.recordSwimmerRest(parent, action.Target.Y)
			} else {
				// Ground walkers return to Idle here; swimmers leave the resting clip to
				// their EntityAnimator (which settles to idle / dead-floating once the
				// motion it reads goes to zero).
				ar.play("Idle")
			}
			ar.actions = ar.actions[0:0:cap(ar.actions)]
			ar.current = 0
			return
		}
		action = ar.actions[ar.current]
	}
	if ar.swimmer {
		// Fish swim through the water column: lerp straight to the target's absolute
		// position (reconstructed from its seabed-relative depth, so it rides terrain
		// edits) and pitch toward the 3D heading. The swim clip (horizontal vs
		// vertical) is owned by the EntityAnimator, which reads this motion, so we
		// drive position + facing only and never call play().
		end := swimTargetAbs(ar.client.TerrainEditor, action.Target)
		dir := Vector3.Sub(end, ar.Initial)
		pos := Vector3.Lerp(ar.Initial, end, ar.progress(action))
		parent.SetPosition(pos)
		if Vector3.LengthSquared(dir) > 0 {
			faceFlightDirection(parent, Vector3.Normalized(dir))
		}
		return
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

	// Rotate the facing toward the target heading at a CONSTANT angular speed
	// (rad/s) around the up axis, rather than an exponential lerp. A constant
	// rate means a sharp turn visibly pivots — for the first moment of a walk the
	// model spins toward its heading roughly in place (at 0.2 u/s ground speed it
	// only creeps ~turnRate⁻¹·speed while turning) before the walk carries it off,
	// and a 180° reversal sweeps around the short way instead of snapping or
	// moonwalking (lerping a vector toward its opposite collapses through zero).
	// Facing is local/cosmetic — not part of the synced action — so rate-limiting
	// it changes nothing other clients reconstruct from the action's timing.
	if Vector3.LengthSquared(ar.CurrentForward) == 0 {
		ar.CurrentForward = Vector3.XYZ{Z: 1} // Assume default forward is +Z
	}
	cur := Vector3.Normalized(ar.CurrentForward)
	angle := signedAngleAround(cur, targetProjectedForward, ar.CurrentUp)
	maxStep := critterTurnRate * delta
	if Float.X(math.Abs(float64(angle))) <= maxStep {
		ar.CurrentForward = targetProjectedForward
	} else {
		if angle < 0 {
			maxStep = -maxStep
		}
		ar.CurrentForward = Vector3.Normalized(rotateAround(cur, ar.CurrentUp, maxStep))
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
	forward := ar.CurrentForward
	// Snakes side-wind at an angle to their body: offset the facing so the body
	// reads as angled to the path it travels. Rotate a COPY (CurrentForward is the
	// smoothing state for next frame — offsetting it would compound every frame).
	if !ar.snakeChecked {
		ar.snakeChecked = true
		ar.snake = isSnakeRig(model.AsNode())
	}
	if ar.snake {
		forward = rotateAround(forward, ar.CurrentUp, snakeMovementYaw)
	}
	target := Vector3.Add(globalPos, forward)
	model.MoreArgs().LookAt(target, ar.CurrentUp, true)
}
