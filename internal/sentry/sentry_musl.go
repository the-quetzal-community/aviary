//go:build musl

// Package sentry statically links the sentry-godot GDExtension (rebuilt for musl/libc++)
// into the aviary binary on musl builds, then registers it at runtime from its in-binary
// entrypoint — no dlopen'd .so and no .gdextension file. graphics.gd's static-musl loader
// borrows the host's dynamic loader, so a foreign .so would be host-libc-bound; linking the
// extension in keeps everything inside the single self-contained binary.
//
// Other platforms (Windows/macOS/glibc) use the official .so addon (see fetch_sentry.sh);
// there this package is a no-op (see sentry_other.go).
//
// The libc++ archives live in ./lib (gitignored). Produce them with ./build_sentry_musl.sh.
package sentry

/*
// inproc backend (no crashpad): libsentry + the vendored libunwind for in-process
// stack unwinding, grouped for their mutual deps; then the curl/openssl/zlib transport.
#cgo LDFLAGS: -Wl,--start-group
#cgo LDFLAGS: ${SRCDIR}/lib/libsentry_godot.linux.release.x86_64.a
#cgo LDFLAGS: ${SRCDIR}/lib/libgodot-cpp.linux.template_release.x86_64.a
#cgo LDFLAGS: ${SRCDIR}/lib/libsentry.a
#cgo LDFLAGS: ${SRCDIR}/lib/libunwind.a
#cgo LDFLAGS: -Wl,--end-group
#cgo LDFLAGS: ${SRCDIR}/lib/libcurl.a ${SRCDIR}/lib/libssl.a ${SRCDIR}/lib/libcrypto.a ${SRCDIR}/lib/libz.a

// The GDExtension entry point, statically linked from the archives above.
extern void sentry_gdextension_init(void);

// The init function's own address. graphics.gd's ptrcall passes this by reference and
// Godot's load_extension_from_function dereferences once (*p_init_func.data), so the net
// effect is that this value is invoked directly — hence the function address itself, not
// the address of a variable holding it. Referencing the symbol here also forces the
// linker to pull the archive member in and keep it resolvable.
static void *sentry_init_func(void) { return (void *)&sentry_gdextension_init; }
*/
import "C"

import (
	"graphics.gd/classdb/GDExtensionManager"
)

// keep retains the archive member defining sentry_gdextension_init in the final binary
// regardless of how dead-code analysis views Register.
var keep = C.sentry_init_func()

// Linked reports whether the statically-linked sentry-godot extension is present.
func Linked() bool { return keep != nil }

// Register loads the statically-linked sentry-godot GDExtension into the running engine via
// its in-binary entrypoint, then starts the SDK with the build-injected DSN (instantiate).
// Call once the engine is up (after startup.LoadingScene). The "libgodot://" prefix is
// required by Godot's function loader; the rest is just the extension's unique identifier.
func Register() {
	GDExtensionManager.LoadExtensionFromFunction("libgodot://sentry", uintptr(C.sentry_init_func()))
	instantiate()
}
