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

	Grid gd.Node3D

	Camera gd.Camera3D
	Light  gd.DirectionalLight3D

	vulture vulture.API
	updates <-chan vulture.Vision
	uplifts <-chan [16 * 16]vulture.Vertex
}

func (root *Root) Ready() {
	if root.vulture.Uplift == nil {
		root.vulture = vulture.New()
	}
	root.Camera.AsNode3D().SetPosition(gd.Vector3{0, 1, 3})
	root.Camera.AsNode3D().LookAt(gd.Vector3{0, 0, 0}, gd.Vector3{0, 1, 0}, false)
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
	select {
	case <-root.uplifts:
		mesh := gd.Create(root.KeepAlive, new(gd.MeshInstance3D))
		plane := gd.Create(root.KeepAlive, new(gd.PlaneMesh)) // FIXME refcount issue?
		plane.SetSize(gd.Vector2{16, 16})
		mesh.SetMesh(plane.AsMesh())
		root.Grid.AsNode().AddChild(mesh.AsNode(), false, 0)
	default:
	}
	tmp := root.Temporary
	Input := gd.Input(tmp)
	// FIXME remove string name allocations
	if Input.IsActionPressed(tmp.StringName("ui_left"), false) {
		root.Camera.AsNode3D().Translate(gd.Vector3{-float32(4 * delta), 0, 0})
	}
	if Input.IsActionPressed(tmp.StringName("ui_right"), false) {
		root.Camera.AsNode3D().Translate(gd.Vector3{float32(4 * delta), 0, 0})
	}
	if Input.IsActionPressed(tmp.StringName("ui_down"), false) {
		root.Camera.AsNode3D().Translate(gd.Vector3{0, 0, float32(4 * delta)})
	}
	if Input.IsActionPressed(tmp.StringName("ui_up"), false) {
		root.Camera.AsNode3D().Translate(gd.Vector3{0, 0, -float32(4 * delta)})
	}
}
