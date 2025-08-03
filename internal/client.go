package internal

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"sync/atomic"

	"github.com/pion/webrtc/v4"
	"graphics.gd/classdb"
	"graphics.gd/classdb/Camera3D"
	"graphics.gd/classdb/DirectionalLight3D"
	"graphics.gd/classdb/Engine"
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
	"runtime.link/api/rest"
	"runtime.link/api/xray"
	"the.quetzal.community/aviary/internal/ice/signalling"

	"github.com/gorilla/websocket"
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
	peer       *webrtc.PeerConnection
	sock       *websocket.Conn

	saving atomic.Bool

	user_id string
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

func (world *Client) ice() error {
	dialer := websocket.Dialer{}
	var err error
	world.sock, _, err = dialer.Dial("wss://via.quetzal.community/session", http.Header{
		"Authorization": []string{"Bearer " + OneTimeUseCode},
	})
	if err != nil {
		return err
	}
	mtype, message, err := world.sock.ReadMessage()
	if err != nil {
		return err
	}
	if mtype != websocket.TextMessage {
		return errors.New("unexpected websocket message type")
	}
	var servers struct {
		Data []webrtc.ICEServer `json:"data"`
	}
	if err := json.Unmarshal(message, &servers); err != nil {
		return err
	}
	world.peer, err = webrtc.NewPeerConnection(webrtc.Configuration{
		ICEServers: servers.Data,
	})
	if err != nil {
		return err
	}
	world.peer.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		fmt.Println("Connection state changed:", state.String())
	})
	return nil
}

func (world *Client) apiJoin(code signalling.Code) {
	if err := world.ice(); err != nil {
		Engine.Raise(xray.New(err))
		return
	}
	offer, err := world.signalling.LookupRoom(context.Background(), code)
	if err != nil {
		Engine.Raise(xray.New(err))
		return
	}
	world.peer.SetRemoteDescription(offer)
	answer, err := world.peer.CreateAnswer(nil)
	if err != nil {
		Engine.Raise(xray.New(err))
		return
	}
	if err := world.peer.SetLocalDescription(answer); err != nil {
		Engine.Raise(xray.New(err))
		return
	}
	<-webrtc.GatheringCompletePromise(world.peer)
	if err := world.signalling.AnswerRoom(context.Background(), code, *world.peer.LocalDescription()); err != nil {
		Engine.Raise(xray.New(err))
		return
	}
}

func (world *Client) apiHost() (signalling.Code, error) {
	if err := world.ice(); err != nil {
		return "", err
	}
	_, err := world.peer.CreateDataChannel("data", nil)
	if err != nil {
		return "", err
	}
	offer, err := world.peer.CreateOffer(nil)
	if err != nil {
		return "", err
	}
	if err := world.peer.SetLocalDescription(offer); err != nil {
		return "", err
	}
	code, err := world.signalling.CreateRoom(context.Background(), *world.peer.LocalDescription())
	if err != nil {
		return "", err
	}
	world.peer.OnICECandidate(func(*webrtc.ICECandidate) {
		if err := world.signalling.UpdateRoom(context.Background(), code, *world.peer.LocalDescription()); err != nil {
			Engine.Raise(err)
		}
	})
	dialer := websocket.Dialer{}
	world.sock, _, err = dialer.Dial("wss://via.quetzal.community/room/"+string(code), http.Header{
		"Authorization": []string{"Bearer " + OneTimeUseCode},
	})
	if err != nil {
		return "", err
	}
	go func() {
		for {
			mtype, message, err := world.sock.ReadMessage()
			if err != nil {
				if errors.Is(err, websocket.ErrCloseSent) {
					return
				}
				Engine.Raise(xray.New(err))
				return
			}
			if mtype != websocket.TextMessage {
				Engine.Raise(fmt.Errorf("unexpected websocket message type: %d", mtype))
				return
			}
			var msg webrtc.SessionDescription
			if err := json.Unmarshal(message, &msg); err != nil {
				Engine.Raise(xray.New(err))
				return
			}
			if err := world.peer.SetRemoteDescription(msg); err != nil {
				Engine.Raise(xray.New(err))
				return
			}
		}
	}()
	return code, err
}

func (world *Client) extend(ctx context.Context, buf []byte) error {
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

func (world *Client) listen(ctx context.Context) (<-chan []byte, int64) {
	ch := make(chan []byte, 1)
	context.AfterFunc(ctx, func() {
		close(ch)
	})
	return ch, 1500
}

func (world *Client) opener(ctx context.Context, path string) (io.ReaderAt, int64, error) {
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

func (world *Client) notify(ctx context.Context, buf []byte) error {
	return nil
}

func (world *Client) crypto(context.Context) ([]crypto.PublicKey, crypto.Signer, error) {
	_, private, _ := ed25519.GenerateKey(nil)
	return nil, private, nil
}

// Ready does a bunch of dependency injection and setup.
func (world *Client) Ready() {
	defer clientReady.Done()
	world.signalling = api.Import[signalling.API](rest.API, SignallingHost, rest.Header("Authorization", "Bearer "+OneTimeUseCode))
	world.mouseOver = make(chan Vector3.XYZ, 100)
	world.PreviewRenderer.preview = make(chan Path.ToResource, 1)
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
	}
	world.FocalPoint.Lens.Camera.AsNode3D().SetPosition(Vector3.New(0, 1, 3))
	world.FocalPoint.Lens.Camera.AsNode3D().LookAt(Vector3.Zero)
	world.Light.AsNode3D().SetRotation(Euler.Radians{X: -Angle.Pi / 2})
	world.VultureRenderer.SetFocalPoint3D(Vector3.Zero)
	world.TerrainTile.brushEvents = world.VultureRenderer.brushEvents
	RenderingServer.SetDebugGenerateWireframes(true)
}

const speed = 8

func (world *Client) Process(dt Float.X) {
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
