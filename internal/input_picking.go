package internal

import (
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/PhysicsDirectSpaceState3D"
	"graphics.gd/classdb/PhysicsRayQueryParameters3D"
	"graphics.gd/classdb/Viewport"
	"graphics.gd/classdb/XRController3D"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Vector3"
)

func MousePicker(node Node3D.Instance) PhysicsDirectSpaceState3D.PhysicsDirectSpaceState3D_Intersection {
	cam := Viewport.Get(node.AsNode()).GetCamera3d()
	space_state := node.AsNode3D().GetWorld3d().DirectSpaceState()
	mpos_2d := Viewport.Get(node.AsNode()).GetMousePosition()
	ray_from, ray_to := cam.ProjectRayOrigin(mpos_2d), cam.ProjectPosition(mpos_2d, 1000)
	var query = PhysicsRayQueryParameters3D.Create(ray_from, ray_to, nil)
	return space_state.IntersectRay(query)
}

// PreviewPicker is MousePicker's VR-aware sibling: in XR mode it
// raycasts from the right controller's aim instead of the mouse, so
// the preview node follows the user's laser pointer rather than
// staying glued to whatever the last 2D cursor position was (which
// in VR is meaningless). Editors call this from PhysicsProcess for
// preview placement so the design ghost tracks the pointer.
//
// The mask excludes the VR UI panels' collision layers — without
// that, the laser hits the design palette (which sits between the
// user and the terrain) and the preview never reaches the ground.
func (world *Client) PreviewPicker() PhysicsDirectSpaceState3D.PhysicsDirectSpaceState3D_Intersection {
	if world.xr && world.xrRight != XRController3D.Nil {
		t := world.xrRight.AsNode3D().GlobalTransform()
		origin := t.Origin
		forward := Vector3.XYZ{X: -t.Basis.Z.X, Y: -t.Basis.Z.Y, Z: -t.Basis.Z.Z}
		to := Vector3.XYZ{X: origin.X + forward.X*1000, Y: origin.Y + forward.Y*1000, Z: origin.Z + forward.Z*1000}
		space := world.AsNode3D().GetWorld3d().DirectSpaceState()
		query := PhysicsRayQueryParameters3D.Create(origin, to, nil)
		query.SetCollisionMask(int(^uint32(vrUILayerLeft | vrUILayerRight | vrUILayerPalette)))
		return space.IntersectRay(query)
	}
	return MousePicker(world.AsNode3D())
}

// MouseRay returns the current mouse ray (origin and direction vector, not
// necessarily unit length) in world space for the viewport associated with node.
func MouseRay(node Node3D.Instance) (origin, dir Vector3.XYZ) {
	cam := Viewport.Get(node.AsNode()).GetCamera3d()
	mpos := Viewport.Get(node.AsNode()).GetMousePosition()
	origin = cam.ProjectRayOrigin(mpos)
	far := cam.ProjectPosition(mpos, 1000)
	dir = Vector3.Sub(far, origin)
	return
}

// IntersectRayPlane computes the intersection of a ray with a plane.
// Returns the point and true on success. Returns zero,false if the ray is
// parallel to the plane or the intersection is behind the ray origin.
func IntersectRayPlane(rayOrigin, rayDir, planePoint, planeNormal Vector3.XYZ) (Vector3.XYZ, bool) {
	denom := Vector3.Dot(planeNormal, rayDir)
	if Float.Abs(denom) < 1e-6 {
		return Vector3.Zero, false
	}
	t := Vector3.Dot(Vector3.Sub(planePoint, rayOrigin), planeNormal) / denom
	if t < 0 {
		return Vector3.Zero, false
	}
	return Vector3.Add(rayOrigin, Vector3.MulX(rayDir, t)), true
}
