shader_type canvas_item;

uniform float aspect : hint_range(0.1, 10.0) = 1.0; // Set this to rect_size.x / rect_size.y via script for aspect correction

void fragment() {
    // Normalized pixel coordinates (from -1 to 1, aspect-corrected)
    vec2 uv = (UV * 2.0 - 1.0);
    uv.x *= aspect;

    // Parameters
    float radius = 0.5;
    float thickness = 0.1;
    float arcLength = 4.0; // in radians, adjust for arc size (e.g., 2.0 is about 1/3 circle)
    float speed = 6.0; // rotation speed
    float blur = 0.005; // for anti-aliasing

    // Distance and angle
    float dist = length(uv);
    float angle = atan(uv.y, uv.x);

    // Rotate the angle
    float rotatedAngle = mod(angle - TIME * speed, TAU);

    // Arc intensity (fades from head to tail)
    float arc = smoothstep(0.0, arcLength, rotatedAngle);
    arc *= step(rotatedAngle, arcLength); // Ensure it's zero outside the arc

    // Ring shape
    float ring = smoothstep(radius - thickness - blur, radius - thickness, dist) -
                smoothstep(radius + blur, radius + thickness + blur, dist);

   // Combine over white background
    vec3 bgColor = vec3(1.0, 1.0, 1.0); // White background
    vec3 spinnerColor = vec3(0.3, 0.3, 0.3); // Spinner color
    float spinnerIntensity = ring * arc;
    vec3 finalColor = mix(bgColor, spinnerColor, spinnerIntensity);

    // Output (fully opaque)
    COLOR = vec4(finalColor, 1.0);
}
