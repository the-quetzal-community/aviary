package internal

import (
	"fmt"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"

	"graphics.gd/classdb/Engine"
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/VisualInstance3D"
	"graphics.gd/variant/Angle"
	"graphics.gd/variant/Euler"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Transform3D"
	"graphics.gd/variant/Vector3"
	"the.quetzal.community/aviary/internal/musical"
)

// librarySizesFile returns the path of the library repo's sizes.txt when
// the library-sizing debug mode is active (AVIARY_LIBRARY_SIZES set), or ""
// otherwise. The mode gates the F2 persist key, the scenery editor's
// GizmoScale toolbar entry and the live size-override preview below.
func librarySizesFile() string { return os.Getenv("AVIARY_LIBRARY_SIZES") }

// librarySizeOverride is one parsed sizes.txt entry: the model's intended
// in-world height, optionally where its visual base should sit relative to
// the seat reference, and optionally where its horizontal geometry centre
// should sit relative to the origin, in the model's own axes (shelter
// parts dialled to a grid-cell edge — see rescale_glb.py for the format).
type librarySizeOverride struct {
	height           Float.X
	offset           Float.X
	hasOffset        bool
	offsetX, offsetZ Float.X
	hasXZ            bool
	rotation         Float.X // degrees about Y, geometry facing correction
	hasRotation      bool
}

// librarySizeOverrides lazily parses the sizes.txt the library-sizing debug
// mode points at, keyed by model ("everything/housing/1"). Nil outside debug
// mode, so the per-entity/per-preview hooks cost one map-nil check. Package
// global (not Client state) because the PreviewRenderer needs it too and has
// no client back-reference. The map is mutable: F2 updates the measured
// model's entry in place so the rest of the session previews the new
// numbers without a restart (main-thread only, like all the hooks).
var librarySizeOverrides = sync.OnceValue(loadLibrarySizeOverrides)

func loadLibrarySizeOverrides() map[string]librarySizeOverride {
	file := librarySizesFile()
	if file == "" {
		return nil
	}
	data, err := os.ReadFile(file)
	if err != nil {
		Engine.Raise(fmt.Errorf("library sizes: %w", err))
		return nil
	}
	overrides := make(map[string]librarySizeOverride)
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(strings.SplitN(line, "#", 2)[0])
		if len(fields) < 2 {
			continue
		}
		height, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			continue
		}
		override := librarySizeOverride{height: Float.X(height)}
		if len(fields) > 2 {
			if offset, err := strconv.ParseFloat(fields[2], 64); err == nil {
				override.offset, override.hasOffset = Float.X(offset), true
			}
		}
		// Optional X/Z pair (lines without it parse exactly as before),
		// then an optional rotation in degrees.
		if len(fields) > 4 {
			x, errX := strconv.ParseFloat(fields[3], 64)
			z, errZ := strconv.ParseFloat(fields[4], 64)
			if errX == nil && errZ == nil {
				override.offsetX, override.offsetZ, override.hasXZ = Float.X(x), Float.X(z), true
			}
		}
		if len(fields) > 5 {
			if rot, err := strconv.ParseFloat(fields[5], 64); err == nil {
				override.rotation, override.hasRotation = Float.X(rot), true
			}
		}
		overrides[fields[0]] = override
	}
	return overrides
}

// applyLibrarySizeOverride previews a sizes.txt entry on a placed entity:
// the node is scaled so its visual height matches the listed height, and —
// for terrain-seated (scenery) placement, mirroring rescale_glb.py's
// grounding rule — lifted so its base sits at the terrain + offset
// (explicit offset wins; everything/* defaults to 0; other packs keep
// their authored origin). Grid-anchored editors (shelter,
// terrainSeated=false) instead have their geometry children transformed to
// the recorded facing/centre/base — see shiftGeometryToOverride. The node is
// changed locally only, no musical Change is emitted: this is a debug view
// of what the .glb bake will produce, not a world mutation, so other
// clients are unaffected. Idempotent — once the bake matches, the measured
// factor converges to 1.
func (world *Client) applyLibrarySizeOverride(entity musical.Entity, design musical.Design, node Node3D.Instance, terrainSeated bool) {
	override, model, listed := world.libraryOverrideFor(design)
	if !listed {
		return
	}
	lo, hi, found := worldVisualYRange(node.AsNode())
	if !found || hi-lo <= 0.001 {
		return
	}
	factor := override.height / (hi - lo)
	if Float.Abs(factor-1) > 0.001 {
		node.SetScale(Vector3.MulX(node.Scale(), factor))
	}
	// Scenery (terrain-seated) NODE-seats against the terrain. A listed
	// model grounds even when the entity carries a float lift: the
	// gizmoFloat lifts dialled while sizing a pre-bake (origin-centred)
	// mesh stay in the musical log, and once rescale_glb.py re-grounds the
	// mesh they double-count on reload — the entity floats above the
	// terrain by the stale lift. The F2-recorded offset already captures
	// where the dialled base sat, so seating base = terrain + offset
	// reproduces the dialled look both before and after the bake.
	// (Debug-mode trade-off: a float dialled on a listed scenery model
	// AFTER its last F2 snaps back to the file's offset on reload — press
	// F2 to persist a re-dial.)
	if terrainSeated && (override.hasOffset || strings.HasPrefix(model, "everything/")) {
		if world.TerrainEditor == nil {
			return
		}
		pos := node.Position()
		seat := world.TerrainEditor.HeightAt(Vector3.New(pos.X, 0, pos.Z))
		// The geometry scales about the node origin, so re-derive where the
		// base ended up after the SetScale above.
		base := pos.Y + (lo-pos.Y)*factor
		if delta := seat + override.offset - base; Float.Abs(delta) > 0.001 {
			pos.Y += delta
			node.SetPosition(pos)
		}
		return
	}
	// Grid-anchored editors (shelter) instead shift the GEOMETRY inside the
	// node — exactly what the bake does to the .glb — so fresh placements
	// preview the dialled in-cell position without ever touching the node's
	// recorded transform (gizmo shifts, float lifts and dressing dropped on
	// surfaces are never yanked; an earlier node-seating attempt pulled
	// tabletop parts to the floor plane).
	if !terrainSeated && (override.hasOffset || override.hasXZ) {
		shiftGeometryToOverride(node, override)
	}
}

// libraryOverrideFor resolves the sizes.txt override entry (and the model
// key) behind a design, or listed=false when the design isn't a library
// resource or carries no entry. Shared by applyLibrarySizeOverride and the
// shelter Change handler's placement-only hook.
func (world *Client) libraryOverrideFor(design musical.Design) (override librarySizeOverride, model string, listed bool) {
	overrides := librarySizeOverrides()
	if len(overrides) == 0 {
		return librarySizeOverride{}, "", false
	}
	resource := world.design_to_string[design]
	if !strings.HasPrefix(resource, "res://library/") {
		return librarySizeOverride{}, "", false
	}
	model = strings.TrimSuffix(strings.TrimPrefix(resource, "res://library/"), path.Ext(resource))
	override, listed = overrides[model]
	return override, model, listed
}

// libraryRotApplied tracks the rotation override already applied to a placed
// node's geometry children, so re-applications (the load sweep, gizmo-update
// hooks) rotate only the difference instead of compounding. Position shifts
// need no such record — they re-derive from the measured bounds — but a
// rotation cannot be read back from geometry. Debug-mode only, bounded by
// the part count.
var libraryRotApplied = map[Node3D.ID]Float.X{}

// shiftGeometryToOverride mirrors rescale_glb.py's placement bake on a
// grid-anchored (shelter) part by transforming the node's geometry children:
// the bake turns the model's facing by `rotation` degrees, grounds its base
// `offset` metres above its origin and places its horizontal geometry centre
// at `offsetX/Z` metres from the origin, in the model's own axes. The
// rotation is applied about the node origin — the absolute centring right
// after makes that equivalent to rotating about the mesh's own XZ centre,
// which is the bake's semantic. Re-derived from the measured bounds (and
// the libraryRotApplied record) on every application, so it is idempotent
// and converges to a no-op once the real bake lands in the .glb.
func shiftGeometryToOverride(node Node3D.Instance, override librarySizeOverride) {
	if override.hasRotation {
		if delta := override.rotation - libraryRotApplied[node.ID()]; Float.Abs(delta) > 0.01 {
			rad := Angle.Radians(delta/180) * Angle.Pi
			for _, child := range node.AsNode().GetChildren() {
				if c3d, ok := Object.As[Node3D.Instance](child); ok {
					c3d.RotateY(rad)
					c3d.SetPosition(orbitAboutPivot(c3d.Position(), Vector3.Zero, rad))
				}
			}
			libraryRotApplied[node.ID()] = override.rotation
		}
	}
	bmin, bmax, found := worldVisualBounds(node.AsNode())
	if !found {
		return
	}
	pos := node.Position()
	a := node.Rotation().Y
	c, s := Angle.Cos(a), Angle.Sin(a)
	var delta Vector3.XYZ // world-space correction for the geometry
	if override.hasOffset {
		delta.Y = pos.Y + override.offset - bmin.Y
	}
	if override.hasXZ {
		// Model-axis offsets rotated into world space (rotation only — the
		// offsets are in in-world metres already).
		wx := override.offsetX*c + override.offsetZ*s
		wz := -override.offsetX*s + override.offsetZ*c
		delta.X = pos.X + wx - (bmin.X+bmax.X)/2
		delta.Z = pos.Z + wz - (bmin.Z+bmax.Z)/2
	}
	scale := node.Scale()
	if Vector3.Length(delta) <= 0.001 || scale.X <= 0.0001 || scale.Y <= 0.0001 || scale.Z <= 0.0001 {
		return
	}
	// The children live in the node's local frame: un-rotate the world
	// delta about Y and divide out the node scale.
	local := Vector3.New(
		(delta.X*c-delta.Z*s)/scale.X,
		delta.Y/scale.Y,
		(delta.X*s+delta.Z*c)/scale.Z,
	)
	for _, child := range node.AsNode().GetChildren() {
		if c3d, ok := Object.As[Node3D.Instance](child); ok {
			c3d.SetPosition(Vector3.Add(c3d.Position(), local))
		}
	}
}

// applySizeOverride is the PreviewRenderer side of the sizes.txt preview:
// without it the ghost shows the design at its stale .glb size while the
// placed entity is snapped by applyLibrarySizeOverride, so the model would
// visibly jump on placement. Scales the freshly attached INSTANCE (not the
// preview node — Change.Bounds records the preview node's scale on commit,
// and the override factor must stay out of the musical log or the entity
// would come out double-scaled once the .glb is actually baked) so the
// ghost's visual height matches the override, and lifts it so its base
// previews the baked grounding (preview origin + offset). The placed entity
// itself spawns at the un-overridden Bounds and is corrected by the creation
// hook within the same mutation, before the frame renders. Only called for
// fresh library picks (!hasExplicitScale) — a duplicate copies its source
// entity's scale, which already includes the override.
func (preview *PreviewRenderer) applySizeOverride(instance Node3D.Instance, overrides map[string]librarySizeOverride) {
	if !strings.HasPrefix(preview.design, "res://library/") {
		return
	}
	model := strings.TrimSuffix(strings.TrimPrefix(preview.design, "res://library/"), path.Ext(preview.design))
	override, listed := overrides[model]
	if !listed {
		return
	}
	lo, hi, found := worldVisualYRange(instance.AsNode())
	if !found || hi-lo <= 0.001 {
		return
	}
	factor := override.height / (hi - lo)
	if Float.Abs(factor-1) > 0.001 {
		instance.SetScale(Vector3.MulX(instance.Scale(), factor))
	}
	if !preview.groundSizeOverride {
		return
	}
	// Turn and shift the instance so the ghost previews the override's
	// placement: facing corrected by the rotation override, base at the
	// preview origin (the contact point) + offset, horizontal geometry
	// centre at the preview origin + offsetX/Z (model axes) — the same
	// rule applyLibrarySizeOverride/shiftGeometryToOverride applies to the
	// placed entity, so the model doesn't jump on drop. The shift happens
	// in preview-local space, so the world-space delta un-rotates by the
	// preview rotation and divides by the preview scale. (Fresh instance
	// per attach, so the rotation needs no applied-state tracking here.)
	if override.hasRotation && Float.Abs(override.rotation) > 0.01 {
		rad := Angle.Radians(override.rotation/180) * Angle.Pi
		instance.RotateY(rad)
		instance.SetPosition(orbitAboutPivot(instance.Position(), Vector3.Zero, rad))
	}
	bmin, bmax, found := worldVisualBounds(instance.AsNode())
	if !found {
		return
	}
	porigin := preview.AsNode3D().GlobalPosition()
	a := preview.AsNode3D().Rotation().Y
	c, s := Angle.Cos(a), Angle.Sin(a)
	var delta Vector3.XYZ
	if override.hasOffset || strings.HasPrefix(model, "everything/") {
		delta.Y = porigin.Y + override.offset - bmin.Y
	}
	if override.hasXZ {
		wx := override.offsetX*c + override.offsetZ*s
		wz := -override.offsetX*s + override.offsetZ*c
		delta.X = porigin.X + wx - (bmin.X+bmax.X)/2
		delta.Z = porigin.Z + wz - (bmin.Z+bmax.Z)/2
	}
	scale := preview.AsNode3D().Scale()
	if Vector3.Length(delta) <= 0.001 || scale.X <= 0.0001 || scale.Y <= 0.0001 || scale.Z <= 0.0001 {
		return
	}
	instance.SetPosition(Vector3.Add(instance.Position(), Vector3.New(
		(delta.X*c-delta.Z*s)/scale.X,
		delta.Y/scale.Y,
		(delta.X*s+delta.Z*c)/scale.Z,
	)))
}

// applyLibrarySizeOverrides sweeps every tracked entity through
// applyLibrarySizeOverride. Run at the end of world load: overrides applied
// during the bulk replay measured against a not-yet-built heightfield
// (HeightAt==0), and re-applying is idempotent, so this single pass settles
// everything against the final terrain.
func (world *Client) applyLibrarySizeOverrides() {
	if len(librarySizeOverrides()) == 0 {
		return
	}
	for design, ids := range world.design_to_entity {
		for _, id := range ids {
			if node, ok := id.Instance(); ok {
				world.applyLibrarySizeOverride(world.object_to_entity[id], design, node, true)
			}
		}
	}
	// Shelter parts live in the shelter editor's own maps (its Change
	// handler short-circuits the generic registration path) and anchor to
	// the level grid rather than the terrain.
	if world.ShelterEditor != nil {
		for design, ids := range world.ShelterEditor.design_to_entity {
			for _, id := range ids {
				if node, ok := id.Instance(); ok {
					world.applyLibrarySizeOverride(world.ShelterEditor.object_to_entity[id], design, node, false)
				}
			}
		}
	}
}

// debugPersistSelectionSize implements the F2 library-sizing helper. It is
// active only when AVIARY_LIBRARY_SIZES points at the library repo's
// sizes.txt: it measures the selected entity's current in-world height and
// the offset of its visual base above the terrain (so size and lift can
// first be dialled in live with the scale and float gizmos) and upserts a
// "<model> <height-in-metres> <base-offset-in-metres>" line into that file.
// Running rescale_glb.py in the library repo then bakes both into the .glb
// geometry, making them the model's defaults after a re-import.
func (world *Client) debugPersistSelectionSize() {
	sizes := librarySizesFile()
	if sizes == "" {
		return
	}
	entity, node, editorID, ok := world.resolveSelection()
	if !ok {
		return
	}
	// Only the scenery (x0.1) and shelter (x0.2) placement chains are known
	// to rescale_glb.py's world_per_authored rule; other editors' parts
	// can't round-trip a measured height yet.
	if editorID != "" && editorID != "shelter" {
		return
	}
	// Resolve the design behind the node: editors with their own entity
	// maps (shelter) answer via DesignForNode, scenery via the global map —
	// the same split DuplicateSelection uses.
	var design musical.Design
	if ed, isClickable := world.ui.Editor.editor.(ClickableEditor); isClickable {
		d, found := ed.DesignForNode(node)
		if !found {
			return
		}
		design = d
	} else {
		d, found := world.findDesignForObject(Node3D.ID(node.ID()))
		if !found {
			return
		}
		design = d
	}
	resource := world.design_to_string[design]
	if !strings.HasPrefix(resource, "res://library/") {
		return
	}
	model := strings.TrimSuffix(strings.TrimPrefix(resource, "res://library/"), path.Ext(resource))
	bmin, bmax, found := worldVisualBounds(node.AsNode())
	if !found || bmax.Y <= bmin.Y {
		return
	}
	lo, hi := bmin.Y, bmax.Y
	// The measurement reference depends on how the editor seats things.
	// Scenery objects sit on terrain, so measure against the terrain under
	// the entity's origin — a GizmoFloat lift moves the whole node (origin
	// included), so origin-relative measurement would always come out 0,
	// while terrain-relative captures the dialled lift.
	ground := node.Position().Y
	var offsetX, offsetZ, rotation Float.X
	if editorID == "" && world.TerrainEditor != nil {
		ground = world.TerrainEditor.HeightAt(Vector3.New(node.Position().X, 0, node.Position().Z))
	} else if editorID == "shelter" {
		// Shelter parts measure against the pose they were FIRST placed
		// with (entity_placement, recorded by the shelter Change handler),
		// so the displacement dialled WITHIN that cell — a wall panel
		// floated up and shifted to the cell edge — is captured even at
		// exactly half a cell, where rounding the current position would
		// anchor it to the neighbouring cell instead. The fallback only
		// triggers without a placement record (replay re-fills the records
		// every load, so in practice it never does).
		pos := node.Position()
		anchor := shelterAnchor{
			offset: Vector3.New(Float.Round(pos.X), Float.Round(pos.Y), Float.Round(pos.Z)),
			angleY: node.Rotation().Y,
		}
		if world.ShelterEditor != nil {
			if first, has := world.ShelterEditor.entity_placement[entity]; has {
				anchor = first
			}
		}
		ground = anchor.offset.Y
		// The in-cell displacement is what you see: the geometry's
		// horizontal CENTRE relative to the anchor (the bounds centre is
		// exact under rotation), un-rotated by the ANCHOR facing — the
		// rotation a future placement of the baked model will use, which
		// is what makes a dialled twist (below) not disturb the measured
		// position.
		dx := (bmin.X+bmax.X)/2 - anchor.offset.X
		dz := (bmin.Z+bmax.Z)/2 - anchor.offset.Z
		c, s := Angle.Cos(anchor.angleY), Angle.Sin(anchor.angleY)
		offsetX = dx*c - dz*s
		offsetZ = dx*s + dz*c
		if Float.Abs(offsetX) < 0.005 {
			offsetX = 0
		}
		if Float.Abs(offsetZ) < 0.005 {
			offsetZ = 0
		}
		// The facing correction: the twist dialled relative to the
		// placement facing, plus whatever rotation override is already
		// applied to the geometry children (so re-pressing F2 after a
		// reset keeps the recorded value instead of zeroing it).
		rotation = Float.X((node.Rotation().Y-anchor.angleY)*180/Angle.Pi) + libraryRotApplied[Node3D.ID(node.ID())]
		for rotation > 180 {
			rotation -= 360
		}
		for rotation <= -180 {
			rotation += 360
		}
		if Float.Abs(rotation) < 0.05 {
			rotation = 0
		}
	}
	offset := lo - ground
	if Float.Abs(offset) < 0.005 {
		offset = 0 // avoid "-0.00" noise from float measurement jitter
	}
	if err := upsertSizeLine(sizes, model, hi-lo, offset, offsetX, offsetZ, rotation, editorID); err != nil {
		Engine.Raise(fmt.Errorf("library sizes: %w", err))
		return
	}
	fmt.Printf("library sizes: %s %.2f %.2f %.2f %.2f %.1f %s\n", model, hi-lo, offset, offsetX, offsetZ, rotation, editorID)
	// Update the in-session override so the new numbers preview immediately
	// (fresh drops, and the post-reset re-dress below) instead of waiting
	// for a restart. hasRotation stays set even at 0 so a prior in-session
	// rotation override un-applies cleanly.
	if overrides := librarySizeOverrides(); overrides != nil {
		entry := librarySizeOverride{height: hi - lo, offset: offset, hasOffset: true}
		if editorID == "shelter" {
			entry.offsetX, entry.offsetZ, entry.hasXZ = offsetX, offsetZ, true
			entry.rotation, entry.hasRotation = rotation, true
		}
		overrides[model] = entry
	}
	// Shelter: transfer the dial into the model. Snap the node back to its
	// placement pose (a real, observable Change — post-bake that IS the
	// entity's correct record) and let the Change handler's override hook
	// re-dress the geometry to the just-measured numbers. Without this the
	// node displacement AND the geometry override would both show — the
	// "double shift".
	if editorID == "shelter" && world.ShelterEditor != nil {
		if anchor, has := world.ShelterEditor.entity_placement[entity]; has {
			prePos, preRot := node.Position(), node.Rotation()
			reset := musical.Change{
				Author: world.id,
				Entity: entity,
				Editor: editorID,
				Offset: anchor.offset,
				Angles: Euler.Radians{Y: anchor.angleY},
				Commit: true,
			}
			if err := world.space.Change(reset); err != nil {
				Engine.Raise(err)
			} else {
				undo := reset
				undo.Offset, undo.Angles = prePos, preRot
				world.RecordChange(reset, undo)
			}
		}
	}
}

// worldVisualBounds reports the world-space axis-aligned bounds of every
// VisualInstance3D bounding box under node (inclusive). The bounds centre is
// exact under rotation (an affine transform maps a box's centre to the
// centre of the transformed corners) even though the extents are
// conservative.
func worldVisualBounds(node Node.Instance) (bmin, bmax Vector3.XYZ, found bool) {
	if vi, isVisual := Object.As[VisualInstance3D.Instance](node); isVisual {
		aabb := vi.GetAabb()
		global := vi.AsNode3D().GlobalTransform()
		for i := range 8 {
			corner := Vector3.New(
				aabb.Position.X+aabb.Size.X*Float.X(i&1),
				aabb.Position.Y+aabb.Size.Y*Float.X(i>>1&1),
				aabb.Position.Z+aabb.Size.Z*Float.X(i>>2&1),
			)
			p := Transform3D.Transform(corner, global)
			if !found {
				bmin, bmax, found = p, p, true
			} else {
				bmin, bmax = Vector3.Min(bmin, p), Vector3.Max(bmax, p)
			}
		}
	}
	for _, child := range node.GetChildren() {
		cmin, cmax, childFound := worldVisualBounds(child)
		if childFound {
			if !found {
				bmin, bmax, found = cmin, cmax, true
			} else {
				bmin, bmax = Vector3.Min(bmin, cmin), Vector3.Max(bmax, cmax)
			}
		}
	}
	return bmin, bmax, found
}

// worldVisualYRange is the vertical extent of worldVisualBounds.
func worldVisualYRange(node Node.Instance) (lo, hi Float.X, found bool) {
	bmin, bmax, found := worldVisualBounds(node)
	return bmin.Y, bmax.Y, found
}

// upsertSizeLine rewrites the sizes file with the given model's height and
// base offset — plus, for shelter, the in-cell X/Z placement and the
// placing editor's id, so rescale_glb.py knows which placement chain the
// height was measured through — replacing the model's existing line
// (comments and other lines are kept as-is) or appending a new one.
// Shelter lines always carry X/Z: the pair is an ABSOLUTE horizontal
// geometry-centre placement, so `0.00 0.00` (centre on the origin) means
// something different from omitting it (keep the authored placement). A
// non-zero facing correction follows as a fifth number (degrees about Y;
// zero is omitted — no rotation and a 0° rotation are the same thing).
// Scenery lines never carry X/Z, keeping the established
// `<model> <height> <y>` form (and full backwards compatibility).
func upsertSizeLine(file, model string, height, offset, offsetX, offsetZ, rotation Float.X, editor string) error {
	data, err := os.ReadFile(file)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	entry := fmt.Sprintf("%s %.2f %.2f", model, height, offset)
	if editor == "shelter" {
		entry += fmt.Sprintf(" %.2f %.2f", offsetX, offsetZ)
		if rotation != 0 {
			entry += fmt.Sprintf(" %.1f", rotation)
		}
	}
	if editor != "" {
		entry += " " + editor
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	replaced := false
	for i, line := range lines {
		fields := strings.Fields(strings.SplitN(line, "#", 2)[0])
		if len(fields) > 0 && fields[0] == model {
			lines[i] = entry
			replaced = true
		}
	}
	if !replaced {
		lines = append(lines, entry)
	}
	return os.WriteFile(file, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}
