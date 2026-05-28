package internal

import (
	"graphics.gd/classdb/Control"
	"graphics.gd/classdb/Panel"
	"graphics.gd/classdb/TextureButton"
	"graphics.gd/classdb/VBoxContainer"
	"graphics.gd/variant/Object"
)

type EditorIndicator struct {
	Control.Extension[EditorIndicator]

	Triangle *Triangle

	EditorIcon     TextureButton.Instance
	EditorSelector struct {
		Panel.Instance

		EditorTypes VBoxContainer.Instance
	}

	rollout Rollout

	client *Client
}

func (ed *EditorIndicator) Ready() {
	ed.Triangle.AsControl().SetAnchorsPreset(Control.PresetFullRect)
	ed.EditorIcon.AsBaseButton().OnPressed(ed.toggle)
	for i, child := range ed.EditorSelector.EditorTypes.AsNode().GetChildren() {
		button, ok := Object.As[TextureButton.Instance](child)
		if ok {
			button.AsBaseButton().OnPressed(func() {
				if ed.rollout.Animating() {
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
	ed.rollout.Toggle(ed.EditorSelector.AsControl())
}
