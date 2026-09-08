package internal

import (
	"graphics.gd/classdb/BaseMaterial3D"
	"graphics.gd/classdb/Input"
	"graphics.gd/classdb/InputEvent"
	"graphics.gd/classdb/InputEventKey"
	"graphics.gd/classdb/InputEventMouseButton"
	"graphics.gd/classdb/MeshInstance3D"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/PrismMesh"
	"graphics.gd/classdb/Shader"
	"graphics.gd/classdb/ShaderMaterial"
	"graphics.gd/classdb/StandardMaterial3D"
	"graphics.gd/classdb/Viewport"
	"graphics.gd/variant/Angle"
	"graphics.gd/variant/Basis"
	"graphics.gd/variant/Color"
	"graphics.gd/variant/Euler"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Plane"
	"graphics.gd/variant/Transform3D"
	"graphics.gd/variant/Vector3"
	"the.quetzal.community/aviary/internal/musical"
)

// coasterPieceScale scales the piece-space dimensions in
// [coasterPieces] to world space. Matches the Preview/Change scaling
// the editor applies to instantiated nodes. At 0.5 a Kenney cell is one
// metre, so the track lives on the same 1m grid the shelter editor uses.
const coasterPieceScale = 0.5

// coasterJoinTolerance is how close (metres) one piece's exit must be
// to another's entry for the two to count as connected when a placed
// track is walked (resume after reload, jump to an end).
const coasterJoinTolerance = 0.05

// CoasterEditor builds a roller-coaster track piece by piece, RCT-
// style, in an empty grid space like the shelter editor's (the world
// stays hidden while a track is built):
//
//   - With no track open, the selected station snaps to the grid cell
//     under the pointer and faces one of the four grid directions (the
//     side of the cell the pointer is on). Clicking starts a track
//     there, and the station's theme (wood, steel, flume...) becomes
//     the track's theme: the explorer hides every other theme until
//     the track is closed.
//   - Once a track is open the editor owns a cursor: the free end of
//     the track, shown as an arrow. Every track piece previews attached
//     to that cursor rather than under the pointer; clicking commits it
//     and advances the cursor, so a run of clicks lays a run of pieces.
//     Track pieces never place freely.
//   - Delete/Backspace pops the last piece; Ctrl+Z does the same
//     through the shared undo stack (and peers see either as a plain
//     removal). Right-click with nothing selected closes the track.
//   - Clicking a placed piece with nothing selected walks the placed
//     track to its two ends and re-opens it at whichever end is nearer
//     the click. The exit end builds onward; the entry end builds
//     backwards, so a station can be extended from either side. This
//     is also how a track is continued after a reload.
//
// Park props from the dressing tabs place on the grid plane: paths,
// queues and station furniture snap to cells, foliage and trains are
// free.
type CoasterEditor struct {
	Node3D.Extension[CoasterEditor]
	musical.Stubbed

	Objects Node3D.Instance
	Preview PreviewRenderer

	// Capability ports into the wider client — see editor_ports.go.
	recorder  Recorder
	library   Library
	workbench Workbench
	rig       CameraRig

	// cursor is the world-space pose of the open end of the track: the
	// exit of the last piece when building forward, the entry of the
	// first piece when building in reverse. In both cases +Z is the
	// direction of travel along the track at that point. When
	// cursorValid is true, track pieces preview at cursor instead of at
	// the pointer.
	cursor      Transform3D.BasisOrigin
	cursorValid bool
	reverse     bool

	// theme is the track's theme once one piece is on it ("" while no
	// track is open). The explorer hides other themes' pieces and
	// SelectDesign re-themes any that slip through.
	theme string

	// start is the snapped grid pose the selected piece would start a
	// new track from (refreshed every frame while no track is open).
	start      Transform3D.BasisOrigin
	startValid bool

	// chain remembers the placed entities of the open track in pop
	// order (last placed first to go) and the cursor before each, so
	// removing the last piece (Delete or undo) rewinds the cursor.
	chain []coasterChainEntry

	// marker is the arrow drawn at the cursor while a track is open.
	marker MeshInstance3D.Instance

	// grid_shader is the camera-cover grid (shared with shelter); the
	// grid plane is the floor the track is built on.
	grid_shader ShaderMaterial.ID

	design_to_entity map[musical.Design][]Node3D.ID
	entity_to_object map[musical.Entity]Node3D.ID
	object_to_entity map[Node3D.ID]musical.Entity
}

type coasterChainEntry struct {
	entity      musical.Entity
	priorCursor Transform3D.BasisOrigin
	priorValid  bool
}

// coasterFloor is the plane the track is built on in the grid space.
var coasterFloor = Plane.NormalD{Normal: Vector3.XYZ{0, 1, 0}}

func (editor *CoasterEditor) Ready() {
	editor.design_to_entity, editor.entity_to_object, editor.object_to_entity = newEntityMaps()
	editor.Preview.setDefaultScale(coasterPieceScale)

	// Cursor arrow: a flat triangle lying on the track, apex along the
	// cursor's +Z (the direction the next piece will run), drawn on top
	// of everything so it stays visible inside a station or a loop.
	mesh := PrismMesh.New()
	mesh.SetSize(Vector3.New(0.5, 0.5, 0.08))
	mat := StandardMaterial3D.New()
	mat.AsBaseMaterial3D().SetShadingMode(BaseMaterial3D.ShadingModeUnshaded)
	mat.AsBaseMaterial3D().SetAlbedoColor(Color.RGBA{R: 1, G: 0.65, B: 0.1, A: 1})
	mat.AsBaseMaterial3D().SetNoDepthTest(true)
	editor.marker = MeshInstance3D.New()
	editor.marker.SetMesh(mesh.AsMesh())
	editor.marker.AsGeometryInstance3D().SetMaterialOverride(mat.AsMaterial())
	editor.marker.AsNode3D().SetVisible(false)
	editor.AsNode().AddChild(editor.marker.AsNode())
}

func (*CoasterEditor) Name() string { return "coaster" }

func (*CoasterEditor) Views() []string          { return nil }
func (*CoasterEditor) SwitchToView(view string) {}

func (*CoasterEditor) Tabs(mode Mode) []string {
	switch mode {
	case ModeGeometry:
		return []string{
			"station",
			"track_f",
			"track_l",
			"track_r",
			"track_s",
		}
	case ModeDressing:
		return []string{
			"coaster-station",
			"coaster-queueing",
			"coaster-pathway",
			"coaster-support",
			"coaster-stall",
			"coaster-foliage",
			"coaster-train",
		}
	default:
		return TextureTabs
	}
}

func (editor *CoasterEditor) EnableEditor() {
	// Track pieces are laid by the cursor, never dragged, so there are
	// no gizmos: a coaster is edited by popping and re-laying pieces.
	editor.workbench.SetGizmos(nil)
	shader := ShaderMaterial.New()
	shader.SetShader(LoadSync[Shader.Instance]("res://shader/grid.gdshader"))
	shader.SetShaderParameter("center_offset", Vector3.New(0, 0, 0))
	editor.grid_shader = shader.ID()
	editor.rig.setCameraCover(shader.AsMaterial())
	editor.marker.AsNode3D().SetVisible(editor.cursorValid)
}

func (editor *CoasterEditor) ChangeEditor() {
	editor.marker.AsNode3D().SetVisible(false)
	// Hand the cover back to the default underwater post-process rather
	// than clearing it, so the waterline effect survives leaving coaster.
	editor.rig.applyCoverDefault()
}

// HidesDesign implements [DesignFilter]: while a track is open only its
// own theme's pieces are offered.
func (editor *CoasterEditor) HidesDesign(mode Mode, resource string) bool {
	if editor.theme == "" {
		return false
	}
	theme := coasterTheme(resource)
	return theme != "" && theme != editor.theme
}

func (editor *CoasterEditor) SelectDesign(mode Mode, design string) {
	if editor.theme != "" {
		design = coasterRetheme(design, editor.theme)
	}
	editor.Preview.SetDesign(design)
	signX := Float.X(coasterPieceScale)
	if piece, ok := coasterPieceForPath(design); ok && piece.mirror {
		signX = -signX
	}
	editor.Preview.AsNode3D().SetScale(Vector3.New(signX, coasterPieceScale, coasterPieceScale))
}

func (*CoasterEditor) SliderConfig(mode Mode, editing string) (init, min, max, step float64) {
	return 0, 0, 1, 0.01
}
func (*CoasterEditor) SliderHandle(mode Mode, editing string, value float64, commit bool) {}

func (editor *CoasterEditor) UnhandledInput(event InputEvent.Instance) {
	if !editor.AsNode3D().Visible() {
		return
	}
	if event, ok := Object.As[InputEventMouseButton.Instance](event); ok && event.AsInputEvent().IsPressed() {
		switch event.ButtonIndex() {
		case Input.MouseButtonRight:
			if editor.Preview.Design() != "" {
				editor.Preview.Remove()
			} else {
				editor.closeTrack()
			}
		case Input.MouseButtonLeft:
			if editor.Preview.Design() != "" {
				editor.commitPreview()
			} else {
				editor.resumeAtPointer()
			}
		}
	}
	if event, ok := Object.As[InputEventKey.Instance](event); ok {
		if isDeletePress(event) {
			editor.popLast()
		}
	}
}

// closeTrack drops the cursor so the next station starts a fresh track
// at the pointer instead of extending this one, and unlocks the theme.
func (editor *CoasterEditor) closeTrack() {
	editor.cursorValid = false
	editor.reverse = false
	editor.chain = editor.chain[:0]
	editor.marker.AsNode3D().SetVisible(false)
	editor.setTheme("")
}

// setTheme locks (or with "" unlocks) the track's theme and re-filters
// the explorer.
func (editor *CoasterEditor) setTheme(theme string) {
	if editor.theme == theme {
		return
	}
	editor.theme = theme
	editor.workbench.refreshDesignExplorer()
}

// coasterPlaced is a placed track piece with its entry and exit poses
// recovered from where its node sits.
type coasterPlaced struct {
	entity musical.Entity
	design string
	entry  Transform3D.BasisOrigin
	exit   Transform3D.BasisOrigin
}

// placedPieces recovers every placed track piece's entry and exit
// poses. The node sits where computePlacement put it (origin = entry -
// basis*entry_local), so both ends follow from its transform.
func (editor *CoasterEditor) placedPieces() map[musical.Entity]coasterPlaced {
	pieces := make(map[musical.Entity]coasterPlaced)
	for entity, id := range editor.entity_to_object {
		node, ok := id.Instance()
		if !ok {
			continue
		}
		design, found := findDesignInMap(editor.design_to_entity, Node3D.ID(node.ID()))
		if !found {
			continue
		}
		uri := editor.library.designURI(design)
		piece, ok := coasterPieceForPath(uri)
		if !ok {
			continue // a park prop, not track
		}
		basis := Basis.FromEuler(node.Rotation(), Angle.OrderXYZ)
		entry := Transform3D.BasisOrigin{
			Basis:  basis,
			Origin: Vector3.Add(node.Position(), Basis.Transform(Vector3.MulX(piece.entry, coasterPieceScale), basis)),
		}
		_, exit := editor.computePlacement(piece, entry)
		pieces[entity] = coasterPlaced{entity: entity, design: uri, entry: entry, exit: exit}
	}
	return pieces
}

// walkTrack returns the placed pieces connected to `from`, in track
// order from the entry end to the exit end. closed is true when the
// track loops back onto itself (no free end).
func walkTrack(pieces map[musical.Entity]coasterPlaced, from musical.Entity) (track []coasterPlaced, closed bool) {
	nextOf := func(p coasterPlaced) (coasterPlaced, bool) {
		for _, q := range pieces {
			if q.entity != p.entity && Vector3.Distance(p.exit.Origin, q.entry.Origin) < coasterJoinTolerance {
				return q, true
			}
		}
		return coasterPlaced{}, false
	}
	prevOf := func(p coasterPlaced) (coasterPlaced, bool) {
		for _, q := range pieces {
			if q.entity != p.entity && Vector3.Distance(q.exit.Origin, p.entry.Origin) < coasterJoinTolerance {
				return q, true
			}
		}
		return coasterPlaced{}, false
	}
	seen := map[musical.Entity]bool{from: true}
	track = []coasterPlaced{pieces[from]}
	for p := pieces[from]; ; {
		next, ok := nextOf(p)
		if !ok {
			break
		}
		if seen[next.entity] {
			return track, true
		}
		seen[next.entity] = true
		track = append(track, next)
		p = next
	}
	for p := pieces[from]; ; {
		prev, ok := prevOf(p)
		if !ok {
			break
		}
		if seen[prev.entity] {
			return track, true
		}
		seen[prev.entity] = true
		track = append([]coasterPlaced{prev}, track...)
		p = prev
	}
	return track, false
}

// resumeAtPointer re-opens the placed track under the pointer at
// whichever of its ends is nearer the click: the exit end to build
// onward, the entry end to build backwards. A closed loop has no end,
// so it re-opens forward at the clicked piece's exit.
func (editor *CoasterEditor) resumeAtPointer() {
	hover := MousePicker(editor.AsNode3D())
	node, ok := Object.As[Node3D.Instance](hover.Collider)
	if !ok {
		return
	}
	entity, _, found := editor.entityForNode(node)
	if !found {
		return
	}
	pieces := editor.placedPieces()
	if _, ok := pieces[entity]; !ok {
		return // a park prop, not track
	}
	track, closed := walkTrack(pieces, entity)
	if closed {
		// Rotate the loop so the clicked piece is last; forward from there.
		for i, p := range track {
			if p.entity == entity {
				track = append(track[i+1:], track[:i+1]...)
				break
			}
		}
	}
	head, tail := track[0], track[len(track)-1]
	reverse := !closed && Vector3.Distance(hover.Position, head.entry.Origin) < Vector3.Distance(hover.Position, tail.exit.Origin)

	editor.chain = editor.chain[:0]
	editor.reverse = reverse
	editor.cursorValid = false
	if reverse {
		// Pop order runs from the entry end back along the track, so
		// the chain is the track reversed; each piece's prior cursor is
		// its own exit (the entry of the piece that came before it).
		for i := len(track) - 1; i >= 0; i-- {
			p := track[i]
			editor.openTrack(p.entry, p.entity, p.design, p.exit, i < len(track)-1)
		}
	} else {
		for i, p := range track {
			editor.openTrack(p.exit, p.entity, p.design, p.entry, i > 0)
		}
	}
}

// openTrack sets the cursor after `entity` was laid (or resumed from),
// recording the pre-placement cursor so popping it can rewind, and
// locks the track's theme on the first themed piece.
func (editor *CoasterEditor) openTrack(next Transform3D.BasisOrigin, entity musical.Entity, design string, prior Transform3D.BasisOrigin, priorValid bool) {
	editor.cursor = next
	editor.cursorValid = true
	editor.chain = append(editor.chain, coasterChainEntry{entity: entity, priorCursor: prior, priorValid: priorValid})
	editor.marker.AsNode3D().SetVisible(true)
	if editor.theme == "" {
		editor.setTheme(coasterTheme(design))
	}
}

// inChain reports whether entity is already part of the open track.
func (editor *CoasterEditor) inChain(entity musical.Entity) bool {
	for _, e := range editor.chain {
		if e.entity == entity {
			return true
		}
	}
	return false
}

// entityForNode resolves a picked collider to the placed entity that
// owns it: library scenes nest the pickable mesh a level or two under
// the entity root.
func (editor *CoasterEditor) entityForNode(node Node3D.Instance) (musical.Entity, Node3D.Instance, bool) {
	for depth := 0; depth < 3 && node != Node3D.Nil; depth++ {
		if e, has := editor.object_to_entity[Node3D.ID(node.ID())]; has {
			return e, node, true
		}
		node = node.GetParentNode3d()
	}
	return musical.Entity{}, Node3D.Nil, false
}

// commitPreview commits the previewed design. Track pieces attach to
// the open cursor (or start a track at the snapped preview pose when
// none is open) and advance it; park props place where they preview.
func (editor *CoasterEditor) commitPreview() {
	design := editor.Preview.Design()
	if design == "" || !editor.recorder.recording() {
		return
	}
	piece, ok := coasterPieceForPath(design)
	if !ok {
		editor.commitProp(design)
		return
	}
	place := editor.cursor
	if !editor.cursorValid {
		if !editor.startValid {
			return
		}
		place = editor.start
	}
	worldTransform, next := editor.attach(piece, place)
	entity := editor.recorder.NextEntity()
	editor.openTrack(next, entity, design, editor.cursor, editor.cursorValid)

	placement := musical.Change{
		Entity: entity,
		Design: editor.library.MusicalDesign(design),
		Offset: worldTransform.Origin,
		Angles: Basis.AsEulerAngles(worldTransform.Basis, Angle.OrderXYZ),
		Editor: "coaster",
		Commit: true,
	}
	editor.recorder.publishChange(placement)
	editor.recorder.RecordChange(placement, musical.Change{
		Author: placement.Author,
		Entity: placement.Entity,
		Remove: true,
	})
	// The piece stays selected: the next click lays the same piece
	// again at the advanced cursor (straight, straight, straight...).
}

// commitProp places a park prop (dressing tab) where it previews.
func (editor *CoasterEditor) commitProp(design string) {
	placement := musical.Change{
		Entity: editor.recorder.NextEntity(),
		Design: editor.library.MusicalDesign(design),
		Offset: editor.Preview.AsNode3D().Position(),
		Angles: editor.Preview.AsNode3D().Rotation(),
		Editor: "coaster",
		Commit: true,
	}
	editor.recorder.publishChange(placement)
	editor.recorder.RecordChange(placement, musical.Change{
		Author: placement.Author,
		Entity: placement.Entity,
		Remove: true,
	})
	if !Input.IsKeyPressed(Input.KeyShift) {
		editor.Preview.Remove()
	}
}

// attach returns the world transform a piece is instantiated at when
// joined to the cursor, and the cursor after it: forward, the piece's
// entry lands on the cursor and the cursor moves to its exit; in
// reverse the piece's exit lands on the cursor and the cursor moves to
// its entry.
func (editor *CoasterEditor) attach(piece coasterPiece, cursor Transform3D.BasisOrigin) (Transform3D.BasisOrigin, Transform3D.BasisOrigin) {
	if !editor.reverse {
		return editor.computePlacement(piece, cursor)
	}
	entryWorld := Vector3.MulX(piece.entry, coasterPieceScale)
	exitWorld := Vector3.MulX(piece.exit, coasterPieceScale)
	basis := Basis.Mul(cursor.Basis, Basis.Inverse(Basis.FromEuler(piece.exitRotation, Angle.OrderXYZ)))
	pieceTransform := Transform3D.BasisOrigin{
		Basis:  basis,
		Origin: Vector3.Sub(cursor.Origin, Basis.Transform(exitWorld, basis)),
	}
	entry := Transform3D.BasisOrigin{
		Basis:  basis,
		Origin: Vector3.Add(pieceTransform.Origin, Basis.Transform(entryWorld, basis)),
	}
	return pieceTransform, entry
}

// computePlacement returns (a) the world transform at which the
// piece's mesh should be instantiated so its entry lands on `place`,
// and (b) the world pose of the piece's exit.
func (editor *CoasterEditor) computePlacement(piece coasterPiece, place Transform3D.BasisOrigin) (Transform3D.BasisOrigin, Transform3D.BasisOrigin) {
	entryWorld := Vector3.MulX(piece.entry, coasterPieceScale)
	exitWorld := Vector3.MulX(piece.exit, coasterPieceScale)

	pieceTransform := Transform3D.BasisOrigin{
		Basis:  place.Basis,
		Origin: Vector3.Sub(place.Origin, Basis.Transform(entryWorld, place.Basis)),
	}
	nextOrigin := Vector3.Add(place.Origin, Basis.Transform(Vector3.Sub(exitWorld, entryWorld), place.Basis))
	nextBasis := Basis.Mul(place.Basis, Basis.FromEuler(piece.exitRotation, Angle.OrderXYZ))
	return pieceTransform, Transform3D.BasisOrigin{Basis: nextBasis, Origin: nextOrigin}
}

// popLast removes the last piece of the open track (Delete key). The
// removal is a committed Change like any other, with the re-placement
// registered as its undo; the cursor rewinds in Change when the
// removal comes back through the space.
func (editor *CoasterEditor) popLast() {
	if len(editor.chain) == 0 {
		return
	}
	last := editor.chain[len(editor.chain)-1]
	node, ok := editor.entity_to_object[last.entity].Instance()
	if !ok {
		editor.chain = editor.chain[:len(editor.chain)-1]
		return
	}
	design, _ := findDesignInMap(editor.design_to_entity, Node3D.ID(node.ID()))
	removal := musical.Change{
		Entity: last.entity,
		Editor: "coaster",
		Remove: true,
		Commit: true,
	}
	editor.recorder.publishChange(removal)
	editor.recorder.RecordChange(removal, musical.Change{
		Author: removal.Author,
		Entity: last.entity,
		Design: design,
		Offset: node.Position(),
		Angles: node.Rotation(),
		Editor: "coaster",
	})
}

// pointerOnFloor returns where the pointer's ray meets the grid floor.
func (editor *CoasterEditor) pointerOnFloor() (Vector3.XYZ, bool) {
	mouse := Viewport.Get(editor.AsNode()).GetMousePosition()
	cam := editor.rig.viewportCamera()
	return Plane.IntersectsRay(coasterFloor, cam.ProjectRayOrigin(mouse), cam.ProjectRayNormal(mouse))
}

// snapToGrid returns the pointer's floor hit snapped to the 1m grid,
// facing the grid direction of the side of the cell the pointer is on
// (the shelter editor's quadrant trick).
func (editor *CoasterEditor) snapToGrid() (Transform3D.BasisOrigin, bool) {
	point, ok := editor.pointerOnFloor()
	if !ok {
		return Transform3D.BasisOrigin{}, false
	}
	cell := Vector3.New(Float.Round(point.X), 0, Float.Round(point.Z))
	dx, dz := point.X-cell.X, point.Z-cell.Z
	// A piece runs along its local +Z; make that the direction from the
	// cell centre towards the pointer, rounded to a quarter turn.
	quarter := Float.X(Angle.Pi / 2)
	facing := Angle.Radians(Float.Round(Float.X(Angle.Atan2(dx, dz))/quarter) * quarter)
	return Transform3D.BasisOrigin{
		Basis:  Basis.FromEuler(Euler.Radians{Y: facing}, Angle.OrderXYZ),
		Origin: cell,
	}, true
}

// coasterSnappedProp reports whether a park prop category lays out on
// the grid (paths, queues, station furniture, stalls) rather than
// freely (foliage, trains).
func coasterSnappedProp(category string) bool {
	switch category {
	case "coaster-pathway", "coaster-queueing", "coaster-station", "coaster-stall", "coaster-support":
		return true
	}
	return false
}

func (editor *CoasterEditor) PhysicsProcess(_ Float.X) {
	if editor.cursorValid {
		editor.marker.AsNode3D().SetGlobalTransform(editor.markerTransform())
	}
	design := editor.Preview.Design()
	if design == "" {
		return
	}
	piece, ok := coasterPieceForPath(design)
	if !ok {
		// Park props: snapped or free on the grid floor.
		if coasterSnappedProp(designCategory(design)) {
			if snap, ok := editor.snapToGrid(); ok {
				editor.Preview.AsNode3D().SetGlobalTransform(Transform3D.BasisOrigin{
					Basis:  Basis.Scaled(snap.Basis, editor.Preview.AsNode3D().Scale()),
					Origin: snap.Origin,
				})
			}
		} else if point, ok := editor.pointerOnFloor(); ok {
			editor.Preview.AsNode3D().SetGlobalPosition(point)
		}
		return
	}

	scale := Vector3.New(coasterPieceScale, coasterPieceScale, coasterPieceScale)
	if piece.mirror {
		scale.X = -scale.X
	}
	place := editor.cursor
	if !editor.cursorValid {
		// No track open: the piece starts one, snapped to the grid with
		// its rails resting on the floor.
		snap, ok := editor.snapToGrid()
		editor.startValid = ok
		if !ok {
			return
		}
		snap.Origin.Y += coasterTrackLift * coasterPieceScale
		editor.start = snap
		place = snap
	}
	pieceTransform, _ := editor.attach(piece, place)
	editor.Preview.AsNode3D().
		SetGlobalPosition(pieceTransform.Origin).
		SetGlobalRotation(Basis.AsEulerAngles(pieceTransform.Basis, Angle.OrderXYZ)).
		SetScale(scale)
}

// markerTransform lays the arrow flat on the cursor, apex pointing the
// way the next piece will extend the track (backwards when building
// in reverse).
func (editor *CoasterEditor) markerTransform() Transform3D.BasisOrigin {
	basis := editor.cursor.Basis
	if editor.reverse {
		basis = Basis.Mul(basis, Basis.FromEuler(Euler.Radians{Y: Angle.Pi}, Angle.OrderXYZ))
	}
	// PrismMesh's apex is +Y; tip it forward onto +Z.
	basis = Basis.Mul(basis, Basis.FromEuler(Euler.Radians{X: Angle.Pi / 2}, Angle.OrderXYZ))
	return Transform3D.BasisOrigin{Basis: basis, Origin: editor.cursor.Origin}
}

func (editor *CoasterEditor) Change(change musical.Change) error {
	if change.Editor != "coaster" {
		return nil
	}
	container := editor.Objects.AsNode()
	scale := editor.designScale(change.Design)
	exists, ok := editor.entity_to_object[change.Entity].Instance()
	if ok {
		if change.Remove {
			removeEntity(editor.design_to_entity, editor.entity_to_object, editor.object_to_entity, change.Design, change.Entity, exists)
			editor.unchain(change.Entity)
			return nil
		}
		exists.
			SetPosition(change.Offset).
			SetRotation(change.Angles).
			SetScale(scale)
		return nil
	}
	if change.Remove {
		return nil
	}
	node := editor.library.instantiateDesign(change.Design)
	node.
		SetPosition(change.Offset).
		SetRotation(change.Angles).
		SetScale(scale)
	registerEntity(editor.design_to_entity, editor.entity_to_object, editor.object_to_entity, change.Design, change.Entity, node)
	container.AddChild(node.AsNode())
	editor.chainArrival(change.Entity)
	return nil
}

// chainArrival extends the open track with a piece that has just
// appeared joined to the cursor: a redo of a popped piece, or a peer
// building on this track. Pieces this client laid itself are already
// chained by commitPreview.
func (editor *CoasterEditor) chainArrival(entity musical.Entity) {
	if !editor.cursorValid || editor.inChain(entity) {
		return
	}
	pieces := editor.placedPieces()
	p, ok := pieces[entity]
	if !ok {
		return
	}
	if editor.reverse {
		if Vector3.Distance(p.exit.Origin, editor.cursor.Origin) < coasterJoinTolerance {
			editor.openTrack(p.entry, entity, p.design, editor.cursor, true)
		}
		return
	}
	if Vector3.Distance(p.entry.Origin, editor.cursor.Origin) < coasterJoinTolerance {
		editor.openTrack(p.exit, entity, p.design, editor.cursor, true)
	}
}

// unchain drops a removed entity from the open track. Removing the
// last piece (Delete, or an undo of its placement — from this client
// or a peer) rewinds the cursor to where it was before that piece;
// removing an earlier piece just forgets it, the cursor stays. When
// the last piece goes the track closes and the theme unlocks.
func (editor *CoasterEditor) unchain(entity musical.Entity) {
	for i := len(editor.chain) - 1; i >= 0; i-- {
		if editor.chain[i].entity != entity {
			continue
		}
		if i == len(editor.chain)-1 {
			last := editor.chain[i]
			editor.cursor = last.priorCursor
			editor.cursorValid = last.priorValid
			editor.marker.AsNode3D().SetVisible(editor.cursorValid)
		}
		editor.chain = append(editor.chain[:i], editor.chain[i+1:]...)
		if len(editor.chain) == 0 && !editor.cursorValid {
			editor.closeTrack()
		}
		return
	}
}

// designScale returns the world-space scale for a coaster entity,
// flipping X when the design is sourced from track_r (right turn).
// Falls back to the standard pieceScale when the design path isn't a
// known coaster path (park props in dressing tabs).
func (editor *CoasterEditor) designScale(design musical.Design) Vector3.XYZ {
	scale := Vector3.New(coasterPieceScale, coasterPieceScale, coasterPieceScale)
	resource := editor.library.designURI(design)
	if resource == "" {
		return scale
	}
	piece, ok := coasterPieceForPath(resource)
	if !ok {
		return scale
	}
	if piece.mirror {
		scale.X = -scale.X
	}
	return scale
}
