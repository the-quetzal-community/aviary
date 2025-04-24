shader_type spatial;
render_mode blend_mix, depth_draw_opaque, cull_back;

uniform sampler2D albedo_texture;
uniform vec2 uv_scroll_speed = vec2(0.0, -0.5); // Scroll downwards
uniform vec4 albedo_color : source_color = vec4(1.0);

void fragment() {
    // Scroll UVs based on time
    vec2 uv = UV + uv_scroll_speed * TIME;
    // Sample texture with scrolled UVs
    vec4 tex_color = texture(albedo_texture, uv);
    ALBEDO = tex_color.rgb * albedo_color.rgb;
    ALPHA = tex_color.a * albedo_color.a;
}