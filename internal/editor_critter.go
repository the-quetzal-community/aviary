package internal

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"graphics.gd/classdb/BaseMaterial3D"
	"graphics.gd/classdb/Camera3D"
	"graphics.gd/classdb/CollisionShape3D"
	"graphics.gd/classdb/Engine"
	"graphics.gd/classdb/Input"
	"graphics.gd/classdb/InputEvent"
	"graphics.gd/classdb/InputEventKey"
	"graphics.gd/classdb/InputEventMouseButton"
	"graphics.gd/classdb/MeshInstance3D"
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

	idleTime   float32
	lastEditAt time.Time
	idlePhase  float32

	// Spine view state. `spineEdit` flips true when the user
	// switches into the spine view, surfacing draggable bone
	// handles and grow/shrink nubs. `dragging` carries the drag
	// target between mouse-down and mouse-up frames.
	spineEdit bool
	spineRig  *spineRig
	dragging  spineDrag
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
	active      bool
}

type dragKind int

const (
	dragNone dragKind = iota
	dragBone
	dragRadius
)

// spineRig holds the editor-only visual scaffolding for the spine
// view: one move-handle per bone, one radius-handle per bone, plus
// four end nubs to grow/shrink the chain. All children of a single
// container Node3D under the editor so they teardown together
// when leaving the view.
type spineRig struct {
	container     Node3D.Instance
	boneHandles   []spineHandle
	radiusHandles []spineHandle
	growHead      spineHandle
	growTail      spineHandle
	shrinkHead    spineHandle
	shrinkTail    spineHandle
}

// spineHandle is one clickable widget: a tiny visible sphere plus
// a sibling StaticBody3D / CollisionShape3D for the raycast picker.
// Layer 1<<2 (== layer 3) keeps it out of the global selection
// scan (which masks out layer 2) and the body's MousePicker
// (which doesn't care — default mask is all layers).
type spineHandle struct {
	node     Node3D.Instance
	body     StaticBody3D.Instance
	shape    CollisionShape3D.Instance
	sphere   SphereShape3D.Instance
	tag      handleTag
	boneIdx  int // for boneHandle / radiusHandle, the bone index
	endpoint int // for grow/shrink nubs: +1 = head end, -1 = tail end
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
}

const (
	jawMaxAngle  = float32(0.55)
	jawPeriod    = float32(4.5)
	jawIdleAfter = 1.5
	muzzleSlot   = "muzzles"

	spineView = "spine"
	placeView = "place"

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

// Views advertises two view modes to the ViewSelector dropdown:
//
//   - "place"  — default mode, click on the body to drop muzzles /
//     antlers / other parts (the original critter
//     editor behaviour). No spine handles drawn.
//   - "spine"  — bone editor: drag handles to move /
//     resize segments, click grow/shrink nubs to
//     extend or shorten the chain.
//
// The ViewSelector lets the user flip between the two without
// losing state.
func (*CritterEditor) Views() []string { return []string{placeView, spineView} }
func (ce *CritterEditor) SwitchToView(view string) {
	ce.ensureLoaded()
	switch view {
	case spineView:
		ce.spineEdit = true
		ce.refreshSpineRig()
	default:
		// Everything that isn't "spine" — including the explicit
		// "place" view and any stray empty string — exits spine
		// edit and tears down handles so part placement clicks
		// can land on the body again.
		ce.spineEdit = false
		if ce.spineRig != nil {
			ce.spineRig.container.AsNode().QueueFree()
			ce.spineRig = nil
		}
	}
}

func (*CritterEditor) Name() string { return "critter" }
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
	ce.ensureLoaded()
}

func (ce *CritterEditor) ChangeEditor() {
	// Leaving the editor — tear down the spine rig too. Otherwise the
	// handles linger as invisible-but-pickable colliders in the
	// world.
	if ce.spineRig != nil {
		ce.spineRig.container.AsNode().QueueFree()
		ce.spineRig = nil
	}
	ce.spineEdit = false
}

func (ce *CritterEditor) ensureLoaded() {
	ce.loadOnce.Do(func() {
		mi := MeshInstance3D.New()
		ce.AsNode3D().AsNode().AddChild(mi.AsNode())
		body, err := AttachCritterBody(mi, critter.New())
		if err != nil {
			ce.loadErr = err
			Engine.Raise(err)
			return
		}
		ce.body = body
		ce.entityToPart = make(map[musical.Entity]Node3D.ID)
		ce.partToEntity = make(map[Node3D.ID]musical.Entity)
		ce.jawCache = make(map[Node3D.ID]*jawState)
	})
}

func (ce *CritterEditor) SelectDesign(mode Mode, design string) {
	if mode != ModeGeometry {
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
	if err := ce.client.space.Sculpt(musical.Sculpt{
		Author: ce.client.id,
		Editor: "critter",
		Slider: editing,
		Amount: Float.X(value),
		Commit: commit,
	}); err != nil {
		Engine.Raise(err)
	}
}

// Sculpt routes incoming sculpts. Anything starting with "bone/" is
// the structural/edit protocol — see applyBoneSculpt.
// Everything else falls through to the legacy macro slider system
// (body shape sliders) so existing scenes replay correctly.
func (ce *CritterEditor) Sculpt(brush musical.Sculpt) error {
	if strings.HasPrefix(brush.Slider, "bone/") {
		ce.applyBoneSculpt(brush.Slider, float32(brush.Amount))
		return nil
	}
	ce.applySlider(brush.Slider, brush.Amount)
	return nil
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
		ce.refreshSpineRig()
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

func (ce *CritterEditor) UnhandledInput(event InputEvent.Instance) {
	if !ce.AsNode3D().Visible() {
		return
	}
	if ce.spineEdit {
		ce.spineUnhandledInput(event)
		return
	}
	if mev, ok := Object.As[InputEventMouseButton.Instance](event); ok && mev.ButtonIndex() == Input.MouseButtonLeft && mev.AsInputEvent().IsPressed() {
		if ce.Preview.Design() == "" {
			return
		}
		ce.lastEditAt = time.Now()
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
	if kev, ok := Object.As[InputEventKey.Instance](event); ok {
		if kev.AsInputEvent().IsPressed() && (kev.Keycode() == Input.KeyDelete || kev.Keycode() == Input.KeyBackspace) && !kev.AsInputEvent().IsEcho() {
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
// action; release ends a drag.
func (ce *CritterEditor) spineUnhandledInput(event InputEvent.Instance) {
	mev, ok := Object.As[InputEventMouseButton.Instance](event)
	if !ok {
		return
	}
	if mev.ButtonIndex() != Input.MouseButtonLeft {
		return
	}
	if !mev.AsInputEvent().IsPressed() {
		ce.dragging = spineDrag{}
		return
	}
	h := ce.handleUnderMouse()
	if h == nil {
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
		ce.dragging = spineDrag{
			kind:       dragBone,
			bone:       h.boneIdx,
			offset:     Vector3.Sub(bw, hit),
			startMouse: Viewport.Get(ce.AsNode()).GetMousePosition(),
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
	for i := range ce.spineRig.radiusHandles {
		if bodyID(ce.spineRig.radiusHandles[i].body) == hitID {
			return &ce.spineRig.radiusHandles[i]
		}
	}
	for _, h := range []*spineHandle{&ce.spineRig.growHead, &ce.spineRig.growTail, &ce.spineRig.shrinkHead, &ce.spineRig.shrinkTail} {
		if bodyID(h.body) == hitID {
			return h
		}
	}
	return nil
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
		ce.body.AttachPart(anchor, PackedScene.Nil)
		return
	}
	if ce.client.space == nil {
		return
	}
	ce.client.entity_ids[ce.client.id]++
	if err := ce.client.space.Change(musical.Change{
		Author: ce.client.id,
		Entity: musical.Entity{
			Author: ce.client.id,
			Number: ce.client.entity_ids[ce.client.id],
		},
		Design: ce.client.MusicalDesign(design),
		Offset: Vector3.XYZ{X: Float.X(anchor.T), Y: Float.X(anchor.Theta), Z: Float.X(anchor.Offset)},
		Editor: "critter",
		Commit: true,
	}); err != nil {
		Engine.Raise(err)
	}
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
		}
		// Drop any pending entry for the same entity — no point
		// retrying a placement that was meant to be removed.
		ce.pendingChanges = filterPendingByEntity(ce.pendingChanges, change.Entity)
		return nil
	}
	if !ce.tryAttachChange(change) {
		// Scene isn't loaded yet — defer until an Import lands and
		// Process retries on the next tick.
		ce.pendingChanges = append(ce.pendingChanges, change)
	}
	return nil
}

// tryAttachChange attempts to materialise a Change into a placed
// part. Returns true on success; false means the design's scene
// hasn't been imported yet so the caller should queue the change
// for retry. Always succeeds (returns true) when ce.client is
// nil — in single-user dev there's no packed_scenes map to wait
// on, so we just place a placeholder.
func (ce *CritterEditor) tryAttachChange(change musical.Change) bool {
	anchor := PartAnchor{
		T:      float32(change.Offset.X),
		Theta:  float32(change.Offset.Y),
		Offset: float32(change.Offset.Z),
	}
	var scene PackedScene.Instance
	if ce.client != nil {
		if s, ok := ce.client.packed_scenes[change.Design].Instance(); !ok {
			// Don't materialise yet — we'd produce an empty
			// placeholder that the existing redesign refresh path
			// can't reach since critter parts aren't tracked in
			// world.design_to_entity.
			return false
		} else {
			scene = s
		}
	}
	node := ce.body.AttachPart(anchor, scene)
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
	if ce.spineEdit {
		ce.spinePhysicsProcess(delta)
		return
	}
	if ce.Preview.Design() == "" {
		return
	}
	if Input.IsMouseButtonPressed(Input.MouseButtonRight) {
		ce.Preview.Remove()
		ce.MirrorPreview.Remove()
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
				// X stays 0 for bilateral symmetry; Y and Z come
				// from the projected mouse. Send each as its own
				// Sculpt so the network can drop one without
				// corrupting the bone state.
				ce.emitBoneSculpt(fmt.Sprintf("bone/%d/y", ce.dragging.bone), float32(local.Y))
				ce.emitBoneSculpt(fmt.Sprintf("bone/%d/z", ce.dragging.bone), float32(local.Z))
			case dragRadius:
				if ce.dragging.bone < 0 || ce.dragging.bone >= ce.body.critter.BoneCount() {
					return
				}
				// Radius drag: NEW radius = (start radius) +
				// (current hit.Y − start hit.Y). Mouse moves up
				// from click point → radius grows; moves down →
				// radius shrinks. Always anchored to the captured
				// start so the value tracks the cursor 1:1 in
				// world units rather than accumulating per frame.
				deltaY := float32(hit.Y - ce.dragging.startHit.Y)
				r := ce.dragging.startRadius + deltaY
				if r < 0.02 {
					r = 0.02
				}
				ce.emitBoneSculpt(fmt.Sprintf("bone/%d/r", ce.dragging.bone), r)
			}
		}
	}
	// Refresh handle positions to track any shape change (incoming
	// network sculpts, or the body deforming under another drag).
	ce.layoutSpineRig()
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
	// Retry any Changes whose packed scene wasn't loaded when the
	// Change arrived. Cheap: pendingChanges is usually empty after
	// the first second or two of a session.
	if len(ce.pendingChanges) > 0 {
		remaining := ce.pendingChanges[:0]
		for _, change := range ce.pendingChanges {
			if !ce.tryAttachChange(change) {
				remaining = append(remaining, change)
			}
		}
		ce.pendingChanges = remaining
	}
	editing := ce.Preview.Design() != "" || time.Since(ce.lastEditAt) < time.Duration(jawIdleAfter*float32(time.Second))
	if !editing {
		ce.idleTime += float32(delta)
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
			jaw := Object.To[Node3D.Instance](child.AsNode().FindChild("LowerJaw"))
			if jaw == Node3D.Nil {
				ce.jawCache[id] = &jawState{}
				continue
			}
			ce.idlePhase += 1.7
			st = &jawState{
				jaw:       jaw,
				restBasis: jaw.AsNode3D().Basis(),
				phase:     ce.idlePhase,
			}
			ce.jawCache[id] = st
		}
		if st.jaw == Node3D.Nil {
			continue
		}
		open := jawOpenCurve(ce.idleTime + st.phase)
		angle := open * jawMaxAngle
		basis := st.restBasis
		basis = Basis.Rotated(basis, Vector3.New(1, 0, 0), Angle.Radians(angle))
		st.jaw.AsNode3D().SetBasis(basis)
	}
}

func jawOpenCurve(t float32) float32 {
	phase := float32(math.Mod(float64(t)/float64(jawPeriod), 1))
	d := phase - 0.5
	if d < -0.1 || d > 0.1 {
		return 0
	}
	x := d * 10
	return 0.5 * (1 + float32(math.Cos(math.Pi*float64(x))))
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
	rig.radiusHandles = make([]spineHandle, len(bones))
	for i := range bones {
		rig.boneHandles[i] = ce.spawnHandle(container, boneHandleRadius, Color.RGBA{R: 0.95, G: 0.7, B: 0.2, A: 1}, tagBone, i, 0)
		rig.radiusHandles[i] = ce.spawnHandle(container, radiusHandleRadius, Color.RGBA{R: 0.4, G: 0.85, B: 1.0, A: 1}, tagRadius, i, 0)
	}
	rig.growHead = ce.spawnHandle(container, growNubRadius, Color.RGBA{R: 0.3, G: 1.0, B: 0.4, A: 1}, tagGrowHead, -1, +1)
	rig.growTail = ce.spawnHandle(container, growNubRadius, Color.RGBA{R: 0.3, G: 1.0, B: 0.4, A: 1}, tagGrowTail, -1, -1)
	rig.shrinkHead = ce.spawnHandle(container, growNubRadius, Color.RGBA{R: 1.0, G: 0.3, B: 0.3, A: 1}, tagShrinkHead, -1, +1)
	rig.shrinkTail = ce.spawnHandle(container, growNubRadius, Color.RGBA{R: 1.0, G: 0.3, B: 0.3, A: 1}, tagShrinkTail, -1, -1)

	ce.spineRig = rig
	ce.layoutSpineRig()
}

// spawnHandle creates one widget: a SphereMesh for the visual plus
// a sibling StaticBody3D + SphereShape3D on the spineHandleLayer
// so the editor's own raycast can find it. Returns the populated
// spineHandle struct so layoutSpineRig can move it each frame.
func (ce *CritterEditor) spawnHandle(parent Node3D.Instance, radius float32, color Color.RGBA, tag handleTag, boneIdx int, endpoint int) spineHandle {
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
		node:     root,
		body:     body,
		shape:    shape,
		sphere:   sphereShape,
		tag:      tag,
		boneIdx:  boneIdx,
		endpoint: endpoint,
	}
}

// layoutSpineRig snaps each handle to its current world position
// without rebuilding the rig — cheap, called each PhysicsProcess
// so handles track the body during drags.
func (ce *CritterEditor) layoutSpineRig() {
	if ce.spineRig == nil || ce.body.critter == nil {
		return
	}
	bones := ce.body.critter.Bones()
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
		// Radius handle sits above the bone — distance shows the
		// current radius — but with a minimum offset so it never
		// overlaps the bone handle (otherwise you can't grab it
		// once the radius shrinks far enough).
		const minRadiusHandleOffset = float32(0.14)
		off := b.Radius + 0.05
		if off < minRadiusHandleOffset {
			off = minRadiusHandleOffset
		}
		rp := Vector3.Add(bp, Vector3.XYZ{X: 0, Y: Float.X(off), Z: 0})
		ce.spineRig.radiusHandles[i].node.AsNode3D().SetGlobalPosition(rp)
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
