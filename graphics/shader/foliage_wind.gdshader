shader_type spatial;

render_mode blend_mix, depth_draw_opaque, cull_back, diffuse_burley, specular_schlick_ggx;

// Leaflet albedo (swapped live from the foliage editor when the user paints materials)
uniform sampler2D albedo_texture : source_color, filter_linear_mipmap, repeat_enable;

// Subtle wind controls – tuned for a nice "editor preview" feel on small trees.
// The leaf sway uses both global tree height and a per-card UV factor so the
// base of each leaf stays fixed while the outer part flutters (prevents floaty look).
// These can be driven from GDScript/Go later if we want user-tunable wind.
uniform float wind_strength : hint_range(0.0, 0.5, 0.005) = 0.07;
uniform float wind_speed    : hint_range(0.2, 4.0, 0.1)   = 1.35;
uniform vec2  wind_dir      = vec2(0.85, 0.35);  // direction is normalized in the shader; hint_range is not supported on vec2

// Controls how strongly the wind is suppressed near the base of each leaf card.
// Higher values make the attachment point stay almost completely still.
uniform float leaf_sway_power : hint_range(0.8, 4.0, 0.05) = 2.0;

varying vec3 world_pos;

void vertex() {
    // Work in world space so wind feels consistent when the preview tree moves/rotates.
    vec4 world = MODEL_MATRIX * vec4(VERTEX, 1.0);
    world_pos = world.xyz;

    // Two-phase low-frequency sway (main gust + secondary flutter).
    // Phase is based on world position so neighbouring leaves don't move in perfect lockstep.
    float t = TIME * wind_speed;
    float sway = sin(t + world_pos.x * 0.65 + world_pos.z * 0.45) * 0.6;
    sway += sin(t * 1.75 + world_pos.x * 1.25) * 0.35;

    // Global height influence (higher branches in the tree sway more overall).
    float tree_height_mask = clamp((world_pos.y + 1.0) * 0.65, 0.0, 1.8);

    // Per-leaf local factor so the *base* of each individual leaf card stays fixed.
    // In this generator's leaf cards, UV.y runs from the outer tip (low values)
    // to the attachment point on the twig (high values).
    //
    // If the wind motion looks inverted on your leaves (base stays still, tip moves),
    // change the next line to:  float leaf_local = UV.y;
    float leaf_local = 1.0 - UV.y;

    // Power curve gives a strong hinge effect: near-zero movement at the base,
    // full movement at the tip. This stops the leaves from looking floaty/rigid.
    float leaf_sway = pow(leaf_local, leaf_sway_power);

    float wind_mask = tree_height_mask * leaf_sway;

    // Primary displacement is sideways (wind_dir), with a tiny vertical "lift" component.
    vec3 disp = vec3(wind_dir.x, 0.12, wind_dir.y) * sway * wind_strength * wind_mask;

    VERTEX += disp;

    // Cheap normal perturbation so lighting follows the movement a little.
    NORMAL = normalize(NORMAL + disp * 2.5);
}

void fragment() {
    vec4 tex = texture(albedo_texture, UV);
    ALBEDO = tex.rgb;
    ALPHA = tex.a;
    // Hard alpha cutoff to match the previous StandardMaterial3D + AlphaScissor behaviour.
    // (The old "alpha_scissor" render_mode does not exist in Godot 4; use ALPHA_SCISSOR_THRESHOLD instead.)
    ALPHA_SCISSOR_THRESHOLD = 0.5;
}