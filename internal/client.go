package internal

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"graphics.gd/classdb/Animation"
	"graphics.gd/classdb/AnimationPlayer"
	"graphics.gd/classdb/Camera3D"
	"graphics.gd/classdb/DirectionalLight3D"
	"graphics.gd/classdb/Engine"
	"graphics.gd/classdb/Environment"
	"graphics.gd/classdb/FileAccess"
	"graphics.gd/classdb/Input"
	"graphics.gd/classdb/InputEvent"
	"graphics.gd/classdb/InputEventKey"
	"graphics.gd/classdb/InputEventMagnifyGesture"
	"graphics.gd/classdb/InputEventMouseButton"
	"graphics.gd/classdb/InputEventMouseMotion"
	"graphics.gd/classdb/InputEventPanGesture"
	"graphics.gd/classdb/InputEventScreenDrag"
	"graphics.gd/classdb/Light3D"
	"graphics.gd/classdb/MeshInstance3D"
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/OS"
	"graphics.gd/classdb/PackedScene"
	"graphics.gd/classdb/PhysicsRayQueryParameters3D"
	"graphics.gd/classdb/PropertyTweener"
	"graphics.gd/classdb/QuadMesh"
	"graphics.gd/classdb/RenderingServer"
	"graphics.gd/classdb/Resource"
	"graphics.gd/classdb/Texture2D"
	"graphics.gd/classdb/Viewport"
	"graphics.gd/classdb/WorldEnvironment"
	"graphics.gd/variant/Angle"
	"graphics.gd/variant/Callable"
	"graphics.gd/variant/Color"
	"graphics.gd/variant/Euler"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Path"
	"graphics.gd/variant/Vector2"
	"graphics.gd/variant/Vector3"
	"runtime.link/api"
	"runtime.link/api/rest"
	"the.quetzal.community/aviary/internal/ice/signalling"
	"the.quetzal.community/aviary/internal/musical"
	"the.quetzal.community/aviary/internal/networking"
)

const (
	SignallingHost = "https://via.quetzal.community"
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

			Camera struct {
				Camera3D.Instance

				Cover MeshInstance3D.Instance // QuadMesh cover.
			}
		}
	}

	mouseOver chan Vector3.XYZ

	Editing Subject
	editors map[string]Editor

	VehicleEditor *VehicleEditor
	ShelterEditor *ShelterEditor
	SceneryEditor *SceneryEditor
	TerrainEditor *TerrainEditor
	FoliageEditor *FoliageEditor
	MineralEditor *BoulderEditor

	signalling signalling.API

	network networking.Connectivity
	updates chan []byte // channel for updates from the server
	println chan string

	saving atomic.Bool

	id     musical.Author
	record musical.WorkID
	space  musical.UsersSpace3D

	SharedResources

	clients chan musical.Networking

	clientReady sync.WaitGroup

	load_last_save bool
	joining        bool

	selection Node3D.ID

	time TimingCoordinator

	last_LookAt      musical.LookAt
	last_lookAt_time time.Time

	last_PaintAt time.Time

	authors map[musical.Author]Node3D.ID

	queue chan func()

	member bool // true when we have been assigned an author ID

	ui *UI
}

func NewClient() *Client {
	var client = &Client{
		clientReady: sync.WaitGroup{},
		SharedResources: SharedResources{
			design_ids:       make(map[musical.Author]uint16),
			entity_ids:       make(map[musical.Author]uint16),
			design_to_entity: make(map[musical.Design][]Node3D.ID),
			entity_to_object: make(map[musical.Entity]Node3D.ID),
			object_to_entity: make(map[Node3D.ID]musical.Entity),
			design_to_string: make(map[musical.Design]string),
			packed_scenes:    make(map[musical.Design]PackedScene.ID),
			textures:         make(map[musical.Design]Texture2D.ID),
			loaded:           make(map[string]musical.Design),
		},
		clients: make(chan musical.Networking),
		authors: make(map[musical.Author]Node3D.ID),

		load_last_save: true,
		queue:          make(chan func(), 1000),
	}
	client.clientReady.Add(1)
	client.loadUserState()
	var save = false
	if UserState.Secret == "" {
		UserState.Secret = uuid.NewString()
		save = true
	}
	if UserState.Device == "" {
		UserState.Device = uuid.NewString()
		save = true
	}
	if UserState.WorkID == (musical.WorkID{}) {
		var buf [16]byte
		if _, err := rand.Read(buf[:]); err != nil {
			Engine.Raise(fmt.Errorf("failed to generate work ID: %w", err))
		} else {
			save = true
			UserState.WorkID = musical.WorkID(buf)
		}
	}
	if save {
		client.saveUserState()
	}
	client.network.Authentication = UserState.Secret
	return client
}

var UserState struct {
	Aviary signalling.User
	Editor Subject
	Device string // public device name
	Secret string // secret to be linked with a Quetzal Community Account.
	WorkID musical.WorkID
}

func NewClientJoining() *Client {
	var client = NewClient()
	client.load_last_save = false
	client.joining = true
	return client
}

func NewClientLoading(record musical.WorkID) *Client {
	var client = NewClient()
	client.record = record
	UserState.WorkID = record
	client.load_last_save = false
	client.saveUserState()
	return client
}

func (world *Client) saveUserState() {
	userfile := FileAccess.Open(OS.GetUserDataDir()+"/user.json", FileAccess.Write)
	buf, err := json.Marshal(UserState)
	if err != nil {
		Engine.Raise(fmt.Errorf("failed to marshal user state: %w", err))
		return
	}
	userfile.StoreBuffer(buf)
	userfile.Close()
}

func (world *Client) loadUserState() {
	userfile := FileAccess.Open(OS.GetUserDataDir()+"/user.json", FileAccess.Read)
	if userfile != FileAccess.Nil {
		buf := userfile.GetBuffer(FileAccess.GetSize(OS.GetUserDataDir() + "/user.json"))
		if err := json.Unmarshal(buf, &UserState); err != nil {
			Engine.Raise(fmt.Errorf("failed to unmarshal user state: %w", err))
		}
	}
	if UserState.Editor == (Subject{}) {
		UserState.Editor = Editing.Scenery
	}
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
	}, musical.WorkID{}, musicalImpl{world})
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
	world.editors = map[string]Editor{
		"terrain": world.TerrainEditor,
		"scenery": world.SceneryEditor,
		"shelter": world.ShelterEditor,
		"foliage": world.FoliageEditor,
		"mineral": world.MineralEditor,
		"boulder": world.MineralEditor,
		"vehicle": world.VehicleEditor,
	}
	defer world.StartEditing(UserState.Editor)
	defer world.clientReady.Done()

	if !world.joining && world.load_last_save && UserState.WorkID != (musical.WorkID{}) {
		world.record = UserState.WorkID
	}

	world.signalling = api.Import[signalling.API](rest.API, SignallingHost, rest.Header("Authorization", "Bearer "+UserState.Secret))

	if !world.joining {
		clients_iter := func(yield func(musical.Networking) bool) {
			for client := range world.clients {
				if !yield(client) {
					return
				}
			}
		}
		var err error
		world.space, _, err = musical.Host("Aviary v"+version, clients_iter, world.record, musicalImpl{world}, musicalImpl{world}, musicalImpl{world}) // FIXME race?
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
	world.mouseOver = make(chan Vector3.XYZ, 100)
	world.TerrainEditor.texture = make(chan Path.ToResource, 1)
	world.TerrainEditor.client = world
	world.VehicleEditor.client = world
	world.TerrainEditor.tile.client = world
	world.FoliageEditor.client = world
	world.MineralEditor.client = world
	world.SceneryEditor.client = world
	world.ShelterEditor.client = world
	editor_scene := Resource.Load[PackedScene.Instance]("res://ui/editor.tscn")
	first := editor_scene.Instantiate()
	editor, ok := Object.As[*UI](first)
	if ok {
		editor.texture = world.TerrainEditor.texture
		editor.client = world
		world.ui = editor
		world.AsNode().AddChild(editor.AsNode())
		editor.Setup()
	}
	world.FocalPoint.Lens.Camera.AsNode3D().SetPosition(Vector3.New(0, 1, 3))
	world.FocalPoint.Lens.Camera.AsNode3D().LookAt(Vector3.Zero)

	world.Light.AsNode3D().SetRotation(Euler.Radians{X: Angle.InRadians(-17), Y: Angle.InRadians(30), Z: Angle.InRadians(11)})
	world.Light.AsLight3D().SetLightEnergy(1)
	world.Light.AsLight3D().SetShadowEnabled(true)
	world.Light.AsLight3D().SetShadowBias(0.015)
	world.Light.AsLight3D().SetShadowNormalBias(0)
	world.Light.AsLight3D().SetShadowBlur(2.0)
	Light3D.Advanced(world.Light.AsLight3D()).SetParam(Light3D.ParamShadowMaxDistance, 30)
	world.Light.SetDirectionalShadowMode(DirectionalLight3D.ShadowOrthogonal)

	env := Environment.New()
	env.SetBackgroundMode(Environment.BgClearColor)
	env.SetAmbientLightColor(Color.X11.White)
	env.SetAmbientLightSkyContribution(0)
	env.SetAmbientLightSource(Environment.AmbientSourceColor)
	env.SetAmbientLightEnergy(0.5)

	worldenv := WorldEnvironment.New()
	worldenv.SetEnvironment(env)

	world.AsNode().AddChild(worldenv.AsNode())
	RenderingServer.SetDebugGenerateWireframes(true)

	cover := QuadMesh.New()
	cover.AsPlaneMesh().SetSize(Vector2.New(2, 2))
	world.FocalPoint.Lens.Camera.Cover.AsNode3D().RotateObjectLocal(Vector3.New(0, 1, 0), Angle.Pi)
	world.FocalPoint.Lens.Camera.Cover.AsGeometryInstance3D().SetExtraCullMargin(16384)
	world.FocalPoint.Lens.Camera.Cover.SetMesh(cover.AsMesh())

	fmt.Println("Client setup complete")
}

const speed = 8

type musicalImpl struct {
	*Client
}

func (world musicalImpl) ReportError(err error) {
	Engine.Raise(fmt.Errorf("%s", err))
}

func (world musicalImpl) Open(space musical.WorkID) (fs.File, error) {
	name := base64.RawURLEncoding.EncodeToString(space[:])
	if UserState.Aviary.TogetherUntil.After(time.Now()) {
		fmt.Println("opening cloud save for", name)
		return OpenCloud(world.signalling, space)
	}
	if err := os.MkdirAll(OS.GetUserDataDir()+"/saves/"+name, 0777); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(OS.GetUserDataDir()+"/saves/"+name+"/"+UserState.Device+".mus3", os.O_RDWR|os.O_CREATE, 0666)
	if err != nil {
		return nil, err
	}
	return file, nil
}

func parseVersion(version string) (major, minor, patch int) {
	_, version, _ = strings.Cut(version, " ")
	version = strings.TrimPrefix(version, ".")
	splits := strings.Split(version, ".")
	if len(splits) != 3 {
		return 0, 0, 0
	}
	fmt.Sscan(splits[0], &major)
	fmt.Sscan(splits[0], &minor)
	fmt.Sscan(splits[0], &patch)
	return
}

func (world musicalImpl) Member(req musical.Member) error {
	if req.Assign {
		if req.Server != "" {
			our_major, our_minor, our_patch := parseVersion(version)
			srv_major, srv_minor, srv_patch := parseVersion(req.Server)
			if srv_major > our_major || srv_major > srv_minor || srv_patch > our_patch {
				if !(our_major == 0 && our_minor == 0 && our_patch == 0) && !(srv_major == 0 && srv_minor == 0 && srv_patch == 0) {
					OS.ShellOpen("https://the.quetzal.community/aviary/mismatch")
				}
			}
		}
		Callable.Defer(Callable.New(func() {
			world.id = req.Author
			world.record = req.Record
			world.member = true
		}))
	}
	return nil
}

func (world musicalImpl) Upload(file musical.Upload) error { return nil }
func (world musicalImpl) Sculpt(brush musical.Sculpt) error {
	world.queue <- func() {
		editor, ok := world.editors[brush.Editor]
		if !ok {
			editor = world.TerrainEditor
		}
		editor.Sculpt(brush)
		world.ui.Editor.Sculpt(brush)
	}
	return nil
}
func (world musicalImpl) Import(uri musical.Import) error {
	world.queue <- func() {
		if _, ok := world.loaded[uri.Import]; ok {
			return
		}
		world.design_ids[uri.Design.Author] = max(world.design_ids[uri.Design.Author], uri.Design.Number)
		res := Object.Leak(Resource.Load[Resource.Instance](uri.Import))
		switch {
		case Object.Is[PackedScene.Instance](res):
			world.packed_scenes[uri.Design] = Object.To[PackedScene.Instance](res).ID()
		case Object.Is[Texture2D.Instance](res):
			world.textures[uri.Design] = Object.To[Texture2D.Instance](res).ID()
		}
		world.loaded[uri.Import] = uri.Design
		world.design_to_string[uri.Design] = uri.Import

		redesigns := world.design_to_entity[uri.Design]
		for i, id := range redesigns {
			node, ok := id.Instance()
			if !ok {
				continue
			}
			if scene, ok := world.packed_scenes[uri.Design].Instance(); ok {
				new_node := Object.To[Node3D.Instance](scene.Instantiate())
				new_node.SetPosition(node.AsNode3D().Position())
				new_node.SetRotation(node.AsNode3D().Rotation())
				new_node.SetScale(node.AsNode3D().Scale())
				if new_node.AsNode().HasNode("AnimationPlayer") {
					anim := Object.To[AnimationPlayer.Instance](new_node.AsNode().GetNode("AnimationPlayer"))
					anim.AsAnimationMixer().GetAnimation("Idle").SetLoopMode(Animation.LoopLinear)
					if anim.AsAnimationMixer().HasAnimation("Idle") {
						anim.PlayNamed("Idle")
					}
				}
				node.AsNode().ReplaceBy(new_node.AsNode())
				node.AsNode().QueueFree()
				redesigns[i] = new_node.ID()
				world.entity_to_object[world.object_to_entity[id]] = new_node.ID()
				world.object_to_entity[new_node.ID()] = world.object_to_entity[id]
				delete(world.object_to_entity, id)
			}
		}
		world.design_to_entity[uri.Design] = redesigns
	}
	return nil
}
func (world musicalImpl) Change(con musical.Change) error {
	world.queue <- func() {
		world.entity_ids[con.Entity.Author] = max(world.entity_ids[con.Entity.Author], con.Entity.Number)

		editor, ok := world.editors[con.Editor]
		if !ok {
			editor = world.TerrainEditor
		} else {
			editor.Change(con)
			return
		}

		container := world.TerrainEditor.AsNode()

		exists, ok := world.entity_to_object[con.Entity].Instance()
		if ok {
			if con.Remove {
				idx := slices.Index(world.design_to_entity[con.Design], exists.ID())
				if idx >= 0 {
					world.design_to_entity[con.Design] = slices.Delete(world.design_to_entity[con.Design], idx, idx)
				}
				exists.AsNode().QueueFree()
				return
			}

			exists.SetPosition(con.Offset)
			exists.SetRotation(con.Angles)
			exists.SetScale(Vector3.New(0.1, 0.1, 0.1))
			return
		}
		var node Node3D.Instance
		scene, ok := world.packed_scenes[con.Design].Instance()
		if ok {
			node = Object.To[Node3D.Instance](scene.Instantiate())
		} else {
			node = Node3D.New()
		}
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
		world.design_to_entity[con.Design] = append(world.design_to_entity[con.Design], node.ID())
		container.AddChild(node.AsNode())
	}
	return nil
}

func (world musicalImpl) Action(action musical.Action) error {
	world.queue <- func() {
		editor, ok := world.editors[action.Editor]
		if !ok {
			editor = world.TerrainEditor
		}
		editor.Action(action)

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
	}
	return nil
}

func (world musicalImpl) LookAt(view musical.LookAt) error {
	world.queue <- func() {
		editor, ok := world.editors[view.Editor]
		if !ok {
			editor = world.TerrainEditor
		}
		editor.LookAt(view)

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
	}
	return nil
}

func (world *Client) Process(dt Float.X) {
	world.time.Process(dt)

	if world.member && time.Since(world.last_lookAt_time) > time.Second/10 && world.space != nil {
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

	if world.TerrainEditor.PaintActive && Input.GetMouseButtonMask()&Input.MouseButtonMaskLeft != 0 {
		if time.Since(world.last_PaintAt) > time.Second/5 {
			world.TerrainEditor.Paint()
			world.last_PaintAt = time.Now()
		}
	}

	Object.Use(world)
	select {
	case msg := <-world.println:
		os.Stderr.WriteString(msg)
	default:
	}

	for i := 0; i < len(world.queue); i++ {
		(<-world.queue)()
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
	if mouse, ok := Object.As[InputEventMouseMotion.Instance](event); ok {
		if Input.IsMouseButtonPressed(Input.MouseButtonMiddle) {
			relative := mouse.Relative()
			world.FocalPoint.AsNode3D().Rotate(Vector3.New(0, 1, 0), -Angle.Radians(relative.X*0.005))
			world.FocalPoint.Lens.AsNode3D().Rotate(Vector3.New(1, 0, 0), -Angle.Radians(relative.Y*0.005))
		}
	}
	if gesture, ok := Object.As[InputEventScreenDrag.Instance](event); ok {
		if gesture.Index() != 0 {
			return
		}
		relative := gesture.Relative()
		world.FocalPoint.AsNode3D().Rotate(Vector3.New(0, 1, 0), -Angle.Radians(relative.X*0.005))
		world.FocalPoint.Lens.AsNode3D().Rotate(Vector3.New(1, 0, 0), -Angle.Radians(relative.Y*0.005))
	}
	if gesture, ok := Object.As[InputEventPanGesture.Instance](event); ok {
		delta := gesture.Delta()
		if delta.X < 0.5 && delta.Y < 0.5 && delta.X > -0.5 && delta.Y > -0.5 {
			return
		}
		cam_pos := world.FocalPoint.Lens.Camera.AsNode3D().Position()
		scale := Float.X(cam_pos.Z) / 3.0
		world.FocalPoint.AsNode3D().Translate(Vector3.New(delta.X*0.01*scale, 0, delta.Y*0.01*scale))
	}
	if gesture, ok := Object.As[InputEventMagnifyGesture.Instance](event); ok {
		factor := gesture.Factor()
		if factor < 1.005 && factor > 0.995 {
			return
		}
		world.FocalPoint.Lens.Camera.AsNode3D().Translate(Vector3.New(0, 0, (1-factor)*5))
	}
	// Tilt the camera up and down with R and F.
	if !world.scroll_lock {
		if mouse, ok := Object.As[InputEventMouseButton.Instance](event); ok {
			if !Input.IsKeyPressed(Input.KeyShift) {
				if mouse.ButtonIndex() == Input.MouseButtonWheelUp {
					//pos := world.FocalPoint.Lens.Camera.AsNode3D().Position()
					//pos = Vector3.Add(pos, Vector3.New(0, 0, -0.4))
					//world.FocalPoint.Lens.Camera.AsNode3D().SetPosition(pos)
					world.FocalPoint.Lens.Camera.AsNode3D().Translate(Vector3.New(0, 0, -0.4))
				}
				if mouse.ButtonIndex() == Input.MouseButtonWheelDown {
					//pos := world.FocalPoint.Lens.Camera.AsNode3D().Position()
					//pos = Vector3.Add(pos, Vector3.New(0, 0, 0.4))
					//world.FocalPoint.Lens.Camera.AsNode3D().SetPosition(pos)
					world.FocalPoint.Lens.Camera.AsNode3D().Translate(Vector3.New(0, 0, 0.4))
				}
			}
			switch {
			case mouse.ButtonIndex() == Input.MouseButtonLeft && mouse.AsInputEvent().IsPressed(): // Select
				cam := Viewport.Get(world.AsNode()).GetCamera3d()
				space_state := world.AsNode3D().GetWorld3d().DirectSpaceState()
				mpos_2d := Viewport.Get(world.AsNode()).GetMousePosition()
				ray_from, ray_to := cam.ProjectRayOrigin(mpos_2d), cam.ProjectPosition(mpos_2d, 1000)
				var query = PhysicsRayQueryParameters3D.Create(ray_from, ray_to, nil)
				query.SetCollisionMask(int(^uint32(1 << 1)))
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
				if world.TerrainEditor.PaintActive {
					world.TerrainEditor.shader.SetShaderParameter("paint_active", false)
					world.TerrainEditor.PaintActive = false
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
			go func() {
				name := base64.RawURLEncoding.EncodeToString(world.record[:])
				file, err := os.Open(OS.GetUserDataDir() + "/snaps/" + name + ".png")
				if err != nil {
					Engine.Raise(fmt.Errorf("failed to open snapshot for upload: %w", err))
					return
				}
				defer file.Close()

				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()

				if err := world.signalling.InsertSnap(ctx, signalling.WorkID(name), file); err != nil {
					Engine.Raise(fmt.Errorf("failed to upload snapshot: %w", err))
				}
			}()
		}
		if event.AsInputEvent().IsPressed() && (event.Keycode() == Input.KeyDelete || event.Keycode() == Input.KeyBackspace) && !event.AsInputEvent().IsEcho() && world.Editing == Editing.Scenery {
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
