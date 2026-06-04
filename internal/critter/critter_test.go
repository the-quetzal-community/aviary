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

// TestRestore_RoundTrip is the snapshot contract: a critter state captured
// via Bones()/Legs()/Weights() and re-installed via Restore() must be
// byte-identical to the original — that fidelity is what lets the load-time
// snapshot skip replaying the thousands of sculpts that built the shape.
func TestRestore_RoundTrip(t *testing.T) {
	// Build a non-trivial state: grow the chain, edit bones, add legs,
	// edit a joint, set some weights — i.e. the kinds of edits the 40k
	// recorded sculpts fold into.
	src := New()
	src.AppendHead()
	src.AppendTail()
	src.MoveBone(2, Vec3{X: 0, Y: 0.35, Z: 0.1})
	src.SetBoneRadius(1, 0.42)
	src.AppendLeg()
	src.AppendLegAt(3)
	src.SetLegJoint(0, LegKnee, Vec3{X: 0.4, Y: -0.2, Z: -0.1})
	src.SetLegJointRadius(1, LegFoot, 0.06)
	src.SetWeight("shape/length", 0.7)
	src.SetWeight("shape/head_size", -0.3)

	bones := src.Bones()
	legs := src.Legs()
	weights := src.Weights()

	// Restore into a fresh critter that started from a DIFFERENT shape, so
	// a partial restore (e.g. forgetting to replace legs) would show up.
	dst := New()
	dst.AppendLeg()
	dst.SetWeight("shape/length", -1)
	dst.Restore(bones, legs, weights)

	if dst.BoneCount() != src.BoneCount() {
		t.Fatalf("bone count = %d, want %d", dst.BoneCount(), src.BoneCount())
	}
	for i, b := range src.BonesView() {
		gb, _ := dst.BoneAt(i)
		if !approxVec(gb.Pos, b.Pos, 0) || gb.Radius != b.Radius {
			t.Errorf("bone %d = %+v, want %+v", i, gb, b)
		}
	}
	if dst.LegCount() != src.LegCount() {
		t.Fatalf("leg count = %d, want %d", dst.LegCount(), src.LegCount())
	}
	for i, l := range src.LegsView() {
		gl := dst.LegsView()[i]
		if gl != l {
			t.Errorf("leg %d = %+v, want %+v", i, gl, l)
		}
	}
	if got, want := dst.Weights(), weights; len(got) != len(want) {
		t.Fatalf("weights len = %d, want %d", len(got), len(want))
	} else {
		for k, v := range want {
			if got[k] != v {
				t.Errorf("weight %q = %v, want %v", k, got[k], v)
			}
		}
	}
}

// TestRestore_NoAliasing guards against the restored critter sharing
// backing storage with the caller's snapshot slices/map — a later edit to
// one must not bleed into the other (the snapshot is a frozen baseline).
func TestRestore_NoAliasing(t *testing.T) {
	src := New()
	src.AppendLeg()
	src.SetWeight("shape/length", 0.5)
	bones, legs, weights := src.Bones(), src.Legs(), src.Weights()

	dst := New()
	dst.Restore(bones, legs, weights)

	// Mutate the snapshot inputs after restore; dst must be unaffected.
	bones[0].Radius = 99
	if len(legs) > 0 {
		legs[0].HipRadius = 99
	}
	weights["shape/length"] = 99

	if gb, _ := dst.BoneAt(0); gb.Radius == 99 {
		t.Error("bone radius aliased the caller's slice")
	}
	if dst.LegCount() > 0 && dst.LegsView()[0].HipRadius == 99 {
		t.Error("leg radius aliased the caller's slice")
	}
	if dst.Weight("shape/length") == 99 {
		t.Error("weight aliased the caller's map")
	}
}

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

func TestAppendLeg_DefaultPose(t *testing.T) {
	c := New()
	idx := c.AppendLeg()
	if idx != 0 {
		t.Fatalf("AppendLeg idx = %d, want 0", idx)
	}
	if c.LegCount() != 1 {
		t.Errorf("LegCount = %d, want 1", c.LegCount())
	}
	leg := c.Legs()[0]
	// Default attach should land somewhere in the chain, not past the
	// ends — defaultLegAttach uses len/4 clamped to ≥1 on the default
	// 5-bone critter.
	if leg.Attach <= 0 || leg.Attach >= c.BoneCount()-1 {
		t.Errorf("Attach = %d, want interior bone in [1,%d)", leg.Attach, c.BoneCount()-1)
	}
	// Joints should be stored on the +X side; foot should sit below
	// the hip (more negative Y) so the rest pose looks plausibly
	// downward.
	if leg.Hip.X < 0 || leg.Knee.X < 0 || leg.Foot.X < 0 {
		t.Errorf("joints must store +X coords; got %+v", leg)
	}
	if !(leg.Foot.Y < leg.Knee.Y && leg.Knee.Y < leg.Hip.Y) {
		t.Errorf("Y ordering broken: hip=%v knee=%v foot=%v", leg.Hip.Y, leg.Knee.Y, leg.Foot.Y)
	}
}

func TestSetLegJoint_NormalisesX(t *testing.T) {
	c := New()
	c.AppendLeg()
	// Passing a negative X should be reflected to positive — legs are
	// mirrored, so the storage convention is +X only.
	if !c.SetLegJoint(0, LegKnee, Vec3{X: -0.4, Y: -0.2, Z: 0.1}) {
		t.Fatal("SetLegJoint returned false on a real change")
	}
	knee := c.Legs()[0].Knee
	if knee.X != 0.4 {
		t.Errorf("X = %v, want 0.4 (positive mirror)", knee.X)
	}
	if knee.Y != -0.2 || knee.Z != 0.1 {
		t.Errorf("Y/Z not stored: got %+v", knee)
	}
}

func TestSetLegJointAxis_Idempotent(t *testing.T) {
	c := New()
	c.AppendLeg()
	if !c.SetLegJointAxis(0, LegKnee, 2, 0.2) {
		t.Fatal("first SetLegJointAxis should report change")
	}
	if c.SetLegJointAxis(0, LegKnee, 2, 0.2) {
		t.Error("repeated SetLegJointAxis to same value should report no change")
	}
}

func TestSetLegJoint_ClampsToGround(t *testing.T) {
	c := New()
	c.AppendLeg()
	c.SetLegJoint(0, LegFoot, Vec3{X: 0.3, Y: -5, Z: 0})
	if got := c.Legs()[0].Foot.Y; got != GroundY {
		t.Errorf("Foot Y after underground set = %v, want %v (GroundY)", got, GroundY)
	}
	c.SetLegJointAxis(0, LegKnee, 1, -10)
	if got := c.Legs()[0].Knee.Y; got != GroundY {
		t.Errorf("Knee Y after underground axis set = %v, want %v", got, GroundY)
	}
}

func TestRemoveLeg_ShiftsIndices(t *testing.T) {
	c := New()
	c.AppendLeg()
	c.AppendLeg()
	c.AppendLeg()
	if !c.RemoveLeg(1) {
		t.Fatal("RemoveLeg refused")
	}
	if c.LegCount() != 2 {
		t.Errorf("LegCount = %d, want 2", c.LegCount())
	}
	if c.RemoveLeg(5) {
		t.Error("RemoveLeg accepted out-of-range index")
	}
}

func TestAppendTail_BumpsLegAttach(t *testing.T) {
	c := New()
	c.AppendLeg()
	before := c.Legs()[0].Attach
	c.AppendTail()
	after := c.Legs()[0].Attach
	if after != before+1 {
		t.Errorf("leg Attach after AppendTail = %d, want %d", after, before+1)
	}
}

func TestRemoveTail_DecrementsLegAttach(t *testing.T) {
	c := New()
	c.AppendLeg()
	before := c.Legs()[0].Attach
	c.RemoveTail()
	after := c.Legs()[0].Attach
	if before > 0 && after != before-1 {
		t.Errorf("leg Attach after RemoveTail = %d, want %d", after, before-1)
	}
}

func TestRemoveHead_ClampsLegAttach(t *testing.T) {
	c := New()
	// Attach a leg explicitly to the head bone so RemoveHead has to
	// clamp it.
	tip := c.BoneCount() - 1
	c.AppendLegAt(tip)
	c.RemoveHead()
	got := c.Legs()[0].Attach
	if got != c.BoneCount()-1 {
		t.Errorf("Attach after RemoveHead = %d, want %d (new tip)", got, c.BoneCount()-1)
	}
}

func TestBuildLegMesh_HasMirrorAndIndicesInRange(t *testing.T) {
	c := New()
	c.AppendLeg()
	leg := c.Legs()[0]
	const rings, around = 4, 6
	m := c.BuildLegMesh(leg, rings, around, true)
	// Single continuous tube: rings*2 - 1 ring samples (shared knee)
	// times segmentsAround verts per ring, plus one foot cap vert.
	// Mirror doubles the total.
	wantPerSide := (rings*2-1)*around + 1
	if len(m.Verts) != wantPerSide*2 {
		t.Errorf("Verts = %d, want %d (per-side x 2)", len(m.Verts), wantPerSide*2)
	}
	if len(m.Normals) != len(m.Verts) {
		t.Errorf("Normals=%d, Verts=%d — must match", len(m.Normals), len(m.Verts))
	}
	for i, idx := range m.Indices {
		if int(idx) < 0 || int(idx) >= len(m.Verts) {
			t.Fatalf("index[%d] = %d out of range [0,%d)", i, idx, len(m.Verts))
		}
	}
	// Mirror half should contain a vert with negated X for each
	// vert in the primary half.
	mid := wantPerSide
	for i := 0; i < mid; i++ {
		if !approx(m.Verts[i].X, -m.Verts[mid+i].X, 1e-4) {
			t.Errorf("mirror X mismatch at %d: %v vs %v", i, m.Verts[i].X, m.Verts[mid+i].X)
		}
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
