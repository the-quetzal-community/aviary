package internal

import (
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/PhysicsDirectSpaceState3D"
	"graphics.gd/classdb/PhysicsRayQueryParameters3D"
	"graphics.gd/classdb/Viewport"
)

func MousePicker(node Node3D.Instance) PhysicsDirectSpaceState3D.PhysicsDirectSpaceState3D_Intersection {
	cam := Viewport.Get(node.AsNode()).GetCamera3d()
	space_state := node.AsNode3D().GetWorld3d().DirectSpaceState()
	mpos_2d := Viewport.Get(node.AsNode()).GetMousePosition()
	ray_from, ray_to := cam.ProjectRayOrigin(mpos_2d), cam.ProjectPosition(mpos_2d, 1000)
	var query = PhysicsRayQueryParameters3D.Create(ray_from, ray_to, nil)
	return space_state.IntersectRay(query)
}
