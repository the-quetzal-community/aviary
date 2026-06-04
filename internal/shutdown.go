package internal

// Shutdown cleanups release resources the app intentionally keeps alive for the
// whole session (via Object.Leak / lifetime caches such as the shared-material
// cache). Without this, those references are still held when the engine runs its
// leak check at exit and report as a "whole scene leaked" wall of errors.
//
// They run once, on the main thread, right after the main loop ends and BEFORE the
// engine tears the scene down (see main.go). That ordering is what makes them safe:
// Object.Free only decrements the refcount, so a resource still bound to a live node
// is NOT destroyed here — it just loses our extra cache ref and is freed for real when
// the node is finalized during teardown. Cleanups also clear their cache, so the
// per-node OnFree paths become no-ops afterwards (no double free).
var shutdownCleanups []func()

// OnShutdown registers a cleanup to run at engine shutdown. Register from an init()
// in the file that owns the cache, so the cache and its release live together.
func OnShutdown(f func()) { shutdownCleanups = append(shutdownCleanups, f) }

// RunShutdownCleanups runs every registered cleanup. Called once from main after the
// main loop ends. No-op on paths that never registered any (e.g. the editor host).
func RunShutdownCleanups() {
	for _, f := range shutdownCleanups {
		f()
	}
}
