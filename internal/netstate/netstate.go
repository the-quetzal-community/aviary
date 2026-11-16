// Package netstate tracks whether the internet is reachable, so expected
// no-connectivity failures can flip the UI's offline indicators instead of
// being reported as errors (every Engine.Raise becomes a Sentry event).
//
// The state starts online (optimistic): the first failed dial marks it
// offline, and any later successful response — or a [Watch] probe — brings it
// back. It is safe for concurrent use; UI code polls [Generation] each frame
// and only reacts when it changes.
package netstate

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

var (
	offline    atomic.Bool
	generation atomic.Uint64
)

// Online reports whether the internet is currently believed reachable.
func Online() bool { return !offline.Load() }

// Generation increments on every online/offline transition, so a poller can
// cheaply tell whether anything changed since it last looked.
func Generation() uint64 { return generation.Load() }

// MarkOffline records that the internet is unreachable.
func MarkOffline() {
	if offline.CompareAndSwap(false, true) {
		generation.Add(1)
	}
}

// MarkOnline records that a network round trip just succeeded.
func MarkOnline() {
	if offline.CompareAndSwap(true, false) {
		generation.Add(1)
	}
}

// Check marks the state offline and returns true if err is an expected
// no-connectivity failure (see [IsOffline]); otherwise it returns false and
// leaves the state alone, so the caller can report err as a genuine failure.
func Check(err error) bool {
	if !IsOffline(err) {
		return false
	}
	MarkOffline()
	return true
}

// offlineMessages are lower-case fragments of connectivity failures from
// clients that flatten errors to strings (velopack's Rust updater, runtime.link
// signalling), matched only when the error chain holds no typed net error.
var offlineMessages = []string{
	"i/o timeout",
	"no such host",
	"connection refused",
	"connection reset",
	"network is unreachable",
	"host is unreachable",
	"no route to host",
	"tls handshake timeout",
	"dial tcp",
	"dns error",
	"error sending request",
	"timed out",
}

// IsOffline reports whether err means the network could not be reached (DNS
// failure, dial/connect/TLS timeout, refused or reset connection) rather than
// the server answering with an error.
func IsOffline(err error) bool {
	if err == nil {
		return false
	}
	var dnsErr *net.DNSError
	var opErr *net.OpError
	if errors.As(err, &dnsErr) || errors.As(err, &opErr) {
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ENETUNREACH) ||
		errors.Is(err, syscall.EHOSTUNREACH) {
		return true
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Timeout() {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, frag := range offlineMessages {
		if strings.Contains(msg, frag) {
			return true
		}
	}
	return false
}

// Watch runs probe every interval while the state is offline, marking it
// online as soon as a probe succeeds. It returns when stop is closed.
func Watch(stop <-chan struct{}, interval time.Duration, probe func() error) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			if Online() {
				continue
			}
			if err := probe(); err == nil {
				MarkOnline()
			}
		}
	}
}
