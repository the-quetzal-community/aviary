package internal

import (
	"graphics.gd/classdb"
	BaseEditorPlugin "graphics.gd/classdb/EditorPlugin"
	"graphics.gd/classdb/GLTFDocument"
)

type EditorPlugin struct {
	BaseEditorPlugin.Extension[EditorPlugin] `gd:"AviaryEditorPlugin"`
	classdb.Tool

	modelLoader *ModelLoader
}

func (ml *EditorPlugin) EnterTree() {
	ml.modelLoader = new(ModelLoader)
	GLTFDocument.RegisterGltfDocumentExtension(ml.modelLoader.AsGLTFDocumentExtension(), false)
}

func (ml *EditorPlugin) ExitTree() {
	GLTFDocument.UnregisterGltfDocumentExtension(ml.modelLoader.AsGLTFDocumentExtension())
}
