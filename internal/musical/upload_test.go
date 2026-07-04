package musical

import (
	"bytes"
	"testing"
)

// TestUploadEncodeDecodeRoundTrip verifies the []byte payload support added for
// Upload survives the layout-mask encode/decode unchanged, including binary
// bytes and a payload larger than the uint16 string cap (the []byte length
// prefix is uint32).
func TestUploadEncodeDecodeRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name   string
		bundle []byte
	}{
		{"small-binary", []byte("creation\x00\x01\x02\xff bundle")},
		{"empty-ish", []byte{0x00}},
		{"large", bytes.Repeat([]byte{0xAB}, 200_000)}, // > 64KB string cap
	} {
		t.Run(tc.name, func(t *testing.T) {
			orig := Upload{Design: Design{Author: 7, Number: 42}, Bundle: tc.bundle}
			buf, err := encode(orig)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			got, err := decode(bytes.NewReader(buf))
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			up, ok := got.(Upload)
			if !ok {
				t.Fatalf("decoded %T, want Upload", got)
			}
			if up.Design != orig.Design {
				t.Errorf("Design = %v, want %v", up.Design, orig.Design)
			}
			if !bytes.Equal(up.Bundle, orig.Bundle) {
				t.Errorf("Bundle len = %d, want %d (equal=%v)", len(up.Bundle), len(orig.Bundle), bytes.Equal(up.Bundle, orig.Bundle))
			}
		})
	}
}

// TestMarshalEntriesRoundTrip verifies a mixed sequence of entries — the shape
// of a user-design creation bundle (Sculpts for the skeleton, Import+Change per
// part) — survives MarshalEntries/UnmarshalEntries with order and values intact.
func TestMarshalEntriesRoundTrip(t *testing.T) {
	in := []any{
		Sculpt{Slider: "bone/0", Amount: 0.5},
		Sculpt{Slider: "bone/1", Amount: 0.25},
		Sculpt{Slider: "leg/0/attach", Amount: 3},
		Sculpt{Slider: "weight/chonk", Amount: 0.8},
		Import{Design: Design{Number: 1}, Import: "res://library/everything/critter/muzzle.glb"},
		Change{Design: Design{Number: 1}, Editor: "critter"},
	}
	data, err := MarshalEntries(in)
	if err != nil {
		t.Fatalf("MarshalEntries: %v", err)
	}
	out, err := UnmarshalEntries(data)
	if err != nil {
		t.Fatalf("UnmarshalEntries: %v", err)
	}
	if len(out) != len(in) {
		t.Fatalf("got %d entries, want %d", len(out), len(in))
	}
	// Spot-check each decoded entry against the input by type.
	for i := range in {
		switch want := in[i].(type) {
		case Sculpt:
			got, ok := out[i].(Sculpt)
			if !ok || got.Slider != want.Slider || got.Amount != want.Amount {
				t.Errorf("entry %d: got %#v, want %#v", i, out[i], want)
			}
		case Import:
			got, ok := out[i].(Import)
			if !ok || got != want {
				t.Errorf("entry %d: got %#v, want %#v", i, out[i], want)
			}
		case Change:
			got, ok := out[i].(Change)
			if !ok || got.Design != want.Design || got.Editor != want.Editor {
				t.Errorf("entry %d: got %#v, want %#v", i, out[i], want)
			}
		}
	}
}

// TestChangeStillRoundTripsAfterSliceCase guards that adding the reflect.Slice
// case to encode/decode didn't disturb a pre-existing entry type with no slice
// field (backwards compatibility of the wire format).
func TestChangeStillRoundTripsAfterSliceCase(t *testing.T) {
	orig := Change{Author: 3, Editor: "scenery", Commit: true, Remove: false}
	orig.Entity = Entity{Author: 3, Number: 9}
	orig.Design = Design{Author: 3, Number: 5}
	buf, err := encode(orig)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := decode(bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	con, ok := got.(Change)
	if !ok {
		t.Fatalf("decoded %T, want Change", got)
	}
	if con.Author != orig.Author || con.Editor != orig.Editor || con.Entity != orig.Entity || con.Design != orig.Design || con.Commit != orig.Commit {
		t.Errorf("roundtrip mismatch:\n got  %+v\n want %+v", con, orig)
	}
}
