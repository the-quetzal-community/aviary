package internal

import (
	"strings"
	"sync"
	"time"

	"graphics.gd/classdb/Engine"
	"graphics.gd/classdb/MeshInstance3D"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/variant/Float"

	"the.quetzal.community/aviary/internal/citizen"
	"the.quetzal.community/aviary/internal/musical"
)

type CitizenEditor struct {
	Node3D.Extension[CitizenEditor]
	musical.Stubbed

	client *Client

	loadOnce sync.Once
	body     CitizenBody

	last_slider_sculpt time.Time

	// pendingSculpts queues Sculpts that arrived before the user
	// entered the citizen editor — typically history replay on
	// session load, occasionally network catch-up. Drained in
	// EnableEditor once the body is built, so loading the world
	// doesn't pay the O(N targets × rebuilds) cost up-front when
	// the user may never open the citizen editor at all.
	pendingSculpts []musical.Sculpt

	lighting // private lighting for this editor
}

func (*CitizenEditor) Name() string    { return "citizen" }
func (*CitizenEditor) Views() []string { return nil }

// EnableEditor fires when the user switches into the citizen
// editor. This is where we pay the deferred load cost — building
// the base mesh + applying any queued Sculpts that piled up
// during world load (or while the user was in another editor) —
// so a session that never opens the citizen editor never builds
// the citizen body at all.
func (ce *CitizenEditor) EnableEditor() {
	ce.client.SetGizmos(nil)
	ce.ensureLoaded()
	ce.lighting.resync(ce.client)
	if len(ce.pendingSculpts) == 0 {
		return
	}
	pending := ce.pendingSculpts
	ce.pendingSculpts = nil
	for _, brush := range pending {
		ce.applySculpt(brush)
	}
}

func (*CitizenEditor) ChangeEditor() {}

// Process runs once per frame; we use it to flush any pending body
// visibility recompute that AttachDressing deferred. During replay of
// scene history a burst of dressing Sculpts queue up and all fire
// inside a single Client.Process drain — without the dirty-flag
// coalescing each one would trigger its own O(body × clothing)
// sweep, lagging startup.
func (ce *CitizenEditor) Process(dt Float.X) {
	ce.body.CommitVisibility()
}

// SwitchToView lazy-loads the citizen base mesh and target deltas the
// first time the editor is entered. Subsequent calls are no-ops.
func (ce *CitizenEditor) SwitchToView(view string) {
	ce.ensureLoaded()
}

func (ce *CitizenEditor) ensureLoaded() {
	ce.loadOnce.Do(func() {
		base, targets, err := LoadCitizenAssets()
		if err != nil {
			Engine.Raise(err)
			return
		}
		mi := MeshInstance3D.New()
		ce.AsNode3D().AsNode().AddChild(mi.AsNode())
		body, err := AttachCitizenBody(mi, base, targets)
		if err != nil {
			Engine.Raise(err)
			return
		}
		ce.body = body
	})
}

func (*CitizenEditor) Tabs(mode Mode) []string {
	switch mode {
	case ModeGeometry:
		return citizenGeometryTabs
	case ModeDressing:
		return []string{
			"helmets",
			"sunnies",
			"pendant",
			"utensil",
			"mittens",
			"daypack",
			"hipwear",
			"jackets",
			"legwear",
			"sandals",
		}
	case ModeMaterial:
		return []string{
			"hairdye",
			"eyetint",
			"pigment",
			"tattoos",
			"posture",
		}
	default:
		return nil
	}
}

// SelectDesign handles the user picking an item from the design
// explorer. For ModeDressing the chosen design is the path to a .obj
// clothing mesh; we route the change through the musical interface
// (via Sculpt + Import) so it replicates to other clients. Local
// application happens when the resulting Sculpt comes back through
// musicalImpl.Sculpt and is dispatched to this editor's Sculpt method.
//
// The design path follows the library convention
// `res://library/<author>/<slot>/<file>.obj`; we extract the slot from
// the second path segment and encode it in the Slider field as
// "dressing/<slot>".
func (ce *CitizenEditor) SelectDesign(mode Mode, design string) {
	// ModeDressing accepts any slot under res://library/<author>/<slot>/.
	// ModeGeometry only accepts the proxy-mesh slots that come from
	// proxy assets — haircut and eyebrows — which look like dressings
	// at runtime but live under the geometry tab because they shape
	// the character's appearance rather than dress it.
	switch mode {
	case ModeDressing:
	case ModeGeometry:
		slot := citizenDressingSlot(design)
		if slot != "haircut" && slot != "eyebrow" {
			return
		}
	default:
		return
	}
	ce.ensureLoaded()
	if ce.body.citizen == nil {
		return
	}
	slot := citizenDressingSlot(design)
	if slot == "" {
		return
	}
	// Sentinel design path: clicking the "_empty.obj" tile in the
	// design grid clears the slot. The .png lives in each preview
	// dir; the .obj is virtual — we never load it, just detect the
	// suffix and route to the clear path.
	clearSlot := strings.HasSuffix(design, "/"+clearDesignName)
	if ce.client == nil {
		// Editor not yet wired into Client; apply locally so single-user
		// development still works while the multiplayer plumbing comes
		// online.
		if clearSlot {
			ce.body.AttachDressing(slot, "")
		} else {
			ce.body.AttachDressing(slot, design)
		}
		return
	}
	var musicalDesign musical.Design
	if !clearSlot {
		musicalDesign = ce.client.MusicalDesign(design)
	}
	if err := ce.client.space.Sculpt(musical.Sculpt{
		Author: ce.client.id,
		Editor: "citizen",
		Slider: dressingSliderPrefix + slot,
		Design: musicalDesign,
		Commit: true,
	}); err != nil {
		Engine.Raise(err)
	}
}

// clearDesignName is the basename (without preview suffix) of the
// sentinel tile rendered in every dressing/proxy slot's design
// grid. Clicking it sends a Sculpt with an empty Design ref, which
// applyDressing interprets as "unequip this slot".
const clearDesignName = "_empty.obj"

// dressingSliderPrefix marks a Sculpt as a dressing-slot change rather
// than a numeric slider adjustment. The remainder of the Slider field
// (after this prefix) is the slot name. Empty Design clears the slot.
const dressingSliderPrefix = "dressing/"

// citizenDressingSlot extracts the slot name from a design path of the
// form `res://library/<author>/<slot>/<file>.obj`. Returns "" if the
// path doesn't match the expected layout.
func citizenDressingSlot(design string) string {
	rest := strings.TrimPrefix(design, "res://library/")
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) < 2 {
		return ""
	}
	return parts[1]
}

// citizenGeometryTabs is the curated, ordered list of tabs shown under
// ModeGeometry. The full catalog in internal/citizen/sliders.go has ~75
// sliders, which is too dense for a UI. This list picks the highest-
// impact ones for video-game-style character creation. To expose more,
// add entries here — citizenEditorSliders already accepts any tab name
// produced by citizen.EditorSpecs.
var citizenGeometryTabs = []string{
	// composite + non-shape placeholders
	"editing/head_size",
	"haircut",
	"eyebrow",
	"stubble",
	// face proportions
	"editing/head_age",
	"editing/head_fat",
	"editing/eyebrows_angle",
	"editing/eyes_size",
	"editing/eyes_horizontal_position",
	"editing/nose_scale_vertical",
	"editing/nose_scale_horizontal",
	"editing/nose_bridge_curve",
	"editing/mouth_scale_horizontal",
	"editing/mouth_upper_lip_volume",
	"editing/mouth_lower_lip_volume",
	"editing/chin_height",
	"editing/cheek_bones",
	// body
	"editing/torso_pectoral",
	"editing/body_hip_scale_horizontal",
	"editing/body_stomach_tone",
}

// citizenEditorSliders maps each shape-driving editor tab to the
// MakeHuman target shape-key names it controls. Decr keys are applied
// when the slider value is negative; Incr when positive. The map is
// built once at init from citizen.EditorSpecs (the catalog in
// internal/citizen/sliders.go) plus a hand-tuned `editing/head_size`
// composite that scales the head uniformly across all three axes.
// Entries for tabs not in citizenGeometryTabs are harmless — they just
// don't show up in the UI.
var citizenEditorSliders = func() map[string]struct {
	Decr, Incr []string
} {
	m := map[string]struct {
		Decr, Incr []string
	}{
		"editing/head_size": {
			Decr: []string{
				"head/head-scale-depth-decr",
				"head/head-scale-horiz-decr",
				"head/head-scale-vert-decr",
			},
			Incr: []string{
				"head/head-scale-depth-incr",
				"head/head-scale-horiz-incr",
				"head/head-scale-vert-incr",
			},
		},
	}
	for _, spec := range citizen.EditorSpecs() {
		m[spec.Tab] = struct {
			Decr, Incr []string
		}{Decr: spec.Decr, Incr: spec.Incr}
	}
	return m
}()

func (*CitizenEditor) SliderConfig(mode Mode, editing string) (init, min, max, step float64) {
	if init, ok := citizenMaterialSliders[editing]; ok {
		return init, 0, 1, 0.01
	}
	if _, ok := citizenEditorSliders[editing]; ok {
		return 0, -1, 1, 0.01
	}
	return 0, 0, 1, 0.01
}

// citizenMaterialSliders are the single-slider material tabs under
// ModeMaterial: one slider per tab, values 0..1, mapped through a
// palette inside CitizenBody.SetPigment / SetEyeTint to actual
// albedo colours. Init values position the slider at the default
// citizen's appearance (fair skin, hazel eyes).
var citizenMaterialSliders = map[string]float64{
	"pigment": defaultPigment,
	"eyetint": defaultEyeTint,
}

func (ce *CitizenEditor) SliderHandle(mode Mode, editing string, value float64, commit bool) {
	if !commit && time.Since(ce.last_slider_sculpt) < time.Second/10 {
		return
	}
	ce.last_slider_sculpt = time.Now()
	if ce.client == nil {
		// Editor not yet wired into Client; apply locally so single-user
		// development still works while the multiplayer plumbing comes
		// online.
		ce.applySlider(editing, Float.X(value))
		return
	}
	ce.client.emitSliderSculpt("citizen", editing, value, commit)
}

// Sculpt overrides musical.Stubbed's no-op so slider changes — local or
// from the network — actually move the displayed mesh. Dressing changes
// piggyback on the same channel: they're encoded with Slider =
// "dressing/<slot>" and Design = the imported library URI's design ref.
//
// If the citizen body hasn't been built yet (the user hasn't entered the
// citizen editor this session), we just queue the sculpt and bail. The
// cost of base-mesh + N-deltas worth of rebuilds is paid in EnableEditor
// once instead of at world load, so opening to any other editor stays
// snappy.
func (ce *CitizenEditor) Sculpt(brush musical.Sculpt) error {
	if isEnvironmentSculpt(brush) {
		return nil
	}
	if ce.body.citizen == nil {
		ce.pendingSculpts = append(ce.pendingSculpts, brush)
		return nil
	}
	ce.applySculpt(brush)
	return nil
}

// applySculpt is the actual handler — called either from Sculpt
// (live) or from EnableEditor (draining the deferred queue).
func (ce *CitizenEditor) applySculpt(brush musical.Sculpt) {
	if strings.HasPrefix(brush.Slider, dressingSliderPrefix) {
		ce.applyDressing(strings.TrimPrefix(brush.Slider, dressingSliderPrefix), brush.Design)
		return
	}
	ce.applySlider(brush.Slider, brush.Amount)
}

// applyDressing resolves the numeric Design back to a library URI and
// hands it to CitizenBody. Empty Design (Author == 0 && Number == 0)
// means "clear this slot".
func (ce *CitizenEditor) applyDressing(slot string, design musical.Design) {
	ce.ensureLoaded()
	if ce.body.citizen == nil {
		return
	}
	uri := ""
	if (design != musical.Design{}) && ce.client != nil {
		uri = ce.client.design_to_string[design]
		if uri == "" {
			// Import hasn't landed yet — the Sculpt arrived before the
			// Design URI mapping. Skip; a retry will follow when the
			// remote replays the scene state.
			return
		}
	}
	ce.body.AttachDressing(slot, uri)
}

func (ce *CitizenEditor) applySlider(editing string, value Float.X) {
	ce.ensureLoaded()
	if ce.body.citizen == nil {
		return
	}
	// Material-tab sliders drive a per-surface material's albedo,
	// not a shape-key, so they short-circuit before the citizen
	// shape-target lookup below.
	switch editing {
	case "pigment":
		ce.body.SetPigment(float32(value))
		return
	case "eyetint":
		ce.body.SetEyeTint(float32(value))
		return
	}
	spec, ok := citizenEditorSliders[editing]
	if !ok {
		return
	}
	v := float32(value)
	for _, k := range spec.Decr {
		ce.body.SetWeight(k, 0)
	}
	for _, k := range spec.Incr {
		ce.body.SetWeight(k, 0)
	}
	switch {
	case v < 0:
		for _, k := range spec.Decr {
			ce.body.SetWeight(k, -v)
		}
	case v > 0:
		for _, k := range spec.Incr {
			ce.body.SetWeight(k, v)
		}
	}
}
