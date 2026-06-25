package sentry

import (
	"graphics.gd/classdb/Engine"
	"graphics.gd/classdb/OS"
	"graphics.gd/classdb/ProjectSettings"
	"graphics.gd/variant/Object"
)

// Sentry severity levels (mirrors sentry-godot's Level enum).
const (
	LevelDebug   = 0
	LevelInfo    = 1
	LevelWarning = 2
	LevelError   = 3
	LevelFatal   = 4
)

// Available reports whether the Sentry SDK singleton is present (static-linked on musl,
// the .so addon elsewhere). Registered when the extension loads, before init.
func Available() bool { return Engine.HasSingleton("SentrySDK") }

// instantiate applies the build-injected DSN to the project settings and manually starts
// the Sentry SDK. We disable auto-init (sentry/options/auto_init=false in project.godot) and
// drive init from here so the DSN — which is baked into the Go binary at build time, not into
// the export pck (the static-musl Godot template doesn't read the embedded override.cfg) — is
// in place before sentry_init reads it. No DSN ⇒ Sentry stays off. Skips F5 play-in-editor
// sessions to match skip_auto_init_on_editor_play. Called by Register once the extension/addon
// is loaded and the SentrySDK singleton exists.
func instantiate() {
	if dsn == "" {
		return
	}
	// Only the shipped release game (no "editor" feature). Skips the interactive editor,
	// play-from-editor, AND the headless export-editor that runs during `gd build` — which
	// otherwise inits Sentry mid-build (sends spurious events, breaks the export).
	if OS.HasFeature("editor") {
		return
	}
	ProjectSettings.SetSetting("sentry/options/dsn", dsn)
	if Engine.HasSingleton("SentrySDK") {
		Object.Call(Engine.GetSingleton("SentrySDK"), "init")
	}
}

// CaptureMessage sends a message event to Sentry at the given level and returns nothing
// if the SDK isn't available. Handy to verify the DSN/transport end to end:
//
//	sentry.CaptureMessage("sentry self-test", sentry.LevelError)
//
// Note Sentry must already be initialized — errors before sentry.Register() (e.g. the
// OpenXR boot warning) happen too early to be captured.
func CaptureMessage(message string, level int) {
	if Available() {
		Object.Call(Engine.GetSingleton("SentrySDK"), "capture_message", message, level)
	}
}
