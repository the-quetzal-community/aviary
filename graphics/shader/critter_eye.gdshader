shader_type spatial;
render_mode blend_mix, cull_back;

// blink ∈ [0, 1] — 0 fully open, 1 fully closed. The lid wipes
// from +Y (top of the eye, head-up direction) down across the
// sphere as blink approaches 1.
uniform float blink : hint_range(0.0, 1.0) = 0.0;

// Eye palette. Sclera covers the bulk, iris fills a disc around
// the look direction, pupil is a smaller dot inside the iris.
uniform vec3 sclera_color : source_color = vec3(0.95, 0.95, 0.92);
uniform vec3 iris_color : source_color = vec3(0.20, 0.42, 0.58);
uniform vec3 pupil_color : source_color = vec3(0.02, 0.02, 0.02);
// Default matches the critter body's plain grey so a closed eye
// blends back into the surface rather than looking like a brown
// patch sitting on top.
uniform vec3 lid_color : source_color = vec3(0.78, 0.78, 0.78);

// look_dir is the unit vector (in local space) the pupil is
// looking at. Driven from CPU each frame for the "eyes wander"
// idle animation.
uniform vec3 look_dir = vec3(0.0, 0.0, 1.0);

uniform float iris_size : hint_range(0.0, 1.0) = 0.35;
uniform float pupil_size : hint_range(0.0, 1.0) = 0.14;

varying vec3 local_dir;

void vertex() {
	// SphereMesh is unit-radius around origin in local space, so the
	// vertex position doubles as its direction-from-center. Pass it
	// to the fragment shader so the colour switch can use the
	// rotation-invariant local-space direction (UV would distort at
	// the poles and rotate with model orientation).
	local_dir = normalize(VERTEX);
}

void fragment() {
	vec3 n = normalize(local_dir);
	float threshold = 1.0 - 2.0 * clamp(blink, 0.0, 1.0);
	if (n.y > threshold) {
		ALBEDO = lid_color;
	} else {
		float c = dot(n, normalize(look_dir));
		if (c > 1.0 - pupil_size * 0.5) {
			ALBEDO = pupil_color;
		} else if (c > 1.0 - iris_size * 0.5) {
			ALBEDO = iris_color;
		} else {
			ALBEDO = sclera_color;
		}
	}
	ROUGHNESS = 0.4;
	SPECULAR = 0.5;
}
