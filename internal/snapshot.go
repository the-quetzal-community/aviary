package internal

import (
	"encoding/base64"
	"encoding/gob"
	"fmt"
	"os"

	"the.quetzal.community/aviary/internal/musical"
)

// snapshot.go is a private, local rasterised cache of the terrain a client has
// already folded, so a second load of a world skips re-folding the strokes it
// already processed. It is NOT a musical mutation and is never shared: each
// client writes its own .snap to user://snapshots keyed by WorkID, and the
// .mus3 stroke log remains the only source of truth.
//
// A snapshot is a PREFIX of the canonical (Timing, Author) stroke order at a
// cutoff Timing: the baked per-tile height / river / texture grids that result
// from folding every active stroke with Timing <= Cutoff. On load we validate
// that the active strokes <= Cutoff are byte-for-byte the same set we baked
// (StrokeHash + StrokeCount); if they are, we restore the grids and fold only
// the strokes with Timing > Cutoff. Any mismatch (an edit, an out-of-order peer
// stroke, a revert of an old stroke, a format change) discards the snapshot and
// falls back to a full replay — so a stale snapshot can never corrupt a world.
//
// Opt-in while it stabilises: set AVIARY_SNAPSHOT=1.

var snapshotEnabled = os.Getenv("AVIARY_SNAPSHOT") == "1"

const terrainSnapshotVersion = 1

type terrainSnapshot struct {
	Version     int
	Cutoff      musical.Timing
	StrokeHash  uint64
	StrokeCount int
	// LayerDesigns is the paint Design at each shared-texture-array layer
	// (index 0 is the untextured base). Tile texture cells store layer indices,
	// and that order depends on upload order, so on restore we remap each cell's
	// index through LayerDesigns -> the current session's layer for that Design.
	LayerDesigns []musical.Design
	Tiles        []tileSnapshot
}

type tileSnapshot struct {
	X, Z          int
	Size          int
	Revealed      bool
	Heights       []float32
	GroundHeights []float32
	RiverDepth    []float32
	WaterFlowX    []float32
	WaterFlowZ    []float32
	Textures      []float32 // layer indices in this snapshot's LayerDesigns space
}

func snapshotPath(work musical.WorkID) string {
	name := base64.RawURLEncoding.EncodeToString(work[:])
	return UserDataDir + "/snapshots/" + name + ".snap"
}

// writeTerrainSnapshot atomically writes snap for the given world (temp file +
// rename), so a crash mid-write never leaves a torn snapshot that would later
// be read as valid.
func writeTerrainSnapshot(work musical.WorkID, snap *terrainSnapshot) error {
	if err := os.MkdirAll(UserDataDir+"/snapshots", 0777); err != nil {
		return err
	}
	path := snapshotPath(work)
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if err := gob.NewEncoder(f).Encode(snap); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// readTerrainSnapshot loads the snapshot for a world, or returns an error if it
// is missing / unreadable / a different format version. The caller still
// validates the stroke set before trusting it.
func readTerrainSnapshot(work musical.WorkID) (*terrainSnapshot, error) {
	f, err := os.Open(snapshotPath(work))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var snap terrainSnapshot
	if err := gob.NewDecoder(f).Decode(&snap); err != nil {
		return nil, err
	}
	if snap.Version != terrainSnapshotVersion {
		return nil, fmt.Errorf("snapshot version %d != %d", snap.Version, terrainSnapshotVersion)
	}
	return &snap, nil
}

// strokeHashMix maps one (Author, Timing) identity to a 64-bit value. Combined
// with XOR across the active stroke set it gives an order-independent digest, so
// the set can be compared regardless of which order tiles/strokes are visited.
func strokeHashMix(s musical.Sculpt) uint64 {
	return uint64(s.Timing)*0x9E3779B97F4A7C15 ^ (uint64(s.Author)+1)*0xC2B2AE3D27D4EB4F
}
