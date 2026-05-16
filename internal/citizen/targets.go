package citizen

import (
	"bufio"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Vec3 is a 3D vector of float32. A local type avoids pulling Godot's
// Vector3 into this pure-data subpackage so the parser stays testable on
// its own.
type Vec3 struct{ X, Y, Z float32 }

// Vec2 is a 2D vector of float32, used for texture coordinates parsed
// from Wavefront OBJ `vt` lines.
type Vec2 struct{ U, V float32 }

// Delta is a single sparse vertex offset within a Target.
type Delta struct {
	Index  int  // vertex index in the citizen base mesh
	Offset Vec3 // delta added to the basis position when the target weight is 1
}

// Target is the sparse list of vertex deltas making up one MakeHuman
// .target file. The file's contents are CC0 (the upstream MakeHuman team
// re-released all asset data CC0 in 2020); this struct is just an
// in-memory mirror of that data, not derived from MakeHuman code.
type Target struct {
	Name   string  // logical name, e.g. "head/head-fat-incr"
	Deltas []Delta // in file order; vertex indices are typically already sorted
}

// ParseTarget reads a single MakeHuman .target file from r. The format is
// documented inline from the file headers and the data itself (no
// reference to MakeHuman source code):
//
//   - Lines beginning with '#' are comments and skipped.
//   - Blank lines are skipped.
//   - Data lines have the form "<vertex_index> <dx> <dy> <dz>",
//     whitespace-separated. The vertex index is a non-negative integer
//     into the base mesh; the deltas are decimal floats in the same
//     coordinate frame as the base mesh.
//   - Indices are sparse: only vertices the target moves are listed.
func ParseTarget(name string, r io.Reader) (*Target, error) {
	sc := bufio.NewScanner(r)
	var deltas []Delta
	line := 0
	for sc.Scan() {
		line++
		s := strings.TrimSpace(sc.Text())
		if s == "" || s[0] == '#' {
			continue
		}
		fields := strings.Fields(s)
		if len(fields) < 4 {
			return nil, fmt.Errorf("%s:%d: want 4 fields, got %d", name, line, len(fields))
		}
		idx, err := strconv.Atoi(fields[0])
		if err != nil {
			return nil, fmt.Errorf("%s:%d: bad index %q: %w", name, line, fields[0], err)
		}
		dx, err := strconv.ParseFloat(fields[1], 32)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: bad dx %q: %w", name, line, fields[1], err)
		}
		dy, err := strconv.ParseFloat(fields[2], 32)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: bad dy %q: %w", name, line, fields[2], err)
		}
		dz, err := strconv.ParseFloat(fields[3], 32)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: bad dz %q: %w", name, line, fields[3], err)
		}
		deltas = append(deltas, Delta{
			Index:  idx,
			Offset: Vec3{float32(dx), float32(dy), float32(dz)},
		})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return &Target{Name: name, Deltas: deltas}, nil
}

// LoadTarget reads a .target file at path. The Target's Name is set from
// the caller-supplied logical name (typically the path under data/targets/
// with the extension stripped, e.g. "head/head-fat-incr") so that the
// runtime can look up targets by the same names used in sliders.go.
func LoadTarget(path, name string) (*Target, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ParseTarget(name, f)
}

// LoadTargetsFromDir walks dir and loads every .target file beneath it.
// Each Target's Name is the file's path relative to dir, forward-slashed,
// with the .target extension stripped (so "head/head-fat-incr.target"
// under dir becomes name "head/head-fat-incr"). This matches the naming
// convention used in sliders.go.
func LoadTargetsFromDir(dir string) ([]*Target, error) {
	var out []*Target
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".target") {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(strings.TrimSuffix(rel, ".target"))
		t, err := LoadTarget(path, name)
		if err != nil {
			return err
		}
		out = append(out, t)
		return nil
	})
	return out, err
}
