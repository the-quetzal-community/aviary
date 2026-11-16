package internal

import (
	"graphics.gd/classdb/Engine"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/variant/Color"
)

// Painting parts. In the material mode the first three sliders (hue,
// saturation, shade) paint the BODY — unless a part is selected, in which
// case they paint that part, and its mirrored twin with it. The selection
// outline is the cue for which one the sliders are holding. Dragging a
// part back to plain white clears its colour, so it returns to the look
// it shipped with (or, for a horn or fin, to following the skin).

// tintTarget is the selected part the colour sliders would paint.
func (ce *CritterEditor) tintTarget() (Node3D.ID, bool) {
	if ce.workbench == nil || ce.spineEdit || ce.control != nil {
		return 0, false
	}
	node, ok := ce.workbench.selectedNode()
	if !ok {
		return 0, false
	}
	id := Node3D.ID(node.ID())
	_, ok = ce.body.partAnchors[id]
	return id, ok
}

// tintComponent maps a colour slider tab to its HSV component.
func tintComponent(tab string) (int, bool) {
	switch tab {
	case "editing/hue":
		return 0, true
	case "editing/sat":
		return 1, true
	case "editing/shade":
		return 2, true
	}
	return 0, false
}

// partHSV is the part's colour as the three slider values (hue,
// saturation, shade). It is remembered per part rather than derived from
// the stored RGB each time, so the hue survives a trip through grey.
func (ce *CritterEditor) partHSV(id Node3D.ID) [3]float32 {
	if v, ok := ce.tintHSV[id]; ok {
		return v
	}
	tint := ce.body.partAnchors[id].Tint
	if tint.A == 0 {
		return [3]float32{} // uncoloured = white = all sliders at zero
	}
	h, s, v := rgbToHSV(tint)
	return [3]float32{h, s, 1 - v}
}

// tintSliderConfig reports the slider position for a colour tab when a
// part is selected.
func (ce *CritterEditor) tintSliderConfig(tab string) (float64, bool) {
	component, ok := tintComponent(tab)
	if !ok {
		return 0, false
	}
	id, ok := ce.tintTarget()
	if !ok {
		return 0, false
	}
	return float64(ce.partHSV(id)[component]), true
}

// tintSliderHandle paints the selected part from a colour slider. It
// reports false when there is no part to paint, so the slider falls
// through to the body's skin.
func (ce *CritterEditor) tintSliderHandle(tab string, value float64, commit bool) bool {
	component, ok := tintComponent(tab)
	if !ok {
		return false
	}
	id, ok := ce.tintTarget()
	if !ok {
		return false
	}
	state := ce.partHSV(id)
	state[component] = float32(value)
	if ce.tintHSV == nil {
		ce.tintHSV = make(map[Node3D.ID][3]float32)
	}
	ce.tintHSV[id] = state
	var colour Color.RGBA // zero: uncoloured
	if state[1] > 0 || state[2] > 0 {
		colour = hsv(state[0], state[1], 1-state[2])
	}
	ids := []Node3D.ID{id}
	if twin, ok := ce.mirrorTwin(id); ok {
		ids = append(ids, twin)
		ce.tintHSV[twin] = state
	}
	if ce.tintBefore == nil {
		// First event of this drag: the anchors to undo back to.
		ce.tintBefore = make(map[Node3D.ID]PartAnchor, len(ids))
		for _, id := range ids {
			ce.tintBefore[id] = ce.body.partAnchors[id]
		}
	}
	after := make(map[Node3D.ID]PartAnchor, len(ids))
	for _, id := range ids {
		anchor := ce.body.partAnchors[id]
		anchor.Tint = colour
		after[id] = anchor
		change, recorded := ce.partChange(id, anchor)
		if !recorded {
			ce.body.SetPartAnchor(id, anchor) // single-user dev: apply directly
			continue
		}
		change.Commit = commit
		if err := ce.recorder.publishChange(change); err != nil && commit {
			Engine.Raise(err)
		}
	}
	if commit {
		ce.recordPartEdit(ce.tintBefore, after)
		ce.tintBefore = nil
	}
	return true
}

// syncTintSliders rebuilds the explorer when the part the colour sliders
// hold changes while they are on screen, so they show that part's colour
// (or the body's again).
func (ce *CritterEditor) syncTintSliders() {
	if ce.workbench == nil || ce.workbench.uiMode() != ModeMaterial {
		return
	}
	id, _ := ce.tintTarget()
	if id == ce.tintShown {
		return
	}
	ce.tintShown = id
	ce.workbench.refreshDesignExplorer()
}
