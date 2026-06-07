package internal

import (
	"sync/atomic"

	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/SceneTree"
)

// quitRequested is set by a SIGUSR1 handler (on platforms that have it) so aviary
// can be driven to a clean shutdown externally — the same path as closing the
// window, so RunShutdownCleanups and the full engine teardown all run. Used for
// automated leak/teardown testing (e.g. `kill -USR1 <pid>` while running under a
// debugger), and harmless otherwise.
var quitRequested atomic.Bool

// quitIfRequested asks the SceneTree to quit if a SIGUSR1 has been received, and
// reports whether a quit is in progress. Must run on the main thread (SceneTree.Quit
// is not safe to call from the signal goroutine), so it is polled from Client.Process.
func quitIfRequested(peer Node.Instance) bool {
	if !quitRequested.Load() {
		return false
	}
	SceneTree.Get(peer).Quit()
	return true
}
