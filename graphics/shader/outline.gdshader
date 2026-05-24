shader_type spatial;
render_mode blend_mix, cull_front, unshaded;

uniform vec4 outline_color : source_color = vec4(1,0,0,1);
uniform float outline_width : hint_range(1.0, 1.5, 0.01) = 1.05;
uniform sampler2D texture_albedo : source_color, filter_linear_mipmap, repeat_enable;
// has_texture flags whether the caller wired up a real texture for
// the alpha-cutout check. Procedural materials (the critter eye
// shader, MakeHuman .obj dressings) don't have a BaseMaterial3D
// albedo texture to extract, so without this opt-in the discard
// below fires for every pixel and the outline never renders.
uniform bool has_texture = false;

void vertex() {
	VERTEX *= outline_width;
	VERTEX.y -= 0.001;
}

void fragment() {
	if (has_texture) {
		vec4 tex = texture(texture_albedo, UV);
		if (tex.a < 0.01) {
			discard;
		}
	}
	ALBEDO = outline_color.rgb;
	ALPHA = outline_color.a;
}
