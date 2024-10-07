package internal

import (
	"math"

	"grow.graphics/gd"
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

	mouseOver chan gd.Vector3

	TerrainRenderer *TerrainRenderer
	PreviewRenderer *PreviewRenderer

	Vulture *Vulture
}

func (world *World) Ready() {
	if world.Vulture == nil {
		world.Vulture = gd.Create(world.KeepAlive, new(Vulture))
	}
	world.mouseOver = make(chan gd.Vector3, 1)
	world.PreviewRenderer.preview = make(chan string, 1)
	world.PreviewRenderer.mouseOver = world.mouseOver
	world.PreviewRenderer.Vulture = world.Vulture
	editor_scene, ok := gd.Load[gd.PackedScene](world.KeepAlive, "res://ui/editor.tscn")
	if ok {
		editor, ok := gd.As[*UI](world.Temporary, editor_scene.Instantiate(world.KeepAlive, 0))
		if ok {
			editor.preview = world.PreviewRenderer.preview
			world.Super().AsNode().AddChild(editor.Super().AsNode(), false, 0)
		}
	}
	world.TerrainRenderer.mouseOver = world.mouseOver
	world.TerrainRenderer.Vulture = world.Vulture
	world.FocalPoint.Lens.Camera.AsNode3D().SetPosition(gd.Vector3{0, 1, 3})
	world.FocalPoint.Lens.Camera.AsNode3D().LookAt(gd.Vector3{0, 0, 0}, gd.Vector3{0, 1, 0}, false)
	world.Light.AsNode3D().SetRotation(gd.Vector3{-math.Pi / 2, 0, 0})
	world.TerrainRenderer.SetFocalPoint3D(gd.Vector3{})
	gd.RenderingServer(world.Temporary).SetDebugGenerateWireframes(true)
}

func (world *World) Process(dt gd.Float) {
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
	world.TerrainRenderer.SetFocalPoint3D(world.FocalPoint.AsNode3D().GetPosition())
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
