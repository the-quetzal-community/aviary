package citizen

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// BaseMesh is a parsed Wavefront OBJ: vertex positions and triangulated
// face indices. Vertex indices stay 1:1 with what the upstream MakeHuman
// .target and .mhclo files reference. UVs, when present in the OBJ, are
// collapsed to one-per-position (the last `vt` referenced for each
// position wins) — this loses continuity at UV seams on a thin strip of
// triangles but preserves the position-indexed invariant required by
// the .mhclo barycentric fitter.
type BaseMesh struct {
	Verts   []Vec3
	Indices []int32
	// UVs is empty when the OBJ has no `vt` references; otherwise it has
	// exactly len(Verts) entries.
	UVs []Vec2
	// EyeIndices are the faces from the `helper-l-eye` and
	// `helper-r-eye` groups, kept separate from Indices so they can be
	// rendered as their own surface with a distinct eye-tint material.
	// MakeHuman's apps/human.py masks these out of its main body
	// render, but we want them visible — eye colour is part of
	// character customisation.
	EyeIndices []int32
}

// ParseOBJ reads a Wavefront OBJ file producing a triangulated BaseMesh.
// The format we accept is the subset used by MakeHuman's base.obj:
//
//   - `v X Y Z` lines for vertex positions (we ignore optional W).
//   - `f I1[/UV1[/N1]] I2... ...` lines, 1-indexed into the vertex list.
//     N-gons (notably quads, which MakeHuman uses pervasively) are fan-
//     triangulated from the first vertex.
//   - Lines starting with `#` and any line type we don't recognise (`vt`,
//     `vn`, `g`, `mtllib`, …) are ignored.
func ParseOBJ(name string, r io.Reader) (*BaseMesh, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var verts []Vec3
	var uvList []Vec2
	var posUV []Vec2
	hasUVRefs := false
	var indices []int32
	var eyeIndices []int32
	type faceRef struct{ pos, uv int32 }
	var faceBuf []faceRef
	// group is the current `g <name>` group context. MakeHuman's base.obj
	// tags joint and helper scaffolding faces with `joint-*` / `helper-*`
	// group names — those are never rendered as body in MH's UI.
	// We drop joint-* and most helper-* groups but route the eye
	// helpers to a separate index buffer so they can be rendered as
	// a tintable surface alongside the body.
	group := ""
	skipGroup := false
	eyeGroup := false
	line := 0
	for sc.Scan() {
		line++
		s := strings.TrimSpace(sc.Text())
		if s == "" || s[0] == '#' {
			continue
		}
		switch {
		case strings.HasPrefix(s, "v "):
			fields := strings.Fields(s)
			if len(fields) < 4 {
				return nil, fmt.Errorf("%s:%d: malformed vertex (%d fields)", name, line, len(fields))
			}
			x, err := strconv.ParseFloat(fields[1], 32)
			if err != nil {
				return nil, fmt.Errorf("%s:%d: bad x: %w", name, line, err)
			}
			y, err := strconv.ParseFloat(fields[2], 32)
			if err != nil {
				return nil, fmt.Errorf("%s:%d: bad y: %w", name, line, err)
			}
			z, err := strconv.ParseFloat(fields[3], 32)
			if err != nil {
				return nil, fmt.Errorf("%s:%d: bad z: %w", name, line, err)
			}
			verts = append(verts, Vec3{X: float32(x), Y: float32(y), Z: float32(z)})
		case strings.HasPrefix(s, "vt "):
			fields := strings.Fields(s)
			if len(fields) < 3 {
				return nil, fmt.Errorf("%s:%d: malformed vt (%d fields)", name, line, len(fields))
			}
			u, err := strconv.ParseFloat(fields[1], 32)
			if err != nil {
				return nil, fmt.Errorf("%s:%d: bad u: %w", name, line, err)
			}
			// Godot expects v=0 at top of texture; OBJ has v=0 at bottom.
			v, err := strconv.ParseFloat(fields[2], 32)
			if err != nil {
				return nil, fmt.Errorf("%s:%d: bad v: %w", name, line, err)
			}
			uvList = append(uvList, Vec2{U: float32(u), V: 1 - float32(v)})
		case strings.HasPrefix(s, "g "):
			fields := strings.Fields(s)
			if len(fields) >= 2 {
				group = fields[1]
			} else {
				group = ""
			}
			eyeGroup = group == "helper-l-eye" || group == "helper-r-eye"
			skipGroup = !eyeGroup && (strings.HasPrefix(group, "joint-") ||
				strings.HasPrefix(group, "helper-"))
		case strings.HasPrefix(s, "f "):
			if skipGroup {
				continue
			}
			fields := strings.Fields(s)
			if len(fields) < 4 {
				return nil, fmt.Errorf("%s:%d: face with %d corners", name, line, len(fields)-1)
			}
			faceBuf = faceBuf[:0]
			for _, f := range fields[1:] {
				idxPart, rest, _ := strings.Cut(f, "/")
				idx, err := strconv.Atoi(idxPart)
				if err != nil {
					return nil, fmt.Errorf("%s:%d: bad face index %q: %w", name, line, idxPart, err)
				}
				uvIdx := int32(-1)
				if rest != "" {
					uvPart, _, _ := strings.Cut(rest, "/")
					if uvPart != "" {
						u, err := strconv.Atoi(uvPart)
						if err != nil {
							return nil, fmt.Errorf("%s:%d: bad uv index %q: %w", name, line, uvPart, err)
						}
						uvIdx = int32(u - 1)
						hasUVRefs = true
					}
				}
				// OBJ is 1-indexed; convert to 0-indexed for Godot.
				faceBuf = append(faceBuf, faceRef{pos: int32(idx - 1), uv: uvIdx})
			}
			// Fan-triangulate from the first vertex. MakeHuman's quads
			// are wound clockwise when viewed from outside the surface;
			// Godot's default back-face culling expects CCW for "front",
			// so we swap the last two vertices of each tri to flip the
			// winding. (If we ever ingest a CCW-wound OBJ, this is the
			// one line to change.)
			dst := &indices
			if eyeGroup {
				dst = &eyeIndices
			}
			for i := 1; i < len(faceBuf)-1; i++ {
				*dst = append(*dst, faceBuf[0].pos, faceBuf[i+1].pos, faceBuf[i].pos)
			}
			// Record one UV per position. Faces are visited in order;
			// last write wins, so seams (where one position has multiple
			// UVs) pick whichever face referenced it last. The body OBJ
			// uses position-indexed Indices, so we have to flatten;
			// dedicated per-corner UVs would require expanding vertices
			// and would desync .mhclo's position-indexed anchors.
			if hasUVRefs {
				if posUV == nil {
					posUV = make([]Vec2, 0)
				}
				for _, fr := range faceBuf {
					if fr.uv < 0 || int(fr.uv) >= len(uvList) {
						continue
					}
					if int(fr.pos) >= len(posUV) {
						grown := make([]Vec2, fr.pos+1)
						copy(grown, posUV)
						posUV = grown
					}
					posUV[fr.pos] = uvList[fr.uv]
				}
			}
		default:
			// `vn`, `g`, `mtllib`, etc — not relevant for the editable
			// mesh.
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	bm := &BaseMesh{Verts: verts, Indices: indices, EyeIndices: eyeIndices}
	if hasUVRefs {
		if len(posUV) < len(verts) {
			grown := make([]Vec2, len(verts))
			copy(grown, posUV)
			posUV = grown
		}
		bm.UVs = posUV
	}
	return bm, nil
}

// LoadBaseMesh opens a Wavefront OBJ file from the local filesystem.
func LoadBaseMesh(path string) (*BaseMesh, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ParseOBJ(path, f)
}
