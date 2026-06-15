package internal

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"os"
	"testing"

	// The internal package links against the Godot engine; the test binary
	// needs the engine entry point or it fails to link ("undefined symbol:
	// main"). A blank startup import supplies it. Run with: gd test ./internal/
	_ "graphics.gd/startup"

	"the.quetzal.community/aviary/internal/ice/signalling"
	"the.quetzal.community/aviary/internal/musical"
)

// TestOpenCloudStripsCachedPartHeaders pins the fix for the "unknown entry type
// 116" load crash. A save part on disk is musical.MagicHeader followed by a
// back-to-back stream of encoded records. When a cloud work spans multiple
// parts they are concatenated for replay, and the decoder consumes exactly ONE
// header up front — so every part reader must strip its own before its body
// joins the stream. cloudReader's "already cached and fresh" branch used to
// hand the raw file (header included) to the concatenated stream, so the
// decoder read the leading 't' of "the.quetzal..." (0x74 = 116) as an
// entry-type tag and aborted the whole load.
//
// Here we stage a local device part plus a second cloud-listed part that is
// already cached on disk and stat-matches the cloud listing (so the cached
// branch is taken, not a re-download) and assert the assembled stream carries
// exactly one header — i.e. the cached part's header was stripped.
func TestOpenCloudStripsCachedPartHeaders(t *testing.T) {
	dir := t.TempDir()
	UserDataDir = dir
	UserState.Device = "device-A"

	work := musical.WorkID{0xAA, 0xBB, 0xCC}
	name := base64.RawURLEncoding.EncodeToString(work[:])
	saveDir := dir + "/saves/" + name
	if err := os.MkdirAll(saveDir, 0o777); err != nil {
		t.Fatal(err)
	}

	hdr := []byte(musical.MagicHeader)
	// Distinct, header-free bodies. The bytes need not be valid records: the
	// bug is purely byte-stream header handling, and exact equality below is a
	// strictly stronger guarantee than "decodes without type 116".
	bodyA := []byte("LOCAL-DEVICE-PART-BODY-A")
	bodyB := []byte("CACHED-CLOUD-PART-BODY-B")

	withHeader := func(body []byte) []byte { return append(append([]byte{}, hdr...), body...) }

	// Local device part (the writable part OpenCloud opens by UserState.Device).
	if err := os.WriteFile(saveDir+"/device-A.mus3", withHeader(bodyA), 0o666); err != nil {
		t.Fatal(err)
	}
	// Second part, authored on another device, already cached on disk.
	const partB = "device-B"
	pathB := saveDir + "/" + partB + ".mus3"
	if err := os.WriteFile(pathB, withHeader(bodyB), 0o666); err != nil {
		t.Fatal(err)
	}
	stB, err := os.Stat(pathB)
	if err != nil {
		t.Fatal(err)
	}

	// The cloud lists part B with a size+time that match the cached file, so
	// cloudReader takes the cached branch instead of re-downloading; LookupSave
	// must therefore never be called.
	api := signalling.API{
		CloudParts: func(context.Context, signalling.WorkID) (map[signalling.PartID]signalling.Part, error) {
			return map[signalling.PartID]signalling.Part{
				signalling.PartID(partB): {Size: stB.Size(), Time: stB.ModTime()},
			}, nil
		},
		LookupSave: func(context.Context, signalling.WorkID, signalling.PartID) (io.ReadCloser, error) {
			t.Error("LookupSave called: part B is cached and fresh, the cached branch should serve it")
			return nil, io.EOF
		},
	}

	f, err := OpenCloud(api, work, true)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	got, err := io.ReadAll(f) // reading to EOF also exercises cloudReader's EOF close (was a nil-panic)
	if err != nil {
		t.Fatalf("read assembled stream: %v", err)
	}

	want := bytes.Join([][]byte{hdr, bodyA, bodyB}, nil)
	if !bytes.Equal(got, want) {
		t.Fatalf("assembled stream mismatch (cached part header leaked?):\n got=%q\nwant=%q", got, want)
	}
	// Belt-and-braces: the header must appear exactly once, at offset 0.
	if i := bytes.Index(got[len(hdr):], hdr); i != -1 {
		t.Fatalf("second MagicHeader leaked into the stream at offset %d", i+len(hdr))
	}
}
