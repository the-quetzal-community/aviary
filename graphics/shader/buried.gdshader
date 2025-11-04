// NOTE: Shader automatically converted from Godot Engine 4.3.stable's StandardMaterial3D.

shader_type spatial;
render_mode blend_mix, depth_draw_opaque, cull_back, diffuse_burley, specular_schlick_ggx, world_vertex_coords;

uniform sampler2D texture_albedo : source_color, filter_linear_mipmap, repeat_enable;
uniform float point_size : hint_range(0.1, 128.0, 0.1);

// will be on the mouse click, to uplift the terrain
uniform vec3 uplift;
uniform float height;
uniform float radius;
uniform bool brush_active;

uniform float blend_sharpness = 8.0; // Higher values make transitions sharper, reducing unwanted blending/shearing on sides
varying vec3 world_pos;
varying vec3 world_normal;

uniform float uv_scale = 0.1;

void vertex() {
    float dx = (VERTEX.x - uplift.x);
    float dy = (VERTEX.z - uplift.z);
	// uplift the terrain, if within radius
	if (dx*dx+dy*dy < radius*radius && VERTEX.y > 0.0) {
        VERTEX.y += height * (1.0 - (dx*dx+dy*dy)/(radius*radius));
    }
    VERTEX.y -= 2.2;
    VERTEX.y = max(VERTEX.y, -2.0);
    world_pos = VERTEX;
    world_normal = NORMAL;
}

void fragment() {
    // Normalize interpolated world normal for accurate blending
    vec3 n = normalize(world_normal);
    ALBEDO = n * 0.5 + 0.5;

    // Compute triplanar blend weights based on absolute world normal
    vec3 blend = abs(n);
    blend = max(blend, vec3(0.00001)); // Avoid zero
    blend = pow(blend, vec3(blend_sharpness)); // Sharpen blend to reduce shearing from unwanted top projection mixing
    blend /= (blend.x + blend.y + blend.z); // Normalize

    // Procedural UVs for each projection plane (scale and fract for repeating tiles)
    vec2 uv_x = fract(world_pos.zy * uv_scale); // Projection for X-normal dominant (side)
    vec2 uv_y = fract(world_pos.xz * uv_scale); // Projection for Y-normal dominant (top/flat)
    vec2 uv_z = fract(world_pos.xy * uv_scale); // Projection for Z-normal dominant (side)

    // Sample the single texture for all projections
    vec3 tex_x = texture(texture_albedo, uv_x).rgb * blend.x;
    vec3 tex_y = texture(texture_albedo, uv_y).rgb * blend.y;
    vec3 tex_z = texture(texture_albedo, uv_z).rgb * blend.z;

    // Blend the samples
    vec3 albedo = tex_x + tex_y + tex_z;
    ALBEDO = albedo;
}
