package internal

import (
	"graphics.gd/classdb/BaseButton"
	"graphics.gd/classdb/Control"
	"graphics.gd/classdb/HBoxContainer"
	"graphics.gd/classdb/PropertyTweener"
	"graphics.gd/classdb/SceneTree"
	"graphics.gd/classdb/TextureRect"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Vector2"
)

type ThemeSelector struct {
	Control.Extension[ThemeSelector]

	Pointer TextureRect.Instance
	Themes  HBoxContainer.Instance
}

func (selector *ThemeSelector) Ready() {
	selector.AsControl().SetPosition(Vector2.Sub(selector.AsControl().Position(), Vector2.New(selector.Themes.AsControl().Size().X/2, 0)))
	start := selector.Pointer.AsControl().Position()
	for i, theme := range selector.Themes.AsNode().GetChildren() {
		Object.To[BaseButton.Instance](theme).OnPressed(func() {
			moveto := Vector2.Add(start, Vector2.New(float32(i)*68, 0))
			PropertyTweener.Make(SceneTree.Get(selector.AsNode()).CreateTween(), selector.Pointer.AsObject(), "position", moveto, 0.2)
		})
	}
}
