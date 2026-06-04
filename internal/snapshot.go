package internal

import (
	"encoding/base64"
	"encoding/gob"
	"fmt"
	"math"
	"os"

	"the.quetzal.community/aviary/internal/critter"
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

// --- Critter snapshot ---------------------------------------------------
//
// The critter editor bakes thousands of bone/leg/weight sculpts (a single
// bone drag historically recorded one Sculpt per axis per frame) into one
// small state: the bone chain, the legs, and the macro-weight map. Unlike
// terrain there is no cheap "record vs expensive fold" split — applying a
// critter sculpt directly mutates the shape — so the snapshot caches the
// fully-folded state and lets a reload skip applying the buffered sculpts
// entirely. Same contract as the terrain snapshot: a private, local,
// per-WorkID cache that is never shared and is fail-safe (any stroke-set
// mismatch falls back to a full replay), gated by AVIARY_SNAPSHOT=1.

const critterSnapshotVersion = 1

type critterSnapshot struct {
	Version int
	// StrokeHash + StrokeCount identify the exact sorted body-shape sculpt
	// buffer that was folded to produce this state. On reload we re-sort the
	// buffer, recompute these, and only trust the snapshot on an exact match.
	StrokeHash  uint64
	StrokeCount int
	Bones       []critter.Bone
	Legs        []critter.Leg
	Weights     map[string]float32
}

func critterSnapshotPath(work musical.WorkID) string {
	name := base64.RawURLEncoding.EncodeToString(work[:])
	return UserDataDir + "/snapshots/" + name + ".critter.snap"
}

func writeCritterSnapshot(work musical.WorkID, snap *critterSnapshot) error {
	if err := os.MkdirAll(UserDataDir+"/snapshots", 0777); err != nil {
		return err
	}
	path := critterSnapshotPath(work)
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

func readCritterSnapshot(work musical.WorkID) (*critterSnapshot, error) {
	f, err := os.Open(critterSnapshotPath(work))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var snap critterSnapshot
	if err := gob.NewDecoder(f).Decode(&snap); err != nil {
		return nil, err
	}
	if snap.Version != critterSnapshotVersion {
		return nil, fmt.Errorf("critter snapshot version %d != %d", snap.Version, critterSnapshotVersion)
	}
	return &snap, nil
}

// hashCritterBuffer digests a sorted body-shape sculpt buffer order-
// DEPENDENTLY (FNV-1a over each sculpt's full identity, not just
// Author/Timing). Critter emits several sculpts that share one (Author,
// Timing) — e.g. a bone move records bone/N/y and bone/N/z together — so
// the order-independent XOR digest used for terrain would let those cancel
// out; folding the full field set in canonical order avoids the collision.
// The buffer MUST already be sorted (sculptOrder) so the digest is stable
// across loads regardless of how the .mus3 device parts were concatenated.
func hashCritterBuffer(buf []musical.Sculpt) (uint64, int) {
	const (
		offset = uint64(1469598103934665603)
		prime  = uint64(1099511628211)
	)
	h := offset
	mix := func(v uint64) {
		h ^= v
		h *= prime
	}
	mixStr := func(s string) {
		for i := 0; i < len(s); i++ {
			mix(uint64(s[i]))
		}
		mix(0xFF) // terminator so "ab"+"c" != "a"+"bc"
	}
	for _, s := range buf {
		mix(uint64(s.Timing))
		mix(uint64(s.Author))
		mixStr(s.Slider)
		mix(uint64(math.Float32bits(float32(s.Amount))))
		mix(uint64(math.Float32bits(float32(s.Target.X))))
		mix(uint64(math.Float32bits(float32(s.Target.Y))))
		mix(uint64(math.Float32bits(float32(s.Target.Z))))
	}
	return h, len(buf)
}

// strokeHashMix maps one (Author, Timing) identity to a 64-bit value. Combined
// with XOR across the active stroke set it gives an order-independent digest, so
// the set can be compared regardless of which order tiles/strokes are visited.
func strokeHashMix(s musical.Sculpt) uint64 {
	return uint64(s.Timing)*0x9E3779B97F4A7C15 ^ (uint64(s.Author)+1)*0xC2B2AE3D27D4EB4F
}
