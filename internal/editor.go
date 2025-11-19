package internal

import (
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/Resource"
	"graphics.gd/classdb/Texture2D"
	"graphics.gd/variant/Vector3"
)

type Subject int

const (
	Scenery Subject = iota
	Terrain
	Foliage
)

// Mode represents whether the editor is currently in geometry or material mode.
type Mode bool

const (
	ModeGeometry Mode = false // add/remove/move/scale/rotate components.
	ModeMaterial Mode = true  // add colours, paint textures & set materials
)

func (world *Client) StartEditing(subject Subject) {
	world.TerrainEditor.AsNode3D().SetVisible(false)
	world.FoliageEditor.AsNode3D().SetVisible(false)
	pos := world.FocalPoint.Lens.AsNode3D().Position()
	pos.Y = 0
	world.FocalPoint.Lens.AsNode3D().SetPosition(pos)
	var editor Editor
	switch subject {
	case Scenery:
		editor = world.SceneryEditor
		world.TerrainEditor.AsNode3D().SetVisible(true)
		world.ui.EditorIndicator.EditorIcon.AsTextureButton().SetTextureNormal(Resource.Load[Texture2D.Instance]("res://ui/scenery.svg"))
	case Terrain:
		editor = world.TerrainEditor
		world.ui.EditorIndicator.EditorIcon.AsTextureButton().SetTextureNormal(Resource.Load[Texture2D.Instance]("res://ui/terrain.svg"))
	case Foliage:
		editor = world.FoliageEditor
		world.FocalPoint.SetPosition(Vector3.New(0, 0, 0))
		pos := world.FocalPoint.Lens.AsNode3D().Position()
		pos.Y = 4
		world.FocalPoint.Lens.AsNode3D().SetPosition(pos)
		world.ui.EditorIndicator.EditorIcon.AsTextureButton().SetTextureNormal(Resource.Load[Texture2D.Instance]("res://ui/foliage.svg"))
	}
	editor.AsNode3D().SetVisible(true)
	world.ui.Editor.editor = editor
	world.ui.Editor.Refresh(world.ui.themes[world.ui.theme_index], world.ui.mode)
}

type Editor interface {
	Node3D.Any

	Tabs(mode Mode) []string

	SelectDesign(mode Mode, design string)
	AdjustSlider(mode Mode, editing string, value float64, commit bool)
}
