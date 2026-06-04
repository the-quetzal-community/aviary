// Package clouds owns every cloud-rendering subsystem and its configuration.
//
// There are four mutually-exclusive cloud renderers, exposed as Mode values
// (cheapest first): a flat 2D sky projection, a sky-shader volumetric march, a
// world-space FogVolume fly-through layer, and the vendored SunshineClouds2
// compositor addon. A single System owns all of them at once; SetMode switches
// which one is active at runtime. The package is deliberately ignorant of the
// app's graphics-quality tiers — the importer maps its own quality policy onto
// these Modes (see internal: GraphicsQuality.cloudMode) and drives every setting
// dynamically through the System methods. The package never imports internal, so
// there is no import cycle and no enum mirroring the quality tiers.
//
// The terrain cloud-shadow term (terrain.gdshader) is fed by global shader
// parameters this package owns too (see shadows.go); the terrain shader only
// reads them by name, so there is no Go-level coupling.
package clouds

import (
	"graphics.gd/classdb/Camera3D"
	"graphics.gd/classdb/DirectionalLight3D"
	"graphics.gd/classdb/Environment"
	"graphics.gd/classdb/FogVolume"
	"graphics.gd/classdb/Light3D"
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/RenderingServer"
	"graphics.gd/classdb/Resource"
	"graphics.gd/classdb/Script"
	"graphics.gd/classdb/Shader"
	"graphics.gd/classdb/ShaderMaterial"
	"graphics.gd/classdb/Sky"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Vector2"
	"graphics.gd/variant/Vector3"
)

// Cloud layer geometry for the world-space volumetric FogVolume (ModeFogVolume).
// The box must be TALL (centred at cloudLayerY, cloudLayerThickness tall) so the
// camera is always inside it: a thin box sitting above the camera gets frustum-
// culled the moment you pitch down to look away from it, popping the whole cloud
// layer in/out as you rotate. With the camera inside, the volume is never culled
// and the layer renders smoothly at every view angle. The clouds' actual altitude
// is NOT the box — it's confined to a world-Y band in the shader
// (cloud_base/cloud_top), so the box can be tall without fog reaching the ground.
// The box follows the camera in XZ each frame (see FollowCamera) so the
// cloudLayerSpanXZ footprint feels endless.
const (
	cloudLayerY         Float.X = 16
	cloudLayerThickness Float.X = 120
	// Wider than 2× the fog length so the layer's footprint always outruns the
	// volumetric-fog reach — the clouds fade out smoothly (via the shader's radial
	// horizon_fade) well inside the box, never at a hard box edge.
	cloudLayerSpanXZ Float.X = 200
)

// Resources are the four preloaded resources the System is built from. They are
// loaded by the importer (via its resource-loader thread, which is package-
// private to internal) and passed in, so this package never touches the loader
// and stays free of any internal import.
type Resources struct {
	SkyShader    Shader.Instance   // res://shader/sky.gdshader
	FogShader    Shader.Instance   // res://shader/clouds_fog.gdshader
	DriverScript Script.Instance   // res://addons/SunshineClouds2/SunshineCloudsDriver.gd
	Effect       Resource.Instance // res://addons/SunshineClouds2/aviary_clouds.tres
}

// System owns the three cloud-rendering subsystems (procedural sky shader,
// world-space FogVolume, SunshineClouds2 compositor effect) plus the terrain
// cloud-shadow globals. All methods are safe to call on a nil *System (no-op),
// so the importer needn't guard the pre-construction launch window.
type System struct {
	sky          ShaderMaterial.Instance // procedural sky material (coverage/steps/wind/moon)
	cloudFog     ShaderMaterial.Instance // FogVolume material
	cloudVolume  FogVolume.Instance      // the box that follows the camera
	cloudsDriver Node.Instance           // SunshineClouds2 GDScript driver (Object.Set/Call)
	driverScript Script.Instance         // SunshineCloudsDriver.gd; held so Free can release it
	cloudsEffect Resource.Instance       // aviary_clouds.tres CompositorEffect
	env          Environment.Instance    // borrowed; owned by the importer
}

// New builds every cloud backend and returns the System in its default state
// (ModeFlat until SetMode is called):
//   - the procedural sky material + Sky, set as env's BgSky background;
//   - the volumetric-fog parameters on env (for the FogVolume clouds);
//   - the FogVolume box, parented under parent;
//   - the SunshineClouds2 driver, parented under parent and tracking sun.
//
// env is borrowed — the importer owns it (ambient/tonemap/exposure live there).
// IMPORTANT: the importer's WorldEnvironment must already be in the scene tree
// before New is called: the SunshineClouds2 driver attaches its CompositorEffect
// by walking the tree for a WorldEnvironment the moment clouds_resource is
// assigned (which New does), and finds nothing if it isn't there yet.
func New(parent Node.Instance, env Environment.Instance, sun DirectionalLight3D.Instance, res Resources) *System {
	s := &System{env: env}

	// Procedural sky with drifting clouds. The shader reacts to the directional
	// light (LIGHT0); only the cloud coverage is pushed in, by the Clouds slider.
	skyMaterial := ShaderMaterial.New()
	skyMaterial.SetShader(res.SkyShader)
	skyMaterial.SetShaderParameter("coverage", 0.0)
	s.sky = skyMaterial
	sky := Sky.New()
	sky.SetSkyMaterial(skyMaterial.AsMaterial())
	sky.SetRadianceSize(Sky.RadianceSize256)
	env.SetBackgroundMode(Environment.BgSky)
	env.SetSky(sky)

	// Drive the volumetric cloud layer harder than the default 1.0 so the sunlit
	// faces read as bright cumulus rather than flat grey. With single-scatter fog
	// (no multiple-scattering brightening) this energy is the WHOLE layer's
	// brightness, so it is a knife-edge: 3.0 blew it to a flat neon white, but 2.0
	// dimmed the entire layer to uniform rain-cloud grey. 2.8 keeps the sunlit tops
	// bright; the not-neon look is recovered instead from the off-white albedo and
	// the lowered ambient inject below (which darkens the bases for real cumulus
	// form). Only affects volumetric fog (the FogVolume clouds); surface lighting
	// is unchanged.
	Light3D.Advanced(sun.AsLight3D()).SetParam(Light3D.ParamVolumetricFogEnergy, 2.8)

	// Volumetric cloud layer (ModeFogVolume). A box-shaped FogVolume sitting at the
	// cloud altitude geometrically confines the fog to its bounds, so it can never
	// fog the terrain below it — that confinement is the box, not the shader (the
	// boundless "world" shape gave no reliable per-froxel world Y, so a shader
	// height-band leaked fog everywhere). Base volumetric density stays 0 so these
	// clouds are the only fog; anisotropy gives a forward silver lining and a
	// little ambient inject keeps undersides off pure black. SetMode toggles
	// VolumetricFogEnabled (on only for ModeFogVolume).
	env.SetVolumetricFogDensity(0)
	// Keep the region small: the froxel buffer is spread across this length, so a
	// shorter reach packs the froxels denser = sharper clouds. The trade-off is the
	// clouds form a contained layer around the camera and fade out at this radius
	// rather than stretching (blurrily) to the horizon — by design here. The
	// shader's horizon_fade default (≈ length/span) tapers density to nothing just
	// inside this so the fog-length cut-off isn't visible.
	env.SetVolumetricFogLength(80)
	// Mild forward scattering: enough to keep a silver lining on the sun-facing
	// side, but low so the cloud isn't strongly view-dependent. The fog is single-
	// scatter, so a strong forward phase (0.5) made the away-from-sun side fall to
	// grey — the bulk "white from any angle" of real cumulus comes from multiple
	// scattering, which a single Henyey-Greenstein lobe can't reproduce. 0.2 trades
	// some of the silver lining for a more uniformly-lit, whiter body (with the
	// ambient inject below carrying the shadowed/away faces).
	env.SetVolumetricFogAnisotropy(0.2)
	// Lift the shadowed bases with sky ambient so they're cumulus-grey, not dark
	// grey — the single-scatter fog has no multiple-scattering brightening, so this
	// stands in for it. Too high would flatten them (everything reads white), too
	// low leaves them dim. 0.7 (was 0.9) keeps a touch more sun->shadow contrast so
	// the clouds have form rather than a uniform bright wash.
	env.SetVolumetricFogAmbientInject(0.7)
	env.SetVolumetricFogGiInject(0.0)
	// Enlarge the volumetric-fog froxel buffer (default 64×64×64) so the clouds
	// aren't a low-res blur: more width/height sharpens them across the screen,
	// more depth sharpens them into the distance. The small cloud region (short fog
	// length) means each froxel covers little space, so a dense grid is affordable.
	// Global RenderingServer state, only paid when volumetric fog is on
	// (ModeFogVolume); dial back toward 128 if the froxel pass costs too much.
	RenderingServer.EnvironmentSetVolumetricFogVolumeSize(256, 256)
	cloudFog := ShaderMaterial.New()
	cloudFog.SetShader(res.FogShader)
	cloudFog.SetShaderParameter("coverage", 0.0)
	s.cloudFog = cloudFog
	cloudVolume := FogVolume.New()
	cloudVolume.SetShape(RenderingServer.FogVolumeShapeBox)
	// Full box size: a thin Y slab (the cloud layer + soft-edge margin) over a
	// wide XZ footprint. Centred at cloudLayerY, so it spans Y ∈ [cloudLayerY±7].
	cloudVolume.SetSize(Vector3.New(cloudLayerSpanXZ, cloudLayerThickness, cloudLayerSpanXZ))
	cloudVolume.SetMaterial(cloudFog.AsMaterial())
	cloudVolume.AsNode().SetName("CloudLayer")
	cloudVolume.AsNode3D().SetPosition(Vector3.New(0, cloudLayerY, 0))
	parent.AddChild(cloudVolume.AsNode())
	s.cloudVolume = cloudVolume

	// SunshineClouds2 (ModeSunshine): the vendored addon (MIT). It is a GDScript
	// CompositorEffect driver with no Go bindings, so it is instanced by attaching
	// its script to a bare Node and driven generically with Object.Set/Call. The
	// driver self-attaches its CompositorEffect to the WorldEnvironment's
	// compositor the moment clouds_resource is assigned — and BOTH the driver node
	// AND a WorldEnvironment must already be in the tree for that — so the driver is
	// AddChild'd (and the importer's WorldEnvironment created) BEFORE the resource
	// is assigned. Tracking sun makes the clouds follow the day/night cycle.
	cloudsDriver := Node.New()
	Object.Instance(cloudsDriver.AsObject()).SetScript(res.DriverScript)
	cloudsDriver.SetName("SunshineClouds")
	Object.Set(cloudsDriver, "ambience_sample_environment", env)
	// Light the clouds with the scene's actual sun energy (multiplier 1.0). The
	// cloud shader feeds this energy into BOTH the direct term (pow(energy,2.2),
	// which blows out fast) AND the ambient/shadow-fill floor (ambientLightColor *
	// totalLightPower) — so over-driving it (an earlier 8x) flooded the shadows
	// brighter than white and erased all shading, leaving a flat white blob. Keep
	// it at the real energy; tune the LOOK via the resource's hot-reloadable
	// cloud_ambient_color (shadow darkness), lighting_density and clouds_density.
	Object.Set(cloudsDriver, "directional_light_power_multiplier", 1.0)
	// Set the tracked sun through a GDScript helper rather than assigning the
	// Array[DirectionalLight3D] property directly: a Go slice / Array through the
	// generic Object.Set arrived as an untyped Variant array that the typed property
	// rejected, leaving it empty (no lights → pitch-black clouds). aviary_set_sun
	// takes one DirectionalLight3D (single-object Call marshals reliably) and builds
	// the typed array on the GDScript side. See SunshineCloudsDriver.gd.
	Object.Call(cloudsDriver, "aviary_set_sun", sun)
	parent.AddChild(cloudsDriver)
	s.cloudsDriver = cloudsDriver
	s.driverScript = res.DriverScript
	s.cloudsEffect = res.Effect
	// Assigning clouds_resource runs the driver's setter, attaching the effect to
	// the compositor (creating one if absent). SetMode then detaches it unless the
	// launch mode is ModeSunshine.
	Object.Set(cloudsDriver, "clouds_resource", res.Effect)
	// Enable per-frame updates ONLY now — after the node is in the tree AND the
	// resource is assigned. The driver's _ready (which ran synchronously during
	// AddChild above) force-disables update_continuously whenever clouds_resource is
	// still null at that moment, so enabling it any earlier silently sticks at false:
	// _process then never runs the light-tracking block and the clouds freeze on the
	// resource's placeholder directional light — never following the real sun (white
	// even at night, unaffected by Time of Day). Setting it here, with the resource
	// and sun both present, re-runs retrieve_texture_data so the clouds track the
	// day/night cycle.
	Object.Set(cloudsDriver, "update_continuously", true)

	return s
}

// Free releases the cloud resources this System owns so they don't report as leaks at
// exit. The FogVolume and driver NODES are freed with the scene tree; here we drop our
// refs on the two materials and the compositor effect. Freeing the effect releases its
// noise textures, compute shaders and the SunshineClouds2 scripts once the compositor and
// driver release theirs during teardown. env is borrowed (the importer owns it) and is
// left alone. Object.Free only decrements, so anything still in use at this point — the
// effect is still held by the compositor and driver — survives until teardown.
func (s *System) Free() {
	if s == nil {
		return
	}
	Object.Free(s.sky)
	Object.Free(s.cloudFog)
	Object.Free(s.cloudsEffect)
	// The driver node frees with the scene tree; dropping our Go ref on its script lets
	// it (and its only parse dependency, SunshineCloudEffector.gd) be released too.
	Object.Free(s.driverScript)
}

// SetDensity pushes the cloud coverage (0 = clear, 1 = overcast) into all cloud
// systems — the procedural sky shader, the world-space FogVolume, and the
// SunshineClouds2 effect — so the Clouds slider drives whichever Mode is active.
// The same coverage feeds the terrain cloud-shadow density global.
func (s *System) SetDensity(coverage Float.X) {
	if s == nil {
		return
	}
	s.sky.SetShaderParameter("coverage", coverage)
	s.cloudFog.SetShaderParameter("coverage", coverage)
	if s.cloudsEffect != (Resource.Instance{}) {
		Object.Set(s.cloudsEffect, "clouds_coverage", 0.5+0.5*coverage)
	}
	EnsureShadowGlobals()
	RenderingServer.GlobalShaderParameterSet("cloud_coverage", 0.5+0.5*coverage)
}

// SetWind drifts every cloud system (and the terrain cloud-shadow term) by the
// given wind amount (0 = gentle base breeze, 1 = full slider). Each system has
// its own base speed but shares the 1+1.8*w ramp so they stay in step.
func (s *System) SetWind(wind Float.X) {
	if s == nil {
		return
	}
	w := wind
	// Sky cloud drift speed scales with wind (base wind in shader is gentle).
	skyBase := Vector2.New(0.045, 0.014)
	s.sky.SetShaderParameter("cloud_wind", Vector2.New(
		float64(skyBase.X*(1+1.8*w)),
		float64(skyBase.Y*(1+1.8*w)),
	))
	// The world-space cloud FogVolume drifts with the same wind (its noise space
	// is in world units, so the base speed is much smaller than the sky's).
	fogBase := Vector2.New(0.03, 0.01)
	s.cloudFog.SetShaderParameter("cloud_wind", Vector2.New(
		float64(fogBase.X*(1+1.8*w)),
		float64(fogBase.Y*(1+1.8*w)),
	))
	// SunshineClouds2 advances each noise octave by its own structure wind speed
	// (world units/sec). The upstream defaults are km-scale (140/100/40/12); these
	// are scaled ~100x down to this world and ramped by the Wind slider with the
	// same 1+1.8*w shape, so a gentle drift persists at wind 0. wind_direction
	// keeps the resource default (1,0,1).
	if s.cloudsDriver != (Node.Instance{}) {
		ws := float64(1 + 1.8*w)
		Object.Set(s.cloudsDriver, "extra_large_structures_wind_speed", 1.4*ws)
		Object.Set(s.cloudsDriver, "large_structures_wind_speed", 1.0*ws)
		Object.Set(s.cloudsDriver, "medium_structures_wind_speed", 0.4*ws)
		Object.Set(s.cloudsDriver, "small_structures_wind_speed", 0.12*ws)
	}
	// Cloud shadows drift with the wind (world units/sec; see terrain.gdshader).
	EnsureShadowGlobals()
	wf := float64(w)
	RenderingServer.GlobalShaderParameterSet("cloud_shadow_wind", Vector2.New(0.6*(1.0+wf), 0.2*(1.0+wf)))
}

// SetSunDirection feeds the toward-sun world vector to the terrain cloud-shadow
// term (terrain.gdshader). The importer computes the vector from its own sun
// pitch/azimuth convention, so this package stays free of that convention.
func (s *System) SetSunDirection(dir Vector3.XYZ) {
	if s == nil {
		return
	}
	EnsureShadowGlobals()
	RenderingServer.GlobalShaderParameterSet("cloud_shadow_sun_dir", dir)
}

// SetMoonPhase hands the moon phase (0 = new, 0.5 = half, 1 = full) to the sky
// shader, which carves the visible crescent on the moon disk.
func (s *System) SetMoonPhase(phase Float.X) {
	if s == nil {
		return
	}
	s.sky.SetShaderParameter("moon_phase", phase)
}

// FollowCamera slides the cloud-layer FogVolume to stay centred on the camera in
// XZ (Y pinned to the layer altitude) so the finite box feels like an endless
// layer as the player pans. The clouds are sampled in world space, so they stay
// put in the world while the box slides beneath them.
func (s *System) FollowCamera(cam Camera3D.Instance) {
	if s == nil || s.cloudVolume == FogVolume.Nil {
		return
	}
	cp := cam.AsNode3D().GlobalPosition()
	s.cloudVolume.AsNode3D().SetGlobalPosition(Vector3.New(cp.X, cloudLayerY, cp.Z))
}
