package citizen

import (
	"math"
	"strings"
	"testing"
)

func approxEq(a, b Vec3) bool {
	const eps = 1e-4
	return math.Abs(float64(a.X-b.X)) < eps &&
		math.Abs(float64(a.Y-b.Y)) < eps &&
		math.Abs(float64(a.Z-b.Z)) < eps
}

func TestParseMHClo_Header(t *testing.T) {
	src := `# comment
uuid 12345
basemesh hm08
name My Shirt
obj_file my_shirt.obj
x_scale 5399 11998 1.4340
y_scale 791 881 2.4098
z_scale 962 5320 2.0001
material my_shirt.mhmat
verts 0
 0 1 2 0.5 0.3 0.2 0.0 0.1 0.0
`
	m, err := ParseMHClo("hdr", strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "My Shirt" {
		t.Errorf("Name = %q, want %q", m.Name, "My Shirt")
	}
	if m.OBJFile != "my_shirt.obj" {
		t.Errorf("OBJFile = %q", m.OBJFile)
	}
	if m.XScale.VertA != 5399 || m.XScale.VertB != 11998 || math.Abs(float64(m.XScale.Reference-1.4340)) > 1e-4 {
		t.Errorf("XScale = %+v", m.XScale)
	}
	if len(m.Anchors) != 1 {
		t.Fatalf("Anchors = %d, want 1", len(m.Anchors))
	}
}

func TestParseMHClo_AnchorMath(t *testing.T) {
	src := `verts 0
 0 1 2 0.5 0.3 0.2 0.0 0.1 0.0
 0 1 2 1.0 0.0 0.0 -1.0 0.0 0.0
`
	m, err := ParseMHClo("anchors", strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Anchors) != 2 {
		t.Fatalf("Anchors = %d, want 2", len(m.Anchors))
	}
	got := m.Anchors[0]
	if got.Verts != [3]int32{0, 1, 2} {
		t.Errorf("Verts = %v", got.Verts)
	}
	if got.Weights != [3]float32{0.5, 0.3, 0.2} {
		t.Errorf("Weights = %v", got.Weights)
	}
	if got.Offset != (Vec3{0, 0.1, 0}) {
		t.Errorf("Offset = %v", got.Offset)
	}
}

func TestMHClo_Fit(t *testing.T) {
	src := `verts 0
 0 1 2 0.5 0.3 0.2 0.0 0.0 0.0
 0 1 2 1.0 0.0 0.0 -10.0 0.0 0.0
`
	m, err := ParseMHClo("fit", strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	body := []Vec3{
		{X: 100, Y: 0, Z: 0}, // body[0]
		{X: 0, Y: 100, Z: 0}, // body[1]
		{X: 0, Y: 0, Z: 100}, // body[2]
	}
	got := m.Fit(body, nil)
	// First clothing vertex: 0.5*body[0] + 0.3*body[1] + 0.2*body[2] + (0,0,0)
	// = (50, 30, 20)
	want0 := Vec3{X: 50, Y: 30, Z: 20}
	if !approxEq(got[0], want0) {
		t.Errorf("Fit[0] = %v, want %v", got[0], want0)
	}
	// Second clothing vertex: 1.0*body[0] + offset (-10,0,0) = (90, 0, 0)
	want1 := Vec3{X: 90, Y: 0, Z: 0}
	if !approxEq(got[1], want1) {
		t.Errorf("Fit[1] = %v, want %v", got[1], want1)
	}
}

func TestMHClo_FitReusesBuffer(t *testing.T) {
	src := `verts 0
 0 1 2 0.5 0.3 0.2 0.0 0.0 0.0
`
	m, _ := ParseMHClo("buf", strings.NewReader(src))
	body := []Vec3{{1, 1, 1}, {1, 1, 1}, {1, 1, 1}}
	buf := make([]Vec3, 4)
	got := m.Fit(body, buf)
	// Length should match anchor count, not the input buffer length.
	if len(got) != 1 {
		t.Errorf("len = %d, want 1", len(got))
	}
	if &got[0] != &buf[0] {
		t.Error("Fit should reuse the passed buffer when capacity allows")
	}
}

func TestParseMHClo_SkipsTrailingNonAnchor(t *testing.T) {
	// Some .mhclo files have post-verts sections like delete-vertex
	// lists with different field counts. The parser should silently
	// stop accumulating anchors instead of erroring.
	src := `verts 0
 0 1 2 0.5 0.3 0.2 0.0 0.0 0.0
delete_verts
 1 2 3 4
`
	m, err := ParseMHClo("trail", strings.NewReader(src))
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if len(m.Anchors) != 1 {
		t.Errorf("Anchors = %d, want 1", len(m.Anchors))
	}
}

func TestParseMHClo_DeleteVerts(t *testing.T) {
	// The delete_verts section uses `<a> - <b>` for inclusive ranges
	// and bare integers for singletons, with tokens spanning lines.
	src := `verts 0
 0 1 2 0.5 0.3 0.2 0.0 0.0 0.0
delete_verts
10 - 12 20 22
30 - 31
`
	m, err := ParseMHClo("dv", strings.NewReader(src))
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	got := m.DeleteVerts
	want := []int32{10, 11, 12, 20, 22, 30, 31}
	if len(got) != len(want) {
		t.Fatalf("DeleteVerts len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("DeleteVerts[%d] = %d, want %d", i, got[i], want[i])
		}
	}
}
