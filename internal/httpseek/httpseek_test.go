package httpseek

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

// makeContent returns n deterministic bytes for read-back comparison.
func makeContent(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*31 + 7)
	}
	return b
}

// parseStart extracts START from an open-ended "bytes=START-" Range header.
func parseStart(rng string) int64 {
	rng = strings.TrimPrefix(rng, "bytes=")
	if i := strings.IndexByte(rng, '-'); i >= 0 {
		rng = rng[:i]
	}
	n, _ := strconv.ParseInt(rng, 10, 64)
	return n
}

// rangeServer serves content with well-behaved range support: a probe GET
// returns the full body with Content-Length, and a ranged GET returns the whole
// remainder from the requested offset with a matching Content-Length. rangeCount,
// if non-nil, counts ranged requests.
func rangeServer(content []byte, rangeCount *atomic.Int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Accept-Ranges", "bytes")
		rng := r.Header.Get("Range")
		if rng == "" {
			w.Header().Set("Content-Length", strconv.Itoa(len(content)))
			w.WriteHeader(http.StatusOK)
			w.Write(content)
			return
		}
		if rangeCount != nil {
			rangeCount.Add(1)
		}
		start := parseStart(rng)
		if start < 0 || start > int64(len(content)) {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		slice := content[start:]
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(content)-1, len(content)))
		w.Header().Set("Content-Length", strconv.Itoa(len(slice)))
		w.WriteHeader(http.StatusPartialContent)
		w.Write(slice)
	})
}

// chunkingServer mimics a proxy that ends each ranged response early: it claims
// (via Content-Range) to span to the end of the resource but delivers only a
// chunk of bytes over a chunked (unknown-length) body, so the client sees a
// clean EOF before contentLength and must reopen to fetch the rest.
func chunkingServer(content []byte, chunk int, rangeCount *atomic.Int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Accept-Ranges", "bytes")
		rng := r.Header.Get("Range")
		if rng == "" {
			w.Header().Set("Content-Length", strconv.Itoa(len(content)))
			w.WriteHeader(http.StatusOK)
			w.Write(content)
			return
		}
		if rangeCount != nil {
			rangeCount.Add(1)
		}
		start := parseStart(rng)
		end := start + int64(chunk)
		if end > int64(len(content)) {
			end = int64(len(content))
		}
		// No Content-Length: flushing before the body is complete forces a
		// chunked transfer, so the client's ContentLength is -1 and the
		// length-mismatch guard is skipped — exactly the proxy case we model.
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(content)-1, len(content)))
		w.WriteHeader(http.StatusPartialContent)
		w.Write(content[start:end])
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	})
}

// closeTracker flags when a response body is closed.
type closeTracker struct {
	io.ReadCloser
	closed *atomic.Bool
}

func (c closeTracker) Close() error {
	c.closed.Store(true)
	return c.ReadCloser.Close()
}

type trackingTransport struct {
	base   http.RoundTripper
	closed *atomic.Bool
}

func (t trackingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	resp.Body = closeTracker{ReadCloser: resp.Body, closed: t.closed}
	return resp, nil
}

// withTrackingClient swaps the package client for one that flags body closes,
// restoring the original when the test ends.
func withTrackingClient(t *testing.T) *atomic.Bool {
	t.Helper()
	closed := new(atomic.Bool)
	old := client
	client = &http.Client{Transport: trackingTransport{base: http.DefaultTransport, closed: closed}}
	t.Cleanup(func() { client = old })
	return closed
}

// TestNewClosesBodyOnError covers fix #1: every error return from New must close
// the response body so the connection is not leaked.
func TestNewClosesBodyOnError(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"non-200", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, "nope")
		}},
		{"missing-accept-ranges", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			io.WriteString(w, "data")
		}},
		{"non-positive-content-length", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Accept-Ranges", "bytes")
			w.WriteHeader(http.StatusOK) // empty body -> Content-Length: 0
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(tc.handler)
			defer ts.Close()
			closed := withTrackingClient(t)

			u, err := New(ts.URL)
			if err == nil {
				u.Close()
				t.Fatalf("expected error from New, got nil")
			}
			if !closed.Load() {
				t.Fatalf("response body was not closed on error path")
			}
		})
	}
}

// TestReadAllFromProbe reads the whole resource straight off the body retained
// by New — no range request should be needed.
func TestReadAllFromProbe(t *testing.T) {
	content := makeContent(4096)
	var rc atomic.Int64
	ts := httptest.NewServer(rangeServer(content, &rc))
	defer ts.Close()

	u, err := New(ts.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer u.Close()

	got, err := io.ReadAll(u)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("content mismatch: got %d bytes", len(got))
	}
	if n := rc.Load(); n != 0 {
		t.Fatalf("expected 0 range requests reading from the probe body, got %d", n)
	}
}

// TestSeekWhence checks the offset arithmetic for all three whence modes.
func TestSeekWhence(t *testing.T) {
	content := makeContent(1000)
	ts := httptest.NewServer(rangeServer(content, nil))
	defer ts.Close()

	u, err := New(ts.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer u.Close()

	steps := []struct {
		offset int64
		whence int
		want   int64
	}{
		{0, io.SeekStart, 0},
		{50, io.SeekStart, 50},
		{10, io.SeekCurrent, 60},
		{0, io.SeekEnd, 1000},
		{-20, io.SeekEnd, 980},
	}
	for _, s := range steps {
		got, err := u.Seek(s.offset, s.whence)
		if err != nil {
			t.Fatalf("Seek(%d, %d): %v", s.offset, s.whence, err)
		}
		if got != s.want {
			t.Fatalf("Seek(%d, %d) = %d, want %d", s.offset, s.whence, got, s.want)
		}
	}
}

// TestRangeReadAfterSeek seeks past the discard threshold (dropping the probe
// body) and reads the tail back via a fresh range request.
func TestRangeReadAfterSeek(t *testing.T) {
	content := makeContent(200_000)
	var rc atomic.Int64
	ts := httptest.NewServer(rangeServer(content, &rc))
	defer ts.Close()

	u, err := New(ts.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer u.Close()

	const off = 199_900
	if _, err := u.Seek(off, io.SeekStart); err != nil {
		t.Fatalf("Seek: %v", err)
	}
	got, err := io.ReadAll(u)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, content[off:]) {
		t.Fatalf("tail mismatch: got %d bytes", len(got))
	}
	if rc.Load() == 0 {
		t.Fatalf("expected a range request after a large seek, got none")
	}
}

// TestSmallForwardSeekReusesConnection covers fix #7: a forward seek within the
// discard threshold consumes bytes from the open body instead of issuing a new
// range request.
func TestSmallForwardSeekReusesConnection(t *testing.T) {
	content := makeContent(4096)
	var rc atomic.Int64
	ts := httptest.NewServer(rangeServer(content, &rc))
	defer ts.Close()

	u, err := New(ts.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer u.Close()

	const skip = 100 // < seekDiscardThreshold
	if skip >= seekDiscardThreshold {
		t.Fatalf("test precondition: skip must be below the discard threshold")
	}
	if _, err := u.Seek(skip, io.SeekStart); err != nil {
		t.Fatalf("Seek: %v", err)
	}
	got, err := io.ReadAll(u)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, content[skip:]) {
		t.Fatalf("content mismatch after small seek: got %d bytes", len(got))
	}
	if n := rc.Load(); n != 0 {
		t.Fatalf("small forward seek should reuse the open body, but %d range requests were made", n)
	}
}

// TestPrematureEOFResumes covers fix #2: when a ranged response ends before the
// logical end of the resource, the reader transparently reopens and resumes, so
// the caller still observes the complete content.
func TestPrematureEOFResumes(t *testing.T) {
	content := makeContent(200_000)
	const chunk = 8192
	var rc atomic.Int64
	ts := httptest.NewServer(chunkingServer(content, chunk, &rc))
	defer ts.Close()

	u, err := New(ts.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer u.Close()

	// Seek past the discard threshold so reads go through the (chunking) range
	// path rather than the full probe body.
	const off = 100_000
	if off <= seekDiscardThreshold {
		t.Fatalf("test precondition: offset must exceed the discard threshold")
	}
	if _, err := u.Seek(off, io.SeekStart); err != nil {
		t.Fatalf("Seek: %v", err)
	}
	got, err := io.ReadAll(u)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, content[off:]) {
		t.Fatalf("content mismatch: got %d bytes, want %d", len(got), len(content)-off)
	}
	if n := rc.Load(); n < 2 {
		t.Fatalf("expected multiple range requests due to early EOF, got %d", n)
	}
}

// TestClose verifies Close is idempotent-safe (errors on second call) and that
// operations fail once closed.
func TestClose(t *testing.T) {
	content := makeContent(128)
	ts := httptest.NewServer(rangeServer(content, nil))
	defer ts.Close()

	u, err := New(ts.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := u.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := u.Close(); err == nil {
		t.Fatalf("second Close should error")
	}
	if _, err := u.Read(make([]byte, 1)); err == nil {
		t.Fatalf("Read after Close should error")
	}
	if _, err := u.Seek(0, io.SeekStart); err == nil {
		t.Fatalf("Seek after Close should error")
	}
}

// TestSeekErrors checks the out-of-range and invalid-whence guards.
func TestSeekErrors(t *testing.T) {
	content := makeContent(100)
	ts := httptest.NewServer(rangeServer(content, nil))
	defer ts.Close()

	u, err := New(ts.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer u.Close()

	if _, err := u.Seek(-1, io.SeekStart); err == nil {
		t.Fatalf("negative seek should error")
	}
	if _, err := u.Seek(1, io.SeekEnd); err == nil {
		t.Fatalf("seek beyond content length should error")
	}
	if _, err := u.Seek(0, 99); err == nil {
		t.Fatalf("invalid whence should error")
	}
}
