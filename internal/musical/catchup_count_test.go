package musical

import (
	"bytes"
	"io/fs"
	"testing"
	"time"
)

// memFile is a read-only fs.File over a byte buffer (no io.Writer, so newStorage
// treats it as a non-writable, already-populated part).
type memFile struct {
	r    *bytes.Reader
	size int64
}

func (m *memFile) Stat() (fs.FileInfo, error) { return memInfo{m.size}, nil }
func (m *memFile) Read(p []byte) (int, error) { return m.r.Read(p) }
func (m *memFile) Close() error               { return nil }

type memInfo struct{ size int64 }

func (i memInfo) Name() string       { return "part.mus3" }
func (i memInfo) Size() int64        { return i.size }
func (i memInfo) Mode() fs.FileMode  { return 0444 }
func (i memInfo) ModTime() time.Time { return time.Time{} }
func (i memInfo) IsDir() bool        { return false }
func (i memInfo) Sys() any           { return nil }

// TestCatchupCountsEveryRecord guards the join catch-up: newStorage must report
// the true number of records on disk, not the Commit-filtered tally. A record
// that decodes Commit=false (as every legacy-format record does, via the bool-
// bit collision) is still a persisted mutation a joining peer must receive, so
// it must be counted. If this regresses, joiners silently lose every record
// after the first `committed` ones — the scene loads partially (heights but no
// scenery/textures). See [server.run] and [newStorage].
func TestCatchupCountsEveryRecord(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteString(MagicHeader)
	records := []encodable{
		Sculpt{Author: 1, Amount: 1, Timing: 10, Commit: true}, // committed: counted by both
		Sculpt{Author: 1, Amount: 2, Timing: 20},               // Commit=false on disk (legacy proxy)
		Import{Design: Design{Author: 1, Number: 1}, Import: "res://x.glb"},
	}
	for _, rec := range records {
		b, err := encode(rec)
		if err != nil {
			t.Fatal(err)
		}
		buf.Write(b)
	}
	data := buf.Bytes()

	var tracker counter
	_, loaded, err := newStorage(
		&memFile{r: bytes.NewReader(data), size: int64(len(data))},
		0, Compose(&tracker, Stubbed{}),
	)
	if err != nil {
		t.Fatal(err)
	}

	if loaded != len(records) {
		t.Fatalf("catch-up limit = %d, want %d (every on-disk record)", loaded, len(records))
	}
	// The old limit was tracker.value, which skips the Commit=false record — the
	// exact undercount that truncated joiners. Confirm the gap exists so this
	// test is meaningful, and that the fix closes it.
	if uint64(loaded) <= tracker.value {
		t.Fatalf("expected decoded count %d > committed count %d", loaded, tracker.value)
	}
}
