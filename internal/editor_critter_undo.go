package internal

import (
	"fmt"
	"strconv"
	"strings"

	"graphics.gd/classdb/Node3D"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Vector3"

	"the.quetzal.community/aviary/internal/critter"
	"the.quetzal.community/aviary/internal/musical"
)

// Undo for the critter editor. Every critter edit is a musical mutation
// that SETS something (a bone coordinate, a skin weight, a part's
// anchor), so an edit undoes by publishing the mutations that put the old
// values back — ordinary records every peer and the save observe, with no
// separate undo channel. This file builds those inverses; the edits
// themselves stay where they were.

func critterSculpt(slider string, amount float32) musical.Sculpt {
	return musical.Sculpt{Editor: "critter", Slider: slider, Amount: Float.X(amount), Commit: true}
}

// recordSculpts pushes an already-published sculpt edit onto the undo
// stack; a no-op without a connected recorder (nothing was recorded).
func (ce *CritterEditor) recordSculpts(dos, undos []musical.Sculpt) {
	if ce.recorder == nil || !ce.recorder.recording() {
		return
	}
	ce.recorder.RecordSculptGroup(dos, undos)
}

// restoreSculpts returns the sculpts that put back whatever `slider`
// (a value-setting bone/ or leg/ slider) is about to overwrite.
func (ce *CritterEditor) restoreSculpts(slider string) []musical.Sculpt {
	c := ce.body.critter
	if c == nil {
		return nil
	}
	parts := strings.Split(slider, "/")
	if len(parts) < 3 {
		return nil
	}
	i, err := strconv.Atoi(parts[1])
	if err != nil {
		return nil
	}
	axis := func(v critter.Vec3, name string) (float32, bool) {
		switch name {
		case "x":
			return v.X, true
		case "y":
			return v.Y, true
		case "z":
			return v.Z, true
		}
		return 0, false
	}
	switch parts[0] {
	case "bone":
		bone, ok := c.BoneAt(i)
		if !ok {
			return nil
		}
		if parts[2] == "r" {
			return []musical.Sculpt{critterSculpt(slider, bone.Radius)}
		}
		if v, ok := axis(bone.Pos, parts[2]); ok {
			return []musical.Sculpt{critterSculpt(slider, v)}
		}
	case "leg":
		legs := c.LegsView()
		if i < 0 || i >= len(legs) {
			return nil
		}
		leg := legs[i]
		prefix := "leg/" + parts[1] + "/"
		switch parts[2] {
		case "r":
			// One slider sets all three radii; three put them back.
			return []musical.Sculpt{
				critterSculpt(prefix+"hip/r", leg.HipRadius),
				critterSculpt(prefix+"knee/r", leg.KneeRadius),
				critterSculpt(prefix+"foot/r", leg.FootRadius),
			}
		case "attach":
			return []musical.Sculpt{critterSculpt(slider, float32(leg.Attach))}
		case "kind":
			return []musical.Sculpt{critterSculpt(slider, float32(leg.Kind))}
		}
		if len(parts) < 4 {
			return nil
		}
		pos, radius := leg.Hip, leg.HipRadius
		switch parts[2] {
		case "knee":
			pos, radius = leg.Knee, leg.KneeRadius
		case "foot":
			pos, radius = leg.Foot, leg.FootRadius
		case "hip":
		default:
			return nil
		}
		if parts[3] == "r" {
			return []musical.Sculpt{critterSculpt(slider, radius)}
		}
		if v, ok := axis(pos, parts[3]); ok {
			return []musical.Sculpt{critterSculpt(slider, v)}
		}
	}
	return nil
}

// legAttachSculpts re-sockets every leg to the bone it is on now (spine
// growth and shrinkage renumber and clamp them).
func legAttachSculpts(legs []critter.Leg) []musical.Sculpt {
	out := make([]musical.Sculpt, len(legs))
	for i, leg := range legs {
		out[i] = critterSculpt(fmt.Sprintf("leg/%d/attach", i), float32(leg.Attach))
	}
	return out
}

// emitSpineResize publishes a grow/shrink of the spine ("bone/grow/head"
// and friends) and records its inverse: a grow undoes with the matching
// shrink; a shrink undoes by growing again and restoring the dropped
// bone and the legs' sockets.
func (ce *CritterEditor) emitSpineResize(slider string) {
	c := ce.body.critter
	if c == nil {
		return
	}
	bones, legs := c.Bones(), c.Legs()
	ce.emitBoneSculpt(slider, 0)
	restoreBone := func(i int, b critter.Bone) []musical.Sculpt {
		p := fmt.Sprintf("bone/%d/", i)
		return []musical.Sculpt{
			critterSculpt(p+"x", b.Pos.X), critterSculpt(p+"y", b.Pos.Y),
			critterSculpt(p+"z", b.Pos.Z), critterSculpt(p+"r", b.Radius),
		}
	}
	var undo []musical.Sculpt
	switch slider {
	case "bone/grow/head":
		undo = []musical.Sculpt{critterSculpt("bone/shrink/head", 0)}
	case "bone/grow/tail":
		undo = []musical.Sculpt{critterSculpt("bone/shrink/tail", 0)}
	case "bone/shrink/head", "bone/shrink/tail":
		if len(bones) <= 2 {
			return // the shrink was refused
		}
		grow, at := "bone/grow/head", len(bones)-1
		if slider == "bone/shrink/tail" {
			grow, at = "bone/grow/tail", 0
		}
		undo = append(undo, critterSculpt(grow, 0))
		undo = append(undo, restoreBone(at, bones[at])...)
		undo = append(undo, legAttachSculpts(legs)...)
	default:
		return
	}
	ce.recordSculpts([]musical.Sculpt{critterSculpt(slider, 0)}, undo)
}

// emitLegGrow publishes a new leg with its hip at the body-local target
// and records its removal as the inverse.
func (ce *CritterEditor) emitLegGrow(slider string, target Vector3.XYZ) {
	if ce.body.critter == nil {
		return
	}
	index := ce.body.critter.LegCount()
	ce.emitLegSculptAt(slider, target, 0)
	do := critterSculpt(slider, 0)
	do.Target = target
	ce.recordSculpts([]musical.Sculpt{do}, []musical.Sculpt{critterSculpt(fmt.Sprintf("leg/shrink/%d", index), 0)})
}

// emitLegRemove publishes the removal of leg i and records, as its
// inverse, re-growing the leg in the same slot with every joint restored.
// (Parts that were anchored to its feet go with the leg and do not come
// back with it.)
func (ce *CritterEditor) emitLegRemove(i int) {
	c := ce.body.critter
	if c == nil || i < 0 || i >= c.LegCount() {
		return
	}
	leg := c.Legs()[i]
	shrink := fmt.Sprintf("leg/shrink/%d", i)
	ce.emitLegSculpt(shrink, 0)
	grow := critterSculpt(fmt.Sprintf("leg/grow_at/%s/%d", leg.Kind.Name(), i), 0)
	grow.Target = Vector3.New(leg.Hip.X, leg.Hip.Y, leg.Hip.Z)
	undo := []musical.Sculpt{grow}
	joints := []struct {
		name   string
		pos    critter.Vec3
		radius float32
	}{{"hip", leg.Hip, leg.HipRadius}, {"knee", leg.Knee, leg.KneeRadius}, {"foot", leg.Foot, leg.FootRadius}}
	for _, j := range joints {
		p := fmt.Sprintf("leg/%d/%s/", i, j.name)
		undo = append(undo,
			critterSculpt(p+"x", j.pos.X), critterSculpt(p+"y", j.pos.Y),
			critterSculpt(p+"z", j.pos.Z), critterSculpt(p+"r", j.radius))
	}
	undo = append(undo, critterSculpt(fmt.Sprintf("leg/%d/attach", i), float32(leg.Attach)))
	ce.recordSculpts([]musical.Sculpt{critterSculpt(shrink, 0)}, undo)
}

// partChange is the committed Change that puts part id at anchor — the
// record a placement, a move, a reshape, or an undo of any of them is.
func (ce *CritterEditor) partChange(id Node3D.ID, anchor PartAnchor) (musical.Change, bool) {
	entity, ok := ce.partToEntity[id]
	if !ok || ce.recorder == nil {
		return musical.Change{}, false
	}
	change := changeFromAnchor(anchor)
	change.Author = ce.recorder.localAuthor()
	change.Entity = entity
	change.Editor = "critter"
	change.Commit = true
	return change, true
}

// recordPartEdit records moving/reshaping/painting parts from their
// `before` anchors to their `after` anchors as one undo entry. The new
// anchors are passed in rather than read back: a published Change is
// applied from the mutation queue, not by the time publishing returns.
func (ce *CritterEditor) recordPartEdit(before, after map[Node3D.ID]PartAnchor) {
	if ce.recorder == nil {
		return
	}
	var dos, undos []musical.Change
	for id, old := range before {
		now, ok := after[id]
		if !ok || now == old {
			continue
		}
		do, ok := ce.partChange(id, now)
		if !ok {
			continue
		}
		undo, _ := ce.partChange(id, old)
		dos, undos = append(dos, do), append(undos, undo)
	}
	if len(dos) > 0 {
		ce.recorder.RecordChangeGroup(dos, undos)
	}
}

// recordPartRemoval records deleting part id (already published) with
// the Change that re-creates it — same entity, design and anchor.
func (ce *CritterEditor) recordPartRemoval(id Node3D.ID, removal musical.Change) {
	uri := ce.partURIs[id]
	anchor, ok := ce.body.partAnchors[id]
	if !ok || uri == "" || ce.library == nil {
		return
	}
	restore, ok := ce.partChange(id, anchor)
	if !ok {
		return
	}
	restore.Design = ce.library.MusicalDesign(uri)
	removal.Author = restore.Author
	ce.recorder.RecordChange(removal, restore)
}
