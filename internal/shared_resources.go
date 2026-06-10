package internal

import (
	"path"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"graphics.gd/classdb/Engine"
	"graphics.gd/classdb/Input"
	"graphics.gd/classdb/InputEventKey"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/PackedScene"
	"graphics.gd/classdb/Texture2D"
	"graphics.gd/variant/Float"
	"the.quetzal.community/aviary/internal/musical"
)

// isDeletePress is true for a non-echo, pressed-state key event whose
// keycode is Delete or Backspace. Shared by every editor's
// remove-selected-entity handler.
func isDeletePress(event InputEventKey.Instance) bool {
	if !event.AsInputEvent().IsPressed() || event.AsInputEvent().IsEcho() {
		return false
	}
	code := event.Keycode()
	return code == Input.KeyDelete || code == Input.KeyBackspace
}

// SharedResources is a singleton responsible for coordinating resource caching and entities for
// a [musical.UsersScene3D] instance.
type SharedResources struct {
	entity_ids map[musical.Author]uint16
	design_ids map[musical.Author]uint16

	design_to_entity map[musical.Design][]Node3D.ID
	entity_to_object map[musical.Entity]Node3D.ID
	object_to_entity map[Node3D.ID]musical.Entity

	// pending_actions holds move Actions replayed before the target entity's
	// placement Change registered it in entity_to_object. This happens because a
	// save is stitched from multiple parts (the local device file then each other
	// device's cloud part — see OpenCloud's io.MultiReader): a critter placed on
	// one device and moved on another has its placement and its move in different
	// parts, and the local part (which may hold only the move) replays first. The
	// Action handler parks such actions here and flushPendingActions replays them
	// once registerEntity creates the entity, so the move isn't lost on reload.
	pending_actions map[musical.Entity][]musical.Action

	// entity_move_timing is the per-entity high-water timestamp of the latest
	// positional mutation applied (placement/gizmo Change.Timing or walk
	// Action.Timing). Position only ever moves FORWARD in time: a Change or Action
	// older than this is ignored. This is what makes reload order positional edits
	// by time rather than by save-part order — without it an older gizmo move
	// replayed from a later part clobbers a newer walk (see [[project_multipart_action_ordering]]).
	// Committed Changes are stamped with the wall clock at record time (stampedSpace);
	// legacy unstamped records (Timing 0) sort oldest, so any timestamped mutation wins.
	entity_move_timing map[musical.Entity]musical.Timing

	// entity_float_delta holds the terrain-relative lift (Change.Offset.Y for an
	// Editor="float" Change) of every entity whose latest positional mutation was a
	// float, so it can be re-seated against the FINAL terrain. During the bulk
	// replay the heightfield isn't built yet, so a float Change reconstructs its Y
	// against HeightAt==0 (placing it at ~delta, not terrain+delta); reseatFloats
	// (run after flushBulkReloads) fixes every such object once the terrain exists.
	// An entity is removed from the map when a non-float positional Change supersedes
	// its float, or when the entity is removed.
	entity_float_delta map[musical.Entity]Float.X

	packed_scenes    map[musical.Design]PackedScene.ID
	textures         map[musical.Design]Texture2D.ID
	design_to_string map[musical.Design]string
	loaded           map[string]musical.Design

	// missing_scenes records designs whose .glb/.scn could not be loaded even
	// though their import URI is known (file absent from the library.pck, or the
	// resource is the wrong Godot type). sceneFor consults it to attempt such a
	// load exactly ONCE rather than re-issuing it every frame: dressing strokes,
	// critters and the placement editors all park-and-retry through sceneFor, so
	// without this a single dangling design re-triggers Godot's "Resource file
	// not found" error on every Process tick for the whole session.
	missing_scenes map[musical.Design]bool
}

// The entity↔object↔design bookkeeping below is what every placement
// editor (Scenery via SharedResources, plus Shelter, Vehicle and
// Coaster, and the world-level path in client.go) keeps to map a
// musical.Entity to its scene node, recover the owning entity from a
// node, and list every node sharing a Design. registerEntity and
// removeEntity centralise the once-copy-pasted bookkeeping so the three
// maps can't drift out of sync.

// registerEntity records a freshly-instantiated node under its entity
// and design across all three maps. Callers still own node creation,
// scene parenting and any per-editor side maps (mirror/chain) — this
// only touches the shared triad.
func registerEntity(designToEntity map[musical.Design][]Node3D.ID, entityToObject map[musical.Entity]Node3D.ID, objectToEntity map[Node3D.ID]musical.Entity, design musical.Design, entity musical.Entity, node Node3D.Instance) {
	entityToObject[entity] = node.ID()
	objectToEntity[node.ID()] = entity
	designToEntity[design] = append(designToEntity[design], node.ID())
	if loadProfileOn {
		debugEverCreated[design] = true
	}
}

// debugEverCreated records every Design that was ever instantiated (created or
// re-designed), so debugResourceUsage can distinguish dead designs that were
// never placed (lazy-load would skip them) from placed-then-removed ones.
var debugEverCreated = map[musical.Design]bool{}

// removeEntity prunes a node from all three maps and frees it. It fixes
// two bugs the inline copies shared: the design_to_entity prune used
// slices.Delete(s, idx, idx) (a zero-width range that deleted nothing,
// leaking freed IDs), and most copies forgot to delete the reverse-map
// entries entirely, leaking entity_to_object / object_to_entity rows.
func removeEntity(designToEntity map[musical.Design][]Node3D.ID, entityToObject map[musical.Entity]Node3D.ID, objectToEntity map[Node3D.ID]musical.Entity, design musical.Design, entity musical.Entity, node Node3D.Instance) {
	if idx := slices.Index(designToEntity[design], node.ID()); idx >= 0 {
		designToEntity[design] = slices.Delete(designToEntity[design], idx, idx+1)
	}
	delete(entityToObject, entity)
	delete(objectToEntity, node.ID())
	node.AsNode().QueueFree()
}

// designCategory returns the library category a resource URI sits in — the name
// of its parent folder (library/<author>/<category>/<file>). Editors switch on
// this to decide how a picked or placed design behaves (e.g. "spinner",
// "fencing", "leaflet", "hanging"/"mounted"). (editor_terrain and the fence tool
// still inline path.Base(path.Dir(...)) for now — adoption is incremental.)
func designCategory(uri string) string {
	return path.Base(path.Dir(uri))
}

// designURI maps a design reference back to its library resource URI.
// Empty when the design's Import hasn't been observed yet.
func (client *Client) designURI(design musical.Design) string {
	return client.design_to_string[design]
}

func (client *Client) MusicalDesign(resource string) musical.Design {
	design, ok := client.loaded[resource]
	if !ok {
		client.design_ids[client.id]++
		design = musical.Design{
			Author: client.id,
			Number: client.design_ids[client.id],
		}
		client.space.Import(musical.Import{
			Design: design,
			Import: resource,
		})
	}
	return design
}

// newEntityMaps allocates the entity↔object↔design tracking triad every
// placement editor that keeps its OWN maps (coaster/shelter/vehicle) initialises
// in Ready, ready for registerEntity/removeEntity. Returned as a triple so each
// editor assigns into its own fields. (Scenery uses the client-global maps and so
// doesn't call this.)
func newEntityMaps() (map[musical.Design][]Node3D.ID, map[musical.Entity]Node3D.ID, map[Node3D.ID]musical.Entity) {
	return map[musical.Design][]Node3D.ID{},
		map[musical.Entity]Node3D.ID{},
		map[Node3D.ID]musical.Entity{}
}

// NextEntity reserves the next Entity id authored by this client and
// returns the full musical.Entity. Replaces the
// `client.entity_ids[client.id]++ ; Entity{Author, Number}` pattern
// every placement editor repeated inline.
func (client *Client) NextEntity() musical.Entity {
	client.entity_ids[client.id]++
	return musical.Entity{
		Author: client.id,
		Number: client.entity_ids[client.id],
	}
}

// applyReflectSlider finds the gd-tagged field `prop` on container
// (a *T pointer) and stores value into it, then calls regenerate.
// Returns true if a matching field was found. Shared by the
// procedural editors (foliage/boulder) whose Sculpt handlers all
// reach into a struct via reflection.
func applyReflectSlider(container any, rtype reflect.Type, prop string, value float64, regenerate func()) bool {
	for i := 0; i < rtype.NumField(); i++ {
		field := rtype.Field(i)
		if field.Tag.Get("gd") != prop {
			continue
		}
		v := reflect.ValueOf(container).Elem().Field(i)
		switch field.Type.Kind() {
		case reflect.Int:
			v.SetInt(int64(value))
		case reflect.Float32, reflect.Float64:
			v.SetFloat(value)
		default:
			return false
		}
		regenerate()
		return true
	}
	return false
}

// reflectSliderConfig reads the gd-tagged field `prop` on rtype and
// returns the slider bounds derived from its `default` and `range`
// struct tags. ok=false when no matching field exists; caller fills
// in its own defaults.
func reflectSliderConfig(rtype reflect.Type, prop string) (init, from, upto, step float64, ok bool) {
	for i := 0; i < rtype.NumField(); i++ {
		field := rtype.Field(i)
		if field.Tag.Get("gd") != prop {
			continue
		}
		init, _ = strconv.ParseFloat(field.Tag.Get("default"), 64)
		ranges := strings.Split(field.Tag.Get("range"), ",")
		if len(ranges) >= 2 {
			from, _ = strconv.ParseFloat(ranges[0], 64)
			upto, _ = strconv.ParseFloat(ranges[1], 64)
		}
		step = 0.001
		if field.Type.Kind() == reflect.Int {
			step = 1
		}
		return init, from, upto, step, true
	}
	return 0, 0, 0, 0, false
}

// reflectSliderConfigOr returns reflectSliderConfig for the "<group>/<prop>"
// editing key, falling back to the supplied defaults when the resource struct
// carries no matching gd-tagged field. Replaces the
// Cut("/") + reflectSliderConfig + hardcoded-default tail every procedural
// editor's SliderConfig repeated.
func reflectSliderConfigOr(rtype reflect.Type, editing string, init, from, upto, step float64) (float64, float64, float64, float64) {
	_, prop, _ := strings.Cut(editing, "/")
	if i, f, u, s, ok := reflectSliderConfig(rtype, prop); ok {
		return i, f, u, s
	}
	return init, from, upto, step
}

// isEnvironmentSculpt reports whether brush targets world lighting
// (environment/* sliders). Those are single-owned by the terrain editor
// (stamped Editor "terrain"), so every other editor drops them on arrival —
// otherwise a non-owner's per-editor lighting cache diverges and clobbers the
// look. Each placement/procedural editor's Sculpt guards on this first.
func isEnvironmentSculpt(brush musical.Sculpt) bool {
	return strings.HasPrefix(brush.Slider, "environment/")
}

// publishSculpt stamps the local author onto brush and records it in the
// shared space. Unlike commitSculpt it stamps no Timing and records no undo
// entry — for editors (citizen, critter, …) whose sculpts don't participate
// in terrain-style undo.
func (client *Client) publishSculpt(brush musical.Sculpt) error {
	if client.space == nil {
		return nil
	}
	brush.Author = client.id
	return client.space.Sculpt(brush)
}

// emitSliderSculpt records a slider-amount Sculpt under editor/slider, raising
// any storage error. Centralises the musical.Sculpt{Author, Editor, Slider,
// Amount, Commit} block every procedural editor's SliderHandle repeated verbatim
// (the per-editor throttle stays at the call site — its state differs per editor).
func (client *Client) emitSliderSculpt(editor, slider string, value float64, commit bool) {
	if err := client.space.Sculpt(musical.Sculpt{
		Author: client.id,
		Editor: editor,
		Slider: slider,
		Amount: Float.X(value),
		Commit: commit,
	}); err != nil {
		Engine.Raise(err)
	}
}

// emitDesignSculpt records a committed material-selection Sculpt: it maps the
// library resource to a musical.Design (registering an Import if new) and emits
// it under editor/slider. Shared by the material-tab editors' SelectDesign.
func (client *Client) emitDesignSculpt(editor, slider, resource string) {
	if err := client.space.Sculpt(musical.Sculpt{
		Author: client.id,
		Editor: editor,
		Slider: slider,
		Design: client.MusicalDesign(resource),
		Commit: true,
	}); err != nil {
		Engine.Raise(err)
	}
}
