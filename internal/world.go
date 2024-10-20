package internal

import (
	"encoding/gob"
	"math"
	"os"
	"sync/atomic"

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

	PreviewRenderer *PreviewRenderer
	VultureRenderer *Renderer

	Vulture *Vulture

	saving atomic.Bool
}

// Ready does a bunch of dependency injection and setup.
func (world *World) Ready() {
	if world.Vulture == nil {
		world.Vulture = gd.Create(world.KeepAlive, new(Vulture))
	}
	world.mouseOver = make(chan gd.Vector3, 100)
	world.PreviewRenderer.preview = make(chan string, 1)
	world.VultureRenderer.texture = make(chan string, 1)
	world.PreviewRenderer.mouseOver = world.mouseOver
	world.PreviewRenderer.Vulture = world.Vulture
	world.PreviewRenderer.terrain = world.VultureRenderer
	world.VultureRenderer.mouseOver = world.mouseOver
	world.VultureRenderer.Vulture = world.Vulture
	world.VultureRenderer.start()
	editor_scene, ok := gd.Load[gd.PackedScene](world.KeepAlive, "res://ui/editor.tscn")
	if ok {
		editor, ok := gd.As[*UI](world.Temporary, editor_scene.Instantiate(world.KeepAlive, 0))
		if ok {
			editor.preview = world.PreviewRenderer.preview
			editor.texture = world.VultureRenderer.texture
			world.Super().AsNode().AddChild(editor.Super().AsNode(), false, 0)
		}
	}
	world.FocalPoint.Lens.Camera.AsNode3D().SetPosition(gd.Vector3{0, 1, 3})
	world.FocalPoint.Lens.Camera.AsNode3D().LookAt(gd.Vector3{0, 0, 0}, gd.Vector3{0, 1, 0}, false)
	world.Light.AsNode3D().SetRotation(gd.Vector3{-math.Pi / 2, 0, 0})
	world.VultureRenderer.SetFocalPoint3D(gd.Vector3{})
	gd.RenderingServer(world.Temporary).SetDebugGenerateWireframes(true)
}

const speed = 8

func (world *World) Process(dt gd.Float) {
	tmp := world.Temporary
	Input := gd.Input(tmp)
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
	if Input.IsKeyPressed(gd.KeyEqual) {
		world.FocalPoint.Lens.Camera.AsNode3D().Translate(gd.Vector3{0, 0, -0.5})
	}
	if Input.IsKeyPressed(gd.KeyMinus) {
		world.FocalPoint.Lens.Camera.AsNode3D().Translate(gd.Vector3{0, 0, 0.5})
	}
	world.VultureRenderer.SetFocalPoint3D(world.FocalPoint.AsNode3D().GetPosition())

	if !world.saving.Load() && Input.IsKeyPressed(gd.KeyCtrl) && Input.IsKeyPressed(gd.KeyS) {
		world.saving.Store(true)
		save, err := os.Create("save.vult")
		if err != nil {
			tmp.Printerr(tmp.Variant(tmp.String(err.Error())))
			return
		}
		defer save.Close()
		if err := gob.NewEncoder(save).Encode(world.VultureRenderer.regions); err != nil {
			tmp.Printerr(tmp.Variant(tmp.String(err.Error())))
			return
		}
		world.saving.Store(false)
	}
}

func (world *World) UnhandledInput(event gd.InputEvent) {
	tmp := world.Temporary
	Input := gd.Input(tmp)
	// Tilt the camera up and down with R and F.
	if event, ok := gd.As[gd.InputEventMouseButton](world.Temporary, event); ok && !Input.IsKeyPressed(gd.KeyShift) {
		if event.GetButtonIndex() == gd.MouseButtonWheelUp {
			world.FocalPoint.Lens.Camera.AsNode3D().Translate(gd.Vector3{0, 0, -0.4})
		}
		if event.GetButtonIndex() == gd.MouseButtonWheelDown {
			world.FocalPoint.Lens.Camera.AsNode3D().Translate(gd.Vector3{0, 0, 0.4})
		}
	}
	if event, ok := gd.As[gd.InputEventKey](world.Temporary, event); ok {
		if event.AsInputEvent().IsPressed() && event.GetKeycode() == gd.KeyF1 {
			vp := world.Super().AsNode().GetViewport(tmp)
			vp.SetDebugDraw(vp.GetDebugDraw() ^ gd.ViewportDebugDrawWireframe)
		}
	}
}
