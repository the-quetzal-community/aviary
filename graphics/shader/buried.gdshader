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

varying vec3 world;
varying flat vec4 texture;
varying vec4 weights;

void vertex() {
    float dx = (VERTEX.x - uplift.x);
    float dy = (VERTEX.z - uplift.z);
	// uplift the terrain, if within radius
	if (dx*dx+dy*dy < radius*radius) {
        VERTEX.y += height * (1.0 - (dx*dx+dy*dy)/(radius*radius));
    }
    world = VERTEX;
    texture = CUSTOM0;
    weights = CUSTOM1;
}

void fragment() {
	vec4 albedo_tex = texture(texture_albedo, UV);

	float highlight = 1.0;
	float dx = (world.x - uplift.x);
    float dy = (world.z - uplift.z);
    ALBEDO = albedo_tex.rgb * highlight;
}
