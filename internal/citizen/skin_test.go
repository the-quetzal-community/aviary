package citizen_test

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"math"
	"testing"

	"the.quetzal.community/aviary/internal/citizen"
)

// makeGLB serializes a single-primitive glTF binary from raw attribute
// slices, mirroring how Blender exports the citizen GLBs (tightly packed,
// FLOAT geometry, USHORT joints + indices). Only the attributes with a
// non-nil slice are emitted.
func makeGLB(t *testing.T, pos, norm []citizen.Vec3, uvs []citizen.Vec2, joints [][4]uint16, weights [][4]float32, indices []int32) []byte {
	t.Helper()
	var bin bytes.Buffer
	var bvs []map[string]any
	var accs []map[string]any

	addBV := func(p []byte) int {
		for bin.Len()%4 != 0 {
			bin.WriteByte(0)
		}
		off := bin.Len()
		bin.Write(p)
		bvs = append(bvs, map[string]any{"buffer": 0, "byteOffset": off, "byteLength": len(p)})
		return len(bvs) - 1
	}
	addAcc := func(bv, comp, count int, typ string) int {
		accs = append(accs, map[string]any{
			"bufferView": bv, "componentType": comp, "count": count, "type": typ,
		})
		return len(accs) - 1
	}
	f32 := func(vals ...float32) []byte {
		b := make([]byte, 4*len(vals))
		for i, v := range vals {
			binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(v))
		}
		return b
	}

	attrs := map[string]int{}
	if pos != nil {
		var b []byte
		for _, v := range pos {
			b = append(b, f32(v.X, v.Y, v.Z)...)
		}
		attrs["POSITION"] = addAcc(addBV(b), 5126, len(pos), "VEC3")
	}
	if norm != nil {
		var b []byte
		for _, v := range norm {
			b = append(b, f32(v.X, v.Y, v.Z)...)
		}
		attrs["NORMAL"] = addAcc(addBV(b), 5126, len(norm), "VEC3")
	}
	if uvs != nil {
		var b []byte
		for _, v := range uvs {
			b = append(b, f32(v.U, v.V)...)
		}
		attrs["TEXCOORD_0"] = addAcc(addBV(b), 5126, len(uvs), "VEC2")
	}
	if joints != nil {
		b := make([]byte, 0, len(joints)*8)
		for _, j := range joints {
			var e [8]byte
			for c := 0; c < 4; c++ {
				binary.LittleEndian.PutUint16(e[c*2:], j[c])
			}
			b = append(b, e[:]...)
		}
		attrs["JOINTS_0"] = addAcc(addBV(b), 5123, len(joints), "VEC4")
	}
	if weights != nil {
		var b []byte
		for _, w := range weights {
			b = append(b, f32(w[0], w[1], w[2], w[3])...)
		}
		attrs["WEIGHTS_0"] = addAcc(addBV(b), 5126, len(weights), "VEC4")
	}
	prim := map[string]any{"attributes": attrs}
	if indices != nil {
		b := make([]byte, 0, len(indices)*2)
		for _, idx := range indices {
			var e [2]byte
			binary.LittleEndian.PutUint16(e[:], uint16(idx))
			b = append(b, e[:]...)
		}
		prim["indices"] = addAcc(addBV(b), 5123, len(indices), "SCALAR")
	}

	doc := map[string]any{
		"asset":       map[string]any{"version": "2.0"},
		"buffers":     []any{map[string]any{"byteLength": bin.Len()}},
		"bufferViews": bvs,
		"accessors":   accs,
		"meshes":      []any{map[string]any{"primitives": []any{prim}}},
	}
	jsonBytes, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal glTF: %v", err)
	}
	for len(jsonBytes)%4 != 0 {
		jsonBytes = append(jsonBytes, ' ')
	}
	binBytes := bin.Bytes()
	for len(binBytes)%4 != 0 {
		binBytes = append(binBytes, 0)
	}

	var out bytes.Buffer
	total := 12 + 8 + len(jsonBytes) + 8 + len(binBytes)
	w32 := func(v uint32) {
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], v)
		out.Write(b[:])
	}
	w32(0x46546C67) // glTF
	w32(2)
	w32(uint32(total))
	w32(uint32(len(jsonBytes)))
	w32(0x4E4F534A) // JSON
	out.Write(jsonBytes)
	w32(uint32(len(binBytes)))
	w32(0x004E4942) // BIN\0
	out.Write(binBytes)
	return out.Bytes()
}

func TestParseGLBRoundtrip(t *testing.T) {
	pos := []citizen.Vec3{{1, 2, 3}, {4, 5, 6}, {7, 8, 9}}
	norm := []citizen.Vec3{{0, 1, 0}, {1, 0, 0}, {0, 0, 1}}
	uvs := []citizen.Vec2{{0.1, 0.2}, {0.3, 0.4}, {0.5, 0.6}}
	joints := [][4]uint16{{0, 1, 0, 0}, {2, 3, 0, 0}, {1, 0, 0, 0}}
	weights := [][4]float32{{1, 0, 0, 0}, {0.5, 0.5, 0, 0}, {1, 0, 0, 0}}
	indices := []int32{0, 1, 2}

	glb := makeGLB(t, pos, norm, uvs, joints, weights, indices)
	g, err := citizen.ParseGLB(bytes.NewReader(glb))
	if err != nil {
		t.Fatalf("ParseGLB: %v", err)
	}
	if len(g.Positions) != 3 || g.Positions[1] != (citizen.Vec3{4, 5, 6}) {
		t.Fatalf("positions wrong: %+v", g.Positions)
	}
	if len(g.Normals) != 3 || g.Normals[0] != (citizen.Vec3{0, 1, 0}) {
		t.Fatalf("normals wrong: %+v", g.Normals)
	}
	if len(g.UVs) != 3 || g.UVs[2] != (citizen.Vec2{0.5, 0.6}) {
		t.Fatalf("uvs wrong: %+v", g.UVs)
	}
	if len(g.Joints) != 3 || g.Joints[1] != [4]uint16{2, 3, 0, 0} {
		t.Fatalf("joints wrong: %+v", g.Joints)
	}
	if len(g.Weights) != 3 || g.Weights[1] != [4]float32{0.5, 0.5, 0, 0} {
		t.Fatalf("weights wrong: %+v", g.Weights)
	}
	if len(g.Indices) != 3 || g.Indices[2] != 2 {
		t.Fatalf("indices wrong: %+v", g.Indices)
	}
}

// rotZ rotates v by +90° about Z around pivot s: (x,y)→(-y,x). Maps a -Y
// "down" arm onto a +X "out" T-pose arm.
func rotZ90About(v, s citizen.Vec3) citizen.Vec3 {
	d := citizen.Vec3{v.X - s.X, v.Y - s.Y, v.Z - s.Z}
	return citizen.Vec3{s.X - d.Y, s.Y + d.X, s.Z + d.Z}
}

func TestBuildCitizenRigStraightensArmDeltas(t *testing.T) {
	const scale = 0.1
	translate := citizen.Vec3{0, 0.8, 0}

	// Canonical base.obj verts (A-pose, decimetre space): one torso vert on
	// the spine bone, plus an arm cloud hanging down -Y off the shoulder.
	shoulder := citizen.Vec3{2, 12, 0}
	apose := []citizen.Vec3{
		{0, 10, 0}, // 0: torso  → joint 0
		{2.2, 12, 0},
		{2, 10, 0},
		{2, 8, 0},
		{2, 10, 0.2},
		{2, 9, -0.2}, // 1..5: arm → joint 1
	}
	dom := []uint16{0, 1, 1, 1, 1, 1}

	// T-pose: rotate the arm verts out to +X about the shoulder; torso stays.
	tpose := make([]citizen.Vec3, len(apose))
	for i, v := range apose {
		if dom[i] == 1 {
			tpose[i] = rotZ90About(v, shoulder)
		} else {
			tpose[i] = v
		}
	}

	toGLB := func(src []citizen.Vec3) []citizen.Vec3 {
		out := make([]citizen.Vec3, len(src))
		for i, v := range src {
			out[i] = citizen.Vec3{
				X: scale*v.X + translate.X,
				Y: scale*v.Y + translate.Y,
				Z: scale*v.Z + translate.Z,
			}
		}
		return out
	}
	joints := make([][4]uint16, len(apose))
	weights := make([][4]float32, len(apose))
	for i := range apose {
		joints[i] = [4]uint16{dom[i], 0, 0, 0}
		weights[i] = [4]float32{1, 0, 0, 0}
	}

	// Unique UV per vertex so the UV match recovers each vertex's own canonical
	// index; base.Indices references all 6 so they're "body" UV-match targets.
	uvs := []citizen.Vec2{{U: 0, V: 0.5}, {U: 0.1, V: 0.5}, {U: 0.2, V: 0.5}, {U: 0.3, V: 0.5}, {U: 0.4, V: 0.5}, {U: 0.5, V: 0.5}}

	animGLB := makeGLB(t, toGLB(tpose), nil, uvs, joints, weights, nil)
	anim, err := citizen.ParseGLB(bytes.NewReader(animGLB))
	if err != nil {
		t.Fatal(err)
	}

	base := &citizen.BaseMesh{Verts: apose, UVs: uvs, Indices: []int32{0, 1, 2, 3, 4, 5}}
	rig, err := citizen.BuildCitizenRig(anim, base, scale, translate)
	if err != nil {
		t.Fatalf("BuildCitizenRig: %v", err)
	}

	// Canonical map: each animated vert recovers its own base vertex by UV, and
	// CanonicalToGLB round-trips.
	for i := range apose {
		if rig.Canonical[i] != int32(i) {
			t.Fatalf("Canonical[%d] = %d, want %d", i, rig.Canonical[i], i)
		}
		if rig.CanonicalToGLB[i] != int32(i) {
			t.Fatalf("CanonicalToGLB[%d] = %d, want %d", i, rig.CanonicalToGLB[i], i)
		}
	}

	// A "lengthen the arm downward" morph delta in A-pose space points -Y.
	// After straightening it must point +X (out along the T-pose arm), scaled
	// into GLB space. Apply Disp to an arm vert (index 2).
	delta := citizen.Vec3{0, -1, 0}
	got := rig.Disp[2].Mul3Vec(delta)
	want := citizen.Vec3{scale * 1, 0, 0} // +X, scaled
	if !vecClose(got, want, 1e-3) {
		t.Fatalf("arm Disp·(0,-1,0) = %+v, want %+v (delta should rotate down→out)", got, want)
	}

	// The torso bone wasn't rotated, so its Disp is just the scale: a -Y
	// delta stays -Y.
	gotT := rig.Disp[0].Mul3Vec(delta)
	wantT := citizen.Vec3{0, -scale, 0}
	if !vecClose(gotT, wantT, 1e-3) {
		t.Fatalf("torso Disp·(0,-1,0) = %+v, want %+v (no rotation)", gotT, wantT)
	}

	// With zero morph, the reconstructed bind position is exactly the GLB
	// bind (animated) position.
	for i := range apose {
		if !vecClose(rig.Bind[i], toGLB(tpose)[i], 1e-4) {
			t.Fatalf("Bind[%d] = %+v, want GLB tpose %+v", i, rig.Bind[i], toGLB(tpose)[i])
		}
	}
}

func vecClose(a, b citizen.Vec3, eps float32) bool {
	return absF(a.X-b.X) < eps && absF(a.Y-b.Y) < eps && absF(a.Z-b.Z) < eps
}
func absF(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}
