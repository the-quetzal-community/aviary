package internal

import (
	"container/heap"
	"fmt"

	"graphics.gd/variant/Angle"
	"graphics.gd/variant/Basis"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Transform3D"
	"graphics.gd/variant/Vector3"
	"the.quetzal.community/aviary/internal/musical"
)

// BuiltinCoasterComplete is the design-explorer tile that closes the
// open track into a circuit (see CoasterEditor.completeCircuit).
const BuiltinCoasterComplete = "procedural://coaster/complete"

// coasterCompletionShapes are the pieces auto-complete may lay, as
// (folder, shape) pairs looked up in the track's theme (every theme
// ships the same shapes in every folder). Everything here moves the
// cursor by whole half-metre cells and quarter-metre rises, so a
// search over the grid stays exact; the cosmetic bumps/skews are left
// to the player. The loop is here for its half-lane shift: a track
// that already has one sits half a cell off, and only another loop
// can bring it back.
var coasterCompletionShapes = [][2]string{
	{"track_f", "straight"},
	{"track_r", "corner-small"}, {"track_l", "corner-small"},
	{"track_r", "corner-large"}, {"track_l", "corner-large"},
	{"track_f", "hill-beginning"}, {"track_f", "hill-end"},
	{"track_f", "hill-complete"}, {"track_f", "hill-complete-half"},
	{"track_d", "hill-end"}, {"track_d", "hill-beginning"},
	{"track_d", "hill-complete"}, {"track_d", "hill-complete-half"},
	{"track_r", "corner-small-ramp"}, {"track_l", "corner-small-ramp"},
	{"track_r", "corner-large-ramp"}, {"track_l", "corner-large-ramp"},
	{"track_r", "curve"}, {"track_l", "curve"},
	{"track_s", "looping"},
}

// coasterCompletionBudget caps the search: how many cursor states may
// be expanded before giving up on a join.
const coasterCompletionBudget = 100000

// coasterMove is one candidate piece for the search.
type coasterMove struct {
	design string
	piece  coasterPiece
}

// cost is what the search pays to lay the piece: straights and
// turns one; anything that changes height, and the lane change, two;
// the loop three, so a join only climbs, dives, sidesteps or loops
// when the ends call for it.
func (move coasterMove) cost() int {
	switch {
	case move.piece.exit.Z == move.piece.entry.Z:
		return 3 // the loop: no progress along the track
	case move.piece.exit.Y != move.piece.entry.Y || (move.piece.exit.X != 0 && move.piece.exitRotation.Y == 0):
		return 2
	}
	return 1
}

// coasterCell keys a cursor pose on the build grid: half-metre cells
// in the plane, quarter-metre rises, one of four headings and the
// track's grade.
type coasterCell struct {
	x, y, z int32
	heading uint8
	pitch   int8
}

// heading is the direction of travel at a track pose.
func heading(pose Transform3D.BasisOrigin) Vector3.XYZ {
	return Basis.Transform(Vector3.New(0, 0, 1), pose.Basis)
}

// coasterCellOf quantises a cursor pose. Every piece auto-complete
// lays moves the cursor by whole cells, so two poses that quantise
// alike are the same pose up to float noise.
func coasterCellOf(pose Transform3D.BasisOrigin, pitch Angle.Radians) coasterCell {
	forward := heading(pose)
	quarter := Float.X(Angle.Pi / 2)
	heading := int(Float.Round(Float.X(Angle.Atan2(forward.X, forward.Z))/quarter)) % 4
	if heading < 0 {
		heading += 4
	}
	return coasterCell{
		x:       int32(Float.Round(pose.Origin.X * 2)),
		y:       int32(Float.Round(pose.Origin.Y * 4)),
		z:       int32(Float.Round(pose.Origin.Z * 2)),
		heading: uint8(heading),
		pitch:   coasterGrade(pitch),
	}
}

// coasterGrade classifies a grade: -1 descending, 0 level, +1 climbing.
func coasterGrade(pitch Angle.Radians) int8 {
	switch {
	case pitch > 0.01:
		return 1
	case pitch < -0.01:
		return -1
	}
	return 0
}

// coasterFootprint is the set of grid columns existing track runs
// through, each with the rail heights (quarter metres) found there,
// so the search can keep new pieces off the old ones.
type coasterFootprint map[[2]int32][]int32

// coasterClearance is the vertical gap (quarter metres) a new piece
// needs over or under existing track before the two count as clear.
const coasterClearance = 6 // 1.5 m

// samples walks the straight line from a to b in half-metre steps,
// skipping the ends themselves (where pieces legitimately touch).
func coasterSamples(a, b Vector3.XYZ) []Vector3.XYZ {
	length := Vector3.Distance(a, b)
	steps := int(Float.Ceil(length / 0.5))
	var out []Vector3.XYZ
	for i := 1; i < steps; i++ {
		out = append(out, Vector3.Lerp(a, b, Float.X(i)/Float.X(steps)))
	}
	return out
}

func (fp coasterFootprint) add(a, b Vector3.XYZ) {
	for _, p := range coasterSamples(a, b) {
		key := [2]int32{int32(Float.Round(p.X * 2)), int32(Float.Round(p.Z * 2))}
		fp[key] = append(fp[key], int32(Float.Round(p.Y*4)))
	}
}

func (fp coasterFootprint) clear(a, b Vector3.XYZ) bool {
	for _, p := range coasterSamples(a, b) {
		key := [2]int32{int32(Float.Round(p.X * 2)), int32(Float.Round(p.Z * 2))}
		y := int32(Float.Round(p.Y * 4))
		for _, other := range fp[key] {
			if d := y - other; d > -coasterClearance && d < coasterClearance {
				return false
			}
		}
	}
	return true
}

// coasterSearchNode is an open-set entry: a cursor pose reached by
// laying `via` after the pose at `parent`.
type coasterSearchNode struct {
	cell   coasterCell
	pose   Transform3D.BasisOrigin
	cost   int
	score  int
	id     int // index in the search arena
	parent int // arena index of the pose this was reached from, -1 at the start
	via    int // index into moves of the piece laid to get here
}

type coasterOpenSet []*coasterSearchNode

func (h coasterOpenSet) Len() int           { return len(h) }
func (h coasterOpenSet) Less(i, j int) bool { return h[i].score < h[j].score }
func (h coasterOpenSet) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *coasterOpenSet) Push(x any)        { *h = append(*h, x.(*coasterSearchNode)) }
func (h *coasterOpenSet) Pop() (x any) {
	old := *h
	n := len(old)
	x = old[n-1]
	*h = old[:n-1]
	return x
}

func coasterAbs(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}

func coasterMax(a, b int32) int32 {
	if a > b {
		return a
	}
	return b
}

// coasterHeuristic bounds the cost still needed from cell to goal: no
// piece moves the cursor more than eight half-metre cells across the
// plane or two quarter-metre steps in height, and every piece costs
// at least one.
func coasterHeuristic(cell, goal coasterCell) int {
	plane := (coasterAbs(cell.x-goal.x) + coasterAbs(cell.z-goal.z) + 7) / 8
	rise := (coasterAbs(cell.y-goal.y) + 1) / 2
	return int(coasterMax(plane, rise))
}

// coasterSearchJoin finds a run of moves that carries a forward cursor
// from start to goal (arriving at the goal's heading and grade)
// without running through the footprint. A* over the build grid on
// the moves' costs.
func coasterSearchJoin(start Transform3D.BasisOrigin, startPitch Angle.Radians, goal Transform3D.BasisOrigin, goalPitch Angle.Radians, moves []coasterMove, footprint coasterFootprint) ([]coasterMove, bool) {
	goalCell := coasterCellOf(goal, goalPitch)
	nodes := []*coasterSearchNode{{cell: coasterCellOf(start, startPitch), pose: start, parent: -1, via: -1}}
	nodes[0].score = coasterHeuristic(nodes[0].cell, goalCell)
	open := coasterOpenSet{nodes[0]}
	best := map[coasterCell]int{nodes[0].cell: 0}
	closed := map[coasterCell]bool{}
	for expanded := 0; open.Len() > 0 && expanded < coasterCompletionBudget; expanded++ {
		node := heap.Pop(&open).(*coasterSearchNode)
		if closed[node.cell] {
			continue
		}
		closed[node.cell] = true
		if node.cell == goalCell {
			var run []coasterMove
			for at := node; at.parent >= 0; at = nodes[at.parent] {
				run = append([]coasterMove{moves[at.via]}, run...)
			}
			return run, true
		}
		for i, move := range moves {
			if coasterGrade(move.piece.entryPitch) != node.cell.pitch {
				continue
			}
			transform, next := move.piece.atEntry(node.pose)
			entry, exit := move.piece.ends(transform)
			if !footprint.clear(entry.Origin, exit.Origin) {
				continue
			}
			cell := coasterCellOf(next, move.piece.exitPitch)
			cost := node.cost + move.cost()
			if prior, seen := best[cell]; seen && prior <= cost {
				continue
			}
			best[cell] = cost
			child := &coasterSearchNode{cell: cell, pose: next, cost: cost, id: len(nodes), parent: node.id, via: i}
			child.score = cost + coasterHeuristic(cell, goalCell)
			nodes = append(nodes, child)
			heap.Push(&open, child)
		}
	}
	return nil, false
}

// completionMoves lists the pieces auto-complete may use for the open
// track: the completion shapes in the track's theme, located beside
// the placed piece `sample` (".../<author>/<folder>/<theme>-<shape>.glb").
func (editor *CoasterEditor) completionMoves(sample string) []coasterMove {
	root := uriDir(uriDir(sample))
	var moves []coasterMove
	for _, fs := range coasterCompletionShapes {
		design := root + "/" + fs[0] + "/" + editor.theme + "-" + fs[1] + ".glb"
		piece, ok := coasterPieceForPath(design)
		if !ok {
			continue
		}
		moves = append(moves, coasterMove{design: design, piece: piece})
	}
	return moves
}

// completeCircuit closes the open track: it searches for a run of
// pieces joining the track's exit end back to its entry end, lays
// them as one grouped undo, and closes the track (a circuit has no
// free end). Nothing happens when no track is open, the track is
// already a loop, or no join is found within the search budget.
func (editor *CoasterEditor) completeCircuit() bool {
	if !editor.cursorValid || len(editor.chain) == 0 || !editor.recorder.recording() {
		fmt.Println("coaster: complete the circuit: no track is open")
		return false
	}
	pieces := editor.placedPieces()
	track, closed := walkTrack(pieces, editor.chain[0].entity)
	if closed || len(track) == 0 {
		fmt.Println("coaster: complete the circuit: the track is already a loop")
		return false
	}
	head, tail := track[0], track[len(track)-1]
	footprint := coasterFootprint{}
	for _, p := range track {
		footprint.add(p.entry.Origin, p.exit.Origin)
	}
	moves := editor.completionMoves(tail.design)
	run, ok := coasterSearchJoin(tail.exit, tail.piece.exitPitch, head.entry, head.piece.entryPitch, moves, footprint)
	if !ok {
		fmt.Printf("coaster: complete the circuit: no join found from %v (heading %v) to %v (heading %v) with %d piece kinds\n",
			tail.exit.Origin, heading(tail.exit), head.entry.Origin, heading(head.entry), len(moves))
		return false
	}
	fmt.Printf("coaster: complete the circuit: laying %d pieces\n", len(run))
	// Building forward the run lays from the exit end in order; in
	// reverse the cursor sits on the entry end and the same run is
	// laid last piece first, each piece's exit landing on the cursor.
	if editor.reverse {
		for i, j := 0, len(run)-1; i < j; i, j = i+1, j-1 {
			run[i], run[j] = run[j], run[i]
		}
	}
	dos := make([]musical.Change, 0, len(run))
	undos := make([]musical.Change, 0, len(run))
	for _, move := range run {
		do, undo := editor.layPiece(move.design, move.piece)
		dos = append(dos, do)
		undos = append(undos, undo)
	}
	editor.recorder.RecordChangeGroup(dos, undos)
	editor.closeTrack()
	return true
}
