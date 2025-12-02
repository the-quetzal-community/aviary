package internal

import (
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/Resource"
	"graphics.gd/classdb/Texture2D"
	"graphics.gd/variant/Enum"
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
		world.ui.EditorIndicator.EditorIcon.AsTextureButton().SetTextureNormal(Resource.Load[Texture2D.Instance]("res://ui/scenery.svg"))
	case Editing.Terrain:
		editor = world.TerrainEditor
		world.ui.EditorIndicator.EditorIcon.AsTextureButton().SetTextureNormal(Resource.Load[Texture2D.Instance]("res://ui/terrain.svg"))
	case Editing.Foliage:
		editor = world.FoliageEditor
		world.FocalPoint.SetPosition(Vector3.New(0, 0, 0))
		pos := world.FocalPoint.Lens.AsNode3D().Position()
		pos.Y = 4
		world.FocalPoint.Lens.AsNode3D().SetPosition(pos)
		world.ui.EditorIndicator.EditorIcon.AsTextureButton().SetTextureNormal(Resource.Load[Texture2D.Instance]("res://ui/foliage.svg"))
	case Editing.Mineral:
		editor = world.MineralEditor
		world.ui.EditorIndicator.EditorIcon.AsTextureButton().SetTextureNormal(Resource.Load[Texture2D.Instance]("res://ui/mineral.svg"))
	case Editing.Shelter:
		editor = world.ShelterEditor
		world.ui.EditorIndicator.EditorIcon.AsTextureButton().SetTextureNormal(Resource.Load[Texture2D.Instance]("res://ui/shelter.svg"))
	case Editing.Vehicle:
		editor = world.VehicleEditor
		world.ui.EditorIndicator.EditorIcon.AsTextureButton().SetTextureNormal(Resource.Load[Texture2D.Instance]("res://ui/vehicle.svg"))
	}
	editor.AsNode3D().SetVisible(true)
	editor.AsNode3D().AsNode().SetProcessMode(Node.ProcessModeInherit)
	world.Editing = subject
	world.ui.Editor.editor = editor
	editor.EnableEditor()
	world.ui.Editor.Refresh(subject, world.ui.themes[world.ui.theme_index], world.ui.mode)
	UserState.Editor = subject
	world.saveUserState()
}

type Editor interface {
	Node3D.Any

	musical.UsersSpace3D

	Name() string
	Tabs(mode Mode) []string

	EnableEditor()
	ChangeEditor()

	SelectDesign(mode Mode, design string)

	SliderConfig(mode Mode, editing string) (init, min, max, step float64)
	SliderHandle(mode Mode, editing string, value float64, commit bool)
}

/*
	type ExampleEditor struct {
		Node3D.Extension[ExampleEditor]
	}

	func (*ExampleEditor) Name() string            { return "example" }
	func (*ExampleEditor) Tabs(mode Mode) []string { return nil }

	func (*ExampleEditor) EnableEditor() {}
	func (*ExampleEditor) ChangeEditor() {}

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
