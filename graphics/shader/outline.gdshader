shader_type spatial;
render_mode blend_mix, cull_front, unshaded;

uniform vec4 outline_color : source_color = vec4(1,0,0,1);
uniform float outline_width : hint_range(1.0, 1.5, 0.01) = 1.05;

void vertex() {
	VERTEX *= outline_width;
}

void fragment() {
	ALBEDO = outline_color.rgb;
	ALPHA = outline_color.a;
}
