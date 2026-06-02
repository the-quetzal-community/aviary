package clouds

import (
	"graphics.gd/classdb/Environment"
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/RenderingServer"
	"graphics.gd/variant/Object"
)

// Mode selects which cloud renderer is active. It is the package's own concept,
// NOT a graphics-quality tier — the importer maps its quality policy onto these.
// Ordered cheapest first.
type Mode int

const (
	// ModeFlat is the cheap flat 2D sky-shader projection (sky.gdshader, no march).
	ModeFlat Mode = iota
	// ModeSkyMarch is the sky-shader volumetric march (sky.gdshader, cloud_steps>0).
	ModeSkyMarch
	// ModeFogVolume is the world-space FogVolume fly-through layer
	// (clouds_fog.gdshader, drawn by the Environment's volumetric fog).
	ModeFogVolume
	// ModeSunshine is the SunshineClouds2 compositor addon (the most expensive /
	// highest-fidelity option).
	ModeSunshine
)

// cloudSteps is the sky.gdshader cloud_steps uniform for this mode: 0 selects the
// flat 2D projection, a positive count switches on the volumetric march (the
// number of samples), and a negative value disables the painted sky clouds
// entirely (a heavier renderer — FogVolume or SunshineClouds2 — draws them).
func (m Mode) cloudSteps() int {
	switch m {
	case ModeSkyMarch:
		// Primary raymarch samples per sky pixel. This is the Average tier's
		// hot loop, so it is kept deliberately low: the Beer's-law accumulation
		// normalises opacity by the step size (att = d*dt*CLOUD_DENSITY), so
		// dropping steps softens only fine vertical detail, not overall density.
		// Paired with the cheap 2-step, detail-free sun-ward shadow march in
		// sky.gdshader, this keeps the per-pixel fbm count a fraction of what a
		// full 40-step / 4-light-step march cost.
		return 28
	case ModeFogVolume, ModeSunshine:
		return -1
	default: // ModeFlat
		return 0
	}
}

// SetMode switches the active cloud renderer. It pushes cloud_steps into the sky
// shader, toggles the Environment's volumetric fog (on only for ModeFogVolume),
// and attaches/detaches the SunshineClouds2 compositor effect (on only for
// ModeSunshine). Safe to call repeatedly — the remove-before-add keeps re-enabling
// idempotent, so settings-slider moves never stack duplicate effects.
func (s *System) SetMode(m Mode) {
	if s == nil {
		return
	}
	s.sky.SetShaderParameter("cloud_steps", m.cloudSteps())
	if s.env != Environment.Nil {
		s.env.SetVolumetricFogEnabled(m == ModeFogVolume)
	}
	if s.cloudsDriver != (Node.Instance{}) {
		Object.Call(s.cloudsDriver, "clouds_res_removed")
		if m == ModeSunshine {
			Object.Call(s.cloudsDriver, "clouds_res_added")
		}
	}
	EnsureShadowGlobals()
	// Ground cloud shadows on the terrain are off in the cheapest mode (ModeFlat /
	// Toaster tier) and on everywhere else.
	cloudShadows := float64(1.0)
	if m == ModeFlat {
		cloudShadows = 0.0
	}
	RenderingServer.GlobalShaderParameterSet("cloud_shadow_enabled", cloudShadows)
	// The cloud shadow map is produced only by the SunshineClouds2 compute pass; off
	// every other mode, so flag it inactive and let the ground shaders fall back to
	// the procedural cloud-shadow noise. At ModeSunshine the addon re-flags it live
	// each frame.
	if m != ModeSunshine {
		RenderingServer.GlobalShaderParameterSet("cloud_shadow_map_active", float64(0.0))
	}
}
