package internal

import (
	"graphics.gd/classdb/CanvasItem"
	"graphics.gd/classdb/Control"
	"graphics.gd/classdb/Gradient"
	"graphics.gd/classdb/GradientTexture2D"
	"graphics.gd/classdb/PropertyTweener"
	"graphics.gd/classdb/TextureRect"
	"graphics.gd/classdb/Tween"
	"graphics.gd/variant/Color"
	"graphics.gd/variant/Vector2"
)

// offlineRed is the colour of every "no internet" indicator.
var offlineRed = Color.RGBA{R: 0.9, G: 0.12, B: 0.1, A: 1}

// newOfflineDot builds the red "no internet" dot: a soft radial disc drawn
// from a gradient (same construction as the cloud control's OnlineIndicator),
// size px across. It starts hidden; pair it with [pulseOffline].
func newOfflineDot(size float32) TextureRect.Instance {
	grad := Gradient.New()
	grad.SetOffsets([]float32{0.3, 0.42, 0.5})
	grad.SetColors([]Color.RGBA{offlineRed, {R: 0.35, G: 0.04, B: 0.03, A: 1}, {R: 1, G: 1, B: 1, A: 0}})
	tex := GradientTexture2D.New()
	tex.SetGradient(grad).SetFill(GradientTexture2D.FillRadial).SetFillFrom(Vector2.New(0.5, 0.5)).SetWidth(64).SetHeight(64)
	dot := TextureRect.New()
	dot.SetTexture(tex.AsTexture2D()).SetExpandMode(TextureRect.ExpandIgnoreSize).SetStretchMode(TextureRect.StretchScale)
	dot.AsControl().SetSize(Vector2.New(size, size))
	dot.AsControl().SetMouseFilter(Control.MouseFilterIgnore)
	dot.AsCanvasItem().SetVisible(false)
	return dot
}

// pulseOffline loops a gentle fade on item so an offline indicator reads as a
// live state rather than decoration. Kill the returned tween to stop it.
func pulseOffline(item CanvasItem.Instance) Tween.Instance {
	tw := item.AsNode().CreateTween().SetLoops()
	PropertyTweener.Make(tw, item.AsObject(), "modulate:a", 0.35, 0.6).SetTrans(Tween.TransSine).SetEase(Tween.EaseInOut)
	PropertyTweener.Make(tw, item.AsObject(), "modulate:a", 1.0, 0.6).SetTrans(Tween.TransSine).SetEase(Tween.EaseInOut)
	return tw
}

// shakeRefused gives control a quick horizontal shake with a red flash — the
// wordless "that didn't work" reaction.
func shakeRefused(control Control.Instance) {
	rest := control.Position()
	tw := control.AsNode().CreateTween()
	for _, dx := range []float32{-8, 8, -5, 5, 0} {
		PropertyTweener.Make(tw, control.AsObject(), "position", Vector2.New(rest.X+dx, rest.Y), 0.05)
	}
	control.AsCanvasItem().SetSelfModulate(offlineRed)
	PropertyTweener.Make(control.AsNode().CreateTween(), control.AsObject(), "self_modulate", Color.RGBA{R: 1, G: 1, B: 1, A: 1}, 0.4)
}
