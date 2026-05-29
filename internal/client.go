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
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"graphics.gd/classdb/Camera3D"
	"graphics.gd/classdb/CylinderMesh"
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
	"graphics.gd/classdb/QuadMesh"
	"graphics.gd/classdb/RayCast3D"
	"graphics.gd/classdb/RenderingServer"
	"graphics.gd/classdb/SubViewport"
	"graphics.gd/classdb/Texture2D"
	"graphics.gd/classdb/Viewport"
	"graphics.gd/classdb/WorldEnvironment"
	"graphics.gd/classdb/XRCamera3D"
	"graphics.gd/classdb/XRController3D"
	"graphics.gd/classdb/XROrigin3D"
	"graphics.gd/variant/Angle"
	"graphics.gd/variant/Callable"
	"graphics.gd/variant/Color"
	"graphics.gd/variant/Euler"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Path"
	"graphics.gd/variant/Transform3D"
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

	// controlLockMovement disables the world's WASD/QE/RF camera
	// translation block so an editor can take ownership of those
	// keys. The critter editor's "control" view sets this so W/S/A/D
	// drive the critter instead of the camera; the editor restores
	// the flag when leaving the view.
	controlLockMovement bool

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
	CitizenEditor *CitizenEditor
	CritterEditor *CritterEditor
	CoasterEditor *CoasterEditor

	signalling signalling.API

	network networking.Connectivity
	updates chan []byte // channel for updates from the server
	println chan string

	id     musical.Author
	record musical.WorkID
	space  musical.UsersSpace3D

	SharedResources

	clients chan musical.Networking

	clientReady sync.WaitGroup

	load_last_save bool
	joining        bool

	selection Node3D.ID

	// gizmoDrag tracks an in-progress object manipulation started while
	// the 2D gizmo toolbar (or Shift hotkey) has GizmoShift selected.
	// We use a horizontal drag plane (constant Y) for the initial
	// implementation so objects slide at their original height.
	gizmoDrag struct {
		active     bool
		entity     musical.Entity
		startPos   Vector3.XYZ
		dragPlaneY Float.X
		startGrab  Vector3.XYZ // world point on the plane under the mouse when drag began

		// For Vehicle mirrored parts: the symmetry plane (point + normal)
		// captured at the start of the gizmo drag. This lets us keep the
		// mirror as a true reflection when the user moves the main part,
		// rather than rigidly translating the old offset.
		hasMirrorPlane    bool
		mirrorPlanePoint  Vector3.XYZ
		mirrorPlaneNormal Vector3.XYZ

		// Captured at drag start for vehicles so we can re-instantiate
		// the mirror copy (using the correct Design) if the user moves
		// back away from the axis after the mirror was temporarily removed.
		design musical.Design

		// --- Twist / local-Y rotation state (for GizmoTwist) ---
		activeGizmo       Gizmo         // the gizmo mode active when this drag started
		twistInitialY     Angle.Radians // original .Y component of the object's Euler rotation
		twistPlaneY       Float.X       // Y level of the virtual horizontal plane used for angle calculation
		twistInitialAngle Float.X       // atan2 angle of the initial mouse projection relative to object center

		// --- Uniform scale state (for GizmoScale) ---
		// scaleInitial is the live Node3D scale captured at drag
		// start; scaleInitialDistance is the planar distance from
		// the object center to the cursor's grab point on the
		// same Y plane. Live scale = scaleInitial * (currentDist
		// / scaleInitialDistance).
		scaleInitial         Vector3.XYZ
		scaleInitialDistance Float.X
		scalePlaneY          Float.X
	}

	time TimingCoordinator

	last_LookAt      musical.LookAt
	last_lookAt_time time.Time

	last_PaintAt time.Time

	authors map[musical.Author]Node3D.ID

	queue chan func()

	member bool // true when we have been assigned an author ID

	ui *UI

	// xr is true once setupXR has confirmed an OpenXR runtime is
	// initialized and switched the viewport into headset rendering.
	// While true, the desktop input/move paths short-circuit and the
	// 2D Control overlay is moved into a SubViewport rendered on a
	// quad attached to the left controller (wrist UI).
	xr       bool
	xrOrigin XROrigin3D.Instance
	xrCamera XRCamera3D.Instance
	xrLeft   XRController3D.Instance
	xrRight  XRController3D.Instance

	// VR UI plumbing — see xr_ui.go. We split the editor's 2D
	// overlay across two wrist panels:
	//   * left hand carries CloudControl + drawer (the "left" set)
	//   * right hand carries EditorIndicator + Toolbar + Trash
	// Each controller pointer aims at the OPPOSITE hand's panel,
	// so the dominant hand interacts with the off-hand menu.
	vrUIViewport        SubViewport.Instance
	vrUIPanel           MeshInstance3D.Instance
	vrUIViewportRight   SubViewport.Instance
	vrUIPanelRight      MeshInstance3D.Instance
	vrUIViewportPalette SubViewport.Instance
	vrUIPanelPalette    MeshInstance3D.Instance
	// vrPaletteGrabHand is the controller currently grabbing the
	// design palette (grip-button held while pointing at it). Nil
	// when not being moved. While set, processVRPointer keeps the
	// palette's transform pinned to (controllerTransform * grabXform)
	// so the palette tracks the hand 1:1 in space.
	vrPaletteGrabHand     XRController3D.Instance
	vrPaletteGrabRelative Transform3D.BasisOrigin // controller^-1 * palette at grab start

	// Grip-as-gizmo-modifier state. Right grip is the Shift
	// equivalent (GizmoShift), left grip is Ctrl (GizmoTwist),
	// both grips together are Shift+Ctrl (GizmoScale). When the
	// first grip comes in we snapshot the user's previous gizmo so
	// we can restore it when both grips are released — matching the
	// desktop CloudControl.Input behaviour.
	vrRightGrip          bool
	vrLeftGrip           bool
	vrGripModifierActive bool
	vrGripGizmoBackup    Gizmo

	vrPointer       RayCast3D.Instance // right hand
	vrLaser         MeshInstance3D.Instance
	vrLaserCyl      CylinderMesh.Instance
	vrRightHovering bool // right pointer ray hit some UI panel last frame
	vrRightTrigger  bool // right trigger is held
	// vrUIHoverViewportRight / vrUIHoverPixelRight track which panel
	// the right ray was hovering on the last tick and where it was
	// pointing in panel-pixel space — so the trigger handler can
	// click whichever panel (left wrist, palette) it was over.
	vrUIHoverViewportRight SubViewport.Instance
	vrUIHoverPixelRight    Vector2.XY
	vrPointerLeft          RayCast3D.Instance // left hand
	vrLaserLeft            MeshInstance3D.Instance
	vrLaserCylLeft         CylinderMesh.Instance
	vrLeftHovering         bool // left pointer ray hit some UI panel last frame
	vrLeftTrigger          bool // left trigger is held
	vrUIHoverViewportLeft  SubViewport.Instance
	vrUIHoverPixelLeft     Vector2.XY
	// vrDragController points at the hand that armed an in-progress
	// gizmo drag. inputRay() uses its pose so updateGizmoDrag follows
	// the controller; release on the same hand commits the drag.
	vrDragController XRController3D.Instance
	// vrRotateArmed tracks whether the right thumbstick has crossed
	// vrRotateLatch since it last returned to centre — a single
	// snap-turn per flick.
	vrRotateArmed bool

	// undo holds the per-client undo/redo history. See undo.go.
	undo UndoStack
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
		"citizen": world.CitizenEditor,
		"critter": world.CritterEditor,
		"coaster": world.CoasterEditor,
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
	// Child Ready runs before parent Ready, so any chunk created in
	// TerrainEditor.Ready (the starter tile) was wired with a nil
	// client — propagate now that ours is known.
	for _, tile := range world.TerrainEditor.tiles {
		tile.client = world
	}
	world.VehicleEditor.client = world
	world.FoliageEditor.client = world
	world.MineralEditor.client = world
	world.SceneryEditor.client = world
	world.ShelterEditor.client = world
	world.CitizenEditor.client = world
	world.CritterEditor.client = world
	world.CoasterEditor.client = world
	editor_scene := LoadSync[PackedScene.Instance]("res://ui/editor.tscn")
	first := editor_scene.Instantiate()
	editor, ok := Object.As[*UI](first)
	if ok {
		editor.texture = world.TerrainEditor.texture
		editor.client = world
		world.ui = editor
		world.AsNode().AddChild(editor.AsNode())
		editor.Setup()
	}
	world.FocalPoint.Lens.Camera.AsNode3D().
		SetPosition(Vector3.New(0, 1, 3)).
		LookAt(Vector3.Zero)

	world.Light.AsNode3D().SetRotation(Euler.Radians{X: Angle.InRadians(-17), Y: Angle.InRadians(30), Z: Angle.InRadians(11)})
	world.Light.
		SetDirectionalShadowMode(DirectionalLight3D.ShadowOrthogonal).
		AsLight3D().
		SetLightEnergy(1).
		SetShadowEnabled(true).
		SetShadowBias(0.015).
		SetShadowNormalBias(0).
		SetShadowBlur(2.0)
	Light3D.Advanced(world.Light.AsLight3D()).SetParam(Light3D.ParamShadowMaxDistance, 30)

	env := Environment.New().
		SetBackgroundMode(Environment.BgClearColor).
		SetAmbientLightColor(Color.X11.White).
		SetAmbientLightSkyContribution(0).
		SetAmbientLightSource(Environment.AmbientSourceColor).
		SetAmbientLightEnergy(0.5)

	worldenv := WorldEnvironment.New().SetEnvironment(env)

	world.AsNode().AddChild(worldenv.AsNode())
	RenderingServer.SetDebugGenerateWireframes(true)

	world.FocalPoint.Lens.Camera.Cover.
		SetMesh(QuadMesh.New().AsPlaneMesh().SetSize(Vector2.New(2, 2)).AsMesh()).
		AsGeometryInstance3D().SetExtraCullMargin(16384).
		AsNode3D().RotateObjectLocal(Vector3.New(0, 1, 0), Angle.Pi)

	fmt.Println("Client setup complete")

	// Attempt to bring up OpenXR. No-op on desktop without an XR
	// runtime; on Quest/Horizon OS this swaps the viewport into
	// stereo headset rendering and hides the 2D editor overlay.
	world.setupXR()
}

const speed = 8

func (world *Client) Process(dt Float.X) {
	world.time.Process(dt)

	if world.member && time.Since(world.last_lookAt_time) > time.Second/10 && world.space != nil {
		camNode := Viewport.Get(world.AsNode()).GetCamera3d().AsNode3D()
		angles := camNode.GlobalRotation()
		angles.X = -angles.X
		angles.Y += Angle.Pi
		view := musical.LookAt{
			Offset: camNode.GlobalPosition(),
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

	// Drain whatever's queued at the start of this frame. Re-evaluating
	// len every iteration risks livelock under producer spam, and a
	// bare <- after a stale len read would block; snapshot the depth
	// and stop there.
	n := len(world.queue)
	for i := 0; i < n; i++ {
		(<-world.queue)()
	}

	if Input.IsKeyPressed(Input.KeyCtrl) {
		return
	}

	// controlLockMovement lets the active editor take exclusive
	// ownership of the keyboard movement keys (W/S/A/D/Q/E/R/F/+/−).
	// The critter editor's "control" view drives the critter with
	// these keys; without the gate, the world would also translate
	// the focal point under our feet each frame.
	if world.controlLockMovement {
		return
	}
	// In XR the headset drives the view; thumbstick locomotion is a
	// follow-up. Until then, keyboard-driven focal-point translation
	// would fight the headset pose. We still drive the controller-ray
	// → UI panel hover loop here so the wrist menu feels live.
	if world.xr {
		world.processVRPointer(dt)
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
		// While the gizmo move tool (Shift or toolbar) is active for one of
		// the supported editors and the user is holding left mouse, treat
		// motion as dragging the currently selected object.
		if Input.IsMouseButtonPressed(Input.MouseButtonLeft) && world.gizmoDrag.active && world.canUseGizmoManipulation() {
			world.updateGizmoDrag()
		}
	}
	if gesture, ok := Object.As[InputEventScreenDrag.Instance](event); ok {
		if gesture.Index() != 0 {
			return
		}
		// While a gizmo manipulation is live (touch started on a
		// selectable item with gizmoShift/gizmoTwist armed), don't
		// also rotate the camera — the emulated MouseMotion event
		// for the same finger drives updateGizmoDrag via the
		// mouse-button-held path above. Without this gate the camera
		// spins under the user's finger as they're trying to
		// translate/twist an object on a touchscreen.
		if world.gizmoDrag.active {
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

				hadSelection := world.selection != 0
				if hadSelection {
					node, ok := world.selection.Instance()
					if ok {
						Select(node.AsNode(), false)
					}
				}

				// Normal raycast-based selection.
				didHitSelectable := false
				if !Object.Is[*TerrainTile](intersect.Collider) {
					if node, ok := Object.As[Node.Instance](intersect.Collider); ok {
						owner := node.Owner()
						if owner != Node.Nil {
							node = owner
							world.selection = Node3D.ID(node.ID())
							Select(node, true)
							didHitSelectable = true
						}
					}
				}

				// For the simple movable editors (scenery/vehicle/shelter) + GizmoShift,
				// a left press that doesn't hit the current selection should NOT clear it.
				// This lets the user click anywhere on screen to start a move drag once
				// something is already selected.
				if !didHitSelectable {
					if world.canUseGizmoManipulation() && hadSelection {
						// Keep the previous selection for gizmo manipulation (move or twist).
						// Re-apply the highlight because we unconditionally deselected
						// at the top of the handler. This prevents the "click to rotate
						// deselects the object" issue, especially noticeable in shelter.
						if node, ok := world.selection.Instance(); ok {
							Select(node.AsNode(), true)
						}
					} else {
						world.selection = 0
						world.gizmoDrag.active = false
						world.gizmoDrag.hasMirrorPlane = false
						world.gizmoDrag.design = musical.Design{}
						world.gizmoDrag.twistInitialY = 0
						world.gizmoDrag.twistInitialAngle = 0
						world.gizmoDrag.twistPlaneY = 0
					}
				}

				// Arm gizmo drag (only for the editors we support right now).
				world.armGizmoDrag()
			case mouse.ButtonIndex() == Input.MouseButtonRight && mouse.AsInputEvent().IsPressed(): // Action
				if world.TerrainEditor.CancelPaint() {
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

			// Left button released: finalize any in-progress gizmo drag with a
			// committed musical Change so the edit is recorded in the .mus3 log.
			if mouse.ButtonIndex() == Input.MouseButtonLeft && !mouse.AsInputEvent().IsPressed() {
				if world.gizmoDrag.active {
					if world.canUseGizmoManipulation() {
						world.commitGizmoDrag()
					}
					world.gizmoDrag.active = false
					world.gizmoDrag.hasMirrorPlane = false
					world.gizmoDrag.design = musical.Design{}
					world.gizmoDrag.twistInitialY = 0
					world.gizmoDrag.twistInitialAngle = 0
					world.gizmoDrag.twistPlaneY = 0
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
		if isDeletePress(event) && world.Editing == Editing.Scenery {
			world.DeleteSelection()
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

// isKeepImporterPath returns true for resource paths Godot ships
// verbatim via the `keep` importer (no .ctex/.scn produced). Calling
// Resource.Load on these logs a noisy error even though the file is
// reachable via FileAccess. The citizen dressing pipeline uses .obj
// + .mhclo files this way to preserve raw vertex order; the critter
// editor uses "procedural://" pseudo-URIs to ship procedurally-
// built parts (eyes, …) through the same Import/Change machinery
// without an actual resource backing them.
func isKeepImporterPath(p string) bool {
	return strings.HasSuffix(p, ".obj") || strings.HasSuffix(p, ".mhclo") || strings.HasSuffix(p, ".region") || strings.HasPrefix(p, "procedural://")
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
