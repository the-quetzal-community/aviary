package internal

import (
	"graphics.gd/classdb/BaseMaterial3D"
	"graphics.gd/classdb/Input"
	"graphics.gd/classdb/InputEvent"
	"graphics.gd/classdb/InputEventKey"
	"graphics.gd/classdb/InputEventMouseButton"
	"graphics.gd/classdb/MeshInstance3D"
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/PrismMesh"
	"graphics.gd/classdb/Shader"
	"graphics.gd/classdb/ShaderMaterial"
	"graphics.gd/classdb/StandardMaterial3D"
	"graphics.gd/variant/Angle"
	"graphics.gd/variant/Basis"
	"graphics.gd/variant/Color"
	"graphics.gd/variant/Euler"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Transform3D"
	"graphics.gd/variant/Vector3"
	"the.quetzal.community/aviary/internal/musical"
)

// coasterPieceScale scales the piece-space dimensions in
// [coasterPieces] to world space. Matches the Preview/Change scaling
// the editor applies to instantiated nodes. At 0.5 a Kenney cell is one
// metre, so the track lives on the same 1m grid the shelter editor uses.
const coasterPieceScale = 0.5

// CoasterEditor builds a roller-coaster track piece by piece, RCT-
// style, on a 1m grid drawn over the terrain:
//
//   - With no track open, whatever piece is selected (normally the
//     station) snaps to the grid cell under the pointer, faces one of
//     the four grid directions (the side of the cell the pointer is
//     on) and seats on the terrain. Clicking starts a track there.
//   - Once a track is open the editor owns a cursor: the world-space
//     end of the last piece, shown as an arrow. Every track piece
//     previews attached to that cursor rather than under the pointer;
//     clicking commits it and advances the cursor, so a run of clicks
//     lays a run of pieces. Track pieces never place freely.
//   - Delete/Backspace pops the last piece; Ctrl+Z does the same
//     through the shared undo stack (and peers see either as a plain
//     removal). Right-click with nothing selected closes the track.
//   - Clicking a placed piece with nothing selected re-opens the
//     track at that piece's exit, which is also how a track is
//     continued after a reload.
//
// Park props from the dressing tabs place like scenery: paths, queues
// and station furniture snap to the grid, foliage and trains are free.
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

	// terrain is the terrain editor, woken while coastering so track
	// pieces can be seated against live ground heights.
	terrain *TerrainEditor

	// cursor is the world-space transform of the end of the last
	// placed piece. When cursorValid is true, track pieces preview at
	// cursor instead of at the pointer.
	cursor      Transform3D.BasisOrigin
	cursorValid bool

	// start is the snapped grid pose the selected piece would start a
	// new track from (refreshed every frame while no track is open).
	start      Transform3D.BasisOrigin
	startValid bool

	// chain remembers the order of placed entities on the current
	// track and the pre-placement cursor for each, so removing the last
	// piece (Delete or undo) rewinds the cursor.
	chain []coasterChainEntry

	// marker is the arrow drawn at the cursor while a track is open.
	marker MeshInstance3D.Instance

	// grid_shader is the camera-cover grid (shared with shelter) whose
	// plane is kept at the level the track is being built on.
	grid_shader ShaderMaterial.ID
	grid_level  Float.X

	design_to_entity map[musical.Design][]Node3D.ID
	entity_to_object map[musical.Entity]Node3D.ID
	object_to_entity map[Node3D.ID]musical.Entity
}

type coasterChainEntry struct {
	entity      musical.Entity
	priorCursor Transform3D.BasisOrigin
	priorValid  bool
}

func (editor *CoasterEditor) Ready() {
	editor.design_to_entity, editor.entity_to_object, editor.object_to_entity = newEntityMaps()
	editor.Preview.setDefaultScale(coasterPieceScale)

	// Cursor arrow: a flat triangular prism pointing along the cursor's
	// +Z (the direction the next piece will run), drawn on top of
	// everything so it stays visible where the track dips into terrain.
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
	editor.terrain.AsNode().SetProcessMode(Node.ProcessModeInherit)
	shader := ShaderMaterial.New()
	shader.SetShader(LoadSync[Shader.Instance]("res://shader/grid.gdshader"))
	editor.grid_shader = shader.ID()
	editor.rig.setCameraCover(shader.AsMaterial())
	editor.setGridLevel(editor.grid_level)
	editor.marker.AsNode3D().SetVisible(editor.cursorValid)
}

func (editor *CoasterEditor) ChangeEditor() {
	editor.terrain.AsNode().SetProcessMode(Node.ProcessModeDisabled)
	editor.marker.AsNode3D().SetVisible(false)
	// Hand the cover back to the default underwater post-process rather
	// than clearing it, so the waterline effect survives leaving coaster.
	editor.rig.applyCoverDefault()
}

// setGridLevel moves the grid plane to the height the track is being
// built at (ground under the station, or the open cursor's ground).
func (editor *CoasterEditor) setGridLevel(level Float.X) {
	editor.grid_level = level
	if shader, ok := editor.grid_shader.Instance(); ok {
		shader.SetShaderParameter("center_offset", Vector3.New(0, level, 0))
	}
}

func (editor *CoasterEditor) SelectDesign(mode Mode, design string) {
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

// closeTrack drops the cursor so the next track piece starts a fresh
// track at the pointer instead of extending this one.
func (editor *CoasterEditor) closeTrack() {
	editor.cursorValid = false
	editor.chain = editor.chain[:0]
	editor.marker.AsNode3D().SetVisible(false)
}

// resumeAtPointer re-opens the track at the exit of the placed piece
// under the pointer, so a track can be continued after a reload or
// after being closed.
func (editor *CoasterEditor) resumeAtPointer() {
	hover := MousePicker(editor.AsNode3D())
	node, ok := Object.As[Node3D.Instance](hover.Collider)
	if !ok {
		return
	}
	entity, owner, found := editor.entityForNode(node)
	if !found {
		return
	}
	design, found := findDesignInMap(editor.design_to_entity, Node3D.ID(owner.ID()))
	if !found {
		return
	}
	piece, ok := coasterPieceForPath(editor.library.designURI(design))
	if !ok {
		return // a park prop, not track
	}
	// The node sits where computePlacement put it: origin = place -
	// basis*entry. Recover the entry point and advance to the exit.
	basis := Basis.FromEuler(owner.Rotation(), Angle.OrderXYZ)
	place := Transform3D.BasisOrigin{
		Basis:  basis,
		Origin: Vector3.Add(owner.Position(), Basis.Transform(Vector3.MulX(piece.entry, coasterPieceScale), basis)),
	}
	_, next := editor.computePlacement(piece, place)
	editor.chain = editor.chain[:0]
	editor.openTrack(next, entity, editor.cursor, editor.cursorValid)
}

// openTrack sets the cursor after `entity` was laid (or resumed from),
// recording the pre-placement cursor so popping it can rewind.
func (editor *CoasterEditor) openTrack(next Transform3D.BasisOrigin, entity musical.Entity, prior Transform3D.BasisOrigin, priorValid bool) {
	editor.cursor = next
	editor.cursorValid = true
	editor.chain = append(editor.chain, coasterChainEntry{entity: entity, priorCursor: prior, priorValid: priorValid})
	editor.marker.AsNode3D().SetVisible(true)
	editor.setGridLevel(next.Origin.Y - coasterTrackLift*coasterPieceScale)
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
	worldTransform, next := editor.computePlacement(piece, place)
	entity := editor.recorder.NextEntity()
	editor.openTrack(next, entity, editor.cursor, editor.cursorValid)

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

// computePlacement returns (a) the world transform at which the
// piece's mesh should be instantiated so its entry lands on `place`,
// and (b) the new world cursor at the piece's exit.
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

// snapToGrid returns the pointer's terrain hit snapped to the 1m grid,
// facing the grid direction of the side of the cell the pointer is on
// (the shelter editor's quadrant trick), seated at the terrain height
// of that cell.
func (editor *CoasterEditor) snapToGrid() (Transform3D.BasisOrigin, bool) {
	hover := MousePicker(editor.AsNode3D())
	if !Object.Is[*TerrainTile](hover.Collider) {
		return Transform3D.BasisOrigin{}, false
	}
	point := hover.Position
	cell := Vector3.New(Float.Round(point.X), 0, Float.Round(point.Z))
	dx, dz := point.X-cell.X, point.Z-cell.Z
	// A piece runs along its local +Z; make that the direction from the
	// cell centre towards the pointer, rounded to a quarter turn.
	quarter := Float.X(Angle.Pi / 2)
	facing := Angle.Radians(Float.Round(Float.X(Angle.Atan2(dx, dz))/quarter) * quarter)
	if editor.terrain != nil {
		cell.Y = editor.terrain.HeightAt(cell)
	} else {
		cell.Y = point.Y
	}
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
		editor.marker.AsNode3D().SetGlobalTransform(editor.cursor)
	}
	design := editor.Preview.Design()
	if design == "" {
		return
	}
	piece, ok := coasterPieceForPath(design)
	if !ok {
		// Park props: snapped or free terrain hover like Scenery.
		if coasterSnappedProp(designCategory(design)) {
			if snap, ok := editor.snapToGrid(); ok {
				editor.Preview.AsNode3D().SetGlobalTransform(Transform3D.BasisOrigin{
					Basis:  Basis.Scaled(snap.Basis, editor.Preview.AsNode3D().Scale()),
					Origin: snap.Origin,
				})
			}
		} else if hover := MousePicker(editor.AsNode3D()); Object.Is[*TerrainTile](hover.Collider) {
			editor.Preview.AsNode3D().SetGlobalPosition(hover.Position)
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
		// its rails lifted onto the ground.
		snap, ok := editor.snapToGrid()
		editor.startValid = ok
		if !ok {
			return
		}
		snap.Origin.Y += coasterTrackLift * coasterPieceScale
		editor.setGridLevel(snap.Origin.Y - coasterTrackLift*coasterPieceScale)
		editor.start = snap
		place = snap
	}
	pieceTransform, _ := editor.computePlacement(piece, place)
	editor.Preview.AsNode3D().
		SetGlobalPosition(pieceTransform.Origin).
		SetGlobalRotation(Basis.AsEulerAngles(pieceTransform.Basis, Angle.OrderXYZ)).
		SetScale(scale)
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
	return nil
}

// unchain drops a removed entity from the open track. Removing the
// last piece (Delete, or an undo of its placement — from this client
// or a peer) rewinds the cursor to where it was before that piece;
// removing an earlier piece just forgets it, the cursor stays.
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
			if editor.cursorValid {
				editor.setGridLevel(editor.cursor.Origin.Y - coasterTrackLift*coasterPieceScale)
			}
		}
		editor.chain = append(editor.chain[:i], editor.chain[i+1:]...)
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
