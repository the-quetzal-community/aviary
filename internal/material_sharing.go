package internal

import (
	"graphics.gd/classdb"
	"graphics.gd/classdb/BaseMaterial3D"
	"graphics.gd/classdb/Decal"
	"graphics.gd/classdb/ImporterMeshInstance3D"
	"graphics.gd/classdb/Material"
	"graphics.gd/classdb/MeshInstance3D"
	"graphics.gd/classdb/Resource"
	"graphics.gd/classdb/Texture2D"
	"graphics.gd/variant/Object"
)

type MaterialSharingMeshInstance3D struct {
	MeshInstance3D.Extension[MaterialSharingMeshInstance3D]
	classdb.Tool

	imported bool

	Identity string
	Material string

	OverrideAO Texture2D.Instance
}

type sharingKey struct {
	Identity string
	Material string
}

type sharingEntry struct {
	RC       int
	Material Material.Instance
}

var cacheAO = make(map[sharingKey]sharingEntry)

func NewMaterialSharingMeshInstance3D(replace ImporterMeshInstance3D.Instance) *MaterialSharingMeshInstance3D {
	clone := new(MaterialSharingMeshInstance3D)
	clone.imported = true
	clone.AsMeshInstance3D().SetMesh(replace.AsImporterMeshInstance3D().Mesh().GetMesh().AsMesh())
	replace.AsNode().ReplaceBy(clone.AsNode())
	replace.AsNode().QueueFree()
	return clone
}

func (ms *MaterialSharingMeshInstance3D) Ready() {
	if ms.imported {
		return
	}
	key := sharingKey{
		Identity: ms.Identity,
		Material: ms.Material,
	}
	if ms.OverrideAO != Texture2D.Nil {
		entry, found := cacheAO[key]
		if found {
			entry.RC++
			cacheAO[key] = entry
			ms.AsMeshInstance3D().Mesh().SurfaceSetMaterial(0, entry.Material)
			return
		}
		var material = Object.Leak(Resource.Duplicate(Resource.Load[BaseMaterial3D.Instance](ms.Material)))
		material.SetAoTexture(ms.OverrideAO)
		cacheAO[key] = sharingEntry{
			RC:       1,
			Material: material.AsMaterial(),
		}
		ms.AsMeshInstance3D().Mesh().SurfaceSetMaterial(0, material.AsMaterial())
		return
	}
	ms.AsMeshInstance3D().Mesh().SurfaceSetMaterial(0, Resource.Load[Material.Instance](ms.Material))
}

func (ms *MaterialSharingMeshInstance3D) OnFree() {
	key := sharingKey{
		Identity: ms.Identity,
		Material: ms.Material,
	}
	if entry, found := cacheAO[key]; found {
		entry.RC--
		if entry.RC <= 0 {
			Object.Free(entry.Material)
			delete(cacheAO, key)
		}
	}
}

type MaterialSharingDecal struct {
	Decal.Extension[MaterialSharingDecal]
	classdb.Tool

	Material string
}

func NewMaterialSharingDecal(replace ImporterMeshInstance3D.Instance) *MaterialSharingDecal {
	clone := new(MaterialSharingDecal)
	replace.AsNode().ReplaceBy(clone.AsNode())
	replace.AsNode().QueueFree()
	return clone
}

func (decal *MaterialSharingDecal) Ready() {
	mat := Object.To[BaseMaterial3D.Instance](Resource.Load[Material.Instance](decal.Material))
	decal.AsDecal().SetTextureAlbedo(mat.AlbedoTexture())
	decal.AsDecal().SetTextureNormal(mat.NormalTexture())
	decal.AsDecal().SetTextureOrm(mat.RoughnessTexture())
}
