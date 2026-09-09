package internal

import (
	"strings"
	"testing"

	"graphics.gd/variant/Angle"
	"graphics.gd/variant/Basis"
	"graphics.gd/variant/Euler"
	"graphics.gd/variant/Transform3D"
	"graphics.gd/variant/Vector3"
	"the.quetzal.community/aviary/internal/musical"
)

func coasterDesign(folder, shape string) string {
	return "res://library/kenney/" + folder + "/wood-" + shape + ".glb"
}

// storedPose round-trips a placement transform the way a placed node
// keeps it: the editor stores Basis.AsEulerAngles in the Change and
// the node hands the same angles back from Rotation() when the track
// is walked (placedPieces).
func storedPose(t Transform3D.BasisOrigin) Transform3D.BasisOrigin {
	angles := Basis.AsEulerAngles(t.Basis, Angle.OrderXYZ)
	return Transform3D.BasisOrigin{Basis: Basis.FromEuler(angles, Angle.OrderXYZ), Origin: t.Origin}
}

func near(a, b Vector3.XYZ) bool { return Vector3.Distance(a, b) < coasterJoinTolerance }

// layTrack attaches designs one after another from start, returning
// each piece as placedPieces would recover it from its node.
func layTrack(t *testing.T, editor *CoasterEditor, start Transform3D.BasisOrigin, designs ...string) (track []coasterPlaced, cursor Transform3D.BasisOrigin) {
	t.Helper()
	cursor = start
	for i, design := range designs {
		piece, ok := coasterPieceForPath(design)
		if !ok {
			t.Fatalf("%s is not a track piece", design)
		}
		transform, next, _ := editor.attach(piece, cursor)
		stored := storedPose(transform)
		for _, axis := range []Vector3.XYZ{Vector3.Right, Vector3.Up, Vector3.Back} {
			if Vector3.Distance(Basis.Transform(axis, transform.Basis), Basis.Transform(axis, stored.Basis)) > 1e-4 {
				t.Fatalf("piece %d (%s): rotation does not survive the Euler round-trip: %v vs %v", i, design, transform.Basis, stored.Basis)
			}
		}
		entry, exit := piece.ends(stored)
		track = append(track, coasterPlaced{entity: musical.Entity{Number: uint16(i + 1)}, design: design, piece: piece, entry: entry, exit: exit})
		cursor = next
	}
	return track, cursor
}

// TestCoasterTrackJoins lays a track through every kind of piece and
// checks that each piece, recovered from where its node sits, leaves
// exactly where the next one enters, heading the same way, and that
// walking the placed pieces recovers the whole track in order.
func TestCoasterTrackJoins(t *testing.T) {
	editor := &CoasterEditor{}
	designs := []string{
		coasterDesign("station", "station"),
		coasterDesign("track_f", "straight"),
		coasterDesign("track_r", "corner-small"),
		coasterDesign("track_f", "hill-beginning"),
		coasterDesign("track_f", "hill-end"),
		coasterDesign("track_l", "corner-large"),
		coasterDesign("track_s", "looping"),
		coasterDesign("track_d", "hill-end"),
		coasterDesign("track_d", "hill-beginning"),
		coasterDesign("track_l", "corner-small-ramp"),
		coasterDesign("track_f", "curve"),
		coasterDesign("track_r", "corner-large"),
		coasterDesign("track_f", "straight"),
	}
	for _, facing := range []Angle.Radians{0, Angle.Pi / 2, Angle.Pi, -Angle.Pi / 2} {
		start := Transform3D.BasisOrigin{Basis: Basis.FromEuler(Euler.Radians{Y: facing}, Angle.OrderXYZ), Origin: Vector3.New(3, coasterTrackLift*coasterPieceScale, -2)}
		track, cursor := layTrack(t, editor, start, designs...)
		if !near(track[0].entry.Origin, start.Origin) || Vector3.Distance(heading(track[0].entry), heading(start)) > 1e-4 {
			t.Fatalf("facing %.2f: the first piece does not start at the start pose", facing)
		}
		for i := 1; i < len(track); i++ {
			prev, next := track[i-1], track[i]
			if !near(prev.exit.Origin, next.entry.Origin) {
				t.Fatalf("facing %.2f: %s exits at %v but %s enters at %v", facing, prev.design, prev.exit.Origin, next.design, next.entry.Origin)
			}
			if Vector3.Distance(heading(prev.exit), heading(next.entry)) > 1e-3 {
				t.Fatalf("facing %.2f: %s exits heading %v but %s enters heading %v", facing, prev.design, heading(prev.exit), next.design, heading(next.entry))
			}
		}
		if last := track[len(track)-1]; !near(last.exit.Origin, cursor.Origin) {
			t.Fatalf("facing %.2f: cursor %v is not at the last exit %v", facing, cursor.Origin, last.exit.Origin)
		}
		pieces := make(map[musical.Entity]coasterPlaced)
		for _, p := range track {
			pieces[p.entity] = p
		}
		walked, closed := walkTrack(pieces, track[5].entity)
		if closed || len(walked) != len(track) {
			t.Fatalf("facing %.2f: walkTrack found %d of %d pieces (closed=%v)", facing, len(walked), len(track), closed)
		}
		for i := range track {
			if walked[i].entity != track[i].entity {
				t.Fatalf("facing %.2f: walkTrack order differs at %d: %v", facing, i, walked[i].design)
			}
		}
	}
}

// TestCoasterHandedness checks that a right turn from +Z heads off
// along -X (clockwise seen from above) and a left turn along +X, and
// that the kit's climbing pieces climb.
func TestCoasterHandedness(t *testing.T) {
	editor := &CoasterEditor{}
	start := Transform3D.BasisOrigin{Basis: Basis.Identity, Origin: Vector3.Zero}
	for _, tc := range []struct {
		design string
		want   Vector3.XYZ
		side   float32
	}{
		{coasterDesign("track_r", "corner-small"), Vector3.New(-1, 0, 0), -1},
		{coasterDesign("track_l", "corner-small"), Vector3.New(1, 0, 0), 1},
		{coasterDesign("track_r", "corner-large-ramp"), Vector3.New(-1, 0, 0), -1},
		{coasterDesign("track_l", "corner-large-ramp"), Vector3.New(1, 0, 0), 1},
	} {
		track, cursor := layTrack(t, editor, start, tc.design)
		if Vector3.Distance(heading(cursor), tc.want) > 1e-4 {
			t.Errorf("%s: heads %v after the turn, want %v", tc.design, heading(cursor), tc.want)
		}
		if exit := track[0].exit.Origin; exit.X*tc.side <= 0 {
			t.Errorf("%s: exits at %v, on the wrong side", tc.design, exit)
		}
	}
	track, cursor := layTrack(t, editor, start, coasterDesign("track_f", "hill-beginning"))
	if h := heading(cursor); h.Y <= 0.3 || h.Z <= 0 {
		t.Errorf("hill-beginning leaves heading %v, want a climb along +Z", h)
	}
	if track[0].exit.Origin.Y <= track[0].entry.Origin.Y {
		t.Errorf("hill-beginning exit %v is not above its entry %v", track[0].exit.Origin, track[0].entry.Origin)
	}
	track, cursor = layTrack(t, editor, start, coasterDesign("track_d", "hill-end"))
	if h := heading(cursor); h.Y >= -0.3 || h.Z <= 0 {
		t.Errorf("descending hill-end leaves heading %v, want a descent along +Z", h)
	}
	if track[0].exit.Origin.Y >= track[0].entry.Origin.Y {
		t.Errorf("descending hill-end exit %v is not below its entry %v", track[0].exit.Origin, track[0].entry.Origin)
	}
}

// TestCoasterReverseBuild lays pieces backwards from a track's entry
// end: each piece's exit lands on the cursor and its entry falls
// behind it, so the cursor keeps pointing along the track.
func TestCoasterReverseBuild(t *testing.T) {
	forward := &CoasterEditor{}
	start := Transform3D.BasisOrigin{Basis: Basis.FromEuler(Euler.Radians{Y: Angle.Pi / 2}, Angle.OrderXYZ), Origin: Vector3.New(0, 0.5, 0)}
	track, _ := layTrack(t, forward, start, coasterDesign("station", "station"), coasterDesign("track_r", "corner-small"))
	reverse := &CoasterEditor{reverse: true}
	cursor := track[0].entry
	for _, design := range []string{
		coasterDesign("track_f", "straight"),
		coasterDesign("track_l", "corner-small"),
		coasterDesign("track_d", "hill-beginning"),
		coasterDesign("track_d", "hill-end"),
		coasterDesign("track_r", "corner-large"),
	} {
		piece, _ := coasterPieceForPath(design)
		transform, next, _ := reverse.attach(piece, cursor)
		entry, exit := piece.ends(storedPose(transform))
		if !near(exit.Origin, cursor.Origin) || Vector3.Distance(heading(exit), heading(cursor)) > 1e-3 {
			t.Fatalf("%s: exit %v heading %v is not on the cursor %v heading %v", design, exit.Origin, heading(exit), cursor.Origin, heading(cursor))
		}
		if !near(entry.Origin, next.Origin) || Vector3.Distance(heading(entry), heading(next)) > 1e-3 {
			t.Fatalf("%s: the cursor moved to %v but the piece enters at %v", design, next.Origin, entry.Origin)
		}
		if Vector3.Dot(Vector3.Sub(next.Origin, cursor.Origin), heading(cursor)) >= 0 {
			t.Fatalf("%s: building in reverse moved the cursor forwards (%v -> %v along %v)", design, cursor.Origin, next.Origin, heading(cursor))
		}
		cursor = next
	}
}

func testCompletionMoves(t *testing.T) []coasterMove {
	t.Helper()
	editor := &CoasterEditor{theme: "wood"}
	moves := editor.completionMoves("res://library/kenney/station/wood-station.glb.tscn")
	if len(moves) != len(coasterCompletionShapes) {
		t.Fatalf("completionMoves offered %d of %d shapes", len(moves), len(coasterCompletionShapes))
	}
	for _, m := range moves {
		if !strings.HasPrefix(m.design, "res://library/kenney/track_") || !strings.Contains(m.design, "/wood-") {
			t.Fatalf("completion design %q is not a wood track resource beside the station", m.design)
		}
	}
	return moves
}

func TestCoasterRetheme(t *testing.T) {
	got := coasterRetheme("res://library/kenney/track_f/wood-straight.glb", "flume")
	if want := "res://library/kenney/track_f/flume-straight.glb"; got != want {
		t.Fatalf("coasterRetheme = %q, want %q", got, want)
	}
}

// TestCoasterCompleteCircuit builds the open part of a track, asks the
// search for the run that closes it, and lays that run both ways
// round: forward from the exit end it must arrive on the entry end,
// and in reverse from the entry end it must arrive on the exit end.
func TestCoasterCompleteCircuit(t *testing.T) {
	moves := testCompletionMoves(t)
	for name, designs := range map[string][]string{
		"U turn": {
			coasterDesign("station", "station"),
			coasterDesign("track_f", "straight"),
			coasterDesign("track_r", "corner-large"),
			coasterDesign("track_r", "corner-large"),
		},
		"climb": {
			coasterDesign("station", "station"),
			coasterDesign("track_f", "hill-beginning"),
			coasterDesign("track_f", "hill-end"),
			coasterDesign("track_l", "corner-large"),
		},
		"offset lane": {
			coasterDesign("station", "station"),
			coasterDesign("track_r", "curve"),
			coasterDesign("track_r", "corner-small"),
		},
		"half lane after a loop": {
			coasterDesign("station", "station"),
			coasterDesign("track_s", "looping"),
			coasterDesign("track_l", "corner-large"),
		},
	} {
		start := Transform3D.BasisOrigin{Basis: Basis.FromEuler(Euler.Radians{Y: -Angle.Pi / 2}, Angle.OrderXYZ), Origin: Vector3.New(4, 0.5, 1)}
		track, cursor := layTrack(t, &CoasterEditor{}, start, designs...)
		head, tail := track[0], track[len(track)-1]
		footprint := coasterFootprint{}
		for _, p := range track {
			footprint.add(p.entry.Origin, p.exit.Origin)
		}
		run, ok := coasterSearchJoin(tail.exit, tail.piece.exitPitch, head.entry, head.piece.entryPitch, moves, footprint)
		if !ok {
			t.Fatalf("%s: no join found", name)
		}
		names := make([]string, len(run))
		forwardDesigns := make([]string, len(run))
		for i, m := range run {
			names[i] = designCategory(m.design) + "/" + m.design[len(m.design)-20:]
			forwardDesigns[i] = m.design
		}
		joined, end := layTrack(t, &CoasterEditor{}, cursor, forwardDesigns...)
		if !near(end.Origin, head.entry.Origin) || Vector3.Distance(heading(end), heading(head.entry)) > 1e-3 {
			t.Fatalf("%s: run %v ends at %v heading %v, want %v heading %v", name, names, end.Origin, heading(end), head.entry.Origin, heading(head.entry))
		}
		for _, p := range joined {
			for _, q := range track {
				if p.entity != q.entity && !footprint.clear(p.entry.Origin, p.exit.Origin) {
					t.Fatalf("%s: %s runs through the existing track", name, p.design)
				}
			}
		}
		// The same run laid backwards from the entry end.
		reverse := &CoasterEditor{reverse: true}
		at := head.entry
		for i := len(run) - 1; i >= 0; i-- {
			_, next, _ := reverse.attach(run[i].piece, at)
			at = next
		}
		if !near(at.Origin, tail.exit.Origin) || Vector3.Distance(heading(at), heading(tail.exit)) > 1e-3 {
			t.Fatalf("%s: reverse run ends at %v heading %v, want %v", name, at.Origin, heading(at), tail.exit.Origin)
		}
		t.Logf("%s: %d pieces: %v", name, len(run), names)
	}
}

func TestRepairResourceURI(t *testing.T) {
	for in, want := range map[string]string{
		"res:/library/kenney/track_f/flume-straight.glb":  "res://library/kenney/track_f/flume-straight.glb",
		"res://library/kenney/track_f/flume-straight.glb": "res://library/kenney/track_f/flume-straight.glb",
		"mod://tracks/thing.glb":                           "mod://tracks/thing.glb",
	} {
		if got := repairResourceURI(in); got != want {
			t.Errorf("repairResourceURI(%q) = %q, want %q", in, got, want)
		}
	}
}
