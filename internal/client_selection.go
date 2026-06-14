package internal

import (
	"graphics.gd/classdb/Engine"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/variant/Euler"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Vector3"
	"the.quetzal.community/aviary/internal/musical"
)

// resolveSelection resolves the current world.selection to its entity,
// the node that owns that entity, and the editor-routing id to stamp on
// a musical.Change (empty for Scenery, which uses the global map and no
// Editor field). It is the single place that knows how to ask the active
// editor — every selection-driven action (delete, duplicate, gizmo) goes
// through it instead of switching on world.Editing. ok is false when
// there's no selection or it isn't an editable entity.
func (world *Client) resolveSelection() (entity musical.Entity, node Node3D.Instance, editorID string, ok bool) {
	if world.selection == 0 || world.space == nil {
		return musical.Entity{}, Node3D.Nil, "", false
	}
	raw, has := world.selection.Instance()
	if !has {
		return musical.Entity{}, Node3D.Nil, "", false
	}
	picked, has := Object.As[Node3D.Instance](raw)
	if !has {
		return musical.Entity{}, Node3D.Nil, "", false
	}
	// Active editors that track their own entities (shelter, vehicle,
	// critter) resolve the pick — including ancestor-walk and routing id.
	if ed, isClickable := world.ui.Editor.editor.(ClickableEditor); isClickable {
		if e, owner, found := ed.EntityForNode(picked); found {
			return e, owner, ed.EditorID(), true
		}
		return musical.Entity{}, Node3D.Nil, "", false
	}
	// Scenery (and any non-ClickableEditor) uses the global map with no
	// Editor routing string.
	if e, found := world.object_to_entity[Node3D.ID(picked.ID())]; found {
		return e, picked, "", true
	}
	return musical.Entity{}, Node3D.Nil, "", false
}

// CanDeleteSelection reports whether DeleteSelection would do anything
// right now. Used by the trash-can button to decide visibility without
// committing to a delete.
func (world *Client) CanDeleteSelection() bool {
	_, _, _, ok := world.resolveSelection()
	return ok
}

// DeleteSelection removes the currently selected entity by routing the
// request through the editor that owns it. Called by both the keyboard
// Delete/Backspace handler and the trash-can UI button so they share
// one canonical path. Returns true if a delete was actually issued.
func (world *Client) DeleteSelection() bool {
	entity, node, editorID, ok := world.resolveSelection()
	if !ok {
		return false
	}
	id := Node3D.ID(node.ID())

	ch := musical.Change{
		Author: world.id,
		Entity: entity,
		Editor: editorID,
		Remove: true,
		Commit: true,
	}

	// Capture the entity's pre-delete state so undo can re-create
	// it with the same design and transform. The design lookup may
	// miss for editor-internal entities that don't go through the
	// global design_to_entity map (critter parts, for example); in
	// that case we still execute the delete, but skip recording an
	// undo entry — replaying a Remove with no matching Create just
	// silently drops on the receiver side, which would surprise the
	// user more than the missing undo.
	design, canRecord := world.findDesignForObject(id)
	prePos := node.AsNode3D().Position()
	preRot := node.AsNode3D().Rotation()

	if err := world.space.Change(ch); err != nil {
		Engine.Raise(err)
		return false
	}
	if canRecord {
		world.RecordChange(ch, musical.Change{
			Author: world.id,
			Entity: ch.Entity,
			Editor: ch.Editor,
			Design: design,
			Offset: prePos,
			Angles: preRot,
		})
	}
	world.selection = 0
	world.gizmoDrag.active = false
	world.gizmoDrag.activeGizmo = 0
	world.gizmoDrag.hasMirrorPlane = false
	world.gizmoDrag.design = musical.Design{}
	world.gizmoDrag.twistInitialY = 0
	world.gizmoDrag.twistInitialAngle = 0
	world.gizmoDrag.twistPlaneY = 0
	world.gizmoDrag.floatInitialY = 0
	world.gizmoDrag.floatPlanePoint = Vector3.Zero
	world.gizmoDrag.floatPlaneNormal = Vector3.Zero
	world.gizmoDrag.floatStartGrabY = 0
	// Deleting a single-placement terrain entity leaves nothing selected, so
	// restore the terrain editor's brush gizmos (no-op outside terrain).
	world.refreshTerrainPlacementGizmos()
	return true
}

// DuplicateSelection enters preview mode for the selected entity's
// design — same flow as picking that design from the design explorer.
// The user then aims and clicks to drop the copy, which keeps the
// duplicate consistent with manual placement (snap-to-terrain, rotate
// with shift-wheel, undo via the normal placement path) instead of
// hard-committing a clone at a fixed offset. Returns true if a preview
// was attached.
func (world *Client) DuplicateSelection() bool {
	_, node, _, ok := world.resolveSelection()
	if !ok {
		return false
	}

	// Resolve the design behind the selected node. ClickableEditors that
	// support duplication answer via DesignForNode; Scenery falls back to
	// the global design map. Editors that can't recover a design (critter)
	// return false here, so duplicate is a no-op for them as before.
	var design musical.Design
	if ed, isClickable := world.ui.Editor.editor.(ClickableEditor); isClickable {
		d, found := ed.DesignForNode(node)
		if !found {
			return false
		}
		design = d
	} else {
		d, found := world.findDesignForObject(Node3D.ID(node.ID()))
		if !found {
			return false
		}
		design = d
	}

	resource, ok := world.design_to_string[design]
	if !ok || resource == "" {
		return false
	}
	if world.ui == nil || world.ui.Editor == nil || world.ui.Editor.editor == nil {
		return false
	}
	world.ui.Editor.editor.SelectDesign(world.ui.mode, resource)
	// Inherit the source entity's rotation and scale so the duplicate
	// preview matches the original's pose 1:1. Position is set per
	// frame by the editor's preview tracking, so we deliberately
	// don't carry it. Optional interface — editors that don't
	// implement it just get a fresh-default preview, which is the
	// pre-existing behaviour.
	if pt, ok := world.ui.Editor.editor.(previewTransformer); ok {
		pt.SetPreviewTransform(node.AsNode3D().Rotation(), node.AsNode3D().Scale())
	}
	return true
}

// previewTransformer is implemented by editors whose preview supports
// being initialised with a specific rotation + scale (rather than the
// editor's defaults). Currently SceneryEditor — other editors compute
// the preview transform from their own gizmos/mouse picker each frame,
// where a one-shot setter wouldn't survive past the next physics tick.
type previewTransformer interface {
	SetPreviewTransform(rot Euler.Radians, scale Vector3.XYZ)
}

// findDesignInMap is the per-editor analogue of findDesignForObject —
// each editor that stores its own design_to_entity map shares the same
// linear-scan shape. Returns the design owning `id`, or the zero
// Design + false if it isn't tracked.
func findDesignInMap(m map[musical.Design][]Node3D.ID, id Node3D.ID) (musical.Design, bool) {
	for design, ids := range m {
		for _, candidate := range ids {
			if candidate == id {
				return design, true
			}
		}
	}
	return musical.Design{}, false
}

// clearSceneryInDisc is used by the bomb tool (the "*" / ClearAllDressingCategory
// removal action). It removes every individually placed library prop (the kind
// you drop with the Scenery editor) whose center lies inside the brush disc.
//
// Each removal is sent as a normal musical.Change so the deletion is observable
// by all clients and replays correctly. We also call RecordChange with the
// inverse Create so undo brings the props back (one undo per prop, which is
// honest for a bulk "nuke" action).
func (c *Client) clearSceneryInDisc(center Vector3.XYZ, radius Float.X) {
	if c == nil || c.space == nil || radius <= 0.01 {
		return
	}
	r2 := float64(radius) * float64(radius)

	type snap struct {
		entity musical.Entity
		design musical.Design
		pos    Vector3.XYZ
		rot    Euler.Radians
	}
	var victims []snap

	for ent, nid := range c.entity_to_object {
		node, ok := nid.Instance()
		if !ok {
			continue
		}
		p := node.AsNode3D().Position()
		dx := float64(p.X - center.X)
		dz := float64(p.Z - center.Z)
		if dx*dx+dz*dz > r2 {
			continue
		}

		design, canRecord := c.findDesignForObject(nid)
		if !canRecord {
			// This is not a top-level library prop (e.g. it's a part of a
			// shelter, vehicle, critter, coaster, etc.). Leave it alone.
			continue
		}

		victims = append(victims, snap{
			entity: ent,
			design: design,
			pos:    p,
			rot:    node.AsNode3D().Rotation(),
		})
	}

	for _, v := range victims {
		ch := musical.Change{
			Author: c.id,
			Entity: v.entity,
			Remove: true,
			Commit: true,
		}

		if err := c.space.Change(ch); err != nil {
			Engine.Raise(err)
			continue
		}

		// Record the inverse so the user can undo the removal of this specific prop.
		c.RecordChange(ch, musical.Change{
			Author: c.id,
			Entity: v.entity,
			Design: v.design,
			Offset: v.pos,
			Angles: v.rot,
			Commit: true,
		})
	}
}
