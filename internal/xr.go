package internal

import (
	"fmt"

	"graphics.gd/classdb/Viewport"
	"graphics.gd/classdb/XRCamera3D"
	"graphics.gd/classdb/XRController3D"
	"graphics.gd/classdb/XRInterface"
	"graphics.gd/classdb/XROrigin3D"
	"graphics.gd/classdb/XRServer"
)

// setupXR attempts to bring up OpenXR. If a real OpenXR runtime is
// available (e.g. running on a Meta Quest), it parents an
// XROrigin3D + XRCamera3D under FocalPoint, wires two XRController3D
// nodes for left/right hands, switches the main viewport to XR
// rendering, hides the 2D editor UI overlay (it doesn't translate to
// VR), and flips world.xr so the desktop input/move paths short-circuit.
//
// On desktop (no OpenXR runtime), XRInterface.IsInitialized() returns
// false and this is a complete no-op — the existing FocalPoint /
// Lens / Camera3D chain keeps driving the view.
func (world *Client) setupXR() {
	iface := XRServer.FindInterface("OpenXR")
	if iface == XRInterface.Nil {
		return
	}
	// Try to bring the interface up if it isn't already. Initialize()
	// is a no-op when already initialized.
	if !iface.IsInitialized() {
		if !iface.Initialize() {
			return
		}
	}
	if !iface.IsInitialized() {
		return
	}

	origin := XROrigin3D.New()
	camera := XRCamera3D.New()
	origin.AsNode().AddChild(camera.AsNode())

	left := XRController3D.New()
	left.AsXRNode3D().SetTracker("left_hand")
	left.AsXRNode3D().SetPose("aim")
	origin.AsNode().AddChild(left.AsNode())

	right := XRController3D.New()
	right.AsXRNode3D().SetTracker("right_hand")
	right.AsXRNode3D().SetPose("aim")
	origin.AsNode().AddChild(right.AsNode())

	// Anchor the XR origin at the current focal point so the headset
	// starts looking at roughly the same scene region the desktop user
	// would have been seeing.
	world.FocalPoint.AsNode().AddChild(origin.AsNode())

	vp := Viewport.Get(world.AsNode())
	vp.SetUseXr(true)

	world.xr = true
	world.xrOrigin = origin
	world.xrLeft = left
	world.xrRight = right

	// Hoist the existing 2D editor overlay onto a quad parented to
	// the left controller, and wire a right-controller raycast +
	// trigger so the same buttons remain interactive. See xr_ui.go.
	if world.ui != nil {
		world.attachUIToVR(world.ui, left.AsNode3D())
	}
	world.setupControllerPointer(right)

	fmt.Println("Aviary: OpenXR initialized, running in VR mode")
}
