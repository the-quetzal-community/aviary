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

// cameraView is the synthetic view appended to every editor's view row: a
// "photo mode" that hides the whole editor overlay for a clean screenshot.
// Selecting it is handled by UI (see UI.enterPhotoMode), not by the active
// editor's SwitchToView. It borrows the shelter's binoculars icon
// (explore.svg) since it has no icon file of its own, and is momentary — it
// never becomes the auto-selected default and never moves the view pointer.
const cameraView = "camera"

func (selector *ViewSelector) Refresh(view int, views []string) {
	for _, child := range selector.Views.AsNode().GetChildren() {
		selector.Views.AsNode().RemoveChild(child)
		child.QueueFree()
	}
	// Append the photo/camera view to every editor (copying so the caller's
	// slice isn't mutated). Centralising it here means the feature lives in
	// one place instead of in all nine editors' Views().
	views = append(append([]string{}, views...), cameraView)
	selector.views = views
	selector.view = view
	// The camera always sits last. A default index landing on it means the
	// editor has no real views, so there is nothing to auto-select and no
	// pointer to show — only the lone camera button.
	cameraIndex := len(views) - 1
	hasDefault := view >= 0 && view < cameraIndex
	selector.AsCanvasItem().SetVisible(true)
	for _, view := range views {
		if symbols, ok := strings.CutPrefix(view, "unicode/"); ok {
			var label = Button.New().
				SetText(symbols).
				AsControl().SetCustomMinimumSize(Vector2.New(64, 64))
			selector.Views.AsNode().AddChild(label.AsNode())
		} else {
			var button = TextureButton.New()
			// The camera view has no icon of its own; reuse the binoculars.
			switch {
			case view == cameraView:
				button.SetTextureNormal(LoadSync[Texture2D.Instance]("res://ui/explore.svg"))
			case FileAccess.FileExists("res://ui/" + view + ".svg.import"):
				button.SetTextureNormal(LoadSync[Texture2D.Instance]("res://ui/" + view + ".svg"))
			default:
				button.SetTextureNormal(LoadSync[Texture2D.Instance]("res://ui/dressing.svg"))
			}
			button.
				SetIgnoreTextureSize(true).
				SetStretchMode(TextureButton.StretchKeepAspectCentered).
				AsControl().SetCustomMinimumSize(Vector2.New(64, 64))
			selector.Views.AsNode().AddChild(button.AsNode())
		}
	}
	selector.AsControl().SetPosition(Vector2.Sub(selector.AsControl().Position(), Vector2.New(selector.Views.AsControl().Size().X/2, 0)))
	for i, theme := range selector.Views.AsNode().GetChildren() {
		Object.To[BaseButton.Instance](theme).OnPressed(func() {
			// The camera is momentary — it hides the overlay rather than
			// switching to a persistent view — so it doesn't move the
			// pointer, leaving it on the real active view for when the UI
			// comes back.
			if views[i] != cameraView {
				child := Object.To[Control.Instance](selector.Views.AsNode().GetChild(i))
				moveto := Vector2.New(child.Position().X+child.Size().X/2-6, 0)
				PropertyTweener.Make(SceneTree.Get(selector.AsNode()).CreateTween(), selector.Pointer.AsObject(), "position", moveto, 0.2)
			}
			selector.ViewSelected.Emit(views[i])
		})
	}
	display := DisplayServer.WindowGetSize(0)
	theme_pos := selector.AsControl().Position()
	theme_scale := selector.AsControl().Scale()
	theme_size := selector.AsControl().Size()
	theme_pos.X = (Float.X(display.X)/2 - (theme_size.X * theme_scale.X * Float.X(len(views))))
	selector.AsControl().SetPosition(theme_pos)

	// The pointer marks the active real view; hide it (and skip the initial
	// view emit) when only the camera button is present.
	selector.Pointer.AsCanvasItem().SetVisible(hasDefault)
	if hasDefault {
		selector.ViewSelected.Emit(views[view])
		RenderingServer.OnFramePostDraw(func() {
			child := Object.To[Control.Instance](selector.Views.AsNode().GetChild(view))
			moveto := Vector2.New(child.Position().X+child.Size().X/2-6, 0)
			PropertyTweener.Make(SceneTree.Get(selector.AsNode()).CreateTween(), selector.Pointer.AsObject(), "position", moveto, 0.2)
		}, Signal.OneShot)
	}
}
