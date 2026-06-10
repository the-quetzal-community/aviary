package internal

import (
	"graphics.gd/classdb/Camera3D"
	"graphics.gd/classdb/Material"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/PackedScene"
	"graphics.gd/classdb/PhysicsDirectSpaceState3D"
	"graphics.gd/classdb/Texture2D"
	"graphics.gd/classdb/XRController3D"
	"graphics.gd/variant/Float"

	"the.quetzal.community/aviary/internal/musical"
)

// This file defines the capability interfaces ("ports") that editors depend
// on instead of holding a *Client directly. Each interface is a narrow slice
// of what Client offers; an editor declares fields only for the capabilities
// it actually uses, so the compiler documents and enforces each editor's
// coupling surface. Client implements all of them — wiring an editor is just
// assigning the client into each port field (see Client.Ready).
//
// Adoption is incremental, editor by editor: an unmigrated editor keeps its
// `client *Client` field and nothing changes for it.

// Recorder is the capability an editor uses to publish its mutations into the
// shared musical space, stamped as the local author. Mutations published here
// replicate to every client (including this one — local application happens
// when the instruction is dispatched back through musicalImpl).
type Recorder interface {
	// publishSculpt stamps the local author onto brush and records it.
	// It does NOT stamp a Timing or an undo entry — editors whose sculpts
	// participate in undo go through Client.commitSculpt instead.
	publishSculpt(brush musical.Sculpt) error

	// emitSliderSculpt records a slider-amount Sculpt under editor/slider.
	emitSliderSculpt(editor, slider string, value float64, commit bool)

	// emitDesignSculpt records a committed design-selection Sculpt under
	// editor/slider, registering an Import for the resource if it's new.
	emitDesignSculpt(editor, slider, resource string)

	// publishChange stamps the local author onto change and records it.
	publishChange(change musical.Change) error

	// NextEntity reserves the next entity id authored by this client.
	NextEntity() musical.Entity

	// recording reports whether the shared space is connected yet. Editors
	// check this before side-effecting allocation (NextEntity/MusicalDesign)
	// for a mutation that couldn't be recorded anyway.
	recording() bool

	// workID identifies the work being edited — used as a cache key for
	// derived state (e.g. the critter snapshot).
	workID() musical.WorkID

	// localAuthor is the author this client's mutations are stamped with.
	// Editors only need it when a published Change's value is reused (e.g.
	// to build the matching undo record); publishing itself stamps it.
	localAuthor() musical.Author

	// RecordChange / RecordChangeGroup push an already-published Change
	// (or a grouped run of them) onto the undo stack with its inverse.
	RecordChange(do, undo musical.Change)
	RecordChangeGroup(dos, undos []musical.Change)
}

// Library resolves between library resource URIs and the numeric
// musical.Design references that appear on the wire and in saves.
type Library interface {
	// MusicalDesign returns the design reference for a resource URI,
	// allocating one (and recording an Import) on first sight.
	MusicalDesign(resource string) musical.Design

	// designURI is the reverse mapping. Empty when the design's Import
	// hasn't been observed yet (e.g. instruction reordering during replay).
	designURI(design musical.Design) string

	// resolveMaterialTexture turns a material-selection design into a
	// usable texture (plain image or shared-material atlas region).
	resolveMaterialTexture(design musical.Design) Texture2D.Instance

	// sceneFor returns the loaded PackedScene for a design, if available.
	sceneFor(design musical.Design) (PackedScene.Instance, bool)

	// instantiateDesign instantiates a design's scene (with the library's
	// per-model processing applied), ready to be parented.
	instantiateDesign(design musical.Design) Node3D.Instance

	// libraryOverrideFor / applyLibrarySizeOverride expose the
	// library-sizing debug mode (sizes.txt measurement workflow).
	libraryOverrideFor(design musical.Design) (override librarySizeOverride, model string, listed bool)
	applyLibrarySizeOverride(entity musical.Entity, design musical.Design, node Node3D.Instance, terrainSeated bool)
}

// Workbench is the slice of the UI shell an editor may observe and adjust.
type Workbench interface {
	SetGizmos(gizmos []Gizmo)

	// uiMode is the current geometry/dressing/material mode.
	uiMode() Mode

	// editing is the currently active editor subject.
	editing() Subject

	// selectedNode resolves the current viewport selection, if any.
	selectedNode() (Node3D.Instance, bool)

	// currentView / refreshViewSelector observe and repopulate the
	// view-selector strip (used by editors with dynamic views, e.g.
	// shelter's per-storey levels).
	currentView() int
	refreshViewSelector(view int, views []string)
}

// CameraRig is the camera rig: the focal point the camera orbits, ray
// projection for cursor picking, and the fullscreen cover quad that editors
// may temporarily borrow (handing it back via applyCoverDefault). (Not named
// Viewport — that would shadow the graphics.gd/classdb/Viewport import.)
type CameraRig interface {
	focalNode() Node3D.Instance
	lensNode() Node3D.Instance
	viewportCamera() Camera3D.Instance
	setCameraCover(material Material.Instance)
	applyCoverDefault()

	// setMovementLocked freezes the user's camera-movement input while a
	// modal view (e.g. the critter chase-cam) drives the focal point itself.
	setMovementLocked(locked bool)

	// PreviewPicker raycasts from the active pointer (mouse projection on
	// desktop, right-controller aim in VR) into the scene.
	PreviewPicker() PhysicsDirectSpaceState3D.PhysicsDirectSpaceState3D_Intersection

	// xrPointer returns the VR aim controller, ok when XR is active and
	// the controller is present.
	xrPointer() (XRController3D.Instance, bool)
}

// LightingConsole drives the live world-lighting renderer state. It is the
// dependency of the embeddable lighting helper (see editor.go) rather than
// of editors directly.
type LightingConsole interface {
	ApplyLightingMenuState(timeOfDay, sunAngle, fog, clouds, rain, snow, wind, moon Float.X)
	GetLightingMenuState() (timeOfDay, sunAngle, fog, clouds, rain, snow, wind, moon Float.X)
}

// Client provides every editor capability.
var (
	_ Recorder        = (*Client)(nil)
	_ Library         = (*Client)(nil)
	_ Workbench       = (*Client)(nil)
	_ CameraRig       = (*Client)(nil)
	_ LightingConsole = (*Client)(nil)
)

// publishChange stamps the local author onto change and records it in the
// shared space (Timing is stamped by the stampedSpace wrapper on commit).
func (world *Client) publishChange(change musical.Change) error {
	if world.space == nil {
		return nil
	}
	change.Author = world.id
	return world.space.Change(change)
}

func (world *Client) uiMode() Mode     { return world.ui.mode }
func (world *Client) editing() Subject { return world.Editing }
func (world *Client) currentView() int { return world.ui.ViewSelector.view }
func (world *Client) refreshViewSelector(view int, views []string) {
	world.ui.ViewSelector.Refresh(view, views)
}
func (world *Client) selectedNode() (Node3D.Instance, bool) {
	return world.selection.Instance()
}

func (world *Client) recording() bool             { return world.space != nil }
func (world *Client) workID() musical.WorkID      { return world.record }
func (world *Client) localAuthor() musical.Author { return world.id }

func (world *Client) focalNode() Node3D.Instance { return world.FocalPoint.Instance }
func (world *Client) lensNode() Node3D.Instance  { return world.FocalPoint.Lens.Instance }
func (world *Client) viewportCamera() Camera3D.Instance {
	return world.FocalPoint.Lens.Camera.Instance
}
func (world *Client) setCameraCover(material Material.Instance) {
	world.FocalPoint.Lens.Camera.Cover.SetSurfaceOverrideMaterial(0, material)
}
func (world *Client) setMovementLocked(locked bool) { world.controlLockMovement = locked }
func (world *Client) xrPointer() (XRController3D.Instance, bool) {
	return world.xrRight, world.xr && world.xrRight != XRController3D.Nil
}
