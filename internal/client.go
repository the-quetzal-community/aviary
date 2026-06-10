package internal

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"graphics.gd/classdb/Camera3D"
	"graphics.gd/classdb/CameraAttributesPractical"
	"graphics.gd/classdb/CylinderMesh"
	"graphics.gd/classdb/DirectionalLight3D"
	"graphics.gd/classdb/Engine"
	"graphics.gd/classdb/Environment"
	"graphics.gd/classdb/FileAccess"
	"graphics.gd/classdb/GPUParticles3D"
	"graphics.gd/classdb/GeometryInstance3D"
	"graphics.gd/classdb/Image"
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
	"graphics.gd/classdb/Resource"
	"graphics.gd/classdb/Script"
	"graphics.gd/classdb/Shader"
	"graphics.gd/classdb/ShaderMaterial"
	"graphics.gd/classdb/SubViewport"
	"graphics.gd/classdb/Texture2D"
	"graphics.gd/classdb/Viewport"
	"graphics.gd/classdb/ViewportTexture"
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
	"the.quetzal.community/aviary/internal/clouds"
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

	// Moon is a second directional light that lights the scene at night, from
	// the side of the sky opposite the sun (so it rises as the sun sets). It is
	// SkyModeLightOnly — it must NOT register in the sky shader's LIGHTn slots
	// (the sun owns LIGHT0); the visible moon disk is drawn directly in
	// sky.gdshader from the sun direction and the moon_phase uniform. Its energy
	// and the disk's crescent are both driven by the Moon slider's phase (see
	// applyMoonState).
	Moon DirectionalLight3D.Instance

	// shadowMaxDistance is the directional-shadow far distance last pushed into
	// both lights. It is driven by the camera zoom (see updateShadowDistance) so
	// shadows reach as far as the view does when pulled out instead of fading at
	// a fixed radius; cached so the per-frame update only pays the cgo SetParam
	// when the zoom has actually moved it.
	shadowMaxDistance Float.X

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
		moon      Float.X // moon phase: 0 = new (dark), 0.5 = half, 1 = full
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

	// clouds owns every cloud-rendering subsystem (procedural sky, FogVolume,
	// SunshineClouds2) and the terrain cloud-shadow globals. The Clouds/Wind
	// sliders, Time-of-Day, and the graphics-quality tier drive it through the
	// applyCloud*/SetWind forwarders below; the sun (world.Light) is tracked so
	// the clouds follow the day/night cycle automatically. See internal/clouds.
	clouds *clouds.System

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
	host   musical.Author // author the session host adopted (0 when we are the host / offline); the joiner follows its clock
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
		// twistPivot is the mesh's world-space bounds centre at drag start.
		// In library-sizing debug mode shelter twists rotate the part about
		// this point instead of the node origin (twistPivotValid), so a part
		// whose geometry is offset to a cell edge spins in place rather than
		// orbiting the cell anchor; the node origin follows the orbit.
		twistPivot      Vector3.XYZ
		twistPivotValid bool

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

	// Loading screen state. While loading is true, Process suppresses 3D
	// rendering (Viewport.SetDisable3d) and fast-drains the replay queue under
	// a full-screen SceneLoader overlay, so the world is built up without
	// rendering the half-finished scene each frame (see beginLoading /
	// processLoading / finishLoading).
	loading        bool
	loadingOverlay *SceneLoader
	// loadProgressArmed gates the byte-counting file wrapper to the FIRST
	// Storage.Open (the initial replay); later Opens serve joining peers
	// (musical server srv.handle) and must not reset the bar.
	loadProgressArmed atomic.Bool
	loadTotalBytes    atomic.Int64 // .mus3 size; 0 ⇒ unknown (multiplayer join)
	loadReadBytes     atomic.Int64 // bytes decoded so far (initial replay)
	loadEnqueued      atomic.Int64 // mutation closures pushed into queue
	loadDequeued      atomic.Int64 // mutation closures applied during loading
	loadIdleSince     time.Time    // when the queue first went idle (join grace)
	loadLastProgress  time.Time    // last time any load counter advanced (stall guard)
	loadLastSeen      int64        // counter snapshot for the stall guard

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
			design_ids:         make(map[musical.Author]uint16),
			entity_ids:         make(map[musical.Author]uint16),
			design_to_entity:   make(map[musical.Design][]Node3D.ID),
			entity_to_object:   make(map[musical.Entity]Node3D.ID),
			object_to_entity:   make(map[Node3D.ID]musical.Entity),
			pending_actions:    make(map[musical.Entity][]musical.Action),
			entity_move_timing: make(map[musical.Entity]musical.Timing),
			entity_float_delta: make(map[musical.Entity]Float.X),
			design_to_string:   make(map[musical.Design]string),
			packed_scenes:      make(map[musical.Design]PackedScene.ID),
			textures:           make(map[musical.Design]Texture2D.ID),
			loaded:             make(map[string]musical.Design),
			missing_scenes:     make(map[musical.Design]bool),
		},
		clients: make(chan musical.Networking),
		authors: make(map[musical.Author]Node3D.ID),

		load_last_save: true,
		queue:          make(chan func(), queueCapFromEnv()),
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
	if UserDataDir == "" {
		UserDataDir = OS.GetUserDataDir()
	}
	client.network.Authentication = UserState.Secret
	return client
}

// deviceAuthor maps this install's stable device id to the musical.Author it
// adopts when hosting/editing offline, so two devices editing the same cloud
// scene don't both write as author 0 and collide their entity numbers when the
// per-device save parts are merged on load. Mapped into [256, 65535]: 0 is the
// legacy/default author and 1..255 stay reserved for the host's sequentially
// assigned live joiners, so an offline device-author can never alias a live
// joiner. uint16 is the Author width, so distinct devices can in principle hash
// alike, but the birthday odds across a single user's handful of devices are
// negligible.
func deviceAuthor(device string) musical.Author {
	if device == "" {
		return 0
	}
	h := fnv.New32a()
	h.Write([]byte(device))
	const reserved = 256
	return musical.Author(reserved + h.Sum32()%(65536-reserved))
}

var UserState struct {
	Aviary signalling.User
	Editor Subject
	Device string // public device name
	Secret string // secret to be linked with a Quetzal Community Account.
	WorkID musical.WorkID

	// GraphicsQuality persists the user's choice from the Settings slider.
	// GraphicsQualitySet distinguishes an explicit choice (including Toaster/0)
	// from the zero-value on first run or after upgrading from an older save.
	GraphicsQuality    GraphicsQuality
	GraphicsQualitySet bool

	// AuthorPreferences is a ranked list of preferred library authors
	// (highest preference first). When the design explorer resets (e.g. on
	// editor or mode switch, which call Refresh with author=""), the first
	// author in this list that has content for the active editor+mode is
	// chosen. Explicitly clicking an author button bumps it to the front
	// and persists the new ranking.
	AuthorPreferences []string
}

// UserDataDir is the value of OS.GetUserDataDir() captured once early on the
// main thread (see main.go). Code that runs on background goroutines (such as
// the musical server goroutines that call Storage.Open for loading .mus3 logs,
// or resource loader thread, or upload goroutines) must use this string for
// constructing user data paths instead of calling OS.GetUserDataDir directly;
// the latter performs cgo into Godot which is not safe off the main thread.
var UserDataDir string

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
	userfile := FileAccess.Open(OS.GetConfigDir()+"/user.json", FileAccess.Write)
	buf, err := json.Marshal(UserState)
	if err != nil {
		Engine.Raise(fmt.Errorf("failed to marshal user state: %w", err))
		return
	}
	userfile.StoreBuffer(buf)
	userfile.Close()
}

func (world *Client) loadUserState() {
	userfile := FileAccess.Open(OS.GetConfigDir()+"/user.json", FileAccess.Read)
	if userfile != FileAccess.Nil {
		buf := userfile.GetBuffer(FileAccess.GetSize(OS.GetConfigDir() + "/user.json"))
		if err := json.Unmarshal(buf, &UserState); err != nil {
			Engine.Raise(fmt.Errorf("failed to unmarshal user state: %w", err))
		}
	}
	if UserState.Editor == (Subject{}) {
		UserState.Editor = Editing.Scenery
	}
	if !UserState.GraphicsQualitySet {
		UserState.GraphicsQuality = defaultGraphicsQuality
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
		// Wrap so every committed Change we record gets a wall-clock Timing —
		// the replay then orders positional edits by time, not save-part order.
		world.space = stampedSpace{inner: space, clock: &world.time}
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
	profMark("Client.Ready: begin")
	defer profMark("Client.Ready: end (returning to engine)")
	if UserDataDir == "" {
		UserDataDir = OS.GetUserDataDir()
	}
	// Bring up the loading overlay and stop rendering the 3D world before any
	// replay/setup runs: the scene is built up under the splash and only shown
	// once the .mus3 log is fully applied (see processLoading/finishLoading).
	// Must happen before musical.Host below so loadProgressArmed is set before
	// the decode goroutine calls Storage.Open.
	world.beginLoading()
	// Register the cloud-shadow global uniforms up front, before any terrain tile (and
	// its shader) is created and compiled — otherwise terrain.gdshader compiles first and
	// Godot errors "Global uniform 'cloud_shadow_sun_dir' does not exist" until the lazy
	// registration catches up. (terrain.gdshader is the only editor/early-compiled shader
	// that reads these; grass is spawned later, after the grass globals register.)
	clouds.EnsureShadowGlobals()
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
		var hosted musical.UsersSpace3D
		// version already carries its "v" prefix (velopack sets "v1.2.3"),
		// and is empty in dev builds.
		hosted, _, err = musical.Host(strings.TrimSpace("Aviary "+version), clients_iter, world.record, musicalImpl{world}, musicalImpl{world}, musicalImpl{world}, deviceAuthor(UserState.Device)) // FIXME race?
		if err != nil {
			Engine.Raise(err)
		}
		// Wrap so every committed Change we record locally gets a wall-clock
		// Timing, exactly like the network-join path above. Without this,
		// single-player edits are written with Timing 0, so on reload the
		// multi-part replay can't order positional edits by time — e.g. a fence
		// lifted with GizmoFloat across two save parts gets a non-deterministic Y
		// (the "fences change height on reload" corruption).
		world.space = stampedSpace{inner: hosted, clock: &world.time}
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
	world.VehicleEditor.recorder = world
	world.VehicleEditor.library = world
	world.VehicleEditor.workbench = world
	world.FoliageEditor.recorder = world
	world.FoliageEditor.library = world
	world.FoliageEditor.workbench = world
	world.FoliageEditor.lights = world
	world.MineralEditor.recorder = world
	world.MineralEditor.library = world
	world.MineralEditor.workbench = world
	world.MineralEditor.lights = world
	world.SceneryEditor.client = world
	world.ShelterEditor.recorder = world
	world.ShelterEditor.library = world
	world.ShelterEditor.workbench = world
	world.ShelterEditor.rig = world
	// CitizenEditor is migrated to capability ports (editor_ports.go): it
	// holds only the narrow interfaces it uses, not the whole client.
	world.CitizenEditor.recorder = world
	world.CitizenEditor.library = world
	world.CitizenEditor.workbench = world
	world.CitizenEditor.lights = world
	world.CritterEditor.client = world
	world.CoasterEditor.recorder = world
	world.CoasterEditor.library = world
	world.CoasterEditor.workbench = world
	world.CoasterEditor.terrain = world.TerrainEditor
	profMark("Ready: loading editor.tscn")
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
	profMark("Ready: editor UI set up")
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
		// Shadow enable + bias (depth + normal) are NOT set here: both are
		// quality-dependent (Toaster casts none; the small low-tier atlas needs
		// more, mostly-normal, bias) and applied by applyShadowQuality below.
		SetShadowBlur(2.0)
	Light3D.Advanced(world.Light.AsLight3D()).SetParam(Light3D.ParamShadowMaxDistance, shadowDistanceBase)
	world.shadowMaxDistance = shadowDistanceBase // updateShadowDistance grows this with zoom
	// (The sun's ParamVolumetricFogEnergy, which only affects the FogVolume cloud
	// layer, is set inside clouds.New together with the rest of the cloud wiring.)

	// The moon: a second directional that lights the night from opposite the sun.
	// SkyModeLightOnly keeps it OUT of the sky shader's LIGHTn slots (the sun is
	// LIGHT0 and the moon disk is drawn procedurally from moon_phase), so it only
	// contributes scene lighting + shadows. It starts dark; applyMoonState raises
	// its energy (and toggles visibility) with the phase and the night gate.
	world.Moon.
		SetDirectionalShadowMode(DirectionalLight3D.ShadowOrthogonal).
		SetSkyMode(DirectionalLight3D.SkyModeLightOnly).
		AsLight3D().
		SetLightEnergy(0).
		// Shadow enable + bias applied per quality tier by applyShadowQuality
		// (see the sun above).
		SetShadowBlur(2.0)
	Light3D.Advanced(world.Moon.AsLight3D()).SetParam(Light3D.ParamShadowMaxDistance, shadowDistanceBase)

	// World Environment: ambient + tonemap + glow are scene-global (they reshape
	// every surface), so they live here; the procedural sky, the volumetric-fog
	// cloud params, and the SunshineClouds2 wiring are all set up by clouds.New
	// below, which borrows this Environment.
	env := Environment.New().
		SetAmbientLightColor(Color.X11.White).
		SetAmbientLightSkyContribution(0).
		SetAmbientLightSource(Environment.AmbientSourceColor).
		SetAmbientLightEnergy(0.5).
		// Filmic tonemapping + glow so HDR highlights — the SunshineClouds2 clouds
		// especially, but also the sky/sun disk — roll off into shaded gradients
		// instead of clipping to flat white the way the default Linear tonemap did
		// (that was why the clouds read as a pure-white blob while looking fluffy in
		// the addon's own example scene, which ships Filmic + glow). NOTE: this is a
		// GLOBAL change — it reshapes every surface's highlights, so the day/night
		// sun energy / ambient (mapTimeOfDaySunAngleFog) may want a follow-up re-tune.
		SetTonemapMode(Environment.ToneMapperFilmic).
		// Raise the tonemapper white point well above the default 1.0. The cloud
		// shader scales its brightness by sun height (sunUpWeight), so an overhead
		// noon sun pushes the clouds past 1.0 and clips them to flat white — detail
		// only survived at sunrise/sunset where the low sun keeps them dim. A higher
		// white reference rolls those bright noon values off into visible shading
		// instead of clipping (Godot recommends >=6 for photographic lighting). It is
		// global, so it also softens terrain/water highlights; dial toward ~4-5 if the
		// scene looks too low-contrast, or re-tune the day/night sun energy.
		SetTonemapWhite(8.0).
		SetGlowEnabled(true)

	// SDFGI (real-time GI, QualityHighest only — see ApplyEnvironmentQuality) reads
	// the sky for its environmental light, so the saturated blue sky bleeds a blue
	// cast across the whole scene that the lower (GI-less) tiers don't have. Halve
	// the GI energy to pull that back toward the Refined look: it tones the whole
	// indirect contribution down, and the sky is the dominant one outdoors, so it
	// mostly takes the blue with it. Harmless when SDFGI is off (lower tiers). If
	// the blue still dominates, the targeted lever is env.SetSdfgiReadSkyLight(false),
	// which drops the sky's contribution entirely while keeping surface colour bleed.
	env.SetSdfgiEnergy(0.5)

	worldenv := WorldEnvironment.New().SetEnvironment(env)

	world.AsNode().AddChild(worldenv.AsNode())
	world.Environment = env
	world.WorldEnvironment = worldenv
	// Auto-exposure on the world camera attributes. The SunshineClouds2 shader
	// scales cloud brightness by sun height (sunUpWeight), so an overhead noon sun
	// makes the clouds intrinsically ~7x brighter than the low-sun sunrise/sunset
	// clouds that already read well — no fixed exposure or tonemap white point can
	// satisfy both (lower it enough for noon and sunset goes black). Auto-exposure
	// adapts exposure to the on-screen brightness: a bright noon sky exposes DOWN so
	// the clouds drop into the tonemapper's range and their shading shows; a dim
	// sunset exposes up. The low scale targets a darker mid-grey (what surfaced the
	// cloud detail in testing); the bounded min/max sensitivity stops it pumping too
	// hard as the view pans between sky and terrain. Pairs with the raised
	// tonemap_white, which gives the still-bright clouds headroom to roll off.
	camAttrs := CameraAttributesPractical.New()
	worldenv.SetCameraAttributes(camAttrs.AsCameraAttributes())

	// All cloud rendering (procedural sky + BgSky background, the world-space
	// volumetric-fog cloud layer, and the SunshineClouds2 compositor) lives in the
	// clouds package. New borrows the Environment — which is already wrapped in the
	// in-tree WorldEnvironment above, a precondition: the SunshineClouds2 driver
	// attaches its CompositorEffect by walking the tree for a WorldEnvironment the
	// moment its resource is assigned inside New. It tracks world.Light as the sun.
	// The four resources are loaded here, on the loader thread (LoadSync is
	// package-private to internal), and handed in.
	profMark("Ready: clouds.New begin (loads sky/fog shaders + SunshineClouds driver)")
	world.clouds = clouds.New(world.AsNode(), env, world.Light, clouds.Resources{
		SkyShader:    LoadSync[Shader.Instance]("res://shader/sky.gdshader"),
		FogShader:    LoadSync[Shader.Instance]("res://shader/clouds_fog.gdshader"),
		DriverScript: LoadSync[Script.Instance]("res://addons/SunshineClouds2/SunshineCloudsDriver.gd"),
		Effect:       LoadSync[Resource.Instance]("res://addons/SunshineClouds2/aviary_clouds.tres"),
	})
	profMark("Ready: clouds.New done")
	// Release the cloud system's resources at shutdown so they don't report as leaks.
	OnShutdown(world.clouds.Free)

	// Seed cloud + SSAO/SDFGI state from the launch quality tier (persisted or
	// default). The UI's launch-time Apply ran during editor.Setup above, before
	// this Environment/sky/clouds existed, so these per-resource flags have to be
	// applied here; the settings slider re-applies them on every move (see
	// buildSettingsMenu).
	UserState.GraphicsQuality.ApplyEnvironmentQuality(env)
	world.applyCloudQuality(UserState.GraphicsQuality)
	world.applyWaterQuality(UserState.GraphicsQuality)
	world.applyShadowQuality(UserState.GraphicsQuality)

	// Start the world in daytime. Driving the initial look through the same
	// friendly path the rolldown uses keeps lightingMenuState authoritative,
	// so the menu opens on the real values and every editor's lighting seeds
	// from a lit world rather than the zero-value (midnight / energy 0 / black).
	world.ApplyLightingMenuState(0.38, 0.08, 0.0, 0.0, 0.0, 0.0, 0.0, 0.5)

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
	// Free the post-process material (and thus its shader + rock texture) at shutdown.
	OnShutdown(func() { Object.Free(world.underwater) })
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

// loadDrainBudget bounds how long processLoading spends applying queued
// mutations per frame before yielding so the 2D loading overlay keeps animating
// and the window stays responsive. The decode goroutine produces faster than we
// apply, so this is effectively a continuous-drain duty cycle: larger ⇒ faster
// build, choppier splash.
var loadDrainBudget = drainBudgetFromEnv()

// drainBudgetFromEnv reads AVIARY_DRAIN_MS (milliseconds) so the per-frame
// replay drain budget can be tuned for profiling without recompiling. Defaults
// to 30ms. 3D rendering is disabled under the splash, so a larger value drains
// the replay queue faster at the cost of a choppier 2D splash — but only helps
// if the main-thread apply is the bottleneck (see loadprofile.go's verdict).
func drainBudgetFromEnv() time.Duration {
	if v := os.Getenv("AVIARY_DRAIN_MS"); v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms > 0 {
			return time.Duration(ms) * time.Millisecond
		}
	}
	return 30 * time.Millisecond
}

// queueCapFromEnv reads AVIARY_QUEUE_CAP so the replay queue's buffer size can be
// tuned for profiling without recompiling. Defaults to 1000. A larger cap lets the
// decode goroutine run ahead instead of blocking on a full queue, so (paired with a
// larger AVIARY_DRAIN_MS) the main thread can apply many more buffered mutations per
// frame — testing whether the load is throttled by the queue cap rather than CPU.
func queueCapFromEnv() int {
	if v := os.Getenv("AVIARY_QUEUE_CAP"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 1000
}

// joinLoadGrace is how long the queue must stay idle (after author assignment)
// before the splash is dismissed on the join path, where the host streams
// catch-up state with no end marker. Single-player loads dismiss immediately on
// idle (the decode completes synchronously before the author assignment).
const joinLoadGrace = 400 * time.Millisecond

// loadStallTimeout fails the loading screen open if no load counter advances for
// this long, so a broken or hung load never traps the user behind the splash.
const loadStallTimeout = 30 * time.Second

// beginLoading raises the SceneLoader splash and suppresses 3D rendering for the
// duration of the initial replay. Called at the top of Ready (before
// musical.Host) for both single-player loads and multiplayer joins.
func (world *Client) beginLoading() {
	profMark("beginLoading: splash up, 3D disabled, replay starts")
	startLoadCPUProfile()
	world.loading = true
	// Suppress per-frame terrain rebuilds for the duration of the replay; the
	// touched tiles are rebuilt once each in finishLoading (flushBulkReloads).
	// Folding ~61k sculpts one rebuild-per-frame at a time dominated load time.
	// AVIARY_DEFER_TERRAIN=0 disables it (for A/B comparison).
	if world.TerrainEditor != nil && os.Getenv("AVIARY_DEFER_TERRAIN") != "0" {
		world.TerrainEditor.bulkReplay = true
	}
	// Buffer the critter editor's bone/leg/weight sculpts during the replay
	// and fold them once in finishLoading (flushCritterReplay), backed by a
	// snapshot that can skip the fold. MEASURED: this saves ~0 wall here —
	// the critter fold of 40k sculpts is only ~30ms (the historical "~4s
	// critter" was queue/cgo-dispatch overhead during the decode-drain, NOT
	// apply work the fold/snapshot can remove). So it's gated behind the
	// snapshot flag, off by default, leaving the default path (incremental
	// per-sculpt apply) unchanged. Kept for parity with the terrain snapshot
	// and in case critter folding ever becomes expensive. AVIARY_DEFER_CRITTER=0
	// force-disables even when snapshots are on (for A/B comparison).
	if world.CritterEditor != nil && snapshotEnabled && os.Getenv("AVIARY_DEFER_CRITTER") != "0" {
		world.CritterEditor.bulkReplay = true
	}
	world.loadProgressArmed.Store(true)
	world.loadLastProgress = time.Now()
	overlay := LoadSync[PackedScene.Instance]("res://ui/scene_loader.tscn").Instantiate()
	if loader, ok := Object.As[*SceneLoader](overlay); ok {
		world.loadingOverlay = loader
		world.AsNode().AddChild(loader.AsNode())
	}
	// Skip the 3D render pass while the opaque splash is up; 2D still draws, so
	// the overlay animates but the half-built world is never rendered.
	Viewport.Get(world.AsNode()).SetDisable3d(true)
}

// finishLoading tears the splash down and resumes 3D rendering once the world
// has been fully built.
// reseatFloats re-applies every tracked float entity's terrain-relative lift
// against the CURRENT terrain heightfield: node.Y = HeightAt(node.XZ) + delta.
// Run after flushBulkReloads, because during the bulk replay the heightfield
// isn't built yet, so an Editor="float" Change reconstructs its Y against
// HeightAt==0 and the object lands at ~delta regardless of the real terrain
// height (too low where terrain is high, too high where it dips below 0). A
// correctly-seated live float re-derives the same Y here, so this is a no-op for
// those; stale entries (entity since removed) are pruned.
func (world *Client) reseatFloats() {
	if world.TerrainEditor == nil {
		return
	}
	for entity, delta := range world.entity_float_delta {
		node, ok := world.entity_to_object[entity].Instance()
		if !ok {
			delete(world.entity_float_delta, entity)
			continue
		}
		pos := node.Position()
		pos.Y = world.TerrainEditor.HeightAt(Vector3.New(pos.X, 0, pos.Z)) + delta
		node.SetPosition(pos)
	}
}

// reseatMobileEntities re-seats mobile dressing entities against the FINAL
// composed terrain, so ground critters are terrain-relative on reload.
//
// A mobile entity records an absolute Y at placement (Editor=""), and objects
// only re-seat against terrain during LIVE edits (TerrainEditor.reprojectObjects,
// HeightAt+delta). On reload there's no live edit and a cloud scene is stitched
// from several device parts replayed non-causally, so another part's terrain
// raise can apply after a critter's placement — the scorpions that "vanished"
// were actually under the ground. Floats carry their own terrain-relative delta
// (reseatFloats) and may sit intentionally low/high, so they're skipped here.
//
//   - Ground-walking categories (critter/citizen/scooter/…) are snapped onto the
//     surface (delta 0, matching reprojectObjects for a ground-seated object), so
//     they're never buried and never float, regardless of how the terrain was
//     composed.
//   - Air/water categories (airship/rockets/seaship/swimmer) keep their absolute
//     Y and are only lifted when strictly BELOW the surface (nothing can be under
//     the ground/seabed), so an airship at altitude or a ship on a lake stays put.
//
// Static scenery is untouched (it may be intentionally embedded).
func (world *Client) reseatMobileEntities() {
	if world.TerrainEditor == nil {
		return
	}
	for design, ids := range world.design_to_entity {
		uri, ok := world.design_to_string[design]
		if !ok {
			continue
		}
		category := designCategory(uri)
		if !isMobileDesignCategory(category) {
			continue
		}
		walksTerrain := isTerrainWalkingCategory(category)
		for _, id := range ids {
			if _, isFloat := world.entity_float_delta[world.object_to_entity[id]]; isFloat {
				continue
			}
			node, ok := id.Instance()
			if !ok {
				continue
			}
			pos := node.Position()
			terrainY := world.TerrainEditor.HeightAt(Vector3.New(pos.X, 0, pos.Z))
			switch {
			case walksTerrain:
				pos.Y = terrainY // terrain-relative: ride the surface
			case pos.Y < terrainY:
				pos.Y = terrainY // air/water: only un-bury, never lower
			default:
				continue
			}
			node.SetPosition(pos)
		}
	}
}

func (world *Client) finishLoading() {
	if !world.loading {
		return
	}
	world.loading = false
	// Rebuild every terrain tile touched during the replay, once each, BEFORE we
	// re-enable 3D — so the finished world is shown, not a half-built one.
	if world.TerrainEditor != nil {
		profMark("finishLoading: flushing bulk terrain reloads")
		world.TerrainEditor.flushBulkReloads()
		// Float Changes that replayed while the heightfield was deferred
		// reconstructed their Y against HeightAt==0; with the terrain now final,
		// re-seat each tracked float to HeightAt(XZ)+delta so it sits at the right
		// height instead of near y=0 (the "lifted props are too high/low on
		// reload" bug).
		world.reseatFloats()
		// Mobile critters/vehicles record an absolute Y, so a cloud scene's
		// non-causal terrain replay (another part's raise landing after the
		// placement) can bury them. Lift any that ended up under the final
		// terrain back to the surface (the "placed scorpions are under the
		// terrain after reload" bug).
		world.reseatMobileEntities()
		// Library-sizing debug mode: settle every sizes.txt override against
		// the final terrain (replay-time applications grounded to HeightAt==0).
		world.applyLibrarySizeOverrides()
	}
	if world.CritterEditor != nil {
		// No-op unless the critter snapshot path buffered the replay (see
		// beginLoading); it logs its own markers when it actually folds.
		world.CritterEditor.flushCritterReplay()
	}
	Viewport.Get(world.AsNode()).SetDisable3d(false)
	if world.loadingOverlay != nil {
		world.loadingOverlay.AsNode().QueueFree()
		world.loadingOverlay = nil
	}
	profMark("finishLoading: splash down, 3D re-enabled — world fully built")
	stopLoadCPUProfile()
	reportLoadProfile(world.loadEnqueued.Load(), world.loadDequeued.Load())
}

// processLoading fast-drains the replay queue under the splash. It applies as
// many queued mutations as fit in loadDrainBudget (decoupling the build from the
// frame rate), refreshes the progress readout, and dismisses the splash once the
// scene is fully built.
func (world *Client) processLoading(dt Float.X) {
	// Advance the timing coordinator so time-driven entities (e.g. the
	// ActionRenderer that replays recorded citizen/critter movement) read a
	// real, advancing client.time.Now() and settle to the right place. Without
	// this, time.Now() stays 0 and the movement interpolation extrapolates to
	// non-finite/astronomical transforms (look_at + is_finite engine errors).
	world.time.Process(dt)
	markMaxQueueDepth(int64(len(world.queue)))
	deadline := time.Now().Add(loadDrainBudget)
	hitBudget := false
	for {
		select {
		case fn := <-world.queue:
			fn()
			world.loadDequeued.Add(1)
			if !time.Now().Before(deadline) {
				hitBudget = true
				goto progress
			}
		default:
			goto progress
		}
	}
progress:
	if loadProfileOn {
		// hitBudget ⇒ we still had queued work at the deadline (apply-bound this
		// frame); else we drained to empty and yielded early (not apply-bound).
		if hitBudget {
			loadFramesBudgetHit.Add(1)
		} else {
			loadFramesQueueEmpty.Add(1)
		}
	}
	world.updateLoadProgress()
	if world.loadingComplete() {
		world.finishLoading()
	}
}

// updateLoadProgress drives the splash readout: a determinate bar for
// single-player loads (bytes decoded, then queued mutations applied) and an
// indeterminate status for joins (state streams in with no known total).
func (world *Client) updateLoadProgress() {
	// Stall guard: remember the last time any counter advanced (independent of
	// the overlay, so loadingComplete's fail-open works even if it didn't load).
	seen := world.loadReadBytes.Load() + world.loadEnqueued.Load() + world.loadDequeued.Load()
	if seen != world.loadLastSeen {
		world.loadLastSeen = seen
		world.loadLastProgress = time.Now()
	}
	if world.loadingOverlay == nil {
		return
	}
	if world.joining {
		world.loadingOverlay.SetIndeterminate()
		return
	}
	total := world.loadTotalBytes.Load()
	if !world.member && total > 0 {
		// Decode/stream phase: how much of the .mus3 log we've read.
		world.loadingOverlay.SetProgress(float64(world.loadReadBytes.Load()) / float64(total))
		return
	}
	// Build phase: the decode finished (member assigned), so loadEnqueued is the
	// final mutation count and dequeued/enqueued is real build progress.
	if enq := world.loadEnqueued.Load(); enq > 0 {
		world.loadingOverlay.SetProgress(float64(world.loadDequeued.Load()) / float64(enq))
	} else {
		world.loadingOverlay.SetProgress(1)
	}
}

// loadingComplete reports whether the initial replay has been fully applied.
func (world *Client) loadingComplete() bool {
	// Fail open if the load wedged (no counter advanced for a long time).
	if time.Since(world.loadLastProgress) > loadStallTimeout {
		return true
	}
	if !world.member || len(world.queue) > 0 {
		world.loadIdleSince = time.Time{}
		return false
	}
	if !world.joining {
		// Single-player: decode finished before the author assignment and the
		// queue has drained — the world is fully built.
		return true
	}
	// Join: the host streams catch-up with no end marker, so require a short
	// continuous-idle grace before assuming the stream has finished.
	if world.loadIdleSince.IsZero() {
		world.loadIdleSince = time.Now()
		return false
	}
	return time.Since(world.loadIdleSince) > joinLoadGrace
}

// Opt-in viewport screenshot for headless verification: when AVIARY_SHOTPATH is
// set, save one PNG of the rendered viewport a short while after loading ends
// (the native Wayland window can't be grabbed by X11 tools / the COSMIC portal).
var (
	shotPath   = os.Getenv("AVIARY_SHOTPATH")
	shotFrames int
	shotDone   bool
)

func (world *Client) maybeCaptureScreenshot() {
	if shotPath == "" || shotDone {
		return
	}
	shotFrames++
	if shotFrames < 90 { // ~1.5s of rendered frames so the world settles
		return
	}
	shotDone = true
	tex := Viewport.Get(world.AsNode()).GetTexture()
	if tex == (ViewportTexture.Instance{}) {
		profMark("screenshot: nil viewport texture")
		return
	}
	img := tex.AsTexture2D().GetImage()
	if img == (Image.Instance{}) {
		profMark("screenshot: nil image")
		return
	}
	if err := img.SavePng(shotPath); err != nil {
		profMark("screenshot save failed: %v", err)
		return
	}
	profMark("screenshot saved to %s", shotPath)
	world.debugResourceUsage()
	if world.TerrainEditor != nil {
		world.TerrainEditor.debugTerrainSignature()
	}
	if world.CritterEditor != nil {
		world.CritterEditor.debugCritterSignature()
	}
}

// debugResourceUsage reports how many imported scenery Designs (packed scenes)
// still have a live entity vs. how many were loaded but have no surviving entity
// (removed or superseded) — i.e. resources we paid to load but that aren't in
// the final scene. Sums the LoadSync time attributed to each bucket.
func (world *Client) debugResourceUsage() {
	if !loadProfileOn {
		return
	}
	liveScenes, deadScenes, deadNeverPlaced := 0, 0, 0
	var liveMs, deadMs, deadNeverPlacedMs float64
	var deadURIs []string
	for design := range world.packed_scenes {
		uri := world.design_to_string[design]
		ms := loadPathMs(boulderCompatPath(uri))
		live := false
		for _, id := range world.design_to_entity[design] {
			if _, ok := id.Instance(); ok {
				live = true
				break
			}
		}
		if live {
			liveScenes++
			liveMs += ms
		} else {
			deadScenes++
			deadMs += ms
			if !debugEverCreated[design] {
				deadNeverPlaced++
				deadNeverPlacedMs += ms
			}
			if len(deadURIs) < 12 {
				deadURIs = append(deadURIs, uri)
			}
		}
	}
	profMark("[res] packed_scenes live=%d (%.0fms) dead=%d (%.0fms; never-placed=%d/%.0fms) textures=%d loaded=%d",
		liveScenes, liveMs, deadScenes, deadMs, deadNeverPlaced, deadNeverPlacedMs, len(world.textures), len(world.loaded))
	for _, u := range deadURIs {
		profMark("[res] dead scene: %s (%.0fms)", u, loadPathMs(boulderCompatPath(u)))
	}
}

const speed = 3

// cameraDefaultZoom is the camera's starting distance from the focal point (the
// Z set in SetPosition below). Pan speed and shadow distance are both normalised
// against it so the world moves — and shadows reach — at the original rate at the
// default zoom, scaling up when pulled out and down when zoomed in.
const cameraDefaultZoom Float.X = 3

// Directional-shadow far distance, scaled with the camera zoom by
// updateShadowDistance. shadowDistanceBase is the reach at the default zoom (the
// value Ready installs); zooming out grows it proportionally, up to
// shadowDistanceMax, so distant ground keeps its shadows instead of fading at a
// fixed radius. A larger atlas-covered area is blurrier, so it is capped rather
// than left to grow without bound.
const shadowDistanceBase = 30 // untyped: float64 for SetParam, Float.X in the zoom math
const shadowDistanceMax = 240

// cameraTerrainClearance is how far above the terrain the camera is held when it
// would otherwise sink into the ground. The camera-terrain collision keeps the
// view from burrowing through hills while still letting it drop below the water
// surface (water is not terrain), so a small clearance keeps it just clear.
const cameraTerrainClearance Float.X = 0.5

func (world *Client) Process(dt Float.X) {
	// Honour an externally-requested clean quit (SIGUSR1 → quitIfRequested) before
	// any per-frame work, so the window-close teardown path runs even mid-load.
	if quitIfRequested(world.AsNode()) {
		return
	}
	// While the world is still replaying, fast-drain the mutation queue under
	// the loading splash (3D rendering is disabled) instead of running the
	// normal per-frame world logic. processLoading still advances the timing
	// coordinator so time-driven entities (ActionRenderer-replayed movement)
	// keep synchronising to their correct positions while we build — we only
	// suppress rendering, not state-building. It dismisses the splash and
	// re-enables rendering once the scene is fully built.
	if world.loading {
		world.processLoading(dt)
		return
	}
	world.time.Process(dt)
	world.maybeCaptureScreenshot()

	// Keep the underwater post-process in sync with the water surface AND terrain
	// floor under the camera: the LOCAL river surface where one is carved (else
	// the global lake level), and the terrain height. The shader shows water only
	// where the floor is below the surface; otherwise (lens inside a hill) it
	// fills with rock, so going below the water plane into the ground reads as
	// being buried rather than underwater.
	if world.TerrainEditor != nil {
		// Glide the water surface toward a changed level (ticks every frame, so a
		// remote/undo change animates even outside the terrain editor).
		world.TerrainEditor.processWaterRise(dt)
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

	// Slide the cloud-layer FogVolume to stay centred on the camera in XZ so the
	// finite box feels like an endless layer as the player pans (see clouds
	// .FollowCamera; the clouds are sampled in world space, so they stay put while
	// the box slides beneath them).
	if cam := Viewport.Get(world.AsNode()).GetCamera3d(); cam != Camera3D.Nil {
		world.clouds.FollowCamera(cam)
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
		world.TerrainEditor.sculptStroke = false // unlock plateau height for the next stroke
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

	// Plateau/smooth are likewise drag-paint: each throttled segment commits one
	// flatten/smooth Sculpt (PaintTerrainSculpt carries its own movement-spacing
	// guard and locks the plateau level at the stroke's start).
	if world.TerrainEditor.specialTerrainBrushActive() && world.TerrainEditor.brushStrokeActive {
		if time.Since(world.last_PaintAt) > time.Second/5 {
			world.TerrainEditor.PaintTerrainSculpt()
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
	// Scale pan speed by how far the camera is zoomed out so a keypress always
	// covers a similar fraction of the screen: a gentle creep when zoomed right
	// in, a brisk sweep when pulled far out. The camera's local Z is its distance
	// from the focal point; clamp the floor so movement never stalls at extreme
	// zoom-in.
	zoom := max(world.FocalPoint.Lens.Camera.AsNode3D().Position().Z, cameraDefaultZoom/4)
	moveSpeed := speed * dt * zoom / cameraDefaultZoom
	// Grow the shadow reach with the same zoom so shadows don't fade out from
	// under the far ground when the camera pulls back.
	world.updateShadowDistance(zoom)
	if Input.IsKeyPressed(Input.KeyA) || Input.IsKeyPressed(Input.KeyLeft) {
		world.FocalPoint.AsNode3D().Translate(Vector3.New(-moveSpeed, 0, 0))
	}
	if Input.IsKeyPressed(Input.KeyD) || Input.IsKeyPressed(Input.KeyRight) {
		world.FocalPoint.AsNode3D().Translate(Vector3.New(moveSpeed, 0, 0))
	}
	if Input.IsKeyPressed(Input.KeyS) || Input.IsKeyPressed(Input.KeyDown) {
		world.FocalPoint.AsNode3D().Translate(Vector3.New(0, 0, moveSpeed))
	}
	if Input.IsKeyPressed(Input.KeyW) || Input.IsKeyPressed(Input.KeyUp) {
		world.FocalPoint.AsNode3D().Translate(Vector3.New(0, 0, -moveSpeed))
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
									// "Walk here" is only for the mobile dressing entities
									// (critters, citizens, vehicles); static scenery stays
									// where it's placed. Gate on the placed design's library
									// category so a selected rock, fence, or building can't
									// be dragged across the terrain by right-clicking.
									design, placed := world.findDesignForObject(node3d.ID())
									if !placed || !isMobileDesignCategory(designCategory(world.design_to_string[design])) {
										break
									}
									// Plain right-click: walk straight to the point (replace any
									// path). Shift: append a segment onto the end of the current
									// path. Ctrl: append AND make the whole path a back-and-forth
									// loop. Shift/Ctrl chain from where the path currently ends
									// (and when it ends) so segments join seamlessly.
									shift := Input.IsKeyPressed(Input.KeyShift)
									ctrl := Input.IsKeyPressed(Input.KeyCtrl)
									startPos := node3d.Position()
									startTime := world.time.Future()
									hasPath := false
									if shift || ctrl {
										if ar, ok := actionRendererFor(node3d); ok {
											if tail, end, active := ar.PathTail(); active {
												// Chain the new segment onto the end of the current
												// path (where, and when, it finishes).
												hasPath = true
												startPos = tail
												if end > startTime {
													startTime = end
												}
											}
										}
									}
									world.space.Action(musical.Action{
										Author: world.id,
										Entity: entity,
										Target: intersect.Position,
										Period: musical.Period(Vector3.Distance(startPos, intersect.Position) * Float.X(time.Second) * 5),
										Timing: startTime,
										// Append (Cancel=false) only when there's a path to extend;
										// a modifier-click with no active path is a fresh walk that
										// advances the move high-water like a plain click. Ctrl marks
										// the path a back-and-forth loop.
										Cancel: !hasPath,
										Repeat: ctrl,
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
		// F2 (debug): with AVIARY_LIBRARY_SIZES set, record the selected
		// entity's current in-world height into the library's sizes.txt so
		// rescale_glb.py can bake it into the .glb as the default size.
		if event.AsInputEvent().IsPressed() && event.Keycode() == Input.KeyF2 && !event.AsInputEvent().IsEcho() {
			world.debugPersistSelectionSize()
		}
		if event.AsInputEvent().IsPressed() && event.Keycode() == Input.KeyS && Input.IsKeyPressed(Input.KeyCtrl) && !event.AsInputEvent().IsEcho() {
			AnimateTheSceneBeingSaved(world, world.record)
			go func() {
				name := base64.RawURLEncoding.EncodeToString(world.record[:])
				file, err := os.Open(UserDataDir + "/snaps/" + name + ".png")
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
	select {
	case nf.Client.Send <- data:
		return nil
	case <-nf.Client.Done:
		return fmt.Errorf("connection closed")
	}
}

func (nf networkingFor) Recv() ([]byte, error) {
	select {
	case data := <-nf.Client.Recv:
		return data, nil
	case <-nf.Client.Done:
		return nil, fmt.Errorf("connection closed")
	}
}

// Close is a no-op: the networking layer owns the peer's lifetime and closes
// Client.Done on disconnect, which is what unblocks the send/recv goroutines.
// Closing Client.Send here would race with concurrent broadcast Sends.
func (nf networkingFor) Close() error {
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
	select {
	case data, ok := <-nv.updates:
		if !ok {
			return nil, fmt.Errorf("connection closed")
		}
		return data, nil
	case <-nv.network.Done():
		return nil, fmt.Errorf("connection closed")
	}
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
	// The sun's height above the horizon (sun_dir.y), recovered from the light
	// pitch: the light points opposite the sun, so forward.y = sin(elevation) and
	// the sun height is its negation. Drives the day/night fade for BOTH the
	// directional warmth and the ambient below, using the SAME smoothstep the sky
	// shader uses for its day blend so sky and ground always agree.
	sunHeight := -math.Sin(float64(elevation))
	day := smoothstep(-0.12, 0.18, sunHeight)
	// The directional light IS the sun: below the horizon there is no direct
	// sunlight, so fade its energy to zero across a short band just under the
	// horizon. Without this the light lingered through the night — dim (the 0.15
	// energy floor in mapTimeOfDaySunAngleFog), pointing UP (the sun pitch flips
	// past the horizon so the light tilts skyward) and pinned to the full golden-
	// hour orange (`low` below saturates at 1), washing the scene's undersides in
	// a stale sunset tint. Zeroing the energy leaves night lit by the cool ambient
	// alone. The node stays ENABLED (energy 0, NOT hidden) so the sky shader keeps
	// a valid LIGHT0_DIRECTION for its own day/night blend; hiding it would default
	// sun_dir to straight up and the sky would read as full daylight.
	horizonGate := smoothstep(-0.1, 0.0, sunHeight)
	if world.Light != DirectionalLight3D.Nil {
		// elevation is the sun's pitch in radians (0 at the horizon, negative
		// tilts the light down from overhead) and azimuth is its compass
		// orientation. A directional light points along its local -Z, so its
		// direction is fully described by pitch (X) + yaw (Y).
		world.Light.AsNode3D().SetRotation(Euler.Radians{
			X: Angle.Radians(elevation),
			Y: Angle.Radians(azimuth),
			Z: 0,
		})
		world.Light.AsLight3D().SetLightEnergy(energy * Float.X(horizonGate))
		// Golden hour: warm the sunlight toward orange while the sun is low (and
		// through twilight), neutral white when it is high. This is what gives
		// sunrise/sunset its glow — and it also tints the sky's sun disk and the
		// sunlit clouds, since they read LIGHT0_COLOR. `low` is 1 at/below the
		// horizon, easing to 0 once the sun has climbed a bit.
		low := 1 - smoothstep(0.04, 0.32, sunHeight)
		world.Light.AsLight3D().SetLightColor(Color.RGBA{
			R: 1.0,
			G: float32(1.0 - 0.40*low),
			B: float32(1.0 - 0.62*low),
			A: 1,
		})
	}
	// Feed the sun direction to the cloud system, which pushes it to the terrain
	// cloud-shadow term (terrain.gdshader). Direction TOWARD the sun in world
	// space, matching the sky shader's sun_dir: the light points along its local
	// -Z, the sun is opposite. With Euler YXZ (X=elevation, Y=azimuth) the
	// toward-sun vector is the following — its .y is -sin(elevation) = the
	// sunHeight used above, so day/night gating agrees. (If shadows ever offset
	// toward the wrong compass direction, flip the sign on the X/Z components —
	// only the horizontal direction is convention-sensitive.) The angle convention
	// stays here; the clouds package takes the finished vector.
	el, az := float64(elevation), float64(azimuth)
	world.clouds.SetSunDirection(Vector3.New(
		Float.X(math.Sin(az)*math.Cos(el)),
		Float.X(-math.Sin(el)),
		Float.X(math.Cos(az)*math.Cos(el)),
	))
	if world.Environment != Environment.Nil {
		world.Environment.SetFogEnabled(fogDensity > 0.0001)
		world.Environment.SetFogDensity(fogDensity)
		world.Environment.SetFogSunScatter(0.25)
		// Ambient must track the day/night cycle. It used to be pinned at 0.5
		// (white) no matter the time of day, so the ground stayed fully lit at
		// night — reading as "the environment is the wrong way around from the
		// scene". On the flat-normal terrain top the ambient is the only light
		// once the sun is below the horizon, so fading it with the sun is what
		// makes night look like night.
		amb := 0.1 + day*0.4 // dim moonlight (~0.1) -> full daylight (0.5)
		if fogDensity > 0.008 {
			amb *= 0.7 // keep the old heavy-fog softening
		}
		// Cool blue night -> neutral white day.
		world.Environment.SetAmbientLightColor(Color.RGBA{
			R: float32(0.55 + day*0.45),
			G: float32(0.62 + day*0.38),
			B: float32(0.90 + day*0.10),
			A: 1,
		})
		world.Environment.SetAmbientLightEnergy(Float.X(amb))
	}
}

// smoothstep is the GLSL smoothstep: 0 below edge0, 1 above edge1, with a smooth
// Hermite ramp in between. Used to keep the Go-side lighting fades identical to
// the shaders'.
func smoothstep(edge0, edge1, x float64) float64 {
	if edge1 == edge0 {
		if x < edge0 {
			return 0
		}
		return 1
	}
	t := (x - edge0) / (edge1 - edge0)
	if t < 0 {
		t = 0
	}
	if t > 1 {
		t = 1
	}
	return t * t * (3 - 2*t)
}

// applyLightingStateFromSlider is called by the lighting menu for immediate
// local feedback (and for legacy technical slider names from saves).
// We have removed support for the old technical names ("azimuth", "elevation", "energy")
// since no public release has shipped yet.
func (world *Client) applyLightingStateFromSlider(slider string, value Float.X) {
	switch slider {
	case "environment/time_of_day":
		_, angle, fog, clouds, rain, snow, wind, moon := world.GetLightingMenuState()
		world.ApplyLightingMenuState(value, angle, fog, clouds, rain, snow, wind, moon)

	case "environment/sun_angle":
		tod, _, fog, clouds, rain, snow, wind, moon := world.GetLightingMenuState()
		world.ApplyLightingMenuState(tod, value, fog, clouds, rain, snow, wind, moon)

	case "environment/fog":
		tod, angle, _, clouds, rain, snow, wind, moon := world.GetLightingMenuState()
		world.ApplyLightingMenuState(tod, angle, value, clouds, rain, snow, wind, moon)

	case "environment/clouds":
		tod, angle, fog, _, rain, snow, wind, moon := world.GetLightingMenuState()
		world.ApplyLightingMenuState(tod, angle, fog, value, rain, snow, wind, moon)

	case "environment/rain":
		tod, angle, fog, clouds, _, snow, wind, moon := world.GetLightingMenuState()
		world.ApplyLightingMenuState(tod, angle, fog, clouds, value, snow, wind, moon)

	case "environment/snow":
		tod, angle, fog, clouds, rain, _, wind, moon := world.GetLightingMenuState()
		world.ApplyLightingMenuState(tod, angle, fog, clouds, rain, value, wind, moon)

	case "environment/wind":
		tod, angle, fog, clouds, rain, snow, _, moon := world.GetLightingMenuState()
		world.ApplyLightingMenuState(tod, angle, fog, clouds, rain, snow, value, moon)

	case "environment/moon":
		tod, angle, fog, clouds, rain, snow, wind, _ := world.GetLightingMenuState()
		world.ApplyLightingMenuState(tod, angle, fog, clouds, rain, snow, wind, value)

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
func (world *Client) ApplyLightingMenuState(timeOfDay, sunAngle, fog, clouds, rain, snow, wind, moon Float.X) {
	world.lightingMenuState.timeOfDay = timeOfDay
	world.lightingMenuState.sunAngle = sunAngle
	world.lightingMenuState.fog = fog
	world.lightingMenuState.clouds = clouds
	world.lightingMenuState.rain = rain
	world.lightingMenuState.snow = snow
	world.lightingMenuState.wind = wind
	world.lightingMenuState.moon = moon

	az, el, en, fd := world.mapTimeOfDaySunAngleFog(timeOfDay, sunAngle, fog)
	world.applyLightingState(az, el, en, fd)
	world.applyMoonState(az, el, moon)
	world.applyCloudDensity(clouds)
	world.applyWeather(rain, snow, wind)
}

// applyMoonState positions and lights the moon for the given sun azimuth/
// elevation (radians) and moon phase (0 = new, 0.5 = half, 1 = full). The moon
// sits opposite the sun, so it climbs as the sun sets; its directional light
// fades in across the horizon (the inverse of the sun's gate in
// applyLightingState) and scales with phase, and the same phase is pushed to the
// sky shader, which carves the visible crescent. Kept separate from
// applyLightingState (which owns the sun) so the two lights stay independent.
func (world *Client) applyMoonState(sunAzimuth, sunElevation, phase Float.X) {
	// sunHeight is the sun's height above the horizon (see applyLightingState);
	// the moon is lit only while the sun is below it. moonGate is the exact
	// complement of the sun's horizonGate, so sun and moon cross-fade at dusk/dawn.
	sunHeight := -math.Sin(float64(sunElevation))
	moonGate := smoothstep(0.0, -0.1, sunHeight) // 1 deep night, 0 by daybreak
	if world.Moon != DirectionalLight3D.Nil {
		// Point the moon light opposite the sun: negate the sun's pitch and swing
		// the yaw 180°, so its forward vector is exactly -(sun forward) and the
		// light arrives from the moon's side of the sky.
		world.Moon.AsNode3D().SetRotation(Euler.Radians{
			X: Angle.Radians(-sunElevation),
			Y: Angle.Radians(sunAzimuth + math.Pi),
			Z: 0,
		})
		// Dim, cool moonlight. Full moon (phase 1) at midnight peaks at moonPeak;
		// a new moon (phase 0) casts nothing. Hidden during the day so its shadow
		// map isn't rendered for a light that contributes nothing.
		const moonPeak = 0.3
		energy := Float.X(moonPeak) * phase * Float.X(moonGate)
		world.Moon.AsLight3D().SetLightColor(Color.RGBA{R: 0.60, G: 0.72, B: 1.0, A: 1})
		world.Moon.AsLight3D().SetLightEnergy(energy)
		world.Moon.AsNode3D().SetVisible(moonGate > 0.001)
	}
	// Hand the phase to the sky so it can carve the crescent on the moon disk.
	world.clouds.SetMoonPhase(phase)
}

// applyCloudDensity pushes the cloud coverage (0 = clear, 1 = overcast) into the
// cloud system, which drives whichever renderer the active Mode is showing plus
// the terrain cloud-shadow density. Safe before the system exists.
func (world *Client) applyCloudDensity(clouds Float.X) {
	world.clouds.SetDensity(clouds)
}

// applyCloudQuality switches the cloud renderer to match the graphics-quality
// tier (see GraphicsQuality.cloudMode for the policy mapping). Safe before the
// cloud system exists.
func (world *Client) applyCloudQuality(q GraphicsQuality) {
	world.clouds.SetMode(q.cloudMode())
}

// applyWaterQuality matches the water to the graphics-quality tier in two ways.
// First it binds the right Shader onto the shared water material: the lowest tier
// (GraphicsQuality.simpleWater) gets water_simple.gdshader — a flat blue
// transparent surface with basic scrolling normals and none of the per-pixel
// depth/screen fetches, foam, swell, or reflections — while every higher tier
// gets the full water.gdshader. Both shaders share the geometry contract and
// brush-preview uniforms, so the swap is invisible to the rest of the water code.
// Second it pushes reflection_strength: the water is transparent (blend_mix), so
// Godot's Environment SSR can't reach it and the full shader ray-marches its own
// reflections, gated on this uniform — off below the Highest tier (and inert on
// the simple shader), where the per-pixel depth march would cost too much. Kept
// on the Client alongside applyCloudQuality since it reaches into the
// TerrainEditor's material; safe before that material exists (the guard no-ops
// during the launch window). The water meshes all share this one material, so a
// single SetShader rebinds every tile at once.
func (world *Client) applyWaterQuality(q GraphicsQuality) {
	te := world.TerrainEditor
	if te == nil || te.water_shader == (ShaderMaterial.Instance{}) {
		return
	}
	shader := te.waterShaderFull
	if q.simpleWater() {
		shader = te.waterShaderSimple
	}
	if shader != (Shader.Instance{}) {
		te.water_shader.SetShader(shader)
	}
	te.water_shader.SetShaderParameter("reflection_strength", q.reflectionStrength())
}

// applyShadowQuality pushes the per-tier directional-shadow settings (whether
// the lights cast shadows at all — see GraphicsQuality.shadowsEnabled — plus the
// depth + normal bias — see GraphicsQuality.shadowBias) into both the sun and
// moon lights. It is split out from light creation because all three depend on
// the shadow atlas resolution, which is quality-dependent: Toaster casts no
// shadows, and the small low-tier atlas spreads each texel over more world
// space, so it needs a larger (mostly normal) bias to suppress the self-shadow
// banding / peter-panning that the dense high-tier atlas never shows. Both
// lights share one atlas, so both get the same settings.
func (world *Client) applyShadowQuality(q GraphicsQuality) {
	enabled := q.shadowsEnabled()
	bias, normalBias := q.shadowBias()
	for _, light := range []DirectionalLight3D.Instance{world.Light, world.Moon} {
		l := light.AsLight3D()
		l.SetShadowEnabled(enabled)
		l.SetShadowBias(Float.X(bias))
		l.SetShadowNormalBias(Float.X(normalBias))
	}
}

// updateShadowDistance scales the directional-shadow far distance with the
// camera zoom so shadows reach as far as the view does. At the default zoom it
// holds shadowDistanceBase (Ready's value); pulling the camera out grows it in
// proportion, capped at shadowDistanceMax. The atlas size is fixed per quality
// tier, so a larger distance trades sharpness for reach — acceptable next to
// shadows visibly fading out from under distant ground. Both lights share one
// atlas, so both get the same distance; the cached last value keeps this to a
// no-op (no cgo SetParam) on the common frame where the zoom hasn't moved.
func (world *Client) updateShadowDistance(zoom Float.X) {
	dist := min(max(shadowDistanceBase*zoom/cameraDefaultZoom, shadowDistanceBase), shadowDistanceMax)
	if dist == world.shadowMaxDistance {
		return
	}
	world.shadowMaxDistance = dist
	for _, light := range []DirectionalLight3D.Instance{world.Light, world.Moon} {
		if light == DirectionalLight3D.Nil {
			continue
		}
		Light3D.Advanced(light.AsLight3D()).SetParam(Light3D.ParamShadowMaxDistance, float64(dist))
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
func (world *Client) GetLightingMenuState() (timeOfDay, sunAngle, fog, clouds, rain, snow, wind, moon Float.X) {
	s := world.lightingMenuState
	return s.timeOfDay, s.sunAngle, s.fog, s.clouds, s.rain, s.snow, s.wind, s.moon
}

// mapTimeOfDaySunAngleFog converts the friendly, artist-friendly slider values
// used in the lighting menu into the low-level technical parameters that
// actually drive the light and environment.
func (world *Client) mapTimeOfDaySunAngleFog(timeOfDay, sunAngle, fog Float.X) (az, el, energy, fogDensity Float.X) {
	// timeOfDay: 0 = midnight, 0.25 = sunrise, 0.5 = noon, 0.75 = sunset, 1 = midnight.
	phase := (float64(timeOfDay) - 0.25) * 2 * math.Pi

	// The sun travels a real arc across the sky — rising on one side, passing
	// high overhead, and setting on the OPPOSITE side — instead of going up and
	// back down the same side (which is what a fixed-azimuth + pitch-only mapping
	// gave). Model the sun's direction as a point sweeping a tilted great circle:
	//   A = the sunrise point on the horizon (compass bearing = Sun Angle),
	//   B = the noon point, 90° around from A and lifted to maxEl elevation.
	// sun = cos(phase)·A + sin(phase)·B, so it sits at A at sunrise, B at noon,
	// −A (the opposite horizon) at sunset and −B (below ground) at midnight.
	// maxEl < 90° keeps it off the exact zenith for a natural, slightly tilted arc.
	const maxEl = 72.0 * math.Pi / 180.0
	azr := float64(sunAngle) * 2 * math.Pi // compass bearing the sun rises from
	ax, az0, azz := math.Sin(azr), 0.0, math.Cos(azr)
	bx := math.Cos(maxEl) * math.Sin(azr+math.Pi/2)
	by := math.Sin(maxEl)
	bz := math.Cos(maxEl) * math.Cos(azr+math.Pi/2)
	cp, sp := math.Cos(phase), math.Sin(phase)
	sunX := cp*ax + sp*bx
	sunY := cp*az0 + sp*by
	sunZ := cp*azz + sp*bz

	// The directional light points the way the light travels — opposite the sun.
	// Recover its pitch/yaw Euler (order YXZ, as Node3D uses) from that forward
	// vector: forward = (−sin(yaw)·cos(pitch), sin(pitch), −cos(yaw)·cos(pitch)),
	// so pitch = asin(forward.y) and yaw = atan2(−forward.x, −forward.z).
	fx, fy, fz := -sunX, -sunY, -sunZ
	el = Float.X(math.Asin(math.Max(-1, math.Min(1, fy))))
	az = Float.X(math.Atan2(-fx, -fz))

	// Energy: stays strong while the sun is above the horizon and rolls off
	// through twilight to a dim — not pitch-black — night. Driven by the sun's
	// actual height (sunY) through the SAME smoothstep the ambient + sky-shader
	// day blend use, so a low-but-visible sun still lights the scene (the
	// sunrise/sunset glow) instead of cutting to night the instant it grazes the
	// horizon. Peak ~1.35 keeps noon clouds and water highlights from blowing out.
	energy = Float.X(0.15 + 1.2*smoothstep(-0.12, 0.18, sunY))

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
		// Flatten preview (plateau/smooth): mode 1 pulls each blade toward
		// grass_brush_target_y by grass_brush_height*(1−d²/r²) instead of an
		// additive lift, so grass rides the flatten the same way it rides a bump.
		RenderingServer.GlobalShaderParameterAdd("grass_brush_mode", RenderingServer.GlobalVarTypeFloat, float64(0.0))
		RenderingServer.GlobalShaderParameterAdd("grass_brush_target_y", RenderingServer.GlobalVarTypeFloat, float64(0.0))
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

	// Drift every cloud system (and the terrain cloud-shadow term) with the wind.
	world.clouds.SetWind(wind)

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
	// (The cloud-shadow drift moved into world.clouds.SetWind above.)

	// Ocean swell scales with wind: no wind ⇒ glassy water (wave_height 0), up
	// to heavy hurricane swell at the top. The shader fades these long swells
	// out in shallow water, so this only heaves the open sea.
	if world.TerrainEditor != nil && world.TerrainEditor.water_shader != (ShaderMaterial.Instance{}) {
		world.TerrainEditor.water_shader.SetShaderParameter("wave_height", wf*2.0)
	}
}
