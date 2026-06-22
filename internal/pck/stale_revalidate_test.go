package pck

import (
	"bytes"
	"crypto/md5"
	"io"
	"os"
	"testing"
)

// writePCK builds a real pck on disk containing the given files (path->content)
// with correct per-file md5 hashes, and returns the open file handle.
func writePCK(t *testing.T, files map[string][]byte) *os.File {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "cloud-*.pck")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	if err := Create(f); err != nil {
		t.Fatalf("Create: %v", err)
	}
	index := make(map[string]File, len(files))
	for p, content := range files {
		index[p] = File{Size: int64(len(content)), Hash: md5.Sum(content)}
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("Seek: %v", err)
	}
	if err := Append(f, index); err != nil {
		t.Fatalf("Append: %v", err)
	}
	// Fill the reserved slots with the actual bytes.
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("Seek: %v", err)
	}
	idx, err := Index(f)
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	for p, content := range files {
		slot := idx[p]
		if _, err := f.Seek(slot.Seek, io.SeekStart); err != nil {
			t.Fatalf("Seek slot: %v", err)
		}
		if _, err := f.Write(content); err != nil {
			t.Fatalf("Write slot: %v", err)
		}
		if err := slot.SetMissing(false, f); err != nil {
			t.Fatalf("SetMissing: %v", err)
		}
	}
	return f
}

// readPCK returns the bytes of path from an open pck, or fails if it is
// missing/absent.
func readPCK(t *testing.T, f *os.File, path string) []byte {
	t.Helper()
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("Seek: %v", err)
	}
	idx, err := Index(f)
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	slot, ok := idx[path]
	if !ok {
		t.Fatalf("%q not in pck", path)
	}
	got, err := slot.Bytes(f)
	if err != nil {
		t.Fatalf("Bytes(%q): %v", path, err)
	}
	return got
}

// fill copies path's bytes from cloud into the matching (reserved) local slot,
// simulating an on-demand download via Remap.
func fill(t *testing.T, local, cloud *os.File, path string) {
	t.Helper()
	li := indexOf(t, local)
	ci := indexOf(t, cloud)
	if err := Remap(local, cloud, li[path], ci[path]); err != nil {
		t.Fatalf("Remap %q: %v", path, err)
	}
}

func indexOf(t *testing.T, f *os.File) map[string]File {
	t.Helper()
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("Seek: %v", err)
	}
	idx, err := Index(f)
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	return idx
}

// TestStaleWhileRevalidate verifies the core of the background-invalidation
// design at the pck layer:
//   - AppendMissing keeps a changed-but-present file pointing at its existing
//     (stale) bytes, so a mount can serve it without a blocking download;
//   - a genuinely new file is reserved missing for on-demand fetch;
//   - Promote atomically swaps a background-refreshed file to fresh bytes.
func TestStaleWhileRevalidate(t *testing.T) {
	a1 := []byte("alpha-v1")
	b1 := []byte("bravo-v1")
	c1 := []byte("charlie-v1")
	cloudV1 := writePCK(t, map[string][]byte{"a": a1, "b": b1, "c": c1})
	defer cloudV1.Close()

	// Build the local mirror as it would be after a prior session: reserve all
	// v1 files then fill them (on-demand download).
	local, err := os.CreateTemp(t.TempDir(), "local-*.pck")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	defer local.Close()
	if err := Create(local); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := local.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("Seek: %v", err)
	}
	if _, err := AppendMissing(local, indexOf(t, cloudV1)); err != nil {
		t.Fatalf("AppendMissing v1: %v", err)
	}
	for _, p := range []string{"a", "b", "c"} {
		fill(t, local, cloudV1, p)
	}
	for p, want := range map[string][]byte{"a": a1, "b": b1, "c": c1} {
		if got := readPCK(t, local, p); !bytes.Equal(got, want) {
			t.Fatalf("after v1 fill %q = %q, want %q", p, got, want)
		}
	}

	// Cloud is updated: b changes, d is added, a and c unchanged.
	b2 := []byte("bravo-v2-is-longer")
	d1 := []byte("delta-v1")
	cloudV2 := writePCK(t, map[string][]byte{"a": a1, "b": b2, "c": c1, "d": d1})
	defer cloudV2.Close()
	cv2 := indexOf(t, cloudV2)

	// Reconcile (the load-time step). No downloads happen here.
	if _, err := local.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("Seek: %v", err)
	}
	if _, err := AppendMissing(local, cv2); err != nil {
		t.Fatalf("AppendMissing v2: %v", err)
	}
	li := indexOf(t, local)

	// b must still be present (non-missing) and serve its STALE v1 bytes.
	if li["b"].Missing() {
		t.Fatalf("b was re-slotted missing; it should serve stale bytes")
	}
	if got := readPCK(t, local, "b"); !bytes.Equal(got, b1) {
		t.Fatalf("b should serve stale v1 %q, got %q", b1, got)
	}
	// d (new) must be reserved missing for on-demand fetch.
	if d, ok := li["d"]; !ok || !d.Missing() {
		t.Fatalf("d should be reserved missing, got %+v ok=%v", d, ok)
	}
	// a and c untouched.
	if got := readPCK(t, local, "a"); !bytes.Equal(got, a1) {
		t.Fatalf("a changed unexpectedly: %q", got)
	}

	// Determine the stale set the loader would compute.
	var stale []string
	for path, cf := range cv2 {
		if lf, ok := li[path]; ok && !lf.Missing() && lf.Hash != cf.Hash {
			stale = append(stale, path)
		}
	}
	if len(stale) != 1 || stale[0] != "b" {
		t.Fatalf("stale set = %v, want [b]", stale)
	}

	// Background refresh: append b's fresh bytes to a new slot (no directory
	// change yet — the mount keeps serving stale b).
	end, err := local.Seek(0, io.SeekEnd)
	if err != nil {
		t.Fatalf("Seek end: %v", err)
	}
	bSlot := File{Seek: end, Size: cv2["b"].Size, Hash: cv2["b"].Hash}
	if err := Remap(local, cloudV2, bSlot, cv2["b"]); err != nil {
		t.Fatalf("Remap b fresh: %v", err)
	}
	// Still stale until promotion.
	if got := readPCK(t, local, "b"); !bytes.Equal(got, b1) {
		t.Fatalf("b should still be stale before Promote, got %q", got)
	}

	// Persist the refresh through the manifest (as the loader's sidecar does)
	// and read it back, so the promote step exercises real serialization.
	var sidecar bytes.Buffer
	if err := WriteManifest(&sidecar, map[string]File{"b": bSlot}); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
	recorded, err := ReadManifest(&sidecar)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}

	// Promote (next-launch step): atomic swap to fresh bytes.
	if _, err := local.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("Seek: %v", err)
	}
	if err := Promote(local, recorded); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if got := readPCK(t, local, "b"); !bytes.Equal(got, b2) {
		t.Fatalf("b should be fresh v2 %q after Promote, got %q", b2, got)
	}
	// Everything else is intact and the directory is consistent.
	if got := readPCK(t, local, "a"); !bytes.Equal(got, a1) {
		t.Fatalf("a corrupted after Promote: %q", got)
	}
	if got := readPCK(t, local, "c"); !bytes.Equal(got, c1) {
		t.Fatalf("c corrupted after Promote: %q", got)
	}
	if li := indexOf(t, local); !li["d"].Missing() {
		t.Fatalf("d should still be missing after Promote")
	}
}

// TestIncrementalReuse mirrors the preview.pck updater: reconciling a new cloud
// version against the previous (backup) version reserves only the changed/new
// entries for download, reusing the unchanged ones in place.
func TestIncrementalReuse(t *testing.T) {
	a1 := []byte("alpha-v1")
	b1 := []byte("bravo-v1")
	c1 := []byte("charlie-v1")
	backup := writePCK(t, map[string][]byte{"a": a1, "b": b1, "c": c1})
	defer backup.Close()

	b2 := []byte("bravo-v2-longer")
	d1 := []byte("delta-v1")
	cloud := writePCK(t, map[string][]byte{"a": a1, "b": b2, "c": c1, "d": d1})
	defer cloud.Close()
	cloudIdx := indexOf(t, cloud)

	// Reconcile the backup against the new cloud directory (the downloader step).
	if _, err := backup.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("Seek: %v", err)
	}
	if err := Append(backup, cloudIdx); err != nil {
		t.Fatalf("Append: %v", err)
	}
	local := indexOf(t, backup)

	// Only b (changed) and d (new) should need downloading; a and c are reused.
	missing := map[string]bool{}
	for p, lf := range local {
		if lf.Missing() {
			missing[p] = true
		}
	}
	if len(missing) != 2 || !missing["b"] || !missing["d"] {
		t.Fatalf("missing set = %v, want {b,d}", missing)
	}
	// Reused entries are immediately readable without any download.
	if got := readPCK(t, backup, "a"); !bytes.Equal(got, a1) {
		t.Fatalf("a not reused: %q", got)
	}
	if got := readPCK(t, backup, "c"); !bytes.Equal(got, c1) {
		t.Fatalf("c not reused: %q", got)
	}

	// Fetch only the delta from the cloud into the reserved slots.
	for p := range missing {
		if err := Remap(backup, cloud, local[p], cloudIdx[p]); err != nil {
			t.Fatalf("Remap %q: %v", p, err)
		}
	}

	// The reconstructed pck now matches the new cloud version exactly.
	for p, want := range map[string][]byte{"a": a1, "b": b2, "c": c1, "d": d1} {
		if got := readPCK(t, backup, p); !bytes.Equal(got, want) {
			t.Fatalf("after delta fetch %q = %q, want %q", p, got, want)
		}
	}
}

// TestManifestRoundTrip checks the refresh sidecar serialization is exact.
func TestManifestRoundTrip(t *testing.T) {
	in := map[string]File{
		"library/a/foo.tres":              {Seek: 10, Size: 20, Hash: [16]byte{1, 2, 3}},
		"library/with spaces/bar baz.scn": {Seek: 1 << 40, Size: 1 << 20, Hash: [16]byte{0xff, 0xaa}},
		"":                                {Seek: 0, Size: 0},
	}
	var buf bytes.Buffer
	if err := WriteManifest(&buf, in); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
	out, err := ReadManifest(&buf)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if len(out) != len(in) {
		t.Fatalf("round-trip count %d, want %d", len(out), len(in))
	}
	for p, want := range in {
		got, ok := out[p]
		if !ok {
			t.Fatalf("missing %q after round-trip", p)
		}
		if got.Seek != want.Seek || got.Size != want.Size || got.Hash != want.Hash {
			t.Fatalf("%q = %+v, want %+v", p, got, want)
		}
	}
}
