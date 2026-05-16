package citizen

import (
	"strings"
	"testing"
)

func TestParseOBJ_BasicTriangle(t *testing.T) {
	src := `# tiny triangle
v 0 0 0
v 1 0 0
v 0 1 0
f 1 2 3
`
	m, err := ParseOBJ("tri", strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Verts) != 3 {
		t.Errorf("got %d verts, want 3", len(m.Verts))
	}
	if len(m.Indices) != 3 {
		t.Errorf("got %d indices, want 3", len(m.Indices))
	}
	// Winding is reversed during triangulation to flip CW-from-outside
	// OBJ winding to CCW for Godot's default back-face cull.
	if m.Indices[0] != 0 || m.Indices[1] != 2 || m.Indices[2] != 1 {
		t.Errorf("indices = %v, want [0 2 1] (reversed for Godot CCW)", m.Indices)
	}
}

func TestParseOBJ_QuadFanTriangulates(t *testing.T) {
	src := `v 0 0 0
v 1 0 0
v 1 1 0
v 0 1 0
f 1 2 3 4
`
	m, err := ParseOBJ("quad", strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Indices) != 6 {
		t.Fatalf("quad should fan-triangulate to 6 indices; got %d", len(m.Indices))
	}
	// Fan-triangulation flips each tri's last two vertices to convert
	// MakeHuman's CW-from-outside winding to Godot's expected CCW.
	want := []int32{0, 2, 1, 0, 3, 2}
	for i, x := range want {
		if m.Indices[i] != x {
			t.Errorf("indices[%d] = %d, want %d (full = %v)", i, m.Indices[i], x, m.Indices)
		}
	}
}

func TestParseOBJ_IgnoresUVsAndNormalsInFace(t *testing.T) {
	// MakeHuman's base.obj uses `f v/vt` format. We must take only the
	// first number per corner.
	src := `v 0 0 0
v 1 0 0
v 0 1 0
f 1/10 2/11 3/12
`
	m, err := ParseOBJ("uv", strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Indices) != 3 || m.Indices[0] != 0 || m.Indices[1] != 2 || m.Indices[2] != 1 {
		t.Errorf("indices = %v, want [0 2 1] (winding reversed)", m.Indices)
	}
}

func TestParseOBJ_SkipsUnrecognisedDirectives(t *testing.T) {
	src := `# comment
mtllib base.mtl
g group
v 0 0 0
vt 0.5 0.5
vn 0 1 0
v 1 0 0
v 0 1 0
f 1/1/1 2/2/1 3/3/1
`
	m, err := ParseOBJ("mixed", strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Verts) != 3 {
		t.Errorf("verts = %d, want 3 (vt/vn must not be counted as vertices)", len(m.Verts))
	}
	if len(m.Indices) != 3 {
		t.Errorf("indices = %d, want 3", len(m.Indices))
	}
}
