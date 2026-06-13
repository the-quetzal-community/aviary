package internal

import (
	"graphics.gd/classdb/Input"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Vector3"
)

// swimPlaceDrag is the press-hold depth drag used while placing a swimmer: the
// fish anchors at its mid-water XZ and the held placement click slides it up and
// down the Y axis (clamped to the water column) before release commits it. Driven
// from SceneryEditor.PhysicsProcess (the drag + release) and begun from its
// UnhandledInput (the press). Inactive for every other design.
type swimPlaceDrag struct {
	active bool
	x, z   Float.X // anchor XZ (the seabed point under the press)
	y      Float.X // current dragged absolute Y

	// Vertical drag plane through the anchor (the GizmoFloat lift mechanism), so
	// the held cursor's vertical motion maps cleanly to world-Y.
	planePoint  Vector3.XYZ
	planeNormal Vector3.XYZ
	grabStartY  Float.X
	startY      Float.X
}

// swimmerSelected reports whether the design currently loaded in the preview is a
// swimmer, so placement hovers it at mid-water and the click becomes a depth drag
// rather than an instant drop.
func (editor *SceneryEditor) swimmerSelected() bool {
	return isSwimmerCategory(designCategory(editor.Preview.Design()))
}

// beginSwimPlaceDrag starts the depth drag from the current (mid-water) hover: it
// locks the XZ and sets up a vertical drag plane so the held cursor's vertical
// motion slides the fish's Y within the water column.
func (editor *SceneryEditor) beginSwimPlaceDrag() {
	pos := editor.Preview.AsNode3D().Position()
	editor.swimDrag.active = true
	editor.swimDrag.x = pos.X
	editor.swimDrag.z = pos.Z
	editor.swimDrag.y = pos.Y
	editor.swimDrag.startY = pos.Y
	editor.previewOnTerrain = true // hold the placement gate through the drag

	o, d := MouseRay(editor.AsNode3D())
	horiz := Vector3.New(d.X, 0, d.Z)
	if l := Vector3.Length(horiz); l > 1e-4 {
		editor.swimDrag.planeNormal = Vector3.DivX(horiz, l)
	} else {
		editor.swimDrag.planeNormal = Vector3.New(1, 0, 0)
	}
	editor.swimDrag.planePoint = pos
	if hit, ok := IntersectRayPlane(o, d, editor.swimDrag.planePoint, editor.swimDrag.planeNormal); ok {
		editor.swimDrag.grabStartY = hit.Y
	} else {
		editor.swimDrag.grabStartY = pos.Y
	}
}

// updateSwimPlaceDrag slides the dragged Y along the drag plane from the live
// cursor ray, clamped to the water column at the anchor XZ.
func (editor *SceneryEditor) updateSwimPlaceDrag() {
	if editor.terrain == nil {
		return
	}
	o, d := MouseRay(editor.AsNode3D())
	if hit, ok := IntersectRayPlane(o, d, editor.swimDrag.planePoint, editor.swimDrag.planeNormal); ok {
		newY := editor.swimDrag.startY + (hit.Y - editor.swimDrag.grabStartY)
		editor.swimDrag.y = editor.terrain.ClampToWater(Vector3.New(editor.swimDrag.x, 0, editor.swimDrag.z), newY)
	}
}

// processSwimPlaceDrag runs each frame while the depth drag is held: it tracks the
// dragged Y and parks the preview there, and on button release commits the
// placement (at the dragged depth) via TryPlacePreview.
func (editor *SceneryEditor) processSwimPlaceDrag() {
	if !Input.IsMouseButtonPressed(Input.MouseButtonLeft) {
		editor.swimDrag.active = false
		editor.TryPlacePreview() // commits at the preview's last (dragged) position
		return
	}
	editor.updateSwimPlaceDrag()
	editor.Preview.AsNode3D().SetVisible(true)
	editor.Preview.AsNode3D().SetGlobalPosition(Vector3.New(editor.swimDrag.x, editor.swimDrag.y, editor.swimDrag.z))
	editor.previewOnTerrain = true
}

// cancelSwimPlaceDrag drops an in-progress depth drag (design cleared / editor
// left) without committing. No-op when not dragging.
func (editor *SceneryEditor) cancelSwimPlaceDrag() {
	editor.swimDrag.active = false
}
