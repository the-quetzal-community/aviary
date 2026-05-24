package internal

import (
	"math"

	"graphics.gd/classdb/BaseMaterial3D"
	"graphics.gd/classdb/Camera3D"
	"graphics.gd/classdb/ImmediateMesh"
	"graphics.gd/classdb/Material"
	"graphics.gd/classdb/Mesh"
	"graphics.gd/classdb/MeshInstance3D"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/StandardMaterial3D"
	"graphics.gd/variant/Angle"
	"graphics.gd/variant/Color"
	"graphics.gd/variant/Euler"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Vector3"

	"the.quetzal.community/aviary/internal/critter"
)

// ribcageVis is the side-view-locked, xray-style ribcage diagram that
// the user sees while the critter editor is in "ribcage" view. It
// owns:
//   - a MeshInstance3D + ImmediateMesh that we redraw each frame
//     with the current spine polyline and per-bone cross-section
//     ribs;
//   - a dark transparent material we slap onto the body's mesh as a
//     SetMaterialOverride so the live body shape stays visible but
//     fades into the background while we're working on the spine;
//   - a snapshot of the world camera state (focal-point position,
//     focal-point yaw, lens pitch) so when the user leaves the view
//     we can put everything back exactly where they left it.
//
// The struct is recreated fresh on every ribcage enter — we don't
// try to keep it warm across exits because tearing down the rig in
// QueueFree leaves us with stale Node3D handles anyway.
type ribcageVis struct {
	container  Node3D.Instance
	mesh       MeshInstance3D.Instance
	immediate  ImmediateMesh.Instance
	lineMat    StandardMaterial3D.Instance
	bodyDarken StandardMaterial3D.Instance

	// savedBodyOverride is what the body MeshInstance3D's
	// material_override was before we set our darken material. Most
	// of the time this is Material.Nil and we restore Nil — we still
	// store it so a future code path that puts an override on the
	// body for some other reason doesn't get clobbered by our exit.
	savedBodyOverride Material.Instance

	// Camera snapshot. We restore these on exit so a user who was
	// orbiting at an arbitrary angle before flipping to ribcage view
	// gets that same angle back when they flip away.
	savedFocalPos   Vector3.XYZ
	savedFocalRot   Euler.Radians
	savedLensRot    Euler.Radians
	savedProjection Camera3D.ProjectionType
}

// Side-view convention: we look at the critter from the +X side
// (camera at world +X, looking toward −X). A yaw of +π/2 around Y
// orbits the FocalPoint default camera (which sits at +Z relative
// to the lens) over to the +X side, which puts the body's +Z axis
// — i.e. the head/forward direction — on the LEFT half of the
// screen. The previous draft used −π/2 (other side); switched on
// user request.
const ribcageSideYaw Angle.Radians = math.Pi / 2

// ribcageEnter installs the dark body override, snapshots the
// camera, points it at a fixed side view, and creates the
// xray-rendered ImmediateMesh that draws the spine + ribs.
//
// Safe to call when already in ribcage view — the early return on
// ce.ribcage != nil makes it idempotent so the SwitchToView dispatch
// doesn't have to be careful.
func (ce *CritterEditor) ribcageEnter() {
	if ce.ribcage != nil {
		return
	}
	if ce.client == nil {
		// Single-user dev path doesn't expose a camera through the
		// editor (the world owns it), so without a client we can't
		// do the side-view lock. We still build the visualization
		// so the mesh shows up — the lock just won't run.
		ce.buildRibcageVis(nil)
		return
	}
	ce.buildRibcageVis(ce.client)
}

// buildRibcageVis is the half of ribcageEnter that doesn't touch the
// camera. Split out so the no-client dev case can skip the camera
// snapshot but still get the dark body + xray mesh.
func (ce *CritterEditor) buildRibcageVis(world *Client) {
	rv := &ribcageVis{}

	container := Node3D.New()
	// Parent under the body MeshInstance3D so the spine + rib
	// ribbons inherit whatever position/scale the body sits at
	// (currently the body is lifted +Y above the ground plate).
	// Parented under the editor root would draw the diagram at
	// the wrong height since rib vertices use body-local coords.
	if ce.body.mesh != MeshInstance3D.Nil {
		ce.body.mesh.AsNode().AddChild(container.AsNode())
	} else {
		ce.AsNode3D().AsNode().AddChild(container.AsNode())
	}
	rv.container = container

	// Spine + rib mesh. ImmediateMesh + LineStrip is dirt-cheap to
	// rebuild every PhysicsProcess and lets us cheat the "thickness"
	// problem by just drawing more rings around each control point.
	mi := MeshInstance3D.New()
	im := ImmediateMesh.New()
	mi.SetMesh(im.AsMesh())
	lineMat := StandardMaterial3D.New()
	lineMat.AsBaseMaterial3D().SetShadingMode(BaseMaterial3D.ShadingModeUnshaded)
	lineMat.AsBaseMaterial3D().SetAlbedoColor(Color.RGBA{R: 1, G: 1, B: 1, A: 1})
	// xray: render on top of (through) the darkened body so the
	// spine + ribs always read regardless of where the camera sits.
	lineMat.AsBaseMaterial3D().SetNoDepthTest(true)
	// Thick lines are emitted as triangle strips; whichever side
	// faces the camera depends on the rib's orientation, so disable
	// back-face culling to ensure both sides render.
	lineMat.AsBaseMaterial3D().SetCullMode(BaseMaterial3D.CullDisabled)
	mi.AsGeometryInstance3D().SetMaterialOverride(lineMat.AsMaterial())
	container.AsNode().AddChild(mi.AsNode())
	rv.mesh = mi
	rv.immediate = im
	rv.lineMat = lineMat

	// Ribcage is a flat side profile — the round ground plate
	// (with its forward arrow) just clutters the view, so hide it
	// for the duration. Restored on exit. ID lookup keeps us safe
	// if the node was freed by something else in the meantime.
	if ground, ok := ce.ground.Instance(); ok {
		ground.AsNode3D().SetVisible(false)
	}

	// Body fade: a dark, half-transparent override on the live body
	// MeshInstance3D so the spine + ribs pop. We save whatever the
	// current override was so we can restore it on exit. The same
	// material is then pushed to every leg MeshInstance3D so legs
	// fade out with the body instead of remaining solid white
	// rectangles in the middle of the diagram.
	if ce.body.mesh != MeshInstance3D.Nil {
		rv.savedBodyOverride = ce.body.mesh.AsGeometryInstance3D().MaterialOverride()
		darken := StandardMaterial3D.New()
		darken.AsBaseMaterial3D().SetTransparency(BaseMaterial3D.TransparencyAlpha)
		darken.AsBaseMaterial3D().SetAlbedoColor(Color.RGBA{R: 0.08, G: 0.08, B: 0.10, A: 0.35})
		darken.AsBaseMaterial3D().SetShadingMode(BaseMaterial3D.ShadingModeUnshaded)
		ce.body.mesh.AsGeometryInstance3D().SetMaterialOverride(darken.AsMaterial())
		rv.bodyDarken = darken
		ce.body.SetLegMaterialOverride(darken.AsMaterial())
		// Hide the back-of-body leg copy while in the side view; the
		// camera-facing one is enough to read the silhouette and
		// stacking both doubles the alpha.
		ce.body.SetLegOneSided(true)
	}

	// Snapshot + lock the world camera if we have one to work with.
	if world != nil {
		rv.savedFocalPos = world.FocalPoint.AsNode3D().Position()
		rv.savedFocalRot = world.FocalPoint.AsNode3D().Rotation()
		rv.savedLensRot = world.FocalPoint.Lens.AsNode3D().Rotation()
		rv.savedProjection = world.FocalPoint.Lens.Camera.Projection()
		ce.ribcage = rv
		ce.ribcageSnapCamera()
	} else {
		ce.ribcage = rv
	}

	ce.ribcageRebuildMesh()
}

// ribcageExit undoes ribcageEnter — restores the body material,
// restores the camera transform, and tears down the visualization
// container. No-op if we never entered.
func (ce *CritterEditor) ribcageExit() {
	if ce.ribcage == nil {
		return
	}
	rv := ce.ribcage
	if ground, ok := ce.ground.Instance(); ok {
		ground.AsNode3D().SetVisible(true)
	}
	if ce.body.mesh != MeshInstance3D.Nil {
		ce.body.mesh.AsGeometryInstance3D().SetMaterialOverride(rv.savedBodyOverride)
	}
	// Clear the leg material override so legs return to their
	// default appearance outside the ribcage view.
	ce.body.SetLegMaterialOverride(Material.Nil)
	// Restore mirrored leg geometry so the world view sees both
	// sides again.
	ce.body.SetLegOneSided(false)
	if ce.client != nil {
		ce.client.FocalPoint.AsNode3D().SetPosition(rv.savedFocalPos)
		ce.client.FocalPoint.AsNode3D().SetRotation(rv.savedFocalRot)
		ce.client.FocalPoint.Lens.AsNode3D().SetRotation(rv.savedLensRot)
		ce.client.FocalPoint.Lens.Camera.SetProjection(rv.savedProjection)
	}
	if rv.container != Node3D.Nil {
		rv.container.AsNode().QueueFree()
	}
	ce.ribcage = nil
}

// ribcageSnapCamera keeps the world camera pinned to a clean side
// profile of the critter. Called every PhysicsProcess while in
// ribcage view so middle-mouse-drag and screen-drag (handled by
// the world's UnhandledInput) can't sneak the camera off-axis.
//
// Pan and zoom are deliberately left alone: panning still moves the
// focal point each frame, but we then re-snap focal-point.position
// to the body origin. Zoom (Camera.position.z, set by mouse wheel
// or magnify gesture) we never touch.
func (ce *CritterEditor) ribcageSnapCamera() {
	if ce.ribcage == nil || ce.client == nil {
		return
	}
	bodyOrigin := Vector3.XYZ{}
	if ce.body.mesh != MeshInstance3D.Nil {
		bodyOrigin = ce.body.mesh.AsNode3D().GlobalPosition()
	}
	ce.client.FocalPoint.AsNode3D().SetGlobalPosition(bodyOrigin)
	ce.client.FocalPoint.AsNode3D().SetRotation(Euler.Radians{
		X: 0,
		Y: ribcageSideYaw,
		Z: 0,
	})
	ce.client.FocalPoint.Lens.AsNode3D().SetRotation(Euler.Radians{})
	// Force orthographic projection in this view — perspective
	// foreshortening makes bones closer to the camera look bigger
	// than equally-radiused bones at the far end, which is a lie
	// when the editor is meant to be a flat side profile. Tie the
	// orthographic size to the camera's local Z position (the
	// existing zoom-via-wheel / pinch input writes to that), so
	// mouse-wheel zooming still works exactly as it did in
	// perspective mode.
	camNode := ce.client.FocalPoint.Lens.Camera
	zoom := camNode.AsNode3D().Position().Z
	if zoom < 0.1 {
		zoom = 0.1
	}
	camNode.SetOrthogonal(zoom, 0.05, 100)
}

// ribcageRebuildMesh redraws the ImmediateMesh from the critter's
// current bones. Cheap — re-runs ClearSurfaces + a couple of dozen
// SurfaceAddVertex calls — so we just run it every PhysicsProcess
// while in ribcage view instead of trying to dirty-track when the
// spine has actually changed.
//
// Layout we emit (everything as TriangleStrips via emitThickLine so
// the lines have visible width — Godot 4 dropped wide-line support
// in the forward renderer, so we draw flat ribbons in the X=0
// plane):
//   - one ribbon walking the spine tail→head through every bone
//     position (the "visual bones connecting each control point");
//   - one ribbon per bone forming a flat rib arc oriented to the
//     spine, so the rib reads as a curved bar that pivots with the
//     local spine tangent. The arc spans top→bottom of the body
//     (perpendicular to the spine) and bows along the spine — i.e.
//     the original sagittal half-arc rotated 90° clockwise, which
//     stops adjacent ribs piling on top of each other at sharp
//     bends in the spine.
func (ce *CritterEditor) ribcageRebuildMesh() {
	if ce.ribcage == nil || ce.body.critter == nil {
		return
	}
	im := ce.ribcage.immediate
	if im == ImmediateMesh.Nil {
		return
	}
	bones := ce.body.critter.Bones()
	if len(bones) < 2 {
		return
	}
	im.ClearSurfaces()

	// Limb-only view: skip the spine + rib pass entirely so the leg
	// controls aren't fighting the body bones for screen space.
	if ce.view == "limbone" {
		ce.ribcageEmitLegs(im)
		return
	}

	// Spine ribbon through every bone position.
	spinePts := make([]Vector3.XYZ, len(bones))
	for i, b := range bones {
		spinePts[i] = Vector3.XYZ{
			X: Float.X(b.Pos.X),
			Y: Float.X(b.Pos.Y),
			Z: Float.X(b.Pos.Z),
		}
	}
	emitThickLine(im, spinePts, spineHalfWidth)

	// Ribs: each rib is a flat half-arc in the body's sagittal (X=0)
	// plane, anchored to spine-local axes so it rotates with the
	// spine. Two local axes (each a 90° CW step from the spine
	// tangent):
	//
	//   Tᵉ — T rotated 90° CW. Points "perpendicular-down" relative
	//        to the spine; the arc anchors lie ±R·Tᵉ from the
	//        circle centre.
	//   Nᵉ — N rotated 90° CW = −T. Bow direction.
	//
	// Two placement tweaks vs the previous draft:
	//
	//   1) The circle centre is shifted by −R·Nᵉ from the bone so
	//      the curve's apex (θ=π/2) lands ON the bone — i.e. the
	//      "centre of the arc" is the control point, instead of
	//      sitting one radius away from it.
	//   2) R is a fraction of bone.Radius rather than the full
	//      radius. With the apex-on-bone shift, the anchors end up
	//      at distance R·√2 from the bone; using R = 0.5·radius
	//      keeps the entire arc inside the body tube even where
	//      neighbouring bones taper.
	const ribSegments = 16
	ribPts := make([]Vector3.XYZ, ribSegments+1)
	for i := range bones {
		g, ok := ribArcGeomAt(bones, i)
		if !ok {
			continue
		}
		for j := 0; j <= ribSegments; j++ {
			theta := math.Pi * float64(j) / float64(ribSegments)
			cs := float32(math.Cos(theta))
			sn := float32(math.Sin(theta))
			ribPts[j] = Vector3.XYZ{
				X: 0,
				Y: Float.X(g.cY + g.R*cs*g.teY + g.R*sn*g.neY),
				Z: Float.X(g.cZ + g.R*cs*g.teZ + g.R*sn*g.neZ),
			}
		}
		emitThickLine(im, ribPts, ribHalfWidth)
	}

}

// ribcageEmitLegs draws the limb editor overlay: per-leg polyline
// (Hip→Knee→Foot), square joint markers, and per-joint radius
// rings — everything the user interacts with in the "limbone"
// view. Pulled out of ribcageRebuildMesh so the two views can pick
// what they draw without one trampling the other.
func (ce *CritterEditor) ribcageEmitLegs(im ImmediateMesh.Instance) {
	legs := ce.body.critter.Legs()
	legPts := make([]Vector3.XYZ, 3)
	shift := Float.X(legHandleYShift)
	for _, leg := range legs {
		legPts[0] = Vector3.XYZ{X: 0, Y: Float.X(leg.Hip.Y) + shift, Z: Float.X(leg.Hip.Z)}
		legPts[1] = Vector3.XYZ{X: 0, Y: Float.X(leg.Knee.Y) + shift, Z: Float.X(leg.Knee.Z)}
		legPts[2] = Vector3.XYZ{X: 0, Y: Float.X(leg.Foot.Y) + shift, Z: Float.X(leg.Foot.Z)}
		emitThickLine(im, legPts, legBoneHalfWidth)
		for _, p := range legPts {
			emitSquareMarker(im, p, legHandleHalfSize)
		}
		// Radius rings: one circle in the X=0 plane per joint, sized
		// to that joint's radius. Doubles as the visual cue for the
		// current thickness AND the click-target for resizing — drag
		// the ring's edge to grow or shrink that joint.
		emitCircle(im, legPts[0], leg.HipRadius, legRingHalfWidth)
		emitCircle(im, legPts[1], leg.KneeRadius, legRingHalfWidth)
		emitCircle(im, legPts[2], leg.FootRadius, legRingHalfWidth)
	}
}

// emitCircle emits a thin ring in the X=0 plane centred on c with
// the given radius. Implemented as a closed polyline with N
// segments rendered through emitThickLine so the ring has visible
// thickness on screen.
func emitCircle(im ImmediateMesh.Instance, c Vector3.XYZ, radius float32, halfWidth Float.X) {
	if radius <= 0 {
		return
	}
	const segs = 32
	pts := make([]Vector3.XYZ, segs+1)
	for i := 0; i <= segs; i++ {
		a := 2 * math.Pi * float64(i) / float64(segs)
		pts[i] = Vector3.XYZ{
			X: 0,
			Y: c.Y + Float.X(float32(math.Cos(a))*radius),
			Z: c.Z + Float.X(float32(math.Sin(a))*radius),
		}
	}
	emitThickLine(im, pts, halfWidth)
}

// emitSquareMarker emits a flat square (two triangles, as a strip)
// in the X=0 plane centred on `c`. Used for the small grab handles
// at each leg joint — distinct visual from the round-ish rib bow
// markers and easy to pick with a proximity check.
func emitSquareMarker(im ImmediateMesh.Instance, c Vector3.XYZ, half Float.X) {
	im.SurfaceBegin(Mesh.PrimitiveTriangleStrip)
	im.SurfaceAddVertex(Vector3.XYZ{X: 0, Y: c.Y - half, Z: c.Z - half})
	im.SurfaceAddVertex(Vector3.XYZ{X: 0, Y: c.Y + half, Z: c.Z - half})
	im.SurfaceAddVertex(Vector3.XYZ{X: 0, Y: c.Y - half, Z: c.Z + half})
	im.SurfaceAddVertex(Vector3.XYZ{X: 0, Y: c.Y + half, Z: c.Z + half})
	im.SurfaceEnd()
}

// ribRadiusScale is the fraction of bone.Radius the rib arc lives
// at. Sized so the whole arc sits inside the body tube even where
// neighbouring bones taper.
const ribRadiusScale = float32(0.7)

// ribArcGeom is the geometry of one bone's rib arc in the body's
// sagittal (X=0) plane: the local (Tᵉ, Nᵉ) basis, the circle's
// radius R, and the circle's centre in body-local coords. The arc
// itself runs through angle ∈ [0, π] in that basis.
type ribArcGeom struct {
	teY, teZ float32 // Tᵉ — chord direction (perpendicular to spine)
	neY, neZ float32 // Nᵉ — bow direction (along the spine)
	R        float32 // arc radius
	cY, cZ   float32 // circle centre, in body-local Y/Z
}

// ribArcGeomAt computes the rib arc geometry for bone i. Returns
// ok=false at degenerate bones (tangent collapses to zero) so
// callers can skip them. Shared between the renderer
// (ribcageRebuildMesh) and the click picker (ribArcUnderMouse) so
// the picker matches the shape that's actually on screen.
func ribArcGeomAt(bones []critter.Bone, i int) (g ribArcGeom, ok bool) {
	ty, tz := ribcageBoneTangent2D(bones, i)
	if ty == 0 && tz == 0 {
		return ribArcGeom{}, false
	}
	g.teY, g.teZ = -tz, ty // T rotated 90° CW (was the old N)
	if i == 0 {
		// Tail bone (rightmost on screen): keep the original bow
		// direction so the arc sits inside the body (apex on the
		// tail tip, chord forward toward bone 1). Inverting it
		// here would poke the arc past the tail tip.
		g.neY, g.neZ = -ty, -tz
	} else {
		// Every other bone: flip the bow direction so the chord
		// lands on the body-interior side of the spine. The
		// default (non-inverted) direction puts the head bone's
		// chord past the head tip; inverting lands it inside.
		g.neY, g.neZ = ty, tz
	}
	g.R = bones[i].Radius * ribRadiusScale
	// Shift centre so the curve apex (θ=π/2) lands on the bone.
	g.cY = bones[i].Pos.Y - g.R*g.neY
	g.cZ = bones[i].Pos.Z - g.R*g.neZ
	return g, true
}

// Half-widths picked by eye against the default 0.08–0.40 bone
// radii: thin enough not to bleed into the body fade, thick enough
// to read clearly through the dark override.
const (
	spineHalfWidth = Float.X(0.015)
	ribHalfWidth   = Float.X(0.012)
	// legBoneHalfWidth is for the thin line drawn through Hip→Knee→
	// Foot in the side view; matches the rib thickness so the leg
	// reads as part of the same diagrammatic skeleton.
	legBoneHalfWidth = Float.X(0.010)
	// legHandleHalfSize is the half-width of the square marker drawn
	// at each leg joint. Sized to be visible without obscuring nearby
	// bones; tuned alongside legHandlePickRadius below so a click on
	// the visible square lands.
	legHandleHalfSize = Float.X(0.025)
	// legRingHalfWidth is the thickness of the radius ring drawn
	// around each leg joint. Thinner than the bone polyline so the
	// ring reads as "wrapper" geometry rather than another segment.
	legRingHalfWidth = Float.X(0.005)
)

// legRingPickTolerance is the cursor distance from the ring edge
// inside which a click is treated as "grab the ring" (= radius
// drag) rather than "grab the joint" (= move). Same numerical scale
// as legHandlePickRadius so the picker priorities don't fight each
// other.
const legRingPickTolerance = float32(0.04)

// legHandleYShift nudges all leg handles (joint markers + radius
// rings) down by this many body-local units in the ribcage view so
// they sit where the user expects relative to the visible leg
// silhouette. Empirical — without it the controls read as slightly
// too high. Applied to the rendered geometry and to the proximity
// pickers so a click on the visible handle still lands.
const legHandleYShift = float32(-0.15)

// legHandlePickRadius is the cursor proximity threshold for picking
// a leg joint handle in the X=0 plane. Slightly larger than the
// visible square so the cursor doesn't have to land pixel-perfectly.
const legHandlePickRadius = float32(0.04)

// emitThickLine emits a TriangleStrip approximating a polyline in
// the body's sagittal (X=0) plane with a given half-width. At each
// vertex we drop two strip vertices offset ±halfWidth perpendicular
// to the local segment direction; interior verts use the bisector
// of the incoming + outgoing unit tangents so the strip stays
// continuous through bends. Endpoints just use the adjacent
// segment's tangent.
//
// Caller must keep the polyline in the X=0 plane (X coord is
// ignored — every emitted vert has X=0) since the side-view camera
// looks straight along ±X and we want the ribbons to face it.
func emitThickLine(im ImmediateMesh.Instance, points []Vector3.XYZ, halfWidth Float.X) {
	if len(points) < 2 {
		return
	}
	im.SurfaceBegin(Mesh.PrimitiveTriangleStrip)
	for i := range points {
		var ty, tz Float.X
		switch {
		case i == 0:
			ty = points[1].Y - points[0].Y
			tz = points[1].Z - points[0].Z
		case i == len(points)-1:
			ty = points[i].Y - points[i-1].Y
			tz = points[i].Z - points[i-1].Z
		default:
			inY := points[i].Y - points[i-1].Y
			inZ := points[i].Z - points[i-1].Z
			inL := Float.X(math.Sqrt(float64(inY*inY + inZ*inZ)))
			outY := points[i+1].Y - points[i].Y
			outZ := points[i+1].Z - points[i].Z
			outL := Float.X(math.Sqrt(float64(outY*outY + outZ*outZ)))
			if inL > 1e-6 && outL > 1e-6 {
				ty = inY/inL + outY/outL
				tz = inZ/inL + outZ/outL
			} else if inL > 1e-6 {
				ty, tz = inY, inZ
			} else {
				ty, tz = outY, outZ
			}
		}
		l := Float.X(math.Sqrt(float64(ty*ty + tz*tz)))
		if l < 1e-6 {
			continue
		}
		// Perpendicular in (Y, Z), rotated 90° CCW: (ty, tz) → (-tz, ty).
		py := -tz / l * halfWidth
		pz := ty / l * halfWidth
		im.SurfaceAddVertex(Vector3.XYZ{X: 0, Y: points[i].Y + py, Z: points[i].Z + pz})
		im.SurfaceAddVertex(Vector3.XYZ{X: 0, Y: points[i].Y - py, Z: points[i].Z - pz})
	}
	im.SurfaceEnd()
}

// ribcageBoneTangent2D returns the unit (Y, Z) components of the
// spine tangent at bone i — i.e. the spine tangent projected into
// the body's sagittal (X=0) plane and normalised. Centred difference
// in the middle of the chain, one-sided at the ends. Returns
// (0, 0) when the surrounding bones are degenerate so callers can
// skip drawing a degenerate rib.
func ribcageBoneTangent2D(bones []critter.Bone, i int) (ty, tz float32) {
	n := len(bones)
	if n < 2 {
		return 0, 0
	}
	switch {
	case i == 0:
		ty = bones[1].Pos.Y - bones[0].Pos.Y
		tz = bones[1].Pos.Z - bones[0].Pos.Z
	case i == n-1:
		ty = bones[i].Pos.Y - bones[i-1].Pos.Y
		tz = bones[i].Pos.Z - bones[i-1].Pos.Z
	default:
		ty = bones[i+1].Pos.Y - bones[i-1].Pos.Y
		tz = bones[i+1].Pos.Z - bones[i-1].Pos.Z
	}
	l := float32(math.Sqrt(float64(ty*ty + tz*tz)))
	if l < 1e-6 {
		return 0, 0
	}
	return ty / l, tz / l
}
