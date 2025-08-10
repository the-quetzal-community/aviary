package internal

import (
	"graphics.gd/classdb/BaseButton"
	"graphics.gd/classdb/Control"
	"graphics.gd/classdb/DirAccess"
	"graphics.gd/classdb/HBoxContainer"
	"graphics.gd/classdb/PropertyTweener"
	"graphics.gd/classdb/Resource"
	"graphics.gd/classdb/SceneTree"
	"graphics.gd/classdb/Texture2D"
	"graphics.gd/classdb/TextureButton"
	"graphics.gd/classdb/TextureRect"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Signal"
	"graphics.gd/variant/Vector2"
)

type ThemeSelector struct {
	Control.Extension[ThemeSelector]

	Pointer TextureRect.Instance
	Themes  HBoxContainer.Instance

	ThemeSelected Signal.Solo[int] `gd:"theme_selected"`
}

func (selector *ThemeSelector) Ready() {
	themes := DirAccess.Open("res://library")
	if themes == DirAccess.Nil {
		selector.Themes.AsNode().QueueFree()
		return
	}
	for _, theme := range themes.GetDirectories() {
		var button = TextureButton.New()
		button.SetTextureNormal(Resource.Load[Texture2D.Instance]("res://library/" + theme + "/icon.png"))
		button.SetIgnoreTextureSize(true)
		button.SetStretchMode(TextureButton.StretchKeepAspectCentered)
		button.AsControl().SetCustomMinimumSize(Vector2.New(64, 64))
		selector.Themes.AsNode().AddChild(button.AsNode())
	}
	start := selector.Pointer.AsControl().Position()
	selector.AsControl().SetPosition(Vector2.Sub(selector.AsControl().Position(), Vector2.New(selector.Themes.AsControl().Size().X/2, 0)))
	for i, theme := range selector.Themes.AsNode().GetChildren() {
		Object.To[BaseButton.Instance](theme).OnPressed(func() {
			moveto := Vector2.Add(start, Vector2.New(float32(i)*68, 0))
			PropertyTweener.Make(SceneTree.Get(selector.AsNode()).CreateTween(), selector.Pointer.AsObject(), "position", moveto, 0.2)
			selector.ThemeSelected.Emit(i + 1)
		})
	}
	selector.ThemeSelected.Emit(1)
}
