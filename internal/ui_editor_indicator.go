package internal

import (
	"graphics.gd/classdb/Panel"
	"graphics.gd/classdb/PropertyTweener"
	"graphics.gd/classdb/TextureButton"
	"graphics.gd/classdb/TextureRect"
	"graphics.gd/classdb/VBoxContainer"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Vector2"
)

type EditorIndicator struct {
	TextureRect.Extension[EditorIndicator]

	EditorIcon     TextureButton.Instance
	EditorSelector struct {
		Panel.Instance

		EditorTypes VBoxContainer.Instance
	}

	editorPos            Vector2.XY
	editorSelectorOpened bool
	editorAnimating      bool

	free_list []Object.Instance

	client *Client
}

func (ed *EditorIndicator) Ready() {
	ed.EditorIcon.AsBaseButton().OnPressed(ed.toggle)
	for _, child := range ed.EditorSelector.EditorTypes.AsNode().GetChildren() {
		button, ok := Object.As[TextureButton.Instance](child)
		if ok {
			normal := Object.Leak(button.AsTextureButton().TextureNormal())
			ed.free_list = append(ed.free_list, normal.AsObject())
			button.AsBaseButton().OnPressed(func() {
				if ed.editorAnimating {
					return
				}
				ed.EditorIcon.AsTextureButton().SetTextureNormal(normal)
				ed.toggle()
			})
		}
	}
}

func (ed *EditorIndicator) toggle() {
	if ed.editorAnimating {
		return
	}
	ed.editorAnimating = true
	ed.editorSelectorOpened = !ed.editorSelectorOpened
	if ed.editorSelectorOpened {
		ed.editorPos = ed.EditorSelector.AsControl().Position()
		next_pos := ed.editorPos
		next_pos.Y = 0
		PropertyTweener.Make(ed.EditorSelector.AsNode().CreateTween(), ed.EditorSelector.AsObject(), "position", next_pos, 0.2).AsTweener().OnFinished(func() {
			ed.editorAnimating = false
		})
	} else {
		PropertyTweener.Make(ed.EditorSelector.AsNode().CreateTween(), ed.EditorSelector.AsObject(), "position", ed.editorPos, 0.2).AsTweener().OnFinished(func() {
			ed.editorAnimating = false
		})
	}
}

func (ed *EditorIndicator) ExitTree() {
	for _, obj := range ed.free_list {
		Object.Free(obj)
	}
	ed.free_list = nil
}
