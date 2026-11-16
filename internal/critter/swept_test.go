package critter

import (
	"math"
	"testing"
)

// frontFacesOutward reports the fraction of triangles wound the way
// Godot treats as front-facing (clockwise seen from outside), judged
// against the averaged vertex normals.
func frontFacesOutward(m Mesh) float64 {
	good, total := 0, 0
	for i := 0; i+2 < len(m.Indices); i += 3 {
		a, b, c := m.Verts[m.Indices[i]], m.Verts[m.Indices[i+1]], m.Verts[m.Indices[i+2]]
		geo := cross(sub(b, a), sub(c, a))
		if dotV(geo, geo) < 1e-14 {
			continue // degenerate (e.g. closed tip)
		}
		n := add(add(m.Normals[m.Indices[i]], m.Normals[m.Indices[i+1]]), m.Normals[m.Indices[i+2]])
		total++
		if dotV(geo, n) < 0 {
			good++
		}
	}
	if total == 0 {
		return 0
	}
	return float64(good) / float64(total)
}

func TestSweptWindingMatchesBody(t *testing.T) {
	// The body tube is the reference for the engine's winding.
	if f := frontFacesOutward(New().BuildMesh(24, 12)); f < 0.99 {
		t.Fatalf("body tube reference winding: %.2f front-facing", f)
	}
	for _, p := range []SweptProfile{ProfileHorn, ProfileFin} {
		for _, s := range []PartShape{{}, {Curl: 2}, {Curl: -3, Length: 1, Girth: -1}} {
			// A blade wider than its bend radius folds over itself on the
			// inside of a tight curl, so a few faces legitimately invert.
			if f := frontFacesOutward(BuildSweptMesh(s, p, 10, 10)); f < 0.95 {
				t.Errorf("profile %+v shape %+v: %.2f front-facing", p, s, f)
			}
		}
	}
}

func TestSweptZeroShapeIsNeutralCone(t *testing.T) {
	length, curl, girth := PartShape{}.Dims()
	if length != sweptNeutralLength || curl != 0 || girth != sweptNeutralGirth {
		t.Fatalf("zero shape = %v %v %v", length, curl, girth)
	}
	tip := PartShape{}.Tip()
	if tip.X != 0 || tip.Y != 0 || math.Abs(float64(tip.Z-sweptNeutralLength)) > 1e-6 {
		t.Fatalf("neutral tip = %+v", tip)
	}
}

func TestSweptTipRoundTrip(t *testing.T) {
	for _, s := range []PartShape{
		{}, {Length: 1}, {Curl: 1.2}, {Length: -1, Curl: -2.5}, {Length: 2, Curl: 4, Girth: 1},
	} {
		tip := s.Tip()
		got := s.WithTip(tip.Y, tip.Z)
		if math.Abs(float64(got.Length-s.Length)) > 1e-3 || math.Abs(float64(got.Curl-s.Curl)) > 1e-3 || got.Girth != s.Girth {
			t.Errorf("WithTip(Tip(%+v)) = %+v", s, got)
		}
	}
}

func TestSweptGirthRoundTrip(t *testing.T) {
	s := PartShape{Girth: 1.5}
	_, _, g := s.Dims()
	if got := (PartShape{}).WithGirth(g); math.Abs(float64(got.Girth-1.5)) > 1e-4 {
		t.Fatalf("WithGirth = %+v", got)
	}
}

func TestSweptClampsHostileShape(t *testing.T) {
	inf := float32(math.Inf(1))
	m := BuildSweptMesh(PartShape{Length: inf, Curl: -inf, Girth: 99}, ProfileHorn, 8, 8)
	for _, v := range m.Verts {
		if math.IsNaN(float64(v.X+v.Y+v.Z)) || math.IsInf(float64(v.X+v.Y+v.Z), 0) {
			t.Fatalf("non-finite vertex %+v", v)
		}
	}
}
