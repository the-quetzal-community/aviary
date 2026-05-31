package internal

import (
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/Texture2D"
	"graphics.gd/variant/Enum"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Vector3"

	"the.quetzal.community/aviary/internal/musical"
)

type Subject Enum.Int[struct {
	Scenery Subject
	Terrain Subject
	Foliage Subject
	Mineral Subject
	Shelter Subject
	Vehicle Subject
	Citizen Subject
	Critter Subject
	Coaster Subject
}]

var Editing = Enum.Values[Subject]()

// Mode represents whether the editor is currently in geometry or material mode.
type Mode int

const (
	ModeGeometry Mode = iota // add/remove/move/scale/rotate components.
	ModeDressing             // add props, details & decorations
	ModeMaterial             // add colours, paint textures & set materials
)

func (world *Client) StartEditing(subject Subject) {
	if world.ui.Editor.editor != nil {
		world.ui.Editor.editor.ChangeEditor()
	}
	var editors = []Editor{
		world.SceneryEditor,
		world.TerrainEditor,
		world.FoliageEditor,
		world.MineralEditor,
		world.ShelterEditor,
		world.VehicleEditor,
		world.CitizenEditor,
		world.CritterEditor,
		world.CoasterEditor,
	}
	for _, editor := range editors {
		editor.AsNode3D().SetVisible(false)
		editor.AsNode3D().AsNode().SetProcessMode(Node.ProcessModeDisabled)
	}
	pos := world.FocalPoint.Lens.AsNode3D().Position()
	pos.Y = 0
	world.FocalPoint.Lens.AsNode3D().SetPosition(pos)
	var editor Editor
	switch subject {
	case Editing.Scenery:
		editor = world.SceneryEditor
		world.TerrainEditor.AsNode3D().SetVisible(true)
		world.ui.EditorIndicator.EditorIcon.AsTextureButton().SetTextureNormal(LoadSync[Texture2D.Instance]("res://ui/scenery.svg"))
	case Editing.Terrain:
		editor = world.TerrainEditor
		world.ui.EditorIndicator.EditorIcon.AsTextureButton().SetTextureNormal(LoadSync[Texture2D.Instance]("res://ui/terrain.svg"))
	case Editing.Foliage:
		editor = world.FoliageEditor
		world.FocalPoint.SetPosition(Vector3.New(0, 0, 0))
		pos := world.FocalPoint.Lens.AsNode3D().Position()
		pos.Y = 4
		world.FocalPoint.Lens.AsNode3D().SetPosition(pos)
		world.ui.EditorIndicator.EditorIcon.AsTextureButton().SetTextureNormal(LoadSync[Texture2D.Instance]("res://ui/foliage.svg"))
	case Editing.Mineral:
		editor = world.MineralEditor
		world.ui.EditorIndicator.EditorIcon.AsTextureButton().SetTextureNormal(LoadSync[Texture2D.Instance]("res://ui/mineral.svg"))
	case Editing.Shelter:
		editor = world.ShelterEditor
		world.ui.EditorIndicator.EditorIcon.AsTextureButton().SetTextureNormal(LoadSync[Texture2D.Instance]("res://ui/shelter.svg"))
	case Editing.Vehicle:
		editor = world.VehicleEditor
		world.ui.EditorIndicator.EditorIcon.AsTextureButton().SetTextureNormal(LoadSync[Texture2D.Instance]("res://ui/vehicle.svg"))
	case Editing.Citizen:
		editor = world.CitizenEditor
		world.ui.EditorIndicator.EditorIcon.AsTextureButton().SetTextureNormal(LoadSync[Texture2D.Instance]("res://ui/citizen.svg"))
	case Editing.Critter:
		editor = world.CritterEditor
		world.ui.EditorIndicator.EditorIcon.AsTextureButton().SetTextureNormal(LoadSync[Texture2D.Instance]("res://ui/critter.svg"))
	case Editing.Coaster:
		editor = world.CoasterEditor
		world.TerrainEditor.AsNode3D().SetVisible(true)
		world.ui.EditorIndicator.EditorIcon.AsTextureButton().SetTextureNormal(LoadSync[Texture2D.Instance]("res://ui/coaster.svg"))
	}
	editor.AsNode3D().SetVisible(true).
		AsNode().SetProcessMode(Node.ProcessModeInherit)
	world.Editing = subject
	world.ui.Editor.editor = editor

	// Give the incoming editor a clean default gizmo toolbar; the editor's
	// EnableEditor (which runs immediately below) may call SetGizmos to
	// restrict it to only the tools it supports.
	if world.ui.CloudControl != nil {
		world.SetGizmos([]Gizmo{
			GizmoPoint, GizmoShift, GizmoTwist, GizmoFloat,
			GizmoSpace, GizmoClone, GizmoTrash,
		})
	}

	editor.EnableEditor()
	world.ui.ViewSelector.Refresh(0, editor.Views())
	world.ui.Editor.Refresh(subject, "", world.ui.mode)
	if world.ui.CloudControl != nil {
		// The terrain brush-size slider lives in the gizmo toolbar and is
		// only relevant while sculpting/painting/dressing terrain. The
		// density slider is shown only while dressing.
		te := world.TerrainEditor
		hasBrush := subject == Editing.Terrain && (te.PaintActive || te.DressActive || te.TerrainBrush != "")
		world.ui.CloudControl.setSizeSliderVisible(hasBrush)
		world.TerrainEditor.SetWaterVisible(subject == Editing.Terrain || subject == Editing.Scenery)
		world.ui.CloudControl.setDensitySliderVisible(subject == Editing.Terrain && world.ui.mode == ModeDressing && te.DressActive)
		world.ui.CloudControl.setPowerSliderVisible(subject == Editing.Terrain && world.ui.mode == ModeGeometry && te.TerrainBrush != "")
	}
	// In terrain mode placed objects must be transparent to the cursor:
	// they can't be selected and they must not block the terrain brush
	// raycast (so the ground can be painted underneath them). Make them
	// non-pickable here and pickable again in every other mode. Also drop
	// any carried-over selection so its outline/gizmo don't linger.
	setPickableExceptTerrain(world.AsNode(), subject != Editing.Terrain)
	if subject == Editing.Terrain && world.selection != 0 {
		if node, ok := world.selection.Instance(); ok {
			Select(node.AsNode(), false)
		}
		world.selection = 0
	}

	UserState.Editor = subject
	world.saveUserState()
}

// SetGizmos configures which gizmo tools are visible in the toolbar while
// the caller is the active editor. Editors should invoke this (typically
// once, from their EnableEditor method) with the subset of [Gizmo] values
// they want to offer, in the exact vertical order they want them rendered.
//
// Example usage inside an editor:
//
//	func (e *MyEditor) EnableEditor() {
//	    e.client.SetGizmos([]Gizmo{GizmoShift, GizmoTwist})
//	}
//
// Each gizmo button loads its icon from res://ui/gizmo/<name>.svg
// (GizmoPoint → point.svg, GizmoShift → shift.svg, etc.).
//
// GizmoSpace inserts a separator. GizmoClone/GizmoTrash insert the
// Duplicate/Delete action buttons at that position in the list.
//
// Passing an empty (or nil) slice hides the entire gizmo column.
// If SetGizmos is never called, the historical default set remains.
func (world *Client) SetGizmos(gizmos []Gizmo) {
	if world.ui == nil || world.ui.CloudControl == nil {
		return
	}
	world.ui.CloudControl.SetGizmos(gizmos)
}

type Editor interface {
	Node3D.Any

	musical.UsersSpace3D

	Name() string
	Tabs(mode Mode) []string
	Views() []string

	EnableEditor() // called when changing to this editor.
	ChangeEditor() // called when changind to another editor.

	SwitchToView(view string)

	SelectDesign(mode Mode, design string)

	SliderConfig(mode Mode, editing string) (init, min, max, step float64)
	SliderHandle(mode Mode, editing string, value float64, commit bool)
}

// ClickableEditor is the contract for editors whose placed entities can
// be picked in the viewport (mouse click or VR pointer) and then deleted,
// duplicated, or gizmo-manipulated. The selection and gizmo systems act
// on the result of a click, so rather than the client switching on
// world.Editing and reaching into each editor's private entity maps, it
// asks the active editor these questions directly — the knowledge of how
// an editor tracks its entities, names itself for musical routing, and
// gates gizmo interaction stays with the editor that owns it.
//
// Adoption is incremental: editors implement this as they migrate off
// the client's per-editor switches. The client falls back to its
// existing handling (the global object_to_entity map, no Editor routing
// string) for editors that don't implement it — which is exactly the
// Scenery behaviour, so Scenery deliberately does NOT implement it.
type ClickableEditor interface {
	// EditorID is the routing string stamped into musical.Change.Editor
	// so the change dispatches back to this editor's Change handler (and
	// matched by its `change.Editor != id` guard). It is distinct from
	// Name(), which is a display name — e.g. BoulderEditor.Name() is
	// "boulder" but it routes changes as "mineral".
	EditorID() string

	// EntityForNode resolves a picked scene node to the entity it
	// belongs to. owner is the node that actually carries the entity:
	// editors that nest pickable children under an entity root (e.g.
	// shelter parts under a floor anchor) walk up to it, so the gizmo
	// transforms the right node. ok is false when the node isn't a
	// placed entity this editor tracks.
	EntityForNode(node Node3D.Instance) (entity musical.Entity, owner Node3D.Instance, ok bool)

	// DesignForNode resolves a picked node to the design it was placed
	// from, for Duplicate (which re-enters preview mode with that
	// design). ok is false when the node isn't a tracked entity or its
	// design can't be recovered.
	DesignForNode(node Node3D.Instance) (design musical.Design, ok bool)

	// GizmoManipulable reports whether the editor currently allows gizmo
	// translate/twist/scale of its selection. Editors with modal
	// sub-views (critter's ribcage/limbone/control own their own drag
	// interactions) return false while those views are active.
	GizmoManipulable() bool
}

// lighting is a small embeddable helper that gives an editor (or the shared
// world state on TerrainEditor) the friendly world-lighting controls: Time of
// Day, Sun Angle, Fog, Clouds, and weather (Rain, Snow, Wind). It stores these
// friendly values directly so that adjusting one axis never discards the others.
// The actual renderer mutation lives in Client.ApplyLightingMenuState.
type lighting struct {
	timeOfDay Float.X
	sunAngle  Float.X
	fog       Float.X
	clouds    Float.X
	rain      Float.X
	snow      Float.X
	wind      Float.X

	// seeded is used so that the first time an editor becomes active it can
	// inherit the current world look instead of snapping to zero values
	// (timeOfDay 0 is midnight, which would render the scene pitch black).
	seeded bool
}

// handleEnvironmentSlider updates a single friendly lighting axis, leaving the
// others untouched. It returns true if it consumed the message.
func (l *lighting) handleEnvironmentSlider(slider string, v Float.X) bool {
	switch slider {
	case "environment/time_of_day":
		l.timeOfDay = v
	case "environment/sun_angle":
		l.sunAngle = v
	case "environment/fog":
		l.fog = v
	case "environment/clouds":
		l.clouds = v
	case "environment/rain":
		l.rain = v
	case "environment/snow":
		l.snow = v
	case "environment/wind":
		l.wind = v
	default:
		return false
	}
	return true
}

// apply pushes the current values to the live renderer via the Client.
func (l *lighting) apply(c *Client) {
	if c == nil {
		return
	}
	l.ensureSeeded(c)
	c.ApplyLightingMenuState(l.timeOfDay, l.sunAngle, l.fog, l.clouds, l.rain, l.snow, l.wind)
}

// ensureSeeded copies the current world lighting state into this lighting the
// first time it is used, so a newly-activated editor inherits the world look
// instead of snapping to zero values (midnight / energy 0 / black).
func (l *lighting) ensureSeeded(c *Client) {
	if l.seeded || c == nil {
		return
	}
	l.timeOfDay, l.sunAngle, l.fog, l.clouds, l.rain, l.snow, l.wind = c.GetLightingMenuState()
	l.seeded = true
}

// BuiltinDesign is one procedural/builtin entry that an editor wants
// shown in a tab alongside (or instead of) library scenes. Used by
// editors whose part categories include shapes generated in code
// rather than backed by an imported .glb — e.g. the critter editor's
// procedural foreleg. The design explorer renders these as tiles
// before the library scan in the same tab.
type BuiltinDesign struct {
	// Resource is the sentinel string passed back to SelectDesign so
	// the editor can recognise its own builtin tile. By convention
	// "procedural://<editor>/<name>" so it never collides with a real
	// file path from a library directory.
	Resource string
	// Icon is an optional res:// path to a thumbnail texture. When
	// empty the tile shows a label-only fallback.
	Icon string
	// Label is shown in tooltips / accessibility metadata.
	Label string
}

// BuiltinDesignProvider is an optional interface that an [Editor]
// can implement to inject procedural tiles into the design explorer.
// The design explorer checks for it via a type assertion, so most
// editors don't need to know it exists.
type BuiltinDesignProvider interface {
	BuiltinDesigns(mode Mode, tab string) []BuiltinDesign
}

/*
	type ExampleEditor struct {
		Node3D.Extension[ExampleEditor]
	}

	func (*ExampleEditor) Name() string            { return "example" }
	func (*ExampleEditor) Tabs(mode Mode) []string { return nil }
	func (*ExampleEditor) Views() []string         { return nil }

	func (*ExampleEditor) EnableEditor() {}
	func (*ExampleEditor) ChangeEditor() {}

	func (*ExampleEditor) SwitchToView(view string) {}

	func (*ExampleEditor) SelectDesign(mode Mode, design string) {}

	func (*ExampleEditor) SliderConfig(mode Mode, editing string) (init, min, max, step float64) {
		return 0, 0, 1, 0.01
	}
	func (*ExampleEditor) SliderHandle(mode Mode, editing string, value float64, commit bool) {}
*/

var TextureTabs = []string{
	"timbers",
	"fabrics",
	"cobbles",
	"cements",
	"painted",
	"glasses",
}
