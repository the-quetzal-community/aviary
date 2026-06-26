// Command crashmonitor is aviary's Go-panic crash monitor: a tiny, engine-free sidecar that
// aviary spawns at startup with the Go runtime's crash output (debug.SetCrashOutput) piped to
// its stdin. On a fatal Go panic the runtime writes the full report here *before* dying; this
// process — separate, so it survives the crash — decodes it and submits it to Sentry with a
// real Go stack trace. That's something neither sentry-godot (native crashes only) nor
// in-process sentry-go (recovered panics only) can do for a fatal Go panic, and unlike
// crashpad it's pure Go so it links fully static (CGO_ENABLED=0), runs on glibc and musl.
//
// Protocol: aviary holds the pipe's write end via SetCrashOutput. A clean exit closes it with
// no data (we read EOF, report nothing); a fatal panic writes the report then closes it.
// DSN/release arrive via the environment (never on disk) from the parent.
package main

import (
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	sentry "github.com/getsentry/sentry-go"
)

func main() {
	// Block until the parent closes the pipe: EOF with no data on a clean exit, or the crash
	// report followed by EOF on a fatal panic. We only initialize Sentry if there's a crash.
	report, err := io.ReadAll(os.Stdin)
	if err != nil || len(report) == 0 {
		return
	}
	dsn := os.Getenv("AVIARY_SENTRY_DSN")
	if dsn == "" {
		return
	}
	if err := sentry.Init(sentry.ClientOptions{
		Dsn:     dsn,
		Release: os.Getenv("AVIARY_SENTRY_RELEASE"),
		Debug:   os.Getenv("AVIARY_SENTRY_DEBUG") != "",
	}); err != nil {
		return
	}
	sentry.CaptureEvent(buildEvent(string(report)))
	sentry.Flush(10 * time.Second)
}

// buildEvent decodes a Go runtime crash report into a Sentry event: the panic message becomes
// the exception value, the faulting goroutine's frames become the stacktrace, and the whole
// raw report is attached for completeness.
func buildEvent(report string) *sentry.Event {
	ev := sentry.NewEvent()
	ev.Level = sentry.LevelFatal

	typ, value := "panic", "fatal Go panic"
	for _, l := range strings.Split(report, "\n") {
		if v, ok := strings.CutPrefix(l, "panic: "); ok {
			value = v
			break
		}
		if v, ok := strings.CutPrefix(l, "fatal error: "); ok {
			typ, value = "fatal error", v
			break
		}
	}

	ev.Exception = []sentry.Exception{{
		Type:       typ,
		Value:      value,
		Stacktrace: parseGoroutine(report),
	}}
	ev.Contexts = map[string]sentry.Context{"go_crash": {"report": report}}
	return ev
}

// parseGoroutine extracts the first goroutine block (the one that crashed) into a Sentry
// stacktrace. Go prints frames newest-first as "<func>(<args>)" / "\t<file>:<line> +<offset>"
// pairs; Sentry wants them oldest-first.
func parseGoroutine(report string) *sentry.Stacktrace {
	lines := strings.Split(report, "\n")

	i := 0
	for ; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "goroutine ") && strings.HasSuffix(lines[i], ":") {
			i++
			break
		}
	}

	var frames []sentry.Frame
	for ; i+1 < len(lines); i += 2 {
		fn := lines[i]
		if fn == "" || strings.HasPrefix(fn, "goroutine ") || !strings.HasPrefix(lines[i+1], "\t") {
			break
		}
		if p := strings.LastIndexByte(fn, '('); p > 0 { // drop "(args)"
			fn = fn[:p]
		}
		loc := strings.TrimSpace(lines[i+1])
		if sp := strings.IndexByte(loc, ' '); sp > 0 { // drop " +0xNN"
			loc = loc[:sp]
		}
		file, lineno := loc, 0
		if c := strings.LastIndexByte(loc, ':'); c > 0 {
			file = loc[:c]
			lineno, _ = strconv.Atoi(loc[c+1:])
		}
		frames = append(frames, sentry.Frame{Function: fn, Filename: file, Lineno: lineno})
	}

	for l, r := 0, len(frames)-1; l < r; l, r = l+1, r-1 { // Sentry expects oldest-first
		frames[l], frames[r] = frames[r], frames[l]
	}
	return &sentry.Stacktrace{Frames: frames}
}
