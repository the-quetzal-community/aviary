shader_type spatial;
render_mode unshaded, blend_mix, depth_draw_never, cull_disabled;

uniform vec4 grid_color : source_color = vec4(0.2, 0.2, 0.2, 1.0);
uniform vec4 checker_color : source_color = vec4(0.1, 0.1, 0.1, 0.3);
uniform float fade_start : hint_range(0.0, 50.0) = 0.0;
uniform float fade_end : hint_range(1.0, 100.0) = 50.0;
uniform float unit_size : hint_range(0.1, 10.0) = 1.0;
uniform int subdivisions : hint_range(1, 10) = 5;
uniform float major_line_thickness : hint_range(0.5, 5.0) = 2.0;
uniform float minor_line_thickness : hint_range(0.1, 3.0) = 1.0;
uniform float minor_line_alpha : hint_range(0.0, 1.0) = 0.3;
uniform vec3 center_offset = vec3(0.0, 0.0, 0.0);
uniform float horizontal_boost : hint_range(1.0, 3.0) = 1.5;  // Boost threshold for horizontal lines to counter low fwidth near horizon

uniform sampler2D depth_texture : hint_depth_texture;

void vertex() {
    POSITION = vec4(VERTEX.xy, 1.0, 1.0);
}

// Separate masks for vertical (X-lines) and horizontal (Z-lines)
float vertical_line(vec2 pos, float unit, float thickness) {
    vec2 threshold = fwidth(pos) * (thickness * 0.5) / unit;
    float coord_x = pos.x / unit;
    float dist = abs(fract(coord_x) - 0.5);
    return 1.0 - clamp(dist / threshold.x, 0.0, 1.0);
}

float horizontal_line(vec2 pos, float unit, float thickness) {
    vec2 threshold = fwidth(pos) * (thickness * 0.5) / unit;
    threshold.y *= horizontal_boost;  // Boost Y threshold to make horizontals visible at distance/low angles
    float coord_z = pos.y / unit;
    float dist = abs(fract(coord_z) - 0.5);
    return 1.0 - clamp(dist / threshold.y, 0.0, 1.0);
}

// Combined grid using separate functions
float grid(vec2 pos, float unit, float thickness) {
    float v = vertical_line(pos, unit, thickness);
    float h = horizontal_line(pos, unit, thickness);
    return max(v, h);
}

// Checkerboard pattern (alternating squares)
float checker(vec2 pos, float unit) {
    vec2 tile = floor(pos / unit);
    return float(mod(tile.x + tile.y, 2.0) < 1.0);
}

void fragment() {
    vec2 uv = SCREEN_UV;
    vec3 cam_pos = CAMERA_POSITION_WORLD;

    // Reconstruct world-space ray direction (Godot 4.3+ / Vulkan-safe: ndc.z = 0.0 for near plane)
    vec2 screen_pos = uv * 2.0 - 1.0;
    vec3 ndc_dir = vec3(screen_pos, 0.0);
    vec3 eye_dir = normalize((INV_PROJECTION_MATRIX * vec4(ndc_dir, 1.0)).xyz);
    vec3 world_dir = normalize(mat3(INV_VIEW_MATRIX) * eye_dir);

    // Intersect ray with ground plane at y = center_offset.y
    float plane_y = center_offset.y;
    if (world_dir.y >= 0.0) {  // Ray pointing upwards, no intersection below
        discard;
    }
    float t = (plane_y - cam_pos.y) / world_dir.y;
    if (t < 0.0) {  // Intersection behind camera
        discard;
    }
    vec3 world_pos = cam_pos + t * world_dir;
    vec2 grid_pos = world_pos.xz - center_offset.xz;

    // Backend-agnostic occlusion via view-space distances (works for Godot 4.5 reversed depth)
    float raw_depth = textureLod(depth_texture, uv, 0.0).r;
    vec4 scene_ndc = vec4(screen_pos, raw_depth, 1.0);
    vec4 scene_view = INV_PROJECTION_MATRIX * scene_ndc;
    scene_view /= scene_view.w;
    float scene_dist = -scene_view.z;  // Positive distance from camera (view z negative)

    vec4 grid_view = VIEW_MATRIX * vec4(world_pos, 1.0);
    float grid_dist = -grid_view.z;

    if (grid_dist >= scene_dist) {  // Grid farther or equal (includes sky at max dist)
        discard;
    }

    // Planar distance from grid center for fading
    float dist = length(grid_pos);
    float fade_factor = 1.0 - clamp((dist - fade_start) / (fade_end - fade_start), 0.0, 1.0);

    // Compute separate major/minor lines
    float v_major = vertical_line(grid_pos, unit_size, major_line_thickness);
    float h_major = horizontal_line(grid_pos, unit_size, major_line_thickness);
    float v_minor = vertical_line(grid_pos, unit_size / float(subdivisions), minor_line_thickness);
    float h_minor = horizontal_line(grid_pos, unit_size / float(subdivisions), minor_line_thickness);

    // Combine with alpha
    float v_line = v_major + v_minor * minor_line_alpha;
    float h_line = h_major + h_minor * minor_line_alpha;
    v_line = clamp(v_line, 0.0, 1.0);
    h_line = clamp(h_line, 0.0, 1.0);

    // Total line strength
    float total_line = max(v_line, h_line);

    // Compute checkerboard
    float chec = checker(grid_pos, unit_size);

    // Debug colors: red for vertical, blue for horizontal, gray for checker/background
    vec3 final_color = mix(checker_color.rgb * chec, grid_color.rgb, total_line);

    // Premultiplied alpha
    float alpha_chec = chec * checker_color.a * (1.0 - total_line);
    float alpha_grid = total_line * grid_color.a;
    ALPHA = (alpha_chec + alpha_grid) * fade_factor;
    ALBEDO = final_color;
}
