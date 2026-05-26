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
	"time"

	"math"

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
	"graphics.gd/classdb/RayCast3D"
	"graphics.gd/classdb/RenderingServer"
	"graphics.gd/classdb/Resource"
	"graphics.gd/classdb/SubViewport"
	"graphics.gd/classdb/Texture2D"
	"graphics.gd/classdb/Viewport"
	"graphics.gd/classdb/WorldEnvironment"
	"graphics.gd/classdb/XRController3D"
	"graphics.gd/classdb/XROrigin3D"
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
	xrLeft   XRController3D.Instance
	xrRight  XRController3D.Instance

	// VR UI plumbing — see xr_ui.go.
	vrUIViewport SubViewport.Instance
	vrUIPanel    MeshInstance3D.Instance
	vrPointer    RayCast3D.Instance
	vrLastPixel  Vector2.XY

	// undo holds the per-client undo/redo history. See undo.go.
	undo UndoStack
}

// canUseGizmoManipulation reports whether the current global gizmo mode
// (toolbar or hotkey) plus the active editor context allows gizmo-based
// manipulation (translate via GizmoShift or twist/rotate-Y via GizmoTwist)
// of the current selection. Restricted to the three "static placed design"
// editors for now.
func (world *Client) canUseGizmoManipulation() bool {
	if world.ui == nil || world.ui.CloudControl == nil {
		return false
	}
	g := world.ui.CloudControl.Gizmo
	if g != GizmoShift && g != GizmoTwist {
		return false
	}
	switch world.Editing {
	case Editing.Scenery, Editing.Vehicle, Editing.Shelter:
		return true
	case Editing.Critter:
		// Critter has multiple sub-views; only allow gizmo manipulation
		// in the default ("explore") view. Ribcage/limbone own their
		// own bone-handle drag interactions and control owns the WASD
		// chase-cam — letting gizmo drags fire there would conflict.
		return world.CritterEditor.view == "" || world.CritterEditor.view == "explore"
	}
	return false
}

// canUseGizmoTransform is the old name, kept for a few call sites during transition.
func (world *Client) canUseGizmoTransform() bool {
	return world.canUseGizmoManipulation() && world.ui.CloudControl.Gizmo == GizmoShift
}

// selectedEntityForGizmo returns the musical.Entity (if any) that corresponds
// to the current world.selection, looking first in the global map and then
// falling back to the per-editor maps for Vehicle and Shelter (which keep
// their own tracking and short-circuit the generic registration path in
// musicalImpl.Change).
func (world *Client) selectedEntityForGizmo() (musical.Entity, Node3D.Instance, bool) {
	if world.selection == 0 {
		return musical.Entity{}, Node3D.Nil, false
	}
	raw, ok := world.selection.Instance()
	if !ok {
		return musical.Entity{}, Node3D.Nil, false
	}
	node, ok := Object.As[Node3D.Instance](raw)
	if !ok {
		return musical.Entity{}, Node3D.Nil, false
	}
	id := Node3D.ID(node.ID())

	if e, has := world.object_to_entity[id]; has {
		return e, node, true
	}

	switch world.Editing {
	case Editing.Vehicle:
		if e, has := world.VehicleEditor.object_to_entity[id]; has {
			return e, node, true
		}
	case Editing.Shelter:
		if e, has := world.ShelterEditor.object_to_entity[id]; has {
			return e, node, true
		}
		// Mirror the fallback used in ShelterEditor's delete handling.
		parent := node.GetParentNode3d()
		if parent != Node3D.Nil {
			if e, has := world.ShelterEditor.object_to_entity[Node3D.ID(parent.ID())]; has {
				return e, parent, true
			}
		}
	case Editing.Critter:
		if e, has := world.CritterEditor.partToEntity[id]; has {
			return e, node, true
		}
		// Library-imported part scenes ship with their StaticBody3D
		// at root; the picker may land on a child of the attached
		// part. Walk up one level so the selection resolves to the
		// entity-owning anchor node.
		parent := node.GetParentNode3d()
		if parent != Node3D.Nil {
			if e, has := world.CritterEditor.partToEntity[Node3D.ID(parent.ID())]; has {
				return e, parent, true
			}
		}
	}
	return musical.Entity{}, Node3D.Nil, false
}

// CanDeleteSelection reports whether DeleteSelection would do anything
// right now. Used by the trash-can button to decide visibility without
// committing to a delete.
func (world *Client) CanDeleteSelection() bool {
	if world.selection == 0 || world.space == nil {
		return false
	}
	raw, ok := world.selection.Instance()
	if !ok {
		return false
	}
	node, ok := Object.As[Node3D.Instance](raw)
	if !ok {
		return false
	}
	id := Node3D.ID(node.ID())
	switch world.Editing {
	case Editing.Scenery:
		_, ok = world.object_to_entity[id]
	case Editing.Shelter:
		_, ok = world.ShelterEditor.object_to_entity[id]
		if !ok {
			if parent := node.GetParentNode3d(); parent != Node3D.Nil {
				_, ok = world.ShelterEditor.object_to_entity[Node3D.ID(parent.ID())]
			}
		}
	case Editing.Vehicle:
		_, ok = world.VehicleEditor.object_to_entity[id]
	case Editing.Critter:
		_, ok = world.CritterEditor.partToEntity[id]
	default:
		return false
	}
	return ok
}

// DeleteSelection removes the currently selected entity by routing the
// request through the editor that owns it. Called by both the keyboard
// Delete/Backspace handler and the trash-can UI button so they share
// one canonical path. Returns true if a delete was actually issued.
func (world *Client) DeleteSelection() bool {
	if world.selection == 0 || world.space == nil {
		return false
	}
	raw, ok := world.selection.Instance()
	if !ok {
		return false
	}
	node, ok := Object.As[Node3D.Instance](raw)
	if !ok {
		return false
	}
	id := Node3D.ID(node.ID())

	var ch musical.Change
	ch.Author = world.id
	ch.Remove = true
	ch.Commit = true

	switch world.Editing {
	case Editing.Scenery:
		entity, has := world.object_to_entity[id]
		if !has {
			return false
		}
		ch.Entity = entity
	case Editing.Shelter:
		entity, has := world.ShelterEditor.object_to_entity[id]
		if !has {
			parent := node.GetParentNode3d()
			if parent == Node3D.Nil {
				return false
			}
			entity, has = world.ShelterEditor.object_to_entity[Node3D.ID(parent.ID())]
			if !has {
				return false
			}
		}
		ch.Entity = entity
		ch.Editor = "shelter"
	case Editing.Vehicle:
		entity, has := world.VehicleEditor.object_to_entity[id]
		if !has {
			return false
		}
		ch.Entity = entity
		ch.Editor = "vehicle"
	case Editing.Critter:
		entity, has := world.CritterEditor.partToEntity[id]
		if !has {
			return false
		}
		ch.Entity = entity
		ch.Editor = "critter"
	default:
		return false
	}

	// Capture the entity's pre-delete state so undo can re-create
	// it with the same design and transform. The design lookup may
	// miss for editor-internal entities that don't go through the
	// global design_to_entity map (critter parts, for example); in
	// that case we still execute the delete, but skip recording an
	// undo entry — replaying a Remove with no matching Create just
	// silently drops on the receiver side, which would surprise the
	// user more than the missing undo.
	design, canRecord := world.findDesignForObject(id)
	prePos := node.AsNode3D().Position()
	preRot := node.AsNode3D().Rotation()

	if err := world.space.Change(ch); err != nil {
		Engine.Raise(err)
		return false
	}
	if canRecord {
		world.RecordChange(ch, musical.Change{
			Author: world.id,
			Entity: ch.Entity,
			Editor: ch.Editor,
			Design: design,
			Offset: prePos,
			Angles: preRot,
		})
	}
	world.selection = 0
	world.gizmoDrag.active = false
	world.gizmoDrag.hasMirrorPlane = false
	world.gizmoDrag.design = musical.Design{}
	world.gizmoDrag.twistInitialY = 0
	world.gizmoDrag.twistInitialAngle = 0
	world.gizmoDrag.twistPlaneY = 0
	return true
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
	fmt.Sscan(splits[1], &minor)
	fmt.Sscan(splits[2], &patch)
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
		// Some imports are non-Godot-resource files shipped verbatim
		// (.obj files used by the citizen dressing pipeline use the
		// `keep` importer). Resource.Load on those logs an error to
		// the console; skip the load for those — we still want the
		// URI→Design mapping registered for later lookup.
		if !isKeepImporterPath(uri.Import) {
			res := Object.Leak(Resource.Load[Resource.Instance](uri.Import))
			switch {
			case Object.Is[PackedScene.Instance](res):
				world.packed_scenes[uri.Design] = Object.To[PackedScene.Instance](res).ID()
			case Object.Is[Texture2D.Instance](res):
				world.textures[uri.Design] = Object.To[Texture2D.Instance](res).ID()
			}
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
				new_node := Object.To[Node3D.Instance](scene.Instantiate()).
					SetPosition(node.AsNode3D().Position()).
					SetRotation(node.AsNode3D().Rotation()).
					SetScale(node.AsNode3D().Scale())
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

			exists.
				SetPosition(con.Offset).
				SetRotation(con.Angles)
			// Do not stomp scale here. The creation path applies the
			// conventional 0.1 factor; subsequent transform edits (gizmo
			// moves, etc.) must preserve whatever scale the instance has.
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
		node.
			SetPosition(con.Offset).
			SetRotation(con.Angles).
			SetScale(Vector3.Mul(node.Scale(), Vector3.New(0.1, 0.1, 0.1)))
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
		avatar := Resource.Load[PackedScene.Is[Node3D.Instance]]("res://library/everything/avatar/bald_eagle.glb").Instantiate().
			SetPosition(view.Offset).
			SetRotation(view.Angles).
			SetScale(Vector3.New(0.1, 0.1, 0.1))
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
		world.processVRPointer()
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
				// We use selectedEntityForGizmo so this also works for Vehicle
				// and Shelter, which maintain their own entity maps.
				if world.canUseGizmoManipulation() {
					if ent, node, ok := world.selectedEntityForGizmo(); ok {
						pos := node.AsNode3D().Position()

						// Record which gizmo mode started this drag so we finish the
						// interaction even if the user releases the temporary hotkey.
						world.gizmoDrag.activeGizmo = world.ui.CloudControl.Gizmo
						world.gizmoDrag.active = true
						world.gizmoDrag.entity = ent
						world.gizmoDrag.startPos = pos
						world.gizmoDrag.dragPlaneY = pos.Y
						world.gizmoDrag.hasMirrorPlane = false
						world.gizmoDrag.mirrorPlanePoint = Vector3.Zero
						world.gizmoDrag.mirrorPlaneNormal = Vector3.Zero
						world.gizmoDrag.design = musical.Design{}
						world.gizmoDrag.twistInitialY = 0
						world.gizmoDrag.twistPlaneY = 0
						world.gizmoDrag.twistInitialAngle = 0

						o, d := MouseRay(world.AsNode3D())
						if hit, ok := IntersectRayPlane(o, d, Vector3.New(pos.X, pos.Y, pos.Z), Vector3.New(0, 1, 0)); ok {
							world.gizmoDrag.startGrab = hit
						} else {
							world.gizmoDrag.startGrab = pos
						}

						// For vehicles: if this part has a mirror twin, capture the
						// symmetry plane defined by the current main+mirror positions.
						// This plane is kept fixed for the duration of the drag so
						// that subsequent moves keep the mirror as a proper reflection
						// (and remove it when the part crosses near the axis).
						if world.Editing == Editing.Vehicle {
							if mirrorRaw, has := world.VehicleEditor.entity_to_mirror[ent].Instance(); has {
								if mnode, ok := Object.As[Node3D.Instance](mirrorRaw); ok {
									mpos := mnode.AsNode3D().Position()
									delta := Vector3.Sub(mpos, pos)
									if Vector3.Length(delta) > 0.05 {
										world.gizmoDrag.mirrorPlanePoint = Vector3.Add(pos, Vector3.MulX(delta, 0.5))
										world.gizmoDrag.mirrorPlaneNormal = Vector3.Normalized(delta)
										world.gizmoDrag.hasMirrorPlane = true
									}
								}
							}

							// Capture the Design that owns this main entity so we can
							// re-create the mirror part from the correct packed scene
							// if the user drags back away from the axis after we removed it.
							for d, ids := range world.VehicleEditor.design_to_entity {
								for _, id := range ids {
									if id == node.ID() {
										world.gizmoDrag.design = d
										goto designCaptured
									}
								}
							}
						designCaptured:
						}

						// --- Twist (local Y rotation) arming ---
						if world.gizmoDrag.activeGizmo == GizmoTwist {
							rot := node.AsNode3D().Rotation()
							world.gizmoDrag.twistInitialY = rot.Y
							// Critter parts derive their rotation from
							// anchor + Twist, not from Node3D.rotation,
							// so the part's live `rot.Y` is always 0
							// even after previous twist edits. Seed from
							// the stored anchor instead so a re-twist
							// continues from where the last one left off.
							if world.Editing == Editing.Critter && world.CritterEditor != nil {
								if a, has := world.CritterEditor.body.partAnchors[Node3D.ID(node.ID())]; has {
									world.gizmoDrag.twistInitialY = Angle.Radians(a.Twist)
								}
							}
							world.gizmoDrag.twistPlaneY = pos.Y

							// Project current mouse onto the same horizontal plane we use for move.
							// The angle of this vector around the object gives us a stable reference.
							o, d := MouseRay(world.AsNode3D())
							if hit, ok := IntersectRayPlane(o, d, Vector3.New(pos.X, pos.Y, pos.Z), Vector3.New(0, 1, 0)); ok {
								dx := hit.X - pos.X
								dz := hit.Z - pos.Z
								world.gizmoDrag.twistInitialAngle = Float.X(Angle.Atan2(Angle.Radians(dz), Angle.Radians(dx)))
							} else {
								world.gizmoDrag.twistInitialAngle = 0
							}
						}
					}
				}
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

// updateGizmoDrag is called on mouse motion (or could be polled) while a
// GizmoShift drag is active. It intersects the current mouse ray against
// the horizontal drag plane captured at drag start and emits a preview
// (Commit:false) musical Change so both the local node and any peers
// immediately see the object move.
//
// For Vehicle and Shelter we set the Editor field so the Change routes to
// their specialized handlers (which do mirroring, floor grouping, etc.).
func (world *Client) updateGizmoDrag() {
	if !world.gizmoDrag.active {
		return
	}
	ent, node, ok := world.selectedEntityForGizmo()
	if !ok || node == Node3D.Nil {
		return
	}
	// Critter parts live in anchor space (T, Theta, Offset) on the
	// body surface, not world XYZ — divert into a dedicated path that
	// raycasts against the body and consults ClosestAnchor.
	if world.Editing == Editing.Critter {
		world.updateCritterGizmoDrag(ent, node, false)
		return
	}
	o, d := MouseRay(world.AsNode3D())
	planePoint := Vector3.New(world.gizmoDrag.startPos.X, world.gizmoDrag.dragPlaneY, world.gizmoDrag.startPos.Z)
	hit, ok := IntersectRayPlane(o, d, planePoint, Vector3.New(0, 1, 0))
	if !ok {
		return
	}
	delta := Vector3.Sub(hit, world.gizmoDrag.startGrab)
	newPos := Vector3.Add(world.gizmoDrag.startPos, delta)

	if world.Editing == Editing.Shelter {
		// Match the grid snapping used during shelter placement previews
		// (most objects snap to integer grid on X/Z).
		newPos.X = Float.Round(newPos.X)
		newPos.Z = Float.Round(newPos.Z)
	}

	// Preserve whatever rotation the object currently has.
	rot := node.AsNode3D().Rotation()

	ch := musical.Change{
		Author: world.id,
		Entity: ent,
		Offset: newPos,
		Angles: rot,
		Commit: false, // live preview during drag
	}
	switch world.Editing {
	case Editing.Vehicle:
		ch.Editor = "vehicle"
		ch.Design = world.gizmoDrag.design
	case Editing.Shelter:
		ch.Editor = "shelter"
	}

	// Vehicle mirror handling: if we captured a symmetry plane at drag
	// start, reflect the target main position over it. This makes the
	// mirror stay on the opposite side of the axis (instead of rigidly
	// following with a fixed offset). If the part is moved close to the
	// axis, we clear the Mirror so the handler removes the twin.
	if world.Editing == Editing.Vehicle {
		if world.gizmoDrag.hasMirrorPlane {
			target := newPos
			v := Vector3.Sub(target, world.gizmoDrag.mirrorPlanePoint)
			d := Vector3.Dot(v, world.gizmoDrag.mirrorPlaneNormal)
			reflected := Vector3.Sub(target, Vector3.MulX(world.gizmoDrag.mirrorPlaneNormal, 2*d))
			ch.Mirror = Vector3.Sub(reflected, target)

			if Float.Abs(d) < 0.25 || Vector3.Length(ch.Mirror) < 0.25 {
				ch.Mirror = Vector3.Zero
			}
		} else if mirrorRaw, has := world.VehicleEditor.entity_to_mirror[ent].Instance(); has {
			// Fallback (no plane captured): keep old relative behavior
			if mirrorNode, ok := Object.As[Node3D.Instance](mirrorRaw); ok {
				mirrorPos := mirrorNode.AsNode3D().Position()
				mainPos := node.AsNode3D().Position()
				ch.Mirror = Vector3.Sub(mirrorPos, mainPos)
			}
		}
	}

	// --- Twist (local Y rotation) handling ---
	if world.gizmoDrag.activeGizmo == GizmoTwist {
		// Recompute current hit on the rotation plane
		o, d := MouseRay(world.AsNode3D())
		if hit, ok := IntersectRayPlane(o, d, Vector3.New(world.gizmoDrag.startPos.X, world.gizmoDrag.twistPlaneY, world.gizmoDrag.startPos.Z), Vector3.New(0, 1, 0)); ok {
			dx := hit.X - world.gizmoDrag.startPos.X
			dz := hit.Z - world.gizmoDrag.startPos.Z
			curAngle := Float.X(Angle.Atan2(Angle.Radians(dz), Angle.Radians(dx)))

			// Invert delta so mouse movement feels natural (left/right matches
			// the visual rotation direction most users expect).
			delta := world.gizmoDrag.twistInitialAngle - curAngle

			newY := world.gizmoDrag.twistInitialY + Angle.Radians(delta)

			// Note: shelter snapping is now only applied on release (see commitGizmoDrag)
			// so the live drag feels responsive. Final value will snap to 90° grid.

			rot := node.AsNode3D().Rotation()
			rot.Y = newY

			twistCh := musical.Change{
				Author: world.id,
				Entity: ent,
				Offset: world.gizmoDrag.startPos, // keep original position during pure twist
				Angles: rot,
				Commit: false,
			}
			switch world.Editing {
			case Editing.Vehicle:
				twistCh.Editor = "vehicle"
				twistCh.Design = world.gizmoDrag.design

				// Preserve the existing mirror offset (if any) so remirror()
				// does not interpret the lack of Mirror field as "remove the twin".
				// This mirrors the logic we use for move drags.
				if mirrorRaw, has := world.VehicleEditor.entity_to_mirror[ent].Instance(); has {
					if mirrorNode, ok := Object.As[Node3D.Instance](mirrorRaw); ok {
						mirrorPos := mirrorNode.AsNode3D().Position()
						mainPos := node.AsNode3D().Position()
						twistCh.Mirror = Vector3.Sub(mirrorPos, mainPos)
					}
				}
			case Editing.Shelter:
				twistCh.Editor = "shelter"
			}
			_ = world.space.Change(twistCh)
		}
		return // twist handled, don't fall into the translation path
	}

	if err := world.space.Change(ch); err != nil {
		// Non-fatal during drag.
		_ = err
	}
}

// commitGizmoDrag writes one final Change with Commit:true using the
// object's *current* live transform (which has been driven by the preview
// changes). This ensures the edit is durably recorded in the musical log.
//
// We set Editor for Vehicle/Shelter so the update goes through their
// specialized Change handlers.
func (world *Client) commitGizmoDrag() {
	if !world.gizmoDrag.active {
		return
	}
	ent, node, ok := world.selectedEntityForGizmo()
	if !ok || node == Node3D.Nil {
		return
	}
	if world.Editing == Editing.Critter {
		world.updateCritterGizmoDrag(ent, node, true)
		return
	}

	pos := node.AsNode3D().Position()
	rot := node.AsNode3D().Rotation()

	if world.Editing == Editing.Shelter {
		// Match the grid snapping used during shelter placement previews.
		pos.X = Float.Round(pos.X)
		pos.Z = Float.Round(pos.Z)
	}

	ch := musical.Change{
		Author: world.id,
		Entity: ent,
		Offset: pos,
		Angles: rot,
		Commit: true,
	}
	switch world.Editing {
	case Editing.Vehicle:
		ch.Editor = "vehicle"
		ch.Design = world.gizmoDrag.design
	case Editing.Shelter:
		ch.Editor = "shelter"
	}

	// Vehicle mirror handling: if we captured a symmetry plane at drag
	// start, reflect the target main position over it. This makes the
	// mirror stay on the opposite side of the axis (instead of rigidly
	// following with a fixed offset). If the part is moved close to the
	// axis, we clear the Mirror so the handler removes the twin.
	if world.Editing == Editing.Vehicle {
		if world.gizmoDrag.hasMirrorPlane {
			target := pos
			v := Vector3.Sub(target, world.gizmoDrag.mirrorPlanePoint)
			d := Vector3.Dot(v, world.gizmoDrag.mirrorPlaneNormal)
			reflected := Vector3.Sub(target, Vector3.MulX(world.gizmoDrag.mirrorPlaneNormal, 2*d))
			ch.Mirror = Vector3.Sub(reflected, target)

			if Float.Abs(d) < 0.25 || Vector3.Length(ch.Mirror) < 0.25 {
				ch.Mirror = Vector3.Zero
			}
		} else if mirrorRaw, has := world.VehicleEditor.entity_to_mirror[ent].Instance(); has {
			// Fallback (no plane captured): keep old relative behavior
			if mirrorNode, ok := Object.As[Node3D.Instance](mirrorRaw); ok {
				mirrorPos := mirrorNode.AsNode3D().Position()
				mainPos := node.AsNode3D().Position()
				ch.Mirror = Vector3.Sub(mirrorPos, mainPos)
			}
		}
	}

	// For a pure twist drag, the live node rotation (updated by the preview
	// Changes) is already correct. Just commit it.
	if world.gizmoDrag.activeGizmo == GizmoTwist {
		rot := node.AsNode3D().Rotation()

		if world.Editing == Editing.Shelter {
			// On release, snap the final rotation to the nearest 90° increment
			// relative to the orientation at the start of this drag.
			step := math.Pi / 2
			deltaFromStart := rot.Y - world.gizmoDrag.twistInitialY
			snapped := math.Round(float64(deltaFromStart)/step) * step
			rot.Y = world.gizmoDrag.twistInitialY + Angle.Radians(snapped)
		}

		twistCh := musical.Change{
			Author: world.id,
			Entity: ent,
			Offset: pos, // position unchanged during pure twist
			Angles: rot,
			Commit: true,
		}
		switch world.Editing {
		case Editing.Vehicle:
			twistCh.Editor = "vehicle"
			twistCh.Design = world.gizmoDrag.design

			// Preserve mirror offset on final commit too.
			if mirrorRaw, has := world.VehicleEditor.entity_to_mirror[ent].Instance(); has {
				if mirrorNode, ok := Object.As[Node3D.Instance](mirrorRaw); ok {
					mirrorPos := mirrorNode.AsNode3D().Position()
					mainPos := node.AsNode3D().Position()
					twistCh.Mirror = Vector3.Sub(mirrorPos, mainPos)
				}
			}
		case Editing.Shelter:
			twistCh.Editor = "shelter"
		}
		_ = world.space.Change(twistCh)
		// Undo of a twist = same Change but with Angles.Y restored
		// to the pre-drag value. Position is unchanged during twist,
		// so we reuse `pos`. Mirror field flows through as captured.
		undo := twistCh
		undo.Angles.Y = world.gizmoDrag.twistInitialY
		world.RecordChange(twistCh, undo)
		return
	}

	if err := world.space.Change(ch); err != nil {
		Engine.Raise(err)
	}
	// Undo of a shift = move back to pre-drag position. Rotation
	// doesn't change during shift, so the live rot (which we just
	// committed) IS the pre-shift rot. Mirror field flows through
	// as captured.
	undo := ch
	undo.Offset = world.gizmoDrag.startPos
	world.RecordChange(ch, undo)
}

// updateCritterGizmoDrag handles GizmoShift / GizmoTwist for the
// critter editor. Critter parts don't live in world-space — they're
// anchored parametrically on the body surface (T along spine, Theta
// around it, Offset radial), or pinned to a leg foot. We can't reuse
// the horizontal-plane intersection the other editors use; instead a
// physics raycast picks the body surface and CritterBody.ClosestAnchor
// turns the hit point into anchor coordinates, which are then encoded
// back into musical.Change.Offset (and Bounds for leg anchors). Twist
// rides on Angles.Y the same way the other editors use rotation.Y.
//
// The `commit` flag toggles between live-preview drags (false) and
// the durable release-time write (true).
func (world *Client) updateCritterGizmoDrag(ent musical.Entity, node Node3D.Instance, commit bool) {
	if world.CritterEditor == nil || world.CritterEditor.body.critter == nil {
		return
	}
	body := &world.CritterEditor.body
	// Encode whatever the current anchor is into a Change template
	// — this preserves the OnLeg/LegFoot/LegSide bits when the user
	// is only twisting (no shift in anchor).
	cur, hasCur := body.partAnchors[Node3D.ID(node.ID())]
	if !hasCur {
		return
	}
	next := cur

	if world.gizmoDrag.activeGizmo == GizmoShift {
		// Raycast the mouse against the body collider (layer 2) and
		// translate the hit into body-local coordinates so
		// ClosestAnchor returns a sensible anchor. PartSelectionMask
		// clears layer 1 so we don't snag any already-placed parts
		// sitting on top of the surface.
		cam := Viewport.Get(world.AsNode()).GetCamera3d()
		space := world.AsNode3D().GetWorld3d().DirectSpaceState()
		mouse := Viewport.Get(world.AsNode()).GetMousePosition()
		from, to := cam.ProjectRayOrigin(mouse), cam.ProjectPosition(mouse, 1000)
		query := PhysicsRayQueryParameters3D.Create(from, to, nil)
		query.SetCollisionMask(int(PartSelectionMask))
		hit := space.IntersectRay(query)
		if hit.Collider == Object.Nil {
			return
		}
		bodyOrigin := body.mesh.AsNode3D().GlobalPosition()
		local := Vector3.Sub(hit.Position, bodyOrigin)
		fresh := body.ClosestAnchor(local)
		next.T = fresh.T
		next.Theta = fresh.Theta
		next.Offset = fresh.Offset
		next.OnLeg = fresh.OnLeg
		next.LegFoot = fresh.LegFoot
		next.LegSide = fresh.LegSide
	}

	if world.gizmoDrag.activeGizmo == GizmoTwist {
		// Same atan2-on-horizontal-plane scheme the other editors
		// use, mapped into the anchor's Twist field instead of the
		// part's world rotation. Snapshot the part's *current* world
		// position once (origin doesn't matter for the relative
		// angle math; we just need a stable pivot per frame).
		pos := node.AsNode3D().GlobalPosition()
		o, d := MouseRay(world.AsNode3D())
		if hit, ok := IntersectRayPlane(o, d, Vector3.New(pos.X, world.gizmoDrag.twistPlaneY, pos.Z), Vector3.New(0, 1, 0)); ok {
			dx := hit.X - pos.X
			dz := hit.Z - pos.Z
			cur := Float.X(Angle.Atan2(Angle.Radians(dz), Angle.Radians(dx)))
			delta := world.gizmoDrag.twistInitialAngle - cur
			next.Twist = float32(world.gizmoDrag.twistInitialY) + float32(delta)
		}
	}

	ch := musical.Change{
		Author: world.id,
		Entity: ent,
		Editor: "critter",
		Offset: Vector3.New(Float.X(next.T), Float.X(next.Theta), Float.X(next.Offset)),
		Angles: Euler.Radians{X: 0, Y: Angle.Radians(next.Twist), Z: 0},
		Commit: commit,
	}
	if next.OnLeg {
		// place() encodes leg-foot anchors as Bounds.X = LegFoot+1
		// so a zero Bounds in old records still decodes as a body
		// anchor. Mirror that here so the receive side decodes the
		// same way (see anchorFromChange in editor_critter.go).
		ch.Bounds = Vector3.New(Float.X(next.LegFoot+1), Float.X(next.LegSide), 0)
	}
	if err := world.space.Change(ch); err != nil {
		if commit {
			Engine.Raise(err)
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
