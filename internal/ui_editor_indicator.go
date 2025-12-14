package internal

import (
	"graphics.gd/classdb/Control"
	"graphics.gd/classdb/Panel"
	"graphics.gd/classdb/PropertyTweener"
	"graphics.gd/classdb/TextureButton"
	"graphics.gd/classdb/VBoxContainer"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Vector2"
)

type EditorIndicator struct {
	Control.Extension[EditorIndicator]

	Triangle *Triangle

	EditorIcon     TextureButton.Instance
	EditorSelector struct {
		Panel.Instance

		EditorTypes VBoxContainer.Instance
	}

	editorPos            Vector2.XY
	editorSelectorOpened bool
	editorAnimating      bool

	client *Client
}

func (ed *EditorIndicator) Ready() {
	ed.Triangle.AsControl().SetAnchorsPreset(Control.PresetFullRect)
	ed.EditorIcon.AsBaseButton().OnPressed(ed.toggle)
	for i, child := range ed.EditorSelector.EditorTypes.AsNode().GetChildren() {
		button, ok := Object.As[TextureButton.Instance](child)
		if ok {
			button.AsBaseButton().OnPressed(func() {
				if ed.editorAnimating {
					return
				}
				var subject Subject
				subject.SetInt(i)
				ed.client.StartEditing(subject)
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
