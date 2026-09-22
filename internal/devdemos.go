package internal

import (
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Vector3"

	"the.quetzal.community/aviary/internal/musical"
)

// Every scripted dev scenario (AVIARY_DEMO) is registered here; see devshot.go
// for the harness and tools/devshot.sh for the runner. Each builds a fresh list
// of steps (so a scenario's closures hold their own state) and runs once the
// world has finished loading, in a scratch profile with an empty world. Adding
// one is a function plus a line in this table, and a line in tools/devshot.sh.
//
// Scenarios drive the same calls the editors make (publishChange + RecordChange,
// commitSculpt, Undo/Redo) and assert on what the world did with them — the
// in-engine counterpart of internal/nettest's record-level mutation gate.
var devDemos = map[string]func() []devStep{
	"boot":    demoBoot,
	"editors": demoEditors,
	"scenery": demoScenery,
	"terrain": demoTerrain,
}

// devDemoDesign is the library model the scenery scenario places: a plain Kenney
// bench, small and without animation, so it measures placement and nothing else.
const devDemoDesign = "res://library/kenney/benches/furniture_bench.glb"

// demoBoot checks a fresh offline world came up whole: every editor wired, the
// shared space recording, and the starting tile revealed.
func demoBoot() []devStep {
	return []devStep{
		do(func(s *devShot, w *Client) {
			s.check(w.finishedLoading, "world not marked loaded")
			s.check(w.recording(), "musical space not connected (offline solo must still record)")
			for name, editor := range w.editors {
				s.check(editor != nil, "editor %s missing", name)
			}
			s.check(w.TerrainEditor.tileRevealedAt(Vector3.Zero), "origin tile not revealed")
			s.logf("world up: editing=%v author=%v", w.Editing, w.id)
		}),
		snap("world"),
	}
}

// demoEditors switches through every editor, as the editor switcher does, and
// checks each one takes over without error.
func demoEditors() []devStep {
	subjects := []struct {
		name    string
		subject Subject
	}{
		{"scenery", Editing.Scenery},
		{"terrain", Editing.Terrain},
		{"foliage", Editing.Foliage},
		{"mineral", Editing.Mineral},
		{"shelter", Editing.Shelter},
		{"vehicle", Editing.Vehicle},
		{"citizen", Editing.Citizen},
		{"critter", Editing.Critter},
		{"coaster", Editing.Coaster},
	}
	var steps []devStep
	for _, e := range subjects {
		steps = append(steps,
			do(func(s *devShot, w *Client) { w.StartEditing(e.subject) }),
			waitFrames(5),
			do(func(s *devShot, w *Client) {
				if s.check(w.Editing == e.subject, "switched to %s but editing=%v", e.name, w.Editing) {
					s.logf("%s editor active", e.name)
				}
			}),
			snap(e.name),
		)
	}
	return append(steps, do(func(s *devShot, w *Client) { w.StartEditing(Editing.Scenery) }))
}

// demoScenery places a library model through the same Change + undo record the
// scenery editor commits, then undoes and redoes it: the entity must appear,
// vanish and come back, at the placed position.
func demoScenery() []devStep {
	var (
		entity musical.Entity
		at     Vector3.XYZ
	)
	placed := func(w *Client) bool {
		_, ok := w.entity_to_object[entity].Instance()
		return ok
	}
	return []devStep{
		do(func(s *devShot, w *Client) {
			w.StartEditing(Editing.Scenery)
			at = devDemoGroundAhead(w, 4)
			entity = w.NextEntity()
			placement := musical.Change{
				Author: w.localAuthor(),
				Entity: entity,
				Design: w.MusicalDesign(devDemoDesign),
				Offset: at,
				Commit: true,
			}
			s.check(w.publishChange(placement) == nil, "publishChange failed")
			w.RecordChange(placement, musical.Change{Author: placement.Author, Entity: entity, Remove: true})
		}),
		until("placed", placed),
		do(func(s *devShot, w *Client) {
			node, _ := w.entity_to_object[entity].Instance()
			s.check(node.AsNode().IsInsideTree(), "placed node not in the tree")
			got := node.Position()
			s.check(Vector3.Distance(got, at) < 0.01, "placed at %v, want %v", got, at)
		}),
		snap("placed"),
		do(func(s *devShot, w *Client) { s.check(w.Undo(), "nothing to undo") }),
		until("undo removed it", func(w *Client) bool { return !placed(w) }),
		do(func(s *devShot, w *Client) { s.check(w.Redo(), "nothing to redo") }),
		until("redo restored it", placed),
	}
}

// demoTerrain raises the ground with the single-shot raise brush's Sculpt, then
// undoes and redoes it, measuring HeightAt at the brush centre each time.
func demoTerrain() []devStep {
	const amount = 2
	var (
		at     Vector3.XYZ
		before Float.X
	)
	height := func(w *Client) Float.X { return w.TerrainEditor.HeightAt(at) }
	return []devStep{
		do(func(s *devShot, w *Client) {
			w.StartEditing(Editing.Terrain)
			at = Vector3.New(-3, 0, -3)
			before = height(w)
			s.check(w.commitSculpt(musical.Sculpt{
				Author: w.localAuthor(),
				Target: Vector3.New(at.X, before, at.Z),
				Radius: 3,
				Amount: amount,
				Commit: true,
			}) == nil, "commitSculpt failed")
		}),
		until("raised", func(w *Client) bool { return height(w) > before+amount/4 }),
		do(func(s *devShot, w *Client) { s.logf("height %.2f -> %.2f", float64(before), float64(height(w))) }),
		snap("raised"),
		do(func(s *devShot, w *Client) { s.check(w.Undo(), "nothing to undo") }),
		until("undo flattened it", func(w *Client) bool { return Float.Abs(height(w)-before) < 0.01 }),
		do(func(s *devShot, w *Client) { s.check(w.Redo(), "nothing to redo") }),
		until("redo raised it again", func(w *Client) bool { return height(w) > before+amount/4 }),
	}
}

// devDemoGroundAhead is the ground point dist metres in front of the camera, so
// whatever a scenario puts there is in its screenshot.
func devDemoGroundAhead(w *Client, dist Float.X) Vector3.XYZ {
	cam := w.FocalPoint.Lens.Camera.AsNode3D().GlobalTransform()
	forward := Vector3.Neg(cam.Basis.Z)
	forward.Y = 0
	if Vector3.Length(forward) < 0.01 {
		forward = Vector3.New(0, 0, -1)
	}
	at := Vector3.Add(cam.Origin, Vector3.MulX(Vector3.Normalized(forward), dist))
	at.Y = w.TerrainEditor.HeightAt(at)
	return at
}
