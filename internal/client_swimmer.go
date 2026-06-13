package internal

import (
	"time"

	"graphics.gd/classdb/BaseMaterial3D"
	"graphics.gd/classdb/ImmediateMesh"
	"graphics.gd/classdb/Input"
	"graphics.gd/classdb/Mesh"
	"graphics.gd/classdb/MeshInstance3D"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/StandardMaterial3D"
	"graphics.gd/variant/Color"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Vector3"

	"the.quetzal.community/aviary/internal/musical"
)

// Swimmers ("swimmer" library category — fish and other marine critters) only
// function in water. This file owns the three swimmer-specific control surfaces
// the generic mobile-entity paths can't express:
//
//   - the depth-aimed "swim here" move (right-drag to set the target depth, with
//     a 3D indicator), in swimAimState / beginSwimAim / updateSwimAim /
//     commitSwimAim;
//   - look-to-swim possession (mouse-aimed 3D swimming clamped to the water
//     column), in updateSwimPossess;
//   - entityIsSwimmer, the category lookup used to give a placed fish's
//     ActionRenderer its 3D (no terrain-snap) movement.
//
// Their depth is always TERRAIN-RELATIVE (stored as a height above the seabed, or
// the Editor="float" delta), so it rides terrain edits and reload — and so a
// dropping water level can strand a fish above the surface, where its
// EntityAnimator freezes it belly-up ("Dead Floating"). Placement (mid-water
// default + press-hold depth drag) lives in the scenery editor; the swim clip
// vocabulary and the out-of-water death are in critter_animation.go /
// critter_motion.go.

const (
	// swimControlSpeed is the metres/second a possessed fish swims.
	swimControlSpeed = Float.X(2.5)
	// swimAimCrossSize is the half-length (world units) of the target marker cross
	// drawn at the aimed swim destination.
	swimAimCrossSize = Float.X(0.25)
)

// swimAimState drives the right-drag "swim here" for a selected swimmer: the
// press anchors the target XZ on the seabed and seeds the depth at the fish's
// current height; dragging the held right button raises/lowers the target Y
// within the water column; releasing issues the 3D move. A depth indicator
// previews the target while aiming.
type swimAimState struct {
	active bool
	entity musical.Entity

	x, z Float.X // locked target XZ (seabed point under the press)
	y    Float.X // current absolute target Y, dragged within the water column

	// Vertical drag plane through the anchor — the GizmoFloat lift mechanism — so
	// mouse motion translates into a clean world-Y.
	planePoint  Vector3.XYZ
	planeNormal Vector3.XYZ
	grabStartY  Float.X
	startY      Float.X

	indicator MeshInstance3D.Instance
	immediate ImmediateMesh.Instance
}

// entityIsSwimmer reports whether a placed entity's design is in the swimmer
// category — used to give its ActionRenderer 3D (no terrain Y-snap) movement and
// to route its right-click to the depth-aimed swim.
func (world *Client) entityIsSwimmer(object Node3D.Instance) bool {
	design, ok := world.findDesignForObject(Node3D.ID(object.ID()))
	if !ok {
		return false
	}
	return isSwimmerCategory(designCategory(world.design_to_string[design]))
}

// beginSwimAim starts a depth-aimed "swim here" for node (the selected fish). The
// press anchors the target XZ on the seabed (terrainHit) and seeds the target
// depth at the swimmer's CURRENT height, so a quick click without dragging swims
// level. The held right button then raises/lowers the target Y (updateSwimAim);
// releasing issues the move (commitSwimAim).
func (world *Client) beginSwimAim(entity musical.Entity, node Node3D.Instance, terrainHit Vector3.XYZ) {
	if world.TerrainEditor == nil {
		return
	}
	cur := node.AsNode3D().Position()
	world.swimAim.active = true
	world.swimAim.entity = entity
	world.swimAim.x = terrainHit.X
	world.swimAim.z = terrainHit.Z
	world.swimAim.y = world.TerrainEditor.ClampToWater(Vector3.New(terrainHit.X, 0, terrainHit.Z), cur.Y)

	// Vertical drag plane through the anchor; its normal is the horizontal part of
	// the cursor ray, so dragging up/down maps to world-Y regardless of azimuth
	// (same construction as GizmoFloat).
	o, d := MouseRay(world.AsNode3D())
	horiz := Vector3.New(d.X, 0, d.Z)
	if l := Vector3.Length(horiz); l > 1e-4 {
		world.swimAim.planeNormal = Vector3.DivX(horiz, l)
	} else {
		world.swimAim.planeNormal = Vector3.New(1, 0, 0)
	}
	world.swimAim.planePoint = Vector3.New(world.swimAim.x, world.swimAim.y, world.swimAim.z)
	world.swimAim.startY = world.swimAim.y
	if hit, ok := IntersectRayPlane(o, d, world.swimAim.planePoint, world.swimAim.planeNormal); ok {
		world.swimAim.grabStartY = hit.Y
	} else {
		world.swimAim.grabStartY = world.swimAim.y
	}
	world.drawSwimAim()
}

// updateSwimAim re-reads the cursor ray each frame while aiming and slides the
// target Y along the drag plane, clamped to the water column at the anchor XZ,
// then redraws the depth indicator.
func (world *Client) updateSwimAim() {
	if world.TerrainEditor == nil {
		return
	}
	o, d := MouseRay(world.AsNode3D())
	if hit, ok := IntersectRayPlane(o, d, world.swimAim.planePoint, world.swimAim.planeNormal); ok {
		newY := world.swimAim.startY + (hit.Y - world.swimAim.grabStartY)
		world.swimAim.y = world.TerrainEditor.ClampToWater(Vector3.New(world.swimAim.x, 0, world.swimAim.z), newY)
	}
	world.drawSwimAim()
}

// commitSwimAim issues the swim-here move at the dragged depth and clears the
// aim. The target is stored TERRAIN-RELATIVE (Target.Y = depth above the seabed),
// reconstructed by the swimmer ActionRenderer, so the resting depth rides terrain
// edits/reload. Shift/Ctrl held at release chain or loop the path exactly like
// the ground walk-here.
func (world *Client) commitSwimAim() {
	aim := world.swimAim
	world.swimAim.active = false
	world.hideSwimAimIndicator()
	if world.TerrainEditor == nil {
		return
	}
	object, ok := world.entity_to_object[aim.entity].Instance()
	if !ok {
		return
	}
	node, ok := Object.As[Node3D.Instance](object)
	if !ok {
		return
	}
	xz := Vector3.New(aim.x, 0, aim.z)
	seabed := world.TerrainEditor.HeightAt(xz)
	targetAbs := Vector3.New(aim.x, aim.y, aim.z)

	// Chain (Shift) / loop (Ctrl) like the ground walk-here: a modifier held at
	// release appends onto the path's tail (in space and time) rather than
	// restarting; Ctrl additionally makes the whole path a back-and-forth loop.
	shift := Input.IsKeyPressed(Input.KeyShift)
	ctrl := Input.IsKeyPressed(Input.KeyCtrl)
	startAbs := node.AsNode3D().Position()
	startTime := world.time.Future()
	hasPath := false
	if shift || ctrl {
		if ar, ok := actionRendererFor(node); ok {
			if tail, end, active := ar.PathTail(); active {
				// tail.Y is the previous segment's seabed-relative depth — reconstruct
				// its absolute position for the Period distance.
				hasPath = true
				startAbs = swimTargetAbs(world.TerrainEditor, tail)
				if end > startTime {
					startTime = end
				}
			}
		}
	}
	world.space.Action(musical.Action{
		Author: world.id,
		Entity: aim.entity,
		// Target.Y is the depth ABOVE the seabed at Target.XZ (not absolute) — see
		// swimTargetAbs. Terrain-relative so the resting depth survives changes and a
		// dropping water level can still strand the fish.
		Target: Vector3.New(aim.x, aim.y-seabed, aim.z),
		Period: musical.Period(Vector3.Distance(startAbs, targetAbs) * Float.X(time.Second) * 5),
		Timing: startTime,
		Cancel: !hasPath,
		Repeat: ctrl,
		Commit: true,
	})
}

// cancelSwimAim drops an in-progress aim without issuing a move (e.g. on leaving
// the editor). No-op when not aiming.
func (world *Client) cancelSwimAim() {
	if !world.swimAim.active {
		return
	}
	world.swimAim.active = false
	world.hideSwimAimIndicator()
}

// ensureSwimAimIndicator lazily builds the reusable depth-indicator mesh (a
// MeshInstance3D + ImmediateMesh, drawn unshaded and on top of everything so the
// depth line reads through the water and terrain). Parented under the Client
// (world origin) so its world-space vertices map straight through.
func (world *Client) ensureSwimAimIndicator() {
	if world.swimAim.indicator != MeshInstance3D.Nil {
		return
	}
	mi := MeshInstance3D.New()
	im := ImmediateMesh.New()
	mi.SetMesh(im.AsMesh())
	mat := StandardMaterial3D.New()
	mat.AsBaseMaterial3D().SetShadingMode(BaseMaterial3D.ShadingModeUnshaded)
	mat.AsBaseMaterial3D().SetTransparency(BaseMaterial3D.TransparencyAlpha)
	mat.AsBaseMaterial3D().SetAlbedoColor(Color.RGBA{R: 0.25, G: 0.9, B: 1, A: 0.9})
	mat.AsBaseMaterial3D().SetNoDepthTest(true)
	mi.AsGeometryInstance3D().SetMaterialOverride(mat.AsMaterial())
	world.AsNode().AddChild(mi.AsNode())
	world.swimAim.indicator = mi
	world.swimAim.immediate = im
}

// drawSwimAim redraws the depth indicator at the current aim: a vertical line
// spanning the swimmable column at the target XZ (water surface → seabed) plus a
// 3D cross marking the target depth, so the player can read where on the Y axis
// they're aiming.
func (world *Client) drawSwimAim() {
	if world.TerrainEditor == nil {
		return
	}
	world.ensureSwimAimIndicator()
	im := world.swimAim.immediate
	im.ClearSurfaces()
	x, y, z := world.swimAim.x, world.swimAim.y, world.swimAim.z
	xz := Vector3.New(x, 0, z)
	surface := world.TerrainEditor.WaterSurfaceAt(xz)
	floor := world.TerrainEditor.HeightAt(xz)

	im.SurfaceBegin(Mesh.PrimitiveLines)
	// Depth column: water surface → seabed at the target XZ (the swimmable range).
	im.SurfaceAddVertex(Vector3.New(x, surface, z))
	im.SurfaceAddVertex(Vector3.New(x, floor, z))
	// Target marker: a 3D cross at the aimed point.
	s := swimAimCrossSize
	im.SurfaceAddVertex(Vector3.New(x-s, y, z))
	im.SurfaceAddVertex(Vector3.New(x+s, y, z))
	im.SurfaceAddVertex(Vector3.New(x, y-s, z))
	im.SurfaceAddVertex(Vector3.New(x, y+s, z))
	im.SurfaceAddVertex(Vector3.New(x, y, z-s))
	im.SurfaceAddVertex(Vector3.New(x, y, z+s))
	im.SurfaceEnd()

	world.swimAim.indicator.AsNode3D().SetVisible(true)
}

// hideSwimAimIndicator hides the depth indicator (kept warm for the next aim).
func (world *Client) hideSwimAimIndicator() {
	if world.swimAim.indicator != MeshInstance3D.Nil {
		world.swimAim.indicator.AsNode3D().SetVisible(false)
	}
}

// updateSwimPossess drives a possessed fish: mouse-aimed 3D swimming (W/S along
// the view direction — look up to rise, down to dive — A/D strafe), CLAMPED to
// the water column so it can never leave the water, with the swim clip picked by
// the dominant motion axis. The camera follows like self-flight (the mouse steers
// it; no yaw recenter). The pose is broadcast terrain-relative by sendPossessChange
// so peers reconstruct the depth (and its EntityAnimator the swim clip).
func (world *Client) updateSwimPossess(node Node3D.Instance, dt Float.X) {
	body := node.AsNode3D()
	pos := body.Position()
	fwd := cameraForward(world)
	heading := horizontal(fwd)
	right := Vector3.New(-heading.Z, 0, heading.X) // screen-right for a +Z heading

	move := Vector3.Zero
	if Input.IsKeyPressed(Input.KeyW) || Input.IsKeyPressed(Input.KeyUp) {
		move = Vector3.Add(move, fwd)
	}
	if Input.IsKeyPressed(Input.KeyS) || Input.IsKeyPressed(Input.KeyDown) {
		move = Vector3.Sub(move, fwd)
	}
	if Input.IsKeyPressed(Input.KeyD) || Input.IsKeyPressed(Input.KeyRight) {
		move = Vector3.Add(move, right)
	}
	if Input.IsKeyPressed(Input.KeyA) || Input.IsKeyPressed(Input.KeyLeft) {
		move = Vector3.Sub(move, right)
	}
	moving := Vector3.Length(move) > 0.001
	if moving {
		pos = Vector3.Add(pos, Vector3.MulX(Vector3.Normalized(move), swimControlSpeed*dt))
	}
	// Stay in the water: clamp Y into the column at the new XZ.
	if world.TerrainEditor != nil {
		pos.Y = world.TerrainEditor.ClampToWater(pos, pos.Y)
	}
	body.SetPosition(pos)
	faceFlightDirection(body, fwd)

	// Drive the clip directly — the EntityAnimator stands down while we possess.
	// Vertical swim when the motion is mostly up/down, else horizontal, else idle.
	want := "idle"
	if moving {
		mn := Vector3.Normalized(move)
		vert := mn.Y
		if vert < 0 {
			vert = -vert
		}
		horiz := Vector3.Length(Vector3.New(mn.X, 0, mn.Z))
		if vert > horiz {
			want = swimClipVertical
		} else {
			want = swimClipHorizontal
		}
	}
	if want != world.possess.intent {
		world.possess.intent = want
		playCritterClip(node, world.possess.player, want)
	}

	// Broadcast (Commit=false, terrain-relative via sendPossessChange) so peers see
	// it swim; the final pose is committed on exit.
	if time.Since(world.possess.lastSent) >= possessSendInterval {
		world.sendPossessChange(node, false)
		world.possess.lastSent = time.Now()
	}
	world.trackFlightCamera(body.GlobalPosition())
}
