package internal

import (
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"graphics.gd/classdb/ArrayMesh"
	"graphics.gd/classdb/BaseMaterial3D"
	"graphics.gd/classdb/Camera3D"
	"graphics.gd/classdb/CollisionShape3D"
	"graphics.gd/classdb/Engine"
	"graphics.gd/classdb/Input"
	"graphics.gd/classdb/InputEvent"
	"graphics.gd/classdb/InputEventKey"
	"graphics.gd/classdb/InputEventMouseButton"
	"graphics.gd/classdb/MeshInstance3D"
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/PackedScene"
	"graphics.gd/classdb/PhysicsDirectSpaceState3D"
	"graphics.gd/classdb/PhysicsRayQueryParameters3D"
	"graphics.gd/classdb/SphereMesh"
	"graphics.gd/classdb/SphereShape3D"
	"graphics.gd/classdb/StandardMaterial3D"
	"graphics.gd/classdb/StaticBody3D"
	"graphics.gd/classdb/Viewport"
	"graphics.gd/variant/Angle"
	"graphics.gd/variant/Basis"
	"graphics.gd/variant/Color"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Vector2"
	"graphics.gd/variant/Vector3"

	"the.quetzal.community/aviary/internal/critter"
	"the.quetzal.community/aviary/internal/musical"
)

type CritterEditor struct {
	Node3D.Extension[CritterEditor]
	musical.Stubbed

	Preview       PreviewRenderer
	MirrorPreview PreviewRenderer

	client *Client

	loadOnce sync.Once
	loadErr  error
	body     CritterBody

	// ground is the Kenney circular base mesh (res://base.obj) we
	// drop under the critter so the user can see "which way is
	// forward". Stored by ID — not Instance — so a stale handle
	// (after a QueueFree from elsewhere, scene reload, etc) doesn't
	// segfault when the ribcage view tries to toggle visibility;
	// the lookup just returns ok=false.
	ground Node3D.ID

	last_slider_sculpt time.Time

	entityToPart map[musical.Entity]Node3D.ID
	partToEntity map[Node3D.ID]musical.Entity

	// pendingChanges holds Change messages that arrived before their
	// referenced Design's PackedScene was imported (typical during
	// history replay or a peer-join, where Imports can be racing
	// Changes through the queue). Each Process tick retries them
	// against the current packed_scenes map; once the scene lands,
	// the muzzle is instantiated and the entry drops out.
	pendingChanges []musical.Change

	jawCache map[Node3D.ID]*jawState

	idleTime    float32
	breatheTime float32
	lastEditAt  time.Time
	idlePhase   float32

	// lastMousePx + lastMouseMoveAt latch when the mouse last moved
	// so eye HintFocus is only flagged valid while the cursor is
	// "live" — eyes never start a tracking burst on a still mouse.
	lastMousePx     Vector2.XY
	lastMouseMoveAt time.Time

	// idleHeadLook drives the occasional "look left / right" neck
	// rotation on the spine's head-end bones via
	// CritterBody.SetHeadLookYaw. Ticks in Process and applies in
	// every view EXCEPT ribcage / limbone (where neck motion would
	// fight the user's editing) and during active drags. Shared
	// across views so the schedule + RNG state stay coherent when
	// the user flips between control and explore.
	idleHeadLook *headLookState

	// Spine view state. `spineEdit` flips true when the user
	// switches into the spine view, surfacing draggable bone
	// handles and grow/shrink nubs. `dragging` carries the drag
	// target between mouse-down and mouse-up frames.
	spineEdit bool
	spineRig  *spineRig
	dragging  spineDrag

	// dragEmits coalesces a bone/leg drag into ONE committed Sculpt
	// per touched slider, recorded at mouse-release instead of every
	// frame. A drag runs in PhysicsProcess at 60-120fps and (with
	// radial propagation) emits 2×N sculpts per frame — historically
	// the dominant load cost (tens of thousands of redundant
	// intermediate bone/weight sculpts in the history). During the
	// drag the edit is applied LOCALLY for live feedback but not
	// recorded; flushDragEmits emits each slider's final value once on
	// release. The committed value equals the last local value
	// (absolute set), so peers + reloads converge on the same pose.
	// dragEmitOrder keeps first-touch order so the flush is
	// deterministic (the values are commutative absolute sets, but a
	// stable order keeps signatures reproducible).
	dragEmits     map[string]float32
	dragEmitOrder []string

	// bulkReplay is set while the client replays the .mus3 log at load
	// (see Client.beginLoading / flushCritterReplay). While set, body-
	// shape sculpts (bone/leg/weight) are appended to replayBuffer
	// instead of applied — folding tens of thousands of them one queued
	// closure at a time (each a strings.Split + Bones() copy +
	// SetBoneAxis) was the dominant load cost. flushCritterReplay folds
	// the buffer once at the end, or restores a cached snapshot and skips
	// the fold entirely. Environment/lighting sculpts are NOT buffered —
	// they stay on the live path so the snapshot only concerns body shape.
	bulkReplay   bool
	replayBuffer []musical.Sculpt

	// ribcage is the side-view, xray diagram shared by the body-
	// editing view ("ribcage") and the limb-editing view
	// ("limbone"). Non-nil iff either view is active; ribcageExit
	// nils it back out on the way out. The two views share the
	// camera lock, body darken and ImmediateMesh container but
	// differ in what they draw and which handles respond to clicks
	// — see `view` below for the active branch.
	ribcage *ribcageVis

	// view is the current ViewSelector mode. "ribcage" surfaces the
	// spine + rib visualization and body-bone handles; "limbone"
	// surfaces only the limb (leg) handles and hides the spine bones
	// so the two editor passes don't overlay each other. Empty (or
	// "explore") means the normal world view with no overlay.
	view string

	// Procedural-leg placement state. Clicking the foreleg/forearm
	// builtin tile sets placingLeg to the tile name ("foreleg" or
	// "forearm"); a ghost MeshInstance3D mirrors the would-be leg at
	// the body-raycast hit until the user commits with a click or
	// cancels with right-click. Acts as a stand-in for the imported
	// PackedScene preview used by other parts (we don't have a .glb
	// to load, so we build the ghost geometry procedurally).
	placingLeg        string
	legGhost          MeshInstance3D.Instance
	legGhostArrayMesh ArrayMesh.Instance

	// Stepper placement state: when the user picks a design from
	// the "stepper" tab (designs whose URI contains "/stepper/"),
	// `placingStepper` flips true and the placement preview snaps
	// to whichever leg foot the cursor is closest to on screen,
	// rather than to the body-surface raycast. The current target
	// (data leg index + which mirrored side) lives in stepperLeg /
	// stepperSide so PhysicsProcess can pose the preview and
	// UnhandledInput can commit the same anchor on click.
	placingStepper bool
	stepperLeg     int
	stepperSide    int

	// animatedParts collects every procedurally-built body part that
	// wants a per-frame Process tick (eyes today, future
	// idle-driven parts later). Keyed by the part's root Node3D ID
	// so the Change Remove handler can drop the entry the moment
	// the Node3D is freed — without this the next Process tick
	// dereferences a stale Godot object handle and segfaults.
	animatedParts map[Node3D.ID]proceduralPart

	// control carries the saved camera + body transform snapshot for
	// the "control" view (the WASD chase cam). Non-nil iff the view
	// is active; controlExit nils it back out on the way out. See
	// editor_critter_control.go for the rest of the implementation.
	control *controlVis

	lighting // private lighting for this editor
}

// proceduralPart is the per-frame contract for any procedurally-
// built body part that needs to animate itself. Implementations
// own all their own state; the editor only drives the tick.
type proceduralPart interface {
	Process(delta float32)
}

// spineDrag carries the in-progress drag interaction across frames.
// Kind picks bone-move vs radius-resize; targetBone is the bone
// being affected; offset is the world-space delta between the
// initial mouse projection and the bone's position, so the drag
// doesn't snap when you grab a handle slightly off-centre.
//
// startMouse + active enforce a dead-zone: the drag only starts
// affecting the bone once the cursor has moved at least
// dragActivatePixels from the click point, so a click that
// doesn't intend to drag doesn't accidentally nudge the bone.
type spineDrag struct {
	kind dragKind
	bone int
	// offset is the world-space (bone − initialHit) for bone drags
	// so the bone keeps its relative grab position under the mouse.
	offset Vector3.XYZ
	// startMouse is in pixels — drives the dead-zone before a drag
	// actually starts emitting.
	startMouse Vector2.XY
	// startHit + startRadius are captured at click time so the drag
	// computes radius as (start + delta-from-start) each frame
	// rather than (current + delta) — without that, the radius
	// accumulated every frame and ran away from the mouse.
	startHit    Vector3.XYZ
	startRadius float32
	// startBones snapshots every bone's body-local position at the
	// moment a bone-move drag began. The drag uses these to
	// compute a stable delta and to propagate the same delta to
	// all bones on the same radial side of the chain centre — so
	// pulling one bone outward stretches that whole "fin" of the
	// chain instead of bending the single bone.
	startBones []critter.Vec3

	// legIdx / legJoint identify the joint a dragLeg drag is moving.
	// startLegPos snapshots the joint's body-local position at click
	// time so each frame's emitted Y/Z is computed from a stable
	// anchor rather than accumulating per-frame deltas.
	legIdx      int
	legJoint    critter.LegJoint
	startLegPos critter.Vec3

	active bool
}

type dragKind int

const (
	dragNone dragKind = iota
	dragBone
	dragRadius
	// dragLeg is a leg-joint drag in the ribcage view: the cursor
	// moves one of {Hip, Knee, Foot} on leg `legIdx` to track the
	// projected mouse position in the X=0 plane.
	dragLeg
	// dragLegRadius resizes one joint's radius by tracking the
	// cursor's distance from that joint in the X=0 plane.
	dragLegRadius
)

// spineRig holds the editor-only visual scaffolding for the spine
// view: one move-handle per bone, one radius-handle per bone, plus
// four end nubs to grow/shrink the chain. All children of a single
// container Node3D under the editor so they teardown together
// when leaving the view.
type spineRig struct {
	container   Node3D.Instance
	boneHandles []spineHandle
	growHead    spineHandle
	growTail    spineHandle
	shrinkHead  spineHandle
	shrinkTail  spineHandle
}

// spineHandle is one clickable widget: a tiny visible sphere plus
// a sibling StaticBody3D / CollisionShape3D for the raycast picker.
// Layer 1<<2 (== layer 3) keeps it out of the global selection
// scan (which masks out layer 2) and the body's MousePicker
// (which doesn't care — default mask is all layers).
type spineHandle struct {
	node    Node3D.Instance
	body    StaticBody3D.Instance
	tag     handleTag
	boneIdx int // for boneHandle / radiusHandle, the bone index
}

// handleTag identifies what a handle does so the raycast dispatcher
// can route a click to the right action.
type handleTag int

const (
	tagBone handleTag = iota + 1
	tagRadius
	tagGrowHead
	tagGrowTail
	tagShrinkHead
	tagShrinkTail
)

// jawState caches the per-muzzle data the idle animator needs so
// the only per-frame cost is a basis rotation update.
type jawState struct {
	jaw       Node3D.Instance
	restBasis Basis.XYZ
	phase     float32

	// Per-jaw RNG + scheduling so paired/multi-muzzle critters
	// don't open their mouths in unison. Events are sampled from a
	// small set (idle/twitch/chew/yawn) with weighted random
	// transitions — real animals don't open on a metronome.
	rng            *rand.Rand
	nextEventAt    float32
	eventEndsAt    float32
	eventAmplitude float32
	eventDuration  float32
}

const (
	jawMaxAngle  = float32(0.55)
	jawPeriod    = float32(4.5)
	jawIdleAfter = 1.5

	// Handles live on a collision layer the global selection raycast
	// is configured to ignore (mask = ^(1<<1)) so clicking a handle
	// doesn't poison the world selection. The body collider already
	// uses 1<<1; handles use 1<<2 so the editor's own picker can
	// distinguish "I hit a handle" from "I hit the body".
	spineHandleLayer = uint32(1 << 2)

	// Render scale for handle spheres; small enough not to clutter
	// the body, large enough to grab with the mouse.
	boneHandleRadius   = float32(0.06)
	radiusHandleRadius = float32(0.04)
	growNubRadius      = float32(0.05)

	// Drag dead-zone: the cursor must travel at least this many
	// pixels from the click point before a bone-move / radius-resize
	// drag starts emitting Sculpts. Below the threshold a click is
	// treated as a no-op so the user can probe handles without
	// accidentally nudging the chain.
	dragActivatePixels = float32(6)
)

// Views advertises the editor's modes to the ViewSelector dropdown:
// explore (place parts), ribcage (spine + rib edit), limbone (leg
// edit), control (WASD-walk preview).
func (*CritterEditor) Views() []string {
	return []string{"explore", "ribcage", "limbone", "control"}
}
func (ce *CritterEditor) SwitchToView(view string) {
	ce.ensureLoaded()
	// Commit any in-progress drag before changing views — the new
	// view tears down the rig the drag was operating on.
	ce.flushDragEmits()
	// Drop any ribcage / control state hanging around from the
	// previous view before we enter the new one. Each enter is
	// idempotent on its own, but exits are NOT — calling
	// ribcageEnter while control is still active would leave the
	// chase-cam state stranded with no way to restore it. Centralise
	// the teardown here so every transition is clean.
	if view != "control" {
		ce.controlExit()
	}
	if view != "ribcage" && view != "limbone" {
		ce.ribcageExit()
	}
	switch view {
	case "ribcage":
		ce.view = view
		ce.spineEdit = true
		ce.refreshSpineRig()
		ce.ribcageEnter()
	case "limbone":
		// Limb-editing view: same xray setup as ribcage but only the
		// limb handles are drawn / pickable. The body's bone handles
		// and rib arcs stay out of the way so leg control points
		// don't overlap with spine ones.
		ce.view = view
		ce.spineEdit = true
		// Tear down any existing bone-handle rig — limb mode hides
		// the body bones so they don't visually conflict with the
		// limb controls.
		if ce.spineRig != nil {
			ce.spineRig.container.AsNode().QueueFree()
			ce.spineRig = nil
		}
		ce.ribcageEnter()
	case "control":
		// WASD chase-cam view: drive the critter around with W/S/A/D
		// while the world camera follows behind. Disables the spine
		// rig + part-placement preview for the duration; controlExit
		// (called by the next SwitchToView) puts the body and camera
		// back where they were on entry.
		ce.view = view
		ce.spineEdit = false
		if ce.spineRig != nil {
			ce.spineRig.container.AsNode().QueueFree()
			ce.spineRig = nil
		}
		ce.clearLegGhost()
		ce.Preview.Remove()
		ce.MirrorPreview.Remove()
		ce.controlEnter()
	default:
		// Everything that isn't "ribcage" / "limbone" / "control" —
		// including the explicit "explore" view and any stray empty
		// string — exits spine edit and tears down handles so part
		// placement clicks can land on the body again.
		ce.view = view
		ce.spineEdit = false
		if ce.spineRig != nil {
			ce.spineRig.container.AsNode().QueueFree()
			ce.spineRig = nil
		}
	}
}

func (*CritterEditor) Name() string { return "critter" }

var _ ClickableEditor = (*CritterEditor)(nil)

func (*CritterEditor) EditorID() string { return "critter" }

// GizmoManipulable implements [ClickableEditor]. Only the default
// ("explore") view permits gizmo manipulation; the ribcage/limbone views
// own their bone-handle drags and control owns the WASD chase-cam, so
// gizmo drags must not fire there.
func (ce *CritterEditor) GizmoManipulable() bool {
	return ce.view == "" || ce.view == "explore"
}

// EntityForNode implements [ClickableEditor]. Critter parts resolve
// through partToEntity; library-imported part scenes carry a StaticBody3D
// at root, so a pick may land on a child — walk up one level.
func (ce *CritterEditor) EntityForNode(node Node3D.Instance) (musical.Entity, Node3D.Instance, bool) {
	if e, has := ce.partToEntity[Node3D.ID(node.ID())]; has {
		return e, node, true
	}
	if parent := node.GetParentNode3d(); parent != Node3D.Nil {
		if e, has := ce.partToEntity[Node3D.ID(parent.ID())]; has {
			return e, parent, true
		}
	}
	return musical.Entity{}, Node3D.Nil, false
}

// DesignForNode implements [ClickableEditor]. Critter parts are placed
// via procedural/encoded designs that don't go through a recoverable
// design map, so duplicate-from-selection isn't supported — returning
// false preserves the prior behaviour where DuplicateSelection bailed
// for critter.
func (ce *CritterEditor) DesignForNode(node Node3D.Instance) (musical.Design, bool) {
	return musical.Design{}, false
}
func (*CritterEditor) Tabs(mode Mode) []string {
	switch mode {
	case ModeGeometry:
		return []string{
			"sensory",
			"muzzles",
			"grabber",
			"forearm",
			"foreleg",
			"stepper",
			"antlers",
			"gliders",
		}
	case ModeDressing:
		return []string{
			"helmets",
			"sunnies",
			"pendant",
			"utensil",
			"daypack",
			"hipwear",
		}
	default:
		return nil
	}
}

func (ce *CritterEditor) EnableEditor() {
	ce.client.SetGizmos(placementGizmos)
	ce.ensureLoaded()
	ce.lighting.resync(ce.client)
}

func (ce *CritterEditor) ChangeEditor() {
	// Commit any in-progress drag before tearing things down so a
	// pending (locally-applied but unrecorded) edit isn't lost when
	// the user leaves mid-drag.
	ce.flushDragEmits()
	// Leaving the editor — tear down the spine rig too. Otherwise the
	// handles linger as invisible-but-pickable colliders in the
	// world.
	if ce.spineRig != nil {
		ce.spineRig.container.AsNode().QueueFree()
		ce.spineRig = nil
	}
	ce.spineEdit = false
	// Restore the body material + world camera if we were sitting in
	// the ribcage view — otherwise the next editor inherits a dark
	// body and a locked camera.
	ce.ribcageExit()
	// Same for the chase-cam: leaving the editor mid-walk should put
	// the critter back at its rest pose, not strand it wherever the
	// user happened to be moving.
	ce.controlExit()
	// Drop any pending leg placement ghost so the next editor doesn't
	// inherit a stray semi-transparent leg following the cursor.
	ce.clearLegGhost()
	// Same for the stepper-snap flag — it's just a placement-mode
	// boolean, no resources to free, but leaving it set would make a
	// future re-enter of this editor think we're mid-stepper-place.
	ce.placingStepper = false
}

func (ce *CritterEditor) ensureLoaded() {
	ce.loadOnce.Do(func() {
		// Ground plate — same Kenney circular base + forward arrow
		// the vehicle editor uses, so the user has a visible
		// reference for which way is "front" of the critter (body
		// +Z points along the arrow). Scaled to half its native
		// size so it doesn't visually dwarf the critter. Loaded
		// first so it lays underneath the body in the scene tree.
		base := LoadSync[PackedScene.Is[Node.Instance]]("res://base.obj")
		baseInst := base.Instantiate()
		ce.AsNode3D().AsNode().AddChild(baseInst)
		if g, ok := Object.As[Node3D.Instance](baseInst); ok {
			g.AsNode3D().SetScale(Vector3.New(0.5, 0.5, 0.5))
			// base.obj's vertices land in body-local Y ∈ [-0.2, -0.1];
			// after the 0.5 scale the plate's TOP surface sits at
			// Y = -0.05. The rest of the editor's math (foot clamp,
			// stepper ground-plant) assumes world Y = 0 IS the
			// ground, so lift the plate by 0.05 to make its top
			// surface line up with that assumption — otherwise
			// every leg foot floats 5 cm above the plate.
			g.AsNode3D().SetPosition(Vector3.New(0, 0.05, 0))
			ce.ground = g.ID()
		}

		mi := MeshInstance3D.New()
		ce.AsNode3D().AsNode().AddChild(mi.AsNode())
		// Float the body a bit above the base plate so the tail bone
		// (the default chain's lowest point) doesn't intersect it.
		mi.AsNode3D().SetPosition(Vector3.New(0, 0.3, 0))
		body, err := AttachCritterBody(mi, critter.New())
		if err != nil {
			Engine.Raise(err)
			return
		}
		ce.body = body
		ce.entityToPart = make(map[musical.Entity]Node3D.ID)
		ce.partToEntity = make(map[Node3D.ID]musical.Entity)
		ce.jawCache = make(map[Node3D.ID]*jawState)
		ce.animatedParts = make(map[Node3D.ID]proceduralPart)
		// Seed the idle head-look scheduler with the current time
		// so multiple critters in the same scene don't synchronise
		// their look events. Process() ticks it each frame.
		ce.idleHeadLook = newHeadLookState(uint64(time.Now().UnixNano()))
	})
}

func (ce *CritterEditor) SelectDesign(mode Mode, design string) {
	// ModeGeometry covers procedural limbs + imported parts (muzzles,
	// antlers, …). ModeDressing covers clothing items (helmets,
	// sunnies, …). Both go through the same hover-preview + click-
	// commit flow — the design's library slot determines how the
	// final part attaches, not the user-facing mode.
	if mode != ModeGeometry && mode != ModeDressing {
		return
	}
	// Builtin procedural designs come through with a sentinel URI
	// instead of a file path. Enter placement mode rather than
	// emitting a leg/grow immediately — the user then hovers the
	// body to preview the attach point and clicks to commit, same
	// shape of interaction as imported parts.
	switch design {
	case BuiltinForelegDesign:
		ce.placingLeg = "foreleg"
		ce.ensureLegGhost()
		return
	case BuiltinForearmDesign:
		ce.placingLeg = "forearm"
		ce.ensureLegGhost()
		return
	case BuiltinEyesDesign:
		// Procedural eye: replace Preview/MirrorPreview's children
		// with a freshly-built eye Node3D and tag the design ref to
		// the sentinel. The existing hover-preview + click-commit
		// flow handles the rest — it doesn't need to know the part
		// is procedural; place() below recognises the sentinel and
		// attaches a fresh eyePart locally instead of going through
		// the PackedScene path. Both previews start hidden so a
		// stale Preview transform (from a previous part placement)
		// doesn't flash an eye at last-cursor's spot before the
		// first hover repositions it.
		ce.setProceduralPreview(&ce.Preview, design, newEyePart(0).Node())
		ce.setProceduralPreview(&ce.MirrorPreview, design, newEyePart(0).Node())
		ce.Preview.AsNode3D().SetVisible(false)
		ce.MirrorPreview.AsNode3D().SetVisible(false)
		return
	}
	if ce.Preview.AsNode().GetChildCount() > 0 {
		Object.To[Node3D.Instance](ce.Preview.AsNode().GetChild(0)).AsNode().QueueFree()
	}
	if ce.MirrorPreview.AsNode().GetChildCount() > 0 {
		Object.To[Node3D.Instance](ce.MirrorPreview.AsNode().GetChild(0)).AsNode().QueueFree()
	}
	ce.Preview.SetDesign(design)
	ce.MirrorPreview.SetDesign(design)
	ce.MirrorPreview.AsNode3D().SetVisible(false)
	// Stepper detection: designs filed under `library/<author>/stepper/`
	// attach to a leg foot rather than the body surface. The preview
	// node is reused (same PackedScene-driven mesh), but PhysicsProcess
	// snaps it to the nearest leg foot on screen instead of running
	// the body raycast, and UnhandledInput commits the click with
	// an OnLeg anchor. Mirror preview is irrelevant in this mode —
	// the stepper rides one specific foot, not both.
	ce.placingStepper = isStepperDesign(design)
	if ce.placingStepper {
		ce.MirrorPreview.AsNode3D().SetVisible(false)
	}
}

// isStepperDesign returns true when a design URI is filed under
// the "stepper" tab of any library author. The path component test
// matches the layout actually used by the design explorer
// (`res://library/<author>/stepper/<name>.glb`) — substring match
// is fine because no other tab name embeds "/stepper/".
func isStepperDesign(design string) bool {
	return strings.Contains(design, "/stepper/")
}

// Sentinel "resource" strings for the critter editor's procedural
// part tiles. The design explorer routes these through SelectDesign
// as if they were library paths; SelectDesign recognises them and
// emits a leg/grow sculpt instead of going through the part-
// placement preview flow.
const (
	BuiltinForelegDesign = "procedural://critter/foreleg"
	BuiltinForearmDesign = "procedural://critter/forearm"
	BuiltinEyesDesign    = "procedural://critter/eyes"
)

// BuiltinDesigns advertises the critter editor's procedural-only
// tiles. Returned by the design explorer's BuiltinDesignProvider
// type-assertion path; library-backed entries continue to come from
// the preview/ directory scan unchanged.
func (ce *CritterEditor) BuiltinDesigns(mode Mode, tab string) []BuiltinDesign {
	if mode != ModeGeometry {
		return nil
	}
	switch tab {
	case "foreleg":
		return []BuiltinDesign{
			{
				Resource: BuiltinForelegDesign,
				Icon:     "res://ui/legwear.svg",
				Label:    "Procedural Leg",
			},
		}
	case "forearm":
		return []BuiltinDesign{
			{
				Resource: BuiltinForearmDesign,
				Icon:     "res://ui/legwear.svg",
				Label:    "Procedural Forearm",
			},
		}
	case "sensory":
		return []BuiltinDesign{
			{
				Resource: BuiltinEyesDesign,
				Icon:     "res://ui/aerials.svg",
				Label:    "Procedural Eyes",
			},
		}
	}
	return nil
}

// SliderConfig + SliderHandle stay only because the Editor
// interface demands them; sliders aren't part of the critter UX
// (the user has moved to direct bone manipulation). Anything that
// still routes through them (e.g. macro sliders the citizen UI
// might expose for compatibility) gets the old behaviour for now.
func (ce *CritterEditor) SliderConfig(mode Mode, editing string) (init, min, max, step float64) {
	for _, s := range critter.Specs() {
		if s.Tab == editing {
			return s.Init, s.Min, s.Max, 0.01
		}
	}
	return 0, 0, 1, 0.01
}

func (ce *CritterEditor) SliderHandle(mode Mode, editing string, value float64, commit bool) {
	if !commit && time.Since(ce.last_slider_sculpt) < time.Second/10 {
		return
	}
	ce.last_slider_sculpt = time.Now()
	ce.lastEditAt = time.Now()
	if ce.client == nil {
		ce.applySlider(editing, Float.X(value))
		return
	}
	ce.client.emitSliderSculpt("critter", editing, value, commit)
}

// Sculpt routes incoming sculpts. Anything starting with "bone/" is
// the bone-editing protocol; "leg/" is the leg-editing protocol.
// Everything else falls through to the legacy macro slider system
// (body shape sliders) so existing scenes replay correctly.
func (ce *CritterEditor) Sculpt(brush musical.Sculpt) error {
	if isEnvironmentSculpt(brush) {
		return nil
	}
	// During the initial bulk replay, buffer body-shape sculpts and fold
	// them once at finishLoading (flushCritterReplay) instead of applying
	// each through the queue. A valid snapshot lets the flush skip the
	// fold entirely. Environment sculpts (handled above) never reach here.
	if ce.bulkReplay {
		ce.replayBuffer = append(ce.replayBuffer, brush)
		return nil
	}
	ce.applyBodySculpt(brush)
	return nil
}

// applyBodySculpt routes one body-shape sculpt to the right apply path.
// Shared by the live Sculpt path and the buffered-replay fold so both
// fold identically. Mirrors the prefix dispatch the editor has always
// used (bone/ → bone op, leg/ → leg op, else macro weight slider).
func (ce *CritterEditor) applyBodySculpt(brush musical.Sculpt) {
	switch {
	case strings.HasPrefix(brush.Slider, "bone/"):
		defer timeIn(&bucketCritterBone)()
		ce.applyBoneSculpt(brush.Slider, float32(brush.Amount))
	case strings.HasPrefix(brush.Slider, "leg/"):
		defer timeIn(&bucketCritterLeg)()
		ce.applyLegSculptBrush(brush)
	default:
		defer timeIn(&bucketCritterSlider)()
		ce.applySlider(brush.Slider, brush.Amount)
	}
}

// flushCritterReplay ends a bulk replay: it folds the buffered body-shape
// sculpts into the critter once (instead of the tens of thousands of
// queued per-sculpt applies that would otherwise dominate load), or — if a
// valid snapshot exists — restores the baked shape and skips the fold
// entirely. Fail-safe: any stroke-set mismatch falls back to the full
// fold, so a stale snapshot can never corrupt the critter. Always writes a
// fresh snapshot so the next load is fast. Called from Client.finishLoading
// before 3D rendering resumes. Gated by AVIARY_SNAPSHOT for the snapshot
// half; the fold-once half always runs (it's a pure speed/equivalence win).
func (ce *CritterEditor) flushCritterReplay() {
	if !ce.bulkReplay {
		return
	}
	ce.bulkReplay = false
	buf := ce.replayBuffer
	ce.replayBuffer = nil
	if len(buf) == 0 {
		return
	}
	ce.ensureLoaded()
	if ce.body.critter == nil {
		return
	}
	// Canonical (Timing, Author) order so the folded shape is independent
	// of the order the .mus3 device parts were concatenated — matches the
	// terrain total-order merge, and makes the snapshot hash reproducible.
	slices.SortStableFunc(buf, func(a, b musical.Sculpt) int {
		return sculptOrder(a, b)
	})
	hash, count := hashCritterBuffer(buf)

	restored := false
	if snapshotEnabled && ce.client != nil {
		if snap, err := readCritterSnapshot(ce.client.record); err == nil {
			if snap.StrokeHash == hash && snap.StrokeCount == count {
				ce.body.RestoreCritter(snap.Bones, snap.Legs, snap.Weights)
				restored = true
				profMark("critter snapshot: restored %d bones %d legs (skipped %d sculpts)",
					len(snap.Bones), len(snap.Legs), count)
			}
		}
	}
	if !restored {
		ce.body.PauseRebuild()
		for i := range buf {
			ce.applyBodySculpt(buf[i])
		}
		ce.body.ResumeRebuild()
	}
	// Leg-anchored parts (steppers) queued during the replay because their
	// leg hadn't been folded yet — now that the legs exist, attach them so
	// they appear in the same frame the splash drops, not one frame later.
	ce.retryPendingChanges()

	if snapshotEnabled && ce.client != nil {
		c := ce.body.Critter()
		snap := &critterSnapshot{
			Version:     critterSnapshotVersion,
			StrokeHash:  hash,
			StrokeCount: count,
			Bones:       c.Bones(),
			Legs:        c.Legs(),
			Weights:     c.Weights(),
		}
		if err := writeCritterSnapshot(ce.client.record, snap); err != nil {
			profMark("critter snapshot: write failed: %v", err)
		} else {
			profMark("critter snapshot: wrote %d bones %d legs %d sculpts",
				len(snap.Bones), len(snap.Legs), count)
		}
	}
}

func (ce *CritterEditor) applySlider(editing string, value Float.X) {
	ce.ensureLoaded()
	if ce.body.critter == nil {
		return
	}
	ce.body.SetWeight(editing, float32(value))
	if ce.spineEdit {
		ce.refreshSpineRig()
	}
}

// applyBoneSculpt decodes the slider name into a bone op and
// applies it via CritterBody. Naming scheme (matches emit*Sculpt
// callers below):
//
//	bone/{i}/x    — set bone i X coord
//	bone/{i}/y    — set bone i Y coord
//	bone/{i}/z    — set bone i Z coord
//	bone/{i}/r    — set bone i body radius
//	bone/grow/head  — extend the chain at the head tip
//	bone/grow/tail  — extend the chain at the tail tip
//	bone/shrink/head — drop the head-tip bone
//	bone/shrink/tail — drop the tail-tip bone
//
// Grow ops use deterministic extrapolation so receivers don't need
// the new bone's coordinates piggy-backed in the sculpt; everyone
// derives the same new bone independently.
func (ce *CritterEditor) applyBoneSculpt(slider string, amount float32) {
	ce.ensureLoaded()
	if ce.body.critter == nil {
		return
	}
	parts := strings.Split(slider, "/")
	if len(parts) < 3 {
		return
	}
	// parts[0] == "bone"
	switch parts[1] {
	case "grow":
		switch parts[2] {
		case "head":
			ce.body.AppendHead()
		case "tail":
			ce.body.AppendTail()
		}
	case "shrink":
		switch parts[2] {
		case "head":
			ce.body.RemoveHead()
		case "tail":
			ce.body.RemoveTail()
		}
	default:
		idx, err := strconv.Atoi(parts[1])
		if err != nil {
			return
		}
		switch parts[2] {
		case "x":
			ce.body.SetBoneAxis(idx, 0, amount)
		case "y":
			ce.body.SetBoneAxis(idx, 1, amount)
		case "z":
			ce.body.SetBoneAxis(idx, 2, amount)
		case "r":
			ce.body.SetBoneRadius(idx, amount)
		}
	}
	if ce.spineEdit {
		// layoutSpineRig is the cheap path (just snaps existing
		// handles to new positions). It auto-promotes to a full
		// refreshSpineRig when the bone count actually changed
		// (grow/shrink), so this stays correct without paying for
		// a tear-down + node-spawn cycle on every position tweak.
		ce.layoutSpineRig()
	}
}

// applyLegSculptBrush decodes a "leg/..." sculpt and applies it via
// CritterBody. Naming scheme (matches emitLegSculpt below):
//
//	leg/grow                         — append default leg
//	leg/grow/{boneIndex}             — append leg attached to bone N
//	leg/grow_at                      — append leg with hip at brush.Target
//	leg/shrink/{i}                   — remove leg i
//	leg/{i}/attach                   — set leg i's attach bone index
//	leg/{i}/r                        — set every joint's radius
//	leg/{i}/{hip|knee|foot}/r        — set one joint's radius
//	leg/{i}/{hip|knee|foot}/{x|y|z}  — set one axis of one joint
//
// Grow / grow_at use deterministic defaults so receivers don't need
// every joint coordinate piggy-backed in the sculpt — grow_at uses
// brush.Target (a Vector3) as the hip position and derives the
// remaining joints from it via LegRestPoseAtPos.
func (ce *CritterEditor) applyLegSculptBrush(brush musical.Sculpt) {
	ce.ensureLoaded()
	if ce.body.critter == nil {
		return
	}
	slider := brush.Slider
	amount := float32(brush.Amount)
	parts := strings.Split(slider, "/")
	if len(parts) < 2 {
		return
	}
	// parts[0] == "leg"
	switch parts[1] {
	case "grow":
		if len(parts) >= 3 {
			if bone, err := strconv.Atoi(parts[2]); err == nil {
				ce.body.AppendLegAt(bone)
				return
			}
		}
		ce.body.AppendLeg()
		return
	case "grow_at":
		ce.body.AppendLegAtPos(critter.Vec3{
			X: float32(brush.Target.X),
			Y: float32(brush.Target.Y),
			Z: float32(brush.Target.Z),
		})
		return
	case "shrink":
		if len(parts) < 3 {
			return
		}
		if i, err := strconv.Atoi(parts[2]); err == nil {
			ce.body.RemoveLeg(i)
		}
		return
	}
	i, err := strconv.Atoi(parts[1])
	if err != nil || len(parts) < 3 {
		return
	}
	switch parts[2] {
	case "attach":
		ce.body.SetLegAttach(i, int(amount))
		return
	case "r":
		ce.body.SetLegRadius(i, amount)
		return
	}
	if len(parts) < 4 {
		return
	}
	var joint critter.LegJoint
	switch parts[2] {
	case "hip":
		joint = critter.LegHip
	case "knee":
		joint = critter.LegKnee
	case "foot":
		joint = critter.LegFoot
	default:
		return
	}
	switch parts[3] {
	case "r":
		ce.body.SetLegJointRadius(i, joint, amount)
		return
	}
	var axis int
	switch parts[3] {
	case "x":
		axis = 0
	case "y":
		axis = 1
	case "z":
		axis = 2
	default:
		return
	}
	ce.body.SetLegJointAxis(i, joint, axis, amount)
}

// ensureLegGhost spawns the procedural-leg placement preview if it
// isn't already in the scene. The ghost shares the same parent and
// coordinate system as real leg meshes (child of the body's
// MeshInstance3D) so a body-local Y/Z position renders at the same
// world point. A semi-transparent white material distinguishes it
// from the committed legs.
func (ce *CritterEditor) ensureLegGhost() {
	if ce.legGhost != MeshInstance3D.Nil {
		return
	}
	if ce.body.mesh == MeshInstance3D.Nil {
		return
	}
	mi := MeshInstance3D.New()
	am := ArrayMesh.New()
	mi.AsMeshInstance3D().SetMesh(am.AsMesh())
	ghostMat := StandardMaterial3D.New()
	// Opaque preview — earlier draft used semi-transparent alpha to
	// say "ghost", but the user finds the cue confusing against the
	// existing body silhouette. Plain solid material reads as "the
	// limb that's about to land here" without depth-sort artefacts.
	ghostMat.AsBaseMaterial3D().SetAlbedoColor(Color.RGBA{R: 0.9, G: 0.9, B: 0.95, A: 1})
	ghostMat.AsBaseMaterial3D().SetShadingMode(BaseMaterial3D.ShadingModeUnshaded)
	mi.AsGeometryInstance3D().SetMaterialOverride(ghostMat.AsMaterial())
	ce.body.mesh.AsNode().AddChild(mi.AsNode())
	ce.legGhost = mi
	ce.legGhostArrayMesh = am
}

// clearLegGhost tears down the placement preview and exits leg
// placement mode. Safe to call when no ghost is up.
func (ce *CritterEditor) clearLegGhost() {
	ce.placingLeg = ""
	if ce.legGhost != MeshInstance3D.Nil {
		ce.legGhost.AsNode().QueueFree()
		ce.legGhost = MeshInstance3D.Nil
		ce.legGhostArrayMesh = ArrayMesh.Nil
	}
}

// updateLegGhostAt rebuilds the ghost's mesh from a leg whose hip
// lands at the given body-local position. Called each frame in
// placement mode so the ghost tracks the cursor exactly (no snap
// to a spine bone — knee/foot derive from the hip, foot lands on
// the ground plane).
func (ce *CritterEditor) updateLegGhostAt(hip critter.Vec3) {
	if ce.legGhostArrayMesh == ArrayMesh.Nil || ce.body.critter == nil {
		return
	}
	leg, ok := ce.body.critter.LegRestPoseAtPos(hip)
	if !ok {
		return
	}
	ce.uploadLegGhostMesh(leg)
}

// uploadLegGhostMesh skins the ghost ArrayMesh from a Leg's rest pose.
func (ce *CritterEditor) uploadLegGhostMesh(leg critter.Leg) {
	UploadCritterMesh(ce.legGhostArrayMesh, ce.body.critter.BuildLegMesh(leg, 6, 8, true))
}

// emitLegSculpt is the outbound counterpart to applyLegSculptBrush:
// local editor actions travel through musical.Sculpt so peers see
// the same change. Falls back to direct apply when no client is
// connected.
func (ce *CritterEditor) emitLegSculpt(slider string, amount float32) {
	ce.emitLegSculptAt(slider, Vector3.XYZ{}, amount)
}

// nearestLegFoot returns the data leg + mirrored side whose Foot
// projects closest to the mouse cursor in screen space, along with
// the foot's world position. Returns ok=false when there are no
// legs to pick from, the camera isn't ready, or the editor's body
// mesh hasn't been built yet — callers should hide the placement
// preview in that case rather than dropping it at the origin.
//
// Side is 0 for the +X (right) foot and 1 for the −X (left) foot.
// The data model stores only the +X side; we generate the mirror
// here inline rather than rounding through the gait code's
// per-side rendering path, since stepper placement runs in the
// regular geometry view where the gait container isn't active.
func (ce *CritterEditor) nearestLegFoot() (legIdx, side int, world Vector3.XYZ, ok bool) {
	if ce.body.critter == nil || ce.body.mesh == MeshInstance3D.Nil {
		return 0, 0, Vector3.XYZ{}, false
	}
	cam := Viewport.Get(ce.AsNode()).GetCamera3d()
	if cam == Camera3D.Nil {
		return 0, 0, Vector3.XYZ{}, false
	}
	legs := ce.body.critter.LegsView()
	if len(legs) == 0 {
		return 0, 0, Vector3.XYZ{}, false
	}
	mousePx := Viewport.Get(ce.AsNode()).GetMousePosition()
	bodyNode := ce.body.mesh.AsNode3D()
	bestSq := Float.X(math.MaxFloat32)
	bestIdx, bestSide := 0, 0
	var bestWorld Vector3.XYZ
	for i, leg := range legs {
		for s := 0; s < 2; s++ {
			x := leg.Foot.X
			if s == 1 {
				x = -x
			}
			local := Vector3.XYZ{
				X: Float.X(x), Y: Float.X(leg.Foot.Y), Z: Float.X(leg.Foot.Z),
			}
			w := bodyNode.ToGlobal(local)
			scr := cam.UnprojectPosition(w)
			dx := scr.X - mousePx.X
			dy := scr.Y - mousePx.Y
			d2 := dx*dx + dy*dy
			if d2 < bestSq {
				bestSq = d2
				bestIdx = i
				bestSide = s
				bestWorld = w
			}
		}
	}
	return bestIdx, bestSide, bestWorld, true
}

// emitLegSculptAt is the variant that carries a body-local Target
// position alongside the slider — used by leg/grow_at so the hip
// position rides with the create message instead of needing a
// follow-up sequence of per-axis sculpts.
func (ce *CritterEditor) emitLegSculptAt(slider string, target Vector3.XYZ, amount float32) {
	ce.lastEditAt = time.Now()
	if ce.client == nil {
		ce.applyLegSculptBrush(musical.Sculpt{
			Editor: "critter",
			Slider: slider,
			Target: target,
			Amount: Float.X(amount),
			Commit: true,
		})
		return
	}
	if err := ce.client.space.Sculpt(musical.Sculpt{
		Author: ce.client.id,
		Editor: "critter",
		Slider: slider,
		Target: target,
		Amount: Float.X(amount),
		Commit: true,
	}); err != nil {
		Engine.Raise(err)
	}
}

// emitBoneSculpt is the symmetric outbound side: when the local
// user takes a bone action via the editor UI it travels through
// musical.Sculpt so other clients see the same edit. In single-
// user dev (no client) it applies directly.
func (ce *CritterEditor) emitBoneSculpt(slider string, amount float32) {
	ce.lastEditAt = time.Now()
	if ce.client == nil {
		ce.applyBoneSculpt(slider, amount)
		return
	}
	if err := ce.client.space.Sculpt(musical.Sculpt{
		Author: ce.client.id,
		Editor: "critter",
		Slider: slider,
		Amount: Float.X(amount),
		Commit: true,
	}); err != nil {
		Engine.Raise(err)
	}
}

// dragEmit applies one in-progress drag edit LOCALLY (so the user sees
// the bone/leg move) and records it as the slider's latest value,
// WITHOUT emitting a musical mutation. The whole drag is coalesced
// into one committed Sculpt per touched slider at mouse-release
// (flushDragEmits) — see the dragEmits field comment for why. Routed
// by slider prefix to the same apply path the echoed mutation would
// take, so the live pose matches the eventual committed pose exactly.
func (ce *CritterEditor) dragEmit(slider string, amount float32) {
	ce.lastEditAt = time.Now()
	if strings.HasPrefix(slider, "leg/") {
		ce.applyLegSculptBrush(musical.Sculpt{
			Editor: "critter",
			Slider: slider,
			Amount: Float.X(amount),
			Commit: true,
		})
	} else {
		ce.applyBoneSculpt(slider, amount)
	}
	if ce.dragEmits == nil {
		ce.dragEmits = make(map[string]float32)
	}
	if _, seen := ce.dragEmits[slider]; !seen {
		ce.dragEmitOrder = append(ce.dragEmitOrder, slider)
	}
	ce.dragEmits[slider] = amount
}

// flushDragEmits commits a coalesced drag: one musical Sculpt per
// slider touched since the drag began, carrying its final value. The
// edits were already applied locally during the drag (dragEmit); this
// records them as observable mutations for peers + persistence. Called
// on mouse-release and defensively when leaving the view/editor
// mid-drag so a pending edit isn't silently dropped. No-op when no
// drag is pending.
func (ce *CritterEditor) flushDragEmits() {
	if len(ce.dragEmitOrder) == 0 {
		return
	}
	for _, slider := range ce.dragEmitOrder {
		amount := ce.dragEmits[slider]
		if strings.HasPrefix(slider, "leg/") {
			ce.emitLegSculpt(slider, amount)
		} else {
			ce.emitBoneSculpt(slider, amount)
		}
	}
	ce.dragEmits = nil
	ce.dragEmitOrder = ce.dragEmitOrder[:0]
}

func (ce *CritterEditor) UnhandledInput(event InputEvent.Instance) {
	if !ce.AsNode3D().Visible() {
		return
	}
	if ce.spineEdit {
		ce.spineUnhandledInput(event)
		return
	}
	if mev, ok := Object.As[InputEventMouseButton.Instance](event); ok && mev.AsInputEvent().IsPressed() {
		// Right-click cancels leg placement (mirrors how regular part
		// placement gets dismissed by right-click in PhysicsProcess).
		if mev.ButtonIndex() == Input.MouseButtonRight && ce.placingLeg != "" {
			ce.clearLegGhost()
			return
		}
		if mev.ButtonIndex() == Input.MouseButtonLeft {
			// Leg placement commit: raycast the body, ship the
			// hit-point hip position through Sculpt.Target (no snap
			// to a spine bone, so the limb can socket anywhere on the
			// surface). Hold Shift to keep the ghost up for multiple
			// placements (matches the part-placement Shift convention).
			if ce.placingLeg != "" {
				hover := ce.placementPicker()
				if hover.Collider == Object.Nil {
					return
				}
				bodyOrigin := ce.body.mesh.AsNode3D().GlobalPosition()
				local := Vector3.Sub(hover.Position, bodyOrigin)
				ce.lastEditAt = time.Now()
				ce.emitLegSculptAt("leg/grow_at", local, 0)
				if !Input.IsKeyPressed(Input.KeyShift) {
					ce.clearLegGhost()
				}
				return
			}
			if ce.Preview.Design() == "" {
				return
			}
			ce.lastEditAt = time.Now()
			if ce.placingStepper {
				// Stepper commit: snapshot the current foot pick from
				// PhysicsProcess (set in stepperLeg / stepperSide on
				// every hover frame) and ship a leg-anchor Change.
				// Mirror preview never participates — a stepper rides
				// one specific foot, picked on screen.
				if !ce.AsNode3D().Visible() {
					return
				}
				if _, _, _, ok := ce.nearestLegFoot(); !ok {
					return
				}
				anchor := PartAnchor{
					OnLeg:   true,
					LegFoot: ce.stepperLeg,
					LegSide: ce.stepperSide,
				}
				ce.place(anchor, ce.Preview.Design())
				if !Input.IsKeyPressed(Input.KeyShift) {
					ce.Preview.Remove()
					ce.placingStepper = false
				}
				return
			}
			primary := ce.previewAnchor(ce.Preview)
			ce.place(primary, ce.Preview.Design())
			if ce.MirrorPreview.AsNode3D().Visible() && ce.MirrorPreview.Design() != "" {
				mirror := ce.previewAnchor(ce.MirrorPreview)
				ce.place(mirror, ce.MirrorPreview.Design())
			}
			if !Input.IsKeyPressed(Input.KeyShift) {
				ce.Preview.Remove()
				ce.MirrorPreview.Remove()
			}
		}
	}
	if kev, ok := Object.As[InputEventKey.Instance](event); ok {
		if isDeletePress(kev) {
			if ce.client == nil {
				return
			}
			node, ok := ce.client.selection.Instance()
			if !ok {
				return
			}
			entity, ok := ce.partToEntity[Node3D.ID(node.ID())]
			if !ok {
				return
			}
			if err := ce.client.space.Change(musical.Change{
				Author: ce.client.id,
				Entity: entity,
				Editor: "critter",
				Remove: true,
				Commit: true,
			}); err != nil {
				Engine.Raise(err)
			}
		}
	}
}

// spineUnhandledInput dispatches clicks in the spine view. Left
// mouse press over a handle starts a drag or fires the grow/shrink
// action; release ends a drag. Branches on the active sub-view
// ("ribcage" vs "limbone") so body bones and limb bones never
// fight each other for picks.
func (ce *CritterEditor) spineUnhandledInput(event InputEvent.Instance) {
	mev, ok := Object.As[InputEventMouseButton.Instance](event)
	if !ok {
		return
	}
	// Right-click is the "remove this leg" gesture in the limb-
	// editing view; in the body-editing view it does nothing
	// (right-click is reserved for camera pan there).
	if mev.ButtonIndex() == Input.MouseButtonRight && mev.AsInputEvent().IsPressed() {
		if ce.view == "limbone" {
			if legIdx, _, legOk := ce.legHandleUnderMouse(); legOk {
				Viewport.Get(ce.AsNode()).SetInputAsHandled()
				ce.emitLegSculpt(fmt.Sprintf("leg/shrink/%d", legIdx), 0)
			}
		}
		return
	}
	if mev.ButtonIndex() != Input.MouseButtonLeft {
		return
	}
	if !mev.AsInputEvent().IsPressed() {
		// Commit the whole drag as one Sculpt per touched slider now
		// that the mouse is up — the intermediate frames were applied
		// locally but never recorded (see dragEmits).
		ce.flushDragEmits()
		ce.dragging = spineDrag{}
		return
	}
	// Disjoint picker domains per sub-view: limbone owns leg
	// handles; ribcage owns body bones + rib arcs. Routing here
	// keeps the two anatomical layers from stealing each other's
	// clicks.
	if ce.view == "limbone" {
		ce.limboneMousePress()
		return
	}
	h := ce.handleUnderMouse()
	if h == nil {
		// Rib arcs drive radius drags in the body-editing view.
		if boneIdx, ribOk := ce.ribArcUnderMouse(); ribOk {
			bones := ce.body.critter.Bones()
			if boneIdx < 0 || boneIdx >= len(bones) {
				return
			}
			bodyOrigin := ce.body.mesh.AsNode3D().GlobalPosition()
			hit, hitOk := ce.mouseOnXZeroPlane(bodyOrigin)
			if !hitOk {
				return
			}
			Viewport.Get(ce.AsNode()).SetInputAsHandled()
			ce.dragging = spineDrag{
				kind:        dragRadius,
				bone:        boneIdx,
				startMouse:  Viewport.Get(ce.AsNode()).GetMousePosition(),
				startHit:    hit,
				startRadius: bones[boneIdx].Radius,
			}
		}
		return
	}
	// Consume the click so the global selection handler in client.go
	// doesn't also run its own raycast and try to select the handle's
	// collider (which has no scene Owner — that's the segfault path).
	Viewport.Get(ce.AsNode()).SetInputAsHandled()
	switch h.tag {
	case tagGrowHead:
		ce.emitBoneSculpt("bone/grow/head", 0)
	case tagGrowTail:
		ce.emitBoneSculpt("bone/grow/tail", 0)
	case tagShrinkHead:
		ce.emitBoneSculpt("bone/shrink/head", 0)
	case tagShrinkTail:
		ce.emitBoneSculpt("bone/shrink/tail", 0)
	case tagBone:
		bones := ce.body.critter.Bones()
		if h.boneIdx < 0 || h.boneIdx >= len(bones) {
			return
		}
		bodyOrigin := ce.body.mesh.AsNode3D().GlobalPosition()
		hit, ok := ce.mouseOnXZeroPlane(bodyOrigin)
		if !ok {
			return
		}
		bw := Vector3.Add(bodyOrigin, Vector3.XYZ{
			X: 0, Y: Float.X(bones[h.boneIdx].Pos.Y), Z: Float.X(bones[h.boneIdx].Pos.Z),
		})
		startBones := make([]critter.Vec3, len(bones))
		for k, b := range bones {
			startBones[k] = b.Pos
		}
		ce.dragging = spineDrag{
			kind:       dragBone,
			bone:       h.boneIdx,
			offset:     Vector3.Sub(bw, hit),
			startMouse: Viewport.Get(ce.AsNode()).GetMousePosition(),
			startBones: startBones,
		}
	case tagRadius:
		bones := ce.body.critter.Bones()
		if h.boneIdx < 0 || h.boneIdx >= len(bones) {
			return
		}
		bodyOrigin := ce.body.mesh.AsNode3D().GlobalPosition()
		hit, ok := ce.mouseOnXZeroPlane(bodyOrigin)
		if !ok {
			return
		}
		ce.dragging = spineDrag{
			kind:        dragRadius,
			bone:        h.boneIdx,
			startMouse:  Viewport.Get(ce.AsNode()).GetMousePosition(),
			startHit:    hit,
			startRadius: bones[h.boneIdx].Radius,
		}
	}
}

// limboneMousePress handles the limb-editor click dispatch:
// resize the joint's radius if the cursor's on the ring edge,
// otherwise move the joint if it's on the marker. Pulled into its
// own function so the limbone-vs-ribcage branch at the call site
// stays a single if-else.
func (ce *CritterEditor) limboneMousePress() {
	// Radius rings sit outside the joint markers, so a click near
	// the marker should grab the marker (move). Take the ring path
	// only when the cursor is on the ring edge AND not on the marker.
	if legIdx, joint, ringOk := ce.legRingUnderMouse(); ringOk {
		if _, _, markerOk := ce.legHandleUnderMouse(); !markerOk {
			legs := ce.body.critter.Legs()
			if legIdx < 0 || legIdx >= len(legs) {
				return
			}
			leg := legs[legIdx]
			var jp critter.Vec3
			var jr float32
			switch joint {
			case critter.LegHip:
				jp, jr = leg.Hip, leg.HipRadius
			case critter.LegKnee:
				jp, jr = leg.Knee, leg.KneeRadius
			default:
				jp, jr = leg.Foot, leg.FootRadius
			}
			bodyOrigin := ce.body.mesh.AsNode3D().GlobalPosition()
			hit, hitOk := ce.mouseOnXZeroPlane(bodyOrigin)
			if !hitOk {
				return
			}
			Viewport.Get(ce.AsNode()).SetInputAsHandled()
			ce.dragging = spineDrag{
				kind:        dragLegRadius,
				startMouse:  Viewport.Get(ce.AsNode()).GetMousePosition(),
				startHit:    hit,
				startRadius: jr,
				legIdx:      legIdx,
				legJoint:    joint,
				startLegPos: jp,
			}
			return
		}
	}
	if legIdx, joint, legOk := ce.legHandleUnderMouse(); legOk {
		legs := ce.body.critter.Legs()
		if legIdx < 0 || legIdx >= len(legs) {
			return
		}
		leg := legs[legIdx]
		var jp critter.Vec3
		switch joint {
		case critter.LegHip:
			jp = leg.Hip
		case critter.LegKnee:
			jp = leg.Knee
		default:
			jp = leg.Foot
		}
		bodyOrigin := ce.body.mesh.AsNode3D().GlobalPosition()
		hit, hitOk := ce.mouseOnXZeroPlane(bodyOrigin)
		if !hitOk {
			return
		}
		// World-space joint position is body-origin + body-local
		// (Y, Z); X stays on the X=0 plane for the side view.
		jw := Vector3.Add(bodyOrigin, Vector3.XYZ{
			X: 0, Y: Float.X(jp.Y), Z: Float.X(jp.Z),
		})
		Viewport.Get(ce.AsNode()).SetInputAsHandled()
		ce.dragging = spineDrag{
			kind:        dragLeg,
			offset:      Vector3.Sub(jw, hit),
			startMouse:  Viewport.Get(ce.AsNode()).GetMousePosition(),
			legIdx:      legIdx,
			legJoint:    joint,
			startLegPos: jp,
		}
	}
}

// placementPicker is the editor's own version of MousePicker that
// excludes the PartSelectionLayer — so hovering for new muzzle
// placement skips already-placed parts and lands on the body
// underneath. Without this, dragging the preview across a placed
// muzzle would stick the next placement on top of it.
func (ce *CritterEditor) placementPicker() PhysicsDirectSpaceState3D.PhysicsDirectSpaceState3D_Intersection {
	cam := Viewport.Get(ce.AsNode()).GetCamera3d()
	if cam == Camera3D.Nil {
		return PhysicsDirectSpaceState3D.PhysicsDirectSpaceState3D_Intersection{}
	}
	mpos := Viewport.Get(ce.AsNode()).GetMousePosition()
	from := cam.ProjectRayOrigin(mpos)
	to := cam.ProjectPosition(mpos, 1000)
	space := ce.AsNode3D().GetWorld3d().DirectSpaceState()
	q := PhysicsRayQueryParameters3D.CreateOptions(from, to, int(PartSelectionMask), nil)
	return space.IntersectRay(q)
}

// handleUnderMouse raycasts against handle layer and returns the
// hit handle (or nil). Reuses the global selection mask trick:
// query layer 1<<2 only.
func (ce *CritterEditor) handleUnderMouse() *spineHandle {
	if ce.spineRig == nil {
		return nil
	}
	cam := Viewport.Get(ce.AsNode()).GetCamera3d()
	if cam == Camera3D.Nil {
		return nil
	}
	mpos := Viewport.Get(ce.AsNode()).GetMousePosition()
	from := cam.ProjectRayOrigin(mpos)
	to := cam.ProjectPosition(mpos, 1000)
	space := ce.AsNode3D().GetWorld3d().DirectSpaceState()
	q := PhysicsRayQueryParameters3D.CreateOptions(from, to, int(spineHandleLayer), nil)
	hit := space.IntersectRay(q)
	// Use ColliderID (Object.ID) for identity comparison rather than
	// the Object.Instance struct value — comparing wrapped Object
	// instances by `==` was matching the wrong handle (or none).
	if hit.ColliderID == 0 {
		return nil
	}
	hitID := hit.ColliderID
	bodyID := func(b StaticBody3D.Instance) Object.ID {
		if b == StaticBody3D.Nil {
			return 0
		}
		return Object.Instance(b.AsObject()).ID()
	}
	for i := range ce.spineRig.boneHandles {
		if bodyID(ce.spineRig.boneHandles[i].body) == hitID {
			return &ce.spineRig.boneHandles[i]
		}
	}
	for _, h := range []*spineHandle{&ce.spineRig.growHead, &ce.spineRig.growTail, &ce.spineRig.shrinkHead, &ce.spineRig.shrinkTail} {
		if bodyID(h.body) == hitID {
			return h
		}
	}
	return nil
}

// ribArcUnderMouse returns the bone index whose rib arc the mouse
// cursor sits on (within a small tolerance), and ok=true on a hit.
//
// The rib arcs are pure procedural geometry — no Godot colliders —
// so we don't go through the physics raycast. The view is locked
// to a side-on orthographic camera that looks along ±X, so we just
// project the mouse onto the body-local X=0 plane and run a
// point-to-arc proximity check in 2D. ribArcGeomAt rebuilds the
// same arc the renderer drew, so a click on the visible white
// curve maps to a hit here.
func (ce *CritterEditor) ribArcUnderMouse() (boneIdx int, ok bool) {
	if ce.body.critter == nil {
		return 0, false
	}
	bodyOrigin := ce.body.mesh.AsNode3D().GlobalPosition()
	hit, hitOk := ce.mouseOnXZeroPlane(bodyOrigin)
	if !hitOk {
		return 0, false
	}
	cy := float32(hit.Y - bodyOrigin.Y)
	cz := float32(hit.Z - bodyOrigin.Z)
	bones := ce.body.critter.BonesView()
	// Tolerance in world units: a bit wider than the rib's visible
	// thickness (~0.024 from ribHalfWidth = 0.012) so the user
	// doesn't have to hit pixel-perfectly, but tight enough not to
	// poach clicks on the spine ribbon or empty space.
	const tolerance = float32(0.06)
	best := tolerance + 1
	bestI := -1
	for i := range bones {
		g, gOk := ribArcGeomAt(bones, i)
		if !gOk {
			continue
		}
		// Cursor relative to circle centre, in the (Tᵉ, Nᵉ) basis.
		relY := cy - g.cY
		relZ := cz - g.cZ
		a := relY*g.teY + relZ*g.teZ     // ←→ along chord
		bComp := relY*g.neY + relZ*g.neZ // ↓↑ along bow direction (apex at +R, chord at 0)
		rho := float32(math.Sqrt(float64(a*a + bComp*bComp)))
		var d float32
		if bComp >= 0 {
			// In the arc band (apex side of the chord); distance to
			// the circle equals |rho − R|.
			d = rho - g.R
			if d < 0 {
				d = -d
			}
		} else {
			// Outside the arc band; nearest point is the closer
			// endpoint at (±R, 0) in the local basis.
			da := a - g.R
			if a < 0 {
				da = a + g.R
			}
			d = float32(math.Sqrt(float64(da*da + bComp*bComp)))
		}
		if d < best {
			best = d
			bestI = i
		}
	}
	if bestI >= 0 && best < tolerance {
		return bestI, true
	}
	return 0, false
}

// legHandleUnderMouse returns (legIdx, joint, ok) when the cursor
// is over one of the leg joint markers drawn in the ribcage view.
// Pure procedural geometry like the rib arcs — no collider — so we
// do a 2D proximity check in the X=0 plane against each leg's Hip,
// Knee, and Foot positions. The closer joint wins; tie-break is by
// the loop order (hip → knee → foot, lower legIdx first), which
// rarely matters in practice since handles sit far apart.
func (ce *CritterEditor) legHandleUnderMouse() (legIdx int, joint critter.LegJoint, ok bool) {
	if ce.body.critter == nil {
		return 0, 0, false
	}
	return ce.legPick(legHandlePickRadius, func(d, _ float32) float32 { return d })
}

// legRingUnderMouse returns (legIdx, joint, ok) when the cursor
// is over the edge of a leg joint's radius ring — i.e. roughly at
// distance R from the joint in the X=0 plane, where R is that
// joint's radius. Used as the click-target for radius resizing.
// Tried *after* legHandleUnderMouse so a click on the joint marker
// itself still moves the joint; only clicks on the ring edge land
// here.
func (ce *CritterEditor) legRingUnderMouse() (legIdx int, joint critter.LegJoint, ok bool) {
	return ce.legPick(legRingPickTolerance, func(d, r float32) float32 {
		if d > r {
			return d - r
		}
		return r - d
	})
}

// legPick walks every (leg, joint) pair and returns the one whose
// score function gives the smallest value under `tolerance`. Shared
// by legHandleUnderMouse (score = distance to joint) and
// legRingUnderMouse (score = |distance − radius|).
func (ce *CritterEditor) legPick(tolerance float32, score func(d, r float32) float32) (legIdx int, joint critter.LegJoint, ok bool) {
	if ce.body.critter == nil {
		return 0, 0, false
	}
	bodyOrigin := ce.body.mesh.AsNode3D().GlobalPosition()
	hit, hitOk := ce.mouseOnXZeroPlane(bodyOrigin)
	if !hitOk {
		return 0, 0, false
	}
	cy := float32(hit.Y - bodyOrigin.Y)
	cz := float32(hit.Z - bodyOrigin.Z)
	bestScore := tolerance
	best := -1
	var bestJ critter.LegJoint
	legs := ce.body.critter.LegsView()
	joints := [3]critter.LegJoint{critter.LegHip, critter.LegKnee, critter.LegFoot}
	for i, leg := range legs {
		points := [3]critter.Vec3{leg.Hip, leg.Knee, leg.Foot}
		radii := [3]float32{leg.HipRadius, leg.KneeRadius, leg.FootRadius}
		for k, p := range points {
			// Match the legHandleYShift the renderer applies so the
			// pick zone tracks the visible handle/ring.
			dy := cy - (p.Y + legHandleYShift)
			dz := cz - p.Z
			d := float32(math.Sqrt(float64(dy*dy + dz*dz)))
			s := score(d, radii[k])
			if s < bestScore {
				bestScore = s
				best = i
				bestJ = joints[k]
			}
		}
	}
	if best < 0 {
		return 0, 0, false
	}
	return best, bestJ, true
}

// legJointName maps a LegJoint enum value to the path component
// used in the sculpt protocol (hip/knee/foot).
func legJointName(j critter.LegJoint) string {
	switch j {
	case critter.LegHip:
		return "hip"
	case critter.LegKnee:
		return "knee"
	case critter.LegFoot:
		return "foot"
	}
	return "hip"
}

// mouseOnXZeroPlane intersects the current mouse ray with the
// body-local X=0 plane (bilateral symmetry plane). Returns the
// world-space hit position; ok=false if the ray is parallel to
// the plane.
func (ce *CritterEditor) mouseOnXZeroPlane(bodyOrigin Vector3.XYZ) (Vector3.XYZ, bool) {
	cam := Viewport.Get(ce.AsNode()).GetCamera3d()
	if cam == Camera3D.Nil {
		return Vector3.XYZ{}, false
	}
	mpos := Viewport.Get(ce.AsNode()).GetMousePosition()
	from := cam.ProjectRayOrigin(mpos)
	to := cam.ProjectPosition(mpos, 1000)
	dir := Vector3.Sub(to, from)
	// Plane: X = bodyOrigin.X (so body-local X=0).
	if Float.Abs(dir.X) < 1e-5 {
		return Vector3.XYZ{}, false
	}
	t := (bodyOrigin.X - from.X) / dir.X
	if t < 0 {
		return Vector3.XYZ{}, false
	}
	return Vector3.Add(from, Vector3.MulX(dir, t)), true
}

func (ce *CritterEditor) previewAnchor(p PreviewRenderer) PartAnchor {
	bodyOrigin := ce.body.mesh.AsNode3D().GlobalPosition()
	local := Vector3.Sub(p.AsNode3D().GlobalPosition(), bodyOrigin)
	return ce.body.ClosestAnchor(local)
}

func (ce *CritterEditor) place(anchor PartAnchor, design string) {
	ce.ensureLoaded()
	if ce.client == nil {
		// Single-user dev path: apply directly without going through
		// the musical Change/Import cycle. Procedural parts still
		// need their owners wired up so selection works.
		if part := newProceduralPart(design); part != nil {
			node := ce.body.AttachPartNode(anchor, part.Node())
			setSubtreeOwner(node.AsNode(), node.AsNode())
			ce.animatedParts[node.ID()] = part
			return
		}
		ce.body.AttachPart(anchor, PackedScene.Nil)
		return
	}
	if ce.client.space == nil {
		return
	}
	// Both library-imported and procedural designs ship through the
	// same Change protocol so peers see the placement. MusicalDesign
	// triggers an Import for the URI; the receiving Import handler
	// recognises "procedural://" prefixes via isKeepImporterPath and
	// skips the Resource.Load attempt, but still registers the URI
	// in design_to_string so tryAttachChange can branch on it.
	change := musical.Change{
		Author: ce.client.id,
		Entity: ce.client.NextEntity(),
		Design: ce.client.MusicalDesign(design),
		Offset: Vector3.XYZ{X: Float.X(anchor.T), Y: Float.X(anchor.Theta), Z: Float.X(anchor.Offset)},
		Editor: "critter",
		Commit: true,
	}
	if anchor.OnLeg {
		// Leg-foot anchor: piggy-back the (index, side) on Change.Bounds
		// so the existing scalar-only Offset/T-Theta-Offset slot stays
		// free for future per-stepper rotation tweaks. Bounds.X is
		// (legIdx + 1) so the zero default unambiguously decodes as
		// "this is a body anchor" on the receiving side — every
		// pre-existing recorded Change has Bounds=0,0,0 so they stay
		// valid without a schema migration.
		change.Bounds = Vector3.XYZ{
			X: Float.X(anchor.LegFoot + 1),
			Y: Float.X(anchor.LegSide),
			Z: Float.X(anchor.Scale),
		}
	} else if anchor.Scale > 0 {
		// Body anchor + per-part scale: only the Z slot is used;
		// X stays 0 so decoders still see this as a body anchor.
		change.Bounds = Vector3.XYZ{Z: Float.X(anchor.Scale)}
	}
	if err := ce.client.space.Change(change); err != nil {
		Engine.Raise(err)
	}
}

// setProceduralPreview swaps the PreviewRenderer's content for a
// procedurally-built node and tags it with the design sentinel so
// the standard hover-preview + click-commit paths treat it like a
// PackedScene-backed part. `p` must be a pointer — PreviewRenderer
// is a value on CritterEditor and a by-value copy would discard the
// design assignment.
func (ce *CritterEditor) setProceduralPreview(p *PreviewRenderer, design string, body Node3D.Instance) {
	if p.AsNode().GetChildCount() > 0 {
		Object.To[Node3D.Instance](p.AsNode().GetChild(0)).AsNode().QueueFree()
	}
	p.AsNode().AddChild(body.AsNode())
	p.design = design
}

// eyeScreenLookDir maps a 2D cursor offset (relative to where the
// eye projects on screen) into a local-space unit look direction
// for the eye's shader. Pure screen math — no raycast against any
// geometry — so the pupil tracks wherever the cursor is, including
// off-body. Assumes the editor camera is roughly facing the
// critter (local +Z ≈ toward camera): screen X maps to local X,
// screen Y (which grows downward) maps to local −Y, and Z is
// chosen so the result stays on the unit sphere in the forward
// hemisphere.
func eyeScreenLookDir(cam Camera3D.Instance, eyeWorld Vector3.XYZ, mousePx Vector2.XY) Vector3.XYZ {
	const pixelsForFullDeflection = Float.X(200)
	eyeScreen := cam.UnprojectPosition(eyeWorld)
	dx := (mousePx.X - eyeScreen.X) / pixelsForFullDeflection
	dy := (mousePx.Y - eyeScreen.Y) / pixelsForFullDeflection
	const maxOff = Float.X(0.55)
	if dx > maxOff {
		dx = maxOff
	} else if dx < -maxOff {
		dx = -maxOff
	}
	if dy > maxOff {
		dy = maxOff
	} else if dy < -maxOff {
		dy = -maxOff
	}
	rSquared := dx*dx + dy*dy
	if rSquared > 1 {
		rSquared = 1
	}
	z := Float.X(math.Sqrt(float64(1 - rSquared)))
	return Vector3.XYZ{X: dx, Y: -dy, Z: z}
}

// setSubtreeOwner walks every descendant of `parent` and points
// its Owner at `root`. Owner is what the global selection picker
// uses to find the entity that a hit collider belongs to —
// procedurally-built parts have no scene file to derive ownership
// from, so we have to wire it up by hand once the subtree is in
// the scene.
func setSubtreeOwner(root, parent Node.Instance) {
	for _, child := range parent.GetChildren() {
		child.SetOwner(root)
		setSubtreeOwner(root, child)
	}
}

// proceduralParter is implemented by procedural body parts that
// can be placed via the standard preview-click flow. Returning
// both the Node3D (to attach) and the per-frame ticker keeps the
// editor agnostic to which kind of part was built.
type proceduralParter interface {
	proceduralPart
	Node() Node3D.Instance
}

// newProceduralPart maps a procedural design sentinel to a fresh
// instance of the corresponding part class. Returns nil for
// non-procedural designs (the regular PackedScene path handles
// those). Adding a new procedural part type is one case here plus
// implementing the proceduralParter interface in its own file.
func newProceduralPart(design string) proceduralParter {
	switch design {
	case BuiltinEyesDesign:
		return newEyePart(0)
	}
	return nil
}

// ExportSubtree implements the Exporter interface (see export.go).
// We duplicate the body MeshInstance3D — which carries every placed
// part, the skeleton skin, and the procedural body mesh — onto a
// fresh root, with its float-above-ground translation zeroed so the
// critter sits at the origin in the resulting .glb (the body is
// raised to (0, 0.3, 0) in the editor only so the tail bone doesn't
// clip the ground plate; that offset is editor scaffolding, not part
// of the model).
func (ce *CritterEditor) ExportSubtree() Node3D.Instance {
	root := Node3D.New()
	root.AsNode().SetName("critter")
	if ce.body.mesh != MeshInstance3D.Nil {
		if dup, ok := Object.As[Node3D.Instance](ce.body.mesh.AsNode().Duplicate()); ok {
			dup.SetPosition(Vector3.Zero)
			root.AsNode().AddChild(dup.AsNode())
		}
	}
	return root
}

func (ce *CritterEditor) Change(change musical.Change) error {
	if change.Editor != "critter" {
		return nil
	}
	ce.ensureLoaded()
	if change.Remove {
		if id, ok := ce.entityToPart[change.Entity]; ok {
			ce.body.DetachPart(id)
			delete(ce.entityToPart, change.Entity)
			delete(ce.partToEntity, id)
			delete(ce.jawCache, id)
			// Drop any animated procedural part bound to this node
			// before the Node3D handle goes invalid; otherwise the
			// next Process tick calls GlobalPosition on a freed
			// object and crashes.
			delete(ce.animatedParts, id)
		}
		// Drop any pending entry for the same entity — no point
		// retrying a placement that was meant to be removed.
		ce.pendingChanges = filterPendingByEntity(ce.pendingChanges, change.Entity)
		return nil
	}
	// Gizmo move/twist on an already-attached part: rebuild the
	// anchor from the wire fields and re-pose in place. This keeps
	// the existing Node3D (so selection state survives the drag)
	// rather than tearing down and re-instantiating the design.
	if id, ok := ce.entityToPart[change.Entity]; ok {
		ce.body.SetPartAnchor(id, anchorFromChange(change))
		return nil
	}
	if !ce.tryAttachChange(change) {
		// Scene isn't loaded yet — defer until an Import lands and
		// Process retries on the next tick.
		ce.pendingChanges = append(ce.pendingChanges, change)
	}
	return nil
}

// debugCritterSignature logs a deterministic checksum of the final folded
// critter state (bones, legs, weights). Used to prove that a snapshot
// restore produces byte-identical state to a full sculpt fold — run a load
// to bake the snapshot, then a second load to restore it, and compare.
func (ce *CritterEditor) debugCritterSignature() {
	if !loadProfileOn || ce.body.critter == nil {
		return
	}
	c := ce.body.critter
	bones := c.BonesView()
	var boneSum float64
	for _, b := range bones {
		boneSum += float64(b.Pos.X) + float64(b.Pos.Y) + float64(b.Pos.Z) + float64(b.Radius)
	}
	legs := c.LegsView()
	var legSum float64
	for _, l := range legs {
		legSum += float64(l.Hip.X+l.Hip.Y+l.Hip.Z) + float64(l.Knee.X+l.Knee.Y+l.Knee.Z) +
			float64(l.Foot.X+l.Foot.Y+l.Foot.Z) + float64(l.HipRadius+l.KneeRadius+l.FootRadius)
	}
	var weightSum float64
	for _, w := range c.Weights() {
		weightSum += float64(w)
	}
	profMark("[critter sig] bones=%d boneSum=%.5f legs=%d legSum=%.5f weightSum=%.5f parts=%d",
		len(bones), boneSum, len(legs), legSum, weightSum, len(ce.partToEntity))
}

// retryPendingChanges re-attempts every Change that couldn't attach when
// it first arrived — either because its design's scene hadn't imported yet,
// or (during the bulk replay) because the leg it anchors to hadn't been
// folded yet. Called every Process tick and once at the end of
// flushCritterReplay (so leg-anchored parts land before 3D re-enables).
func (ce *CritterEditor) retryPendingChanges() {
	if len(ce.pendingChanges) == 0 {
		return
	}
	remaining := ce.pendingChanges[:0]
	for _, change := range ce.pendingChanges {
		if !ce.tryAttachChange(change) {
			remaining = append(remaining, change)
		}
	}
	ce.pendingChanges = remaining
}

// anchorFromChange decodes a wire Change back into a PartAnchor.
// Mirrors the encode in place() / the gizmo emit path: Offset.XYZ
// holds (T, Theta, Offset); Bounds.X > 0 signals a leg-foot anchor
// with (LegFoot+1, LegSide) packed into (Bounds.X, Bounds.Y);
// Angles.Y carries the runtime-only Twist rotation around the
// surface normal.
func anchorFromChange(change musical.Change) PartAnchor {
	a := PartAnchor{
		T:      float32(change.Offset.X),
		Theta:  float32(change.Offset.Y),
		Offset: float32(change.Offset.Z),
		Twist:  float32(change.Angles.Y),
		// Bounds.Z carries the per-part scale multiplier (0 = legacy
		// default, treated as 1.0 by positionPart). Sits in the slot
		// the leg-foot encoding leaves empty (Bounds.X = LegFoot+1,
		// Bounds.Y = LegSide) so scale + leg anchors can coexist.
		Scale: float32(change.Bounds.Z),
	}
	if change.Bounds.X > 0 {
		a.OnLeg = true
		a.LegFoot = int(change.Bounds.X) - 1
		a.LegSide = int(change.Bounds.Y)
	}
	return a
}

// tryAttachChange attempts to materialise a Change into a placed
// part. Returns true on success; false means the design's scene
// hasn't been imported yet so the caller should queue the change
// for retry. Always succeeds (returns true) when ce.client is
// nil — in single-user dev there's no packed_scenes map to wait
// on, so we just place a placeholder.
func (ce *CritterEditor) tryAttachChange(change musical.Change) bool {
	anchor := anchorFromChange(change)
	// Bounds.X > 0 signals a leg-foot anchor (place() encodes it that
	// way so the zero default of historical Change records still
	// decodes as a body anchor). Bail out if the named leg doesn't
	// exist yet — caller queues the change for retry once the leg
	// sculpt that creates it arrives.
	if anchor.OnLeg {
		if ce.body.critter == nil || anchor.LegFoot >= ce.body.critter.LegCount() {
			return false
		}
	}
	var node Node3D.Instance
	if ce.client != nil {
		uri, hasURI := ce.client.design_to_string[change.Design]
		switch {
		case hasURI && newProceduralPart(uri) != nil:
			// Procedural design — build geometry locally. The
			// part is re-instantiated on each client; only the
			// anchor and design URI ride the wire.
			part := newProceduralPart(uri)
			node = ce.body.AttachPartNode(anchor, part.Node())
			setSubtreeOwner(node.AsNode(), node.AsNode())
			ce.animatedParts[node.ID()] = part
		default:
			if s, ok := ce.client.sceneFor(change.Design); ok {
				node = ce.body.AttachPart(anchor, s)
			} else if hasURI && strings.HasSuffix(uri, ".obj") {
				// MakeHuman-style .obj without a PackedScene
				// importer — load as static mesh. Static-mesh path
				// builds its own collision box (see
				// loadStaticObjNode); we just need to walk owners
				// so the selection picker can find this entity.
				if objNode := loadStaticObjNode(uri); objNode != Node3D.Nil {
					node = ce.body.AttachPartNode(anchor, objNode)
					setSubtreeOwner(node.AsNode(), node.AsNode())
				}
			}
			if node == Node3D.Nil {
				return false
			}
		}
	} else {
		node = ce.body.AttachPart(anchor, PackedScene.Nil)
	}
	if node == Node3D.Nil {
		return false
	}
	id := node.ID()
	ce.entityToPart[change.Entity] = id
	ce.partToEntity[id] = change.Entity
	return true
}

func filterPendingByEntity(pending []musical.Change, entity musical.Entity) []musical.Change {
	out := pending[:0]
	for _, c := range pending {
		if c.Entity != entity {
			out = append(out, c)
		}
	}
	return out
}

func (ce *CritterEditor) PhysicsProcess(delta Float.X) {
	// Replay-window guard: many sculpts during the queue drain mark
	// the body dirty without firing flushRebuild yet (rebuild is
	// Callable.Defer-coalesced). RepositionPartsAnimated below indexes
	// the skeleton by the critter's CURRENT bone count — if the
	// skeleton hasn't been rebuilt to match, those reads go
	// out-of-bounds and parts collapse to the origin. Force the flush
	// here so the skeleton agrees with the critter before any frame's
	// skeleton-driven work runs.
	ce.body.EnsureFlushed()
	if ce.spineEdit {
		ce.spinePhysicsProcess(delta)
		return
	}
	if ce.view == "control" {
		ce.controlPhysicsProcess(float32(delta))
		return
	}
	// Procedural leg placement preview: same shape as the regular
	// part preview below but the ghost is a procedural MeshInstance3D
	// rather than an instanced PackedScene, so we rebuild its
	// geometry each frame to match the hover position. The hip lands
	// exactly at the body-surface raycast hit — no snap to a spine
	// bone — so the user can socket the limb anywhere on the body.
	if ce.placingLeg != "" {
		hover := ce.placementPicker()
		if hover.Collider == Object.Nil {
			return
		}
		bodyOrigin := ce.body.mesh.AsNode3D().GlobalPosition()
		local := Vector3.Sub(hover.Position, bodyOrigin)
		ce.updateLegGhostAt(critter.Vec3{
			X: float32(local.X), Y: float32(local.Y), Z: float32(local.Z),
		})
		return
	}
	if ce.Preview.Design() == "" {
		return
	}
	if Input.IsMouseButtonPressed(Input.MouseButtonRight) {
		ce.Preview.Remove()
		ce.MirrorPreview.Remove()
		ce.placingStepper = false
		return
	}
	if ce.placingStepper {
		// Stepper preview rides whichever leg foot is closest to the
		// cursor on screen. No body raycast — the user shouldn't
		// have to land the cursor on the body to attach to a leg.
		idx, side, footWorld, ok := ce.nearestLegFoot()
		if !ok {
			ce.Preview.AsNode3D().SetVisible(false)
			return
		}
		ce.stepperLeg = idx
		ce.stepperSide = side
		ce.Preview.AsNode3D().SetGlobalPosition(footWorld)
		ce.Preview.AsNode3D().SetBasis(Basis.XYZ{
			Vector3.New(ce.body.partScale, 0, 0),
			Vector3.New(0, ce.body.partScale, 0),
			Vector3.New(0, 0, ce.body.partScale),
		})
		ce.Preview.AsNode3D().SetVisible(true)
		return
	}
	hover := ce.placementPicker()
	if hover.Collider == Object.Nil {
		return
	}
	worldPos := hover.Position
	bodyOrigin := ce.body.mesh.AsNode3D().GlobalPosition()
	local := Vector3.Sub(worldPos, bodyOrigin)
	if Float.Abs(local.X) < 0.05 {
		local.X = 0
	}
	primary := ce.body.ClosestAnchor(local)
	ce.poseAt(ce.Preview, primary)
	// Whichever way SelectDesign left the primary preview (hidden, for
	// procedural sentinels, to avoid a flash at the previous anchor),
	// the first valid hover snaps it back on at the cursor.
	ce.Preview.AsNode3D().SetVisible(true)
	if local.X != 0 {
		mirror := PartAnchor{T: primary.T, Theta: -primary.Theta, Offset: primary.Offset}
		ce.poseAt(ce.MirrorPreview, mirror)
		ce.MirrorPreview.AsNode3D().SetVisible(true)
	} else {
		ce.MirrorPreview.AsNode3D().SetVisible(false)
	}
}

// spinePhysicsProcess updates the dragging bone (if any) from the
// current mouse position and refreshes handle positions to follow
// the body shape.
func (ce *CritterEditor) spinePhysicsProcess(delta Float.X) {
	if ce.dragging.kind != dragNone {
		// Dead-zone: don't start moving the bone until the cursor
		// has travelled enough pixels — keeps a probe-click from
		// nudging the bone unintentionally.
		if !ce.dragging.active {
			mpos := Viewport.Get(ce.AsNode()).GetMousePosition()
			dx := mpos.X - ce.dragging.startMouse.X
			dy := mpos.Y - ce.dragging.startMouse.Y
			if dx*dx+dy*dy < Float.X(dragActivatePixels*dragActivatePixels) {
				return
			}
			ce.dragging.active = true
		}
		bodyOrigin := ce.body.mesh.AsNode3D().GlobalPosition()
		hit, ok := ce.mouseOnXZeroPlane(bodyOrigin)
		if ok {
			switch ce.dragging.kind {
			case dragBone:
				target := Vector3.Add(hit, ce.dragging.offset)
				local := Vector3.Sub(target, bodyOrigin)
				// Pause the body rebuild for the duration of this
				// frame's bone batch — every emitBoneSculpt below
				// would otherwise trigger a full mesh + collision
				// + repositionParts pass via applyBoneSculpt, and
				// with radial propagation enabled that fires 2 ×
				// N_bones times per frame. ResumeRebuild flushes a
				// single rebuild at the end.
				ce.body.PauseRebuild()
				// X stays 0 for bilateral symmetry; Y and Z come
				// from the projected mouse. Send each as its own
				// Sculpt so the network can drop one without
				// corrupting the bone state.
				ce.dragEmit(fmt.Sprintf("bone/%d/y", ce.dragging.bone), float32(local.Y))
				ce.dragEmit(fmt.Sprintf("bone/%d/z", ce.dragging.bone), float32(local.Z))
				// Propagate the same delta to every bone strictly
				// between the dragged one and the nearer chain
				// endpoint. Index-based, not geometry-based:
				//
				//   chain of 10 bones [0..9]
				//   drag bone 2 → also drag bones 0, 1
				//   drag bone 7 → also drag bones 8, 9
				//   drag bone 0 or 9 → no propagation
				//
				// Endpoints picked by `i*2 <= n-1`: the dragged
				// bone counts as "closer to tail" when its index
				// is at or before the chain midpoint, and the
				// propagation runs through the lower-index range
				// j < i; otherwise propagation runs through the
				// higher-index range j > i.
				if len(ce.dragging.startBones) > 1 {
					startI := ce.dragging.startBones[ce.dragging.bone]
					dY := float32(local.Y) - startI.Y
					dZ := float32(local.Z) - startI.Z
					n := len(ce.dragging.startBones)
					i := ce.dragging.bone
					tailSide := i*2 <= n-1
					for j, sj := range ce.dragging.startBones {
						if j == i {
							continue
						}
						propagate := false
						if tailSide {
							propagate = j < i
						} else {
							propagate = j > i
						}
						if propagate {
							ce.dragEmit(fmt.Sprintf("bone/%d/y", j), sj.Y+dY)
							ce.dragEmit(fmt.Sprintf("bone/%d/z", j), sj.Z+dZ)
						}
					}
				}
				ce.body.ResumeRebuild()
			case dragLeg:
				if ce.dragging.legIdx < 0 || ce.dragging.legIdx >= ce.body.critter.LegCount() {
					return
				}
				target := Vector3.Add(hit, ce.dragging.offset)
				local := Vector3.Sub(target, bodyOrigin)
				// Y/Z only; X stays on the +X side (storage convention
				// for mirrored legs). The data model clamps Y to
				// GroundY, so a drag that pushes a joint underground
				// is silently pinned to ground level. The handles
				// render with a fixed Y shift; undo it here so the
				// stored joint Y matches the unshifted "true" joint
				// position again.
				ce.body.PauseRebuild()
				jname := legJointName(ce.dragging.legJoint)
				ce.dragEmit(
					fmt.Sprintf("leg/%d/%s/y", ce.dragging.legIdx, jname),
					float32(local.Y)-legHandleYShift,
				)
				ce.dragEmit(
					fmt.Sprintf("leg/%d/%s/z", ce.dragging.legIdx, jname),
					float32(local.Z),
				)
				ce.body.ResumeRebuild()
			case dragLegRadius:
				if ce.dragging.legIdx < 0 || ce.dragging.legIdx >= ce.body.critter.LegCount() {
					return
				}
				// New radius = startRadius + (cursor distance from
				// joint now − distance at click). Anchored to start
				// values so the response is linear in world units,
				// not accumulated per frame — same idea as the bone
				// radius drag.
				jw := Vector3.Add(bodyOrigin, Vector3.XYZ{
					X: 0,
					Y: Float.X(ce.dragging.startLegPos.Y),
					Z: Float.X(ce.dragging.startLegPos.Z),
				})
				dY := hit.Y - jw.Y
				dZ := hit.Z - jw.Z
				curD := Float.X(math.Sqrt(float64(dY*dY + dZ*dZ)))
				sdY := ce.dragging.startHit.Y - jw.Y
				sdZ := ce.dragging.startHit.Z - jw.Z
				startD := Float.X(math.Sqrt(float64(sdY*sdY + sdZ*sdZ)))
				r := ce.dragging.startRadius + float32(curD-startD)
				if r < 0.005 {
					r = 0.005
				}
				jname := legJointName(ce.dragging.legJoint)
				ce.dragEmit(
					fmt.Sprintf("leg/%d/%s/r", ce.dragging.legIdx, jname),
					r,
				)
			case dragRadius:
				// Radius drag is now driven by the rib arc itself:
				// new radius = (start radius) + (cursor distance
				// from bone now − cursor distance at click), with
				// both distances measured in the body's sagittal
				// (X=0) plane. Drag the cursor away from the bone
				// → arc grows; drag toward the bone → arc shrinks.
				// Anchored to start values so the response is
				// linear in world units, not accumulated per frame.
				bone, ok := ce.body.critter.BoneAt(ce.dragging.bone)
				if !ok {
					return
				}
				bdy := Float.X(bone.Pos.Y) + bodyOrigin.Y
				bdz := Float.X(bone.Pos.Z) + bodyOrigin.Z
				dY := hit.Y - bdy
				dZ := hit.Z - bdz
				curD := Float.X(math.Sqrt(float64(dY*dY + dZ*dZ)))
				sdY := ce.dragging.startHit.Y - bdy
				sdZ := ce.dragging.startHit.Z - bdz
				startD := Float.X(math.Sqrt(float64(sdY*sdY + sdZ*sdZ)))
				r := ce.dragging.startRadius + float32(curD-startD)
				if r < 0.02 {
					r = 0.02
				}
				ce.dragEmit(fmt.Sprintf("bone/%d/r", ce.dragging.bone), r)
			}
		}
	}
	// Refresh handle positions to track any shape change (incoming
	// network sculpts, or the body deforming under another drag).
	ce.layoutSpineRig()
	// Re-pin the world camera to the side profile and redraw the
	// xray ribcage from the latest bones. Both are no-ops outside
	// the ribcage view.
	ce.ribcageSnapCamera()
	ce.ribcageRebuildMesh()
}

func (ce *CritterEditor) poseAt(p PreviewRenderer, anchor PartAnchor) {
	if ce.body.critter == nil {
		return
	}
	pos, outward, _ := ce.body.critter.AnchorPoint(anchor.T, anchor.Theta, anchor.Offset)
	fwd := Vector3.Normalized(Vector3.XYZ{
		X: Float.X(outward.X), Y: Float.X(outward.Y), Z: Float.X(outward.Z),
	})
	right, up := partOrientation(fwd)
	bodyOrigin := ce.body.mesh.AsNode3D().GlobalPosition()
	origin := Vector3.Add(bodyOrigin, Vector3.XYZ{
		X: Float.X(pos.X), Y: Float.X(pos.Y), Z: Float.X(pos.Z),
	})
	scale := ce.body.partScale
	basis := Basis.XYZ{
		Vector3.MulX(right, scale),
		Vector3.MulX(up, scale),
		Vector3.MulX(fwd, scale),
	}
	p.AsNode3D().SetGlobalPosition(origin)
	p.AsNode3D().SetBasis(basis)
}

func (ce *CritterEditor) Process(delta Float.X) {
	if ce.body.parts == Node3D.Nil {
		return
	}
	// Same flush guard as PhysicsProcess — sculpts processed via the
	// queue drain leave the skeleton lagging the critter until the
	// deferred flushRebuild fires; RepositionPartsAnimated below
	// would otherwise read OOB on the skeleton.
	ce.body.EnsureFlushed()
	// Retry any Changes whose packed scene wasn't loaded when the
	// Change arrived. Cheap: pendingChanges is usually empty after
	// the first second or two of a session.
	ce.retryPendingChanges()
	// Mouse-pointer hint for eye parts: pure 2D screen-relative —
	// project each eye's world position to screen pixels, take the
	// cursor's 2D offset, normalise to a local look direction. No
	// raycast against the body needed; the pupil tracks wherever the
	// cursor is on screen, even far from the critter. Eyes decide
	// on their own when to enter a tracking burst (see eyePart.Process).
	cam := Viewport.Get(ce.AsNode()).GetCamera3d()
	mousePx := Viewport.Get(ce.AsNode()).GetMousePosition()
	// Only flag the eye hint valid while the cursor is "live" — moved
	// recently enough that latching onto it reads as the critter
	// noticing motion. A still mouse leaves hintValid false so eyes
	// keep their idle saccade pattern; bursts already underway run
	// out their burst window and then disengage.
	if mousePx != ce.lastMousePx {
		ce.lastMousePx = mousePx
		ce.lastMouseMoveAt = time.Now()
	}
	mouseLive := !ce.lastMouseMoveAt.IsZero() && time.Since(ce.lastMouseMoveAt) < 500*time.Millisecond
	for id, p := range ce.animatedParts {
		node, ok := id.Instance()
		if !ok {
			// Node freed out from under us without the Change
			// Remove path firing (scene teardown, etc.) — drop the
			// stale entry so the next frame doesn't trip over it.
			delete(ce.animatedParts, id)
			continue
		}
		if eye, ok := p.(*eyePart); ok {
			if cam == Camera3D.Nil || !mouseLive {
				eye.HintFocus(Vector3.XYZ{}, false)
			} else {
				eye.HintFocus(eyeScreenLookDir(cam, node.GlobalPosition(), mousePx), true)
			}
		}
		p.Process(float32(delta))
	}
	editing := ce.Preview.Design() != "" || time.Since(ce.lastEditAt) < time.Duration(jawIdleAfter*float32(time.Second))
	if !editing {
		ce.idleTime += float32(delta)
	}
	// Idle breathing: subtle radial puff on the spine's chest bone
	// via the skeleton, NOT on the MeshInstance3D transform. The
	// old MI-scale path dragged every attached part along with the
	// breath (eyes, hats, dressings all puffed); driving it through
	// the skeleton instead means parts stay anchored to their
	// rest-pose surface points and only the body skin breathes.
	// Pauses in ribcage / limbone view (stable side profile for
	// editing) and during active placement.
	if ce.body.mesh != MeshInstance3D.Nil {
		// Keep the MI transform at identity scale — historical
		// breathing wrote here, so reset in case anything else
		// (control-view gait, etc.) hasn't already.
		ce.body.mesh.AsNode3D().SetScale(Vector3.New(1, 1, 1))
		if ce.spineEdit || editing {
			ce.body.SetBreathe(0)
		} else {
			ce.breatheTime += float32(delta)
			const breathePeriod = float32(4.0)     // seconds per breath
			const breatheAmplitude = float32(0.03) // ±3 % chest puff
			phase := ce.breatheTime * (2 * float32(math.Pi) / breathePeriod)
			ce.body.SetBreathe(breatheAmplitude * float32(math.Sin(float64(phase))))
		}
	}
	// Idle head-look: schedule occasional sideways glances on the
	// neck. Suspended when actively editing or while the ribcage/
	// limbone overlays are open (the user wants the bones to stay
	// still under their cursor). The scheduler keeps ticking when
	// suspended so the gap timing stays continuous; we just don't
	// apply the resulting yaw.
	if ce.idleHeadLook != nil {
		ce.idleHeadLook.advance(float32(delta))
		if ce.spineEdit || editing {
			ce.body.SetHeadLookYaw(0)
		} else {
			ce.body.SetHeadLookYaw(ce.idleHeadLook.angle)
		}
	}
	// Reposition attached parts (eyes, hats, dressings, etc.) using
	// the current bone poses — LBS over the rest-anchor (T, Theta,
	// Offset) so each part tracks the breathing chest, the head-
	// look neck, and the gait-driven body bob. Must run AFTER
	// SetBreathe / SetHeadLookYaw above so the bone poses are
	// current; positions written here are visible next frame.
	if ce.body.mesh != MeshInstance3D.Nil {
		ce.body.RepositionPartsAnimated()
	}
	parts := ce.body.parts.AsNode()
	for i := 0; i < parts.GetChildCount(); i++ {
		child, ok := Object.As[Node3D.Instance](parts.GetChild(i))
		if !ok {
			continue
		}
		id := child.ID()
		st, ok := ce.jawCache[id]
		if !ok {
			// Object.As over Object.To: parts that aren't library-
			// imported muzzles (e.g. our static MakeHuman .obj
			// dressings) don't have a "LowerJaw" subnode, and even
			// when they do, FindChild can return a non-Node3D node —
			// the panicking To-cast would crash the editor instead of
			// just skipping the part.
			jawNode := child.AsNode().FindChild("LowerJaw")
			jaw, jawOk := Object.As[Node3D.Instance](jawNode)
			if !jawOk || jaw == Node3D.Nil {
				ce.jawCache[id] = &jawState{}
				continue
			}
			ce.idlePhase += 1.7
			seed1 := uint64(math.Float32bits(ce.idlePhase))*6364136223846793005 + 1442695040888963407
			seed2 := uint64(math.Float32bits(ce.idlePhase+0.91)) * 1234567891
			rng := rand.New(rand.NewPCG(seed1, seed2))
			st = &jawState{
				jaw:       jaw,
				restBasis: jaw.AsNode3D().Basis(),
				phase:     ce.idlePhase,
				rng:       rng,
			}
			st.scheduleJawEvent(ce.idleTime)
			ce.jawCache[id] = st
		}
		if st.jaw == Node3D.Nil {
			continue
		}
		open := st.openness(ce.idleTime)
		angle := open * jawMaxAngle
		basis := st.restBasis
		basis = Basis.Rotated(basis, Vector3.New(1, 0, 0), Angle.Radians(angle))
		st.jaw.AsNode3D().SetBasis(basis)
	}
}

// openness returns the jaw's open fraction (0..1 of jawMaxAngle)
// at idle time t, advancing the per-jaw event schedule when the
// current event ends.
//
// Three event types:
//   - twitch: tiny brief open, ~30% of events. The critter is
//     ostensibly breathing / micro-chewing.
//   - chew:   medium open, repeated 3-5 times in quick bursts,
//     ~65% of events. Snaps shut between repeats.
//   - yawn:   wide slow open, ~5% of events. The whole critter
//     looks like it's resting deeply.
func (s *jawState) openness(t float32) float32 {
	if t >= s.eventEndsAt {
		// Closed between events; advance to next when it's time.
		if t >= s.nextEventAt {
			s.scheduleJawEvent(t)
		} else {
			return 0
		}
	}
	if s.eventDuration <= 0 {
		return 0
	}
	x := (t - (s.eventEndsAt - s.eventDuration)) / s.eventDuration // 0..1
	if x < 0 {
		x = 0
	}
	if x > 1 {
		x = 1
	}
	// 0→1→0 hump via sin(πx) so the open/close motion is smooth.
	return s.eventAmplitude * float32(math.Sin(math.Pi*float64(x)))
}

// scheduleJawEvent picks the next jaw event type via weighted
// random and queues it to start at `t`. Events have type-specific
// duration + amplitude ranges; the inter-event gap also depends on
// type (a chew burst leaves a short gap; a yawn leaves a long one).
func (s *jawState) scheduleJawEvent(t float32) {
	if s.rng == nil {
		s.nextEventAt = t + 4
		return
	}
	r := s.rng.Float32()
	var dur, amp, gap float32
	switch {
	case r < 0.05:
		// Yawn: 1.0-2.0 s, full amplitude, long gap after.
		dur = 1.0 + s.rng.Float32()*1.0
		amp = 0.9 + s.rng.Float32()*0.1
		gap = 6 + s.rng.Float32()*8
	case r < 0.35:
		// Twitch: tiny + brief, short gap.
		dur = 0.05 + s.rng.Float32()*0.1
		amp = 0.05 + s.rng.Float32()*0.15
		gap = 0.2 + s.rng.Float32()*1.5
	default:
		// Chew: medium burst.
		dur = 0.08 + s.rng.Float32()*0.18
		amp = 0.25 + s.rng.Float32()*0.35
		gap = 0.08 + s.rng.Float32()*0.6
	}
	s.eventEndsAt = t + dur
	s.eventDuration = dur
	s.eventAmplitude = amp
	s.nextEventAt = s.eventEndsAt + gap
}

// refreshSpineRig (re)builds the spine handle scene from scratch
// to match the current bone count. Called on view-enter, on bone
// count changes (grow/shrink), and whenever the chain structure
// mutates. For pose-only changes use layoutSpineRig.
func (ce *CritterEditor) refreshSpineRig() {
	if ce.body.critter == nil {
		return
	}
	if ce.spineRig != nil {
		ce.spineRig.container.AsNode().QueueFree()
		ce.spineRig = nil
	}
	rig := &spineRig{}
	container := Node3D.New()
	ce.AsNode3D().AsNode().AddChild(container.AsNode())
	rig.container = container

	bones := ce.body.critter.Bones()
	rig.boneHandles = make([]spineHandle, len(bones))
	for i := range bones {
		rig.boneHandles[i] = ce.spawnHandle(container, boneHandleRadius, Color.RGBA{R: 0.95, G: 0.7, B: 0.2, A: 1}, tagBone, i)
	}
	// Radius edits are driven by the rib arcs directly (see
	// ribArcUnderMouse) — no separate handle to spawn.
	rig.growHead = ce.spawnHandle(container, growNubRadius, Color.RGBA{R: 0.3, G: 1.0, B: 0.4, A: 1}, tagGrowHead, -1)
	rig.growTail = ce.spawnHandle(container, growNubRadius, Color.RGBA{R: 0.3, G: 1.0, B: 0.4, A: 1}, tagGrowTail, -1)
	rig.shrinkHead = ce.spawnHandle(container, growNubRadius, Color.RGBA{R: 1.0, G: 0.3, B: 0.3, A: 1}, tagShrinkHead, -1)
	rig.shrinkTail = ce.spawnHandle(container, growNubRadius, Color.RGBA{R: 1.0, G: 0.3, B: 0.3, A: 1}, tagShrinkTail, -1)

	ce.spineRig = rig
	ce.layoutSpineRig()
}

// spawnHandle creates one widget: a SphereMesh for the visual plus
// a sibling StaticBody3D + SphereShape3D on the spineHandleLayer
// so the editor's own raycast can find it. Returns the populated
// spineHandle struct so layoutSpineRig can move it each frame.
func (ce *CritterEditor) spawnHandle(parent Node3D.Instance, radius float32, color Color.RGBA, tag handleTag, boneIdx int) spineHandle {
	root := Node3D.New()
	parent.AsNode().AddChild(root.AsNode())
	mesh := MeshInstance3D.New()
	sphere := SphereMesh.New()
	sphere.AsPrimitiveMesh().AsMesh()
	sphere.SetRadius(Float.X(radius))
	sphere.SetHeight(Float.X(radius * 2))
	mat := StandardMaterial3D.New()
	mat.AsBaseMaterial3D().SetAlbedoColor(color)
	mat.AsBaseMaterial3D().SetShadingMode(BaseMaterial3D.ShadingModeUnshaded)
	// X-ray: render the handle on top of the body so the user can
	// always see and click it even when the bone sits inside the
	// mesh. Only applies inside the spine view; normal mode shows
	// no handles at all so opacity isn't a concern there.
	mat.AsBaseMaterial3D().SetNoDepthTest(true)
	sphere.AsPrimitiveMesh().AsMesh().SurfaceSetMaterial(0, mat.AsMaterial())
	mesh.SetMesh(sphere.AsMesh())
	root.AsNode().AddChild(mesh.AsNode())
	body := StaticBody3D.New()
	body.AsCollisionObject3D().SetCollisionLayer(int(spineHandleLayer))
	body.AsCollisionObject3D().SetCollisionMask(0)
	shape := CollisionShape3D.New()
	sphereShape := SphereShape3D.New()
	sphereShape.SetRadius(Float.X(radius))
	shape.SetShape(sphereShape.AsShape3D())
	body.AsNode().AddChild(shape.AsNode())
	root.AsNode().AddChild(body.AsNode())
	return spineHandle{
		node:    root,
		body:    body,
		tag:     tag,
		boneIdx: boneIdx,
	}
}

// layoutSpineRig snaps each handle to its current world position
// without rebuilding the rig — cheap, called each PhysicsProcess
// so handles track the body during drags.
func (ce *CritterEditor) layoutSpineRig() {
	if ce.spineRig == nil || ce.body.critter == nil {
		return
	}
	bones := ce.body.critter.BonesView()
	if len(bones) != len(ce.spineRig.boneHandles) {
		// Chain length changed — full rebuild.
		ce.refreshSpineRig()
		return
	}
	bodyOrigin := ce.body.mesh.AsNode3D().GlobalPosition()
	for i, b := range bones {
		bp := Vector3.Add(bodyOrigin, Vector3.XYZ{
			X: 0, Y: Float.X(b.Pos.Y), Z: Float.X(b.Pos.Z),
		})
		ce.spineRig.boneHandles[i].node.AsNode3D().SetGlobalPosition(bp)
	}
	if len(bones) >= 2 {
		// Extrapolated nub positions at each tip (same direction
		// AppendHead / AppendTail will place new bones).
		headDir := critter.Vec3{
			X: bones[len(bones)-1].Pos.X - bones[len(bones)-2].Pos.X,
			Y: bones[len(bones)-1].Pos.Y - bones[len(bones)-2].Pos.Y,
			Z: bones[len(bones)-1].Pos.Z - bones[len(bones)-2].Pos.Z,
		}
		tailDir := critter.Vec3{
			X: bones[0].Pos.X - bones[1].Pos.X,
			Y: bones[0].Pos.Y - bones[1].Pos.Y,
			Z: bones[0].Pos.Z - bones[1].Pos.Z,
		}
		growHeadPos := Vector3.Add(bodyOrigin, Vector3.XYZ{
			X: 0,
			Y: Float.X(bones[len(bones)-1].Pos.Y + headDir.Y),
			Z: Float.X(bones[len(bones)-1].Pos.Z + headDir.Z),
		})
		growTailPos := Vector3.Add(bodyOrigin, Vector3.XYZ{
			X: 0,
			Y: Float.X(bones[0].Pos.Y + tailDir.Y),
			Z: Float.X(bones[0].Pos.Z + tailDir.Z),
		})
		ce.spineRig.growHead.node.AsNode3D().SetGlobalPosition(growHeadPos)
		ce.spineRig.growTail.node.AsNode3D().SetGlobalPosition(growTailPos)

		// Shrink nubs sit just inside the head/tail tips so they
		// don't overlap the grow nubs.
		shrinkHeadPos := Vector3.Add(bodyOrigin, Vector3.XYZ{
			X: 0,
			Y: Float.X(bones[len(bones)-1].Pos.Y),
			Z: Float.X(bones[len(bones)-1].Pos.Z),
		})
		shrinkHeadPos.Y += Float.X(bones[len(bones)-1].Radius) * 1.5
		shrinkTailPos := Vector3.Add(bodyOrigin, Vector3.XYZ{
			X: 0,
			Y: Float.X(bones[0].Pos.Y),
			Z: Float.X(bones[0].Pos.Z),
		})
		shrinkTailPos.Y += Float.X(bones[0].Radius) * 1.5
		ce.spineRig.shrinkHead.node.AsNode3D().SetGlobalPosition(shrinkHeadPos)
		ce.spineRig.shrinkTail.node.AsNode3D().SetGlobalPosition(shrinkTailPos)
		// Hide shrink nubs when we're at the floor of 2 bones, so
		// you can't accidentally bottom out the chain.
		canShrink := len(bones) > 2
		ce.spineRig.shrinkHead.node.AsNode3D().SetVisible(canShrink)
		ce.spineRig.shrinkTail.node.AsNode3D().SetVisible(canShrink)
	}
}
