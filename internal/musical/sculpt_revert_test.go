package musical

import (
	"bytes"
	"encoding/binary"
	"testing"

	"graphics.gd/variant/Vector3"
)

// TestSculptRevertRoundTrip checks that the Revert flag and a stamped Timing
// survive a full encode/decode unchanged, and — crucially for backwards
// compatibility — that a Commit-only Sculpt (what every pre-Revert writer
// produced) still decodes with Revert=false.
func TestSculptRevertRoundTrip(t *testing.T) {
	cases := []Sculpt{
		// Commit-only: byte-identical to a pre-Revert sculpt; must decode Revert=false.
		{Author: 7, Timing: 123456789, Commit: true},
		// A height stroke with both bools set.
		{Author: 7, Timing: 123456789, Commit: true, Revert: true},
		// A river revert carrying its routing fields + identity.
		{Author: 3, Target: Vector3.XYZ{X: 1, Y: 2, Z: 3}, Radius: 4, Amount: -5, Slider: "river", Timing: 42, Commit: true, Revert: true},
		// Bool independence: Revert without Commit.
		{Author: 1, Revert: true},
		// Water level: no Timing, no Revert.
		{Author: 9, Slider: "editing/water_level", Amount: 2, Commit: true},
	}
	for i, in := range cases {
		buf, err := encode(in)
		if err != nil {
			t.Fatalf("case %d: encode: %v", i, err)
		}
		out, err := decode(bytes.NewReader(buf))
		if err != nil {
			t.Fatalf("case %d: decode: %v", i, err)
		}
		got, ok := out.(Sculpt)
		if !ok {
			t.Fatalf("case %d: decoded %T, want Sculpt", i, out)
		}
		if got != in {
			t.Errorf("case %d: round-trip mismatch\n in: %+v\nout: %+v", i, in, got)
		}
	}
}

// TestSculptBoolBits pins the bit assignment: Commit must keep bit 14 (the first
// bool) now that Revert (the second bool) claims bit 13. If Revert had been
// inserted before Commit, Commit would shift to bit 13 and every old .mus3 file
// would decode its Commit flag as a phantom Revert.
func TestSculptBoolBits(t *testing.T) {
	layoutOf := func(s Sculpt) uint16 {
		buf, err := encode(s)
		if err != nil {
			t.Fatal(err)
		}
		return binary.LittleEndian.Uint16(buf[1:3])
	}
	commitOnly := layoutOf(Sculpt{Commit: true})
	if commitOnly&(1<<14) == 0 {
		t.Errorf("Commit should occupy bit 14, layout=%#x", commitOnly)
	}
	if commitOnly&(1<<13) != 0 {
		t.Errorf("Revert bit 13 unexpectedly set on a Commit-only sculpt, layout=%#x", commitOnly)
	}
	both := layoutOf(Sculpt{Commit: true, Revert: true})
	if both&(1<<14) == 0 || both&(1<<13) == 0 {
		t.Errorf("expected Commit(bit14)+Revert(bit13), layout=%#x", both)
	}
}
