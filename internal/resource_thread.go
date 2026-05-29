package internal

import (
	"runtime"

	"graphics.gd/classdb/BaseMaterial3D"
	"graphics.gd/classdb/Material"
	"graphics.gd/classdb/MeshInstance3D"
	"graphics.gd/classdb/Resource"
	"graphics.gd/classdb/ResourceLoader"
	"graphics.gd/classdb/Texture2D"
	"graphics.gd/variant/Callable"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Path"
)

// Community library resources live in library.pck, fetched on demand over
// HTTP range requests by [CommunityResourceLoader]. Mesh geometry ships in
// preview.pck and is always local. Loading a resource that isn't
// downloaded yet therefore blocks for a network round trip — and doing
// that on Godot's main thread stalls the whole UI and, worst of all, the
// VR compositor, the first time a not-yet-downloaded design is selected.
//
// Godot allows resource loading from a single dedicated thread (what it
// forbids is concurrent loads from several threads). CommunityResourceLoader
// already documents that it assumes "a single dedicated resource loading
// thread", but its RecognizePath/download/load mutate maps with no locks —
// so the moment two threads load (or call ResourceLoader.Exists, which
// also routes through RecognizePath) concurrently, those maps race.
//
// This file funnels EVERY aviary resource load and existence check through
// one goroutine, restoring that single-thread invariant by construction
// (which is why CommunityResourceLoader needs no locks). Two modes:
//
//   - LoadAsync: fire-and-forget; the caller keeps rendering and an apply
//     callback runs on the main thread once the resource is ready. Used by
//     the hot preview/material path, so a not-yet-downloaded design appears
//     immediately (untextured) instead of freezing.
//   - LoadSync: blocks the caller until the resource is loaded. Used by the
//     startup / rare call sites that need the resource inline. The load
//     still happens on the loader thread (preserving the invariant); the
//     caller just waits for it.
//
// All Godot scene mutation stays on the main thread: the loader goroutine
// only performs the load itself and hands results back via Callable.Defer.

// resourceJobQueue carries type-erased load closures to the loader
// goroutine. Buffered so a burst of loads (a scene with many
// MaterialSharingMeshInstance3D surfaces) doesn't block the producer; if
// it fills, async sends run inline rather than dropping work (LoadAsync).
var resourceJobQueue = make(chan func(), 256)

// onLoaderThread is true only on the loader goroutine, so a LoadSync /
// ExistsSync issued from within a job (e.g. a Ready() that itself loads)
// runs inline instead of enqueuing to itself and deadlocking. Written and
// read only on the loader goroutine, so it needs no synchronisation.
var onLoaderThread bool

// loaderRunning gates the funnel. Until StartResourceThread runs there is
// no goroutine draining the queue, so LoadSync/LoadAsync/ExistsSync load
// inline on the caller. This covers the Godot-editor path (where the
// thread is never started) and any pre-startup preload — both single-
// threaded anyway. Set once, before the thread starts; read only after.
var loaderRunning bool

// StartResourceThread launches the dedicated loader goroutine. It exits
// when ShuttingDown is closed (same lifecycle hook the cloud saver uses).
// Call once at startup, before any scene that loads resources is added.
func StartResourceThread() {
	loaderRunning = true
	go func() {
		runtime.LockOSThread()
		onLoaderThread = true
		for {
			select {
			case <-ShuttingDown:
				return
			case job := <-resourceJobQueue:
				job()
			}
		}
	}()
}

// LoadSync loads the resource at path on the loader thread and returns it,
// blocking until ready. Safe from the main thread or any goroutine; if
// called from within the loader thread itself it loads inline to avoid
// self-deadlock.
func LoadSync[T Resource.Any, P string | Path.ToResource](path P) T {
	if onLoaderThread || !loaderRunning {
		return Resource.Load[T](path)
	}
	done := make(chan T, 1)
	resourceJobQueue <- func() { done <- Resource.Load[T](path) }
	return <-done
}

// LoadAsync loads the resource at path on the loader thread without
// blocking the caller. Once ready, apply runs on the main thread (via
// Callable.Defer) with the loaded resource, so apply may freely touch the
// scene graph. If the loader queue is momentarily full the load runs
// inline on the calling thread rather than being dropped.
func LoadAsync[T Resource.Any, P string | Path.ToResource](path P, apply func(T)) {
	if !loaderRunning {
		apply(Resource.Load[T](path))
		return
	}
	job := func() {
		res := Resource.Load[T](path)
		Callable.Defer(Callable.New(func() { apply(res) }))
	}
	select {
	case resourceJobQueue <- job:
	default:
		apply(Resource.Load[T](path))
	}
}

// ExistsSync reports whether a resource exists, routed through the loader
// thread because ResourceLoader.Exists consults
// CommunityResourceLoader.RecognizePath (which touches the loader's maps).
// Keeping it on the one thread preserves the single-loader invariant.
func ExistsSync(path string) bool {
	if onLoaderThread || !loaderRunning {
		return ResourceLoader.Exists(path, "")
	}
	done := make(chan bool, 1)
	resourceJobQueue <- func() { done <- ResourceLoader.Exists(path, "") }
	return <-done
}

// applyMeshMaterial assigns a freshly-loaded material to surface 0 of the
// mesh identified by id, honouring the same AO-override sharing cache as
// the synchronous path. Runs on the main thread (only ever called from a
// LoadAsync apply callback). The node may have been freed while the
// material downloaded (e.g. the preview was swept past), so it re-resolves
// and validates the instance first.
func applyMeshMaterial(id Object.ID, key sharingKey, overrideAO Texture2D.Instance, mat Material.Instance) {
	raw := id.Instance()
	if raw == Object.Nil {
		return
	}
	ms, ok := Object.As[MeshInstance3D.Instance](raw)
	if !ok {
		return
	}
	if mat == Material.Nil {
		return // load failed; leave the default surface in place.
	}
	if overrideAO != Texture2D.Nil {
		if entry, found := cacheAO[key]; found {
			entry.RC++
			cacheAO[key] = entry
			ms.Mesh().SurfaceSetMaterial(0, entry.Material)
			return
		}
		dup := Object.Leak(Resource.Duplicate(Object.To[BaseMaterial3D.Instance](mat)))
		dup.SetAoTexture(overrideAO)
		cacheAO[key] = sharingEntry{RC: 1, Material: dup.AsMaterial()}
		ms.Mesh().SurfaceSetMaterial(0, dup.AsMaterial())
		return
	}
	ms.Mesh().SurfaceSetMaterial(0, mat)
}
