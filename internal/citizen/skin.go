package citizen

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
)

// GLB holds the geometry + skin attributes we extract from a glTF binary.
// Only the first mesh's first primitive is read — the citizen GLBs are a
// single primitive. Animations, the skin's joint hierarchy, and materials
// are deliberately NOT parsed here: Godot's own glTF importer builds the
// Skeleton3D + AnimationPlayer + Skin from the same file at runtime, and we
// reuse those. All we need from Go is the bind-pose geometry and the
// per-vertex bone bindings so we can rebuild the renderable ArrayMesh with
// our procedural morph deltas folded in.
//
// JOINTS_0 values index into the glTF skin's `joints` array, which is the
// exact order Godot's importer uses to build the mesh's Skin binds — so the
// raw values double as Godot ARRAY_BONES indices when the imported Skin is
// reused.
type GLB struct {
	Positions []Vec3
	Normals   []Vec3       // empty if the primitive had no NORMAL
	UVs       []Vec2       // empty if no TEXCOORD_0
	Joints    [][4]uint16  // empty if no JOINTS_0
	Weights   [][4]float32 // empty if no WEIGHTS_0
	Indices   []int32
}

// glTF JSON subset — just the chunks needed to locate and decode the first
// primitive's accessors. Everything else (skins, animations, nodes,
// materials, scenes) is ignored.
type gltfDoc struct {
	Accessors   []gltfAccessor   `json:"accessors"`
	BufferViews []gltfBufferView `json:"bufferViews"`
	Meshes      []struct {
		Primitives []struct {
			Attributes map[string]int `json:"attributes"`
			Indices    *int           `json:"indices"`
		} `json:"primitives"`
	} `json:"meshes"`
}

type gltfAccessor struct {
	BufferView    *int   `json:"bufferView"`
	ByteOffset    int    `json:"byteOffset"`
	ComponentType int    `json:"componentType"`
	Count         int    `json:"count"`
	Type          string `json:"type"`
	Normalized    bool   `json:"normalized"`
}

type gltfBufferView struct {
	Buffer     int `json:"buffer"`
	ByteOffset int `json:"byteOffset"`
	ByteLength int `json:"byteLength"`
	ByteStride int `json:"byteStride"`
}

const (
	compByte   = 5120
	compUByte  = 5121
	compShort  = 5122
	compUShort = 5123
	compUInt   = 5125
	compFloat  = 5126
)

func componentSize(ct int) int {
	switch ct {
	case compByte, compUByte:
		return 1
	case compShort, compUShort:
		return 2
	case compUInt, compFloat:
		return 4
	}
	return 0
}

func typeComponents(t string) int {
	switch t {
	case "SCALAR":
		return 1
	case "VEC2":
		return 2
	case "VEC3":
		return 3
	case "VEC4":
		return 4
	case "MAT4":
		return 16
	}
	return 0
}

// ParseGLB reads a binary glTF (.glb) and extracts the first primitive's
// geometry + skin attributes. The single embedded BIN chunk is the only
// buffer; external/data-URI buffers are not supported (the citizen GLBs are
// always self-contained).
func ParseGLB(r io.Reader) (*GLB, error) {
	all, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	if len(all) < 12 {
		return nil, fmt.Errorf("glb: too short (%d bytes)", len(all))
	}
	if binary.LittleEndian.Uint32(all[0:4]) != 0x46546C67 { // "glTF"
		return nil, fmt.Errorf("glb: bad magic")
	}
	// Walk the chunk list: first JSON, then BIN.
	var jsonChunk, binChunk []byte
	off := 12
	for off+8 <= len(all) {
		clen := int(binary.LittleEndian.Uint32(all[off : off+4]))
		ctype := binary.LittleEndian.Uint32(all[off+4 : off+8])
		off += 8
		if off+clen > len(all) {
			return nil, fmt.Errorf("glb: chunk overruns file")
		}
		data := all[off : off+clen]
		switch ctype {
		case 0x4E4F534A: // "JSON"
			jsonChunk = data
		case 0x004E4942: // "BIN\0"
			binChunk = data
		}
		off += clen
	}
	if jsonChunk == nil {
		return nil, fmt.Errorf("glb: no JSON chunk")
	}
	var doc gltfDoc
	if err := json.Unmarshal(jsonChunk, &doc); err != nil {
		return nil, fmt.Errorf("glb: json: %w", err)
	}
	if len(doc.Meshes) == 0 || len(doc.Meshes[0].Primitives) == 0 {
		return nil, fmt.Errorf("glb: no mesh/primitive")
	}
	prim := doc.Meshes[0].Primitives[0]

	// accessorBytes returns the raw element bytes for accessor i, honouring
	// the bufferView byteStride (tightly packed when absent).
	accessorBytes := func(i int) ([]byte, gltfAccessor, int, int, error) {
		if i < 0 || i >= len(doc.Accessors) {
			return nil, gltfAccessor{}, 0, 0, fmt.Errorf("glb: accessor %d out of range", i)
		}
		acc := doc.Accessors[i]
		if acc.BufferView == nil {
			return nil, acc, 0, 0, fmt.Errorf("glb: accessor %d has no bufferView", i)
		}
		bv := doc.BufferViews[*acc.BufferView]
		elem := componentSize(acc.ComponentType) * typeComponents(acc.Type)
		if elem == 0 {
			return nil, acc, 0, 0, fmt.Errorf("glb: accessor %d unsupported type", i)
		}
		stride := bv.ByteStride
		if stride == 0 {
			stride = elem
		}
		base := bv.ByteOffset + acc.ByteOffset
		if binChunk == nil {
			return nil, acc, 0, 0, fmt.Errorf("glb: accessor %d needs BIN chunk", i)
		}
		return binChunk[base:], acc, stride, elem, nil
	}

	readFloat := func(b []byte, ct int, norm bool) float32 {
		switch ct {
		case compFloat:
			return math.Float32frombits(binary.LittleEndian.Uint32(b))
		case compUByte:
			if norm {
				return float32(b[0]) / 255
			}
			return float32(b[0])
		case compUShort:
			if norm {
				return float32(binary.LittleEndian.Uint16(b)) / 65535
			}
			return float32(binary.LittleEndian.Uint16(b))
		case compByte:
			v := int8(b[0])
			if norm {
				return float32(v) / 127
			}
			return float32(v)
		case compShort:
			v := int16(binary.LittleEndian.Uint16(b))
			if norm {
				return float32(v) / 32767
			}
			return float32(v)
		}
		return 0
	}

	readVec3 := func(ai int) ([]Vec3, error) {
		raw, acc, stride, _, err := accessorBytes(ai)
		if err != nil {
			return nil, err
		}
		cs := componentSize(acc.ComponentType)
		out := make([]Vec3, acc.Count)
		for k := 0; k < acc.Count; k++ {
			b := raw[k*stride:]
			out[k] = Vec3{
				X: readFloat(b, acc.ComponentType, acc.Normalized),
				Y: readFloat(b[cs:], acc.ComponentType, acc.Normalized),
				Z: readFloat(b[2*cs:], acc.ComponentType, acc.Normalized),
			}
		}
		return out, nil
	}
	readVec2 := func(ai int) ([]Vec2, error) {
		raw, acc, stride, _, err := accessorBytes(ai)
		if err != nil {
			return nil, err
		}
		cs := componentSize(acc.ComponentType)
		out := make([]Vec2, acc.Count)
		for k := 0; k < acc.Count; k++ {
			b := raw[k*stride:]
			out[k] = Vec2{
				U: readFloat(b, acc.ComponentType, acc.Normalized),
				V: readFloat(b[cs:], acc.ComponentType, acc.Normalized),
			}
		}
		return out, nil
	}
	readWeights := func(ai int) ([][4]float32, error) {
		raw, acc, stride, _, err := accessorBytes(ai)
		if err != nil {
			return nil, err
		}
		cs := componentSize(acc.ComponentType)
		out := make([][4]float32, acc.Count)
		for k := 0; k < acc.Count; k++ {
			b := raw[k*stride:]
			for c := 0; c < 4; c++ {
				out[k][c] = readFloat(b[c*cs:], acc.ComponentType, acc.Normalized)
			}
		}
		return out, nil
	}
	readJoints := func(ai int) ([][4]uint16, error) {
		raw, acc, stride, _, err := accessorBytes(ai)
		if err != nil {
			return nil, err
		}
		cs := componentSize(acc.ComponentType)
		out := make([][4]uint16, acc.Count)
		for k := 0; k < acc.Count; k++ {
			b := raw[k*stride:]
			for c := 0; c < 4; c++ {
				switch acc.ComponentType {
				case compUByte:
					out[k][c] = uint16(b[c*cs])
				case compUShort:
					out[k][c] = binary.LittleEndian.Uint16(b[c*cs:])
				}
			}
		}
		return out, nil
	}
	readIndices := func(ai int) ([]int32, error) {
		raw, acc, stride, _, err := accessorBytes(ai)
		if err != nil {
			return nil, err
		}
		out := make([]int32, acc.Count)
		for k := 0; k < acc.Count; k++ {
			b := raw[k*stride:]
			switch acc.ComponentType {
			case compUByte:
				out[k] = int32(b[0])
			case compUShort:
				out[k] = int32(binary.LittleEndian.Uint16(b))
			case compUInt:
				out[k] = int32(binary.LittleEndian.Uint32(b))
			}
		}
		return out, nil
	}

	g := &GLB{}
	posAcc, ok := prim.Attributes["POSITION"]
	if !ok {
		return nil, fmt.Errorf("glb: primitive has no POSITION")
	}
	if g.Positions, err = readVec3(posAcc); err != nil {
		return nil, err
	}
	if ai, ok := prim.Attributes["NORMAL"]; ok {
		if g.Normals, err = readVec3(ai); err != nil {
			return nil, err
		}
	}
	if ai, ok := prim.Attributes["TEXCOORD_0"]; ok {
		if g.UVs, err = readVec2(ai); err != nil {
			return nil, err
		}
	}
	if ai, ok := prim.Attributes["JOINTS_0"]; ok {
		if g.Joints, err = readJoints(ai); err != nil {
			return nil, err
		}
	}
	if ai, ok := prim.Attributes["WEIGHTS_0"]; ok {
		if g.Weights, err = readWeights(ai); err != nil {
			return nil, err
		}
	}
	if prim.Indices != nil {
		if g.Indices, err = readIndices(*prim.Indices); err != nil {
			return nil, err
		}
	}
	return g, nil
}

// LoadGLB reads a binary glTF from the local filesystem.
func LoadGLB(path string) (*GLB, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ParseGLB(f)
}
