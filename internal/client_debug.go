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
// in-world height, and optionally where its visual base should sit relative
// to the ground (see rescale_glb.py in the library repo for the format).
type librarySizeOverride struct {
	height    Float.X
	offset    Float.X
	hasOffset bool
}

// librarySizeOverrides lazily parses the sizes.txt the library-sizing debug
// mode points at, keyed by model ("everything/housing/1"). Nil outside debug
// mode, so the per-entity/per-preview hooks cost one map-nil check. Package
// global (not Client state) because the PreviewRenderer needs it too and has
// no client back-reference.
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
		overrides[fields[0]] = override
	}
	return overrides
}

// applyLibrarySizeOverride previews a sizes.txt entry on a placed entity:
// the node is scaled so its visual height matches the listed height, and —
// mirroring rescale_glb.py's grounding rule — lifted so its base sits at
// the seat reference + offset (explicit offset wins; everything/* defaults
// to 0; other packs keep their authored origin). terrainSeated picks the
// reference: terrain under the origin for scenery-style placement, the
// node's own origin for grid-anchored editors (shelter). The node is
// changed locally only, no musical Change is emitted: this is a debug view
// of what the .glb bake will produce, not a world mutation, so other
// clients are unaffected. Idempotent — once the bake matches, the measured
// factor converges to 1.
func (world *Client) applyLibrarySizeOverride(entity musical.Entity, design musical.Design, node Node3D.Instance, terrainSeated bool) {
	overrides := librarySizeOverrides()
	if len(overrides) == 0 {
		return
	}
	resource := world.design_to_string[design]
	if !strings.HasPrefix(resource, "res://library/") {
		return
	}
	model := strings.TrimSuffix(strings.TrimPrefix(resource, "res://library/"), path.Ext(resource))
	override, listed := overrides[model]
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
	ground := override.hasOffset || (terrainSeated && strings.HasPrefix(model, "everything/"))
	if _, floated := world.entity_float_delta[entity]; floated {
		ground = false // respect a user-dialled float lift
	}
	if ground {
		pos := node.Position()
		seat := pos.Y
		if terrainSeated {
			if world.TerrainEditor == nil {
				return
			}
			seat = world.TerrainEditor.HeightAt(Vector3.New(pos.X, 0, pos.Z))
		}
		// The geometry scales about the node origin, so re-derive where the
		// base ended up after the SetScale above.
		base := pos.Y + (lo-pos.Y)*factor
		if delta := seat + override.offset - base; Float.Abs(delta) > 0.001 {
			pos.Y += delta
			node.SetPosition(pos)
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
	instanceOrigin := instance.GlobalPosition().Y
	if Float.Abs(factor-1) > 0.001 {
		instance.SetScale(Vector3.MulX(instance.Scale(), factor))
	}
	if override.hasOffset || strings.HasPrefix(model, "everything/") {
		// Re-derive where the base ended up after the SetScale above (the
		// geometry scales about the instance origin), then shift the
		// instance so the base sits at the preview origin (the terrain
		// contact point) + offset. The shift happens in preview-local
		// space, so the world-space delta divides by the preview scale.
		base := instanceOrigin + (lo-instanceOrigin)*factor
		scale := preview.AsNode3D().Scale().Y
		target := preview.AsNode3D().GlobalPosition().Y + override.offset
		if delta := target - base; Float.Abs(delta) > 0.001 && scale > 0.0001 {
			pos := instance.Position()
			pos.Y += delta / scale
			instance.SetPosition(pos)
		}
	}
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
	_, node, editorID, ok := world.resolveSelection()
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
	lo, hi, found := worldVisualYRange(node.AsNode())
	if !found || hi <= lo {
		return
	}
	// The base offset reference depends on how the editor seats things.
	// Scenery objects sit on terrain, so measure against the terrain under
	// the entity's origin — a GizmoFloat lift moves the whole node (origin
	// included), so origin-relative measurement would always come out 0,
	// while terrain-relative captures the dialled lift. Shelter parts
	// anchor to the level grid instead, so the part's own origin is the
	// reference (terrain-relative would record the floor height).
	ground := node.Position().Y
	if editorID == "" && world.TerrainEditor != nil {
		ground = world.TerrainEditor.HeightAt(Vector3.New(node.Position().X, 0, node.Position().Z))
	}
	offset := lo - ground
	if Float.Abs(offset) < 0.005 {
		offset = 0 // avoid "-0.00" noise from float measurement jitter
	}
	if err := upsertSizeLine(sizes, model, hi-lo, offset, editorID); err != nil {
		Engine.Raise(fmt.Errorf("library sizes: %w", err))
		return
	}
	fmt.Printf("library sizes: %s %.2f %.2f %s\n", model, hi-lo, offset, editorID)
}

// worldVisualYRange reports the world-space vertical extent of every
// VisualInstance3D bounding box under node (inclusive).
func worldVisualYRange(node Node.Instance) (lo, hi Float.X, found bool) {
	if vi, isVisual := Object.As[VisualInstance3D.Instance](node); isVisual {
		aabb := vi.GetAabb()
		global := vi.AsNode3D().GlobalTransform()
		for i := range 8 {
			corner := Vector3.New(
				aabb.Position.X+aabb.Size.X*Float.X(i&1),
				aabb.Position.Y+aabb.Size.Y*Float.X(i>>1&1),
				aabb.Position.Z+aabb.Size.Z*Float.X(i>>2&1),
			)
			y := Transform3D.Transform(corner, global).Y
			if !found || y < lo {
				lo = y
			}
			if !found || y > hi {
				hi = y
			}
			found = true
		}
	}
	for _, child := range node.GetChildren() {
		clo, chi, childFound := worldVisualYRange(child)
		if childFound {
			if !found || clo < lo {
				lo = clo
			}
			if !found || chi > hi {
				hi = chi
			}
			found = true
		}
	}
	return lo, hi, found
}

// upsertSizeLine rewrites the sizes file with the given model's height and
// base offset (plus the placing editor's id, when not scenery, so
// rescale_glb.py knows which placement chain the height was measured
// through), replacing the model's existing line (comments and other lines
// are kept as-is) or appending a new one.
func upsertSizeLine(file, model string, height, offset Float.X, editor string) error {
	data, err := os.ReadFile(file)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	entry := fmt.Sprintf("%s %.2f %.2f", model, height, offset)
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
