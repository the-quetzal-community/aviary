package clouds

import (
	"sync"

	"graphics.gd/classdb/PlaceholderTexture2D"
	"graphics.gd/classdb/RenderingServer"
	"graphics.gd/variant/Vector2"
	"graphics.gd/variant/Vector3"
)

var shadowGlobalsOnce sync.Once

// EnsureShadowGlobals registers the global shader parameters that drive the
// terrain cloud-shadow term (terrain.gdshader: cloud_shadow_factor). The sun
// direction is pushed by System.SetSunDirection, the coverage by
// System.SetDensity, and the drift by System.SetWind. Registered lazily — the
// renderer must know a global before any shader that reads it is drawn, and the
// Once makes the multiple setter call sites safe.
//
// Exposed as a package function (not a System method) because the importer must
// register these BEFORE the terrain shader first compiles, which happens before
// the cloud System is constructed.
func EnsureShadowGlobals() {
	shadowGlobalsOnce.Do(func() {
		RenderingServer.GlobalShaderParameterAdd("cloud_shadow_sun_dir", RenderingServer.GlobalVarTypeVec3, Vector3.New(0, 1, 0))
		RenderingServer.GlobalShaderParameterAdd("cloud_coverage", RenderingServer.GlobalVarTypeFloat, float64(0.0))
		RenderingServer.GlobalShaderParameterAdd("cloud_shadow_wind", RenderingServer.GlobalVarTypeVec2, Vector2.New(0.6, 0.2))
		// Master on/off for the terrain ground cloud-shadow term. Defaults on; SetMode
		// turns it off for ModeFlat (the cheapest renderer / Toaster tier).
		RenderingServer.GlobalShaderParameterAdd("cloud_shadow_enabled", RenderingServer.GlobalVarTypeFloat, float64(1.0))
		// Cloud shadow map (the matched, SunshineClouds2-derived coverage texture). The
		// sampler global 'cloud_shadow_map' is registered + filled by the addon; these are
		// its numeric companions. active=0 keeps ground shaders on the procedural fallback
		// until the addon's compute pass is live (ModeSunshine) — see System.SetMode.
		RenderingServer.GlobalShaderParameterAdd("cloud_shadow_map_center", RenderingServer.GlobalVarTypeVec2, Vector2.Zero)
		RenderingServer.GlobalShaderParameterAdd("cloud_shadow_map_extent", RenderingServer.GlobalVarTypeFloat, float64(256.0))
		RenderingServer.GlobalShaderParameterAdd("cloud_shadow_map_active", RenderingServer.GlobalVarTypeFloat, float64(0.0))
		// The sampler global itself is registered here too (with a placeholder) so the
		// terrain shader resolves it at first compile; the addon overwrites the value with
		// its live RD texture each frame at ModeSunshine.
		RenderingServer.GlobalShaderParameterAdd("cloud_shadow_map", RenderingServer.GlobalVarTypeSampler2d, PlaceholderTexture2D.New())
	})
}
