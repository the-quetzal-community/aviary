package internal

import (
	"graphics.gd/classdb/Engine"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/variant/Euler"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Vector3"
	"the.quetzal.community/aviary/internal/musical"
)

// CanDeleteSelection reports whether DeleteSelection would do anything
// right now. Used by the trash-can button to decide visibility without
// committing to a delete.
func (world *Client) CanDeleteSelection() bool {
	if world.selection == 0 || world.space == nil {
		return false
	}
	raw, ok := world.selection.Instance()
	if !ok {
		return false
	}
	node, ok := Object.As[Node3D.Instance](raw)
	if !ok {
		return false
	}
	id := Node3D.ID(node.ID())
	switch world.Editing {
	case Editing.Scenery:
		_, ok = world.object_to_entity[id]
	case Editing.Shelter:
		_, ok = world.ShelterEditor.object_to_entity[id]
		if !ok {
			if parent := node.GetParentNode3d(); parent != Node3D.Nil {
				_, ok = world.ShelterEditor.object_to_entity[Node3D.ID(parent.ID())]
			}
		}
	case Editing.Vehicle:
		_, ok = world.VehicleEditor.object_to_entity[id]
	case Editing.Critter:
		_, ok = world.CritterEditor.partToEntity[id]
	default:
		return false
	}
	return ok
}

// DeleteSelection removes the currently selected entity by routing the
// request through the editor that owns it. Called by both the keyboard
// Delete/Backspace handler and the trash-can UI button so they share
// one canonical path. Returns true if a delete was actually issued.
func (world *Client) DeleteSelection() bool {
	if world.selection == 0 || world.space == nil {
		return false
	}
	raw, ok := world.selection.Instance()
	if !ok {
		return false
	}
	node, ok := Object.As[Node3D.Instance](raw)
	if !ok {
		return false
	}
	id := Node3D.ID(node.ID())

	var ch musical.Change
	ch.Author = world.id
	ch.Remove = true
	ch.Commit = true

	switch world.Editing {
	case Editing.Scenery:
		entity, has := world.object_to_entity[id]
		if !has {
			return false
		}
		ch.Entity = entity
	case Editing.Shelter:
		entity, has := world.ShelterEditor.object_to_entity[id]
		if !has {
			parent := node.GetParentNode3d()
			if parent == Node3D.Nil {
				return false
			}
			entity, has = world.ShelterEditor.object_to_entity[Node3D.ID(parent.ID())]
			if !has {
				return false
			}
		}
		ch.Entity = entity
		ch.Editor = "shelter"
	case Editing.Vehicle:
		entity, has := world.VehicleEditor.object_to_entity[id]
		if !has {
			return false
		}
		ch.Entity = entity
		ch.Editor = "vehicle"
	case Editing.Critter:
		entity, has := world.CritterEditor.partToEntity[id]
		if !has {
			return false
		}
		ch.Entity = entity
		ch.Editor = "critter"
	default:
		return false
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
	world.gizmoDrag.hasMirrorPlane = false
	world.gizmoDrag.design = musical.Design{}
	world.gizmoDrag.twistInitialY = 0
	world.gizmoDrag.twistInitialAngle = 0
	world.gizmoDrag.twistPlaneY = 0
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
	if world.selection == 0 || world.space == nil {
		return false
	}
	raw, ok := world.selection.Instance()
	if !ok {
		return false
	}
	node, ok := Object.As[Node3D.Instance](raw)
	if !ok {
		return false
	}
	id := Node3D.ID(node.ID())

	var design musical.Design
	switch world.Editing {
	case Editing.Scenery:
		if _, has := world.object_to_entity[id]; !has {
			return false
		}
		d, ok := world.findDesignForObject(id)
		if !ok {
			return false
		}
		design = d
	case Editing.Shelter:
		owner := id
		if _, has := world.ShelterEditor.object_to_entity[owner]; !has {
			parent := node.GetParentNode3d()
			if parent == Node3D.Nil {
				return false
			}
			owner = Node3D.ID(parent.ID())
			if _, has := world.ShelterEditor.object_to_entity[owner]; !has {
				return false
			}
		}
		d, ok := findDesignInMap(world.ShelterEditor.design_to_entity, owner)
		if !ok {
			return false
		}
		design = d
	case Editing.Vehicle:
		if _, has := world.VehicleEditor.object_to_entity[id]; !has {
			return false
		}
		d, ok := findDesignInMap(world.VehicleEditor.design_to_entity, id)
		if !ok {
			return false
		}
		design = d
	default:
		return false
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
