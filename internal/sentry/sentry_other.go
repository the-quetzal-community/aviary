//go:build !musl

// On non-musl builds the official sentry-godot .so addon is used (fetch_sentry.sh) and loads
// via its own .gdextension, so this package links nothing; Register just starts the SDK.
package sentry

// Linked reports whether the statically-linked sentry-godot extension is present
// (only on musl builds — see sentry_musl.go).
func Linked() bool { return false }

// Register starts the Sentry SDK with the build-injected DSN (instantiate). The .so addon is
// already loaded via its .gdextension; auto_init is off so we drive init here. No-op without
// the addon (no SentrySDK singleton) or without a DSN.
func Register() { instantiate() }
