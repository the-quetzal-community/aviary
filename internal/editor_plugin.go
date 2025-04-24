package internal

import (
	"graphics.gd/classdb"
	BaseEditorPlugin "graphics.gd/classdb/EditorPlugin"
	"graphics.gd/classdb/GLTFDocument"
)

type EditorPlugin struct {
	classdb.Extension[EditorPlugin, BaseEditorPlugin.Instance] `gd:"AviaryEditorPlugin"`
	classdb.Tool
}

func (ml *EditorPlugin) EnterTree() {
	GLTFDocument.RegisterGltfDocumentExtension(new(ModelLoader).Super(), false)
}

func (ml *EditorPlugin) ExitTree() {
	GLTFDocument.UnregisterGltfDocumentExtension(new(ModelLoader).Super())
}
