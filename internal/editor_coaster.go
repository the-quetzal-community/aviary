package internal

import (
	"graphics.gd/classdb/Input"
	"graphics.gd/classdb/InputEvent"
	"graphics.gd/classdb/InputEventKey"
	"graphics.gd/classdb/InputEventMouseButton"
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/variant/Angle"
	"graphics.gd/variant/Basis"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Transform3D"
	"graphics.gd/variant/Vector3"
	"the.quetzal.community/aviary/internal/musical"
)

// coasterPieceScale scales the piece-space dimensions in
// [coasterPieces] to world space. Matches the Preview/Change scaling
// the editor applies to instantiated nodes.
const coasterPieceScale = 0.5

// CoasterEditor builds a roller-coaster track piece by piece, RCT-
// style: place a station to start, then pick the next piece (straight,
// turn, hill, loop) from a tab and click to commit it to the end of
// the chain. The piece's geometry — measured from the Kenney Coaster
// Kit GLBs in [coasterPieces] — tells the editor where the cursor
// should advance to for the following piece.
type CoasterEditor struct {
	Node3D.Extension[CoasterEditor]
	musical.Stubbed

	Objects Node3D.Instance
	Preview PreviewRenderer

	client *Client

	// cursor is the world-space transform of the end of the last
	// placed piece. When cursorValid is true, non-startable pieces
	// preview at cursor instead of at the mouse.
	cursor      Transform3D.BasisOrigin
	cursorValid bool

	// chain remembers the order of placed entities on the current
	// track and the pre-placement cursor for each, so Delete can pop
	// the last piece and rewind the cursor.
	chain []coasterChainEntry

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
	editor.client.SetGizmos(placementGizmos)
	editor.client.TerrainEditor.AsNode().SetProcessMode(Node.ProcessModeInherit)
}
func (editor *CoasterEditor) ChangeEditor() {
	editor.client.TerrainEditor.AsNode().SetProcessMode(Node.ProcessModeDisabled)
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
	if event, ok := Object.As[InputEventMouseButton.Instance](event); ok {
		if event.ButtonIndex() == Input.MouseButtonRight && event.AsInputEvent().IsPressed() {
			editor.Preview.Remove()
		}
		if event.ButtonIndex() == Input.MouseButtonLeft && event.AsInputEvent().IsPressed() {
			editor.commitPreview()
		}
	}
	if event, ok := Object.As[InputEventKey.Instance](event); ok {
		if isDeletePress(event) {
			editor.undoLast()
		}
	}
}

// commitPreview commits the previewed piece. For a startable piece
// (a station), the preview's current world position/orientation
// becomes the new track origin. For chained pieces, the preview is
// snapped to the active cursor before commit and the cursor advances
// past the just-placed piece.
func (editor *CoasterEditor) commitPreview() {
	design := editor.Preview.Design()
	if design == "" {
		return
	}
	piece, ok := coasterPieceForPath(design)
	if !ok {
		editor.client.space.Change(musical.Change{
			Author: editor.client.id,
			Entity: editor.client.NextEntity(),
			Design: editor.client.MusicalDesign(design),
			Offset: editor.Preview.AsNode3D().Position(),
			Angles: editor.Preview.AsNode3D().Rotation(),
			Editor: "coaster",
			Commit: true,
		})
		if !Input.IsKeyPressed(Input.KeyShift) {
			editor.Preview.Remove()
		}
		return
	}
	if !editor.cursorValid && !piece.startable {
		return
	}

	var place Transform3D.BasisOrigin
	if piece.startable {
		place = Transform3D.BasisOrigin{
			Basis:  Basis.FromEuler(editor.Preview.AsNode3D().Rotation(), Angle.OrderXYZ),
			Origin: editor.Preview.AsNode3D().Position(),
		}
	} else {
		place = editor.cursor
	}

	priorCursor := editor.cursor
	priorValid := editor.cursorValid
	worldTransform, nextCursor := editor.computePlacement(piece, place)

	entity := editor.client.NextEntity()
	editor.cursor = nextCursor
	editor.cursorValid = true
	editor.chain = append(editor.chain, coasterChainEntry{
		entity:      entity,
		priorCursor: priorCursor,
		priorValid:  priorValid,
	})

	editor.client.space.Change(musical.Change{
		Author: editor.client.id,
		Entity: entity,
		Design: editor.client.MusicalDesign(design),
		Offset: worldTransform.Origin,
		Angles: Basis.AsEulerAngles(worldTransform.Basis, Angle.OrderXYZ),
		Editor: "coaster",
		Commit: true,
	})
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

func (editor *CoasterEditor) undoLast() {
	if len(editor.chain) == 0 {
		return
	}
	last := editor.chain[len(editor.chain)-1]
	editor.chain = editor.chain[:len(editor.chain)-1]
	editor.cursor = last.priorCursor
	editor.cursorValid = last.priorValid
	editor.client.space.Change(musical.Change{
		Author: editor.client.id,
		Entity: last.entity,
		Editor: "coaster",
		Remove: true,
		Commit: true,
	})
}

func (editor *CoasterEditor) PhysicsProcess(_ Float.X) {
	design := editor.Preview.Design()
	if design == "" {
		return
	}
	piece, ok := coasterPieceForPath(design)
	if !ok {
		// Park props: track terrain hover like Scenery.
		if hover := MousePicker(editor.AsNode3D()); Object.Is[*TerrainTile](hover.Collider) {
			editor.Preview.AsNode3D().SetGlobalPosition(hover.Position)
		}
		return
	}

	if piece.startable || !editor.cursorValid {
		if hover := MousePicker(editor.AsNode3D()); Object.Is[*TerrainTile](hover.Collider) {
			editor.Preview.AsNode3D().SetGlobalPosition(hover.Position)
		}
		return
	}

	entryWorld := Vector3.MulX(piece.entry, coasterPieceScale)
	pos := Vector3.Sub(editor.cursor.Origin, Basis.Transform(entryWorld, editor.cursor.Basis))
	editor.Preview.AsNode3D().
		SetGlobalPosition(pos).
		SetGlobalRotation(Basis.AsEulerAngles(editor.cursor.Basis, Angle.OrderXYZ))
	scale := coasterPieceScale
	signX := Float.X(scale)
	if piece.mirror {
		signX = -signX
	}
	editor.Preview.AsNode3D().SetScale(Vector3.New(signX, Float.X(scale), Float.X(scale)))
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
			return nil
		}
		exists.
			SetPosition(change.Offset).
			SetRotation(change.Angles).
			SetScale(scale)
		return nil
	}
	node := editor.client.instantiateDesign(change.Design)
	node.
		SetPosition(change.Offset).
		SetRotation(change.Angles).
		SetScale(scale)
	registerEntity(editor.design_to_entity, editor.entity_to_object, editor.object_to_entity, change.Design, change.Entity, node)
	container.AddChild(node.AsNode())
	return nil
}

// designScale returns the world-space scale for a coaster entity,
// flipping X when the design is sourced from track_r (right turn).
// Falls back to the standard pieceScale when the design path isn't a
// known coaster path (park props in dressing tabs).
func (editor *CoasterEditor) designScale(design musical.Design) Vector3.XYZ {
	scale := Vector3.New(coasterPieceScale, coasterPieceScale, coasterPieceScale)
	resource, ok := editor.client.design_to_string[design]
	if !ok {
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
