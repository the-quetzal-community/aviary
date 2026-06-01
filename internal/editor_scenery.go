package internal

import (
	"graphics.gd/classdb/Input"
	"graphics.gd/classdb/InputEvent"
	"graphics.gd/classdb/InputEventMouseButton"
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/XRController3D"
	"graphics.gd/variant/Angle"
	"graphics.gd/variant/Euler"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Vector3"
	"the.quetzal.community/aviary/internal/musical"
)

type SceneryEditor struct {
	Node3D.Extension[SceneryEditor]
	musical.Stubbed

	Preview PreviewRenderer

	// previewOnTerrain is true when PhysicsProcess's last picker
	// query landed on an actual TerrainTile. Placement is only
	// allowed in that state so a VR trigger can't drop the design
	// at a floating laser-endpoint fallback the user can see for
	// orientation but isn't a valid drop site.
	previewOnTerrain bool

	client *Client
}

// sceneryLibraryScale is the "library placement" factor every scenery design is
// scaled by when dropped into the world (on top of any intrinsic root scale the
// .glb/.scn carries — see PreviewRenderer.attach). The library meshes are
// authored ~10× their in-world size, so this brings them down to size. The
// dressing brush scatters those same library meshes and applies the SAME factor
// (see dressingParams.baseScale) so a scattered prop matches its scenery size.
const sceneryLibraryScale Float.X = 0.1

func (*SceneryEditor) Views() []string          { return nil }
func (*SceneryEditor) SwitchToView(view string) {}

func (editor *SceneryEditor) Ready() {
	editor.Preview.defaultScale = Vector3.New(sceneryLibraryScale, sceneryLibraryScale, sceneryLibraryScale)
	editor.Preview.AsNode3D().SetScale(editor.Preview.defaultScale)
}

func (editor *SceneryEditor) UnhandledInput(event InputEvent.Instance) {
	if event, ok := Object.As[InputEventMouseButton.Instance](event); ok {
		if Input.IsKeyPressed(Input.KeyShift) {
			if event.ButtonIndex() == Input.MouseButtonWheelUp {
				editor.Preview.AsNode3D().Rotate(Vector3.XYZ{0, 1, 0}, -Angle.Pi/64)
			}
			if event.ButtonIndex() == Input.MouseButtonWheelDown {
				editor.Preview.AsNode3D().Rotate(Vector3.XYZ{0, 1, 0}, Angle.Pi/64)
			}
		}
		if event.ButtonIndex() == Input.MouseButtonRight && event.AsInputEvent().IsPressed() {
			editor.Preview.Remove()
		}
		if event.ButtonIndex() == Input.MouseButtonLeft && event.AsInputEvent().IsPressed() {
			editor.TryPlacePreview()
		}
	}
}

// TryPlacePreview commits the current preview design as a placed
// entity if one is loaded, and returns true on success. Shared by
// the desktop left-click path and the VR trigger handler (where it's
// invoked via the PreviewPlacer interface when the user pulls trigger
// off-UI with a design ready to drop). Holding Shift on desktop keeps
// the preview attached for rapid duplication; in VR there's no Shift
// hotkey, so the preview always clears.
func (editor *SceneryEditor) TryPlacePreview() bool {
	if editor.Preview.Design() == "" {
		return false
	}
	// Require a live terrain hit before placement so a VR trigger
	// can't drop the design at the floating laser-endpoint preview
	// the user sees as feedback when pointing at sky or geometry.
	if !editor.previewOnTerrain {
		return false
	}
	placement := musical.Change{
		Author: editor.client.id,
		Entity: editor.client.NextEntity(),
		Design: editor.client.MusicalDesign(editor.Preview.Design()),
		Offset: editor.Preview.AsNode3D().Position(),
		Angles: editor.Preview.AsNode3D().Rotation(),
		// Carry the preview's scale forward so a duplicate-from-
		// selection preserves the source's user-scaled size. For a
		// fresh placement the value has been adjusted (in
		// PreviewRenderer.attach) to include both the editor default
		// (0.1) and any intrinsic root scale from the design (Kenney
		// "preset scale" models ship .scn with non-1 root scales).
		// The musical Change path then uses this as the absolute
		// root scale for the placed entity so it matches the preview.
		Bounds: editor.Preview.AsNode3D().Scale(),
		Commit: true,
	}
	editor.client.space.Change(placement)
	editor.client.RecordChange(placement, musical.Change{
		Author: editor.client.id,
		Entity: placement.Entity,
		Remove: true,
	})
	if !Input.IsKeyPressed(Input.KeyShift) {
		editor.Preview.Remove()
	}
	return true
}

func (editor *SceneryEditor) PhysicsProcess(_ Float.X) {
	if editor.Preview.Design() == "" {
		editor.previewOnTerrain = false
		return
	}
	// PreviewPicker routes to the right controller's aim in VR
	// (and the mouse projection on desktop). When the pointer lands
	// on a terrain tile, the preview snaps there and is committable.
	// When it doesn't:
	//   - desktop: hide the preview (matches the pre-VR behaviour).
	//   - VR: float the preview 3 m down the laser as visual
	//     feedback so the user can see what they're holding, while
	//     keeping previewOnTerrain=false so a trigger pull won't
	//     commit it in mid-air.
	hover := editor.client.PreviewPicker()
	onTerrain := Object.Is[*TerrainTile](hover.Collider)
	editor.previewOnTerrain = onTerrain
	if onTerrain {
		editor.Preview.AsNode3D().SetVisible(true)
		editor.Preview.AsNode3D().SetGlobalPosition(hover.Position)
		return
	}
	if editor.client.xr && editor.client.xrRight != XRController3D.Nil {
		t := editor.client.xrRight.AsNode3D().GlobalTransform()
		forward := Vector3.XYZ{X: -t.Basis.Z.X, Y: -t.Basis.Z.Y, Z: -t.Basis.Z.Z}
		pos := Vector3.Add(t.Origin, Vector3.MulX(forward, 3.0))
		editor.Preview.AsNode3D().SetVisible(true)
		editor.Preview.AsNode3D().SetGlobalPosition(pos)
		return
	}
	editor.Preview.AsNode3D().SetVisible(false)
}

func (fe *SceneryEditor) Name() string { return "scenery" }

func (fe *SceneryEditor) EnableEditor() {
	fe.client.SetGizmos([]Gizmo{
		GizmoPoint, GizmoShift, GizmoTwist, GizmoFloat,
		GizmoSpace, GizmoClone, GizmoTrash,
	})
	fe.client.TerrainEditor.AsNode().SetProcessMode(Node.ProcessModeInherit)
}
func (fe *SceneryEditor) ChangeEditor() {
	fe.client.TerrainEditor.AsNode().SetProcessMode(Node.ProcessModeDisabled)
}

func (es *SceneryEditor) Tabs(mode Mode) []string {
	switch mode {
	case ModeGeometry:
		return []string{
			// natural
			"foliage",
			"boulder",

			// village
			"housing",
			"village",
			"farming",
			"factory",
			"defense",
			"obelisk",
			"utility",

			"fencing",
		}
	case ModeDressing:
		return []string{
			// vehicle
			"citizen",
			"critter",
			"swimmer",
			"scooter",
			"bicycle",
			"roadway",
			"railway",
			"seaship",
			"airship",
			"rockets",
		}
	case ModeMaterial:
		return []string{
			"colours",
			"posture",
		}
	default:
		return nil
	}
}

func (fe *SceneryEditor) SelectDesign(mode Mode, design string) {
	fe.Preview.SetDesign(design)
}

// PreviewOverDropZone tells the design-explorer drag flow whether the
// in-progress preview is currently parked on a valid drop site, so it
// can swap between showing the 3D preview (true) and the 2D thumbnail
// ghost following the cursor (false).
func (editor *SceneryEditor) PreviewOverDropZone() bool {
	return editor.Preview.Design() != "" && editor.previewOnTerrain
}

// SetPreviewTransform overrides the preview's rotation and scale, used
// by Client.DuplicateSelection so the in-progress duplicate matches
// the source entity's pose. Position is left alone — PhysicsProcess
// will snap the preview to wherever the cursor lands next.
func (editor *SceneryEditor) SetPreviewTransform(rot Euler.Radians, scale Vector3.XYZ) {
	editor.Preview.AsNode3D().SetRotation(rot)
	editor.Preview.AsNode3D().SetScale(scale)
	editor.Preview.hasExplicitScale = true
}

// ClearPreview is the design-explorer drag flow's escape hatch for a
// release that landed off any valid drop site — wipes the preview so
// the ghost doesn't persist after a missed drop.
func (editor *SceneryEditor) ClearPreview() {
	editor.Preview.Remove()
}

func (fe *SceneryEditor) SliderHandle(mode Mode, editing string, value float64, commit bool) {

}

func (fe *SceneryEditor) SliderConfig(mode Mode, editing string) (init, min, max, step float64) {
	return 0, 0, 1, 0.01
}
