package internal

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"graphics.gd/classdb/Animation"
	"graphics.gd/classdb/AnimationPlayer"
	"graphics.gd/classdb/Camera3D"
	"graphics.gd/classdb/DirectionalLight3D"
	"graphics.gd/classdb/Engine"
	"graphics.gd/classdb/FileAccess"
	"graphics.gd/classdb/Input"
	"graphics.gd/classdb/InputEvent"
	"graphics.gd/classdb/InputEventKey"
	"graphics.gd/classdb/InputEventMouseButton"
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/OS"
	"graphics.gd/classdb/PackedScene"
	"graphics.gd/classdb/PhysicsRayQueryParameters3D"
	"graphics.gd/classdb/PropertyTweener"
	"graphics.gd/classdb/RenderingServer"
	"graphics.gd/classdb/Resource"
	"graphics.gd/classdb/Texture2D"
	"graphics.gd/classdb/Viewport"
	"graphics.gd/variant/Angle"
	"graphics.gd/variant/Callable"
	"graphics.gd/variant/Euler"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Path"
	"graphics.gd/variant/Vector3"
	"runtime.link/api"
	"runtime.link/api/rest"
	"the.quetzal.community/aviary/internal/ice/signalling"
	"the.quetzal.community/aviary/internal/musical"
	"the.quetzal.community/aviary/internal/networking"
)

const (
	SignallingHost = "https://via.quetzal.community"
	OneTimeUseCode = "4d128c18-23e9-4b98-bf70-2cb94295406f"
)

// Client represents a creative space accessible via Aviary.
type Client struct {
	Node3D.Extension[Client] `gd:"AviaryWorld"`

	scroll_lock bool

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
	TerrainRenderer *TerrainRenderer

	signalling signalling.API

	network networking.Connectivity
	updates chan []byte // channel for updates from the server
	println chan string

	saving atomic.Bool

	user_id string

	id         musical.Author
	record     musical.Unique
	space      musical.UsersSpace3D
	entities   uint16
	design_ids uint16

	entity_to_object map[musical.Entity]Node3D.ID
	object_to_entity map[Node3D.ID]musical.Entity

	packed_scenes map[musical.Design]PackedScene.ID
	textures      map[musical.Design]Texture2D.ID
	loaded        map[string]musical.Design

	clients chan musical.Networking

	clientReady sync.WaitGroup

	load_last_save bool
	joining        bool

	selection Node3D.ID

	time TimingCoordinator

	last_LookAt      musical.LookAt
	last_lookAt_time time.Time

	authors map[musical.Author]Node3D.ID
}

func NewClient() *Client {
	var client = &Client{
		clientReady:      sync.WaitGroup{},
		entity_to_object: make(map[musical.Entity]Node3D.ID),
		object_to_entity: make(map[Node3D.ID]musical.Entity),
		packed_scenes:    make(map[musical.Design]PackedScene.ID),
		textures:         make(map[musical.Design]Texture2D.ID),
		loaded:           make(map[string]musical.Design),
		clients:          make(chan musical.Networking),
		authors:          make(map[musical.Author]Node3D.ID),
		load_last_save:   true,
	}
	client.clientReady.Add(1)
	return client
}

var UserState struct {
	Record musical.Unique
}

func NewClientJoining() *Client {
	var client = NewClient()
	client.load_last_save = false
	client.joining = true
	return client
}

func NewClientLoading(record musical.Unique) *Client {
	var client = NewClient()
	client.record = record
	UserState.Record = record
	client.load_last_save = false
	userfile := FileAccess.Open(OS.GetUserDataDir()+"/user.json", FileAccess.Write)
	buf, err := json.Marshal(UserState)
	if err != nil {
		Engine.Raise(fmt.Errorf("failed to marshal user state: %w", err))
		return client
	}
	userfile.StoreBuffer(buf)
	userfile.Close()
	return client
}

func (world *Client) isOnline() bool {
	return world.user_id != ""
}

func (world *Client) apiJoin(code networking.Code) {
	world.clientReady.Wait()
	err := world.network.Join(code, world.updates)
	if err != nil {
		Engine.Raise(fmt.Errorf("failed to join room %s: %w", code, err))
		return
	}
	space, err := musical.Join(musical.Networking{
		Instructions: networkingVia{&world.network, world.updates},
		MediaUploads: stubbedNetwork{},
		ErrorReports: musicalImpl{world},
	}, musical.Unique{}, musicalImpl{world})
	if err != nil {
		Engine.Raise(fmt.Errorf("failed to join musical room %s: %w", code, err))
		return
	}
	Callable.Defer(Callable.New(func() {
		world.space = space
	}))
}

func (world *Client) apiHost() (networking.Code, error) {
	code, err := world.network.Host(world.updates, func(client networking.Client) {
		world.clients <- musical.Networking{
			Instructions: networkingFor{client},
			MediaUploads: stubbedNetwork{},
			ErrorReports: musicalImpl{world},
		}
	})
	if err != nil {
		return "", fmt.Errorf("failed to host room: %w", err)
	}
	return code, nil
}

// Ready does a bunch of dependency injection and setup.
func (world *Client) Ready() {
	defer world.clientReady.Done()

	if !world.joining && world.load_last_save {
		userfile := FileAccess.Open(OS.GetUserDataDir()+"/user.json", FileAccess.Read)
		if userfile != FileAccess.Nil {
			buf := userfile.GetBuffer(FileAccess.GetSize(OS.GetUserDataDir() + "/user.json"))
			if err := json.Unmarshal(buf, &UserState); err != nil {
				Engine.Raise(fmt.Errorf("failed to unmarshal user state: %w", err))
				return
			}
			world.record = UserState.Record
		}
	}

	if !world.joining {
		clients_iter := func(yield func(musical.Networking) bool) {
			for client := range world.clients {
				if !yield(client) {
					return
				}
			}
		}
		var err error
		world.space, _, err = musical.Host(clients_iter, world.record, musicalImpl{world}, musicalImpl{world}, musicalImpl{world}) // FIXME race?
		if err != nil {
			Engine.Raise(err)
		}
	}

	world.println = make(chan string, 10)
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
	world.TerrainRenderer.texture = make(chan Path.ToResource, 1)
	world.TerrainRenderer.client = world
	world.TerrainRenderer.tile.client = world
	world.PreviewRenderer.mouseOver = world.mouseOver
	world.PreviewRenderer.terrain = world.TerrainRenderer
	world.TerrainRenderer.mouseOver = world.mouseOver
	editor_scene := Resource.Load[PackedScene.Instance]("res://ui/editor.tscn")
	first := editor_scene.Instantiate()
	editor, ok := Object.As[*UI](first)
	if ok {
		editor.preview = world.PreviewRenderer.preview
		editor.texture = world.TerrainRenderer.texture
		editor.client = world
		world.AsNode().AddChild(editor.AsNode())
		editor.Setup()
	}
	world.FocalPoint.Lens.Camera.AsNode3D().SetPosition(Vector3.New(0, 1, 3))
	world.FocalPoint.Lens.Camera.AsNode3D().LookAt(Vector3.Zero)
	world.Light.AsNode3D().SetRotation(Euler.Radians{X: -Angle.Pi / 2})
	RenderingServer.SetDebugGenerateWireframes(true)
}

const speed = 8

type musicalImpl struct {
	*Client
}

func (world musicalImpl) ReportError(err error) {
	Engine.Raise(err)
}

func (world musicalImpl) Open(space musical.Unique) (fs.File, error) {
	name := base64.RawURLEncoding.EncodeToString(space[:])
	return os.OpenFile(OS.GetUserDataDir()+"/"+name+".mus3", os.O_RDWR|os.O_CREATE, 0666)
}

func (world musicalImpl) Member(req musical.Member) error {
	if req.Assign {
		Callable.Defer(Callable.New(func() {
			world.id = req.Author
			world.record = req.Record
		}))
	}
	return nil
}

func (world musicalImpl) Upload(file musical.Upload) error { return nil }
func (world musicalImpl) Sculpt(brush musical.Sculpt) error {
	Callable.Defer(Callable.New(func() {
		world.TerrainRenderer.Sculpt(brush)
	}))
	return nil
}
func (world musicalImpl) Import(uri musical.Import) error {
	Callable.Defer(Callable.New(func() {
		if _, ok := world.loaded[uri.Import]; ok {
			return
		}
		if uri.Design.Author == world.id {
			world.design_ids = max(world.design_ids, uri.Design.Number)
		}
		res := Object.Leak(Resource.Load[Resource.Instance](uri.Import))
		switch {
		case Object.Is[PackedScene.Instance](res):
			world.packed_scenes[uri.Design] = Object.To[PackedScene.Instance](res).ID()
		case Object.Is[Texture2D.Instance](res):
			world.textures[uri.Design] = Object.To[Texture2D.Instance](res).ID()
		}
		world.loaded[uri.Import] = uri.Design
	}))
	return nil
}
func (world musicalImpl) Change(con musical.Change) error {
	Callable.Defer(Callable.New(func() {
		if con.Entity.Author == world.id {
			world.entities = max(world.entities, con.Entity.Number)
		}
		container := world.TerrainRenderer.AsNode()

		exists, ok := world.entity_to_object[con.Entity].Instance()
		if ok {
			if con.Remove {
				exists.AsNode().QueueFree()
				return
			}

			exists.SetPosition(con.Offset)
			exists.SetRotation(con.Angles)
			exists.SetScale(Vector3.New(0.1, 0.1, 0.1))
			return
		}

		scene, ok := world.packed_scenes[con.Design].Instance()
		if !ok {
			return
		}
		node := Object.To[Node3D.Instance](scene.Instantiate())
		if node.AsNode().HasNode("AnimationPlayer") {
			anim := Object.To[AnimationPlayer.Instance](node.AsNode().GetNode("AnimationPlayer"))
			anim.AsAnimationMixer().GetAnimation("Idle").SetLoopMode(Animation.LoopLinear)
			if anim.AsAnimationMixer().HasAnimation("Idle") {
				anim.PlayNamed("Idle")
			}
		}
		node.SetPosition(con.Offset)
		node.SetRotation(con.Angles)
		node.SetScale(Vector3.Mul(node.Scale(), Vector3.New(0.1, 0.1, 0.1)))
		world.entity_to_object[con.Entity] = node.ID()
		world.object_to_entity[node.ID()] = con.Entity
		container.AddChild(node.AsNode())
	}))
	return nil
}

func (world musicalImpl) Action(action musical.Action) error {
	Callable.Defer(Callable.New(func() {
		object, ok := world.entity_to_object[action.Entity].Instance()
		if ok {
			if !object.AsNode().HasNode("ActionRenderer") {
				actions := new(ActionRenderer)
				actions.client = world.Client
				actions.Initial = object.AsNode3D().Position()
				actions.AsNode().SetName("ActionRenderer")
				object.AsNode().AddChild(actions.AsNode())
			}
			actions := Object.To[*ActionRenderer](object.AsNode().GetNode("ActionRenderer"))
			actions.Add(action)
		}
	}))
	return nil
}

func (world musicalImpl) LookAt(view musical.LookAt) error {
	Callable.Defer(Callable.New(func() {
		if world.joining && view.Author == 0 {
			world.time.Follow(view.Timing)
		}
		if view.Author == world.id {
			return
		}
		if avatar, ok := world.authors[view.Author].Instance(); ok {
			tween := avatar.AsNode().CreateTween()
			PropertyTweener.Make(tween, avatar.AsObject(), "position", view.Offset, 0.1)
			PropertyTweener.Make(tween, avatar.AsObject(), "rotation", view.Angles, 0.1)
			return
		}
		avatar := Resource.Load[PackedScene.Is[Node3D.Instance]]("res://library/everything/avatar/bald_eagle.glb").Instantiate()
		avatar.SetPosition(view.Offset)
		avatar.SetRotation(view.Angles)
		avatar.SetScale(Vector3.New(0.1, 0.1, 0.1))
		if avatar.AsNode().HasNode("AnimationPlayer") {
			anim := Object.To[AnimationPlayer.Instance](avatar.AsNode().GetNode("AnimationPlayer"))
			if anim.AsAnimationMixer().HasAnimation("Flap") {
				anim.AsAnimationMixer().GetAnimation("Flap").SetLoopMode(Animation.LoopLinear)
				anim.PlayNamed("Flap")
			}
		}
		world.AsNode().AddChild(avatar.AsNode())
		world.authors[view.Author] = avatar.ID()
	}))
	return nil
}

func (world *Client) Process(dt Float.X) {
	world.time.Process(dt)

	if time.Since(world.last_lookAt_time) > time.Second/10 && world.space != nil {
		angles := Viewport.Get(world.AsNode()).GetCamera3d().AsNode3D().GlobalRotation()
		angles.X = -angles.X
		angles.Y += Angle.Pi
		view := musical.LookAt{
			Offset: Viewport.Get(world.AsNode()).GetCamera3d().AsNode3D().GlobalPosition(),
			Angles: angles,
			Author: world.id,
			Timing: world.time.Now(),
		}
		if world.last_LookAt.Offset != view.Offset || world.last_LookAt.Angles != view.Angles || !world.joining {
			world.space.LookAt(view)
			world.last_LookAt = view
			world.last_lookAt_time = time.Now()
		}
	}

	Object.Use(world)
	select {
	case msg := <-world.println:
		os.Stderr.WriteString(msg)
	default:
	}
	if Input.IsKeyPressed(Input.KeyCtrl) {
		return
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
}

func (world *Client) UnhandledInput(event InputEvent.Instance) {
	// Tilt the camera up and down with R and F.
	if !world.scroll_lock {
		if mouse, ok := Object.As[InputEventMouseButton.Instance](event); ok {
			if !Input.IsKeyPressed(Input.KeyShift) {
				if mouse.ButtonIndex() == Input.MouseButtonWheelUp {
					world.FocalPoint.Lens.Camera.AsNode3D().Translate(Vector3.New(0, 0, -0.4))
				}
				if mouse.ButtonIndex() == Input.MouseButtonWheelDown {
					world.FocalPoint.Lens.Camera.AsNode3D().Translate(Vector3.New(0, 0, 0.4))
				}
			}
			switch {
			case mouse.ButtonIndex() == Input.MouseButtonLeft && mouse.AsInputEvent().IsPressed(): // Select
				if world.TerrainRenderer.PaintActive {
					world.TerrainRenderer.Paint(mouse.AsInputEventMouse())
					break
				}
				if world.PreviewRenderer.Enabled() {
					world.PreviewRenderer.Place()
					break
				}
				cam := Viewport.Get(world.AsNode()).GetCamera3d()
				space_state := world.AsNode3D().GetWorld3d().DirectSpaceState()
				mpos_2d := Viewport.Get(world.AsNode()).GetMousePosition()
				ray_from, ray_to := cam.ProjectRayOrigin(mpos_2d), cam.ProjectPosition(mpos_2d, 1000)
				var query = PhysicsRayQueryParameters3D.Create(ray_from, ray_to, nil)
				var intersect = space_state.IntersectRay(query)

				if world.selection != 0 {
					node, ok := world.selection.Instance()
					if ok {
						Select(node.AsNode(), false)
					}
				}

				if Object.Is[*TerrainTile](intersect.Collider) {
					world.selection = 0
				} else {
					node, ok := Object.As[Node.Instance](intersect.Collider)
					if ok {
						node = node.Owner()
						world.selection = Node3D.ID(node.ID())
						Select(node, true)
					}
				}
			case mouse.ButtonIndex() == Input.MouseButtonRight && mouse.AsInputEvent().IsPressed(): // Action
				if world.TerrainRenderer.PaintActive {
					world.TerrainRenderer.shader.SetShaderParameter("paint_active", false)
					world.TerrainRenderer.PaintActive = false
					break
				}
				if world.PreviewRenderer.Enabled() {
					world.PreviewRenderer.Discard()
					break
				}
				if world.selection != 0 {
					cam := Viewport.Get(world.AsNode()).GetCamera3d()
					space_state := world.AsNode3D().GetWorld3d().DirectSpaceState()
					mpos_2d := Viewport.Get(world.AsNode()).GetMousePosition()
					ray_from, ray_to := cam.ProjectRayOrigin(mpos_2d), cam.ProjectPosition(mpos_2d, 1000)
					var query = PhysicsRayQueryParameters3D.Create(ray_from, ray_to, nil)
					var intersect = space_state.IntersectRay(query)
					if Object.Is[*TerrainTile](intersect.Collider) {
						node, ok := world.selection.Instance()
						if ok {
							if node3d, ok := Object.As[Node3D.Instance](node); ok {
								if entity, ok := world.object_to_entity[node3d.ID()]; ok {
									world.space.Action(musical.Action{
										Author: world.id,
										Entity: entity,
										Target: intersect.Position,
										Period: musical.Period(Vector3.Distance(node3d.Position(), intersect.Position) * Float.X(time.Second) * 5),
										Timing: world.time.Future(), // FIXME
										Cancel: true,
										Commit: true,
									})
								}
							}
						}
					}
				}
			}
		}
	}
	if event, ok := Object.As[InputEventKey.Instance](event); ok {
		if event.AsInputEvent().IsPressed() && event.Keycode() == Input.KeyF1 {
			vp := Viewport.Get(world.AsNode())
			vp.SetDebugDraw(vp.DebugDraw() ^ Viewport.DebugDrawWireframe)
		}
		if event.AsInputEvent().IsPressed() && event.Keycode() == Input.KeyS && Input.IsKeyPressed(Input.KeyCtrl) && !event.AsInputEvent().IsEcho() {
			AnimateTheSceneBeingSaved(world, world.record)
		}
		if event.AsInputEvent().IsPressed() && (event.Keycode() == Input.KeyDelete || event.Keycode() == Input.KeyBackspace) && !event.AsInputEvent().IsEcho() {
			node, ok := world.selection.Instance()
			if ok {
				if entity, ok := world.object_to_entity[Node3D.ID(node.ID())]; ok {
					world.space.Change(musical.Change{
						Author: world.id,
						Entity: entity,
						Remove: true,
						Commit: true,
					})
				}
			}
		}
	}
}

type networkingFor struct {
	networking.Client
}

func (nf networkingFor) Send(data []byte) error {
	nf.Client.Send <- data
	return nil
}

func (nf networkingFor) Recv() ([]byte, error) {
	data, ok := <-nf.Client.Recv
	if !ok {
		return nil, fmt.Errorf("connection closed")
	}
	return data, nil
}

func (nf networkingFor) Close() error {
	close(nf.Client.Send)
	return nil
}

type stubbedNetwork struct{}

func (sn stubbedNetwork) Send(data []byte) error { return nil }
func (sn stubbedNetwork) Recv() ([]byte, error)  { select {} }
func (sn stubbedNetwork) Close() error           { return nil }

type fileWrapped struct {
	path string
	file FileAccess.Instance
}

func (fw fileWrapped) Stat() (fs.FileInfo, error) {
	return fw, nil
}

func (fw fileWrapped) Name() string {
	return filepath.Base(fw.path)
}

func (fw fileWrapped) Size() int64 {
	return int64(FileAccess.GetSize(fw.path))
}

func (fw fileWrapped) Mode() fs.FileMode {
	return 0666
}

func (fw fileWrapped) IsDir() bool {
	return false
}

func (fw fileWrapped) Sys() any {
	return fw.file
}

func (fw fileWrapped) ModTime() (t time.Time) {
	return time.Unix(int64(FileAccess.GetModifiedTime(fw.path)), 0)
}

func (fw fileWrapped) Read(p []byte) (n int, err error) {
	if fw.file.EofReached() {
		return 0, io.EOF
	}
	n = copy(p, fw.file.GetBuffer(len(p)))
	if n < len(p) {
		return n, io.EOF
	}
	return n, fw.file.GetError()
}

func (fw fileWrapped) Write(p []byte) (n int, err error) {
	fw.file.SeekEnd()
	if ok := fw.file.StoreBuffer(p); !ok {
		return 0, fw.file.GetError()
	}
	return len(p), nil
}

func (fw fileWrapped) Close() error {
	fw.file.Close()
	Object.Free(fw.file)
	return nil
}

type networkingVia struct {
	network *networking.Connectivity
	updates chan []byte
}

func (nv networkingVia) Send(data []byte) error {
	nv.network.Send(data)
	return nil
}

func (nv networkingVia) Recv() ([]byte, error) {
	data, ok := <-nv.updates
	if !ok {
		return nil, fmt.Errorf("connection closed")
	}
	return data, nil
}

func (nv networkingVia) Close() error {
	return nil
}

type TimingCoordinator struct {
	target musical.Timing // leader time - latency
	smooth musical.Timing // current smoothed time
	future musical.Timing // leader time + latency

	leader []musical.Timing // ring of samples, from the host (author = 0).
	locals []time.Time      // local times when each sample was recorded.
	offset int              // current index in the ring
}

func (tc *TimingCoordinator) Now() musical.Timing { return tc.smooth }
func (tc *TimingCoordinator) Future() musical.Timing {
	if tc.future == 0 {
		return tc.smooth
	}
	return tc.target
}

func (tc *TimingCoordinator) Process(delta Float.X) {
	if tc.target > 0 {
		if tc.smooth == 0 {
			tc.smooth = musical.Timing(time.Now().UnixNano())
		} else {
			tc.smooth += musical.Timing(Float.X(delta) * Float.X(time.Second))
		}
		// use frame delta to exponentially move smooth timing towards target timing.
		// this helps to avoid sudden jumps in timing.
		diff := Float.X(tc.target - tc.smooth)
		tc.smooth += musical.Timing(diff * Float.X(delta) * 4)
	} else {
		tc.smooth = musical.Timing(time.Now().UnixNano())
	}
}

func (tc *TimingCoordinator) Follow(t musical.Timing) {
	if tc.leader == nil {
		tc.leader = make([]musical.Timing, 10)
		tc.locals = make([]time.Time, 10)
	}
	tc.leader[tc.offset] = t
	tc.locals[tc.offset] = time.Now()
	tc.offset = (tc.offset + 1) % len(tc.leader)

	// calculate average latency and adjust our own timing
	// so that we are slightly behind the leader to account
	// for network delay.

	var totalDelay time.Duration
	var count int
	for i := 0; i < len(tc.leader); i++ {
		if tc.leader[i] != 0 {
			leaderTime := time.Unix(0, int64(tc.leader[i]))
			localTime := tc.locals[i]
			delay := localTime.Sub(leaderTime)
			totalDelay += delay
			count++
		}
	}
	if count > 0 {
		avgDelay := totalDelay / time.Duration(count)
		tc.target = musical.Timing(time.Now().Add(-avgDelay).UnixNano())
		tc.future = musical.Timing(time.Now().Add(avgDelay).UnixNano())
	}
}
