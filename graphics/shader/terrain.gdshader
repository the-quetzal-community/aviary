// NOTE: Shader automatically converted from Godot Engine 4.3.stable's StandardMaterial3D.

shader_type spatial;
render_mode blend_mix, depth_draw_opaque, cull_back, diffuse_burley, specular_schlick_ggx, world_vertex_coords;

uniform sampler2DArray texture_albedo : source_color, filter_linear_mipmap, repeat_enable;
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

uniform vec3 selection;
uniform bool selection_active;

uniform sampler2D paint_texture : source_color, filter_linear_mipmap, repeat_enable;

varying vec3 world;
varying flat vec4 texture;
varying vec4 weights;

void vertex() {
	UV = UV * uv1_scale.xy + uv1_offset.xy;

    float dx = (VERTEX.x - uplift.x);
    float dy = (VERTEX.z - uplift.z);
	// uplift the terrain, if within radius
	if (dx*dx+dy*dy < radius*radius) {
        VERTEX.y += height * (1.0 - (dx*dx+dy*dy)/(radius*radius));
    }
    VERTEX.y = max(VERTEX.y, -2.0);
    world = VERTEX;
    texture = CUSTOM0;
    weights = CUSTOM1;
}

bool in_brush(vec2 uv) {
    float dx = (world.x - uplift.x);
    float dy = (world.z - uplift.z);
    return dx*dx+dy*dy < radius*radius;
}

void fragment() {
	vec2 base_uv = UV;

    // texture has top-left, top-right, bottom-left, bottom-right
    // samples of the terrain around the vertex, we will interpolate
    // between these samples to get the final albedo
	vec4 colorTL = texture(texture_albedo, vec3(base_uv, texture.r));
	vec4 colorTR = texture(texture_albedo, vec3(base_uv, texture.g));
	vec4 colorBL = texture(texture_albedo, vec3(base_uv, texture.b));
	vec4 colorBR = texture(texture_albedo, vec3(base_uv, texture.a));

    vec4 albedo_tex = colorTL * weights.x + colorTR * weights.y + colorBL * weights.z + colorBR * weights.w;

	float highlight = 1.0;
	float dx = (world.x - uplift.x);
    float dy = (world.z - uplift.z);
	// uplift the terrain, if within radius
	if (brush_active && dx*dx+dy*dy < radius*radius) {
        highlight = 2.0;
	}
	if (paint_active && dx*dx+dy*dy < radius*radius) {
        albedo_tex = texture(paint_texture, base_uv);
    }
    ALBEDO = albedo_tex.rgb * highlight;

    if (selection_active) {
        float sdx = (world.x - selection.x);
        float sdy = (world.z - selection.y);
        if (sdx*sdx+sdy*sdy < selection.z*selection.z) {
           ALBEDO = vec3(1.0, 1.0, 1.0);
        }
    }
}
