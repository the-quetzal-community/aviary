shader_type spatial;
render_mode blend_mix, cull_front, unshaded;

uniform vec4 outline_color : source_color = vec4(1,0,0,1);
uniform float outline_width : hint_range(1.0, 1.5, 0.01) = 1.05;
uniform sampler2D texture_albedo : source_color, filter_linear_mipmap, repeat_enable;

void vertex() {
	VERTEX *= outline_width;
	VERTEX.y -= 0.001;
}

void fragment() {
    vec4 tex = texture(texture_albedo, UV);
    if (tex.a < 0.01) {
        discard;
    }
	ALBEDO = outline_color.rgb;
	ALPHA = outline_color.a;
}
