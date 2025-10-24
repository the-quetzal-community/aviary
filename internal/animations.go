package internal

import (
	"graphics.gd/classdb/ImageTexture"
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/PropertyTweener"
	"graphics.gd/classdb/SceneTree"
	"graphics.gd/classdb/TextureRect"
	"graphics.gd/classdb/Viewport"
	"graphics.gd/variant/Vector2"
)

// AnimationSaving is played when the scene is explicitly saved with Ctrl+S
type AnimationSaving struct {
	TextureRect.Extension[AnimationSaving]
}

// AnimateTheSceneBeingSaved animates the scene being saved by adding [AnimationSaving]
// to the [SceneTree].
func AnimateTheSceneBeingSaved(parent Node.Any) {
	tex := ImageTexture.CreateFromImage(Viewport.Get(parent.AsNode()).GetTexture().AsTexture2D().GetImage())
	saving := new(AnimationSaving)
	saving.AsTextureRect().SetTexture(tex.AsTexture2D())
	parent.AsNode().AddChild(saving.AsNode())
}

// Ready implements [Node.Interface.Ready]
func (anim *AnimationSaving) Ready() {
	var tween = SceneTree.Get(anim.AsNode()).CreateTween()
	anim.AsNode().BindToTween(tween)
	PropertyTweener.Make(tween, anim.AsObject(), "scale", Vector2.XY{0.1, 0.1}, 0.5)
	tween.OnFinished(func() {
		anim.AsNode().QueueFree()
	})
}
