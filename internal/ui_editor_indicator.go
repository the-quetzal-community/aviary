package internal

import (
	"graphics.gd/classdb/Control"
	"graphics.gd/classdb/Panel"
	"graphics.gd/classdb/TextureButton"
	"graphics.gd/classdb/TextureRect"
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

	// Shading is the lighting / environment rolldown trigger. It used to
	// live in the Toolbar but now sits beside the editor switcher; its
	// rollout (environmentRollout) and handler still live on UI, which
	// wires this button in UI.Setup.
	Shading TextureButton.Instance

	// Arrows is the up/down chevron overlaid on the editor icon; it spins
	// when the selector rolls out and in (wired as the rollout's icon).
	Arrows TextureRect.Instance

	rollout Rollout

	client *Client
}

func (ed *EditorIndicator) Ready() {
	ed.Triangle.AsControl().SetAnchorsPreset(Control.PresetFullRect)
	ed.EditorIcon.AsBaseButton().OnPressed(ed.toggle)
	ed.rollout.icon = ed.Arrows.AsControl()
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
