#[vertex]
#version 450

#include "./CloudsInc.comp"

layout(location = 0) in vec2 VertPos;

void main() {
	gl_Position = vec4(VertPos, 0.0, 1.0);
}

#[fragment]
#version 450

#include "./CloudsInc.comp"

// AVIARY pass (not upstream): writes the cloud's front-face depth into the scene depth
// buffer wherever an opaque cloud sits, so the transparent water/foliage that renders
// AFTER this (effect_callback_type = PRE_TRANSPARENT) depth-rejects behind the cloud
// instead of drawing on top of it. Without this you see the ground through clouds from
// above. Color writes are masked off in the pipeline — this pass only touches depth.

layout(binding = 0) uniform sampler2D input_color_image; // .a = accumulated cloud density
layout(binding = 1) uniform sampler2D input_data_image;  // .r = initialdistanceSample (cloud front, linear)

layout(binding = 2, std140) uniform SceneDataBlock {
	SceneData data;
	SceneData prev_data;
} scene_data_block;

layout(binding = 3) uniform GenericDataBlock {
	GenericData data;
} genericData;

layout(location = 0) out vec4 RastColor;

void main() {
	RastColor = vec4(0.0); // masked off by the pipeline color-blend state; never stored.

	vec2 fullSize = vec2(genericData.data.raster_size) * genericData.data.resolutionscale;
	vec2 uv = gl_FragCoord.xy / fullSize;

	// Only fully-formed cloud cores occlude; wispy low-density edges stay see-through
	// (realistic, and avoids hard depth edges around thin cloud). Threshold is tunable.
	float density = texture(input_color_image, uv).a;
	if (density < 0.5) { discard; }

	float cloudFrontDist = texture(input_data_image, uv).r;
	if (cloudFrontDist <= 0.0) { discard; }

	// Reconstruct this pixel's world-space view ray (mirrors SunshineCloudsPostCompute).
	vec2 ndc = uv * 2.0 - 1.0;
	vec4 viewPos = scene_data_block.data.inv_projection_matrix * vec4(ndc, 0.0, 1.0);
	viewPos.xyz /= viewPos.w;
	vec3 raydir = normalize(mat3(scene_data_block.data.main_cam_inv_view_matrix) * normalize(viewPos.xyz));
	vec3 rayOrigin = scene_data_block.data.main_cam_inv_view_matrix[3].xyz;

	vec3 worldPos = rayOrigin + raydir * cloudFrontDist;

	// Forward-project the cloud front to a reverse-Z depth value. Use inverses of the
	// matrices the rest of the addon trusts (inv_projection / main_cam_inv_view) to avoid
	// the view_matrix-is-rotation-only convention quirk seen in the compute reprojection.
	mat4 viewM = inverse(scene_data_block.data.main_cam_inv_view_matrix);
	mat4 projM = inverse(scene_data_block.data.inv_projection_matrix);
	vec4 clip = projM * viewM * vec4(worldPos, 1.0);
	gl_FragDepth = clip.z / clip.w;
}
