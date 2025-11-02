package internal

import (
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
				outline := Resource.Load[Shader.Instance]("res://shader/outline.gdshader")
				shader := ShaderMaterial.New()
				shader.SetShader(outline)
				shader.SetShaderParameter("outline_width", 1.05)
				shader.SetShaderParameter("outline_color", Color.X11.White)
				mat := Resource.Duplicate(mesh.SurfaceGetMaterial(i))
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
