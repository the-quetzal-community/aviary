package internal

import (
	"graphics.gd/classdb/Environment"
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/RenderingServer"
	"graphics.gd/classdb/Viewport"
)

// GraphicsQuality is a coarse, single-axis quality level driven by the
// toaster→sports-car slider in the Settings menu. Each step trades
// fidelity for frame-rate by adjusting 3D antialiasing and shadow
// filtering together, so the user doesn't have to understand MSAA vs
// FXAA vs shadow atlas size — they just slide from "toaster" (potato)
// to "sports car" (max). The upper two tiers additionally switch on
// screen-space ambient occlusion (SSAO) for grounded contact shadows.
type GraphicsQuality int

const (
	// QualityToaster: no MSAA, no AA, hard (unfiltered) shadows, small
	// shadow atlas. Cheapest — the low end of the slider.
	QualityToaster GraphicsQuality = iota
	// QualityAverage: FXAA only, very-low soft shadows.
	QualityAverage
	// QualityRefined: 2× MSAA, low soft shadows, larger atlas,
	// medium-quality SSAO.
	QualityRefined
	// QualityHighest: 4× MSAA, high soft shadows, large atlas,
	// high-quality SSAO.
	// Most expensive — the high end of the slider. (We deliberately stop
	// at 4× MSAA and leave TAA off: 8× MSAA + TAA together stalled the
	// renderer hard on first runtime apply.)
	QualityHighest
)

// graphicsQualitySteps is the number of discrete positions on the
// slider; the HSlider is configured 0..graphicsQualitySteps-1.
const graphicsQualitySteps = QualityHighest + 1

// defaultGraphicsQuality is the fallback applied on first launch (or when
// no persisted choice exists in UserState). QualityRefined (2× MSAA) gives
// a smooth look without forcing the most expensive tier on first run.
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

// ssaoEnabled reports whether screen-space ambient occlusion should be on
// at this tier. SSAO is moderately expensive (a full-resolution depth
// pass), so we reserve it for the upper two "refined"/"sports car" tiers
// and leave the potato/average tiers free of it.
func (q GraphicsQuality) ssaoEnabled() bool {
	return q >= QualityRefined
}

// ssaoQuality maps each tier to the global SSAO sample/blur quality. Only
// meaningful where ssaoEnabled is true; the lower tiers return the
// cheapest level for completeness but never actually render SSAO.
func (q GraphicsQuality) ssaoQuality() RenderingServer.EnvironmentSSAOQuality {
	switch q {
	case QualityHighest:
		return RenderingServer.EnvSsaoQualityHigh
	case QualityRefined:
		return RenderingServer.EnvSsaoQualityMedium
	default:
		return RenderingServer.EnvSsaoQualityVeryLow
	}
}

// cloudSteps is the cloud_steps uniform pushed into the procedural sky shader at
// this tier (see Client.applyCloudQuality, which owns the wiring). 0 selects the
// shader's cheap flat 2D projection (QualityToaster); a positive count switches on
// the sky-shader volumetric march (QualityAverage); a negative value disables the
// painted sky clouds at the top two tiers, where the FogVolume (QualityRefined,
// fogVolumeClouds) and SunshineClouds2 (QualityHighest, sunshineClouds) draw the
// clouds instead.
func (q GraphicsQuality) cloudSteps() int {
	switch q {
	case QualityHighest:
		return -1 // sky clouds off; SunshineClouds2 draws fly-through clouds.
	case QualityRefined:
		return -1 // sky clouds off; the world-space FogVolume draws the clouds.
	case QualityAverage:
		// Sky-shader volumetric march (demoted from QualityRefined); fewer steps
		// than the old Refined value since this is now the mid tier.
		return 40
	default: // QualityToaster: flat clouds, no march.
		return 0
	}
}

// fogVolumeClouds reports whether the world-space FogVolume cloud layer (the
// clouds_fog.gdshader fly-through clouds) should be active at this tier. Reserved
// for QualityRefined: it is the second-most-expensive cloud option (a per-frame
// volumetric-fog froxel pass), sitting just below the SunshineClouds2 compositor
// clouds at QualityHighest.
func (q GraphicsQuality) fogVolumeClouds() bool {
	return q == QualityRefined
}

// sunshineClouds reports whether the SunshineClouds2 compositor cloud system (the
// vendored addon, the most expensive / highest-fidelity option) should be active.
// Reserved for QualityHighest.
func (q GraphicsQuality) sunshineClouds() bool {
	return q == QualityHighest
}

// reflectionStrength is the screen-space-reflection intensity pushed into the
// water shader's reflection_strength uniform (see Client.applyWaterQuality).
// The water is transparent, so Godot's Environment SSR can't reach it and the
// shader ray-marches its own reflections — a per-water-pixel loop of depth
// fetches, the priciest water effect. Reserved for QualityHighest: 1 turns the
// march fully on; every cheaper tier returns 0, which makes the shader skip the
// loop entirely.
func (q GraphicsQuality) reflectionStrength() float64 {
	if q == QualityHighest {
		return 1.0
	}
	return 0.0
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

	// SSAO sample/blur quality is global renderer state, like the shadow
	// filter above; the per-Environment on/off flag is set separately by
	// ApplyAmbientOcclusion. The trailing args are Godot's stock defaults
	// (no half-res, adaptive_target 0.5, 2 blur passes, fade 50→300).
	RenderingServer.EnvironmentSetSsaoQuality(q.ssaoQuality(), false, 0.5, 2, 50, 300)
}

// ApplyAmbientOcclusion toggles screen-space ambient occlusion on the
// given world Environment for this quality tier. It is kept separate from
// Apply because the on/off flag lives on the Environment resource (which
// the Client owns and creates after the UI's launch-time Apply runs),
// whereas Apply only reaches per-viewport and global renderer state. Safe
// to call with a nil Environment (no-op), so callers needn't guard the
// pre-creation launch window.
func (q GraphicsQuality) ApplyAmbientOcclusion(env Environment.Instance) {
	if env == Environment.Nil {
		return
	}
	env.SetSsaoEnabled(q.ssaoEnabled())
}
