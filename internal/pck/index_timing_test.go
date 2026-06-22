package pck

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"the.quetzal.community/aviary/internal/httpseek"
)

// These tests measure the cost of reading a library.pck *directory* (the
// "header" / file index), which lives at the very end of the pck and is the
// fixed per-startup cost paid by CommunityResourceLoader.load via
// pck.Index(cloudReader). The real cloud directory is ~0.94 MB over ~10k
// entries (measured 2026-06: dir_offset 2.343 GB of a 2.344 GB file).
//
// Run with:
//
//	go test ./internal/pck/ -run TestIndexTiming -v
//	AVIARY_PCK_URL=https://vpk.quetzal.community/library.pck \
//	  go test ./internal/pck/ -run TestIndexRemote -v -timeout 180s

// realisticName returns a path roughly matching library asset path lengths so
// the synthetic directory's byte size tracks the real one (~93 bytes/entry).
func realisticName(i int) string {
	return fmt.Sprintf("library/wildfire_games/foliage/asset_group_%06d_variant.tres", i)
}

// buildSyntheticPCK writes a temp pck with n directory entries. One sentinel
// entry carries gap bytes of (sparse) file data so the directory lands gap
// bytes past the header — mirroring the real layout where the directory sits
// at the end of a multi-GB file, forcing pck.Index to do a large seek + a
// fresh range request rather than streaming straight off the probe body.
// Returns the full file bytes (the gap reads back as zeros) and the dir_offset.
func buildSyntheticPCK(t *testing.T, n int, gap int64) ([]byte, int64) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "synthetic-*.pck")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	defer f.Close()
	if err := Create(f); err != nil {
		t.Fatalf("Create: %v", err)
	}
	files := make(map[string]File, n+1)
	// Sentinel first so it sorts to the front of the data region; its size is
	// the gap that pushes the directory to the end.
	files["library/__gap__.bin"] = File{Size: gap, Hash: [16]byte{0xff}}
	for i := 0; i < n; i++ {
		var h [16]byte
		h[0], h[1], h[2], h[3] = byte(i), byte(i>>8), byte(i>>16), byte(i>>24)
		files[realisticName(i)] = File{Size: 0, Hash: h}
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("Seek: %v", err)
	}
	if err := Append(f, files); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("Seek: %v", err)
	}
	content, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	// dir_offset is the int64 at byte 32 (see Create/Index).
	var dirOffset int64
	for i := 0; i < 8; i++ {
		dirOffset |= int64(content[32+i]) << (8 * i)
	}
	return content, dirOffset
}

// rangeServeBytes serves content with byte-range support, counting ranged GETs.
func rangeServeBytes(content []byte, rangeCount *atomic.Int64) http.Handler {
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
		s := strings.TrimPrefix(rng, "bytes=")
		if i := strings.IndexByte(s, '-'); i >= 0 {
			s = s[:i]
		}
		start, _ := strconv.ParseInt(s, 10, 64)
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

func TestIndexTiming(t *testing.T) {
	const n = 10_000           // ~ real library.pck entry count
	const gap = int64(1) << 20 // 1 MiB, enough to force a seek + range request

	content, dirOffset := buildSyntheticPCK(t, n, gap)
	dirSize := int64(len(content)) - dirOffset
	t.Logf("synthetic pck: %d entries, file %d bytes, dir_offset %d, directory %d bytes (%.2f MB)",
		n+1, len(content), dirOffset, dirSize, float64(dirSize)/1e6)

	// (1) Pure parse cost: read the directory straight from memory. Isolates
	// the binary.Read / map-build CPU with zero IO overhead.
	{
		start := time.Now()
		idx, err := Index(bytes.NewReader(content))
		dur := time.Since(start)
		if err != nil {
			t.Fatalf("Index(memory): %v", err)
		}
		if len(idx) != n+1 {
			t.Fatalf("Index(memory): got %d entries, want %d", len(idx), n+1)
		}
		t.Logf("(1) parse-from-memory:      %v", dur)
	}

	// (2) Over HTTP loopback via httpseek — the faithful production path:
	// probe, large seek to the directory, one range request, stream + parse.
	var rc atomic.Int64
	ts := httptest.NewServer(rangeServeBytes(content, &rc))
	defer ts.Close()
	{
		start := time.Now()
		u, err := httpseek.New(ts.URL)
		if err != nil {
			t.Fatalf("httpseek.New: %v", err)
		}
		defer u.Close()
		idx, err := Index(u)
		dur := time.Since(start)
		if err != nil {
			t.Fatalf("Index(httpseek): %v", err)
		}
		if len(idx) != n+1 {
			t.Fatalf("Index(httpseek): got %d entries, want %d", len(idx), n+1)
		}
		t.Logf("(2) over-http-loopback:     %v  (%d range request(s))", dur, rc.Load())
		if rc.Load() == 0 {
			t.Fatalf("expected a range request for the end-of-file directory, got none")
		}
	}
}

// TestIndexRemote measures the real-world cost against an actual hosted pck.
// Skipped unless AVIARY_PCK_URL is set so it never runs in normal CI. This is
// read-only (range GETs) and harmless to the served object.
func TestIndexRemote(t *testing.T) {
	url := os.Getenv("AVIARY_PCK_URL")
	if url == "" {
		t.Skip("set AVIARY_PCK_URL to measure a real remote pck directory read")
	}
	start := time.Now()
	u, err := httpseek.New(url)
	if err != nil {
		t.Fatalf("httpseek.New(%s): %v", url, err)
	}
	defer u.Close()
	connected := time.Since(start)

	idxStart := time.Now()
	idx, err := Index(u)
	if err != nil {
		t.Fatalf("Index(%s): %v", url, err)
	}
	idxDur := time.Since(idxStart)
	t.Logf("remote %s", url)
	t.Logf("  httpseek.New (probe GET):  %v", connected)
	t.Logf("  Index (seek+range+parse):  %v", idxDur)
	t.Logf("  total:                     %v", time.Since(start))
	t.Logf("  entries:                   %d", len(idx))
}
