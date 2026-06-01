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

// brush_mode (see terrain.gdshader): only 0 (add) vs 1/2 (special) affect the
// simple height preview here; sides use a close approximation.
uniform float brush_mode = 0.0;

uniform float blend_sharpness = 8.0; // Higher values make transitions sharper, reducing unwanted blending/shearing on sides
varying vec3 world_pos;
varying vec3 world_normal;

uniform float uv_scale = 1;

void vertex() {
    float dx = (VERTEX.x - uplift.x);
    float dy = (VERTEX.z - uplift.z);
	// uplift the terrain, if within radius
	if (dx*dx+dy*dy < radius*radius && VERTEX.y > 0.0) {
		float br2 = dx*dx + dy*dy;
		float rr = radius*radius;
		float w = 1.0 - br2/rr;
		if (brush_mode > 0.5) {
			// plateau/smooth preview: shift the wall RIGIDLY by the delta its top edge
			// moves, so the skirt stays welded to the (flattened) terrain top instead
			// of collapsing toward uplift.y. VERTEX.y here is the terrain top height
			// PLUS the skirt's +2.2 offset (see reloadSides), so recover the true top
			// before measuring the flatten delta — matching terrain.gdshader, whose
			// top sits at top + (uplift.y - top)*fac. Plateau uses the flat-topped
			// falloff, smooth the soft cone (PLATEAU_FLAT_CORE matches editor_terrain.go).
			float top = VERTEX.y - 2.2;
			float ff = w; // smooth: 1−d²/r² cone
			if (brush_mode < 1.5) {
				float t = sqrt(br2) / radius;
				ff = 1.0 - smoothstep(0.6, 1.0, t); // plateau: flat top
			}
			float fac = clamp(height*ff, 0.0, 1.0);
			VERTEX.y += (uplift.y - top) * fac;
		} else {
			VERTEX.y += height * w;
		}
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

// Custom lighting that ignores shadow attenuation. ATTENUATION for the
// primary DirectionalLight includes the shadow map term; by not using it
// the terrain side walls neither cast nor receive shadows. We still get
// proper directional response (sun-facing sides brighter) and a touch of
// specular so the rock doesn't look completely flat.
void light() {
    vec3 L = LIGHT;
    vec3 N = NORMAL;
    // Note: we use -LIGHT because the side wall normals (generated via
    // Cross product in reloadSides) end up producing inverted shading
    // with the raw LIGHT vector in view space for this particular
    // geometry. Negating gives the expected "sun-facing side brighter".
    float NdotL = max(0.0, dot(N, -L));

    // Simple Lambert diffuse (good enough for the rough mineral texture;
    // the default Burley path is bypassed once a light() function exists).
    DIFFUSE_LIGHT += ALBEDO * LIGHT_COLOR * NdotL;

    // The scene's environment ambient (Environment ambient light + sky) is added
    // on top of DIFFUSE_LIGHT automatically by the engine, even with a custom
    // light() function — and AMBIENT_LIGHT isn't a light() built-in anyway — so
    // the side walls get the same base illumination as the top terrain surface
    // without adding it by hand here.

    // Subtle specular so the rock walls catch a little light; keep it low
    // to preserve the matte buried-rock appearance.
    vec3 V = VIEW;
    vec3 H = normalize(-L + V);   // consistent with the -L used for diffuse
    float NdotH = max(0.0, dot(N, H));
    float spec = pow(NdotH, 24.0);
    SPECULAR_LIGHT += LIGHT_COLOR * spec * 0.08;
}
