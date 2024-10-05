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

		Lens struct {
			gd.Node3D

			Camera gd.Camera3D
		}
	}

	// ActiveAreas is a container for all of the visible [Area]
	// nodes in the scene, Aviary will page areas in and
	// out depending on whether they are in focus of the
	// camera.
	ActiveAreas gd.Node3D // []Area
	CachedAreas gd.Node3D // []Area

	vulture vulture.API
	updates <-chan vulture.Vision
	uplifts chan vulture.Terrain

	loadedAreas map[vulture.Area]bool

	grass *gd.StandardMaterial3D
}

func (root *Root) Ready() {
	if root.vulture.Uplift == nil {
		root.vulture = vulture.New()
	}
	root.FocalPoint.Lens.Camera.AsNode3D().SetPosition(gd.Vector3{0, 1, 3})
	root.FocalPoint.Lens.Camera.AsNode3D().LookAt(gd.Vector3{0, 0, 0}, gd.Vector3{0, 1, 0}, false)
	root.Light.AsNode3D().SetRotation(gd.Vector3{-math.Pi / 2, 0, 0})

	root.loadedAreas = make(map[vulture.Area]bool)
	root.uplifts = make(chan vulture.Terrain)
	root.uplift(gd.Vector2{})

	texture, ok := gd.Load[gd.Texture2D](root.Temporary, "res://terrain/alpine_grass.png")
	if !ok {
		return
	}

	root.grass = gd.Create(root.KeepAlive, new(gd.StandardMaterial3D))
	root.grass.AsBaseMaterial3D().SetTexture(gd.BaseMaterial3DTextureAlbedo, texture)

	gd.RenderingServer(root.Temporary).SetDebugGenerateWireframes(true)
}

func (root *Root) uplift(pos gd.Vector2) {
	// transform to vulture area coordinates in multiples of 16
	if pos[0] < 0 {
		pos[0] -= 16
	}
	if pos[1] < 0 {
		pos[1] -= 16
	}
	v2i := pos.Divf(16).Vector2i()
	// we need to load all 9 neighboring areas
	for x := int16(-1); x <= 1; x++ {
		for y := int16(-1); y <= 1; y++ {
			area := vulture.Area{int16(v2i.X()) + x, int16(v2i.Y()) + y}
			if root.loadedAreas[area] {
				continue
			}
			root.loadedAreas[area] = true
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				terrain, err := root.vulture.Uplift(ctx, vulture.Uplift{
					Area: area,
					Cell: 0,
					Size: 0,
					Lift: 0,
				})
				if err != nil {
					fmt.Println(err)
					return
				}
				root.uplifts <- terrain
			}()
		}
	}
}

func (root *Root) Process(dt gd.Float) {
	tmp := root.Temporary
	select {
	case terrain := <-root.uplifts:
		mesh := gd.Create(root.KeepAlive, new(gd.MeshInstance3D))
		plane := gd.Create(tmp, new(gd.PlaneMesh))
		plane.SetSize(gd.Vector2{16, 16})
		plane.SetSubdivideDepth(14)
		plane.SetSubdivideWidth(14)
		mesh.AsGeometryInstance3D().SetMaterialOverride(root.grass.AsMaterial())
		mesh.SetMesh(plane.AsMesh())
		mesh.AsNode3D().SetPosition(gd.Vector3{
			float32(terrain.Area[0])*16 + 8,
			0,
			float32(terrain.Area[1])*16 + 8,
		})
		root.ActiveAreas.AsNode().AddChild(mesh.AsNode(), false, 0)
	default:
	}
	root.cameraControl(dt)
}

func (root *Root) cameraControl(dt gd.Float) {
	Input := gd.Input(root.Temporary)
	const speed = 16
	if Input.IsKeyPressed(gd.KeyQ) {
		root.FocalPoint.AsNode3D().GlobalRotate(gd.Vector3{0, 1, 0}, -dt)
	}
	if Input.IsKeyPressed(gd.KeyE) {
		root.FocalPoint.AsNode3D().GlobalRotate(gd.Vector3{0, 1, 0}, dt)
	}
	if Input.IsKeyPressed(gd.KeyA) || Input.IsKeyPressed(gd.KeyLeft) {
		root.FocalPoint.AsNode3D().Translate(gd.Vector3{-float32(speed * dt), 0, 0})
	}
	if Input.IsKeyPressed(gd.KeyD) || Input.IsKeyPressed(gd.KeyRight) {
		root.FocalPoint.AsNode3D().Translate(gd.Vector3{float32(speed * dt), 0, 0})
	}
	if Input.IsKeyPressed(gd.KeyS) || Input.IsKeyPressed(gd.KeyDown) {
		root.FocalPoint.AsNode3D().Translate(gd.Vector3{0, 0, float32(speed * dt)})
	}
	if Input.IsKeyPressed(gd.KeyW) || Input.IsKeyPressed(gd.KeyUp) {
		root.FocalPoint.AsNode3D().Translate(gd.Vector3{0, 0, -float32(speed * dt)})
	}
	if Input.IsKeyPressed(gd.KeyR) {
		root.FocalPoint.Lens.AsNode3D().Rotate(gd.Vector3{1, 0, 0}, -dt)
	}
	if Input.IsKeyPressed(gd.KeyF) {
		root.FocalPoint.Lens.AsNode3D().Rotate(gd.Vector3{1, 0, 0}, dt)
	}
	pos := root.FocalPoint.AsNode3D().GetPosition()
	root.uplift(gd.Vector2{pos[0], pos[2]})
}

func (root *Root) UnhandledInput(event gd.InputEvent) {
	tmp := root.Temporary
	if event, ok := gd.As[gd.InputEventMouseButton](root.Temporary, event); ok {
		if event.GetButtonIndex() == gd.MouseButtonWheelUp {
			root.FocalPoint.Lens.Camera.AsNode3D().Translate(gd.Vector3{0, 0, -0.5})
		}
		if event.GetButtonIndex() == gd.MouseButtonWheelDown {
			root.FocalPoint.Lens.Camera.AsNode3D().Translate(gd.Vector3{0, 0, 0.5})
		}
	}
	if event, ok := gd.As[gd.InputEventKey](root.Temporary, event); ok {
		if event.AsInputEvent().IsPressed() && event.GetKeycode() == gd.KeyF1 {
			vp := root.Super().AsNode().GetViewport(tmp)
			vp.SetDebugDraw(vp.GetDebugDraw() ^ gd.ViewportDebugDrawWireframe)
		}
	}
}
