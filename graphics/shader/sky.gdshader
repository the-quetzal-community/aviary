shader_type sky;

// Procedural day/night sky with drifting volumetric-ish clouds.
//
// The sky reacts automatically to the scene's DirectionalLight (LIGHT0): the
// sun disk, the day/night colour blend and the cloud shading all follow the
// light's direction and energy, so the existing Time of Day / Sun Angle
// controls "just work" here without any extra uniforms. Only the cloud
// coverage is driven directly, by the environment menu's Clouds slider.

// 0 = clear sky, 1 = full overcast. Wired from Client.applyCloudDensity.
uniform float coverage : hint_range(0.0, 1.0) = 0.0;
// Apparent size of the cloud cells and the drift velocity (uv units / second)
// of the wind that carries them across the sky.
uniform float cloud_scale = 1.6;
uniform vec2 cloud_wind = vec2(0.045, 0.014);

// --- tileable value noise + fbm ----------------------------------------------
//
// The lattice wraps every CLOUD_PERIOD cells (the hash is fed mod(cell, period)),
// so the cloud field repeats seamlessly. That is what lets the drift offset be
// wrapped in sky() without a visible jump, which in turn keeps every TIME-driven
// coordinate small and free of float-precision decay over a long session.
const float CLOUD_PERIOD = 12.0;

float hash(vec2 p) {
	p = fract(p * vec2(123.34, 345.45));
	p += dot(p, p + 34.345);
	return fract(p.x * p.y);
}

float noise(vec2 p, vec2 period) {
	vec2 i = floor(p);
	vec2 f = fract(p);
	f = f * f * (3.0 - 2.0 * f); // smootherstep
	float a = hash(mod(i, period));
	float b = hash(mod(i + vec2(1.0, 0.0), period));
	float c = hash(mod(i + vec2(0.0, 1.0), period));
	float d = hash(mod(i + vec2(1.0, 1.0), period));
	return mix(mix(a, b, f.x), mix(c, d, f.x), f.y);
}

// Integer lacunarity (2.0) so each octave tiles on the same period and the whole
// fbm repeats every CLOUD_PERIOD units in p.
float fbm(vec2 p) {
	float v = 0.0;
	float amp = 0.5;
	float freq = 1.0;
	for (int i = 0; i < 5; i++) {
		v += amp * noise(p * freq, vec2(CLOUD_PERIOD * freq));
		freq *= 2.0;
		amp *= 0.5;
	}
	return v;
}

void sky() {
	vec3 dir = normalize(EYEDIR);

	// Direction toward the sun in the sky (LIGHT0_DIRECTION points the way the
	// light travels, i.e. away from the sun).
	vec3 sun_dir = LIGHT0_ENABLED ? normalize(-LIGHT0_DIRECTION) : vec3(0.0, 1.0, 0.0);
	// 1 in broad daylight, 0 once the sun has set well below the horizon.
	float day = smoothstep(-0.12, 0.18, sun_dir.y);

	// Base sky gradient: horizon -> zenith, blended day -> night.
	float h = clamp(dir.y, 0.0, 1.0);
	vec3 day_sky = mix(vec3(0.72, 0.80, 0.95), vec3(0.20, 0.46, 0.88), h);
	vec3 night_sky = mix(vec3(0.05, 0.06, 0.12), vec3(0.01, 0.02, 0.05), h);
	vec3 sky = mix(night_sky, day_sky, day);

	// Sun disk + soft glow (only meaningful above the horizon).
	if (LIGHT0_ENABLED) {
		float sd = dot(dir, sun_dir);
		vec3 sun_col = LIGHT0_COLOR * max(LIGHT0_ENERGY, 0.001);
		float disk = smoothstep(0.9994, 0.9998, sd);
		float glow = pow(max(sd, 0.0), 220.0);
		sky += (disk * 12.0 + glow * 0.5) * sun_col * day;
	}

	// Clouds on the upper hemisphere. Project the view direction onto a dome
	// so cells near the horizon stretch realistically, then threshold the noise
	// by coverage: more coverage -> lower threshold -> denser, broader cloud.
	if (dir.y > 0.0 && coverage > 0.001) {
		// Drift the field with the wind, but wrap the offset to the tiling
		// period so it never grows without bound (no float-precision decay over
		// a long session). Because the field tiles, the wrap is seamless.
		vec2 wind = mod(cloud_wind * TIME, vec2(CLOUD_PERIOD));
		vec2 uv = dir.xz / (dir.y + 0.18) * cloud_scale + wind;
		float n = fbm(uv);
		float thresh = mix(1.0, 0.18, coverage);
		float cloud = smoothstep(thresh, thresh + 0.28, n);
		// Fade out toward the horizon to hide the projection seam.
		cloud *= smoothstep(0.0, 0.22, dir.y);

		// Shade: bright tops, darker bases, tinted by the sun and time of day.
		// Constant offset (not a scale) so this layer also tiles and stays
		// seamless across the wind wrap.
		float lit = 0.6 + 0.4 * fbm(uv + vec2(31.7, 11.3));
		vec3 cloud_col = mix(vec3(0.32, 0.35, 0.42), vec3(1.0), lit);
		cloud_col *= mix(vec3(0.18, 0.20, 0.30), vec3(1.0), day);
		// Warm "silver lining" on the cloud edge facing the sun.
		float silver = pow(max(dot(dir, sun_dir), 0.0), 8.0);
		cloud_col += vec3(1.0, 0.95, 0.85) * day * silver * 0.5;

		sky = mix(sky, cloud_col, cloud);
	}

	COLOR = sky;
}
