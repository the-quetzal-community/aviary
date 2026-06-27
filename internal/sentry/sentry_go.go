package sentry

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"time"

	sentrygo "github.com/getsentry/sentry-go"
	"graphics.gd/classdb/OS"
	"graphics.gd/startup"
)

// initGo starts the pure-Go Sentry SDK (sentry-go) and wires the two crash paths it adds on top
// of sentry-godot: (1) engine crashes (NOTIFICATION_CRASH) via startup.OnCrash, reported with the
// crash-time Go stack; (2) fatal Go panics via the crash-monitor sidecar (startCrashMonitor /
// debug.SetCrashOutput) — those unwind past the engine, so OnCrash never sees them.
//
// Shipped-game-only: skips the editor/export like instantiate (no "editor" feature), so it
// doesn't init or hook during `gd build`. No DSN ⇒ no-op. Uses the same build-injected DSN.
func initGo(release string) {
	if dsn == "" || OS.HasFeature("editor") {
		return
	}
	if err := sentrygo.Init(sentrygo.ClientOptions{
		Dsn:     dsn,
		Release: release,
		Debug:   OS.GetEnvironment("AVIARY_SENTRY_DEBUG") != "",
	}); err != nil {
		return
	}
	startup.OnCrash(func() {
		// An engine-level crash (NOTIFICATION_CRASH) — a native Godot/C++ crash, not a Go
		// panic (those unwind past the engine and are caught by startCrashMonitor instead).
		// There's no recovered value, but the current goroutine's stack still shows where Go
		// was when the engine went down, so dump it as context alongside the report.
		stack := debug.Stack()
		sentrygo.WithScope(func(scope *sentrygo.Scope) {
			scope.SetContext("go", sentrygo.Context{"stack": string(stack)})
			scope.SetLevel(sentrygo.LevelFatal)
			sentrygo.CaptureMessage("engine crash (NOTIFICATION_CRASH)")
		})
		// Flush so the event is sent even though the engine is about to take the process down.
		sentrygo.Flush(2 * time.Second)
	})
	startCrashMonitor(release)
}

// startCrashMonitor spawns the engine-free crash-monitor sidecar (shipped next to the binary
// as "aviary-crashmonitor") and points the Go runtime's fatal-crash output at it via a pipe.
// On a fatal Go panic — the kind that unwinds past graphics.gd's recovery to go_main and
// aborts — the runtime writes the full report to the pipe before dying, and the monitor (a
// separate, surviving process) submits it to Sentry with a real Go stacktrace. No-op if the
// sidecar isn't shipped. The DSN/release are passed via the environment, never written to disk.
func startCrashMonitor(release string) {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	name := "aviary-crashmonitor"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	monitor := filepath.Join(filepath.Dir(exe), name)
	if _, err := os.Stat(monitor); err != nil {
		return // sidecar not shipped — skip
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		return
	}
	cmd := exec.Command(monitor)
	cmd.Stdin = pr
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(),
		"AVIARY_SENTRY_DSN="+dsn,
		"AVIARY_SENTRY_RELEASE="+release,
	)
	if err := cmd.Start(); err != nil {
		pr.Close()
		pw.Close()
		return
	}
	pr.Close() // the monitor owns the read end now
	// pw is deliberately kept open: the runtime writes the crash report to it on a fatal
	// panic, and a clean process exit closes it (the monitor reads EOF and reports nothing).
	debug.SetCrashOutput(pw, debug.CrashOptions{})
}

// Recover reports the in-flight panic to sentry-go and stops it. Defer it at the top of
// goroutines you start (e.g. the resource thread), which graphics.gd's callback/main recovery
// doesn't cover. No-op if sentry-go wasn't initialized.
func Recover() {
	if r := recover(); r != nil {
		sentrygo.CurrentHub().Recover(r)
		sentrygo.Flush(2 * time.Second)
	}
}
