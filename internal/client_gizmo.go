package internal

import (
	"math"

	"graphics.gd/classdb/Engine"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/PhysicsRayQueryParameters3D"
	"graphics.gd/classdb/Viewport"
	"graphics.gd/classdb/XRController3D"
	"graphics.gd/variant/Angle"
	"graphics.gd/variant/Euler"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Vector3"
	"the.quetzal.community/aviary/internal/musical"
)

// stampRouting fills in the musical.Change routing fields for a gizmo
// drag so it dispatches back to the active editor's specialised Change
// handler. The editor-id comes from the ClickableEditor contract; the
// captured design is carried for editors whose handler needs it to
// re-resolve the entity (vehicle). Scenery (not a ClickableEditor) wants
// neither field set, which the type assertion handles by leaving the
// Change untouched. Centralises the routing-stamp switch that the move /
// twist / scale drag paths each repeated.
func (world *Client) stampRouting(ch *musical.Change) {
	ed, ok := world.ui.Editor.editor.(ClickableEditor)
	if !ok {
		return
	}
	ch.Editor = ed.EditorID()
	if world.Editing == Editing.Vehicle {
		ch.Design = world.gizmoDrag.design
	}
}

// canUseGizmoManipulation reports whether the current global gizmo mode
// (toolbar or hotkey) plus the active editor context allows gizmo-based
// manipulation (translate via GizmoShift, lift via GizmoFloat, or
// twist/rotate-Y via GizmoTwist) of the current selection. Scale is
// additionally supported for critter parts.
func (world *Client) canUseGizmoManipulation() bool {
	if world.ui == nil || world.ui.CloudControl == nil {
		return false
	}
	g := world.ui.CloudControl.Gizmo
	if g != GizmoShift && g != GizmoTwist && g != GizmoFloat && g != GizmoScale {
		return false
	}
	// Scenery uses the global entity map and isn't a ClickableEditor, but
	// it supports gizmo manipulation; allow it explicitly. Every other
	// gizmo-capable editor answers GizmoManipulable() for itself (e.g.
	// critter gates on its active sub-view). GizmoScale is supported for
	// critter via a per-part anchor.Scale field piggy-backed on Bounds.Z,
	// so it coexists with the leg-foot encoding in Bounds.X/Y without a
	// schema migration.
	if world.Editing == Editing.Scenery {
		return true
	}
	if ed, ok := world.ui.Editor.editor.(ClickableEditor); ok {
		return ed.GizmoManipulable()
	}
	return false
}

// canUseGizmoTransform is the old name, kept for a few call sites during transition.
func (world *Client) canUseGizmoTransform() bool {
	return world.canUseGizmoManipulation() && world.ui.CloudControl.Gizmo == GizmoShift
}

// armGizmoDrag captures the current selection's start state into
// world.gizmoDrag using the current input ray, so subsequent
// updateGizmoDrag calls can translate (or twist) the object relative
// to the initial grab point. Shared by the desktop left-click path
// and the VR trigger-press path — the only environmental difference
// (mouse vs controller) is hidden inside inputRay().
func (world *Client) armGizmoDrag() {
	if !world.canUseGizmoManipulation() {
		return
	}
	ent, node, ok := world.selectedEntityForGizmo()
	if !ok {
		return
	}
	pos := node.AsNode3D().Position()

	world.gizmoDrag.activeGizmo = world.ui.CloudControl.Gizmo
	world.gizmoDrag.active = true
	world.gizmoDrag.entity = ent
	world.gizmoDrag.startPos = pos
	world.gizmoDrag.dragPlaneY = pos.Y
	world.gizmoDrag.hasMirrorPlane = false
	world.gizmoDrag.mirrorPlanePoint = Vector3.Zero
	world.gizmoDrag.mirrorPlaneNormal = Vector3.Zero
	world.gizmoDrag.design = musical.Design{}
	world.gizmoDrag.twistInitialY = 0
	world.gizmoDrag.twistPlaneY = 0
	world.gizmoDrag.twistInitialAngle = 0
	world.gizmoDrag.scaleInitial = Vector3.Zero
	world.gizmoDrag.scaleInitialDistance = 0
	world.gizmoDrag.scalePlaneY = 0

	o, d := world.inputRay()
	if hit, ok := IntersectRayPlane(o, d, Vector3.New(pos.X, pos.Y, pos.Z), Vector3.New(0, 1, 0)); ok {
		world.gizmoDrag.startGrab = hit
	} else {
		world.gizmoDrag.startGrab = pos
	}

	if world.Editing == Editing.Vehicle {
		if mirrorRaw, has := world.VehicleEditor.entity_to_mirror[ent].Instance(); has {
			if mnode, ok := Object.As[Node3D.Instance](mirrorRaw); ok {
				mpos := mnode.AsNode3D().Position()
				delta := Vector3.Sub(mpos, pos)
				if Vector3.Length(delta) > 0.05 {
					world.gizmoDrag.mirrorPlanePoint = Vector3.Add(pos, Vector3.MulX(delta, 0.5))
					world.gizmoDrag.mirrorPlaneNormal = Vector3.Normalized(delta)
					world.gizmoDrag.hasMirrorPlane = true
				}
			}
		}
		for des, ids := range world.VehicleEditor.design_to_entity {
			for _, id := range ids {
				if id == node.ID() {
					world.gizmoDrag.design = des
					goto designCaptured
				}
			}
		}
	designCaptured:
	}

	if world.gizmoDrag.activeGizmo == GizmoTwist {
		rot := node.AsNode3D().Rotation()
		world.gizmoDrag.twistInitialY = rot.Y
		if world.Editing == Editing.Critter && world.CritterEditor != nil {
			if a, has := world.CritterEditor.body.partAnchors[Node3D.ID(node.ID())]; has {
				world.gizmoDrag.twistInitialY = Angle.Radians(a.Twist)
			}
		}
		world.gizmoDrag.twistPlaneY = pos.Y

		o, d := world.inputRay()
		if hit, ok := IntersectRayPlane(o, d, Vector3.New(pos.X, pos.Y, pos.Z), Vector3.New(0, 1, 0)); ok {
			dx := hit.X - pos.X
			dz := hit.Z - pos.Z
			world.gizmoDrag.twistInitialAngle = Float.X(Angle.Atan2(Angle.Radians(dz), Angle.Radians(dx)))
		} else {
			world.gizmoDrag.twistInitialAngle = 0
		}
	}

	if world.gizmoDrag.activeGizmo == GizmoScale {
		world.gizmoDrag.scaleInitial = node.AsNode3D().Scale()
		world.gizmoDrag.scalePlaneY = pos.Y
		o, d := world.inputRay()
		if hit, ok := IntersectRayPlane(o, d, Vector3.New(pos.X, pos.Y, pos.Z), Vector3.New(0, 1, 0)); ok {
			dx := float64(hit.X - pos.X)
			dz := float64(hit.Z - pos.Z)
			world.gizmoDrag.scaleInitialDistance = Float.X(math.Sqrt(dx*dx + dz*dz))
		}
		// Critter override: critter parts don't expose meaningful
		// Node3D.Scale (it's baked into a custom basis by
		// positionPart) and they sit deep inside the body tree, so
		// Position is local rather than world. Capture the part's
		// per-anchor Scale (1.0 default) into scaleInitial.X and
		// re-do the plane intersection using GlobalPosition so the
		// distance matches what the user sees on screen.
		if world.Editing == Editing.Critter && world.CritterEditor != nil {
			initial := float32(1.0)
			if a, has := world.CritterEditor.body.partAnchors[Node3D.ID(node.ID())]; has && a.Scale > 0 {
				initial = a.Scale
			}
			world.gizmoDrag.scaleInitial = Vector3.New(initial, initial, initial)
			worldPos := node.AsNode3D().GlobalPosition()
			world.gizmoDrag.startPos = worldPos // re-used by update as plane center
			world.gizmoDrag.scalePlaneY = worldPos.Y
			if hit, ok := IntersectRayPlane(o, d, worldPos, Vector3.New(0, 1, 0)); ok {
				dx := float64(hit.X - worldPos.X)
				dz := float64(hit.Z - worldPos.Z)
				world.gizmoDrag.scaleInitialDistance = Float.X(math.Sqrt(dx*dx + dz*dz))
			}
		}
	}

	if world.gizmoDrag.activeGizmo == GizmoFloat {
		world.gizmoDrag.floatInitialY = pos.Y
		o, d := world.inputRay()
		// Vertical drag plane through the object. Its normal is the
		// horizontal component of the initial input ray so that
		// "pulling up" on the mouse (or controller) produces a clean
		// world-Y lift regardless of camera azimuth.
		horiz := Vector3.New(d.X, 0, d.Z)
		if l := Vector3.Length(horiz); l > 1e-4 {
			world.gizmoDrag.floatPlaneNormal = Vector3.DivX(horiz, l)
		} else {
			world.gizmoDrag.floatPlaneNormal = Vector3.New(1, 0, 0)
		}
		world.gizmoDrag.floatPlanePoint = pos
		if hit, ok := IntersectRayPlane(o, d, pos, world.gizmoDrag.floatPlaneNormal); ok {
			world.gizmoDrag.floatStartGrabY = hit.Y
		} else {
			world.gizmoDrag.floatStartGrabY = pos.Y
		}
	}
}

// inputRay is the current "primary" input ray in world space:
// desktop returns the mouse-projected ray from the main camera; XR
// returns the active gizmo-drag controller's ray (or, if no drag is
// armed, the right hand's). Direction is not normalised — callers
// pass it to IntersectRayPlane which doesn't care about length.
func (world *Client) inputRay() (origin, dir Vector3.XYZ) {
	if world.xr {
		ctrl := world.vrDragController
		if ctrl == XRController3D.Nil {
			ctrl = world.xrRight
		}
		if ctrl != XRController3D.Nil {
			t := ctrl.AsNode3D().GlobalTransform()
			return t.Origin, Vector3.New(-t.Basis.Z.X, -t.Basis.Z.Y, -t.Basis.Z.Z)
		}
	}
	return MouseRay(world.AsNode3D())
}

// selectedEntityForGizmo returns the musical.Entity (if any) that corresponds
// to the current world.selection, looking first in the global map and then
// falling back to the per-editor maps for Vehicle and Shelter (which keep
// their own tracking and short-circuit the generic registration path in
// musicalImpl.Change).
func (world *Client) selectedEntityForGizmo() (musical.Entity, Node3D.Instance, bool) {
	if world.selection == 0 {
		return musical.Entity{}, Node3D.Nil, false
	}
	raw, ok := world.selection.Instance()
	if !ok {
		return musical.Entity{}, Node3D.Nil, false
	}
	node, ok := Object.As[Node3D.Instance](raw)
	if !ok {
		return musical.Entity{}, Node3D.Nil, false
	}
	if e, has := world.object_to_entity[Node3D.ID(node.ID())]; has {
		return e, node, true
	}

	// Editors that keep their own entity maps (shelter, vehicle, critter)
	// resolve the pick themselves via ClickableEditor, including any
	// ancestor-walk for nested pickable children. Scenery deliberately
	// doesn't implement it — it uses the global map handled above.
	if ed, ok := world.ui.Editor.editor.(ClickableEditor); ok {
		if e, owner, ok := ed.EntityForNode(node); ok {
			return e, owner, true
		}
	}
	return musical.Entity{}, Node3D.Nil, false
}

// updateGizmoDrag is called on mouse motion (or could be polled) while a
// GizmoShift / GizmoFloat / GizmoTwist / GizmoScale drag is active. It
// computes the appropriate delta (horizontal translate, vertical lift,
// rotation, or uniform scale) and emits a preview (Commit:false) musical
// Change.
//
// For Vehicle and Shelter we set the Editor field so the Change routes to
// their specialized handlers (which do mirroring, floor grouping, etc.).
func (world *Client) updateGizmoDrag() {
	if !world.gizmoDrag.active {
		return
	}
	ent, node, ok := world.selectedEntityForGizmo()
	if !ok || node == Node3D.Nil {
		return
	}
	// Critter parts live in anchor space (T, Theta, Offset) on the
	// body surface, not world XYZ — divert into a dedicated path that
	// raycasts against the body and consults ClosestAnchor.
	if world.Editing == Editing.Critter {
		world.updateCritterGizmoDrag(ent, node, false)
		return
	}
	// --- Uniform scale handling ---
	// Diverted before the translate/twist paths because scale has its
	// own grab-point math (distance-from-center) and emits a Change
	// that should *only* set Bounds, keeping Offset/Angles at their
	// pre-drag values.
	if world.gizmoDrag.activeGizmo == GizmoScale && world.gizmoDrag.scaleInitialDistance > 0.001 {
		o, d := world.inputRay()
		if hit, ok := IntersectRayPlane(o, d, Vector3.New(world.gizmoDrag.startPos.X, world.gizmoDrag.scalePlaneY, world.gizmoDrag.startPos.Z), Vector3.New(0, 1, 0)); ok {
			dx := float64(hit.X - world.gizmoDrag.startPos.X)
			dz := float64(hit.Z - world.gizmoDrag.startPos.Z)
			cur := Float.X(math.Sqrt(dx*dx + dz*dz))
			factor := cur / world.gizmoDrag.scaleInitialDistance
			if factor < 0.1 {
				factor = 0.1
			}
			if factor > 10 {
				factor = 10
			}
			newScale := Vector3.MulX(world.gizmoDrag.scaleInitial, factor)
			scaleCh := musical.Change{
				Author: world.id,
				Entity: ent,
				Offset: world.gizmoDrag.startPos,
				Angles: node.AsNode3D().Rotation(),
				Bounds: newScale,
				Commit: false,
			}
			world.stampRouting(&scaleCh)
			_ = world.space.Change(scaleCh)
		}
		return // scale handled, skip translate/twist
	}

	// --- Float (vertical lift) handling ---
	if world.gizmoDrag.activeGizmo == GizmoFloat {
		o, d := world.inputRay()
		n := world.gizmoDrag.floatPlaneNormal
		p := world.gizmoDrag.floatPlanePoint
		if Vector3.Length(n) < 1e-4 {
			n = Vector3.New(1, 0, 0)
		}
		if hit, ok := IntersectRayPlane(o, d, p, n); ok {
			deltaY := hit.Y - world.gizmoDrag.floatStartGrabY
			newY := world.gizmoDrag.floatInitialY + deltaY

			newPos := world.gizmoDrag.startPos
			if world.Editing == Editing.Scenery {
				xz := Vector3.New(newPos.X, 0, newPos.Z)
				terrainY := world.TerrainEditor.HeightAt(xz)
				delta := newY - terrainY
				newPos.Y = delta
			} else {
				newPos.Y = newY
			}

			// Preserve whatever rotation the object currently has.
			rot := node.AsNode3D().Rotation()

			floatCh := musical.Change{
				Author: world.id,
				Entity: ent,
				Offset: newPos,
				Angles: rot,
				Commit: false,
			}
			if world.Editing == Editing.Scenery {
				floatCh.Editor = "float" // marker: Offset.Y is lift delta relative to terrain
			}
			world.stampRouting(&floatCh)

			// Vehicle mirror: keep any existing mirror offset so the
			// twin moves in lockstep on the Y axis too.
			if world.Editing == Editing.Vehicle {
				if mirrorRaw, has := world.VehicleEditor.entity_to_mirror[ent].Instance(); has {
					if mirrorNode, ok := Object.As[Node3D.Instance](mirrorRaw); ok {
						mirrorPos := mirrorNode.AsNode3D().Position()
						mainPos := node.AsNode3D().Position()
						floatCh.Mirror = Vector3.Sub(mirrorPos, mainPos)
					}
				}
			}

			if err := world.space.Change(floatCh); err != nil {
				_ = err
			}
		}
		return // float handled
	}

	o, d := world.inputRay()
	planePoint := Vector3.New(world.gizmoDrag.startPos.X, world.gizmoDrag.dragPlaneY, world.gizmoDrag.startPos.Z)
	hit, ok := IntersectRayPlane(o, d, planePoint, Vector3.New(0, 1, 0))
	if !ok {
		return
	}
	delta := Vector3.Sub(hit, world.gizmoDrag.startGrab)
	newPos := Vector3.Add(world.gizmoDrag.startPos, delta)

	if world.Editing == Editing.Shelter {
		// Match the grid snapping used during shelter placement previews
		// (most objects snap to integer grid on X/Z).
		newPos.X = Float.Round(newPos.X)
		newPos.Z = Float.Round(newPos.Z)
	}

	// For scenery objects, gizmoShift moves follow the terrain surface while
	// preserving whatever lift the object had when the drag started, so a
	// model raised with GizmoFloat doesn't snap back to the ground when you
	// slide it sideways. We store Y as a terrain-relative *delta* (Editor
	// "float") rather than an absolute world Y: the horizontal (X/Z)
	// translation rides the terrain and the lift is carried on top of it
	// (and keeps riding later terrain edits, exactly like a pure float).
	if world.Editing == Editing.Scenery {
		startTerrainY := world.TerrainEditor.HeightAt(Vector3.New(world.gizmoDrag.startPos.X, 0, world.gizmoDrag.startPos.Z))
		newPos.Y = world.gizmoDrag.startPos.Y - startTerrainY
	}

	// Preserve whatever rotation the object currently has.
	rot := node.AsNode3D().Rotation()

	ch := musical.Change{
		Author: world.id,
		Entity: ent,
		Offset: newPos,
		Angles: rot,
		Commit: false, // live preview during drag
	}
	if world.Editing == Editing.Scenery {
		ch.Editor = "float" // Offset.Y is a terrain-relative lift delta
	}
	world.stampRouting(&ch)

	// Vehicle mirror handling: if we captured a symmetry plane at drag
	// start, reflect the target main position over it. This makes the
	// mirror stay on the opposite side of the axis (instead of rigidly
	// following with a fixed offset). If the part is moved close to the
	// axis, we clear the Mirror so the handler removes the twin.
	if world.Editing == Editing.Vehicle {
		if world.gizmoDrag.hasMirrorPlane {
			target := newPos
			v := Vector3.Sub(target, world.gizmoDrag.mirrorPlanePoint)
			d := Vector3.Dot(v, world.gizmoDrag.mirrorPlaneNormal)
			reflected := Vector3.Sub(target, Vector3.MulX(world.gizmoDrag.mirrorPlaneNormal, 2*d))
			ch.Mirror = Vector3.Sub(reflected, target)

			if Float.Abs(d) < 0.25 || Vector3.Length(ch.Mirror) < 0.25 {
				ch.Mirror = Vector3.Zero
			}
		} else if mirrorRaw, has := world.VehicleEditor.entity_to_mirror[ent].Instance(); has {
			// Fallback (no plane captured): keep old relative behavior
			if mirrorNode, ok := Object.As[Node3D.Instance](mirrorRaw); ok {
				mirrorPos := mirrorNode.AsNode3D().Position()
				mainPos := node.AsNode3D().Position()
				ch.Mirror = Vector3.Sub(mirrorPos, mainPos)
			}
		}
	}

	// --- Twist (local Y rotation) handling ---
	if world.gizmoDrag.activeGizmo == GizmoTwist {
		// Recompute current hit on the rotation plane
		o, d := world.inputRay()
		if hit, ok := IntersectRayPlane(o, d, Vector3.New(world.gizmoDrag.startPos.X, world.gizmoDrag.twistPlaneY, world.gizmoDrag.startPos.Z), Vector3.New(0, 1, 0)); ok {
			dx := hit.X - world.gizmoDrag.startPos.X
			dz := hit.Z - world.gizmoDrag.startPos.Z
			curAngle := Float.X(Angle.Atan2(Angle.Radians(dz), Angle.Radians(dx)))

			// Invert delta so mouse movement feels natural (left/right matches
			// the visual rotation direction most users expect).
			delta := world.gizmoDrag.twistInitialAngle - curAngle

			newY := world.gizmoDrag.twistInitialY + Angle.Radians(delta)

			// Note: shelter snapping is now only applied on release (see commitGizmoDrag)
			// so the live drag feels responsive. Final value will snap to 90° grid.

			rot := node.AsNode3D().Rotation()
			rot.Y = newY

			twistCh := musical.Change{
				Author: world.id,
				Entity: ent,
				Offset: world.gizmoDrag.startPos, // keep original position during pure twist
				Angles: rot,
				Commit: false,
			}
			world.stampRouting(&twistCh)
			// Preserve the existing mirror offset (if any) so remirror()
			// does not interpret the lack of Mirror field as "remove the twin".
			// This mirrors the logic we use for move drags.
			if world.Editing == Editing.Vehicle {
				if mirrorRaw, has := world.VehicleEditor.entity_to_mirror[ent].Instance(); has {
					if mirrorNode, ok := Object.As[Node3D.Instance](mirrorRaw); ok {
						mirrorPos := mirrorNode.AsNode3D().Position()
						mainPos := node.AsNode3D().Position()
						twistCh.Mirror = Vector3.Sub(mirrorPos, mainPos)
					}
				}
			}
			_ = world.space.Change(twistCh)
		}
		return // twist handled, don't fall into the translation path
	}

	if err := world.space.Change(ch); err != nil {
		// Non-fatal during drag.
		_ = err
	}
}

// commitGizmoDrag writes one final Change with Commit:true using the
// object's *current* live transform (which has been driven by the preview
// changes). This ensures the edit is durably recorded in the musical log.
//
// We set Editor for Vehicle/Shelter so the update goes through their
// specialized Change handlers.
func (world *Client) commitGizmoDrag() {
	if !world.gizmoDrag.active {
		return
	}
	ent, node, ok := world.selectedEntityForGizmo()
	if !ok || node == Node3D.Nil {
		return
	}
	if world.Editing == Editing.Critter {
		world.updateCritterGizmoDrag(ent, node, true)
		return
	}

	pos := node.AsNode3D().Position()
	rot := node.AsNode3D().Rotation()

	if world.Editing == Editing.Shelter {
		// Match the grid snapping used during shelter placement previews.
		pos.X = Float.Round(pos.X)
		pos.Z = Float.Round(pos.Z)
	}

	ch := musical.Change{
		Author: world.id,
		Entity: ent,
		Offset: pos,
		Angles: rot,
		Commit: true,
	}
	world.stampRouting(&ch)

	// Vehicle mirror handling: if we captured a symmetry plane at drag
	// start, reflect the target main position over it. This makes the
	// mirror stay on the opposite side of the axis (instead of rigidly
	// following with a fixed offset). If the part is moved close to the
	// axis, we clear the Mirror so the handler removes the twin.
	if world.Editing == Editing.Vehicle {
		if world.gizmoDrag.hasMirrorPlane {
			target := pos
			v := Vector3.Sub(target, world.gizmoDrag.mirrorPlanePoint)
			d := Vector3.Dot(v, world.gizmoDrag.mirrorPlaneNormal)
			reflected := Vector3.Sub(target, Vector3.MulX(world.gizmoDrag.mirrorPlaneNormal, 2*d))
			ch.Mirror = Vector3.Sub(reflected, target)

			if Float.Abs(d) < 0.25 || Vector3.Length(ch.Mirror) < 0.25 {
				ch.Mirror = Vector3.Zero
			}
		} else if mirrorRaw, has := world.VehicleEditor.entity_to_mirror[ent].Instance(); has {
			// Fallback (no plane captured): keep old relative behavior
			if mirrorNode, ok := Object.As[Node3D.Instance](mirrorRaw); ok {
				mirrorPos := mirrorNode.AsNode3D().Position()
				mainPos := node.AsNode3D().Position()
				ch.Mirror = Vector3.Sub(mirrorPos, mainPos)
			}
		}
	}

	// For a pure scale drag, only Bounds changed during the drag;
	// the live node scale (driven by preview Changes) is authoritative.
	// Commit one final Change recording it, and the undo entry restores
	// the pre-drag scale.
	if world.gizmoDrag.activeGizmo == GizmoScale {
		scaleCh := musical.Change{
			Author: world.id,
			Entity: ent,
			Offset: world.gizmoDrag.startPos,
			Angles: node.AsNode3D().Rotation(),
			Bounds: node.AsNode3D().Scale(),
			Commit: true,
		}
		world.stampRouting(&scaleCh)
		if err := world.space.Change(scaleCh); err != nil {
			Engine.Raise(err)
		}
		undo := scaleCh
		undo.Bounds = world.gizmoDrag.scaleInitial
		world.RecordChange(scaleCh, undo)
		return
	}

	// For a pure twist drag, the live node rotation (updated by the preview
	// Changes) is already correct. Just commit it.
	if world.gizmoDrag.activeGizmo == GizmoTwist {
		rot := node.AsNode3D().Rotation()

		if world.Editing == Editing.Shelter {
			// On release, snap the final rotation to the nearest 90° increment
			// relative to the orientation at the start of this drag.
			step := math.Pi / 2
			deltaFromStart := rot.Y - world.gizmoDrag.twistInitialY
			snapped := math.Round(float64(deltaFromStart)/step) * step
			rot.Y = world.gizmoDrag.twistInitialY + Angle.Radians(snapped)
		}

		twistCh := musical.Change{
			Author: world.id,
			Entity: ent,
			Offset: pos, // position unchanged during pure twist
			Angles: rot,
			Commit: true,
		}
		world.stampRouting(&twistCh)
		// Preserve mirror offset on final commit too.
		if world.Editing == Editing.Vehicle {
			if mirrorRaw, has := world.VehicleEditor.entity_to_mirror[ent].Instance(); has {
				if mirrorNode, ok := Object.As[Node3D.Instance](mirrorRaw); ok {
					mirrorPos := mirrorNode.AsNode3D().Position()
					mainPos := node.AsNode3D().Position()
					twistCh.Mirror = Vector3.Sub(mirrorPos, mainPos)
				}
			}
		}
		_ = world.space.Change(twistCh)
		// Undo of a twist = same Change but with Angles.Y restored
		// to the pre-drag value. Position is unchanged during twist,
		// so we reuse `pos`. Mirror field flows through as captured.
		undo := twistCh
		undo.Angles.Y = world.gizmoDrag.twistInitialY
		world.RecordChange(twistCh, undo)
		return
	}

	// For a pure float (vertical) drag we just need to make sure the
	// final lifted Y is stored in the (already stamped + mirrored) musical
	// Change and let the common send + undo recording path handle it.
	// Scenery objects store their height as a *delta* above the terrain
	// (Editor="float") so they ride terrain edits. Apply this for a vertical
	// float *and* a horizontal shift, so moving a lifted model sideways keeps
	// its lift instead of snapping it to the ground. (Twist and Scale return
	// earlier, so only Float/Shift reach this point.)
	if world.Editing == Editing.Scenery &&
		(world.gizmoDrag.activeGizmo == GizmoFloat || world.gizmoDrag.activeGizmo == GizmoShift) {
		current := node.AsNode3D().Position()
		xz := Vector3.New(current.X, 0, current.Z)
		terrainY := world.TerrainEditor.HeightAt(xz)
		ch.Offset.Y = current.Y - terrainY
		ch.Editor = "float"
		// Fall through for common send + Record.
	} else if world.gizmoDrag.activeGizmo == GizmoFloat {
		// Non-scenery float: keep the absolute lifted Y (already in ch.Offset).
		ch.Offset.Y = node.AsNode3D().Position().Y
	}

	if err := world.space.Change(ch); err != nil {
		Engine.Raise(err)
	}
	// Undo of a shift = move back to pre-drag position. Rotation
	// doesn't change during shift, so the live rot (which we just
	// committed) IS the pre-shift rot. Mirror field flows through
	// as captured.
	undo := ch
	if ch.Editor == "float" {
		// Restore previous delta (0 if it was grounded before the float).
		preXZ := Vector3.New(world.gizmoDrag.startPos.X, 0, world.gizmoDrag.startPos.Z)
		preT := world.TerrainEditor.HeightAt(preXZ)
		preD := world.gizmoDrag.startPos.Y - preT
		undo.Offset = Vector3.New(world.gizmoDrag.startPos.X, preD, world.gizmoDrag.startPos.Z)
	} else {
		undo.Offset = world.gizmoDrag.startPos
	}
	world.RecordChange(ch, undo)
}

// updateCritterGizmoDrag handles GizmoShift / GizmoTwist for the
// critter editor. Critter parts don't live in world-space — they're
// anchored parametrically on the body surface (T along spine, Theta
// around it, Offset radial), or pinned to a leg foot. We can't reuse
// the horizontal-plane intersection the other editors use; instead a
// physics raycast picks the body surface and CritterBody.ClosestAnchor
// turns the hit point into anchor coordinates, which are then encoded
// back into musical.Change.Offset (and Bounds for leg anchors). Twist
// rides on Angles.Y the same way the other editors use rotation.Y.
//
// The `commit` flag toggles between live-preview drags (false) and
// the durable release-time write (true).
func (world *Client) updateCritterGizmoDrag(ent musical.Entity, node Node3D.Instance, commit bool) {
	if world.CritterEditor == nil || world.CritterEditor.body.critter == nil {
		return
	}
	body := &world.CritterEditor.body
	// Encode whatever the current anchor is into a Change template
	// — this preserves the OnLeg/LegFoot/LegSide bits when the user
	// is only twisting (no shift in anchor).
	cur, hasCur := body.partAnchors[Node3D.ID(node.ID())]
	if !hasCur {
		return
	}
	next := cur

	if world.gizmoDrag.activeGizmo == GizmoShift {
		// Raycast the mouse against the body collider (layer 2) and
		// translate the hit into body-local coordinates so
		// ClosestAnchor returns a sensible anchor. PartSelectionMask
		// clears layer 1 so we don't snag any already-placed parts
		// sitting on top of the surface.
		cam := Viewport.Get(world.AsNode()).GetCamera3d()
		space := world.AsNode3D().GetWorld3d().DirectSpaceState()
		mouse := Viewport.Get(world.AsNode()).GetMousePosition()
		from, to := cam.ProjectRayOrigin(mouse), cam.ProjectPosition(mouse, 1000)
		query := PhysicsRayQueryParameters3D.Create(from, to, nil)
		query.SetCollisionMask(int(PartSelectionMask))
		hit := space.IntersectRay(query)
		if hit.Collider == Object.Nil {
			return
		}
		bodyOrigin := body.mesh.AsNode3D().GlobalPosition()
		local := Vector3.Sub(hit.Position, bodyOrigin)
		fresh := body.ClosestAnchor(local)
		next.T = fresh.T
		next.Theta = fresh.Theta
		next.Offset = fresh.Offset
		next.OnLeg = fresh.OnLeg
		next.LegFoot = fresh.LegFoot
		next.LegSide = fresh.LegSide
	}

	if world.gizmoDrag.activeGizmo == GizmoScale && world.gizmoDrag.scaleInitialDistance > 0.001 {
		// Per-part uniform scale. armGizmoDrag's critter override
		// stored the initial multiplier (1.0 default) in scaleInitial.X
		// and the part's world position in startPos; the plane is at
		// scalePlaneY.
		o, d := world.inputRay()
		center := Vector3.New(world.gizmoDrag.startPos.X, world.gizmoDrag.scalePlaneY, world.gizmoDrag.startPos.Z)
		if hit, ok := IntersectRayPlane(o, d, center, Vector3.New(0, 1, 0)); ok {
			dx := float64(hit.X - world.gizmoDrag.startPos.X)
			dz := float64(hit.Z - world.gizmoDrag.startPos.Z)
			curDist := Float.X(math.Sqrt(dx*dx + dz*dz))
			factor := curDist / world.gizmoDrag.scaleInitialDistance
			if factor < 0.1 {
				factor = 0.1
			}
			if factor > 10 {
				factor = 10
			}
			next.Scale = float32(world.gizmoDrag.scaleInitial.X) * float32(factor)
		}
	}

	if world.gizmoDrag.activeGizmo == GizmoTwist {
		// Same atan2-on-horizontal-plane scheme the other editors
		// use, mapped into the anchor's Twist field instead of the
		// part's world rotation. Snapshot the part's *current* world
		// position once (origin doesn't matter for the relative
		// angle math; we just need a stable pivot per frame).
		pos := node.AsNode3D().GlobalPosition()
		o, d := world.inputRay()
		if hit, ok := IntersectRayPlane(o, d, Vector3.New(pos.X, world.gizmoDrag.twistPlaneY, pos.Z), Vector3.New(0, 1, 0)); ok {
			dx := hit.X - pos.X
			dz := hit.Z - pos.Z
			cur := Float.X(Angle.Atan2(Angle.Radians(dz), Angle.Radians(dx)))
			delta := world.gizmoDrag.twistInitialAngle - cur
			next.Twist = float32(world.gizmoDrag.twistInitialY) + float32(delta)
		}
	}

	ch := musical.Change{
		Author: world.id,
		Entity: ent,
		Editor: "critter",
		Offset: Vector3.New(Float.X(next.T), Float.X(next.Theta), Float.X(next.Offset)),
		Angles: Euler.Radians{X: 0, Y: Angle.Radians(next.Twist), Z: 0},
		Commit: commit,
	}
	if next.OnLeg {
		// place() encodes leg-foot anchors as Bounds.X = LegFoot+1
		// so a zero Bounds in old records still decodes as a body
		// anchor. Mirror that here so the receive side decodes the
		// same way (see anchorFromChange in editor_critter.go).
		// Bounds.Z carries the per-part scale (0 = legacy default).
		ch.Bounds = Vector3.New(Float.X(next.LegFoot+1), Float.X(next.LegSide), Float.X(next.Scale))
	} else if next.Scale > 0 {
		// Body anchor + per-part scale: only Bounds.Z is used; .X
		// stays 0 so anchorFromChange still sees it as a body anchor.
		ch.Bounds = Vector3.New(0, 0, Float.X(next.Scale))
	}
	if err := world.space.Change(ch); err != nil {
		if commit {
			Engine.Raise(err)
		}
	}
}
