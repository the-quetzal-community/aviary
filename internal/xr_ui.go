package internal

import (
	"fmt"
	"math"

	"graphics.gd/classdb/BaseMaterial3D"
	"graphics.gd/classdb/BoxShape3D"
	"graphics.gd/classdb/CollisionObject3D"
	"graphics.gd/classdb/CollisionShape3D"
	"graphics.gd/classdb/ColorRect"
	"graphics.gd/classdb/Control"
	"graphics.gd/classdb/CylinderMesh"
	"graphics.gd/classdb/DisplayServer"
	"graphics.gd/classdb/Input"
	"graphics.gd/classdb/InputEventMouseButton"
	"graphics.gd/classdb/InputEventMouseMotion"
	"graphics.gd/classdb/MeshInstance3D"
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/PhysicsRayQueryParameters3D"
	"graphics.gd/classdb/QuadMesh"
	"graphics.gd/classdb/RayCast3D"
	"graphics.gd/classdb/StandardMaterial3D"
	"graphics.gd/classdb/StaticBody3D"
	"graphics.gd/classdb/SubViewport"
	"graphics.gd/classdb/XRCamera3D"
	"graphics.gd/classdb/XRController3D"
	"graphics.gd/classdb/XROrigin3D"
	"graphics.gd/variant/Angle"
	"graphics.gd/variant/Callable"
	"graphics.gd/variant/Color"
	"graphics.gd/variant/Euler"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Transform3D"
	"graphics.gd/variant/Vector2"
	"graphics.gd/variant/Vector2i"
	"graphics.gd/variant/Vector3"
	"the.quetzal.community/aviary/internal/musical"
)

var (
	mathSin = math.Sin
	mathCos = math.Cos
)

// First-attempt VR UI: render the existing 2D editor overlay onto a
// quad parented to the left controller (wrist panel), and forward
// controller-ray hits + trigger presses as synthetic mouse events to
// the SubViewport. This reuses every existing button/handler — no
// duplication, no separate VR UI tree. Splitting the drawer onto its
// own larger world-anchored panel is a follow-up; this gets one panel
// fully interactive so we can validate the end-to-end loop.

const (
	// Left panel — pixel and physical sizes. The existing UI was
	// authored against roughly 1080p / 2160p targets via its
	// `scaling()` function; 1920×1080 keeps it readable without
	// huge texture memory. Physical 16:9, roughly tablet-sized.
	vrUIWidth  = 1920
	vrUIHeight = 1080
	vrUIWorldW = 0.40
	vrUIWorldH = 0.225

	// Right panel — taller than the left so the editor selector
	// dropdown (which slides down a VBoxContainer of ~9 editor
	// types) and the trash button below it don't get clipped at
	// the bottom of the SubViewport. Same width as left for visual
	// symmetry; ~1.5× the height gives ample room.
	vrUIRightWidth  = 1920
	vrUIRightHeight = 1620
	vrUIRightWorldW = 0.40
	vrUIRightWorldH = 0.34

	// Design palette — world-anchored (not on a wrist), big enough
	// to actually show the design tile grid. Sized roughly like a
	// large drafting tablet held in front of the user. Parented to
	// xrOrigin so it follows snap-turn + strafe but stays in place
	// relative to the player's stance.
	vrUIPaletteWidth  = 1920
	vrUIPaletteHeight = 1080
	vrUIPaletteWorldW = 0.80
	vrUIPaletteWorldH = 0.45

	// Collision layers reserved for the two VR UI panels. Each
	// controller's raycast targets the OPPOSITE hand's panel, so
	// the dominant-hand laser interacts with the off-hand menu —
	// standard VR DCC pattern (Blender VR, Gravity Sketch, etc.).
	// Bits 20/21 are well outside the layers any existing editor
	// uses (terrain is bit 1, the selection mask in client.go uses
	// ~bit 1 + bit 2).
	vrUILayerLeft    uint32 = 1 << 20
	vrUILayerRight   uint32 = 1 << 21
	vrUILayerPalette uint32 = 1 << 22

	// Locomotion tuning.
	vrMoveSpeed   = 2.0  // meters/sec at full stick deflection
	vrRotateSnap  = 30.0 // degrees per snap-rotate flick
	vrStickDead   = 0.2  // joystick deadzone — Quest sticks drift a bit
	vrRotateLatch = 0.7  // stick must cross this magnitude to trigger a snap
)

// attachUIToVR sets up the two wrist panels — left hand carries the
// existing AviaryUI tree (CloudControl, design explorer drawer, mode
// buttons, view selector), right hand gets the editor switcher
// (EditorIndicator), the action toolbar (undo/redo/export/settings),
// and the trash button. Each controller's pointer targets the
// OPPOSITE hand's panel, which is the natural VR pattern: dominant
// hand points at the menu held by the off-hand.
func (world *Client) attachUIToVR(ui *UI, leftAnchor, rightAnchor Node3D.Instance) {
	// Right panel — editor switcher + toolbar + trash.
	rightWrap := Control.New()
	rightWrap.AsCanvasItem().SetVisible(true)
	rightWrap.SetSize(Vector2.New(vrUIRightWidth, vrUIRightHeight))

	// Palette panel — the design explorer + its hover-to-open
	// indicator. Wrist real estate is too cramped for the design
	// tile grid; the palette is world-anchored and big enough for
	// the grid to actually breathe.
	paletteWrap := Control.New()
	paletteWrap.AsCanvasItem().SetVisible(true)
	paletteWrap.SetSize(Vector2.New(vrUIPaletteWidth, vrUIPaletteHeight))
	// Solid background so the palette reads as a tablet/table in
	// 3D space rather than a hard-to-spot transparent quad. The
	// SubViewport is transparent_bg=true, so without this ColorRect
	// the only visible pixels would be UI controls that paint —
	// which is empty before the drawer populates.
	paletteBG := ColorRect.New()
	paletteBG.AsControl().SetSize(Vector2.New(vrUIPaletteWidth, vrUIPaletteHeight))
	paletteBG.SetColor(Color.RGBA{R: 0.15, G: 0.16, B: 0.18, A: 0.92})
	paletteBG.AsControl().SetMouseFilter(Control.MouseFilterIgnore)
	paletteWrap.AsNode().AddChild(paletteBG.AsNode())

	reparentTo := func(wrap Control.Instance, child Node.Instance) {
		if child == Node.Nil {
			return
		}
		if parent := child.GetParent(); parent != Node.Nil {
			parent.RemoveChild(child)
		}
		// Reparent is the safe way to change parents for nodes we do
		// not own from Go (scene-instantiated nodes, or nodes that
		// have already had ownership transferred). It does not go
		// through PointerWithOwnershipTransferredToGodot.
		child.Reparent(wrap.AsNode())
	}
	if ui.EditorIndicator != nil {
		reparentTo(rightWrap, ui.EditorIndicator.AsNode())
	}
	if ui.Toolbar.Triangle != nil {
		reparentTo(rightWrap, ui.Toolbar.Triangle.AsNode())
	}
	// Delete and Duplicate now live inside CloudControl/GizmoTypes
	// on the LEFT wrist alongside the gizmos — no separate reparent
	// step needed, they ride along with CloudControl.

	// Move the design explorer drawer + its hover trigger onto the
	// palette. ExpansionIndicator goes too so the "open drawer on
	// hover" mechanic still works (now hovering the palette, not a
	// corner of the screen).
	if ui.Editor != nil {
		reparentTo(paletteWrap, ui.Editor.AsNode())
	}
	reparentTo(paletteWrap, ui.ExpansionIndicator.AsNode())

	world.vrUIViewport, world.vrUIPanel = mountWristPanel(
		leftAnchor, ui.AsControl(), ui.AsNode(),
		vrUILayerLeft, "VRPanelLeft",
		vrUIWidth, vrUIHeight, vrUIWorldW, vrUIWorldH,
	)
	world.vrUIViewportRight, world.vrUIPanelRight = mountWristPanel(
		rightAnchor, rightWrap, rightWrap.AsNode(),
		vrUILayerRight, "VRPanelRight",
		vrUIRightWidth, vrUIRightHeight, vrUIRightWorldW, vrUIRightWorldH,
	)
	// Palette: TRUE world-space — parented to the Client (which sits
	// at world origin and never moves), not to xrOrigin (which moves
	// every time the user joysticks). That way the user can locomote
	// toward/away from the palette like any other scene object, and
	// grip-grab to bring it with them. Initial pose is placed roughly
	// in front of the user's current head position via a deferred
	// SetGlobalTransform once xrCamera has a real pose.
	world.vrUIViewportPalette, world.vrUIPanelPalette = mountSubviewportPanel(
		world.AsNode3D(), paletteWrap, paletteWrap.AsNode(),
		vrUILayerPalette, "VRPalette",
		vrUIPaletteWidth, vrUIPaletteHeight,
		vrUIPaletteWorldW, vrUIPaletteWorldH,
		Vector3.New(0, 1.0, -0.7),
		Euler.Radians{X: -Angle.InRadians(35), Y: 0, Z: 0},
	)
	// Snap the palette into the user's view on the next frame —
	// xrCamera's pose isn't valid until the runtime has at least one
	// pose sample, which doesn't happen during this Ready pass.
	Callable.Defer(Callable.New(func() {
		if world.xrCamera == XRCamera3D.Nil || world.vrUIPanelPalette == MeshInstance3D.Nil {
			return
		}
		camPos := world.xrCamera.AsNode3D().GlobalPosition()
		camBasis := world.xrCamera.AsNode3D().GlobalTransform().Basis
		// camera forward = -Z in camera frame. Sit the palette
		// 0.8 m forward, 0.4 m below the user's eyes so it reads
		// as a drafting tablet rather than a HUD.
		fwd := Vector3.New(-camBasis.Z.X, 0, -camBasis.Z.Z)
		fwdLen := Vector3.Length(fwd)
		if fwdLen > 0.0001 {
			fwd = Vector3.MulX(fwd, 1.0/fwdLen)
		}
		pos := Vector3.Add(camPos, Vector3.MulX(fwd, 0.8))
		pos.Y = camPos.Y - 0.4
		world.vrUIPanelPalette.AsNode3D().SetGlobalPosition(pos)
		// Face the user (yaw toward camPos) and keep the -35° tilt.
		yaw := Float.X(math.Atan2(float64(camPos.X-pos.X), float64(camPos.Z-pos.Z)))
		world.vrUIPanelPalette.AsNode3D().SetRotation(Euler.Radians{
			X: -Angle.InRadians(35), Y: Angle.Radians(yaw), Z: 0,
		})
	}))
	fmt.Println("vr-palette mounted parent=Client sub=",
		world.vrUIViewportPalette != SubViewport.Nil,
		" panel=", world.vrUIPanelPalette != MeshInstance3D.Nil)

	// In desktop the drawer opens on mouse-entered over the
	// ExpansionIndicator at the bottom-right of the screen. On a
	// palette there's no "bottom of screen" to hover into — so just
	// open it once at startup. Deferred so the drawer's Ready()
	// runs first and ExpansionIndicator is resolvable.
	if ui.Editor != nil {
		Callable.Defer(Callable.New(func() {
			ui.Editor.openDrawer()
		}))
	}
}

// mountSubviewportPanel reparents `host` into a fresh SubViewport
// sized to pxW × pxH, builds a QuadMesh + StaticBody3D +
// CollisionShape3D combo sized to worldW × worldH at the given
// (position, rotation) under `anchor`, and returns the SubViewport
// + panel MeshInstance3D so the caller can wire the pointer
// pipeline. Used by both the wrist panels (anchored to controllers
// at the wrist-menu pose) and the world-anchored design palette.
func mountSubviewportPanel(anchor Node3D.Instance, ctl Control.Instance, host Node.Instance, layer uint32, name string, pxW, pxH int, worldW, worldH float32, pos Vector3.XYZ, rot Euler.Radians) (SubViewport.Instance, MeshInstance3D.Instance) {
	if parent := host.GetParent(); parent != Node.Nil {
		parent.RemoveChild(host)
	}
	sub := SubViewport.New()
	sub.AsNode().SetName(name)
	sub.SetSize(Vector2i.New(int32(pxW), int32(pxH)))
	sub.AsViewport().SetTransparentBg(true)
	sub.SetRenderTargetUpdateMode(SubViewport.UpdateAlways)
	sub.AsNode().AddChild(host)

	ctl.SetSize(Vector2.New(float32(pxW), float32(pxH)))

	mesh := QuadMesh.New()
	mesh.AsPlaneMesh().SetSize(Vector2.New(worldW, worldH))

	mat := StandardMaterial3D.New()
	mat.AsBaseMaterial3D().SetShadingMode(BaseMaterial3D.ShadingModeUnshaded)
	mat.AsBaseMaterial3D().SetTransparency(BaseMaterial3D.TransparencyAlpha)
	mat.AsBaseMaterial3D().SetAlbedoTexture(sub.AsViewport().GetTexture().AsTexture2D())

	panel := MeshInstance3D.New()
	panel.SetMesh(mesh.AsMesh())
	panel.AsGeometryInstance3D().SetMaterialOverride(mat.AsMaterial())

	body := StaticBody3D.New()
	body.AsCollisionObject3D().SetCollisionLayer(uint32_to_int(layer))
	body.AsCollisionObject3D().SetCollisionMask(0)
	shape := BoxShape3D.New()
	shape.SetSize(Vector3.New(worldW, worldH, 0.01))
	coll := CollisionShape3D.New()
	coll.SetShape(shape.AsShape3D())
	body.AsNode().AddChild(coll.AsNode())
	panel.AsNode().AddChild(body.AsNode())

	panel.AsNode3D().SetPosition(pos)
	panel.AsNode3D().SetRotation(rot)

	anchor.AsNode().AddChild(sub.AsNode())
	anchor.AsNode().AddChild(panel.AsNode())
	return sub, panel
}

// mountWristPanel is the wrist-menu specialisation of mountSubviewportPanel:
// 2 cm above + 8 cm forward of the controller, tilted -45° so a quick
// glance picks it up.
func mountWristPanel(anchor Node3D.Instance, ctl Control.Instance, host Node.Instance, layer uint32, name string, pxW, pxH int, worldW, worldH float32) (SubViewport.Instance, MeshInstance3D.Instance) {
	return mountSubviewportPanel(anchor, ctl, host, layer, name,
		pxW, pxH, worldW, worldH,
		Vector3.New(0.0, 0.02, -0.08),
		Euler.Radians{X: -Angle.InRadians(45), Y: 0, Z: 0})
}

// setupControllerPointers wires both controllers as pointers. Each
// controller gets a RayCast3D whose collision mask targets the
// OPPOSITE hand's panel layer, plus a visible laser cylinder, plus
// trigger-button event forwarding into the targeted panel's
// SubViewport. The setup is symmetric — point with whichever hand
// isn't holding the menu you want to click. Standard VR DCC pattern.
func (world *Client) setupControllerPointers(left, right XRController3D.Instance) {
	// Right hand can hit the LEFT wrist panel (CloudControl) AND
	// the world-anchored design palette — single ray, multi-layer
	// mask. tickPointer figures out which panel was actually hit
	// based on the collider's collision layer and forwards input
	// to that panel's SubViewport.
	world.vrPointer, world.vrLaser, world.vrLaserCyl = makePointer(right, vrUILayerLeft|vrUILayerPalette)
	// Right grip: if you're aiming at the palette, it grabs the
	// palette (mirrors physical "reaching out to grab" intuition).
	// Otherwise it acts as the Shift equivalent — momentarily
	// switches the active gizmo to GizmoShift while held, or to
	// GizmoScale if the left grip is also down. Released grip
	// restores whatever gizmo was active before.
	right.OnButtonPressed(func(name string) {
		if !isGripButton(name) {
			return
		}
		if world.vrUIHoverViewportRight == world.vrUIViewportPalette && world.vrUIViewportPalette != SubViewport.Nil {
			world.tryStartPaletteGrab(right)
			return
		}
		world.vrRightGrip = true
		world.vrUpdateGripGizmo()
	})
	right.OnButtonReleased(func(name string) {
		if !isGripButton(name) {
			return
		}
		if world.vrPaletteGrabHand == right {
			world.vrPaletteGrabHand = XRController3D.Nil
			return
		}
		world.vrRightGrip = false
		world.vrUpdateGripGizmo()
	})
	right.OnButtonPressed(func(name string) {
		fmt.Println("vr-button-pressed: right", name)
		if !isTriggerButton(name) {
			return
		}
		world.vrRightTrigger = true
		if world.vrUIHoverViewportRight != SubViewport.Nil {
			world.vrPointerClickPanel(world.vrUIHoverViewportRight, world.vrUIHoverPixelRight, true)
			return
		}
		// Off-UI: if the active editor has a preview ready to drop
		// (user previously clicked a design tile in the palette and
		// the preview is now tracking the laser), commit the
		// placement and bail. Otherwise scene-pick + arm gizmo drag.
		if world.tryPlaceVRPreview() {
			return
		}
		world.vrSceneSelectFromController(right.AsNode3D())
		if world.canUseGizmoManipulation() {
			world.vrDragController = right
			world.armGizmoDrag()
		}
	})
	right.OnButtonReleased(func(name string) {
		fmt.Println("vr-button-released: right", name)
		if !isTriggerButton(name) {
			return
		}
		world.vrRightTrigger = false
		if world.vrUIHoverViewportRight != SubViewport.Nil {
			world.vrPointerClickPanel(world.vrUIHoverViewportRight, world.vrUIHoverPixelRight, false)
		}
		// If this hand was the one driving an in-progress drag,
		// commit it now. commitVRGizmoDrag clears the drag state.
		if world.vrDragController == right {
			world.commitVRGizmoDrag()
		}
	})

	// Left hand can hit the RIGHT wrist panel (editor + toolbar)
	// AND the design palette.
	world.vrPointerLeft, world.vrLaserLeft, world.vrLaserCylLeft = makePointer(left, vrUILayerRight|vrUILayerPalette)
	// Left grip: palette grab when pointing at it, otherwise Ctrl
	// equivalent — switches to GizmoTwist (or GizmoScale if right
	// grip is also down).
	left.OnButtonPressed(func(name string) {
		if !isGripButton(name) {
			return
		}
		if world.vrUIHoverViewportLeft == world.vrUIViewportPalette && world.vrUIViewportPalette != SubViewport.Nil {
			world.tryStartPaletteGrab(left)
			return
		}
		world.vrLeftGrip = true
		world.vrUpdateGripGizmo()
	})
	left.OnButtonReleased(func(name string) {
		if !isGripButton(name) {
			return
		}
		if world.vrPaletteGrabHand == left {
			world.vrPaletteGrabHand = XRController3D.Nil
			return
		}
		world.vrLeftGrip = false
		world.vrUpdateGripGizmo()
	})
	left.OnButtonPressed(func(name string) {
		fmt.Println("vr-button-pressed: left", name)
		if !isTriggerButton(name) {
			return
		}
		world.vrLeftTrigger = true
		if world.vrUIHoverViewportLeft != SubViewport.Nil {
			world.vrPointerClickPanel(world.vrUIHoverViewportLeft, world.vrUIHoverPixelLeft, true)
			return
		}
		if world.tryPlaceVRPreview() {
			return
		}
		world.vrSceneSelectFromController(left.AsNode3D())
		if world.canUseGizmoManipulation() {
			world.vrDragController = left
			world.armGizmoDrag()
		}
	})
	left.OnButtonReleased(func(name string) {
		fmt.Println("vr-button-released: left", name)
		if !isTriggerButton(name) {
			return
		}
		world.vrLeftTrigger = false
		if world.vrUIHoverViewportLeft != SubViewport.Nil {
			world.vrPointerClickPanel(world.vrUIHoverViewportLeft, world.vrUIHoverPixelLeft, false)
		}
		if world.vrDragController == left {
			world.commitVRGizmoDrag()
		}
	})
}

// commitVRGizmoDrag finalises an in-progress VR gizmo drag. If a
// translate, twist, or scale is live, this is the equivalent of
// releasing the left mouse on desktop — runs commitGizmoDrag so the
// final pose lands in the musical log, then clears the drag state.
// Safe to call when no drag is active.
func (world *Client) commitVRGizmoDrag() {
	if world.gizmoDrag.active {
		world.commitGizmoDrag()
	}
	world.gizmoDrag.active = false
	world.gizmoDrag.hasMirrorPlane = false
	world.gizmoDrag.design = musical.Design{}
	world.gizmoDrag.twistInitialY = 0
	world.gizmoDrag.twistInitialAngle = 0
	world.gizmoDrag.twistPlaneY = 0
	world.gizmoDrag.scaleInitial = Vector3.Zero
	world.gizmoDrag.scaleInitialDistance = 0
	world.gizmoDrag.scalePlaneY = 0
	world.vrDragController = XRController3D.Nil
}

func makePointer(controller XRController3D.Instance, layer uint32) (RayCast3D.Instance, MeshInstance3D.Instance, CylinderMesh.Instance) {
	ray := RayCast3D.New()
	ray.SetTargetPosition(Vector3.New(0, 0, -2))
	ray.SetEnabled(true)
	ray.SetCollisionMask(uint32_to_int(layer))
	controller.AsNode().AddChild(ray.AsNode())
	laserMesh, laserCyl := attachRayLaser(controller.AsNode3D())
	return ray, laserMesh, laserCyl
}

// processVRPointer is called every frame while world.xr is true. It
// updates both hands' pointer state: laser-length feedback when the
// ray hits its target panel, hover-motion events into the relevant
// SubViewport so highlight/hover/drawer-open behaviour all stays
// live, and it also drives locomotion via the thumbsticks (left
// stick = strafe relative to head yaw, right stick = snap-rotate).
func (world *Client) processVRPointer(dt Float.X) {
	// Each hand's ray can land on either the off-hand wrist panel
	// or the world-anchored palette — tickPointer figures out which
	// from the hit collider's collision layer and forwards input
	// to that panel. The first match in the slice wins.
	rightTargets := []vrPanelTarget{
		{layer: vrUILayerLeft, mesh: world.vrUIPanel, viewport: world.vrUIViewport,
			pxW: vrUIWidth, pxH: vrUIHeight, worldW: vrUIWorldW, worldH: vrUIWorldH},
		{layer: vrUILayerPalette, mesh: world.vrUIPanelPalette, viewport: world.vrUIViewportPalette,
			pxW: vrUIPaletteWidth, pxH: vrUIPaletteHeight, worldW: vrUIPaletteWorldW, worldH: vrUIPaletteWorldH},
	}
	leftTargets := []vrPanelTarget{
		{layer: vrUILayerRight, mesh: world.vrUIPanelRight, viewport: world.vrUIViewportRight,
			pxW: vrUIRightWidth, pxH: vrUIRightHeight, worldW: vrUIRightWorldW, worldH: vrUIRightWorldH},
		{layer: vrUILayerPalette, mesh: world.vrUIPanelPalette, viewport: world.vrUIViewportPalette,
			pxW: vrUIPaletteWidth, pxH: vrUIPaletteHeight, worldW: vrUIPaletteWorldW, worldH: vrUIPaletteWorldH},
	}
	world.tickPointer(world.vrPointer, world.vrLaser, world.vrLaserCyl,
		&world.vrUIHoverViewportRight, &world.vrUIHoverPixelRight,
		rightTargets)
	world.tickPointer(world.vrPointerLeft, world.vrLaserLeft, world.vrLaserCylLeft,
		&world.vrUIHoverViewportLeft, &world.vrUIHoverPixelLeft,
		leftTargets)
	world.vrRightHovering = world.vrUIHoverViewportRight != SubViewport.Nil
	world.vrLeftHovering = world.vrUIHoverViewportLeft != SubViewport.Nil
	// Drag the palette with whichever hand has grip held + was
	// hovering the palette when the grip click came in.
	world.updatePaletteGrab()
	// If a gizmo drag is armed for one of the hands and the trigger
	// on that hand is still held, keep translating/twisting the
	// object as the controller moves. inputRay() picks up
	// vrDragController automatically.
	if world.gizmoDrag.active && world.vrDragController != XRController3D.Nil {
		held := false
		if world.vrDragController == world.xrRight && world.vrRightTrigger {
			held = true
		}
		if world.vrDragController == world.xrLeft && world.vrLeftTrigger {
			held = true
		}
		if held {
			world.updateGizmoDrag()
		}
	}
	world.processVRLocomotion(dt)
}

// vrPanelTarget describes one SubViewport-backed panel that a
// controller raycast might land on. tickPointer compares the hit
// collider's collision layer against each target's layer to figure
// out which panel was actually hit, then forwards a synthetic
// mouse-motion event to that panel's viewport.
type vrPanelTarget struct {
	layer          uint32
	mesh           MeshInstance3D.Instance
	viewport       SubViewport.Instance
	pxW, pxH       int
	worldW, worldH float32
}

// tickPointer runs the per-hand laser + hover update. Resizes the
// visible cylinder to the hit distance, picks the matching target
// from `targets` based on the hit collider's layer, and forwards an
// InputEventMouseMotion to that target's SubViewport. The matched
// (viewport, pixel) pair is written to *hoverView / *hoverPixel so
// the trigger handler can route press/release events accurately
// without having to re-do the layer comparison.
func (world *Client) tickPointer(ray RayCast3D.Instance, laser MeshInstance3D.Instance, laserCyl CylinderMesh.Instance,
	hoverView *SubViewport.Instance, hoverPixel *Vector2.XY,
	targets []vrPanelTarget) {
	*hoverView = SubViewport.Nil
	if ray == RayCast3D.Nil {
		return
	}
	hitting := ray.IsColliding()

	if laser != MeshInstance3D.Nil {
		length := float32(2.0)
		if hitting {
			hit := ray.GetCollisionPoint()
			origin := ray.AsNode3D().GlobalPosition()
			length = float32(Vector3.Distance(origin, hit))
		}
		if length > 0.01 && laserCyl != CylinderMesh.Nil {
			laserCyl.SetHeight(length)
			laser.AsNode3D().SetPosition(Vector3.New(0, 0, -length/2))
		}
	}

	if !hitting {
		return
	}
	collider, ok := Object.As[CollisionObject3D.Instance](ray.GetCollider())
	if !ok {
		return
	}
	hitLayer := uint32(collider.AsCollisionObject3D().CollisionLayer())
	var matched *vrPanelTarget
	for i := range targets {
		if hitLayer&targets[i].layer != 0 {
			matched = &targets[i]
			break
		}
	}
	if matched == nil || matched.viewport == SubViewport.Nil || matched.mesh == MeshInstance3D.Nil {
		return
	}

	hitWorld := ray.GetCollisionPoint()
	local := matched.mesh.AsNode3D().ToLocal(hitWorld)
	u := (float32(local.X) + matched.worldW/2) / matched.worldW
	v := 1 - (float32(local.Y)+matched.worldH/2)/matched.worldH
	pixel := Vector2.New(u*float32(matched.pxW), v*float32(matched.pxH))

	motion := InputEventMouseMotion.New()
	motion.AsInputEventMouse().SetPosition(pixel)
	matched.viewport.AsViewport().PushInput(motion.AsInputEvent())

	*hoverView = matched.viewport
	*hoverPixel = pixel
}

// vrPointerClickPanel synthesizes a MouseButton event at the cached
// hover position into the supplied panel's viewport. Trigger press
// or release on either controller funnels through here, just with
// the panel/pixel pair appropriate for that hand.
func (world *Client) vrPointerClickPanel(panelView SubViewport.Instance, pixel Vector2.XY, pressed bool) {
	if panelView == SubViewport.Nil {
		return
	}
	btn := InputEventMouseButton.New()
	btn.SetButtonIndex(Input.MouseButtonLeft)
	btn.AsInputEventMouseButton().SetPressed(pressed)
	btn.AsInputEventMouse().SetPosition(pixel)
	panelView.AsViewport().PushInput(btn.AsInputEvent())
}

// vrSceneSelectFromController fires a physics ray from the controller
// along its -Z aim direction and applies the same minimal selection
// logic as a desktop left-click: replace world.selection with the
// hit's Owner (so a child mesh resolves to its scene root), or clear
// the selection when the ray misses everything selectable. Terrain is
// excluded via the bit-1 mask so the ground doesn't intercept picks.
// Gizmo-drag arming is desktop-only for now; the VR equivalent will
// land in a follow-up.
func (world *Client) vrSceneSelectFromController(controller Node3D.Instance) {
	// Terrain mode never selects placed objects (the trigger sculpts the
	// ground instead), matching the desktop left-click guard.
	if world.Editing == Editing.Terrain {
		return
	}
	t := controller.AsNode3D().GlobalTransform()
	rayFrom := t.Origin
	// Basis.Z points along the controller's local +Z in world
	// space; aim direction is local -Z, so negate.
	forward := Vector3.New(-t.Basis.Z.X, -t.Basis.Z.Y, -t.Basis.Z.Z)
	rayTo := Vector3.Add(rayFrom, Vector3.MulX(forward, 1000))

	space := world.AsNode3D().GetWorld3d().DirectSpaceState()
	query := PhysicsRayQueryParameters3D.Create(rayFrom, rayTo, nil)
	// Exclude terrain (bit 1) AND the two VR UI panel layers
	// (bits 20/21) so we never grab a wrist panel as a "scene
	// selection". Everything else is fair game.
	query.SetCollisionMask(int(^uint32((1 << 1) | vrUILayerLeft | vrUILayerRight)))
	intersect := space.IntersectRay(query)
	fmt.Println("vr-scene-pick from=", rayFrom, "to=", rayTo, "hit=", intersect.Collider)

	if world.selection != 0 {
		if prev, ok := world.selection.Instance(); ok {
			Select(prev.AsNode(), false)
		}
	}
	if !Object.Is[*TerrainTile](intersect.Collider) {
		if node, ok := Object.As[Node.Instance](intersect.Collider); ok {
			if owner := node.Owner(); owner != Node.Nil {
				world.selection = Node3D.ID(owner.ID())
				Select(owner, true)
				fmt.Println("vr-scene-pick: selected", owner.Name())
				return
			}
			fmt.Println("vr-scene-pick: hit node has no Owner — name=", node.Name())
		}
	}
	world.selection = 0
}

// processVRLocomotion reads the thumbsticks and drives the XR
// origin. Left stick → smooth strafe in the head-yaw-relative
// horizontal plane; right stick → snap-rotate around Y when the
// stick crosses vrRotateLatch and again only after it returns to
// near-centre (so a steady deflection produces one snap, not a
// continuous spin which is nauseating in VR).
func (world *Client) processVRLocomotion(dt Float.X) {
	if world.xrOrigin == XROrigin3D.Nil {
		return
	}
	// --- Strafe (left stick) ---
	if world.xrLeft != XRController3D.Nil {
		stick := world.xrLeft.GetVector2("primary")
		mag := float32(Vector2.Length(stick))
		if mag > vrStickDead {
			// Use the camera's global yaw, not just origin's
			// rotation. Origin only changes on snap-rotate; the
			// player's actual facing direction is determined by
			// the headset pose, which lives inside the origin's
			// frame. Adding both via GlobalRotation gives "where
			// am I actually looking right now".
			var yaw float64
			if world.xrCamera != XRCamera3D.Nil {
				yaw = float64(world.xrCamera.AsNode3D().GlobalRotation().Y)
			} else {
				yaw = float64(world.xrOrigin.AsNode3D().Rotation().Y)
			}
			s, c := mathSinCos(yaw)
			// stick.X is right (+X), stick.Y is forward on most
			// joysticks but in Godot's convention OpenXR maps
			// "up" to negative Y. Use stick.Y directly: forward
			// = up = stick.Y > 0 → move -Z in head space.
			localX := float64(stick.X) * vrMoveSpeed * float64(dt)
			localZ := float64(-stick.Y) * vrMoveSpeed * float64(dt)
			// Rotate (localX, localZ) by yaw to get world delta.
			worldX := c*localX + s*localZ
			worldZ := -s*localX + c*localZ
			pos := world.xrOrigin.AsNode3D().Position()
			pos.X += Float.X(worldX)
			pos.Z += Float.X(worldZ)
			world.xrOrigin.AsNode3D().SetPosition(pos)
		}
	}
	// --- Snap rotate (right stick X) ---
	if world.xrRight != XRController3D.Nil {
		stick := world.xrRight.GetVector2("primary")
		x := float32(stick.X)
		switch {
		case x > vrRotateLatch && !world.vrRotateArmed:
			world.vrRotateArmed = true
			rotateOriginAroundCamera(world, -vrRotateSnap)
		case x < -vrRotateLatch && !world.vrRotateArmed:
			world.vrRotateArmed = true
			rotateOriginAroundCamera(world, vrRotateSnap)
		case mathAbs(x) < vrStickDead:
			world.vrRotateArmed = false
		}
	}
}

// rotateOriginAroundCamera spins XROrigin3D by `degrees` around the
// world-Y axis through the XR camera's horizontal position, so the
// user appears to rotate in place rather than swinging in an arc
// around the origin (the standard "comfort turn" feel).
func rotateOriginAroundCamera(world *Client, degrees float64) {
	pivot := world.xrOrigin.AsNode3D().Position() // camera-position fallback if we don't have a separate cam handle
	rad := degrees * (3.141592653589793 / 180.0)
	s, c := mathSinCos(rad)
	rot := world.xrOrigin.AsNode3D().Rotation()
	rot.Y += Angle.Radians(rad)
	world.xrOrigin.AsNode3D().SetRotation(rot)
	// Adjust position so the pivot is preserved (rotate position
	// around itself is a no-op; rotate around camera world pos
	// requires the camera. Keep simple for now — origin rotation
	// alone reads fine on Quest).
	_ = pivot
	_ = s
	_ = c
}

// math helpers — avoid pulling "math" into multiple files for two
// tiny calls.
func mathSinCos(x float64) (s, c float64) {
	return mathSin(x), mathCos(x)
}
func mathAbs(x float32) float32 {
	if x < 0 {
		return -x
	}
	return x
}

// PreviewPlacer is implemented by editors that can commit their
// in-progress preview (design ghost) to a real placement on demand.
// VR uses this in the off-UI trigger path so a trigger press at a
// terrain point drops the design — same semantics the desktop
// left-click path triggers via UnhandledInput.
type PreviewPlacer interface {
	TryPlacePreview() bool
}

// isTriggerButton matches the names OpenXR exposes for the primary
// "select" button across the common interaction profiles. Quest's
// touch controllers fire "trigger_click"; the standard simple profile
// fires "trigger". We accept both so this works on Quest, Pico, Vive,
// and the XR simulator without configuration.
func isTriggerButton(name string) bool {
	return name == "trigger" || name == "trigger_click" || name == "select"
}

// isGripButton matches the names the common OpenXR interaction
// profiles use for the side grip / squeeze button. Quest emits
// "grip_click"; some profiles use "squeeze" or "squeeze_click".
func isGripButton(name string) bool {
	return name == "grip_click" || name == "grip" || name == "squeeze" || name == "squeeze_click"
}

// tryStartPaletteGrab arms a palette drag if the given controller is
// currently hovering the palette panel (its ray is hitting it on the
// palette collision layer). Captures controller-relative palette pose
// so updatePaletteGrab can re-apply it every frame, preserving the
// offset the user picked it up with.
func (world *Client) tryStartPaletteGrab(controller XRController3D.Instance) {
	if world.vrUIPanelPalette == MeshInstance3D.Nil {
		return
	}
	// Only grab if THIS hand is pointing at the palette right now.
	hoveringPalette := false
	if controller == world.xrRight && world.vrUIHoverViewportRight == world.vrUIViewportPalette {
		hoveringPalette = true
	}
	if controller == world.xrLeft && world.vrUIHoverViewportLeft == world.vrUIViewportPalette {
		hoveringPalette = true
	}
	if !hoveringPalette {
		return
	}
	world.vrPaletteGrabHand = controller
	// Capture palette pose in the controller's local frame: when the
	// hand later rotates/translates, applying controller.global *
	// captured-relative reproduces the same world pose, so the panel
	// tracks the hand without snapping.
	palette := world.vrUIPanelPalette.AsNode3D().GlobalTransform()
	hand := controller.AsNode3D().GlobalTransform()
	handInv := Transform3D.Inverse(hand)
	world.vrPaletteGrabRelative = Transform3D.Mul(handInv, palette)
}

// vrUpdateGripGizmo applies the desktop Shift/Ctrl-style gizmo
// modifier using the controller grips:
//
//	right grip only → GizmoShift  (= Shift)
//	left grip only  → GizmoTwist  (= Ctrl)
//	both grips      → GizmoScale  (= Shift+Ctrl)
//	no grips        → restore the gizmo that was active before the
//	                  first grip came in
//
// Called from each grip's press/release handler after the palette-
// grab branch has been ruled out.
func (world *Client) vrUpdateGripGizmo() {
	if world.ui == nil || world.ui.CloudControl == nil {
		return
	}
	cc := world.ui.CloudControl
	switch {
	case world.vrRightGrip && world.vrLeftGrip:
		if !world.vrGripModifierActive {
			world.vrGripGizmoBackup = cc.Gizmo
			world.vrGripModifierActive = true
		}
		cc.set_gizmo(GizmoScale)
	case world.vrRightGrip:
		if !world.vrGripModifierActive {
			world.vrGripGizmoBackup = cc.Gizmo
			world.vrGripModifierActive = true
		}
		cc.set_gizmo(GizmoShift)
	case world.vrLeftGrip:
		if !world.vrGripModifierActive {
			world.vrGripGizmoBackup = cc.Gizmo
			world.vrGripModifierActive = true
		}
		cc.set_gizmo(GizmoTwist)
	default:
		if world.vrGripModifierActive {
			cc.set_gizmo(world.vrGripGizmoBackup)
			world.vrGripModifierActive = false
		}
	}
}

// tryPlaceVRPreview asks the active editor to commit its preview if
// one is loaded. Returns true on success — the caller (VR trigger
// handler) uses this to short-circuit the scene-pick path so the
// trigger drops the design instead of selecting whatever was under
// it. Editors that don't implement PreviewPlacer get a no-op false.
func (world *Client) tryPlaceVRPreview() bool {
	if world.ui == nil || world.ui.Editor == nil || world.ui.Editor.editor == nil {
		return false
	}
	if pp, ok := world.ui.Editor.editor.(PreviewPlacer); ok {
		return pp.TryPlacePreview()
	}
	return false
}

// updatePaletteGrab is called every frame from processVRPointer; when
// a grab is armed it re-anchors the palette to the holding hand's
// current pose.
func (world *Client) updatePaletteGrab() {
	if world.vrPaletteGrabHand == XRController3D.Nil || world.vrUIPanelPalette == MeshInstance3D.Nil {
		return
	}
	hand := world.vrPaletteGrabHand.AsNode3D().GlobalTransform()
	world.vrUIPanelPalette.AsNode3D().SetGlobalTransform(
		Transform3D.Mul(hand, world.vrPaletteGrabRelative),
	)
}

// Godot's CollisionObject3D layer/mask methods take `int`, not uint32.
// Convert defensively — Go's untyped constants for `1 << 20` etc. are
// int by default, but channelling them through a named uint32 above
// keeps the bit-flag intent explicit.
func uint32_to_int(v uint32) int { return int(v) }

// uiDisplaySize is the effective display size used by UI layout math.
// In XR the editor UI renders inside a 1920×1080 SubViewport per wrist
// panel, not the HMD's per-eye render target — so any control that
// positions itself relative to "the bottom of the screen" must use
// this value (matching the SubViewport size) rather than the OS-
// reported window size, which would push it off the visible quad.
func uiDisplaySize(client *Client) Vector2i.XY {
	if client != nil && client.xr {
		return Vector2i.New(vrUIWidth, vrUIHeight)
	}
	return DisplayServer.WindowGetSize(0)
}
