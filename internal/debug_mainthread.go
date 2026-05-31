package internal

import (
	"fmt"
	"os"
	"runtime"

	"graphics.gd/classdb/OS"
)

// assertMainThread logs (with a Go stack trace) when called off Godot's main
// thread. Scene-graph mutations (set_position, set_visible, …) are main-thread
// only, so this pinpoints an off-thread caller. DEBUG: remove once the
// offending path is found and wrapped in Callable.Defer.
func assertMainThread(where string) {
	if OS.GetThreadCallerId() == OS.GetMainThreadId() {
		return
	}
	var buf [8192]byte
	n := runtime.Stack(buf[:], false)
	fmt.Fprintf(os.Stderr, "\n[OFF-MAIN-THREAD] %s (caller=%d main=%d)\n%s\n",
		where, OS.GetThreadCallerId(), OS.GetMainThreadId(), buf[:n])
}
