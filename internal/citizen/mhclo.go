package citizen

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// MHClo is the parsed contents of a MakeHuman .mhclo clothing fit file.
// Each clothing vertex's runtime position is reconstructed from three
// body vertices' positions and a fixed offset — see [MHClo.Fit].
type MHClo struct {
	Name    string
	OBJFile string // sibling .obj filename referenced by the .mhclo
	// Anchors[i] is the fit recipe for clothing vertex i. The clothing
	// mesh's vertex array is expected to have len(Anchors) vertices in
	// the same order as the .obj they ship alongside.
	Anchors []MHCloAnchor
	// Per-axis scale references. Each gives a pair of body vertex
	// indices that define a reference extent on that axis at clothing-
	// authoring time. The math for applying these isn't implemented in
	// v1 — the barycentric fit alone is enough for clothing that
	// matches the default body proportions; scale adjustments matter
	// when body sliders deform the body and we want clothing to follow
	// the proportional change.
	XScale MHCloScale
	YScale MHCloScale
	ZScale MHCloScale
	// DeleteVerts lists body vertex indices to hide when this clothing
	// is equipped — MakeHuman's solution to the body poking through
	// clothing. Triangles touching any of these vertices should be
	// dropped from the body's index buffer while this item is worn.
	DeleteVerts []int32
}

// MHCloAnchor maps a single clothing vertex to a weighted sum of three
// body vertices plus a fixed offset:
//
//	clothing[i] = Weights[0] * body[Verts[0]]
//	            + Weights[1] * body[Verts[1]]
//	            + Weights[2] * body[Verts[2]]
//	            + Offset
type MHCloAnchor struct {
	Verts   [3]int32
	Weights [3]float32
	Offset  Vec3
}

// MHCloScale records one axis's reference: two body vertex indices that
// bound the axis at clothing-authoring time, and a baked-in extent
// value the authoring tool recorded. v1 ignores these.
type MHCloScale struct {
	VertA, VertB int32
	Reference    float32
}

// ParseMHClo reads a MakeHuman .mhclo file. The format we accept:
//
//   - `#`-prefixed comment lines and blank lines are skipped.
//   - Header lines `key value...` set fields by key (name, obj_file,
//     x_scale, y_scale, z_scale). Unrecognised keys are ignored.
//   - A line starting `verts` (optionally followed by a 0) marks the
//     start of the vertex anchor table.
//   - Each subsequent line is one clothing vertex's anchor:
//     `<bv0> <bv1> <bv2> <w0> <w1> <w2> <dx> <dy> <dz>`
//     — three body vertex indices, three barycentric weights, three
//     offset components. Lines with fewer fields are skipped.
func ParseMHClo(name string, r io.Reader) (*MHClo, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	out := &MHClo{}
	section := ""
	line := 0
	for sc.Scan() {
		line++
		s := strings.TrimSpace(sc.Text())
		if s == "" || s[0] == '#' {
			continue
		}
		fields := strings.Fields(s)
		// Section header? A bare keyword on its own line switches us
		// into that section. We recognise `verts` and `delete_verts`;
		// other section starts (e.g. `material`) reset us out of any
		// list-parsing mode so we don't keep appending stray data.
		if section != "" && len(fields) > 0 {
			switch fields[0] {
			case "verts":
				section = "verts"
				continue
			case "delete_verts":
				section = "delete_verts"
				continue
			}
		}
		if section == "" {
			if len(fields) == 0 {
				continue
			}
			switch fields[0] {
			case "name":
				if len(fields) >= 2 {
					out.Name = strings.Join(fields[1:], " ")
				}
			case "obj_file":
				if len(fields) >= 2 {
					out.OBJFile = fields[1]
				}
			case "x_scale":
				out.XScale = parseMHCloScale(fields, name, line)
			case "y_scale":
				out.YScale = parseMHCloScale(fields, name, line)
			case "z_scale":
				out.ZScale = parseMHCloScale(fields, name, line)
			case "verts":
				section = "verts"
			case "delete_verts":
				section = "delete_verts"
			}
			continue
		}
		if section == "delete_verts" {
			// Tokens are either a single vertex index or `<a> - <b>`
			// for an inclusive range. Ranges can span lines.
			for i := 0; i < len(fields); i++ {
				if i+2 < len(fields) && fields[i+1] == "-" {
					a, errA := strconv.Atoi(fields[i])
					b, errB := strconv.Atoi(fields[i+2])
					if errA == nil && errB == nil && b >= a {
						for v := a; v <= b; v++ {
							out.DeleteVerts = append(out.DeleteVerts, int32(v))
						}
					}
					i += 2
					continue
				}
				v, err := strconv.Atoi(fields[i])
				if err == nil {
					out.DeleteVerts = append(out.DeleteVerts, int32(v))
				}
			}
			continue
		}
		// section == "verts": MakeHuman compresses anchors that pin to a
		// single body vertex (weight 1, no offset) to just one field.
		// Full barycentric anchors are 9 fields: 3 verts, 3 weights,
		// 3 offset components. Anything else (0 fields, etc.) is skipped.
		switch len(fields) {
		case 1:
			v, err := strconv.Atoi(fields[0])
			if err != nil {
				continue
			}
			out.Anchors = append(out.Anchors, MHCloAnchor{
				Verts:   [3]int32{int32(v), int32(v), int32(v)},
				Weights: [3]float32{1, 0, 0},
			})
		case 9:
			// On any parse error treat the line as a non-anchor; mhclo
			// can include trailing sections like `delete_verts <ranges>`
			// where each line has 9+ fields but isn't a fit row (see the
			// t-bar asset).
			var a MHCloAnchor
			valid := true
			for i := 0; i < 3 && valid; i++ {
				v, err := strconv.Atoi(fields[i])
				if err != nil {
					valid = false
					break
				}
				a.Verts[i] = int32(v)
			}
			for i := 0; i < 3 && valid; i++ {
				w, err := strconv.ParseFloat(fields[3+i], 32)
				if err != nil {
					valid = false
					break
				}
				a.Weights[i] = float32(w)
			}
			if valid {
				dx, errX := strconv.ParseFloat(fields[6], 32)
				dy, errY := strconv.ParseFloat(fields[7], 32)
				dz, errZ := strconv.ParseFloat(fields[8], 32)
				if errX != nil || errY != nil || errZ != nil {
					valid = false
				} else {
					a.Offset = Vec3{X: float32(dx), Y: float32(dy), Z: float32(dz)}
				}
			}
			if valid {
				out.Anchors = append(out.Anchors, a)
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return out, nil
}

func parseMHCloScale(fields []string, name string, line int) MHCloScale {
	if len(fields) < 4 {
		return MHCloScale{}
	}
	a, errA := strconv.Atoi(fields[1])
	b, errB := strconv.Atoi(fields[2])
	r, errR := strconv.ParseFloat(fields[3], 32)
	if errA != nil || errB != nil || errR != nil {
		return MHCloScale{}
	}
	return MHCloScale{VertA: int32(a), VertB: int32(b), Reference: float32(r)}
}

// LoadMHClo opens a .mhclo file at path.
func LoadMHClo(path string) (*MHClo, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ParseMHClo(path, f)
}

// Fit computes clothing vertex positions from current body vertex
// positions. The returned slice has len(m.Anchors) entries — one per
// clothing vertex — and reuses the passed-in `out` slice if it has
// enough capacity.
//
// Indices in m.Anchors are not range-checked; the caller is responsible
// for passing a body slice whose length covers every Verts[k] referenced
// (typically the citizen's full Recompute output).
func (m *MHClo) Fit(body []Vec3, out []Vec3) []Vec3 {
	if cap(out) < len(m.Anchors) {
		out = make([]Vec3, len(m.Anchors))
	} else {
		out = out[:len(m.Anchors)]
	}
	for i, a := range m.Anchors {
		v0 := body[a.Verts[0]]
		v1 := body[a.Verts[1]]
		v2 := body[a.Verts[2]]
		w0, w1, w2 := a.Weights[0], a.Weights[1], a.Weights[2]
		out[i] = Vec3{
			X: w0*v0.X + w1*v1.X + w2*v2.X + a.Offset.X,
			Y: w0*v0.Y + w1*v1.Y + w2*v2.Y + a.Offset.Y,
			Z: w0*v0.Z + w1*v1.Z + w2*v2.Z + a.Offset.Z,
		}
	}
	return out
}
