package internal

import (
	"strings"

	"graphics.gd/classdb/BaseButton"
	"graphics.gd/classdb/Button"
	"graphics.gd/classdb/Control"
	"graphics.gd/classdb/DisplayServer"
	"graphics.gd/classdb/FileAccess"
	"graphics.gd/classdb/HBoxContainer"
	"graphics.gd/classdb/PropertyTweener"
	"graphics.gd/classdb/RenderingServer"
	"graphics.gd/classdb/Resource"
	"graphics.gd/classdb/SceneTree"
	"graphics.gd/classdb/Texture2D"
	"graphics.gd/classdb/TextureButton"
	"graphics.gd/classdb/TextureRect"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Signal"
	"graphics.gd/variant/Vector2"
)

type ViewSelector struct {
	Control.Extension[ViewSelector]

	Pointer TextureRect.Instance
	Views   HBoxContainer.Instance
	views   []string
	view    int

	ViewSelected Signal.Solo[string]
}

func (selector *ViewSelector) Refresh(view int, views []string) {
	for _, child := range selector.Views.AsNode().GetChildren() {
		child.AsObject()[0].Free()
	}
	selector.views = views
	selector.view = view
	if len(views) == 0 {
		selector.AsCanvasItem().SetVisible(false)
		return
	}
	selector.AsCanvasItem().SetVisible(true)
	for _, view := range views {
		if strings.HasPrefix(view, "unicode/") {
			var label = Button.New()
			label.SetText(strings.TrimPrefix(view, "unicode/"))
			label.AsControl().SetCustomMinimumSize(Vector2.New(64, 64))
			selector.Views.AsNode().AddChild(label.AsNode())
		} else {
			var button = TextureButton.New()
			if FileAccess.FileExists("res://ui/" + view + ".svg.import") {
				button.SetTextureNormal(Resource.Load[Texture2D.Instance]("res://ui/" + view + ".svg"))
			} else {
				button.SetTextureNormal(Resource.Load[Texture2D.Instance]("res://ui/dressing.svg"))
			}
			button.SetIgnoreTextureSize(true)
			button.SetStretchMode(TextureButton.StretchKeepAspectCentered)
			button.AsControl().SetCustomMinimumSize(Vector2.New(64, 64))
			selector.Views.AsNode().AddChild(button.AsNode())
		}
	}
	selector.AsControl().SetPosition(Vector2.Sub(selector.AsControl().Position(), Vector2.New(selector.Views.AsControl().Size().X/2, 0)))
	for i, theme := range selector.Views.AsNode().GetChildren() {
		Object.To[BaseButton.Instance](theme).OnPressed(func() {
			child := Object.To[Control.Instance](selector.Views.AsNode().GetChild(i))
			moveto := Vector2.New(child.Position().X+child.Size().X/2-6, 0)
			PropertyTweener.Make(SceneTree.Get(selector.AsNode()).CreateTween(), selector.Pointer.AsObject(), "position", moveto, 0.2)
			selector.ViewSelected.Emit(views[i])
		})
	}
	display := DisplayServer.WindowGetSize(0)
	theme_pos := selector.AsControl().Position()
	theme_scale := selector.AsControl().Scale()
	theme_size := selector.AsControl().Size()
	theme_pos.X = (Float.X(display.X)/2 - (theme_size.X * theme_scale.X * Float.X(len(views))))
	selector.AsControl().SetPosition(theme_pos)

	selector.ViewSelected.Emit(views[view])
	RenderingServer.OnFramePostDraw(func() {
		child := Object.To[Control.Instance](selector.Views.AsNode().GetChild(view))
		moveto := Vector2.New(child.Position().X+child.Size().X/2-6, 0)
		PropertyTweener.Make(SceneTree.Get(selector.AsNode()).CreateTween(), selector.Pointer.AsObject(), "position", moveto, 0.2)
	}, Signal.OneShot)
}
