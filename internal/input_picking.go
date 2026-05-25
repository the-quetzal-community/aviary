package internal

import (
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/PhysicsDirectSpaceState3D"
	"graphics.gd/classdb/PhysicsRayQueryParameters3D"
	"graphics.gd/classdb/Viewport"
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
