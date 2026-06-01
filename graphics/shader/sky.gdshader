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
// Moon phase, wired from Client.applyMoonState: 0 = new moon (dark), 0.5 = half,
// 1 = full. The moon is drawn directly here (it is NOT a LIGHTn — the cool
// moonlight is a separate LightOnly directional in Go), sitting opposite the
// sun so it rises as the sun sets. moon_phase both carves the lit crescent and
// scales the disk's brightness, mirroring the Go-side light energy.
uniform float moon_phase : hint_range(0.0, 1.0) = 0.5;
// Apparent size of the cloud cells and the drift velocity (uv units / second)
// of the wind that carries them across the sky.
uniform float cloud_scale = 1.6;
uniform vec2 cloud_wind = vec2(0.045, 0.014);

// Primary raymarch step count, driven by the graphics-quality tier
// (Client.applyCloudQuality). 0 selects the cheap flat 2D projection used on the
// lower tiers; > 0 switches on the volumetric march (Refined), where the value is
// the number of samples taken through the cloud slab; < 0 disables the painted
// sky clouds entirely (Highest), where a world-space FogVolume draws real,
// fly-through clouds instead (clouds_fog.gdshader).
uniform int cloud_steps = 0;

// --- volumetric cloud slab -----------------------------------------------------
//
// In the march branch the eye sits at the origin and the clouds occupy a flat
// slab between these two "heights"; the view ray crosses the slab between
// t = CLOUD_BASE/dir.y and t = CLOUD_TOP/dir.y. Sampling the horizontal shape
// at dir.xz * t reproduces the same horizon-stretching perspective as the cheap
// branch's dir.xz / (dir.y + 0.18) projection. A *lower* base brings the slab
// nearer the eye, so the clouds sit lower in the sky and read as larger, closer
// puffs rather than a thin distant band. The light march walks a few short steps
// toward the sun for self-shadowing (Beer's law); combined with the powder term
// and phase function in sky() that is what gives the volume its fluffy, lit look.
//
// These are the look knobs — they hot-reload in a running Godot, so they can be
// dialled by eye without rebuilding the Go side.
const float CLOUD_BASE = 0.5;          // slab bottom: lower = nearer / lower clouds
const float CLOUD_TOP = 1.05;          // slab top: TOP-BASE is the cloud thickness
const int CLOUD_LIGHT_STEPS = 4;       // sun-ward samples per primary step
const float CLOUD_LIGHT_STEP = 0.10;   // spacing of those samples
const float CLOUD_DENSITY = 3.6;       // lower = lighter / airier clouds
const float CLOUD_LIGHT_ABSORB = 2.6;  // lower = brighter interiors (less self-shadow)

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

// Henyey-Greenstein phase function: how much light scatters toward the view
// direction given the cosine between the view and the sun. g in (0,1) biases
// forward, which is what produces the bright rim / silver lining when looking
// toward the sun through a cloud edge.
float hg(float cos_t, float g) {
	float g2 = g * g;
	float denom = 1.0 + g2 - 2.0 * g * cos_t;
	return (1.0 - g2) / (4.0 * PI * pow(max(denom, 0.0001), 1.5));
}

// Cloud density at a world-ish point in the march branch. The horizontal shape
// reuses the tiling 2D fbm (so it wraps seamlessly under the drifting wind, with
// no float-precision decay over a long session, exactly like the cheap branch),
// while p.y selects a cumulus-like vertical profile: a flattish base that
// billows and erodes toward a rounded top. A finer fbm carves fluffy lobes,
// eroding the top far more than the base. 'wind' is the already-wrapped drift
// offset, passed in so every sample shares it.
float cloud_density(vec3 p, vec2 wind) {
	vec2 uv = p.xz * cloud_scale + wind;
	float shape = fbm(uv);
	// Coverage lowers the threshold -> broader cloud. The narrow smoothstep band
	// (0.10, vs the old 0.28) gives crisp, well-defined cloud bodies instead of
	// the soft blurry blobs.
	float thresh = mix(1.0, 0.30, coverage);
	float d = smoothstep(thresh, thresh + 0.10, shape);
	if (d <= 0.0) {
		return 0.0;
	}
	// Cumulus profile: solid flat base (hf small), rounded billowing top.
	float hf = clamp((p.y - CLOUD_BASE) / (CLOUD_TOP - CLOUD_BASE), 0.0, 1.0);
	float vfall = smoothstep(0.0, 0.10, hf) * smoothstep(1.0, 0.30, hf);
	// High-frequency erosion carves fluffy cauliflower lobes; erode the top much
	// harder than the base so the tops look puffy and the bases stay defined.
	float detail = fbm(uv * 4.3 + vec2(19.1, 7.7));
	d -= (1.0 - detail) * mix(0.25, 0.95, hf);
	return clamp(d, 0.0, 1.0) * vfall;
}

void sky() {
	vec3 dir = normalize(EYEDIR);

	// Direction toward the sun. In a Godot SKY shader LIGHT0_DIRECTION already
	// points TOWARD the light — Godot's own ProceduralSkyMaterial draws the sun
	// where dot(LIGHT0_DIRECTION, EYEDIR) ~ 1 — so it is used directly, NOT
	// negated. (This is the OPPOSITE sign from a spatial shader's light(), where
	// the direction is the way the light travels.) The old `-LIGHT0_DIRECTION`
	// put the sun at the antipode, which inverted the whole sky relative to the
	// world: bright at noon-as-midnight, dark when the world was lit, sun up at
	// night.
	vec3 sun_dir = LIGHT0_ENABLED ? normalize(LIGHT0_DIRECTION) : vec3(0.0, 1.0, 0.0);
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

	// Moon disk + soft halo, opposite the sun so it climbs as the sun sets. The
	// moon is cosmetic here (the scene-lighting counterpart is a LightOnly
	// directional in Go); moon_phase carves the lit crescent and scales the glow.
	{
		vec3 moon_dir = -sun_dir;                         // antipodal to the sun
		// Visible only once the moon clears the horizon AND the sky is dark
		// (night = 1 - day), so it fades out at dawn instead of hanging in blue sky.
		float vis = (1.0 - day) * smoothstep(-0.05, 0.10, moon_dir.y);
		float md = dot(dir, moon_dir);
		if (vis > 0.001 && md > 0.0) {
			// A stable tangent frame spanning the moon's face. Near the zenith
			// (midnight, moon overhead) world-up degenerates, so fall back to +X.
			vec3 ref   = abs(moon_dir.y) < 0.99 ? vec3(0.0, 1.0, 0.0) : vec3(1.0, 0.0, 0.0);
			vec3 right = normalize(cross(ref, moon_dir));
			vec3 upv   = cross(moon_dir, right);
			const float R = 0.035;                        // angular radius (~2°)
			float u = dot(dir, right) / R;                // -1..1 across the face
			float v = dot(dir, upv) / R;
			float disk = smoothstep(1.0, 0.85, u * u + v * v); // soft unit disc
			// Phase terminator: a half-ellipse through the poles, bulging to
			// (1 - 2*phase) at centre. Lit where u >= termX. phase 0 -> right limb
			// (new moon, nothing lit); phase 1 -> left limb (full moon, all lit).
			float termX = (1.0 - 2.0 * moon_phase) * sqrt(max(0.0, 1.0 - v * v));
			float lit = smoothstep(termX - 0.06, termX + 0.06, u);
			float halo = pow(max(md, 0.0), 1400.0);       // tight outer glow
			vec3 moon_col = vec3(0.85, 0.90, 1.0);        // cool pale white
			sky += (disk * lit * 4.0 + halo * 0.5 * moon_phase) * moon_col * vis;
		}
	}

	// Clouds on the upper hemisphere. Drift the field with the wind, but wrap the
	// offset to the tiling period so it never grows without bound (no float-
	// precision decay over a long session). Because the field tiles, the wrap is
	// seamless. The cloud_steps uniform selects between the cheap flat projection
	// (lower tiers) and the volumetric march (Refined/Highest).
	if (dir.y > 0.0 && coverage > 0.001 && cloud_steps >= 0) {
		vec2 wind = mod(cloud_wind * TIME, vec2(CLOUD_PERIOD));

		// The radiance cubemap only feeds reflections and ambient light, where
		// the cheap flat clouds are indistinguishable from the marched ones — so
		// restrict the volumetric march to the directly-viewed sky and keep the
		// realtime radiance update (this shader animates via TIME) cheap.
		bool march = cloud_steps > 0 && !AT_CUBEMAP_PASS;

		if (!march) {
			// --- cheap branch: project onto a dome and threshold the noise ---
			// Cells near the horizon stretch realistically; more coverage ->
			// lower threshold -> denser, broader cloud.
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
			cloud_col += vec3(1.0, 0.95, 0.85) * day * silver * 0.3;

			sky = mix(sky, cloud_col, cloud);
		} else if (dir.y > 0.02) {
			// --- volumetric branch: march the slab the ray crosses ---
			// (dir.y guarded above 0.02 so the 1/dir.y entry/exit distances stay
			// bounded near the horizon, where the fade below hides the cut-off.)
			float t0 = CLOUD_BASE / dir.y;
			float t1 = CLOUD_TOP / dir.y;
			float dt = (t1 - t0) / float(cloud_steps);

			// Lighting terms reused for every sample along this ray.
			vec3 sun_lum = LIGHT0_ENABLED ? LIGHT0_COLOR * max(LIGHT0_ENERGY, 0.001) : vec3(1.0);
			// Cool sky-fill on the shaded faces. This is deliberately well below
			// white: it is the only light the cloud's underside / self-shadowed
			// lobes receive, so it sets the DARK end of the cloud's range. Lifting
			// it toward white (as it used to, 0.66-0.84) left even the shadowed
			// side near-white, and with the sunlit side clipping past 1.0 the whole
			// cloud pinned to a flat neon-white cutout. Keeping it a believable
			// blue-grey skylight gives the billows somewhere darker to sit, so the
			// sun -> shadow gradient reads as real 3D form.
			vec3 ambient = mix(vec3(0.30, 0.34, 0.45), vec3(0.45, 0.49, 0.57), day);
			// Scattering split so a sunlit cloud reads as a solid lit body from
			// ANY view angle, not only when looking toward the sun. A near-
			// isotropic BASE carries the bulk brightness (this is what keeps the
			// sunlit tops bright white when seen from above — they used to fall to
			// grey because a forward-only phase dropped to ~0 with the sun behind
			// the viewer), and a sharp FORWARD lobe is added on top for the
			// silver-lining glow when looking toward the sun through a cloud edge.
			float cos_t = dot(dir, sun_dir);
			float phase = (0.7 + 0.3 * hg(cos_t, 0.25)) + hg(cos_t, 0.76) * 0.25;

			float transmittance = 1.0;
			vec3 scattered = vec3(0.0);
			for (int i = 0; i < cloud_steps; i++) {
				float t = t0 + (float(i) + 0.5) * dt;
				vec3 p = dir * t;
				float d = cloud_density(p, wind);
				if (d > 0.001) {
					// Short march toward the sun -> optical depth -> self-shadow.
					float ld = 0.0;
					for (int j = 0; j < CLOUD_LIGHT_STEPS; j++) {
						vec3 lp = p + sun_dir * (float(j) + 0.5) * CLOUD_LIGHT_STEP;
						ld += cloud_density(lp, wind);
					}
					float lightT = exp(-ld * CLOUD_LIGHT_STEP * CLOUD_LIGHT_ABSORB);
					// Powder term: darkens the lobes facing the light, the cue that
					// makes cumulus read as fluffy 3D cauliflower instead of flat.
					float powder = 1.0 - exp(-d * 8.0);

					// Direct sunlight (self-shadow + scatter + powder) over soft
					// ambient. The self-shadow term (lightT) is what gives the 3D
					// form — bright sunlit tops, darker bases — independent of view.
					// 0.85 (was 1.2): the noon sun is already energy ~1.35, so the
					// full 1.2 boost drove lit faces to ~2.0 and clipped the whole
					// cloud to white. At 0.85 the diffuse-lit bulk lands just under
					// 1.0 (bright white but with visible texture), while the sharp
					// forward phase lobe toward the sun still overshoots for a
					// genuine silver lining — bright where it should be, not flat.
					vec3 col = ambient + sun_lum * lightT * phase * powder * 0.85 * day;

					// Beer's law accumulation: blend in front-to-back.
					float att = d * dt * CLOUD_DENSITY;
					scattered += transmittance * col * att;
					transmittance *= exp(-att);
					if (transmittance < 0.02) {
						break;
					}
				}
			}

			// Composite over the sky, fading the contribution out toward the
			// horizon to hide the slab cut-off the same way the cheap branch
			// hides its projection seam. The fade ends low (0.10) so the clouds
			// reach down nearer the horizon and sit lower in the sky.
			float fade = smoothstep(0.02, 0.10, dir.y);
			sky = mix(sky, sky * transmittance + scattered, fade);
		}
	}

	COLOR = sky;
}
