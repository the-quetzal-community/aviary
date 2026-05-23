package critter

import (
	"math"
	"testing"
)

func TestSetWeight_Idempotent(t *testing.T) {
	c := New()
	if !c.SetWeight("shape/length", 0.5) {
		t.Fatal("first SetWeight should report a change")
	}
	if c.SetWeight("shape/length", 0.5) {
		t.Error("repeated SetWeight to the same value should report no change")
	}
	if !c.SetWeight("shape/length", 0) {
		t.Error("returning to zero should report a change")
	}
	if len(c.Weights()) != 0 {
		t.Errorf("zero-weight slider should have been removed; weights=%v", c.Weights())
	}
}

func TestComputeShape_AppliesLength(t *testing.T) {
	c := New()
	base, _ := c.ComputeShape()
	c.SetWeight("shape/length", 1) // max stretch ⇒ ×1.5 Z extent
	stretched, _ := c.ComputeShape()
	if !approx(stretched[0].Z, base[0].Z*1.5, 1e-4) {
		t.Errorf("tail Z = %v, want %v", stretched[0].Z, base[0].Z*1.5)
	}
	if !approx(stretched[4].Z, base[4].Z*1.5, 1e-4) {
		t.Errorf("head Z = %v, want %v", stretched[4].Z, base[4].Z*1.5)
	}
}

func TestComputeShape_AppliesRadiusScalers(t *testing.T) {
	c := New()
	_, base := c.ComputeShape()
	c.SetWeight("shape/head_size", 1)
	c.SetWeight("shape/tail_size", -1)
	_, scaled := c.ComputeShape()
	if !approx(scaled[4], base[4]*1.5, 1e-4) {
		t.Errorf("head radius = %v, want %v (×1.5)", scaled[4], base[4]*1.5)
	}
	if !approx(scaled[0], base[0]*0.5, 1e-4) {
		t.Errorf("tail radius = %v, want %v (×0.5)", scaled[0], base[0]*0.5)
	}
}

func TestBuildMesh_HasExpectedTopology(t *testing.T) {
	c := New()
	const along, around = 8, 6
	m := c.BuildMesh(along, around)
	// Ring verts + 2 caps.
	wantVerts := along*around + 2
	if len(m.Verts) != wantVerts {
		t.Errorf("Verts = %d, want %d", len(m.Verts), wantVerts)
	}
	// (along-1) ring quads ⇒ 2 tris each, + 2 cap fans ⇒ around tris each.
	wantIndices := ((along-1)*around*2 + around*2) * 3
	if len(m.Indices) != wantIndices {
		t.Errorf("Indices = %d, want %d", len(m.Indices), wantIndices)
	}
}

func TestBuildMesh_AllIndicesInRange(t *testing.T) {
	c := New()
	c.SetWeight("shape/length", 1)
	c.SetWeight("shape/arch", 1)
	m := c.BuildMesh(16, 12)
	for i, idx := range m.Indices {
		if int(idx) < 0 || int(idx) >= len(m.Verts) {
			t.Fatalf("index[%d] = %d out of range [0, %d)", i, idx, len(m.Verts))
		}
	}
}

func TestBuildMesh_CapsConnectToRings(t *testing.T) {
	// Sanity: caps are the last 2 verts; tail cap should be near the
	// first control, head cap near the last.
	c := New()
	const along, around = 12, 8
	m := c.BuildMesh(along, around)
	controls, _ := c.ComputeShape()
	tail := m.Verts[along*around]
	head := m.Verts[along*around+1]
	if !approxVec(tail, controls[0], 1e-4) {
		t.Errorf("tail cap = %v, want %v", tail, controls[0])
	}
	if !approxVec(head, controls[len(controls)-1], 1e-4) {
		t.Errorf("head cap = %v, want %v", head, controls[len(controls)-1])
	}
}

func approx(a, b, tol float32) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d <= tol
}

func approxVec(a, b Vec3, tol float32) bool {
	return approx(a.X, b.X, tol) && approx(a.Y, b.Y, tol) && approx(a.Z, b.Z, tol)
}

// Suppress "imported and not used" if a test gets stripped during edits.
var _ = math.Pi

func TestAnchorPoint_OnSurface(t *testing.T) {
	c := New()
	// Pick an arbitrary point part-way along the body. The anchor
	// position should lie on the radial surface at that t — i.e.
	// distance from the spine equals the radius there.
	const tt, theta = float32(0.3), float32(0.7)
	pos, outward, along := c.AnchorPoint(tt, theta, 0)
	// outward should be a unit vector.
	mag := outward.X*outward.X + outward.Y*outward.Y + outward.Z*outward.Z
	if !approx(mag, 1, 1e-4) {
		t.Errorf("|outward| = %v, want 1", mag)
	}
	// along should also be unit-length.
	mag = along.X*along.X + along.Y*along.Y + along.Z*along.Z
	if !approx(mag, 1, 1e-4) {
		t.Errorf("|along| = %v, want 1", mag)
	}
	// Distance from the spine sample to pos must equal radius at t.
	s := c.sampleSpineAt(tt)
	dx, dy, dz := pos.X-s.pos.X, pos.Y-s.pos.Y, pos.Z-s.pos.Z
	d := float32(math.Sqrt(float64(dx*dx + dy*dy + dz*dz)))
	if !approx(d, s.radius, 1e-4) {
		t.Errorf("surface offset = %v, want radius %v", d, s.radius)
	}
}

func TestClosestAnchor_Roundtrip(t *testing.T) {
	c := New()
	for _, tc := range []struct{ T, Theta float32 }{
		{0.25, 0.5},
		{0.5, -1.2},
		{0.75, 2.5},
	} {
		pos, _, _ := c.AnchorPoint(tc.T, tc.Theta, 0)
		gotT, gotTheta, _ := c.ClosestAnchor(pos)
		// Coarse scan precision: t is correct to ~1/63, theta is
		// exact once t is locked in.
		if approx(gotT, tc.T, 0.05) == false {
			t.Errorf("T: got %v want %v", gotT, tc.T)
		}
		// Wrap theta into [-π, π] for comparison.
		dt := gotTheta - tc.Theta
		for dt > math.Pi {
			dt -= 2 * math.Pi
		}
		for dt < -math.Pi {
			dt += 2 * math.Pi
		}
		if approx(dt, 0, 0.2) == false {
			t.Errorf("Theta: got %v want %v (diff %v)", gotTheta, tc.Theta, dt)
		}
	}
}

func TestAnchorPoint_CapAcceptsLateralOffset(t *testing.T) {
	c := New()
	// At the head cap with a non-zero radial offset, the anchor
	// should land away from the spine endpoint in the perpendicular
	// plane, and outward should still be the spine tangent (= snout
	// direction) so a beak placed off-centre still points forward.
	const theta = float32(1.1)
	const off = float32(0.15)
	pos, outward, _ := c.AnchorPoint(1, theta, off)
	s := c.sampleSpineAt(1)
	dx, dy, dz := pos.X-s.pos.X, pos.Y-s.pos.Y, pos.Z-s.pos.Z
	dist := float32(math.Sqrt(float64(dx*dx + dy*dy + dz*dz)))
	if !approx(dist, off, 1e-4) {
		t.Errorf("cap pos offset = %v, want %v", dist, off)
	}
	// outward should equal the spine tangent (not the radial).
	if !approxVec(outward, s.tan, 1e-4) {
		t.Errorf("cap outward = %v, want tangent %v", outward, s.tan)
	}
}

func TestAppendHead_ExtendsForward(t *testing.T) {
	c := New()
	n0 := c.BoneCount()
	last := c.Bones()[n0-1]
	step := Vec3{
		X: last.Pos.X - c.Bones()[n0-2].Pos.X,
		Y: last.Pos.Y - c.Bones()[n0-2].Pos.Y,
		Z: last.Pos.Z - c.Bones()[n0-2].Pos.Z,
	}
	idx := c.AppendHead()
	if idx != n0 {
		t.Errorf("AppendHead idx = %d, want %d", idx, n0)
	}
	if c.BoneCount() != n0+1 {
		t.Errorf("BoneCount = %d, want %d", c.BoneCount(), n0+1)
	}
	got := c.Bones()[n0].Pos
	want := Vec3{X: last.Pos.X + step.X, Y: last.Pos.Y + step.Y, Z: last.Pos.Z + step.Z}
	if !approxVec(got, want, 1e-4) {
		t.Errorf("new head pos = %v, want %v", got, want)
	}
}

func TestAppendTail_ExtendsBackward(t *testing.T) {
	c := New()
	n0 := c.BoneCount()
	first := c.Bones()[0]
	step := Vec3{
		X: first.Pos.X - c.Bones()[1].Pos.X,
		Y: first.Pos.Y - c.Bones()[1].Pos.Y,
		Z: first.Pos.Z - c.Bones()[1].Pos.Z,
	}
	idx := c.AppendTail()
	if idx != 0 {
		t.Errorf("AppendTail idx = %d, want 0", idx)
	}
	if c.BoneCount() != n0+1 {
		t.Errorf("BoneCount = %d, want %d", c.BoneCount(), n0+1)
	}
	got := c.Bones()[0].Pos
	want := Vec3{X: first.Pos.X + step.X, Y: first.Pos.Y + step.Y, Z: first.Pos.Z + step.Z}
	if !approxVec(got, want, 1e-4) {
		t.Errorf("new tail pos = %v, want %v", got, want)
	}
}

func TestRemoveBone_RefusesUnderTwo(t *testing.T) {
	c := New()
	for c.BoneCount() > 2 {
		if !c.RemoveHead() {
			t.Fatal("RemoveHead refused to remove with >2 bones")
		}
	}
	if c.RemoveHead() {
		t.Errorf("RemoveHead succeeded with only 2 bones; expected refusal")
	}
	if c.RemoveTail() {
		t.Errorf("RemoveTail succeeded with only 2 bones; expected refusal")
	}
}

func TestMoveBone_UpdatesShape(t *testing.T) {
	c := New()
	idx := c.BoneCount() - 1
	want := Vec3{X: 0, Y: 2, Z: 3}
	if !c.MoveBone(idx, want) {
		t.Fatal("MoveBone returned false on a real change")
	}
	if c.MoveBone(idx, want) {
		t.Error("MoveBone returned true on no-op")
	}
	controls, _ := c.ComputeShape()
	if controls[idx] != want {
		t.Errorf("controls[%d] = %v, want %v", idx, controls[idx], want)
	}
}

func TestClosestAnchor_CapPreservesRadial(t *testing.T) {
	c := New()
	// Construct a point ON the head cap disc (perpendicular to the
	// tangent at the head endpoint, offset by some radial distance)
	// and check ClosestAnchor reports t=1 + a matching offset.
	const theta = float32(0.4)
	const off = float32(0.2)
	pos, _, _ := c.AnchorPoint(1, theta, off)
	gotT, _, gotOff := c.ClosestAnchor(pos)
	if gotT != 1 {
		t.Errorf("t: got %v, want 1", gotT)
	}
	if !approx(gotOff, off, 1e-3) {
		t.Errorf("off: got %v, want %v", gotOff, off)
	}
}
