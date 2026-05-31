package internal

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"math"
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
	"graphics.gd/classdb/GPUParticles3D"
	"graphics.gd/classdb/GeometryInstance3D"
	"graphics.gd/classdb/Input"
	"graphics.gd/classdb/InputEvent"
	"graphics.gd/classdb/InputEventKey"
	"graphics.gd/classdb/InputEventMagnifyGesture"
	"graphics.gd/classdb/InputEventMouseButton"
	"graphics.gd/classdb/InputEventMouseMotion"
	"graphics.gd/classdb/InputEventPanGesture"
	"graphics.gd/classdb/InputEventScreenDrag"
	"graphics.gd/classdb/Light3D"
	"graphics.gd/classdb/Material"
	"graphics.gd/classdb/MeshInstance3D"
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/OS"
	"graphics.gd/classdb/PackedScene"
	"graphics.gd/classdb/ParticleProcessMaterial"
	"graphics.gd/classdb/PhysicsRayQueryParameters3D"
	"graphics.gd/classdb/QuadMesh"
	"graphics.gd/classdb/RayCast3D"
	"graphics.gd/classdb/RenderingServer"
	"graphics.gd/classdb/Shader"
	"graphics.gd/classdb/ShaderMaterial"
	"graphics.gd/classdb/Sky"
	"graphics.gd/classdb/SubViewport"
	"graphics.gd/classdb/Texture2D"
	"graphics.gd/classdb/Viewport"
	"graphics.gd/classdb/WorldEnvironment"
	"graphics.gd/classdb/XRCamera3D"
	"graphics.gd/classdb/XRController3D"
	"graphics.gd/classdb/XROrigin3D"
	"graphics.gd/variant/AABB"
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

	// Environment and WorldEnvironment are the live resources/nodes
	// for global scene lighting + fog. They are mutated by editor-
	// specific environment sliders (routed as special Sculpt records
	// with Slider "environment/..."). Terrain owns the shared "world"
	// values used by scenery+terrain; other editors have private ones.
	Environment      Environment.Instance
	WorldEnvironment WorldEnvironment.Instance

	// lightingMenuState holds the current friendly values for the lighting
	// rolldown (Time of Day, Sun Angle, Fog, Clouds). This is the source of
	// truth while the user is interacting with the menu so the sliders stay
	// independent.
	lightingMenuState struct {
		timeOfDay Float.X
		sunAngle  Float.X
		fog       Float.X
		clouds    Float.X
		rain      Float.X
		snow      Float.X
		wind      Float.X
	}

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

	// underwater is the fullscreen post-process material applied to the camera
	// cover by default (tints the view + draws a waterline below the water
	// plane). Editors that borrow the cover (the shelter grid) hand it back via
	// applyCoverDefault.
	underwater ShaderMaterial.Instance

	// sky is the ShaderMaterial backing the Environment's procedural sky. The
	// Clouds slider drives its "coverage" parameter via applyCloudDensity.
	sky ShaderMaterial.Instance

	// weatherAnchor is a node positioned under the camera each frame so that
	// rain and snow particles are always emitted in a volume around the viewer.
	weatherAnchor       Node3D.Instance
	rainParticles       GPUParticles3D.Instance
	snowParticles       GPUParticles3D.Instance
	rainProcessMaterial ParticleProcessMaterial.Instance
	snowProcessMaterial ParticleProcessMaterial.Instance

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

	// lastTiming is a strictly-increasing per-client counter used to stamp every
	// committed Sculpt's Timing, giving each stroke a stable (Author, Timing)
	// identity that a Revert sculpt references for undo/redo. Seeded from the
	// high-water mark of replayed sculpts authored by this client (see
	// TerrainEditor.note Timing on load) so it never reuses a value across
	// sessions. Guarded by nextTiming.
	lastTiming musical.Timing

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

		// --- Float (vertical lift) state (for GizmoFloat) ---
		floatInitialY    Float.X     // original world Y when drag started
		floatPlanePoint  Vector3.XYZ // a point on the vertical drag plane
		floatPlaneNormal Vector3.XYZ // horizontal normal (Y=0) of the vertical plane used for lift
		floatStartGrabY  Float.X     // Y of the initial ray intersection on that plane
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

	world.Light.
		SetDirectionalShadowMode(DirectionalLight3D.ShadowOrthogonal).
		// Contribute to the sky shader (LIGHT0) so the procedural sky's sun
		// disk and day/night blend follow this light automatically.
		SetSkyMode(DirectionalLight3D.SkyModeLightAndSky).
		AsLight3D().
		SetLightEnergy(1).
		SetShadowEnabled(true).
		SetShadowBias(0.015).
		SetShadowNormalBias(0).
		SetShadowBlur(2.0)
	Light3D.Advanced(world.Light.AsLight3D()).SetParam(Light3D.ParamShadowMaxDistance, 30)

	// Procedural sky with drifting clouds. The shader reacts to the directional
	// light (LIGHT0); only the cloud coverage is pushed in, by the Clouds slider.
	skyMaterial := ShaderMaterial.New()
	skyMaterial.SetShader(LoadSync[Shader.Instance]("res://shader/sky.gdshader"))
	skyMaterial.SetShaderParameter("coverage", 0.0)
	world.sky = skyMaterial
	sky := Sky.New()
	sky.SetSkyMaterial(skyMaterial.AsMaterial())
	sky.SetRadianceSize(Sky.RadianceSize256)

	env := Environment.New().
		SetBackgroundMode(Environment.BgSky).
		SetSky(sky).
		SetAmbientLightColor(Color.X11.White).
		SetAmbientLightSkyContribution(0).
		SetAmbientLightSource(Environment.AmbientSourceColor).
		SetAmbientLightEnergy(0.5)

	worldenv := WorldEnvironment.New().SetEnvironment(env)

	world.AsNode().AddChild(worldenv.AsNode())
	world.Environment = env
	world.WorldEnvironment = worldenv

	// Seed SSAO on/off from the launch quality tier. The UI's launch-time
	// Apply ran during editor.Setup above, before this Environment existed,
	// so the per-Environment ambient-occlusion flag has to be applied here;
	// the slider re-applies it on every move (see buildSettingsMenu).
	defaultGraphicsQuality.ApplyAmbientOcclusion(env)

	// Start the world in daytime. Driving the initial look through the same
	// friendly path the rolldown uses keeps lightingMenuState authoritative,
	// so the menu opens on the real values and every editor's lighting seeds
	// from a lit world rather than the zero-value (midnight / energy 0 / black).
	world.ApplyLightingMenuState(0.38, 0.08, 0.0, 0.0, 0.0, 0.0, 0.0)

	// Weather particles (rain, snow) + wind propagation. Created once; intensity
	// is driven live by the environment menu via applyWeather.
	world.setupWeatherParticles()

	RenderingServer.SetDebugGenerateWireframes(true)

	world.FocalPoint.Lens.Camera.Cover.
		SetMesh(QuadMesh.New().AsPlaneMesh().SetSize(Vector2.New(2, 2)).AsMesh()).
		AsGeometryInstance3D().SetExtraCullMargin(16384).
		AsNode3D().RotateObjectLocal(Vector3.New(0, 1, 0), Angle.Pi)

	// Underwater post-process: tints the view and draws a waterline whenever the
	// camera approaches/crosses the global water plane. Lives on the camera
	// cover by default; the shelter grid temporarily borrows the cover and hands
	// it back through applyCoverDefault.
	underwater := ShaderMaterial.New()
	underwater.SetShader(LoadSync[Shader.Instance]("res://shader/underwater.gdshader"))
	// Rock texture for the "buried" fill — the same mineral the terrain sides use.
	underwater.SetShaderParameter("rock_sampler", LoadSync[Texture2D.Instance]("res://default/mineral.jpg"))
	world.underwater = underwater
	world.applyCoverDefault()

	fmt.Println("Client setup complete")

	// Attempt to bring up OpenXR. No-op on desktop without an XR
	// runtime; on Quest/Horizon OS this swaps the viewport into
	// stereo headset rendering and hides the 2D editor overlay.
	world.setupXR()
}

// applyCoverDefault restores the camera cover's default fullscreen material
// (the underwater post-process). Editors that borrow the cover for their own
// fullscreen pass (the shelter grid) call this to hand it back instead of
// clearing it to nil.
func (world *Client) applyCoverDefault() {
	var mat Material.Instance
	if world.underwater != (ShaderMaterial.Instance{}) {
		mat = world.underwater.AsMaterial()
	}
	world.FocalPoint.Lens.Camera.Cover.SetSurfaceOverrideMaterial(0, mat)
}

const speed = 8

// cameraTerrainClearance is how far above the terrain the camera is held when it
// would otherwise sink into the ground. The camera-terrain collision keeps the
// view from burrowing through hills while still letting it drop below the water
// surface (water is not terrain), so a small clearance keeps it just clear.
const cameraTerrainClearance Float.X = 0.5

func (world *Client) Process(dt Float.X) {
	world.time.Process(dt)

	// Keep the underwater post-process in sync with the water surface AND terrain
	// floor under the camera: the LOCAL river surface where one is carved (else
	// the global lake level), and the terrain height. The shader shows water only
	// where the floor is below the surface; otherwise (lens inside a hill) it
	// fills with rock, so going below the water plane into the ground reads as
	// being buried rather than underwater.
	if world.TerrainEditor != nil {
		camNode := world.FocalPoint.Lens.Camera.AsNode3D()
		camPos := camNode.GlobalPosition()
		// Camera-terrain collision: stop the camera descending into the ground.
		// FocalPoint Y is a pure vertical offset on the rig, so we lift it just
		// enough that the camera rides at/above the terrain floor under it,
		// recomputed every frame so it settles back down over lower ground.
		// HeightAt is the terrain (not the water) surface, so the camera can
		// still drop below the water plane and go underwater. Skipped in XR and
		// while an editor owns the camera (e.g. the critter control view).
		if !world.xr && !world.controlLockMovement {
			fp := world.FocalPoint.AsNode3D()
			fpPos := fp.Position()
			camGroundY := camPos.Y - fpPos.Y // camera Y with the rig on the ground
			lift := world.TerrainEditor.HeightAt(camPos) + cameraTerrainClearance - camGroundY
			if lift < 0 {
				lift = 0
			}
			if lift != fpPos.Y {
				fpPos.Y = lift
				fp.SetPosition(fpPos)
				camPos = camNode.GlobalPosition()
			}
		}
		if world.underwater != (ShaderMaterial.Instance{}) {
			world.underwater.SetShaderParameter("water_level", float64(world.TerrainEditor.WaterSurfaceAt(camPos)))
			world.underwater.SetShaderParameter("terrain_level", float64(world.TerrainEditor.HeightAt(camPos)))
		}
	}

	// Keep weather particles following the camera position + yaw so the emission
	// volume is always oriented toward where the player is looking. This prevents
	// the "empty when you turn around" problem common with weather systems.
	if world.weatherAnchor != Node3D.Nil {
		if cam := Viewport.Get(world.AsNode()).GetCamera3d(); cam != Camera3D.Nil {
			camNode := cam.AsNode3D()
			cp := camNode.GlobalPosition()

			anchor := world.weatherAnchor.AsNode3D()
			anchor.SetGlobalPosition(Vector3.New(cp.X, cp.Y+12, cp.Z))

			// Only copy yaw (Y rotation) so the tall box always faces the view direction.
			// Pitch/roll would tilt the weather which looks wrong.
			rot := camNode.GlobalRotation()
			rot.X = 0
			rot.Z = 0
			anchor.SetGlobalRotation(rot)
		}
	}

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

	// Any release of the left button (even while the pointer is over 2D UI)
	// ends the current world stroke. Terrain tile releases also clear it,
	// but this catches releases that happen while the mouse is over the
	// design explorer or other UI.
	if !Input.IsMouseButtonPressed(Input.MouseButtonLeft) {
		world.TerrainEditor.brushStrokeActive = false
	}

	// Texture painting and dressing only produce strokes when the user
	// is actively holding the left button after a press that actually
	// landed on terrain geometry (see TerrainTile.InputEvent). This
	// prevents clicks inside the 2D design explorer from ever starting
	// a paint or dressing action at the last-known BrushTarget.
	if world.TerrainEditor.PaintActive && world.TerrainEditor.brushStrokeActive {
		if time.Since(world.last_PaintAt) > time.Second/5 {
			world.TerrainEditor.Paint()
			world.last_PaintAt = time.Now()
		}
	}

	if world.TerrainEditor.DressActive && world.TerrainEditor.brushStrokeActive {
		te := world.TerrainEditor
		if time.Since(world.last_PaintAt) > time.Second/5 {
			te.PaintDressing()
			world.last_PaintAt = time.Now()
		}
	}

	// Category removal tools (armed from the "removal" tab in ModeDressing).
	// These are the replacement for the old Ctrl+Shift + dressing-design gesture.
	if world.TerrainEditor.ClearActive && world.TerrainEditor.brushStrokeActive {
		te := world.TerrainEditor
		if time.Since(world.last_PaintAt) > time.Second/5 {
			te.EraseDressingCategory(te.ClearCategory)
			world.last_PaintAt = time.Now()
		}
	}

	// The river paint/erase brush is a drag-paint tool driven exactly like
	// dressing: each throttled segment commits one Sculpt (PaintRiver carries
	// its own movement-spacing guard, which also establishes the flow heading).
	if world.TerrainEditor.riverBrushActive() && world.TerrainEditor.brushStrokeActive {
		if time.Since(world.last_PaintAt) > time.Second/5 {
			world.TerrainEditor.PaintRiver()
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
			switch {
			case Input.IsKeyPressed(Input.KeyShift) && world.Editing == Editing.Terrain:
				// Shift+wheel resizes the terrain brush (and nudges the gizmo-
				// toolbar size slider) instead of dollying the camera. WheelUp
				// grows the brush; WheelDown shrinks it.
				var delta Float.X
				if mouse.ButtonIndex() == Input.MouseButtonWheelUp {
					delta = brushRadiusScrollStep
				}
				if mouse.ButtonIndex() == Input.MouseButtonWheelDown {
					delta = -brushRadiusScrollStep
				}
				if delta != 0 {
					r := world.TerrainEditor.NudgeBrushRadius(delta)
					if world.ui != nil && world.ui.CloudControl != nil {
						world.ui.CloudControl.setSizeSliderValue(float64(r))
					}
				}
			case Input.IsKeyPressed(Input.KeyCtrl) && world.Editing == Editing.Terrain:
				// Ctrl+wheel adjusts the active brush's GizmoPower parameter —
				// sculpt power for raise/lower, channel depth for the river tools
				// — (and nudges the gizmo-toolbar slider) instead of dollying the
				// camera. WheelUp increases it; WheelDown decreases it.
				var delta Float.X
				if mouse.ButtonIndex() == Input.MouseButtonWheelUp {
					delta = brushPowerScrollStep
				}
				if mouse.ButtonIndex() == Input.MouseButtonWheelDown {
					delta = -brushPowerScrollStep
				}
				if delta != 0 {
					p := world.TerrainEditor.NudgeGizmoPower(delta)
					if world.ui != nil && world.ui.CloudControl != nil {
						world.ui.CloudControl.setPowerSliderValue(float64(p))
					}
				}
			case !Input.IsKeyPressed(Input.KeyShift):
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
				// While terrain editing the left button paints the ground
				// (TerrainTile.InputEvent); don't let it select or gizmo-grab
				// placed objects. This guard is required even though those
				// objects are made non-pickable in terrain mode: that only
				// gates Godot's viewport picking (the brush), whereas the
				// selection query below is an explicit intersect_ray that
				// ignores input_ray_pickable and would still hit them.
				if world.Editing == Editing.Terrain {
					break
				}
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
						world.gizmoDrag.activeGizmo = 0
						world.gizmoDrag.hasMirrorPlane = false
						world.gizmoDrag.design = musical.Design{}
						world.gizmoDrag.twistInitialY = 0
						world.gizmoDrag.twistInitialAngle = 0
						world.gizmoDrag.twistPlaneY = 0
						world.gizmoDrag.floatInitialY = 0
						world.gizmoDrag.floatPlanePoint = Vector3.Zero
						world.gizmoDrag.floatPlaneNormal = Vector3.Zero
						world.gizmoDrag.floatStartGrabY = 0
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
			// Commit using the gizmo captured at drag start (activeGizmo), not
			// the current live toolbar state. This ensures Ctrl+Shift (Float)
			// and plain Shift/Ctrl drags still produce a Commit:true Change
			// even if the user released the modifiers before the mouse button.
			if mouse.ButtonIndex() == Input.MouseButtonLeft && !mouse.AsInputEvent().IsPressed() {
				if world.gizmoDrag.active {
					if world.gizmoDrag.activeGizmo == GizmoShift ||
						world.gizmoDrag.activeGizmo == GizmoTwist ||
						world.gizmoDrag.activeGizmo == GizmoFloat ||
						world.gizmoDrag.activeGizmo == GizmoScale {
						world.commitGizmoDrag()
					}
					world.gizmoDrag.active = false
					world.gizmoDrag.activeGizmo = 0
					world.gizmoDrag.hasMirrorPlane = false
					world.gizmoDrag.design = musical.Design{}
					world.gizmoDrag.twistInitialY = 0
					world.gizmoDrag.twistInitialAngle = 0
					world.gizmoDrag.twistPlaneY = 0
					world.gizmoDrag.floatInitialY = 0
					world.gizmoDrag.floatPlanePoint = Vector3.Zero
					world.gizmoDrag.floatPlaneNormal = Vector3.Zero
					world.gizmoDrag.floatStartGrabY = 0
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

// applyLightingState writes the four lighting parameters into the live
// DirectionalLight and Environment. Angles are in radians. This is the
// single place that actually touches the renderer nodes; all per-editor
// environment Sculpt handlers and the lighting menu call it (or wrappers).
func (world *Client) applyLightingState(azimuth, elevation, energy, fogDensity Float.X) {
	if world.Light != DirectionalLight3D.Nil {
		// elevation is the sun's pitch in radians (0 at the horizon, negative
		// tilts the light down from overhead) and azimuth is its compass
		// orientation. A directional light points along its local -Z, so its
		// direction is fully described by pitch (X) + yaw (Y). The previous
		// code put the height on Z (roll), which is a no-op for a directional
		// light, so the sun never actually rose or set with the time of day.
		world.Light.AsNode3D().SetRotation(Euler.Radians{
			X: Angle.Radians(elevation),
			Y: Angle.Radians(azimuth),
			Z: 0,
		})
		world.Light.AsLight3D().SetLightEnergy(energy)
	}
	if world.Environment != Environment.Nil {
		world.Environment.SetFogEnabled(fogDensity > 0.0001)
		world.Environment.SetFogDensity(fogDensity)
		world.Environment.SetFogSunScatter(0.25)
		// Gentle ambient reduction in heavy fog for atmosphere.
		amb := Float.X(0.5)
		if fogDensity > 0.008 {
			amb = 0.32
		}
		world.Environment.SetAmbientLightEnergy(amb)
	}
}

// activeLightingEditorKey returns the musical Editor routing key that
// should be stamped on environment Sculpt records from the lighting menu.
// scenery and terrain share the world lighting owned by the "terrain" editor.
// The map keys (not Name()) are used for dispatch.
func (world *Client) activeLightingEditorKey() string {
	if world.Editing == Editing.Scenery || world.Editing == Editing.Terrain {
		return "terrain"
	}
	switch world.Editing {
	case Editing.Foliage:
		return "foliage"
	case Editing.Mineral:
		return "mineral"
	case Editing.Shelter:
		return "shelter"
	case Editing.Vehicle:
		return "vehicle"
	case Editing.Citizen:
		return "citizen"
	case Editing.Critter:
		return "critter"
	case Editing.Coaster:
		return "coaster"
	}
	return "terrain"
}

// applyLightingStateFromSlider is called by the lighting menu for immediate
// local feedback (and for legacy technical slider names from saves).
// We have removed support for the old technical names ("azimuth", "elevation", "energy")
// since no public release has shipped yet.
func (world *Client) applyLightingStateFromSlider(slider string, value Float.X) {
	switch slider {
	case "environment/time_of_day":
		_, angle, fog, clouds, rain, snow, wind := world.GetLightingMenuState()
		world.ApplyLightingMenuState(value, angle, fog, clouds, rain, snow, wind)

	case "environment/sun_angle":
		tod, _, fog, clouds, rain, snow, wind := world.GetLightingMenuState()
		world.ApplyLightingMenuState(tod, value, fog, clouds, rain, snow, wind)

	case "environment/fog":
		tod, angle, _, clouds, rain, snow, wind := world.GetLightingMenuState()
		world.ApplyLightingMenuState(tod, angle, value, clouds, rain, snow, wind)

	case "environment/clouds":
		tod, angle, fog, _, rain, snow, wind := world.GetLightingMenuState()
		world.ApplyLightingMenuState(tod, angle, fog, value, rain, snow, wind)

	case "environment/rain":
		tod, angle, fog, clouds, _, snow, wind := world.GetLightingMenuState()
		world.ApplyLightingMenuState(tod, angle, fog, clouds, value, snow, wind)

	case "environment/snow":
		tod, angle, fog, clouds, rain, _, wind := world.GetLightingMenuState()
		world.ApplyLightingMenuState(tod, angle, fog, clouds, rain, value, wind)

	case "environment/wind":
		tod, angle, fog, clouds, rain, snow, _ := world.GetLightingMenuState()
		world.ApplyLightingMenuState(tod, angle, fog, clouds, rain, snow, value)

	// Old technical names are no longer supported (pre-release).
	default:
		// Fallback: treat as direct low-level for any remaining old records
		// (harmless if none exist).
		az, el, en, fg := Float.X(0.52), Float.X(0.19), Float.X(1.0), Float.X(0.0)
		if world.Light != DirectionalLight3D.Nil {
			r := world.Light.AsNode3D().Rotation()
			az, el, en = Float.X(r.Y), Float.X(r.Z), world.Light.AsLight3D().LightEnergy()
		}
		if world.Environment != Environment.Nil {
			fg = world.Environment.FogDensity()
		}
		switch slider {
		case "environment/azimuth":
			az = value
		case "environment/elevation":
			el = value
		case "environment/energy":
			en = value
		case "environment/fog":
			fg = value
		}
		world.applyLightingState(az, el, en, fg)
	}
}

// ApplyLightingMenuState is the single authoritative way to drive the
// friendly lighting + weather controls (Time of Day + Sun Angle + Fog + Clouds + Rain + Snow + Wind).
// It updates the stored friendly state and immediately applies visuals.
func (world *Client) ApplyLightingMenuState(timeOfDay, sunAngle, fog, clouds, rain, snow, wind Float.X) {
	world.lightingMenuState.timeOfDay = timeOfDay
	world.lightingMenuState.sunAngle = sunAngle
	world.lightingMenuState.fog = fog
	world.lightingMenuState.clouds = clouds
	world.lightingMenuState.rain = rain
	world.lightingMenuState.snow = snow
	world.lightingMenuState.wind = wind

	az, el, en, fd := world.mapTimeOfDaySunAngleFog(timeOfDay, sunAngle, fog)
	world.applyLightingState(az, el, en, fd)
	world.applyCloudDensity(clouds)
	world.applyWeather(rain, snow, wind)
}

// applyCloudDensity pushes the cloud coverage (0 = clear, 1 = overcast) into
// the procedural sky shader. Safe to call before the sky exists.
func (world *Client) applyCloudDensity(clouds Float.X) {
	if world.sky != (ShaderMaterial.Instance{}) {
		world.sky.SetShaderParameter("coverage", clouds)
	}
}

// applyWeather is called whenever rain/snow/wind change. It drives particle
// intensity and the wind uniforms on sky + foliage (when available).
func (world *Client) applyWeather(rain, snow, wind Float.X) {
	// Real implementation added after particle nodes are created in Ready.
	// For now this is safe to call early (particles may be Nil).
	world.updateWeatherIntensity(rain, snow, wind)
}

// GetLightingMenuState returns the current friendly lighting + weather values.
func (world *Client) GetLightingMenuState() (timeOfDay, sunAngle, fog, clouds, rain, snow, wind Float.X) {
	s := world.lightingMenuState
	return s.timeOfDay, s.sunAngle, s.fog, s.clouds, s.rain, s.snow, s.wind
}

// mapTimeOfDaySunAngleFog converts the friendly, artist-friendly slider values
// used in the lighting menu into the low-level technical parameters that
// actually drive the light and environment.
func (world *Client) mapTimeOfDaySunAngleFog(timeOfDay, sunAngle, fog Float.X) (az, el, energy, fogDensity Float.X) {
	// timeOfDay: 0 = midnight, 0.25 = sunrise, 0.5 = noon, 0.75 = sunset, 1 = midnight.
	phase := (float64(timeOfDay) - 0.25) * 2 * math.Pi
	height := math.Sin(phase) // -1 at deepest night .. +1 at noon

	// Sun pitch: near the horizon (0) around sunrise/sunset, tilted up toward
	// overhead (~ -80°) at noon. Negative tilts the directional light down
	// toward the ground (it points along -Z). Driving X here is what actually
	// makes the sun rise and set as the time of day changes.
	el = Float.X(-height * math.Pi * 0.45)

	// Energy: a broad, bright daytime plateau that rolls off softly through
	// twilight to a dim — but not pitch-black — night.
	day := math.Max(0, height)
	energy = Float.X(0.15 + day*1.6)

	// Azimuth: the artist-controlled Sun Angle, a full circle around 0..1.
	az = Float.X(float64(sunAngle) * 2 * math.Pi)

	// Fog / atmosphere amount (user-facing 0-1 maps to reasonable density).
	fogDensity = fog * 0.055

	return az, el, energy, fogDensity
}

// setupWeatherParticles creates the global rain and snow particle systems.
// They live under a weatherAnchor that is moved to follow the camera every frame.
func (world *Client) setupWeatherParticles() {
	anchor := Node3D.New()
	anchor.AsNode().SetName("WeatherAnchor")
	world.AsNode().AddChild(anchor.AsNode())
	world.weatherAnchor = anchor

	// Rain: tall vertical emission volume (GPU particles for performance at high counts).
	rain := GPUParticles3D.New()
	rain.AsNode().SetName("Rain")
	rain.SetAmount(1400)
	rain.SetLifetime(1.3)
	rain.SetDrawPasses(1)
	streak := QuadMesh.New().AsPlaneMesh().SetSize(Vector2.New(0.022, 0.48)).AsMesh()
	rain.SetDrawPass1(streak)
	rain.SetVisibilityAabb(AABB.PositionSize{
		Position: Vector3.New(-180, -90, -180),
		Size:     Vector3.New(360, 200, 360),
	})
	rain.SetPreprocess(0.8)
	rain.SetDrawOrder(GPUParticles3D.DrawOrderViewDepth)
	rain.SetLocalCoords(false)
	rain.AsGeometryInstance3D().SetCastShadow(GeometryInstance3D.ShadowCastingSettingOff)

	rainMat := ParticleProcessMaterial.New()
	rainMat.SetEmissionShape(ParticleProcessMaterial.EmissionShapeBox)
	rainMat.SetEmissionBoxExtents(Vector3.New(160, 55, 160))
	rainMat.SetDirection(Vector3.New(0, -1, 0))
	rainMat.SetSpread(8) // small random angle helps fill the view when looking around
	rainMat.SetInitialVelocityMin(16)
	rainMat.SetInitialVelocityMax(23)
	rainMat.SetGravity(Vector3.New(0, -2.5, 0))
	rainMat.SetScaleMin(0.85)
	rainMat.SetScaleMax(1.1)
	rainMat.SetColor(Color.RGBA{R: 0.82, G: 0.85, B: 0.92, A: 0.72})
	rain.SetProcessMaterial(rainMat.AsMaterial())

	world.weatherAnchor.AsNode().AddChild(rain.AsNode())
	world.rainParticles = rain
	world.rainProcessMaterial = rainMat

	// Snow (GPU particles): tall volume for visibility in every direction.
	snow := GPUParticles3D.New()
	snow.AsNode().SetName("Snow")
	snow.SetAmount(3000)
	snow.SetLifetime(4.8)
	snow.SetDrawPasses(1)
	flake := QuadMesh.New().AsPlaneMesh().SetSize(Vector2.New(0.16, 0.16)).AsMesh()
	snow.SetDrawPass1(flake)
	snow.SetVisibilityAabb(AABB.PositionSize{
		Position: Vector3.New(-175, -85, -175),
		Size:     Vector3.New(350, 190, 350),
	})
	snow.SetPreprocess(1.8)
	snow.SetDrawOrder(GPUParticles3D.DrawOrderViewDepth)
	snow.SetLocalCoords(false)
	snow.AsGeometryInstance3D().SetCastShadow(GeometryInstance3D.ShadowCastingSettingOff)

	snowMat := ParticleProcessMaterial.New()
	snowMat.SetEmissionShape(ParticleProcessMaterial.EmissionShapeBox)
	snowMat.SetEmissionBoxExtents(Vector3.New(155, 52, 155))
	snowMat.SetDirection(Vector3.New(0, -1, 0))
	snowMat.SetSpread(12) // helps snow fill space in all directions
	snowMat.SetInitialVelocityMin(0.8)
	snowMat.SetInitialVelocityMax(2.6)
	snowMat.SetGravity(Vector3.New(0, -0.35, 0))
	snowMat.SetScaleMin(0.22)
	snowMat.SetScaleMax(0.42)
	snowMat.SetColor(Color.RGBA{R: 0.96, G: 0.97, B: 1.0, A: 0.92})
	snowMat.SetAngularVelocityMin(-28)
	snowMat.SetAngularVelocityMax(28)
	snow.SetProcessMaterial(snowMat.AsMaterial())

	world.weatherAnchor.AsNode().AddChild(snow.AsNode())
	world.snowParticles = snow
	world.snowProcessMaterial = snowMat

	// Start disabled; intensity is driven later by environment sliders via Apply.
	rain.SetEmitting(false)
	snow.SetEmitting(false)
}

// grassWindGlobalsOnce guards one-time registration of the global shader
// parameters the terrain grass wind shader reads (grass_wind.gdshader). They
// live on the RenderingServer, which is process-global, so registering once —
// before any grass shader compiles — is enough; updateWeatherIntensity then
// just writes the current values. Registering up front also stops Godot from
// logging "global parameter not found" when the first grass blade renders.
var grassWindGlobalsOnce sync.Once

func ensureGrassWindGlobals() {
	grassWindGlobalsOnce.Do(func() {
		RenderingServer.GlobalShaderParameterAdd("grass_wind_strength", RenderingServer.GlobalVarTypeFloat, float64(0.05))
		RenderingServer.GlobalShaderParameterAdd("grass_wind_speed", RenderingServer.GlobalVarTypeFloat, float64(1.1))
		RenderingServer.GlobalShaderParameterAdd("grass_wind_dir", RenderingServer.GlobalVarTypeVec2, Vector2.New(0.85, 0.35))
		RenderingServer.GlobalShaderParameterAdd("grass_wind_bias", RenderingServer.GlobalVarTypeFloat, float64(0.0))
		// Height-brush hover preview (see grass_wind.gdshader). Off by default;
		// TerrainEditor.Process pushes the live brush centre/strength/radius while a
		// height brush is hovering and resets height to 0 otherwise.
		RenderingServer.GlobalShaderParameterAdd("grass_brush_uplift", RenderingServer.GlobalVarTypeVec2, Vector2.Zero)
		RenderingServer.GlobalShaderParameterAdd("grass_brush_height", RenderingServer.GlobalVarTypeFloat, float64(0.0))
		RenderingServer.GlobalShaderParameterAdd("grass_brush_radius", RenderingServer.GlobalVarTypeFloat, float64(0.0))
	})
}

// updateWeatherIntensity modulates emission rate (via amount) and wind bias
// for the precipitation particles, and also pushes wind into sky + foliage +
// terrain grass.
func (world *Client) updateWeatherIntensity(rain, snow, wind Float.X) {
	// Rain (GPU)
	if world.rainParticles != GPUParticles3D.Nil {
		if rain > 0.001 {
			amt := int(1400 * float64(rain))
			if amt < 8 {
				amt = 8
			}
			world.rainParticles.SetAmount(amt)
			world.rainParticles.SetEmitting(true)

			if world.rainProcessMaterial != (ParticleProcessMaterial.Instance{}) {
				wx := Float.X(wind) * 1.1
				world.rainProcessMaterial.SetDirection(Vector3.New(wx, -1, 0))
			}
		} else {
			world.rainParticles.SetEmitting(false)
		}
	}

	// Snow (GPU)
	if world.snowParticles != GPUParticles3D.Nil {
		if snow > 0.001 {
			amt := int(2600 * float64(snow))
			if amt < 25 {
				amt = 25
			}
			world.snowParticles.SetAmount(amt)
			world.snowParticles.SetEmitting(true)

			if world.snowProcessMaterial != (ParticleProcessMaterial.Instance{}) {
				wx := Float.X(wind) * 2.4
				wz := Float.X(wind) * 0.7
				world.snowProcessMaterial.SetDirection(Vector3.New(wx, -0.65, wz))
			}
		} else {
			world.snowParticles.SetEmitting(false)
		}
	}

	// Sky cloud drift speed scales with wind (base wind in shader is gentle).
	if world.sky != (ShaderMaterial.Instance{}) {
		base := Vector2.New(0.045, 0.014)
		w := Float.X(wind)
		world.sky.SetShaderParameter("cloud_wind", Vector2.New(
			float64(base.X*(1+1.8*w)),
			float64(base.Y*(1+1.8*w)),
		))
	}

	// Foliage editor preview wind (if active).
	if world.FoliageEditor != nil && world.FoliageEditor.leafletMaterial != (ShaderMaterial.Instance{}) {
		mat := world.FoliageEditor.leafletMaterial
		// wind_strength in shader is 0..0.5 range; map our 0..1 nicely.
		str := 0.02 + float64(wind)*0.22
		mat.SetShaderParameter("wind_strength", str)
		// Slightly faster gusts at high wind.
		spd := 1.2 + float64(wind)*1.8
		mat.SetShaderParameter("wind_speed", spd)
		// Bias the wind direction a little with the wind slider (keeps it lively).
		dirx := 0.85 + float64(wind)*0.3
		diry := 0.35 + float64(wind)*0.15
		mat.SetShaderParameter("wind_dir", Vector2.New(dirx, diry))
	}

	// Terrain grass wind (global shader parameters; see grass_wind.gdshader).
	// strength is the lean as a fraction of blade height (the shader caps it at
	// 0.8 so it never lies flat). Keep a faint idle breeze, then ramp up: at the
	// top the blades are pulled ~0.8 of the way over and held there, jittering.
	// gust speed scales up too so the high-wind "pull" flutters rapidly.
	ensureGrassWindGlobals()
	wf := float64(wind)
	RenderingServer.GlobalShaderParameterSet("grass_wind_strength", 0.03+wf*0.8)
	RenderingServer.GlobalShaderParameterSet("grass_wind_speed", 0.9+wf*3.1)
	RenderingServer.GlobalShaderParameterSet("grass_wind_dir", Vector2.New(0.85+wf*0.3, 0.35+wf*0.15))
	// One-sidedness ramps in late (squared) so low/mid wind keeps the symmetric
	// travelling waves, and only the top of the slider becomes the steady
	// hard-pull-in-one-direction hurricane look.
	RenderingServer.GlobalShaderParameterSet("grass_wind_bias", wf*wf)

	// Ocean swell scales with wind: no wind ⇒ glassy water (wave_height 0), up
	// to heavy hurricane swell at the top. The shader fades these long swells
	// out in shallow water, so this only heaves the open sea.
	if world.TerrainEditor != nil && world.TerrainEditor.water_shader != (ShaderMaterial.Instance{}) {
		world.TerrainEditor.water_shader.SetShaderParameter("wave_height", wf*2.0)
	}
}
