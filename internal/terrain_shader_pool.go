package internal

import (
	"grow.graphics/gd"
)

// TerrainShaderPool is designed to efficiently share shaders across
// multiple [TerrainTile] instances. Such that new shaders only need
// to be created when we run out of texture samplers.
type TerrainShaderPool struct {
	gd.Class[TerrainShaderPool, gd.Resource]

	shader gd.ShaderMaterial // at the moment we just have a single shared shader.
}

func (pool *TerrainShaderPool) OnCreate() {
	tmp := pool.Temporary
	shader, ok := gd.Load[gd.Shader](tmp, "res://shader/terrain.gdshader")
	if !ok {
		return
	}
	grass, ok := gd.Load[gd.Texture2D](tmp, "res://terrain/alpine_grass.png")
	if !ok {
		return
	}
	pool.shader = *gd.Create(pool.KeepAlive, new(gd.ShaderMaterial))
	pool.shader.SetShader(shader)
	pool.shader.SetShaderParameter(tmp.StringName("albedo"), tmp.Variant(gd.Color{1, 1, 1, 1}))
	pool.shader.SetShaderParameter(tmp.StringName("uv1_scale"), tmp.Variant(gd.Vector2{1, 1}))
	pool.shader.SetShaderParameter(tmp.StringName("texture_albedo"), tmp.Variant(grass))
	pool.shader.SetShaderParameter(tmp.StringName("radius"), tmp.Variant(2.0))
}

func (pool *TerrainShaderPool) GetShader() gd.ShaderMaterial { return pool.shader }
