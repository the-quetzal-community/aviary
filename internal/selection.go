package internal

import (
	"graphics.gd/classdb/BaseMaterial3D"
	"graphics.gd/classdb/Material"
	"graphics.gd/classdb/MeshInstance3D"
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/Resource"
	"graphics.gd/classdb/Shader"
	"graphics.gd/classdb/ShaderMaterial"
	"graphics.gd/variant/Color"
	"graphics.gd/variant/Object"
)

// Select creates a highlight around the given node to indicate selection.
func Select(node Node.Instance, selected bool) {
	instance, ok := Object.As[MeshInstance3D.Instance](node)
	if ok {
		mesh := instance.Mesh()
		for i := range mesh.GetSurfaceCount() {
			if selected {
				// Pick the material we'll wrap with the outline pass.
				// Library parts set per-surface materials on the
				// mesh; procedural parts (eyes, …) typically only
				// set a MaterialOverride on the MeshInstance3D. Fall
				// back to the override when the surface itself has
				// no material, and skip the outline entirely when
				// neither exists — Resource.Duplicate on a Nil
				// reference panics ("invalid reference").
				source := mesh.SurfaceGetMaterial(i)
				if source == Material.Nil {
					source = instance.AsGeometryInstance3D().MaterialOverride()
				}
				if source == Material.Nil {
					continue
				}
				outline := LoadSync[Shader.Instance]("res://shader/outline.gdshader")
				shader := ShaderMaterial.New()
				shader.SetShader(outline)
				shader.SetShaderParameter("outline_width", 1.05)
				shader.SetShaderParameter("outline_color", Color.X11.White)
				if mat, ok := Object.As[BaseMaterial3D.Instance](source); ok {
					shader.SetShaderParameter("texture_albedo", mat.AsBaseMaterial3D().AlbedoTexture())
					shader.SetShaderParameter("has_texture", true)
				}
				mat := Resource.Duplicate(source)
				mat.SetNextPass(shader.AsMaterial())
				instance.SetSurfaceOverrideMaterial(i, mat)
			} else {
				instance.SetSurfaceOverrideMaterial(i, Material.Nil)
			}
		}
	}
	for _, child := range node.GetChildren() {
		Select(child, selected)
	}
}
