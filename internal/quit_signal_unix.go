//go:build !windows

package internal

import (
	"os"
	"os/signal"
	"syscall"
)

// On Unix, listen for SIGUSR1 and flag a clean shutdown. Windows has no SIGUSR1,
// so quitRequested simply stays false there (see quit_signal.go).
func init() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGUSR1)
	go func() {
		for range ch {
			quitRequested.Store(true)
		}
	}()
}
