package internal

import (
	"context"
	"fmt"
	"math"
	"time"

	"grow.graphics/gd"

	"the.quetzal.community/aviary/protocol/vulture"
)

// World represents a creative space accessible via Vulture.
type World struct {
	gd.Class[World, gd.Node3D] `gd:"AviaryWorld"`

	Light gd.DirectionalLight3D

	// FocalPoint is the point in the scene that the camera is
	// focused on, it is used to determine which areas should be
	// loaded and which should be unloaded.
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

	shaderPool *TerrainShaderPool
}

func (world *World) Ready() {
	if world.vulture.Uplift == nil {
		world.vulture = vulture.New()
	}
	world.FocalPoint.Lens.Camera.AsNode3D().SetPosition(gd.Vector3{0, 1, 3})
	world.FocalPoint.Lens.Camera.AsNode3D().LookAt(gd.Vector3{0, 0, 0}, gd.Vector3{0, 1, 0}, false)
	world.Light.AsNode3D().SetRotation(gd.Vector3{-math.Pi / 2, 0, 0})

	world.loadedAreas = make(map[vulture.Area]bool)
	world.uplifts = make(chan vulture.Terrain)
	world.uplift(gd.Vector2{})

	world.shaderPool = gd.Create(world.KeepAlive, new(TerrainShaderPool))

	gd.RenderingServer(world.Temporary).SetDebugGenerateWireframes(true)
}

func (world *World) uplift(pos gd.Vector2) {
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
			if world.loadedAreas[area] {
				continue
			}
			world.loadedAreas[area] = true
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				terrain, err := world.vulture.Uplift(ctx, vulture.Uplift{
					Area: area,
					Cell: 0,
					Size: 0,
					Lift: 0,
				})
				if err != nil {
					fmt.Println(err)
					return
				}
				world.uplifts <- terrain
			}()
		}
	}
}

func (world *World) Process(dt gd.Float) {
	//tmp := world.Temporary
	select {
	case terrain := <-world.uplifts:
		area := gd.Create(world.KeepAlive, new(TerrainTile))
		area.vulture = terrain
		area.shaders = world.shaderPool
		//area.Super().AsNode().SetName(tmp.String(fmt.Sprintf("%dx%dy", terrain.Area[0], terrain.Area[1])))
		world.ActiveAreas.AsNode().AddChild(area.Super().AsNode(), false, 0)
	default:
	}
	world.cameraControl(dt)
}

func (world *World) cameraControl(dt gd.Float) {
	Input := gd.Input(world.Temporary)
	const speed = 16
	if Input.IsKeyPressed(gd.KeyQ) {
		world.FocalPoint.AsNode3D().GlobalRotate(gd.Vector3{0, 1, 0}, -dt)
	}
	if Input.IsKeyPressed(gd.KeyE) {
		world.FocalPoint.AsNode3D().GlobalRotate(gd.Vector3{0, 1, 0}, dt)
	}
	if Input.IsKeyPressed(gd.KeyA) || Input.IsKeyPressed(gd.KeyLeft) {
		world.FocalPoint.AsNode3D().Translate(gd.Vector3{-float32(speed * dt), 0, 0})
	}
	if Input.IsKeyPressed(gd.KeyD) || Input.IsKeyPressed(gd.KeyRight) {
		world.FocalPoint.AsNode3D().Translate(gd.Vector3{float32(speed * dt), 0, 0})
	}
	if Input.IsKeyPressed(gd.KeyS) || Input.IsKeyPressed(gd.KeyDown) {
		world.FocalPoint.AsNode3D().Translate(gd.Vector3{0, 0, float32(speed * dt)})
	}
	if Input.IsKeyPressed(gd.KeyW) || Input.IsKeyPressed(gd.KeyUp) {
		world.FocalPoint.AsNode3D().Translate(gd.Vector3{0, 0, -float32(speed * dt)})
	}
	if Input.IsKeyPressed(gd.KeyR) {
		world.FocalPoint.Lens.AsNode3D().Rotate(gd.Vector3{1, 0, 0}, -dt)
	}
	if Input.IsKeyPressed(gd.KeyF) {
		world.FocalPoint.Lens.AsNode3D().Rotate(gd.Vector3{1, 0, 0}, dt)
	}
	pos := world.FocalPoint.AsNode3D().GetPosition()
	world.uplift(gd.Vector2{pos[0], pos[2]})
}

func (world *World) UnhandledInput(event gd.InputEvent) {
	tmp := world.Temporary
	Input := gd.Input(tmp)
	// Tilt the camera up and down with R and F.
	if event, ok := gd.As[gd.InputEventMouseButton](world.Temporary, event); ok && !Input.IsKeyPressed(gd.KeyShift) {
		if event.GetButtonIndex() == gd.MouseButtonWheelUp {
			world.FocalPoint.Lens.Camera.AsNode3D().Translate(gd.Vector3{0, 0, -0.5})
		}
		if event.GetButtonIndex() == gd.MouseButtonWheelDown {
			world.FocalPoint.Lens.Camera.AsNode3D().Translate(gd.Vector3{0, 0, 0.5})
		}
	}
	if event, ok := gd.As[gd.InputEventKey](world.Temporary, event); ok {
		if event.AsInputEvent().IsPressed() && event.GetKeycode() == gd.KeyF1 {
			vp := world.Super().AsNode().GetViewport(tmp)
			vp.SetDebugDraw(vp.GetDebugDraw() ^ gd.ViewportDebugDrawWireframe)
		}
	}
}
