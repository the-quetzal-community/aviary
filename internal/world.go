package internal

import (
	"encoding/gob"
	"math"
	"os"
	"sync/atomic"

	"graphics.gd/classdb"
	"graphics.gd/classdb/Camera3D"
	"graphics.gd/classdb/DirectionalLight3D"
	"graphics.gd/classdb/Engine"
	"graphics.gd/classdb/Input"
	"graphics.gd/classdb/InputEvent"
	"graphics.gd/classdb/InputEventKey"
	"graphics.gd/classdb/InputEventMouseButton"
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/PackedScene"
	"graphics.gd/classdb/RenderingServer"
	"graphics.gd/classdb/Resource"
	"graphics.gd/classdb/Viewport"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Vector3"
	"the.quetzal.community/aviary/protocol/vulture"
)

// World represents a creative space accessible via Vulture.
type World struct {
	classdb.Extension[World, Node3D.Instance] `gd:"AviaryWorld"`

	Light DirectionalLight3D.Instance

	// FocalPoint is the point in the scene that the camera is
	// focused on, it is used to determine which areas should be
	// loaded and which should be unloaded.
	FocalPoint struct {
		Node3D.Instance

		Lens struct {
			Node3D.Instance

			Camera Camera3D.Instance
		}
	}

	mouseOver chan Vector3.XYZ

	PreviewRenderer *PreviewRenderer
	VultureRenderer *Renderer

	Vulture *Vulture

	saving atomic.Bool
}

func (world *World) AsNode() Node.Instance { return world.Super().AsNode() }

// Ready does a bunch of dependency injection and setup.
func (world *World) Ready() {
	if world.Vulture == nil {
		world.Vulture = &Vulture{
			api: vulture.New(),
		}
	}
	world.mouseOver = make(chan Vector3.XYZ, 100)
	world.PreviewRenderer.preview = make(chan Resource.Path, 1)
	world.VultureRenderer.texture = make(chan Resource.Path, 1)
	world.PreviewRenderer.mouseOver = world.mouseOver
	world.PreviewRenderer.Vulture = world.Vulture
	world.PreviewRenderer.terrain = world.VultureRenderer
	world.VultureRenderer.mouseOver = world.mouseOver
	world.VultureRenderer.Vulture = world.Vulture
	world.VultureRenderer.start()
	editor_scene := Resource.Load[PackedScene.Instance]("res://ui/editor.tscn")
	first := Node.Instance(editor_scene.Instantiate())
	editor, ok := classdb.As[*UI](first)
	if ok {
		editor.preview = world.PreviewRenderer.preview
		editor.texture = world.VultureRenderer.texture
		world.Super().AsNode().AddChild(editor.Super().AsNode())
	}
	world.FocalPoint.Lens.Camera.AsNode3D().SetPosition(Vector3.New(0, 1, 3))
	world.FocalPoint.Lens.Camera.AsNode3D().LookAt(Vector3.Zero)
	world.Light.AsNode3D().SetRotation(Vector3.New(-math.Pi/2, 0, 0))
	world.VultureRenderer.SetFocalPoint3D(Vector3.Zero)
	RenderingServer.SetDebugGenerateWireframes(true)
}

const speed = 8

func (world *World) Process(dt Float.X) {
	if Input.IsKeyPressed(Input.KeyQ) {
		world.FocalPoint.AsNode3D().GlobalRotate(Vector3.New(0, 1, 0), -dt)
	}
	if Input.IsKeyPressed(Input.KeyE) {
		world.FocalPoint.AsNode3D().GlobalRotate(Vector3.New(0, 1, 0), dt)
	}
	if Input.IsKeyPressed(Input.KeyA) || Input.IsKeyPressed(Input.KeyLeft) {
		world.FocalPoint.AsNode3D().Translate(Vector3.New(-speed*dt, 0, 0))
	}
	if Input.IsKeyPressed(Input.KeyD) || Input.IsKeyPressed(Input.KeyRight) {
		world.FocalPoint.AsNode3D().Translate(Vector3.New(speed*dt, 0, 0))
	}
	if Input.IsKeyPressed(Input.KeyS) || Input.IsKeyPressed(Input.KeyDown) {
		world.FocalPoint.AsNode3D().Translate(Vector3.New(0, 0, speed*dt))
	}
	if Input.IsKeyPressed(Input.KeyW) || Input.IsKeyPressed(Input.KeyUp) {
		world.FocalPoint.AsNode3D().Translate(Vector3.New(0, 0, -speed*dt))
	}
	if Input.IsKeyPressed(Input.KeyR) {
		world.FocalPoint.Lens.AsNode3D().Rotate(Vector3.New(1, 0, 0), -dt)
	}
	if Input.IsKeyPressed(Input.KeyF) {
		world.FocalPoint.Lens.AsNode3D().Rotate(Vector3.New(1, 0, 0), dt)
	}
	if Input.IsKeyPressed(Input.KeyEqual) {
		world.FocalPoint.Lens.Camera.AsNode3D().Translate(Vector3.New(0, 0, -0.5))
	}
	if Input.IsKeyPressed(Input.KeyMinus) {
		world.FocalPoint.Lens.Camera.AsNode3D().Translate(Vector3.New(0, 0, 0.5))
	}
	world.VultureRenderer.SetFocalPoint3D(world.FocalPoint.AsNode3D().Position())

	if !world.saving.Load() && Input.IsKeyPressed(Input.KeyCtrl) && Input.IsKeyPressed(Input.KeyS) {
		world.saving.Store(true)
		save, err := os.Create("save.vult")
		if err != nil {
			Engine.Raise(err)
			return
		}
		defer save.Close()
		if err := gob.NewEncoder(save).Encode(world.VultureRenderer.regions); err != nil {
			Engine.Raise(err)
			return
		}
		world.saving.Store(false)
	}
}

func (world *World) UnhandledInput(event InputEvent.Instance) {
	// Tilt the camera up and down with R and F.
	if event, ok := classdb.As[InputEventMouseButton.Instance](event); ok && !Input.IsKeyPressed(Input.KeyShift) {
		if event.ButtonIndex() == InputEventMouseButton.MouseButtonWheelUp {
			world.FocalPoint.Lens.Camera.AsNode3D().Translate(Vector3.New(0, 0, -0.4))
		}
		if event.ButtonIndex() == InputEventMouseButton.MouseButtonWheelDown {
			world.FocalPoint.Lens.Camera.AsNode3D().Translate(Vector3.New(0, 0, 0.4))
		}
	}
	if event, ok := classdb.As[InputEventKey.Instance](event); ok {
		if event.AsInputEvent().IsPressed() && event.Keycode() == InputEventKey.KeyF1 {
			vp := Viewport.Instance(world.Super().AsNode().GetViewport())
			vp.SetDebugDraw(vp.DebugDraw() ^ Viewport.DebugDrawWireframe)
		}
	}
}
