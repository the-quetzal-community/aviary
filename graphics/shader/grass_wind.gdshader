shader_type spatial;

// Wind sway for the instanced (MultiMesh) terrain grass. Each blade is a
// MultiMesh instance, so MODEL_MATRIX carries that blade's world transform.
//
// This shader reproduces the look of the imported yughues grass material —
// double-sided, normal mapped, roughness ~0.5 — while adding the vertex sway.
// The per-surface look (albedo/normal/roughness/metallic) is copied from the
// source BaseMaterial3D in Go (see grassWindMaterial); the wind is shared.
//
// Alpha is an ALPHA-TO-COVERAGE cut-out, NOT a smooth blend: the fragment
// alpha-tests against alpha_scissor so thousands of blades render in the OPAQUE
// pass and write depth. A smooth ALPHA + blend_mix would instead drop the grass
// into the transparency queue, where it is sorted only by per-object distance
// with no depth write — which made the blades pop in front of / behind the
// (also transparent) water. Staying opaque keeps it out of that queue so it
// depth-tests correctly against water and itself.
//
// A plain scissor antialiases nothing, so the cut edge looks pixelated. The
// alpha_to_coverage render_mode fixes that without re-introducing transparency:
// the hardware turns the alpha edge into MSAA coverage samples, so the cut-out
// is antialiased by the very MSAA the quality tiers already run (2× at Refined,
// 4× at Highest — see graphics_quality.go). ALPHA_ANTIALIASING_EDGE +
// ALPHA_TEXTURE_COORDINATE drive Godot's derivative-based edge sharpening so the
// soft band stays ~1px and doesn't shrink with mips at distance. On the no-MSAA
// tiers (Toaster/Average) this degrades gracefully to the hard scissor.
//
// world_vertex_coords lets us read and write VERTEX in world space, so the
// wind blows in a single consistent world direction across the whole field
// (instead of each blade's randomly-yawed local axis) and the per-blade
// height mask is a plain world-Y difference from the blade's plant point.
//
// cull_disabled keeps the flat billboard blades visible from both sides
// (the source material is doubleSided).
render_mode blend_mix, depth_draw_opaque, cull_disabled, diffuse_burley, specular_schlick_ggx, world_vertex_coords, alpha_to_coverage;

uniform sampler2D albedo_texture : source_color, filter_linear_mipmap, repeat_enable;
uniform vec4 albedo : source_color = vec4(1.0);
uniform sampler2D normal_texture : hint_normal, filter_linear_mipmap, repeat_enable;
uniform bool has_normal_texture = false;
uniform float normal_scale : hint_range(-16.0, 16.0) = 1.0;
uniform float roughness : hint_range(0.0, 1.0) = 0.5;
uniform float metallic : hint_range(0.0, 1.0) = 0.0;
// Dielectric specular reflectance (Godot's f0; 0.5 is the engine default, ~4%).
// Defaults to 0 — matte — because this is instanced cut-out foliage: the flat
// blades are usually seen near edge-on, where Fresnel drives the AMBIENT
// specular toward white, so under the colour-source day/night ambient any
// non-zero value lays a bright sheen on the grass that the matte terrain never
// shows. Overridable per-material if a design ever wants a glint. NOTE:
// grassWindMaterial copies albedo/roughness/metallic/normal from the source but
// NOT specular, so this default is the operative value at runtime regardless of
// the imported material (and Godot's glTF import doesn't reliably carry a
// specular factor anyway). The source asset is also kept matte at import time.
uniform float specular : hint_range(0.0, 1.0) = 0.0;
uniform float alpha_scissor : hint_range(0.0, 1.0) = 0.5;
// Width of the antialiased coverage band around the cut-out edge (only visible
// with MSAA on). Larger = softer/fuzzier blade edges; 0 collapses to a hard cut.
uniform float alpha_aa_edge : hint_range(0.0, 1.0) = 0.3;
// Distance thinning recovery. As the grass texture mips down, the scissor cutoff
// is eased toward alpha_scissor * thin_recover_floor so blades stop dropping out
// (lower floor = blades survive further out, but distant grass fattens). The
// reduction reaches full strength at mip level thin_recover_lod.
uniform float thin_recover_floor : hint_range(0.1, 1.0) = 0.35;
uniform float thin_recover_lod   : hint_range(1.0, 8.0) = 4.0;

// Wind is driven from the environment "Wind" slider via global shader
// parameters (registered + updated in client.go), so every grass blade in the
// world reacts to the one shared wind with no per-material bookkeeping.
// strength is the horizontal lean (world units) at the tip of a 1-unit-tall
// blade during a full gust; the lean scales with height up the blade so grass
// of any size sways proportionally.
global uniform float grass_wind_strength;
global uniform float grass_wind_speed;
global uniform vec2  grass_wind_dir;
// 0 = symmetric travelling waves (light wind ripples across the field, passing
// back and forth through vertical). 1 = a steady one-directional push that
// holds the blades over downwind with the ripple riding on top as flutter —
// the hurricane look. Driven up with the slider (see updateWeatherIntensity).
global uniform float grass_wind_bias;

// Height-brush hover preview. While a raise/lower (or river) terrain brush is
// armed and hovering, terrain.gdshader lifts its surface by
// grass_brush_height * (1 − d²/r²) inside the brush disc and floors it at −2
// (see its vertex()). These globals let each grass blade ride that same preview
// shift before the stroke commits; reprojectGrass re-plants on the real surface
// on commit and the editor resets grass_brush_height to 0 when not previewing.
// Only the brush centre's XZ is needed — the lift is a pure vertical offset.
global uniform vec2  grass_brush_uplift;
global uniform float grass_brush_height;
global uniform float grass_brush_radius;
// Flatten preview (plateau/smooth): when grass_brush_mode > 0.5 the blade is
// pulled toward grass_brush_target_y by grass_brush_height*(1−d²/r²) instead of
// lifted additively, mirroring terrain.gdshader's flatten branch so grass rides
// the previewed plateau/smooth before it commits. grass_brush_height then carries
// the strength fraction rather than an additive height.
global uniform float grass_brush_mode;
global uniform float grass_brush_target_y;

void vertex() {
	// The instance's plant point in world space. Every vertex of one blade
	// shares it, so the blade hinges as a unit; using it (rather than each
	// vertex) for the wave phase keeps a blade coherent while neighbouring
	// blades stay out of lockstep.
	vec3 root = MODEL_MATRIX[3].xyz;

	// Height of this vertex above its plant point. Blades stand up +Y in world
	// space once planted, so this measures distance up the blade. max() anchors
	// the base (and any verts modelled at/below the pivot) so the grass stays
	// rooted while the tips travel.
	float height = max(VERTEX.y - root.y, 0.0);

	vec2 dir = normalize(grass_wind_dir);
	float t = TIME * grass_wind_speed;
	// Phase advances along the wind direction so gusts visibly roll downwind.
	float phase = dot(root.xz, dir) * 0.35;

	// Light wind: a symmetric travelling wave that ripples across the field,
	// rocking the blades back and forth through vertical.
	float wave = sin(t + phase) * 0.7 + sin(t * 1.9 + phase * 1.7) * 0.3;

	// Strong wind: a steady downwind pull with rapid, small flutter on top —
	// the blades are yanked over one way and "pull" in fast jitters rather than
	// rolling in a wave. No low-frequency wave, and the offset stays positive
	// (always downwind) so it never rocks back through vertical.
	float jitter = sin(t * 5.3 + phase * 3.1) * 0.12 + sin(t * 8.7 + phase) * 0.06;
	float pull = 0.85 + jitter;

	// Cross-fade wave → pull as the wind rises (grass_wind_bias ramps in late),
	// so low/mid wind keeps the waves and a hurricane is a hard directional pull.
	float sway = mix(wave, pull, grass_wind_bias);

	// Lean expressed as a fraction of the blade's height (1.0 would lay the blade
	// flat on the ground). Cap it below 1 so even a hurricane only yanks the
	// blade hard over, never completely flat, and drop the tip (sqrt) so the
	// blade bends/pivots about its rooted base instead of stretching.
	const float MAX_LEAN = 0.8;
	float leanFrac = clamp(sway * grass_wind_strength, -MAX_LEAN, MAX_LEAN);
	vec2 disp = dir * (leanFrac * height);
	VERTEX.xz += disp;
	// Shift Y by the drop only; verts at/below the pivot (height == 0) are
	// left untouched so base geometry isn't disturbed.
	float dropped = sqrt(max(height * height - dot(disp, disp), 0.0));
	VERTEX.y += dropped - height;

	// Height-brush preview lift (see the grass_brush_* uniforms). A pure vertical
	// offset on the whole blade, evaluated at its root so the blade rides the
	// previewed terrain coherently. Mirrors terrain.gdshader's disc falloff and
	// −2 floor so grass tracks the surface the user is about to commit.
	float br2 = grass_brush_radius * grass_brush_radius;
	if (grass_brush_height != 0.0 && br2 > 0.0) {
		vec2 bd = root.xz - grass_brush_uplift;
		float bd2 = dot(bd, bd);
		if (bd2 < br2) {
			float w = 1.0 - bd2 / br2;
			float newy;
			if (grass_brush_mode > 1.5) {
				// Smooth: pull toward the target with a soft 1−d²/r² cone. grass_brush_height
				// is the strength fraction, matching terrain.gdshader's smooth preview.
				float fac = clamp(grass_brush_height * w, 0.0, 1.0);
				newy = mix(root.y, grass_brush_target_y, fac);
			} else if (grass_brush_mode > 0.5) {
				// Plateau: flat-topped falloff so the blade rides the mesa (dead flat
				// across the core). PLATEAU_FLAT_CORE matches plateauFlatCore in
				// editor_terrain.go.
				float t = sqrt(bd2) / grass_brush_radius;
				float ff = 1.0 - smoothstep(0.6, 1.0, t);
				float fac = clamp(grass_brush_height * ff, 0.0, 1.0);
				newy = mix(root.y, grass_brush_target_y, fac);
			} else {
				// Additive lift (raise/lower/river).
				newy = root.y + grass_brush_height * w;
			}
			VERTEX.y += max(newy, -2.0) - root.y;
		}
	}
}

void fragment() {
	vec4 tex = texture(albedo_texture, UV) * albedo;
	ALBEDO = tex.rgb;

	// Mip level estimated from how fast the texel-space UV changes per screen
	// pixel — the same quantity the GPU uses to pick a mip. 0 up close, rising
	// with distance.
	vec2  tx  = UV * vec2(textureSize(albedo_texture, 0));
	float lod = max(0.0, 0.5 * log2(max(dot(dFdx(tx), dFdx(tx)), dot(dFdy(tx), dFdy(tx)))));

	// Ease the alpha-test cutoff down with mip level. Box-averaged mips sink a
	// thin blade's peak alpha below a fixed threshold, so whole blades vanish and
	// the field goes patchy at distance; lowering the cutoff as it mips keeps the
	// blades alive (cheap in-shader stand-in for coverage-preserving mips). Close
	// up (lod≈0) the cutoff is the full alpha_scissor, so the crisp look is kept.
	float cutoff = alpha_scissor * mix(1.0, thin_recover_floor, clamp(lod / thin_recover_lod, 0.0, 1.0));

	ALPHA = tex.a;
	ALPHA_SCISSOR_THRESHOLD = cutoff; // opaque pass + depth write (no transparency sort vs water)
	// Antialias the cut edge via MSAA coverage (see alpha_to_coverage above).
	// ALPHA_TEXTURE_COORDINATE in texel units lets Godot size the soft band by
	// the alpha texture's own derivatives, so it stays crisp at any distance.
	ALPHA_ANTIALIASING_EDGE = alpha_aa_edge;
	ALPHA_TEXTURE_COORDINATE = tx;
	ROUGHNESS = roughness;
	METALLIC = metallic;
	// Matte by default (see the `specular` uniform). f0 == 0 removes the direct
	// AND the ambient specular (the ambient term is f0-gated), so the blades
	// respond to ambient through diffuse only — like the terrain. Diffuse ambient
	// is specular-independent, so the lit look is unchanged.
	SPECULAR = specular;
	if (has_normal_texture) {
		NORMAL_MAP = texture(normal_texture, UV).rgb;
		NORMAL_MAP_DEPTH = normal_scale;
	}
}
