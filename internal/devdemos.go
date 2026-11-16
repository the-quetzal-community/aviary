package internal

import (
	"graphics.gd/classdb/Node3D"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Vector3"

	"the.quetzal.community/aviary/internal/critter"
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
	"critter": demoCritter,
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

// demoCritter colours the critter through the material sliders, places a
// mirrored pair of parametric horns and a fin, sculpts the horns (the twin must
// follow), and removes the fin — every step through the editor's own calls, so
// each one is a recorded, replicated mutation.
func demoCritter() []devStep {
	var horns, fins []Node3D.ID
	swept := func(w *Client, design string) []Node3D.ID {
		var ids []Node3D.ID
		for id, uri := range w.CritterEditor.partURIs {
			if _, ok := w.CritterEditor.animatedParts[id].(*sweptPart); ok && uri == design {
				ids = append(ids, id)
			}
		}
		return ids
	}
	sculpted := critter.PartShape{Length: 0.6, Curl: 1.6, Girth: 0.3}
	var (
		footZ float32
		head  critter.Bone
	)
	return []devStep{
		do(func(s *devShot, w *Client) {
			w.StartEditing(Editing.Critter)
			ce := w.CritterEditor
			for tab, value := range map[string]float64{
				"editing/hue": 0.08, "editing/sat": 0.7, "editing/shade": 0.1,
				"editing/pattern": 1, "editing/hue2": 0.6, "editing/sat2": 0.5, "editing/belly": 0.5,
			} {
				ce.SliderHandle(ModeMaterial, tab, value, true)
			}
		}),
		until("skin weights landed", func(w *Client) bool {
			c := w.CritterEditor.body.critter
			return c != nil && c.Weight("skin/pattern") == 1 && c.Weight("skin/belly") == 0.5
		}),
		do(func(s *devShot, w *Client) {
			ce := w.CritterEditor
			primary, _ := skinColours(ce.body.critter)
			s.check(primary.R > primary.B, "skin primary not warm: %v", primary)
			var pair []musical.Change
			for _, x := range []Float.X{0.15, -0.15} {
				placed, ok := ce.place(ce.body.ClosestAnchor(Vector3.New(x, 0.8, 1.15)), BuiltinHornDesign)
				s.check(ok, "horn not placed")
				pair = append(pair, placed)
			}
			ce.recordPlacements(pair...)
			ce.place(ce.body.ClosestAnchor(Vector3.New(0, 0.7, 0)), BuiltinFinDesign)
		}),
		until("horns and fin attached", func(w *Client) bool {
			horns, fins = swept(w, BuiltinHornDesign), swept(w, BuiltinFinDesign)
			return len(horns) == 2 && len(fins) == 1
		}),
		snap("placed"),
		do(func(s *devShot, w *Client) {
			twin, ok := w.CritterEditor.mirrorTwin(horns[0])
			s.check(ok && twin == horns[1], "horn has no mirror twin")
			w.CritterEditor.publishShape(horns[0], sculpted, true)
		}),
		until("both horns took the shape", func(w *Client) bool {
			for _, id := range horns {
				if part := w.CritterEditor.animatedParts[id].(*sweptPart); part.shape != sculpted {
					return false
				}
			}
			return true
		}),
		do(func(s *devShot, w *Client) {
			ce := w.CritterEditor
			s.check(ce.body.partAnchors[horns[1]].Shape == sculpted, "twin anchor lost its shape")
			s.check(w.publishChange(musical.Change{
				Entity: ce.partToEntity[fins[0]], Editor: "critter", Remove: true, Commit: true,
			}) == nil, "publishChange failed")
		}),
		until("fin removed", func(w *Client) bool { return len(swept(w, BuiltinFinDesign)) == 0 }),
		// Reshape the body after colouring it: the skin must survive the
		// surface being rebuilt.
		do(func(s *devShot, w *Client) {
			s.check(w.publishSculpt(musical.Sculpt{Editor: "critter", Slider: "bone/2/r", Amount: 0.55, Commit: true}) == nil, "publishSculpt failed")
		}),
		until("body reshaped", func(w *Client) bool {
			b, _ := w.CritterEditor.body.critter.BoneAt(2)
			return b.Radius > 0.5
		}),
		waitFrames(3),
		snap("sculpted"),

		// Undo/redo, through the editor's own recording paths.
		// A mirrored pair placed by one click is one step.
		do(func(s *devShot, w *Client) { s.check(w.Undo(), "nothing to undo (horn pair)") }),
		until("undo removed both horns", func(w *Client) bool { return len(swept(w, BuiltinHornDesign)) == 0 }),
		do(func(s *devShot, w *Client) { s.check(w.Redo(), "nothing to redo (horn pair)") }),
		until("redo restored both horns", func(w *Client) bool { return len(swept(w, BuiltinHornDesign)) == 2 }),
		// A skin slider.
		do(func(s *devShot, w *Client) { w.CritterEditor.SliderHandle(ModeMaterial, "editing/hue", 0.5, true) }),
		until("hue changed", func(w *Client) bool { return w.CritterEditor.body.critter.Weight("skin/hue") == 0.5 }),
		do(func(s *devShot, w *Client) { s.check(w.Undo(), "nothing to undo (hue)") }),
		until("undo restored the hue", func(w *Client) bool { return w.CritterEditor.body.critter.Weight("skin/hue") == float32(0.08) }),
		// A leg: grow, drag a joint, remove — then unwind all three.
		do(func(s *devShot, w *Client) { w.CritterEditor.emitLegGrow("leg/grow_at", Vector3.New(0.3, 0, 0.4)) }),
		until("leg grown", func(w *Client) bool { return w.CritterEditor.body.critter.LegCount() == 1 }),
		do(func(s *devShot, w *Client) {
			ce := w.CritterEditor
			footZ = ce.body.critter.LegsView()[0].Foot.Z
			ce.dragEmit("leg/0/foot/z", footZ+0.3)
			ce.flushDragEmits()
			ce.emitLegRemove(0)
		}),
		until("leg removed", func(w *Client) bool { return w.CritterEditor.body.critter.LegCount() == 0 }),
		do(func(s *devShot, w *Client) { s.check(w.Undo(), "nothing to undo (leg removal)") }),
		until("undo regrew the dragged leg", func(w *Client) bool {
			legs := w.CritterEditor.body.critter.LegsView()
			return len(legs) == 1 && Float.Abs(Float.X(legs[0].Foot.Z-footZ-0.3)) < 1e-4
		}),
		do(func(s *devShot, w *Client) { s.check(w.Undo(), "nothing to undo (joint drag)") }),
		until("undo put the foot back", func(w *Client) bool {
			legs := w.CritterEditor.body.critter.LegsView()
			return len(legs) == 1 && Float.Abs(Float.X(legs[0].Foot.Z-footZ)) < 1e-4
		}),
		do(func(s *devShot, w *Client) { s.check(w.Undo(), "nothing to undo (leg grow)") }),
		until("undo removed the leg", func(w *Client) bool { return w.CritterEditor.body.critter.LegCount() == 0 }),
		// The spine: shrinking the head drops a bone; undo brings it back as it was.
		do(func(s *devShot, w *Client) {
			c := w.CritterEditor.body.critter
			head, _ = c.BoneAt(c.BoneCount() - 1)
			w.CritterEditor.emitSpineResize("bone/shrink/head")
		}),
		until("head bone dropped", func(w *Client) bool { return w.CritterEditor.body.critter.BoneCount() == 4 }),
		do(func(s *devShot, w *Client) { s.check(w.Undo(), "nothing to undo (spine shrink)") }),
		until("undo restored the head bone", func(w *Client) bool {
			c := w.CritterEditor.body.critter
			b, ok := c.BoneAt(4)
			return ok && c.BoneCount() == 5 && b == head
		}),
		snap("undone"),
		// The explorer's rendered icons: the skin sliders' tabs, then the
		// parametric horn's tile.
		do(func(s *devShot, w *Client) {
			if w.ui == nil {
				return
			}
			// With a bookmark saved, the "my stuff" theme exists in every
			// mode; it must not take the material mode over from the sliders.
			_, err := saveBookmark("devshot", "critter", w.CritterEditor.CaptureCreation())
			s.check(err == nil && hasBookmarks(), "bookmark not saved: %v", err)
			w.ui.SetMode(ModeMaterial)
			s.check(w.ui.Editor.Tabs.AsNode().GetChildCount() == len(skinSliders),
				"material mode shows %d tabs, want the %d skin sliders", w.ui.Editor.Tabs.AsNode().GetChildCount(), len(skinSliders))
		}),
		waitFrames(6),
		snap("material"),
		do(func(s *devShot, w *Client) {
			if w.ui == nil {
				return
			}
			w.ui.SetMode(ModeGeometry)
			for i, tab := range w.ui.Editor.Tabs.AsNode().GetChildren() {
				if tab.Name() == "antlers" {
					w.ui.Editor.Tabs.SetCurrentTab(i)
				}
			}
		}),
		waitFrames(6),
		snap("antlers"),
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
