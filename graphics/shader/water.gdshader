shader_type spatial;

// ============================================================================
// Water surface shader (rivers + lakes).
//
// GEOMETRY CONTRACT — set per-vertex by reloadWater() in editor_terrain.go:
//   VERTEX.y    : base water surface, world Y (the node sits at y=0, so
//                 mesh-local Y == world Y). For rivers this is the original
//                 ground minus a small lip; for lakes it is the global level.
//   CUSTOM0.r   : world-space terrain floor (channel/lake bed) at this vertex.
//   CUSTOM0.gb  : normalised world-XZ flow direction (0,0 for still water).
//   CUSTOM0.a   : DUAL-PURPOSE, disambiguated by NORMAL (flat plane vs. wall):
//                 * flat surface  -> downhill slope along flow (drives foam).
//                 * cut-through wall -> heave weight: 1 on the top row (follows
//                   the swell, welding to the plane), 0 on the bed row (pinned).
//   UV.y        : cut-through WALL only -> the column's water-surface world-Y,
//                 the same at the top and bed verts, so the wall colours by the
//                 water COLUMN depth (UV.y - v_floor = surface - bed), uniform down
//                 the slice and matching the flat plane at the waterline. The flat
//                 plane's UV is unused (it fades by screen-space depth instead).
//
// DESIGN NOTES
//   * Fine ripple detail comes from scrolling NORMAL MAPS (mip-filtered, so no
//     aliasing). Open/still water additionally gets LONG-wavelength geometric
//     Gerstner swells; their wavelengths are kept well above the ~1-unit mesh
//     cell so they don't alias (short waves aliasing on the coarse grid was the
//     original "horizontal line" artifact). Rivers (flow) stay flat — just the
//     normal-map ripples.
//   * Anything driven by TIME is kept BOUNDED so it can't decay over a long
//     session (an unbounded `dir * speed * TIME` offset loses float precision
//     and the surface degrades into stepped bands): the normal maps AND the
//     foam use a two-phase flow-map (offsets fract()'d into [0,1), half-cycle
//     cross-blend hides the reset), made contrast-preserving in flow_sample()
//     so it doesn't pulse crisp/blurry; the Gerstner phase is wrapped to [0,2π).
//   * Water that sits below the terrain floor is simply occluded by the opaque
//     terrain (no clamp needed).
// ============================================================================

render_mode blend_mix, depth_draw_always, cull_back;

// --- Textures (filled from TerrainEditor.Ready) -----------------------------
uniform sampler2D normalmap_a_sampler : hint_normal, filter_linear_mipmap, repeat_enable;
uniform sampler2D normalmap_b_sampler : hint_normal, filter_linear_mipmap, repeat_enable;
uniform sampler2D uv_sampler : hint_default_black, filter_linear_mipmap, repeat_enable;
uniform sampler2D foam_sampler : source_color, filter_linear_mipmap, repeat_enable;
uniform sampler2D screen_texture : hint_screen_texture, filter_linear_mipmap;
uniform sampler2D depth_texture : hint_depth_texture, filter_linear_mipmap;

// --- Surface detail ---------------------------------------------------------
uniform float normal_scale = 0.18;                     // world tiling of normal map A
uniform float normal_detail_scale = 0.41;              // world tiling of normal map B
uniform float normal_strength : hint_range(0.0, 2.0) = 0.6;
uniform float distort_scale = 0.12;                    // tiling of the UV distortion map
uniform float distort_strength = 0.03;
uniform float still_speed = 0.05;                      // drift speed of still (lake) water
uniform float flow_speed = 0.40;                       // advection speed of flowing water (world u/s)
uniform float flow_slope_gain : hint_range(0.0, 12.0) = 2.5; // how much surface slope speeds the current
uniform float cycle_seconds = 2.5;                     // flow-map cycle length

// --- Sea swell (geometric Gerstner waves, still water only) -----------------
// Long wavelengths ONLY: they must stay well above the ~1-unit water-mesh cell
// or the displacement aliases into line artifacts. The phase is wrapped to
// [0,2π) so it never decays as TIME grows. Faded out on rivers (flow) so only
// the open "sea" heaves; faded out in shallow water so waves never poke through
// the terrain at the shoreline. Each wave is (dir.x, dir.y, steepness, length).
uniform vec4 wave_a = vec4(1.0, 0.6, 0.12, 14.0);
uniform vec4 wave_b = vec4(0.7, 1.0, 0.10, 9.0);
uniform vec4 wave_c = vec4(1.0, 1.3, 0.08, 6.0);
uniform float wave_speed = 0.6;
uniform float wave_height : hint_range(0.0, 3.0) = 0.2;

// --- Colour / depth ---------------------------------------------------------
uniform vec4 color_shallow : source_color = vec4(0.20, 0.45, 0.52, 1.0);
uniform vec4 color_deep : source_color = vec4(0.06, 0.16, 0.28, 1.0);
uniform float depth_fade = 1.0;                        // how fast colour saturates with depth
uniform float refraction = 0.04;
uniform float foam_amount : hint_range(0.0, 2.0) = 0.25;
uniform float foam_edge : hint_range(0.0, 4.0) = 0.7;  // shoreline foam distance
uniform float foam_scale = 0.22;                       // world tiling of the foam texture
uniform float river_foam_amount : hint_range(0.0, 2.0) = 0.25; // wispy whitewater across the flowing surface
uniform float slope_foam_gain : hint_range(0.0, 8.0) = 2.5;    // how much surface slope grows whitewater
uniform float shallow_calm : hint_range(0.0, 2.0) = 0.5;
uniform float flow_opacity : hint_range(0.0, 0.8) = 0.30; // extra opacity for rivers
uniform float min_alpha : hint_range(0.0, 1.0) = 0.45;

// --- Reflections (Highest quality tier only) --------------------------------
// Screen-space reflection intensity. The water is transparent (blend_mix), so
// Godot's Environment SSR — which only runs on the opaque GBuffer — never
// reaches it; the sky still reflects via the material's specular IBL, but
// on-screen scene geometry (banks, trees, placed objects) does not. So we
// ray-march the reflection ourselves against the depth buffer below. 0 disables
// the whole march (the cheaper tiers, where it costs nothing); the Highest tier
// sets it to ~1. Pushed from Go by Client.applyWaterQuality.
uniform float reflection_strength : hint_range(0.0, 1.0) = 0.0;

// Pushed by the editor for backwards compatibility; unused now (the surface is
// carried per-vertex in VERTEX.y).
uniform float water_level = 0.0;

// Transient visual offset for a CHANGING water level: the editor rebuilds the
// mesh at the new level and eases this (= previous_level - new_level) to 0 over a
// few frames so the still water glides to its new height instead of teleporting
// through the slider's discrete steps. 0 when the level is settled.
uniform float water_rise = 0.0;

// --- Terrain-sculpt preview (mirrors terrain.gdshader / buried.gdshader) -----
// While the raise/lower brush previews a height change, the water tracks the
// previewed terrain so it doesn't detach from the ground before the sculpt
// commits. The bed (v_floor) always follows the displacement; the water surface
// follows only on rivers (which sit on the ground) — lakes keep their flat
// level, matching what reloadWater rebuilds. `height` is 0 when not previewing,
// so off-preview (and in the scenery editor) this block is inert.
uniform vec3 uplift;
uniform float height = 0.0;
uniform float radius = 1.0;
// >0 when the river brush is active: its value is the channel depth. The bed is
// carved by the negative `height` above; this additionally lifts the otherwise
// hidden still-water plane to ~ground level inside the brush disc, so the river's
// water shows before the stroke commits (no flow direction is known on hover).
uniform float river_preview = 0.0;

// brush_mode uniform declared for parity with terrain/buried so the editor can
// always set it without shader warnings (preview ride logic below stays on
// the generic `height` for all height-changing brushes).
uniform float brush_mode = 0.0;

varying vec3 world_pos;   // world position (XZ used for texture lookups)
varying float v_surface;  // per-vertex base water surface (world Y)
varying float v_floor;    // per-vertex terrain floor (world Y)
varying vec2 v_flow;      // per-vertex normalised world-XZ flow
varying float v_slope;    // per-vertex downhill surface slope along flow (rise/run)
varying float v_side;     // 1 on the vertical cross-section walls, 0 on the flat surface

// Single Gerstner wave: displaces `pos` and accumulates the analytic tangent /
// binormal so a surface normal can be derived. The phase is wrapped to [0,2π)
// (sin/cos are 2π-periodic, so this is exact) — an unbounded argument makes the
// GPU's sin/cos quantise and the waves decay into stepped bands over time.
vec3 gerstner(vec4 w, vec3 pos, inout vec3 tangent, inout vec3 binormal, float t, float scale) {
	float steep = w.z;
	float len = max(w.w, 0.0001);
	float k = 6.2831853 / len;
	float c = sqrt(9.8 / k);
	vec2 d = normalize(w.xy);
	float f = mod(k * dot(d, pos.xz), 6.2831853) - mod(k * c * t, 6.2831853);
	float a = steep / k;
	float sinf = sin(f);
	float cosf = cos(f);
	tangent  += scale * vec3(-d.x * d.x * (steep * sinf), d.x * (steep * cosf), -d.x * d.y * (steep * sinf));
	binormal += scale * vec3(-d.x * d.y * (steep * sinf), d.y * (steep * cosf), -d.y * d.y * (steep * sinf));
	return scale * vec3(d.x * (a * cosf), a * sinf, d.y * (a * cosf));
}

void vertex() {
	world_pos = (MODEL_MATRIX * vec4(VERTEX, 1.0)).xyz; // XZ kept undisplaced — keeps texture UVs stable
	v_floor = CUSTOM0.r;
	v_flow = CUSTOM0.gb;
	v_slope = CUSTOM0.a;
	// CUSTOM0.r is the (pre-stroke) terrain floor here. On dry land / the leading
	// edge of a fresh river it equals the original ground, which is the reference
	// the river surface sits a lip below; the river_preview fill below clamps UP
	// from the existing surface, so re-painting an already-carved channel (where
	// the floor sits below ground) never lowers the standing water.
	float ground_ref = CUSTOM0.r;

	// Terrain-sculpt preview: lift/sink the water with the previewed terrain
	// using the SAME world-space falloff as terrain.gdshader, so the water and
	// the previewed ground stay welded. The bed always moves; the surface moves
	// only on flowing (river) vertices, so lakes keep their flat level and the
	// raised bed simply shoals the water (then the opaque terrain occludes it,
	// exactly as after the commit). Only world_pos.y is touched, so the XZ-keyed
	// texture UVs stay stable.
	if (brush_mode > 0.5 && height != 0.0 && radius > 0.0) {
		// Flatten preview (plateau/smooth): pull the terrain floor toward the target
		// height (uplift.y) so the lake shoals over a raised mesa / covers a lowered
		// one, matching the previewed ground. The lake SURFACE itself doesn't move (a
		// flatten doesn't change the water level) — only v_floor follows. `height` is
		// the strength fraction; PLATEAU_FLAT_CORE matches plateauFlatCore in
		// editor_terrain.go.
		float bdx = world_pos.x - uplift.x;
		float bdz = world_pos.z - uplift.z;
		float bd2 = bdx * bdx + bdz * bdz;
		if (bd2 < radius * radius) {
			float ff;
			if (brush_mode > 1.5) {
				ff = 1.0 - bd2 / (radius * radius);   // smooth cone
			} else {
				float tt = sqrt(bd2) / radius;        // plateau flat top
				ff = 1.0 - smoothstep(0.6, 1.0, tt);
			}
			float fac = clamp(height * ff, 0.0, 1.0);
			v_floor = max(mix(v_floor, uplift.y, fac), -2.0);
		}
	} else if (height != 0.0 && radius > 0.0) {
		float bdx = world_pos.x - uplift.x;
		float bdz = world_pos.z - uplift.z;
		float bd2 = bdx * bdx + bdz * bdz;
		if (bd2 < radius * radius) {
			float disp = height * (1.0 - bd2 / (radius * radius));
			v_floor = max(v_floor + disp, -2.0);
			// The raise/lower brush moves the ground, so a river riding it follows.
			// The river CARVE brush also drives `height` (to deepen the bed/column
			// preview) but does NOT move groundHeights, so its surface must stay put
			// — the river_preview block below sets it. Skip the ride when carving
			// (river_preview > 0), else re-painting a channel would sink its water.
			if (length(v_flow) > 0.001 && river_preview <= 0.0) {
				world_pos.y += disp; // river surface rides the ground
			}
		}
	}

	// River-tool preview: lift the (otherwise hidden) still-water plane so the
	// channel's water shows before the stroke commits, matching the committed
	// surface (reloadWater's waterAt) rather than a brush-shaped puddle:
	//   * FLAT fill (not a falloff cone): the surface sits a fixed lip below the
	//     local ground across the whole footprint, the way a real river laps its
	//     banks at one level — the old `mix(..., weight)` domed it up at the
	//     cursor and pinched it back to nothing at the rim.
	//   * reaches ONE cell past the brush radius — the bank collar (riverBankCollar
	//     = 1) the commit extends the water onto so it tucks under the bank instead
	//     of ending in a vertical edge.
	//   * clamped UP only (max): never lowers a pre-existing lake/river, and on dry
	//     land away from the disc the surface stays at the hidden global level.
	if (river_preview > 0.0 && radius > 0.0) {
		float rdx = world_pos.x - uplift.x;
		float rdz = world_pos.z - uplift.z;
		float rd2 = rdx * rdx + rdz * rdz;
		float reach = radius + 1.0; // +1 world unit == the one-cell bank collar
		if (rd2 < reach * reach) {
			float target = ground_ref - 0.2; // 0.2 == waterSurfaceDrop (editor_terrain_water.go)
			target = max(target, water_level); // never below the global lake level
			world_pos.y = max(world_pos.y, target);
		}
	}

	// The water body's vertical cross-section walls carry a horizontal mesh
	// normal; the flat surface carries an up normal. Flag the walls so they
	// render as a clean "cut through" slice (no foam, no ripple) rather than
	// running the churning surface treatment up a vertical face. Their TOP edge
	// still heaves with the swell (below) so they stay welded to the ocean.
	// (Computed before v_surface so the water-level glide can weight the walls.)
	vec3 mesh_normal = NORMAL;
	float side = (abs(mesh_normal.y) < 0.5) ? 1.0 : 0.0;
	v_side = side;

	// Smooth water-level changes: the editor rebuilds the mesh at the NEW level
	// and eases water_rise (= previous_level - new_level) to 0 over a few frames,
	// so the surface GLIDES to its new height instead of teleporting through the
	// slider's discrete steps. Only STILL water (the global lake/ocean) rides the
	// level, so gate on zero flow — rivers keep their own ground-relative lip. On
	// a cut-through wall only the TOP follows (reuse the CUSTOM0.a heave weight);
	// the bed stays pinned to the terrain. Applied before v_surface so the swell
	// and shoreline calming below track the displayed height.
	if (water_rise != 0.0 && length(v_flow) < 0.001) {
		world_pos.y += water_rise * ((side > 0.5) ? clamp(v_slope, 0.0, 1.0) : 1.0);
	}

	v_surface = world_pos.y;

	// Swell only on open, still, deep water: off on rivers (flow), faded out in
	// shallow water near the shore so crests never rise through the terrain.
	float fl = length(v_flow);
	float column = max(v_surface - v_floor, 0.0);
	float shallow = (shallow_calm > 0.0001) ? clamp(column / shallow_calm, 0.0, 1.0) : 1.0;
	// Heave weight: the flat plane heaves fully; on a cut-through wall only the
	// TOP edge follows the swell while the bottom stays pinned to the bed, so the
	// wall welds to the heaving ocean surface above AND the terrain below.
	// (Disabling swell on walls entirely left them static, disconnecting them
	// from the moving ocean.) The weight is carried PER-VERTEX in CUSTOM0.a (1 on
	// the top row, 0 on the bed row) rather than inferred from water depth: at a
	// shallow shore the top vertex's `column` is small, so the old `column*4`
	// proxy gave it heave < 1 while the COINCIDENT plane-edge vertex used heave =
	// 1 — the two welded surfaces then disagreed on the same vertex and the seam
	// tore (the plane lifted, the wall top lagged: a vertex stuck out of sync at
	// the waterline). A top weight of exactly 1 makes the wall top reproduce the
	// plane's swell bit-for-bit (v_floor/v_flow/v_surface/world-XZ already match),
	// so the shared vertex stays welded at any depth.
	float heave = (side > 0.5) ? clamp(v_slope, 0.0, 1.0) : 1.0;
	float swell = wave_height * (1.0 - clamp(fl * 2.0, 0.0, 1.0)) * shallow * heave;

	vec3 tangent = vec3(1.0, 0.0, 0.0);
	vec3 binormal = vec3(0.0, 0.0, 1.0);
	vec3 p = world_pos;
	if (swell > 0.001) {
		float t = TIME * wave_speed;
		vec3 off = vec3(0.0);
		off += gerstner(wave_a, world_pos, tangent, binormal, t, swell);
		off += gerstner(wave_b, world_pos, tangent, binormal, t, swell);
		off += gerstner(wave_c, world_pos, tangent, binormal, t, swell);
		p += off;
	}
	VERTEX = (inverse(MODEL_MATRIX) * vec4(p, 1.0)).xyz;

	if (side > 0.5) {
		// Keep the wall's own outward-facing normal so the slice is lit as the
		// vertical surface it is; its ripple normal map is flattened in fragment.
		NORMAL = normalize(mesh_normal);
	} else {
		// Tangent frame from the swell (identity when flat) so NORMAL_MAP ripples
		// layer correctly on top of the geometric wave slopes.
		NORMAL = normalize(cross(binormal, tangent));
		TANGENT = normalize(tangent);
		BINORMAL = normalize(binormal);
	}
}

// Two-phase flow-map sample with CONTRAST PRESERVATION. `adv` is the per-cycle
// advection vector (same UV space as `uv`); p0/p1 are half-cycle-offset phases
// kept in [0,1) by fract(), so the sampling offset is ALWAYS bounded — no
// long-run precision decay — and the triangle cross-blend hides each phase's
// per-cycle reset.
//
// The plain cross-blend averages two decorrelated samples at the half-cycle
// midpoint, which halves the variance: the texture looks blurry there and crisp
// at the phase ends — a visible crisp<->blurry pulse. Dividing the deviation
// from `neutral` by sqrt((1-w)^2 + w^2) (= 1 at the ends, ~0.707 at the
// midpoint) restores the variance, so the apparent sharpness stays constant
// across the cycle and the pulse is gone.
vec3 flow_sample(sampler2D tex, vec2 uv, vec2 adv, float phase, vec3 neutral) {
	float p0 = fract(phase);
	float p1 = fract(phase + 0.5);
	float w = abs(1.0 - 2.0 * p0);
	vec3 s0 = texture(tex, uv - adv * p0).rgb;
	vec3 s1 = texture(tex, uv - adv * p1).rgb;
	vec3 b = mix(s0, s1, w);
	float norm = sqrt((1.0 - w) * (1.0 - w) + w * w);
	return neutral + (b - neutral) / norm;
}

void fragment() {
	vec2 buv = world_pos.xz;

	// Low-frequency UV distortion to break up the normal-map tiling.
	vec2 distort = (texture(uv_sampler, buv * distort_scale).rg * 2.0 - 1.0) * distort_strength;

	// Advection direction: downstream for rivers, a gentle fixed drift for
	// still water. `adv` is in WORLD units per cycle; it is scaled into each
	// map's UV space at the call site so both maps move the same distance.
	float fl = length(v_flow);
	float slope = clamp(v_slope, 0.0, 1.0);   // downhill steepness of the surface
	float phase = TIME / cycle_seconds;
	// Steeper water runs faster: scale the RIPPLE advection by the surface slope.
	float river_speed = flow_speed * (1.0 + flow_slope_gain * slope);
	vec2 adv = (fl > 0.001)
		? (v_flow / fl) * (river_speed * cycle_seconds)
		: vec2(0.7, 0.4) * (still_speed * cycle_seconds);
	// Foam advects at the BASE speed regardless of slope (slope only grows its
	// DENSITY, below) — so steep water foams up more without the foam racing.
	vec2 adv_foam = (fl > 0.001)
		? (v_flow / fl) * (flow_speed * cycle_seconds)
		: vec2(0.7, 0.4) * (still_speed * cycle_seconds);

	vec3 na = flow_sample(normalmap_a_sampler, buv * normal_scale + distort, adv * normal_scale, phase, vec3(0.5, 0.5, 1.0));
	vec3 nb = flow_sample(normalmap_b_sampler, buv * normal_detail_scale + distort, adv * normal_detail_scale, phase + 0.37, vec3(0.5, 0.5, 1.0));
	vec3 nmap = mix(na, nb, 0.4);                 // tangent-space normal, [0,1]
	vec2 perturb = nmap.xy * 2.0 - 1.0;           // decoded XY for refraction

	// --- Scene depth behind the water (linear view space) -------------------
	float raw = texture(depth_texture, SCREEN_UV).r;
	vec3 ndc = vec3(SCREEN_UV * 2.0 - 1.0, raw);
	vec4 vp = INV_PROJECTION_MATRIX * vec4(ndc, 1.0);
	float scene_z = -(vp.z / max(vp.w, 1e-4));
	float surf_z = -VERTEX.z;
	float depth = max(scene_z - surf_z, 0.0);

	// The cut-through side walls (v_side) sit just in front of the opaque terrain
	// skirt, so the screen-space depth-to-background is ~0 over the WHOLE slice — it
	// read as uniformly shallow/clear, a glassy "mirror". Colour them by the WATER
	// COLUMN depth instead (surface minus bed: UV.y carries the column's surface
	// world-Y, v_floor its bed — both constant down the wall), NOT a vertical
	// gradient: a deep column is then uniformly dark and a shallow one light, and
	// the wall MATCHES the flat plane's colour at the shared waterline. (A
	// top-to-bed gradient left the wall's top edge pale where it met the dark
	// deep-water plane — the colour mismatch at the top.) The flat plane ignores UV
	// and keeps its screen-space depth.
	if (v_side > 0.5) {
		depth = max(UV.y - v_floor, 0.0);
	}

	// --- Shallow-water calming from the heightfield (stable, per-vertex) ----
	float column = max(v_surface - v_floor, 0.0);
	float calm = (shallow_calm > 0.0001) ? clamp(column / shallow_calm, 0.0, 1.0) : 1.0;

	// --- Depth-tinted colour + screen-space refraction ----------------------
	float fade = clamp(1.0 - exp(-depth_fade * depth), 0.0, 1.0);
	vec3 dye = mix(color_shallow.rgb, color_deep.rgb, fade);

	// No refraction offset on the cut-through walls (1 - v_side): `perturb` comes from
	// the XZ-keyed ripple, which is constant down a vertical face, so it smears the
	// scene BEHIND the wall into vertical streaks — the noisy overlay seen on a fish
	// viewed THROUGH a side wall. Sample straight there (the nmap is likewise
	// flattened on walls below, so this just finishes the "clean water slice").
	vec2 ruv = SCREEN_UV + perturb * refraction * calm * (1.0 - v_side);
	vec3 behind = texture(screen_texture, ruv).rgb;
	vec3 col = mix(behind, dye, clamp(fade + 0.25, 0.0, 1.0));

	// --- Foam ---------------------------------------------------------------
	// Two soft contributions, both drifting with the surface via the contrast-
	// preserving flow-map so neither pulses crisp<->blurry:
	//   * shoreline band — soft white where the water is shallow / meets a bank,
	//     eased in with smoothstep so there is no hard edge.
	//   * river streaks  — wispy whitewater across the flowing surface, gated on
	//     flow AND scaled by the surface SLOPE: gentle stretches stay calm, only
	//     steeper/faster water turns white (this is what keeps it from reading as
	//     a hard, opaque sheet on flat rivers).
	float f0 = flow_sample(foam_sampler, buv * foam_scale + distort * 2.0, adv_foam * foam_scale, phase, vec3(0.5)).r;

	// Shoreline foam keys off the TERRAIN water column (surface - bed), NOT the
	// screen-space `depth` (distance to whatever's behind the water). The latter
	// shrinks wherever ANY geometry sits close under the surface, so a fish or placed
	// object swimming near the top sprouted a bright white foam halo — the shader
	// couldn't tell the seabed/bank from an undersea object. `column` is the real
	// heightfield water depth, so foam appears only where the water is genuinely
	// shallow over the ground.
	float shore = 1.0 - smoothstep(0.0, foam_edge, column);
	float shore_foam = shore * mix(0.65, 1.0, f0) * foam_amount;

	// Second octave at a different scale/phase so the streaks don't visibly tile;
	// the product is the raw foam field. SLOPE drives the foam DENSITY: on a flat
	// river a high threshold keeps only the brightest peaks (sparse, soft wisps),
	// and as the surface steepens the threshold drops so more and more of the
	// field turns white, tending toward a solid waterfall sheet. The amount also
	// ramps from the soft river_foam_amount up to full white over the same range.
	float f1 = flow_sample(foam_sampler, buv * foam_scale * 2.7 - distort * 1.5, adv_foam * foam_scale * 2.7, phase + 0.5, vec3(0.5)).r;
	float field = f0 * f1 * 2.0;
	float density = clamp(slope * slope_foam_gain, 0.0, 1.0); // 0 flat -> 1 waterfall
	float lo = mix(0.9, -0.1, density);     // sparse peaks -> nearly everything
	float hi = mix(1.5, 0.45, density);
	float wisp = smoothstep(lo, hi, field);
	float flow_mask = smoothstep(0.03, 0.15, fl);
	float flow_foam = flow_mask * wisp * mix(river_foam_amount, 1.0, density);

	// The cut-through walls take no foam: the foam UVs key off world XZ, so on a
	// vertical face the shoreline band + river streaks smear into flickering
	// vertical lines. Zero foam there so the wall reads as a clean water slice.
	float foam = (shore_foam + flow_foam) * calm * (1.0 - v_side);
	col = mix(col, vec3(1.0), clamp(foam, 0.0, 1.0));

	// --- Screen-space reflections (Highest quality tier only) ---------------
	// Mirror the on-screen scene in the surface by ray-marching the reflected
	// view ray against the depth buffer and folding the hit colour in, Fresnel-
	// weighted so it reads hard at grazing angles and barely head-on (real
	// water). Gated on reflection_strength > 0 (set only at the Highest tier) so
	// the cheaper tiers skip the loop entirely, and off on the cut-through walls
	// (their ripple is flattened and they have no stable tangent frame).
	if (reflection_strength > 0.0 && v_side < 0.5) {
		// Perturbed surface normal in VIEW space — the ripple riding the macro
		// up-normal, from the same tangent frame the vertex stage set. `perturb`
		// is the full-strength decoded ripple; scale it by the shaded strength
		// (and shallow-water calm) so the reflection wobbles like the lighting.
		vec3 ts_n = normalize(vec3(perturb * normal_strength * calm, 1.0));
		vec3 n_view = normalize(TANGENT * ts_n.x + BINORMAL * ts_n.y + NORMAL * ts_n.z);
		vec3 incident = normalize(VERTEX);          // camera -> fragment (view space)
		vec3 refl = reflect(incident, n_view);      // reflected ray direction

		// March in view space, projecting each step to screen and testing scene
		// depth. A coarse step that lengthens with distance gives cheap reach;
		// the thickness gate stops the ray matching the far background (sky).
		float step_len = 0.4;
		vec3 ray = VERTEX;
		float hit = 0.0;
		vec2 hit_uv = vec2(0.0);
		for (int i = 0; i < 24; i++) {
			ray += refl * step_len;
			step_len *= 1.08;
			if (ray.z > -0.01) break;               // crossed behind the camera plane
			vec4 clip = PROJECTION_MATRIX * vec4(ray, 1.0);
			vec2 suv = clip.xy / clip.w * 0.5 + 0.5;
			if (suv.x < 0.0 || suv.x > 1.0 || suv.y < 0.0 || suv.y > 1.0) break;
			float sraw = texture(depth_texture, suv).r;
			vec4 svp = INV_PROJECTION_MATRIX * vec4(suv * 2.0 - 1.0, sraw, 1.0);
			float scene_z = svp.z / max(svp.w, 1e-4); // scene view-space z (negative ahead)
			float gap = scene_z - ray.z;            // >0 once the ray passes behind a surface
			if (gap > 0.0 && gap < step_len * 2.0) {
				hit = 1.0;
				hit_uv = suv;
				break;
			}
		}

		if (hit > 0.5) {
			float ct = clamp(dot(n_view, -incident), 0.0, 1.0);
			float fres = 0.02 + 0.98 * pow(1.0 - ct, 5.0); // Schlick, F0 ~0.02 (water)
			// Fade as the sample nears a screen edge so off-screen geometry that
			// scrolls out of frame doesn't pop a hard reflection seam.
			vec2 ef = smoothstep(vec2(0.0), vec2(0.12), hit_uv) * (1.0 - smoothstep(vec2(0.88), vec2(1.0), hit_uv));
			vec3 refl_col = texture(screen_texture, hit_uv).rgb;
			float amt = reflection_strength * fres * ef.x * ef.y * calm;
			col = mix(col, refl_col, clamp(amt, 0.0, 1.0));
		}
	}

	// Flatten the ripple normal in very shallow water for a still, glassy look,
	// and fully on the cut-through walls (the XZ-keyed ripple smears vertically).
	nmap = mix(vec3(0.5, 0.5, 1.0), nmap, calm);
	nmap = mix(nmap, vec3(0.5, 0.5, 1.0), v_side);

	ALBEDO = col;
	NORMAL_MAP = nmap;
	NORMAL_MAP_DEPTH = normal_strength * calm;
	METALLIC = 0.0;
	ROUGHNESS = 0.12;
	SPECULAR = 0.6;
	// Deeper water is more opaque; rivers get an extra opacity floor.
	float river_a = (fl > 0.001) ? flow_opacity : 0.0;
	ALPHA = clamp(min_alpha + fade * (1.0 - min_alpha) + river_a, 0.0, 1.0);
}
