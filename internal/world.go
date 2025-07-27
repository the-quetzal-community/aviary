package internal

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"io"
	"os"
	"sync/atomic"

	"graphics.gd/classdb"
	"graphics.gd/classdb/Camera3D"
	"graphics.gd/classdb/DirectionalLight3D"
	"graphics.gd/classdb/Input"
	"graphics.gd/classdb/InputEvent"
	"graphics.gd/classdb/InputEventKey"
	"graphics.gd/classdb/InputEventMouseButton"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/OS"
	"graphics.gd/classdb/PackedScene"
	"graphics.gd/classdb/RenderingServer"
	"graphics.gd/classdb/Resource"
	"graphics.gd/classdb/Viewport"
	"graphics.gd/variant/Angle"
	"graphics.gd/variant/Euler"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Path"
	"graphics.gd/variant/Vector3"
	"runtime.link/api"
	"runtime.link/api/stub"
	"the.quetzal.community/editor/echoable"
	"the.quetzal.community/protocol/echo"
)

// World represents a creative space accessible via Vulture.
type World struct {
	Node3D.Extension[World] `gd:"AviaryWorld"`

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

	TerrainTile *TerrainTile

	mouseOver chan Vector3.XYZ

	PreviewRenderer *PreviewRenderer
	VultureRenderer *Renderer

	edits echoable.API

	saving atomic.Bool
}

func (world *World) extend(ctx context.Context, buf []byte) error {
	path := OS.GetUserDataDir()
	if err := os.MkdirAll(path, 0755); err != nil {
		return err
	}
	file, err := os.OpenFile(path+"/local.echo", os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		return err
	}
	if _, err := file.Write(buf); err != nil {
		return err
	}
	return nil
}

func (world *World) listen(ctx context.Context) (<-chan []byte, int64) {
	ch := make(chan []byte, 1)
	context.AfterFunc(ctx, func() {
		close(ch)
	})
	return ch, 1500
}

func (world *World) opener(ctx context.Context, path string) (io.ReaderAt, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	stat, err := file.Stat()
	if err != nil {
		return nil, 0, err
	}
	return file, stat.Size(), nil
}

func (world *World) notify(ctx context.Context, buf []byte) error {
	return nil
}

func (world *World) crypto(context.Context) ([]crypto.PublicKey, crypto.Signer, error) {
	_, private, _ := ed25519.GenerateKey(nil)
	return nil, private, nil
}

// Ready does a bunch of dependency injection and setup.
func (world *World) Ready() {
	world.edits = echo.New(api.Import[echoable.API](stub.API, "", nil), echo.Clone{
		Crypto: world.crypto,
		Listen: world.listen,
		Opener: world.opener,
		Notify: world.notify,
		Extend: world.extend,
	})
	world.mouseOver = make(chan Vector3.XYZ, 100)
	world.PreviewRenderer.preview = make(chan Path.ToResource, 1)
	world.VultureRenderer.texture = make(chan Path.ToResource, 1)
	world.PreviewRenderer.mouseOver = world.mouseOver
	world.PreviewRenderer.edits = world.edits
	world.PreviewRenderer.terrain = world.VultureRenderer
	world.VultureRenderer.mouseOver = world.mouseOver
	world.VultureRenderer.edits = world.edits
	world.VultureRenderer.start()
	editor_scene := Resource.Load[PackedScene.Instance]("res://ui/editor.tscn")
	first := editor_scene.Instantiate()
	editor, ok := classdb.As[*UI](first)
	if ok {
		editor.preview = world.PreviewRenderer.preview
		editor.texture = world.VultureRenderer.texture
		world.AsNode().AddChild(editor.AsNode())
	}
	world.FocalPoint.Lens.Camera.AsNode3D().SetPosition(Vector3.New(0, 1, 3))
	world.FocalPoint.Lens.Camera.AsNode3D().LookAt(Vector3.Zero)
	world.Light.AsNode3D().SetRotation(Euler.Radians{X: -Angle.Pi / 2})
	world.VultureRenderer.SetFocalPoint3D(Vector3.Zero)
	world.TerrainTile.brushEvents = world.VultureRenderer.brushEvents
	RenderingServer.SetDebugGenerateWireframes(true)
}

const speed = 8

func (world *World) Process(dt Float.X) {
	if Input.IsKeyPressed(Input.KeyQ) {
		world.FocalPoint.AsNode3D().GlobalRotate(Vector3.New(0, 1, 0), -Angle.Radians(dt))
	}
	if Input.IsKeyPressed(Input.KeyE) {
		world.FocalPoint.AsNode3D().GlobalRotate(Vector3.New(0, 1, 0), Angle.Radians(dt))
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
		world.FocalPoint.Lens.AsNode3D().Rotate(Vector3.New(1, 0, 0), -Angle.Radians(dt))
	}
	if Input.IsKeyPressed(Input.KeyF) {
		world.FocalPoint.Lens.AsNode3D().Rotate(Vector3.New(1, 0, 0), Angle.Radians(dt))
	}
	if Input.IsKeyPressed(Input.KeyEqual) {
		world.FocalPoint.Lens.Camera.AsNode3D().Translate(Vector3.New(0, 0, -0.5))
	}
	if Input.IsKeyPressed(Input.KeyMinus) {
		world.FocalPoint.Lens.Camera.AsNode3D().Translate(Vector3.New(0, 0, 0.5))
	}
	world.VultureRenderer.SetFocalPoint3D(world.FocalPoint.AsNode3D().Position())
}

func (world *World) UnhandledInput(event InputEvent.Instance) {
	// Tilt the camera up and down with R and F.
	if !DrawExpanded.Load() {
		if event, ok := classdb.As[InputEventMouseButton.Instance](event); ok && !Input.IsKeyPressed(Input.KeyShift) {
			if event.ButtonIndex() == Input.MouseButtonWheelUp {
				world.FocalPoint.Lens.Camera.AsNode3D().Translate(Vector3.New(0, 0, -0.4))
			}
			if event.ButtonIndex() == Input.MouseButtonWheelDown {
				world.FocalPoint.Lens.Camera.AsNode3D().Translate(Vector3.New(0, 0, 0.4))
			}
		}
	}
	if event, ok := classdb.As[InputEventKey.Instance](event); ok {
		if event.AsInputEvent().IsPressed() && event.Keycode() == Input.KeyF1 {
			vp := Viewport.Get(world.AsNode())
			vp.SetDebugDraw(vp.DebugDraw() ^ Viewport.DebugDrawWireframe)
		}
	}
}
