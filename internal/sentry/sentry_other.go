//go:build !musl

// On non-musl builds the official sentry-godot .so addon is used (fetch_sentry.sh) and loads
// via its own .gdextension, so this package links nothing; Register just starts the SDK.
package sentry

// Linked reports whether the statically-linked sentry-godot extension is present
// (only on musl builds — see sentry_musl.go).
func Linked() bool { return false }

// Register starts both Sentry SDKs with the build-injected DSN: sentry-godot via instantiate
// (the .so addon is already loaded via its .gdextension; auto_init is off so we drive init
// here) and sentry-go via initGo (Go panics). No-op without the addon / without a DSN.
func Register() {
	release := releaseTag()
	instantiate(release)
	initGo(release)
}
