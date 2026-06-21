package internal

import (
	"graphics.gd/classdb"
	"graphics.gd/classdb/BaseMaterial3D"
	"graphics.gd/classdb/Decal"
	"graphics.gd/classdb/Engine"
	"graphics.gd/classdb/Material"
	"graphics.gd/classdb/MeshInstance3D"
	"graphics.gd/classdb/Resource"
	"graphics.gd/classdb/Texture2D"
	"graphics.gd/variant/Object"
)

type MaterialSharingMeshInstance3D struct {
	MeshInstance3D.Extension[MaterialSharingMeshInstance3D]
	classdb.Tool

	Identity string
	Material string

	OverrideAO Texture2D.Instance
}

type sharingKey struct {
	Identity string
	Material string
}

// cacheAO holds the AO-override-merged library prop materials, keyed by
// (Identity, Material), for the WHOLE session — NOT refcounted per node. An earlier
// version freed each material in OnFree once the last MeshInstance3D referencing it
// was freed; on a scene reload/join (replaceSceneTree QueueFrees the old client and
// builds the new one immediately) that ran the old nodes' frees INTERLEAVED with the
// new scene's rebuild, so a material was Object.Free'd while still bound to the
// SHARED mesh the reloaded scene reuses → everything that prop touched rendered
// magenta. Keeping the cache session-lifetime (like keptImports and the terrain /
// grass / foliage shared materials, which all free only at shutdown) means a reload
// reuses the live material instead of freeing and re-loading it. Bounded in practice
// by the library's distinct (Identity, Material) pairs, which recur across reloads.
var cacheAO = make(map[sharingKey]Material.Instance)

func init() {
	// Release the session-lifetime shared materials at shutdown (each was created
	// with Object.Leak, so the GC never reclaims them — see shutdown.go). Object.Free
	// only drops our ref; a material still bound to a live mesh survives until that
	// node is finalized during teardown.
	OnShutdown(func() {
		for key, mat := range cacheAO {
			Object.Free(mat)
			delete(cacheAO, key)
		}
	})
}

func (ms *MaterialSharingMeshInstance3D) Ready() {
	key := sharingKey{
		Identity: ms.Identity,
		Material: ms.Material,
	}
	// In the editor we keep loading synchronously: tool-script execution
	// and resource imports expect the material to be present immediately,
	// and there's no live UI to stall.
	if Engine.IsEditorHint() {
		ms.applySync(key)
		return
	}
	// Fast path: an AO-shared material that's already resolved is in
	// memory, so there's no network to wait on — apply it inline.
	if ms.OverrideAO != Texture2D.Nil {
		if mat, found := cacheAO[key]; found {
			ms.AsMeshInstance3D().Mesh().SurfaceSetMaterial(0, mat)
			return
		}
	}
	// Otherwise the material may live in library.pck and require an HTTP
	// fetch. Leave the surface on Godot's default material and load it on
	// the dedicated loader thread; applyMeshMaterial assigns it on the
	// main thread once ready (skipping if this node was freed meanwhile).
	id := Object.Instance(ms.AsObject()).ID()
	overrideAO := ms.OverrideAO
	LoadAsync(ms.Material, func(mat Material.Instance) {
		applyMeshMaterial(id, key, overrideAO, mat)
	})
}

// applySync is the original blocking load, kept for the editor path.
func (ms *MaterialSharingMeshInstance3D) applySync(key sharingKey) {
	if ms.OverrideAO != Texture2D.Nil {
		if mat, found := cacheAO[key]; found {
			ms.AsMeshInstance3D().Mesh().SurfaceSetMaterial(0, mat)
			return
		}
		material := Object.Leak(Resource.Duplicate(loadSafe[BaseMaterial3D.Instance](ms.Material)))
		material.SetAoTexture(ms.OverrideAO)
		cacheAO[key] = material.AsMaterial()
		ms.AsMeshInstance3D().Mesh().SurfaceSetMaterial(0, material.AsMaterial())
		return
	}
	ms.AsMeshInstance3D().Mesh().SurfaceSetMaterial(0, loadSafe[Material.Instance](ms.Material))
}

type MaterialSharingDecal struct {
	Decal.Extension[MaterialSharingDecal]
	classdb.Tool

	Material string
}

func (decal *MaterialSharingDecal) Ready() {
	if Engine.IsEditorHint() {
		decal.applyDecal(loadSafe[Material.Instance](decal.Material))
		return
	}
	// The decal's source material may live in library.pck; load it off
	// the main thread and bind the textures once ready, so a not-yet-
	// downloaded design doesn't stall the UI/VR compositor.
	id := Object.Instance(decal.AsObject()).ID()
	LoadAsync(decal.Material, func(mat Material.Instance) {
		raw := id.Instance()
		if raw == Object.Nil {
			return
		}
		d, ok := Object.As[*MaterialSharingDecal](raw)
		if !ok {
			return
		}
		d.applyDecal(mat)
	})
}

// applyDecal binds the source material's textures onto the decal. Runs on
// the main thread. mat may be Nil if the load failed, in which case the
// decal simply renders nothing.
func (decal *MaterialSharingDecal) applyDecal(mat Material.Instance) {
	if mat == Material.Nil {
		return
	}
	base := Object.To[BaseMaterial3D.Instance](mat)
	decal.AsDecal().
		SetTextureAlbedo(base.AlbedoTexture()).
		SetTextureNormal(base.NormalTexture()).
		SetTextureOrm(base.RoughnessTexture())
}
