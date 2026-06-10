package internal

import (
	"graphics.gd/classdb/Texture2D"
	"graphics.gd/variant/Float"

	"the.quetzal.community/aviary/internal/musical"
)

// This file defines the capability interfaces ("ports") that editors depend
// on instead of holding a *Client directly. Each interface is a narrow slice
// of what Client offers; an editor declares fields only for the capabilities
// it actually uses, so the compiler documents and enforces each editor's
// coupling surface. Client implements all of them — wiring an editor is just
// assigning the client into each port field (see Client.Ready).
//
// Adoption is incremental, editor by editor: an unmigrated editor keeps its
// `client *Client` field and nothing changes for it.

// Recorder is the capability an editor uses to publish its mutations into the
// shared musical space, stamped as the local author. Mutations published here
// replicate to every client (including this one — local application happens
// when the instruction is dispatched back through musicalImpl).
type Recorder interface {
	// publishSculpt stamps the local author onto brush and records it.
	// It does NOT stamp a Timing or an undo entry — editors whose sculpts
	// participate in undo go through Client.commitSculpt instead.
	publishSculpt(brush musical.Sculpt) error

	// emitSliderSculpt records a slider-amount Sculpt under editor/slider.
	emitSliderSculpt(editor, slider string, value float64, commit bool)

	// emitDesignSculpt records a committed design-selection Sculpt under
	// editor/slider, registering an Import for the resource if it's new.
	emitDesignSculpt(editor, slider, resource string)
}

// Library resolves between library resource URIs and the numeric
// musical.Design references that appear on the wire and in saves.
type Library interface {
	// MusicalDesign returns the design reference for a resource URI,
	// allocating one (and recording an Import) on first sight.
	MusicalDesign(resource string) musical.Design

	// designURI is the reverse mapping. Empty when the design's Import
	// hasn't been observed yet (e.g. instruction reordering during replay).
	designURI(design musical.Design) string

	// resolveMaterialTexture turns a material-selection design into a
	// usable texture (plain image or shared-material atlas region).
	resolveMaterialTexture(design musical.Design) Texture2D.Instance
}

// Workbench is the slice of the UI shell an editor may adjust.
type Workbench interface {
	SetGizmos(gizmos []Gizmo)
}

// LightingConsole drives the live world-lighting renderer state. It is the
// dependency of the embeddable lighting helper (see editor.go) rather than
// of editors directly.
type LightingConsole interface {
	ApplyLightingMenuState(timeOfDay, sunAngle, fog, clouds, rain, snow, wind, moon Float.X)
	GetLightingMenuState() (timeOfDay, sunAngle, fog, clouds, rain, snow, wind, moon Float.X)
}

// Client provides every editor capability.
var (
	_ Recorder        = (*Client)(nil)
	_ Library         = (*Client)(nil)
	_ Workbench       = (*Client)(nil)
	_ LightingConsole = (*Client)(nil)
)
