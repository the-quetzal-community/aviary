package internal

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"

	"graphics.gd/classdb"
	"graphics.gd/classdb/Camera3D"
	"graphics.gd/classdb/DirectionalLight3D"
	"graphics.gd/classdb/Engine"
	"graphics.gd/classdb/Input"
	"graphics.gd/classdb/InputEvent"
	"graphics.gd/classdb/InputEventKey"
	"graphics.gd/classdb/InputEventMouseButton"
	"graphics.gd/classdb/Node3D"
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
	"runtime.link/api/rest"
	"runtime.link/xyz"
	"the.quetzal.community/aviary/internal/community"
	"the.quetzal.community/aviary/internal/ice/signalling"
	"the.quetzal.community/aviary/internal/networking"
)

const (
	SignallingHost = "https://via.quetzal.community"
	OneTimeUseCode = "4d128c18-23e9-4b98-bf70-2cb94295406f"
)

// Client represents a creative space accessible via Aviary.
type Client struct {
	Node3D.Extension[Client] `gd:"AviaryWorld"`

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

	signalling signalling.API

	network networking.Connectivity
	updates chan []byte // channel for updates from the server
	println chan string

	log *community.Log

	saving atomic.Bool

	user_id string

	api *community.Log

	objects map[xyz.Pair[string, community.Object]]Node3D.ID
}

func (world *Client) isOnline() bool {
	return world.user_id != ""
}

var clientReady sync.WaitGroup

func init() {
	clientReady.Add(1)
}

func (world *Client) goOnline() error {
	clientReady.Wait()
	user_id, err := world.signalling.LookupUser(context.Background())
	if err != nil {
		return err
	}
	world.user_id = user_id
	return nil
}

func (world *Client) apiJoin(code networking.Code) {
	if err := world.network.Join(code, world.updates); err != nil {
		Engine.Raise(fmt.Errorf("failed to join room %s: %w", code, err))
		return
	}
	world.api = community.SendTo(world.network.Send) // FIXME race
}

func (world *Client) apiHost() (networking.Code, error) {
	var habitat community.Habitat
	code, err := world.network.Host(world.updates, func(client networking.Client) {
		out := community.SendVia(client.Send)
		habitat.AddClient(out)
		defer habitat.DelClient(out)
		community.Process(client.Recv, habitat.Log())
	})
	if err != nil {
		return "", fmt.Errorf("failed to host room: %w", err)
	}
	world.api = community.SendTo(world.network.Send) // FIXME race?
	return code, nil
}

// Ready does a bunch of dependency injection and setup.
func (world *Client) Ready() {
	defer clientReady.Done()
	world.println = make(chan string, 10)
	world.log = world.Log()
	world.api = world.log
	world.network.Raise = Engine.Raise
	world.network.Print = func(format string, args ...any) {
		select {
		case world.println <- fmt.Sprintf(format, args...):
		default:
			os.Stdout.WriteString(fmt.Sprintf(format, args...))
		}
	}
	world.updates = make(chan []byte, 20)
	world.signalling = api.Import[signalling.API](rest.API, SignallingHost, rest.Header("Authorization", "Bearer "+OneTimeUseCode))
	world.mouseOver = make(chan Vector3.XYZ, 100)
	world.PreviewRenderer.preview = make(chan Path.ToResource, 1)
	world.PreviewRenderer.client = world
	world.VultureRenderer.texture = make(chan Path.ToResource, 1)
	world.PreviewRenderer.mouseOver = world.mouseOver
	world.PreviewRenderer.terrain = world.VultureRenderer
	world.VultureRenderer.mouseOver = world.mouseOver
	world.VultureRenderer.start()
	editor_scene := Resource.Load[PackedScene.Instance]("res://ui/editor.tscn")
	first := editor_scene.Instantiate()
	editor, ok := classdb.As[*UI](first)
	if ok {
		editor.preview = world.PreviewRenderer.preview
		editor.texture = world.VultureRenderer.texture
		editor.client = world
		world.AsNode().AddChild(editor.AsNode())
		editor.Setup()
	}
	world.FocalPoint.Lens.Camera.AsNode3D().SetPosition(Vector3.New(0, 1, 3))
	world.FocalPoint.Lens.Camera.AsNode3D().LookAt(Vector3.Zero)
	world.Light.AsNode3D().SetRotation(Euler.Radians{X: -Angle.Pi / 2})
	world.VultureRenderer.SetFocalPoint3D(Vector3.Zero)
	world.TerrainTile.brushEvents = world.VultureRenderer.brushEvents
	RenderingServer.SetDebugGenerateWireframes(true)
}

const speed = 8

func (world *Client) Log() *community.Log {
	if world.objects == nil {
		world.objects = make(map[xyz.Pair[string, community.Object]]Node3D.ID)
	}
	return &community.Log{
		InsertObject: func(design string, initial community.Object) {
			container := world.VultureRenderer.AsNode()
			node := Resource.Load[PackedScene.Is[Node3D.Instance]](design).Instantiate()
			node.SetPosition(initial.Offset)
			node.SetRotation(initial.Angles)
			node.SetScale(Vector3.New(0.1, 0.1, 0.1))
			world.objects[xyz.NewPair(design, initial)] = node.ID()
			container.AddChild(node.AsNode())
		},
	}
}

func (world *Client) Process(dt Float.X) {
	select {
	case update := <-world.updates:
		community.ProcessSingle(update, world.Log())
	case msg := <-world.println:
		os.Stderr.WriteString(msg)
	default:
	}
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

func (world *Client) UnhandledInput(event InputEvent.Instance) {
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
