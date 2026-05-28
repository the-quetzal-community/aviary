package internal

import (
	"fmt"

	"graphics.gd/classdb/BaseMaterial3D"
	"graphics.gd/classdb/BoxMesh"
	"graphics.gd/classdb/CylinderMesh"
	"graphics.gd/classdb/MeshInstance3D"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/StandardMaterial3D"
	"graphics.gd/classdb/Viewport"
	"graphics.gd/classdb/XRCamera3D"
	"graphics.gd/classdb/XRController3D"
	"graphics.gd/classdb/XRInterface"
	"graphics.gd/classdb/XROrigin3D"
	"graphics.gd/classdb/XRServer"
	"graphics.gd/variant/Angle"
	"graphics.gd/variant/Color"
	"graphics.gd/variant/Euler"
	"graphics.gd/variant/Vector2"
	"graphics.gd/variant/Vector3"
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
	world.xrCamera = camera
	world.xrLeft = left
	world.xrRight = right

	// Visible meshes for the controllers — Quest tracks pose but
	// renders nothing by itself. A small white box where each
	// controller is gives the user a "this is my hand" anchor.
	attachControllerVisual(left.AsNode3D(), Color.RGBA{R: 0.9, G: 0.9, B: 1.0, A: 1})
	attachControllerVisual(right.AsNode3D(), Color.RGBA{R: 0.9, G: 0.9, B: 1.0, A: 1})

	// Split the editor UI onto two wrist panels (left = CloudControl
	// + drawer, right = editor switcher + toolbar + trash), wire each
	// controller as a pointer at the OPPOSITE hand's panel, and tie
	// up locomotion (sticks) — see xr_ui.go.
	if world.ui != nil {
		world.attachUIToVR(world.ui, left.AsNode3D(), right.AsNode3D())
	}
	world.setupControllerPointers(left, right)

	fmt.Println("Aviary: OpenXR initialized, running in VR mode")
}

// attachControllerVisual hangs a small box mesh on a controller node
// so the user can see where each tracked hand is. The XRController3D
// node by itself has no geometry — Quest's Horizon OS renders nothing
// for it. Could be swapped for the vendor's controller-model glyphs
// later; a 3×3×8 cm box is enough orientation feedback for now.
func attachControllerVisual(parent Node3D.Instance, col Color.RGBA) {
	mesh := MeshInstance3D.New()
	box := BoxMesh.New()
	box.SetSize(Vector3.New(0.03, 0.03, 0.08))
	mesh.SetMesh(box.AsMesh())

	mat := StandardMaterial3D.New()
	mat.AsBaseMaterial3D().SetShadingMode(BaseMaterial3D.ShadingModeUnshaded)
	mat.AsBaseMaterial3D().SetAlbedoColor(col)
	mesh.AsGeometryInstance3D().SetMaterialOverride(mat.AsMaterial())

	parent.AsNode().AddChild(mesh.AsNode())
}

// attachRayLaser draws a long thin red cylinder along the controller's
// forward axis so the user can see where their pointer is aimed.
// Returns both the MeshInstance3D (for repositioning) and the
// CylinderMesh resource (for resizing the height) so processVRPointer
// can shrink the laser to the collision distance each frame.
func attachRayLaser(parent Node3D.Instance) (MeshInstance3D.Instance, CylinderMesh.Instance) {
	mesh := MeshInstance3D.New()
	cyl := CylinderMesh.New()
	cyl.SetTopRadius(0.002)
	cyl.SetBottomRadius(0.002)
	cyl.SetHeight(2.0)
	cyl.SetRadialSegments(8)
	mesh.SetMesh(cyl.AsMesh())

	mat := StandardMaterial3D.New()
	mat.AsBaseMaterial3D().SetShadingMode(BaseMaterial3D.ShadingModeUnshaded)
	mat.AsBaseMaterial3D().SetAlbedoColor(Color.RGBA{R: 1, G: 0.2, B: 0.2, A: 1})
	mat.AsBaseMaterial3D().SetEmissionEnabled(true)
	mat.AsBaseMaterial3D().SetEmission(Color.RGBA{R: 1, G: 0.2, B: 0.2, A: 1})
	mesh.AsGeometryInstance3D().SetMaterialOverride(mat.AsMaterial())

	// CylinderMesh's local Y axis is the cylinder's length. The
	// controller's aim points along -Z, so rotate -90° about X to
	// align the cylinder's Y with the controller's -Z, then shift
	// 1 m forward so the cylinder spans (0,0,0) → (0,0,-2).
	mesh.AsNode3D().SetRotation(Euler.Radians{X: -Angle.Pi / 2, Y: 0, Z: 0})
	mesh.AsNode3D().SetPosition(Vector3.New(0, 0, -1))

	parent.AsNode().AddChild(mesh.AsNode())
	return mesh, cyl
}

// silence "declared and not used" if Vector2 is only needed in
// xr_ui.go imports already.
var _ = Vector2.New[float32]
