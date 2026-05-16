package citizen

import (
	"strings"
	"testing"
)

func TestParseTarget_HappyPath(t *testing.T) {
	src := `# This is a target file for MakeHuman
# Copyright (C) 2020 Data Collection AB — CC0
#
42 0.1 -0.2 0.3
99 .5 .6 .7
`
	tgt, err := ParseTarget("sample", strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if tgt.Name != "sample" {
		t.Errorf("Name = %q, want %q", tgt.Name, "sample")
	}
	if len(tgt.Deltas) != 2 {
		t.Fatalf("got %d deltas, want 2", len(tgt.Deltas))
	}
	if got, want := tgt.Deltas[0], (Delta{Index: 42, Offset: Vec3{0.1, -0.2, 0.3}}); got != want {
		t.Errorf("delta[0] = %+v, want %+v", got, want)
	}
	if got, want := tgt.Deltas[1], (Delta{Index: 99, Offset: Vec3{0.5, 0.6, 0.7}}); got != want {
		t.Errorf("delta[1] = %+v, want %+v", got, want)
	}
}

func TestParseTarget_SkipsBlankAndComments(t *testing.T) {
	src := `
# comment

   # indented comment is not skipped — TrimSpace runs first
1 0 0 0

3 0 0 0
`
	tgt, err := ParseTarget("blanks", strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(tgt.Deltas) != 2 {
		t.Fatalf("got %d deltas, want 2", len(tgt.Deltas))
	}
	if tgt.Deltas[0].Index != 1 || tgt.Deltas[1].Index != 3 {
		t.Errorf("indices = %d, %d; want 1, 3", tgt.Deltas[0].Index, tgt.Deltas[1].Index)
	}
}

func TestParseTarget_RejectsMalformed(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
	}{
		{"too-few-fields", "1 0 0\n"},
		{"bad-index", "abc 0 0 0\n"},
		{"bad-delta", "1 NaN-ish 0 0\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseTarget(tc.name, strings.NewReader(tc.src))
			if err == nil {
				t.Errorf("expected error parsing %q", tc.src)
			}
		})
	}
}
