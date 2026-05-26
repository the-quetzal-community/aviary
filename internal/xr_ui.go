package internal

import (
	"graphics.gd/classdb/BaseMaterial3D"
	"graphics.gd/classdb/BoxShape3D"
	"graphics.gd/classdb/CollisionShape3D"
	"graphics.gd/classdb/Input"
	"graphics.gd/classdb/InputEventMouseButton"
	"graphics.gd/classdb/InputEventMouseMotion"
	"graphics.gd/classdb/MeshInstance3D"
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/QuadMesh"
	"graphics.gd/classdb/RayCast3D"
	"graphics.gd/classdb/StandardMaterial3D"
	"graphics.gd/classdb/StaticBody3D"
	"graphics.gd/classdb/SubViewport"
	"graphics.gd/classdb/XRController3D"
	"graphics.gd/variant/Angle"
	"graphics.gd/variant/Euler"
	"graphics.gd/variant/Vector2"
	"graphics.gd/variant/Vector2i"
	"graphics.gd/variant/Vector3"
)

// First-attempt VR UI: render the existing 2D editor overlay onto a
// quad parented to the left controller (wrist panel), and forward
// controller-ray hits + trigger presses as synthetic mouse events to
// the SubViewport. This reuses every existing button/handler — no
// duplication, no separate VR UI tree. Splitting the drawer onto its
// own larger world-anchored panel is a follow-up; this gets one panel
// fully interactive so we can validate the end-to-end loop.

const (
	// SubViewport pixel size. The existing UI was authored against
	// roughly 1080p / 2160p targets via its `scaling()` function;
	// 1920x1080 keeps it readable without huge texture memory.
	vrUIWidth  = 1920
	vrUIHeight = 1080

	// Physical panel size in meters. ~16:9, roughly the size of a
	// large tablet — close enough to read on the wrist, small enough
	// that a controller-ray sweep covers it comfortably.
	vrUIWorldW = 0.40
	vrUIWorldH = 0.225

	// Collision layer reserved for VR UI panels. Bit 20 is well
	// outside the layers any existing editor uses (terrain is bit 1,
	// the selection mask in client.go uses ~bit 1 + bit 2), so
	// controller raycasts can target panels exclusively.
	vrUILayer uint32 = 1 << 20
)

// attachUIToVR moves the existing AviaryUI Control out of the main
// viewport and into a SubViewport, then displays that SubViewport as
// a textured quad parented to the supplied anchor (left controller).
// The 2D UI continues to work normally — buttons fire, animations
// tween, scaling logic runs — it just renders to a texture instead of
// the main framebuffer.
func (world *Client) attachUIToVR(ui *UI, anchor Node3D.Instance) {
	// Detach UI from its current parent (world).
	if parent := ui.AsNode().GetParent(); parent != Node.Nil {
		parent.RemoveChild(ui.AsNode())
	}

	// SubViewport hosts the Control tree.
	sub := SubViewport.New()
	sub.SetSize(Vector2i.New(vrUIWidth, vrUIHeight))
	sub.AsViewport().SetTransparentBg(true)
	sub.SetRenderTargetUpdateMode(SubViewport.UpdateAlways)
	sub.AsNode().AddChild(ui.AsNode())

	// Force the UI to fill the viewport. Without this it inherits
	// whatever size it had in the main window — usually a stale
	// pre-scaling value — and ends up clipped or empty.
	ui.AsControl().SetSize(Vector2.New(vrUIWidth, vrUIHeight))

	// QuadMesh + StandardMaterial3D textured with the SubViewport.
	mesh := QuadMesh.New()
	mesh.AsPlaneMesh().SetSize(Vector2.New(vrUIWorldW, vrUIWorldH))

	mat := StandardMaterial3D.New()
	mat.AsBaseMaterial3D().SetShadingMode(BaseMaterial3D.ShadingModeUnshaded)
	mat.AsBaseMaterial3D().SetTransparency(BaseMaterial3D.TransparencyAlpha)
	mat.AsBaseMaterial3D().SetAlbedoTexture(sub.AsViewport().GetTexture().AsTexture2D())

	panel := MeshInstance3D.New()
	panel.SetMesh(mesh.AsMesh())
	panel.AsGeometryInstance3D().SetMaterialOverride(mat.AsMaterial())

	// Collider matches the quad bounds (with a thin Z extent) so a
	// RayCast3D can land precise hit points used to compute UV.
	body := StaticBody3D.New()
	body.AsCollisionObject3D().SetCollisionLayer(uint32_to_int(vrUILayer))
	body.AsCollisionObject3D().SetCollisionMask(0)
	shape := BoxShape3D.New()
	shape.SetSize(Vector3.New(vrUIWorldW, vrUIWorldH, 0.01))
	coll := CollisionShape3D.New()
	coll.SetShape(shape.AsShape3D())
	body.AsNode().AddChild(coll.AsNode())
	panel.AsNode().AddChild(body.AsNode())

	// Position the panel above the left controller, tilted toward
	// the user's face so a glance lands on it — classic wrist-menu
	// pose used by most VR DCC tools.
	panel.AsNode3D().SetPosition(Vector3.New(0.0, 0.08, -0.10))
	panel.AsNode3D().SetRotation(Euler.Radians{X: -Angle.InRadians(35), Y: 0, Z: 0})

	anchor.AsNode().AddChild(sub.AsNode())
	anchor.AsNode().AddChild(panel.AsNode())

	world.vrUIViewport = sub
	world.vrUIPanel = panel
}

// setupControllerPointer adds a RayCast3D to the right controller that
// only checks the VR-UI layer, plus signal handlers for the trigger
// button that synthesize MouseButton events into the panel's viewport.
func (world *Client) setupControllerPointer(right XRController3D.Instance) {
	ray := RayCast3D.New()
	ray.SetTargetPosition(Vector3.New(0, 0, -2))
	ray.SetEnabled(true)
	ray.SetCollisionMask(uint32_to_int(vrUILayer))
	right.AsNode().AddChild(ray.AsNode())

	right.OnButtonPressed(func(name string) {
		if isTriggerButton(name) {
			world.vrPointerClick(true)
		}
	})
	right.OnButtonReleased(func(name string) {
		if isTriggerButton(name) {
			world.vrPointerClick(false)
		}
	})

	world.vrPointer = ray
}

// processVRPointer is called every frame while world.xr is true. It
// reads the controller raycast and, if it's hitting the UI panel,
// computes the UV from the hit point and pushes a MouseMotion event
// into the SubViewport so highlight/hover state updates live.
func (world *Client) processVRPointer() {
	if world.vrPointer == RayCast3D.Nil || world.vrUIViewport == SubViewport.Nil || world.vrUIPanel == MeshInstance3D.Nil {
		return
	}
	if !world.vrPointer.IsColliding() {
		return
	}
	hitWorld := world.vrPointer.GetCollisionPoint()
	local := world.vrUIPanel.AsNode3D().ToLocal(hitWorld)

	// QuadMesh at default orientation has +X right, +Y up, normal
	// along -Z. Local (0,0) is the quad's center. Map to UV → pixel.
	u := (float32(local.X) + vrUIWorldW/2) / vrUIWorldW
	v := 1 - (float32(local.Y)+vrUIWorldH/2)/vrUIWorldH
	pixel := Vector2.New(u*vrUIWidth, v*vrUIHeight)

	motion := InputEventMouseMotion.New()
	motion.AsInputEventMouse().SetPosition(pixel)
	world.vrUIViewport.AsViewport().PushInput(motion.AsInputEvent())
	world.vrLastPixel = pixel
}

// vrPointerClick synthesizes a MouseButton event at the last hover
// position. Called on trigger press / release from the right controller.
func (world *Client) vrPointerClick(pressed bool) {
	if world.vrUIViewport == SubViewport.Nil {
		return
	}
	btn := InputEventMouseButton.New()
	btn.SetButtonIndex(Input.MouseButtonLeft)
	btn.AsInputEventMouseButton().SetPressed(pressed)
	btn.AsInputEventMouse().SetPosition(world.vrLastPixel)
	world.vrUIViewport.AsViewport().PushInput(btn.AsInputEvent())
}

// isTriggerButton matches the names OpenXR exposes for the primary
// "select" button across the common interaction profiles. Quest's
// touch controllers fire "trigger_click"; the standard simple profile
// fires "trigger". We accept both so this works on Quest, Pico, Vive,
// and the XR simulator without configuration.
func isTriggerButton(name string) bool {
	return name == "trigger" || name == "trigger_click" || name == "select"
}

// Godot's CollisionObject3D layer/mask methods take `int`, not uint32.
// Convert defensively — Go's untyped constants for `1 << 20` etc. are
// int by default, but channelling them through a named uint32 above
// keeps the bit-flag intent explicit.
func uint32_to_int(v uint32) int { return int(v) }
