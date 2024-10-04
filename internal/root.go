package internal

import (
	"context"
	"fmt"
	"math"
	"time"

	"grow.graphics/gd"

	"the.quetzal.community/aviary/protocol/vulture"
)

type Root struct {
	gd.Class[Root, gd.Node3D] `gd:"AviaryRoot"`

	Light gd.DirectionalLight3D

	FocalPoint struct {
		gd.Node3D

		Camera gd.Camera3D
	}

	// ActiveAreas is a container for all of the visible [Area]
	// nodes in the scene, Aviary will page areas in and
	// out depending on whether they are in focus of the
	// camera.
	ActiveAreas gd.Node3D // []Area
	CachedAreas gd.Node3D // []Area

	vulture vulture.API
	updates <-chan vulture.Vision
	uplifts <-chan [16 * 16]vulture.Vertex
}

func (root *Root) Ready() {
	if root.vulture.Uplift == nil {
		root.vulture = vulture.New()
	}
	root.FocalPoint.Camera.AsNode3D().SetPosition(gd.Vector3{0, 1, 3})
	root.FocalPoint.Camera.AsNode3D().LookAt(gd.Vector3{0, 0, 0}, gd.Vector3{0, 1, 0}, false)
	root.Light.AsNode3D().SetRotation(gd.Vector3{-math.Pi / 2, 0, 0})

	uplifts := make(chan [16 * 16]vulture.Vertex)
	root.uplifts = uplifts
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		terrain, err := root.vulture.Uplift(ctx, vulture.Uplift{
			Area: vulture.Area{},
			Cell: 0,
			Size: 0,
			Lift: 0,
		})
		if err != nil {
			fmt.Println(err)
			return
		}
		uplifts <- terrain
	}()
}

func (root *Root) Process(delta gd.Float) {
	tmp := root.Temporary
	Input := gd.Input(tmp)

	select {
	case <-root.uplifts:
		mesh := gd.Create(root.KeepAlive, new(gd.MeshInstance3D))
		plane := gd.Create(tmp, new(gd.PlaneMesh))
		plane.SetSize(gd.Vector2{16, 16})
		mesh.SetMesh(plane.AsMesh())
		root.ActiveAreas.AsNode().AddChild(mesh.AsNode(), false, 0)
	default:
	}
	if Input.IsKeyPressed(gd.KeyQ) {
		root.FocalPoint.AsNode3D().GlobalRotate(gd.Vector3{0, 1, 0}, -delta)
	}
	if Input.IsKeyPressed(gd.KeyE) {
		root.FocalPoint.AsNode3D().GlobalRotate(gd.Vector3{0, 1, 0}, delta)
	}
	if Input.IsKeyPressed(gd.KeyA) || Input.IsKeyPressed(gd.KeyLeft) {
		root.FocalPoint.AsNode3D().Translate(gd.Vector3{-float32(4 * delta), 0, 0})
	}
	if Input.IsKeyPressed(gd.KeyD) || Input.IsKeyPressed(gd.KeyRight) {
		root.FocalPoint.AsNode3D().Translate(gd.Vector3{float32(4 * delta), 0, 0})
	}
	if Input.IsKeyPressed(gd.KeyS) || Input.IsKeyPressed(gd.KeyDown) {
		root.FocalPoint.AsNode3D().Translate(gd.Vector3{0, 0, float32(4 * delta)})
	}
	if Input.IsKeyPressed(gd.KeyW) || Input.IsKeyPressed(gd.KeyUp) {
		root.FocalPoint.AsNode3D().Translate(gd.Vector3{0, 0, -float32(4 * delta)})
	}
}
