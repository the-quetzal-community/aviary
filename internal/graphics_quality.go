package internal

import (
	"graphics.gd/classdb/Environment"
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/OS"
	"graphics.gd/classdb/RenderingServer"
	"graphics.gd/classdb/Viewport"
	"the.quetzal.community/aviary/internal/clouds"
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
	// QualityToaster: no MSAA, no AA, and no real-time shadows at all (the
	// shadow pass is dropped entirely here — see shadowsEnabled). Cheapest — the
	// low end of the slider.
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

// shadowsEnabled reports whether the directional (sun + moon) lights cast
// real-time shadows at this tier. Disabled at QualityToaster: the shadow pass is
// a genuine cost on the lowest-end target, and its small 1024 atlas makes it the
// worst-looking tier regardless (the banding / peter-panning that bias alone
// can't fully tame on fat texels). Dropping shadows there buys frame-rate and
// sidesteps the artifacts entirely; every higher tier keeps them. The terrain's
// shader-faked cloud shadows are independent of this and stay on.
func (q GraphicsQuality) shadowsEnabled() bool {
	return q > QualityToaster
}

// shadowBias maps each tier to the directional-shadow depth bias and normal
// bias applied to both the sun and moon lights (see Client.applyShadowQuality).
// The biases must scale UP as the directional shadow atlas
// (directionalShadowAtlasSize) shrinks: a lower-res atlas spreads each shadow
// texel across more world space, so the depth gradient across one texel grows
// and self-shadowing "acne" — the horizontal banding on gently-sloped terrain —
// reappears. The high tiers keep the original hand-tuned low bias (0.015 depth,
// 0 normal): their dense atlas has no acne and the low depth bias avoids
// peter-panning (the shadow detaching from the caster's base).
//
// The low tiers lean on NORMAL bias rather than more DEPTH bias. Depth bias is a
// constant push into the light: enough of it to kill acne on the fat low-res
// texels over-biases the near contacts and detaches their shadows (peter-pan),
// which is exactly the both-at-once artifact on QualityToaster. Normal bias
// instead offsets the shadow lookup along the surface normal, so it adapts to
// the grazing angle — killing acne where the texels straddle a slope without
// pulling flat contacts off the ground. So depth bias barely moves across tiers;
// the normal bias carries the correction.
func (q GraphicsQuality) shadowBias() (bias, normalBias float64) {
	switch q {
	case QualityToaster: // 1024 atlas: fattest texels, needs the most normal bias.
		return 0.02, 2.0
	case QualityAverage: // 2048 atlas.
		return 0.02, 1.0
	case QualityRefined: // 4096 atlas.
		return 0.015, 0.5
	default: // QualityHighest, 8192 atlas: original low bias, no acne.
		return 0.015, 0.0
	}
}

// ssaoEnabled reports whether screen-space ambient occlusion should be on
// at this tier. SSAO is moderately expensive (a full-resolution depth
// pass), so we reserve it for the upper two "refined"/"sports car" tiers
// and leave the potato/average tiers free of it.
func (q GraphicsQuality) ssaoEnabled() bool {
	return q >= QualityRefined
}

// sdfgiEnabled reports whether signed-distance-field global illumination should
// be on at this tier. SDFGI is fully dynamic real-time GI (no bake), which suits
// this open terrain world, but it is the priciest lighting feature here — a
// per-frame SDF + cascade update — so it is reserved for QualityHighest alone.
// Scenery and terrain participate via their default (static) GI mode; the
// scattered grass is deliberately excluded (GiModeDisabled, see
// buildPatchNodes) so the dense instances never burden the GI pass.
func (q GraphicsQuality) sdfgiEnabled() bool {
	return q == QualityHighest
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

// cloudMode maps this quality tier to the cloud renderer the clouds package
// should use (see Client.applyCloudQuality). This is the importer's policy: the
// clouds package owns the renderers (Modes) and their settings; it knows nothing
// about graphics-quality tiers, so the mapping lives here. Cheapest first:
// Toaster's flat sky projection, Average's sky-shader march, Refined's world-
// space FogVolume, and Highest's SunshineClouds2 compositor.
func (q GraphicsQuality) cloudMode() clouds.Mode {
	switch q {
	case QualityHighest:
		return clouds.ModeSunshine
	case QualityRefined:
		return clouds.ModeFogVolume
	case QualityAverage:
		return clouds.ModeSkyMarch
	default: // QualityToaster
		return clouds.ModeFlat
	}
}

// simpleWater reports whether the cheap water shader (water_simple.gdshader — a
// flat blue transparent surface with one scrolling normal map, no depth/screen
// fetches, foam, swell, or reflections) should be bound instead of the full
// water.gdshader. Reserved for QualityToaster: the full shader's per-pixel depth
// + screen samples and foam octaves are exactly the bandwidth the potato tier
// can't spare, and the simple surface still reads as water. Swapped in by
// Client.applyWaterQuality, which binds the matching Shader onto the shared water
// material; the simple shader keeps the same geometry contract so terrain edits
// still weld the water to the ground.
func (q GraphicsQuality) simpleWater() bool {
	return q == QualityToaster
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
			// SunshineClouds2 (the Highest-tier compositor clouds) writes to a
			// multisampled storage image on its MSAA code path, which Metal/macOS
			// does not support (no writable MS textures) — so the cloud effect
			// produces nothing on Mac whenever the viewport is MSAA. Vulkan
			// (Windows/Linux) supports it, so keep 4× MSAA there; on macOS fall back
			// to FXAA so the Metal-safe non-MSAA cloud path runs and clouds appear.
			// (Trade-off: Mac loses MSAA edge AA at Highest, incl. grass
			// alpha-to-coverage, which degrades to a hard scissor.)
			if OS.GetName() == "macOS" {
				vp.SetMsaa3d(Viewport.MsaaDisabled)
				vp.SetScreenSpaceAa(Viewport.ScreenSpaceAaFxaa)
			} else {
				vp.SetMsaa3d(Viewport.Msaa4x)
				vp.SetScreenSpaceAa(Viewport.ScreenSpaceAaDisabled)
			}
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
	// ApplyEnvironmentQuality. The trailing args are Godot's stock defaults
	// (no half-res, adaptive_target 0.5, 2 blur passes, fade 50→300).
	RenderingServer.EnvironmentSetSsaoQuality(q.ssaoQuality(), false, 0.5, 2, 50, 300)
}

// ApplyEnvironmentQuality toggles the per-tier lighting flags that live on the
// world Environment resource — screen-space ambient occlusion (ssaoEnabled) and
// signed-distance-field global illumination (sdfgiEnabled). It is kept separate
// from Apply because these flags live on the Environment resource (which the
// Client owns and creates after the UI's launch-time Apply runs), whereas Apply
// only reaches per-viewport and global renderer state. Safe to call with a nil
// Environment (no-op), so callers needn't guard the pre-creation launch window.
func (q GraphicsQuality) ApplyEnvironmentQuality(env Environment.Instance) {
	if env == Environment.Nil {
		return
	}
	env.SetSsaoEnabled(q.ssaoEnabled())
	env.SetSdfgiEnabled(q.sdfgiEnabled())
}
