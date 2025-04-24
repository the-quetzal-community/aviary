package internal

import (
	"strings"

	"graphics.gd/classdb"
	"graphics.gd/classdb/BaseMaterial3D"
	"graphics.gd/classdb/Decal"
	"graphics.gd/classdb/GLTFDocumentExtension"
	"graphics.gd/classdb/GLTFState"
	"graphics.gd/classdb/ImporterMesh"
	"graphics.gd/classdb/ImporterMeshInstance3D"
	"graphics.gd/classdb/Mesh"
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/Resource"
	"graphics.gd/classdb/Shader"
	"graphics.gd/classdb/ShaderMaterial"
	"graphics.gd/variant/AABB"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Packed"
	"graphics.gd/variant/Vector3"
)

type ModelLoader struct {
	classdb.Extension[ModelLoader, GLTFDocumentExtension.Instance] `gd:"AviaryModelLoader"`
	classdb.Tool
}

func (loader ModelLoader) ImportPost(state GLTFState.Instance, root Node.Instance) error {
	path := state.BasePath()
	if strings.Contains(path, "foliage") || strings.Contains(path, "mineral") { // we want the leaves to be transparent.
		for _, mat := range state.Materials() {
			if mat, ok := Object.As[BaseMaterial3D.Instance](mat); ok {
				mat.SetTransparency(BaseMaterial3D.TransparencyAlphaScissor)
				mat.SetCullMode(BaseMaterial3D.CullDisabled)
				if !strings.Contains(mat.AsResource().ResourceName(), "waterfall") { // we want the water to move.
					mat.SetTextureRepeat(false)
				}
			}
		}
	}
	for i, node := range state.Nodes() {
		imported, isImporterMeshInstance3D := Object.As[ImporterMeshInstance3D.Instance](state.GetSceneNode(i))
		if isImporterMeshInstance3D {
			mesh := imported.Mesh()
			if mat, ok := Object.As[BaseMaterial3D.Instance](mesh.GetSurfaceMaterial(0)); ok {
				if mat.AsResource().ResourceName() == "waterfall.png" {
					var material = ShaderMaterial.New()
					material.SetShader(Resource.Load[Shader.Instance]("res://shader/waterfall.gdshader"))
					material.SetShaderParameter("albedo_texture", mat.AlbedoTexture())
					mesh.SetSurfaceMaterial(0, material.AsMaterial())
				}
			}
		}
		if node.OriginalName() == "Decal" { // we want decals to be decals.
			node := state.GetSceneNode(i)
			var decal = Decal.New()
			if isImporterMeshInstance3D {
				mesh := imported.Mesh()
				if mat, ok := Object.As[BaseMaterial3D.Instance](mesh.GetSurfaceMaterial(0)); ok {
					mat.SetTextureRepeat(false)
					decal.SetTextureAlbedo(mat.AlbedoTexture())
					decal.SetTextureNormal(mat.NormalTexture())
					decal.SetTextureOrm(mat.OrmTexture())
					var aabb AABB.PositionSize
					var zero = true
					for surface := range mesh.GetSurfaceCount() {
						var arrays = ImporterMesh.Advanced(mesh).GetSurfaceArrays(int64(surface))
						var vertices = arrays.Index(int(Mesh.ArrayVertex)).Interface().(Packed.Array[Vector3.XYZ])
						for _, vertex := range vertices.Iter() {
							if zero {
								aabb.Position = vertex
								aabb.Size = Vector3.Zero
								zero = false
							} else {
								aabb = AABB.ExpandTo(vertex, aabb)
							}
						}
					}
					scale := imported.AsNode3D().Scale()
					decal.SetSize(Vector3.New(Float.Abs(aabb.Size.X*scale.X), 0.1, Float.Abs(aabb.Size.Z*scale.Z)))
				}
			}
			Object.To[Node3D.Instance](node).SetVisible(false)
			root.AddChild(decal.AsNode())
			decal.AsNode().SetOwner(root)
		}
	}
	return nil
}
