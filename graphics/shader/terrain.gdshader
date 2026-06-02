// NOTE: Shader automatically converted from Godot Engine 4.3.stable's StandardMaterial3D.
// Updated for proper normal map blending and ORM sampling.

shader_type spatial;
render_mode blend_mix, depth_draw_opaque, cull_back, diffuse_burley, specular_schlick_ggx, world_vertex_coords;

uniform sampler2DArray texture_albedo : source_color, filter_linear_mipmap, repeat_enable;
uniform sampler2DArray texture_normal : hint_normal, filter_linear_mipmap, repeat_enable;
// Grayscale gloss maps from the terrain pack ("<name>_spec.png"), one layer per
// painted design, normalised to R8 on the CPU (see editor_terrain.go). Sampled
// as a roughness source in fragment(); no mipmaps, so plain linear filtering.
uniform sampler2DArray texture_spec   : filter_linear, repeat_enable;

uniform float point_size : hint_range(0.1, 128.0, 0.1);

uniform vec3 uv1_scale;
uniform vec3 uv1_offset;
uniform vec3 uv2_scale;
uniform vec3 uv2_offset;

// will be on the mouse click, to uplift the terrain
uniform vec3 uplift;
uniform float height;
uniform float radius;
uniform bool brush_active;
uniform bool paint_active;
// >0.5 while the river-erase ("no water") brush previews refilling a channel.
// CUSTOM2.r carries the original ground height per vertex; the brush disc blends
// the bed up toward it (see vertex()), so the eraser shows the terrain rising
// back to ground before the stroke commits.
uniform float river_fill = 0.0;
// >0 while the river ("carve") brush is hovering: its value is the channel depth.
// The carve uses PAINT-OVER (replace) semantics matching the commit, not the
// additive `height` bump — so an overlapping drag segment (or re-painting an
// existing river) overwrites the channel instead of digging additively deeper.
uniform float river_carve = 0.0;

// brush_mode: 0=additive (raise/lower), 1=plateau (flat-topped flatten), 2=smooth
// (soft-cone flatten). For the flatten modes the `height` uniform carries the
// strength fraction (Amount=0 on the wire for BC); plateau uses a flat-topped
// falloff (PLATEAU_FLAT_CORE) so it carves a mesa, smooth a 1−d²/r² cone.
uniform float brush_mode = 0.0;

uniform vec3 selection;
uniform bool selection_active;

uniform sampler2D paint_texture : source_color, filter_linear_mipmap, repeat_enable;

uniform float blend_sharpness : hint_range(1.0, 10.0, 0.1) = 4.0;

varying vec3 world;
varying flat vec4 texture;
varying vec4 weights;

// --- Cloud shadows (AVIARY) --------------------------------------------------
// Dappled, drifting shadows from the cloud layer. Driven by globals shared with
// the cloud appearance (the Clouds slider + wind) and the sun direction, so they
// track the overcast level and move with the weather. Each ground point is
// projected ALONG the sun up to the cloud altitude and a scrolling fBm coverage
// is sampled there, so the shadow offsets correctly with sun angle (straight
// down at noon, long across the ground near sunset). This APPROXIMATES the
// visible clouds — it shares coverage/wind/sun, not the exact density field —
// but fine detail never reads in a soft ground shadow anyway.
global uniform vec3  cloud_shadow_sun_dir; // world-space direction TOWARD the sun
global uniform float cloud_coverage;       // 0..1, the Clouds slider
global uniform vec2  cloud_shadow_wind;    // world units/sec drift of the cloud field
global uniform float cloud_shadow_enabled; // >0.5 = draw ground cloud shadows; 0 = off (cheapest mode)
// Matched cloud shadow map: SunshineClouds2 (Highest tier) renders its actual coarse
// coverage top-down into this texture each frame (SunshineCloudsShadowMap.glsl). When
// active we sample it so shadows match the real clouds; else fall back to the noise above.
global uniform sampler2D cloud_shadow_map;
global uniform vec2  cloud_shadow_map_center; // world XZ centre of the map window
global uniform float cloud_shadow_map_extent; // half-extent of the map in world units
global uniform float cloud_shadow_map_active; // >0.5 = map live; else procedural fallback
const float CLOUD_SHADOW_ALT    = 21.0;    // cloud-layer mid altitude (SunshineClouds2 floor 14 / ceiling 28)
const float CLOUD_SHADOW_SCALE  = 0.03;    // fallback-noise frequency (lower = bigger patches)
const float CLOUD_SHADOW_DARKEN = 0.3;     // deepest shadow (0 = black, 1 = no darkening)
const float CLOUD_SHADOW_DEBUG  = 0.0;     // DIAGNOSTIC: 1.0 = moving stripes (plumbing test,
                                           // ignores globals); 2.0 = show raw coverage (dark = cloud).

float cs_hash(vec2 p) {
    p = fract(p * vec2(123.34, 345.45));
    p += dot(p, p + 34.345);
    return fract(p.x * p.y);
}
float cs_noise(vec2 p) {
    vec2 i = floor(p);
    vec2 f = fract(p);
    f = f * f * (3.0 - 2.0 * f);
    float a = cs_hash(i);
    float b = cs_hash(i + vec2(1.0, 0.0));
    float c = cs_hash(i + vec2(0.0, 1.0));
    float d = cs_hash(i + vec2(1.0, 1.0));
    return mix(mix(a, b, f.x), mix(c, d, f.x), f.y);
}
// Returns a multiplier in [CLOUD_SHADOW_DARKEN, 1]: 1 in full sun, darker under cloud.
float cloud_shadow_factor(vec3 world_pos) {
    // DIAGNOSTIC: with DEBUG on, ignore ALL globals and draw obvious moving stripes
    // from world position + TIME. If these stripes appear and move, the shader path
    // and the ALBEDO multiply work — so any "no shadows" problem is the globals not
    // feeding (rebuild the Go side; check the log for "global uniform ... does not
    // exist"). If you see nothing even here, the shader edit isn't live.
    if (CLOUD_SHADOW_DEBUG > 0.5 && CLOUD_SHADOW_DEBUG < 1.5) {
        return mix(0.25, 1.0, step(0.5, fract((world_pos.x + world_pos.z + TIME * 6.0) * 0.05)));
    }
    // Cloud shadows can be disabled entirely (the cheapest cloud mode / Toaster
    // tier skips the ground cloud-shadow term). Driven by the cloud system's
    // SetMode via the cloud_shadow_enabled global.
    if (cloud_shadow_enabled < 0.5) {
        return 1.0;
    }
    // No sun below the horizon -> no cloud shadow (night is lit by ambient only).
    float day = smoothstep(0.02, 0.12, cloud_shadow_sun_dir.y);
    if (day <= 0.0) {
        return 1.0;
    }
    // Project the ground point up along the sun to the cloud layer.
    float t = (CLOUD_SHADOW_ALT - world_pos.y) / max(cloud_shadow_sun_dir.y, 0.05);
    vec2 sxz = world_pos.xz + cloud_shadow_sun_dir.xz * t;

    float cloud;
    if (cloud_shadow_map_active > 0.5) {
        // MATCHED: sample the SunshineClouds2-derived coverage map (drift is already baked
        // into the map by the addon's wind-driven noise positions, so no TIME term here).
        vec2 uv = (sxz - cloud_shadow_map_center) / (2.0 * cloud_shadow_map_extent) + 0.5;
        uv = clamp(uv, vec2(0.001), vec2(0.999));
        cloud = texture(cloud_shadow_map, uv).r;
    } else {
        // FALLBACK: independent drifting noise — doesn't match the clouds, but reads fine.
        if (cloud_coverage <= 0.001) { return 1.0; }
        vec2 p = (sxz + cloud_shadow_wind * TIME) * CLOUD_SHADOW_SCALE;
        float n = cs_noise(p) * 0.6 + cs_noise(p * 2.3) * 0.3 + cs_noise(p * 5.1) * 0.1;
        float thresh = mix(0.9, 0.45, cloud_coverage);
        cloud = smoothstep(thresh, thresh + 0.18, n);
    }
    if (CLOUD_SHADOW_DEBUG > 1.5) { return clamp(1.0 - cloud, 0.0, 1.0); } // viz: dark = cloud
    return mix(1.0, mix(1.0, CLOUD_SHADOW_DARKEN, cloud), day);
}

void vertex() {
    UV = UV * uv1_scale.xy + uv1_offset.xy;

    float dx = (VERTEX.x - uplift.x);
    float dy = (VERTEX.z - uplift.z);
    float br2 = dx*dx + dy*dy;
    if (br2 < radius*radius) {
        float w = 1.0 - br2/(radius*radius);
        if (river_carve > 0.0) {
            // River carve preview: PAINT-OVER (replace) the channel depth, matching
            // the commit (editor_terrain.go: riverDepth += (target − riverDepth)*w).
            // riverDepth here is original ground (CUSTOM2.r) − current bed (VERTEX.y),
            // so re-carving an existing channel (or an overlapping drag segment)
            // overwrites it to `river_carve` deep instead of stacking another carve.
            float old_depth = CUSTOM2.r - VERTEX.y;
            float new_depth = old_depth + (river_carve - old_depth) * w;
            VERTEX.y = CUSTOM2.r - new_depth;
        } else if (brush_mode > 1.5) {
            // Smooth preview: pull toward the cursor height with a soft 1−d²/r² cone
            // (the real stroke neighbour-averages; this is the visual cue). `height`
            // carries the editor-scaled strength fraction.
            float fac = clamp(height * w, 0.0, 1.0);
            VERTEX.y = mix(VERTEX.y, uplift.y, fac);
        } else if (brush_mode > 0.5) {
            // Plateau preview: flat-topped falloff — dead flat across the core, eased
            // to 0 over the skirt — so the hover shows the mesa the commit carves
            // instead of a soft dome. PLATEAU_FLAT_CORE matches plateauFlatCore in
            // editor_terrain.go; `height` carries the strength fraction.
            float t = sqrt(br2) / radius;
            float ff = 1.0 - smoothstep(0.6, 1.0, t);
            float fac = clamp(height * ff, 0.0, 1.0);
            VERTEX.y = mix(VERTEX.y, uplift.y, fac);
        } else {
            // Raise/lower: additive, accumulating into the ground.
            VERTEX.y += height * w;
        }
    }
    // River-erase ("no water") preview: fill the carved channel back toward the
    // original ground (CUSTOM2.r) inside the brush disc, matching what one erase
    // stroke commits (riverDepth -> 0, so the bed rises to the original ground).
    // The same disc falloff as the carve blends the bed up toward ground, so the
    // river shoals and the separately-rendered water is occluded where the channel
    // fills. Inert unless the eraser is previewing (river_fill > 0.5).
    if (river_fill > 0.5 && dx*dx+dy*dy < radius*radius) {
        float fw = 1.0 - (dx*dx+dy*dy)/(radius*radius);
        VERTEX.y = mix(VERTEX.y, CUSTOM2.r, fw);
    }
    VERTEX.y = max(VERTEX.y, -2.0);
    world = VERTEX;
    texture = CUSTOM0;
    weights = CUSTOM1;
}

bool corner_in_brush(float cx, float cz) {
    float dx = cx - uplift.x;
    float dz = cz - uplift.z;
    return dx*dx + dz*dz <= radius*radius;
}

void fragment() {
    vec2 base_uv = UV;

    // MSAA evaluates varyings at each sample position; for a pixel on a
    // triangle edge some samples land just outside the triangle and
    // extrapolate `weights` below 0. pow() of a negative base is undefined
    // in GLSL and returns NaN, which shows up as the black dotted speckle
    // along every terrain edge once MSAA is enabled. Clamp to [0,1] (a
    // no-op for the in-triangle samples) so every sample stays defined, and
    // guard the normalisation against an all-zero sum.
    vec4 w = clamp(weights, vec4(0.0), vec4(1.0));
    vec4 blended = pow(w, vec4(blend_sharpness));
    blended /= max(blended.x + blended.y + blended.z + blended.w, 1e-5);

    // texture has top-left, top-right, bottom-left, bottom-right
    // samples of the terrain around the vertex, we will interpolate
    // between these samples to get the final albedo
    vec4 colorTL = texture(texture_albedo, vec3(base_uv, texture.r));
    vec4 colorTR = texture(texture_albedo, vec3(base_uv, texture.g));
    vec4 colorBL = texture(texture_albedo, vec3(base_uv, texture.b));
    vec4 colorBR = texture(texture_albedo, vec3(base_uv, texture.a));

    vec4 albedo_tex = colorTL * blended.x + colorTR * blended.y + colorBL * blended.z + colorBR * blended.w;

    // Sample the four layers, unpack to [-1,1], blend by the visible weights,
    // and renormalize so the blend stays a unit normal.
    vec3 normalTL = texture(texture_normal, vec3(base_uv, texture.r)).rgb * 2.0 - 1.0;
    vec3 normalTR = texture(texture_normal, vec3(base_uv, texture.g)).rgb * 2.0 - 1.0;
    vec3 normalBL = texture(texture_normal, vec3(base_uv, texture.b)).rgb * 2.0 - 1.0;
    vec3 normalBR = texture(texture_normal, vec3(base_uv, texture.a)).rgb * 2.0 - 1.0;

    vec3 normal_tex = normalize(normalTL * blended.x + normalTR * blended.y + normalBL * blended.z + normalBR * blended.w);
    // NORMAL_MAP expects the RAW [0,1] texture range — Godot unpacks it itself
    // and applies it in tangent space. Re-pack our blended [-1,1] normal so it
    // isn't double-unpacked (which previously tilted even a flat normal map).
    NORMAL_MAP = normal_tex * 0.5 + 0.5;

    // Sample the four corner gloss layers and blend them with the SAME
    // `blended` weights as albedo/normal (the old code blended ORM with the
    // raw, un-sharpened `weights`, which never matched the visible texture
    // split). Brighter gloss = shinier = less rough. Terrain is dielectric,
    // so metallic stays 0, and the pack carries no AO so AO = 1.
    float specTL = texture(texture_spec, vec3(base_uv, texture.r)).r;
    float specTR = texture(texture_spec, vec3(base_uv, texture.g)).r;
    float specBL = texture(texture_spec, vec3(base_uv, texture.b)).r;
    float specBR = texture(texture_spec, vec3(base_uv, texture.a)).r;

    float gloss = specTL * blended.x + specTR * blended.y + specBL * blended.z + specBR * blended.w;

    ROUGHNESS = 1.0 - gloss;
    METALLIC = 0.0;
    AO = 1.0;

    float highlight = 1.0;
    float dx = (world.x - uplift.x);
    float dy = (world.z - uplift.z);
    if (brush_active && dx*dx+dy*dy < radius*radius) {
        highlight = 2.0;
    }

    vec3 albedo_final = albedo_tex.rgb * highlight;

    if (paint_active) {
        float fx = floor(world.x);
        float fz = floor(world.z);
        bool h00 = corner_in_brush(fx, fz);
        bool h10 = corner_in_brush(fx + 1.0, fz);
        bool h01 = corner_in_brush(fx, fz + 1.0);
        bool h11 = corner_in_brush(fx + 1.0, fz + 1.0);

        // Compute how much of this fragment would be contributed by newly-painted
        // corners after a real commit (using the exact same weights + blend_sharpness
        // the real path uses when it overwrites only the hit corners in CUSTOM0).
        vec4 hits = vec4(h00 ? 1.0 : 0.0, h10 ? 1.0 : 0.0,
                         h01 ? 1.0 : 0.0, h11 ? 1.0 : 0.0);
        float paint_influence = dot(blended, hits);

        if (paint_influence > 0.0) {
            vec3 paint_color = texture(paint_texture, base_uv).rgb;
            albedo_final = mix(albedo_tex.rgb, paint_color, paint_influence) * highlight;

            // Only switch to the flat painted material properties once the new
            // texture would dominate (keeps correct normals/ORM on the fade edges).
            if (paint_influence > 0.5) {
                NORMAL_MAP = vec3(0.0, 0.0, 1.0);
                ROUGHNESS = 1.0;
                METALLIC = 0.0;
                AO = 1.0;
            }
        }
    }

    ALBEDO = albedo_final;

    // Cloud shadows: dim the ground under the drifting cloud layer (AVIARY).
    ALBEDO *= cloud_shadow_factor(world);

    if (selection_active) {
        float sdx = (world.x - selection.x);
        float sdy = (world.z - selection.y);
        if (sdx*sdx+sdy*sdy < selection.z*selection.z) {
           ALBEDO = vec3(1.0, 1.0, 1.0);
        }
    }
}
