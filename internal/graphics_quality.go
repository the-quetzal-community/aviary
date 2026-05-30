package internal

import (
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/RenderingServer"
	"graphics.gd/classdb/Viewport"
)

// GraphicsQuality is a coarse, single-axis quality level driven by the
// toaster→sports-car slider in the Settings menu. Each step trades
// fidelity for frame-rate by adjusting 3D antialiasing and shadow
// filtering together, so the user doesn't have to understand MSAA vs
// FXAA vs shadow atlas size — they just slide from "toaster" (potato)
// to "sports car" (max).
type GraphicsQuality int

const (
	// QualityToaster: no MSAA, no AA, hard (unfiltered) shadows, small
	// shadow atlas. Cheapest — the low end of the slider.
	QualityToaster GraphicsQuality = iota
	// QualityAverage: FXAA only, very-low soft shadows.
	QualityAverage
	// QualityRefined: 2× MSAA, low soft shadows, larger atlas.
	QualityRefined
	// QualityHighest: 4× MSAA, high soft shadows, large atlas.
	// Most expensive — the high end of the slider. (We deliberately stop
	// at 4× MSAA and leave TAA off: 8× MSAA + TAA together stalled the
	// renderer hard on first runtime apply.)
	QualityHighest
)

// graphicsQualitySteps is the number of discrete positions on the
// slider; the HSlider is configured 0..graphicsQualitySteps-1.
const graphicsQualitySteps = QualityHighest + 1

// defaultGraphicsQuality is the level applied on launch and the slider's
// initial position. QualityHigh (2× MSAA) gives a smooth look without
// forcing the most expensive tier on first run.
const defaultGraphicsQuality = QualityRefined

// directionalShadowAtlasSize maps each quality level to the directional
// shadow atlas resolution. project.godot ships 8192; we only shrink it
// at the lower tiers and never grow beyond the shipped value.
func (q GraphicsQuality) directionalShadowAtlasSize() int {
	switch q {
	case QualityToaster:
		return 1024
	case QualityAverage:
		return 2048
	case QualityRefined:
		return 4096
	default: // QualityFerrari
		return 8192
	}
}

// Apply pushes this quality level into the live renderer. The Viewport
// settings (MSAA / screen-space AA / TAA) are per-viewport, resolved
// from any node in the tree; the shadow-filter quality and atlas size
// are global RenderingServer state. Called once at startup and again
// whenever the Settings slider moves, so changes are immediately
// visible without a restart.
func (q GraphicsQuality) Apply(anyNode Node.Instance) {
	vp := Viewport.Get(anyNode)
	if vp != Viewport.Nil {
		switch q {
		case QualityToaster:
			vp.SetMsaa3d(Viewport.MsaaDisabled)
			vp.SetScreenSpaceAa(Viewport.ScreenSpaceAaDisabled)
			vp.SetUseTaa(false)
		case QualityAverage:
			vp.SetMsaa3d(Viewport.MsaaDisabled)
			vp.SetScreenSpaceAa(Viewport.ScreenSpaceAaFxaa)
			vp.SetUseTaa(false)
		case QualityRefined:
			vp.SetMsaa3d(Viewport.Msaa2x)
			vp.SetScreenSpaceAa(Viewport.ScreenSpaceAaDisabled)
			vp.SetUseTaa(false)
		default: // QualityHighest
			vp.SetMsaa3d(Viewport.Msaa4x)
			vp.SetScreenSpaceAa(Viewport.ScreenSpaceAaDisabled)
			vp.SetUseTaa(false)
		}
	}

	var shadow RenderingServer.ShadowQuality
	switch q {
	case QualityToaster:
		shadow = RenderingServer.ShadowQualityHard
	case QualityAverage:
		shadow = RenderingServer.ShadowQualitySoftVeryLow
	case QualityRefined:
		shadow = RenderingServer.ShadowQualitySoftLow
	default: // QualityHighest
		shadow = RenderingServer.ShadowQualitySoftHigh
	}
	RenderingServer.DirectionalSoftShadowFilterSetQuality(shadow)
	RenderingServer.PositionalSoftShadowFilterSetQuality(shadow)
	RenderingServer.DirectionalShadowAtlasSetSize(q.directionalShadowAtlasSize(), false)
}
