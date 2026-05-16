package citizen

import "testing"

func TestCitizen_RecomputeWithoutWeights(t *testing.T) {
	base := []Vec3{{0, 0, 0}, {1, 1, 1}, {2, 2, 2}}
	c := New(base)
	got := c.Recompute()
	for i, v := range got {
		if v != base[i] {
			t.Errorf("vertex[%d] = %+v, want %+v", i, v, base[i])
		}
	}
}

func TestCitizen_RecomputeWithSingleTarget(t *testing.T) {
	c := New([]Vec3{{0, 0, 0}, {1, 1, 1}, {2, 2, 2}})
	c.AddTarget(&Target{
		Name: "stretch",
		Deltas: []Delta{
			{Index: 1, Offset: Vec3{0.5, 0, 0}},
		},
	})

	c.SetWeight("stretch", 0.5)
	got := c.Recompute()
	if got[1] != (Vec3{1.25, 1, 1}) {
		t.Errorf("at weight 0.5, vertex 1 = %+v, want (1.25, 1, 1)", got[1])
	}

	c.SetWeight("stretch", 1.0)
	got = c.Recompute()
	if got[1] != (Vec3{1.5, 1, 1}) {
		t.Errorf("at weight 1.0, vertex 1 = %+v, want (1.5, 1, 1)", got[1])
	}

	// Untouched vertices stay at base.
	if got[0] != (Vec3{0, 0, 0}) {
		t.Errorf("untouched vertex 0 = %+v", got[0])
	}
	if got[2] != (Vec3{2, 2, 2}) {
		t.Errorf("untouched vertex 2 = %+v", got[2])
	}
}

func TestCitizen_RecomputeIsAdditive(t *testing.T) {
	c := New([]Vec3{{0, 0, 0}, {0, 0, 0}})
	c.AddTarget(&Target{
		Name:   "a",
		Deltas: []Delta{{Index: 0, Offset: Vec3{1, 0, 0}}},
	})
	c.AddTarget(&Target{
		Name:   "b",
		Deltas: []Delta{{Index: 0, Offset: Vec3{0, 1, 0}}},
	})
	c.SetWeight("a", 1)
	c.SetWeight("b", 1)
	got := c.Recompute()
	if got[0] != (Vec3{1, 1, 0}) {
		t.Errorf("vertex 0 = %+v, want (1, 1, 0)", got[0])
	}
}

func TestCitizen_SetWeightZeroClears(t *testing.T) {
	c := New([]Vec3{{0, 0, 0}})
	c.AddTarget(&Target{Name: "t", Deltas: []Delta{{Index: 0, Offset: Vec3{1, 0, 0}}}})

	c.SetWeight("t", 0.5)
	if c.Weight("t") != 0.5 {
		t.Errorf("Weight = %v, want 0.5", c.Weight("t"))
	}
	c.SetWeight("t", 0)
	if _, ok := c.weights["t"]; ok {
		t.Errorf("weight 0 should drop entry from working set")
	}
	got := c.Recompute()
	if got[0] != (Vec3{0, 0, 0}) {
		t.Errorf("after clearing weight, vertex 0 = %+v", got[0])
	}
}

func TestCitizen_RecomputeReusesBuffer(t *testing.T) {
	c := New([]Vec3{{0, 0, 0}, {1, 1, 1}})
	a := c.Recompute()
	b := c.Recompute()
	// Same backing slice, so the second call should reuse memory.
	if &a[0] != &b[0] {
		t.Error("Recompute returned a fresh slice; expected reused buffer")
	}
}

func TestCitizen_NewIsolatesBase(t *testing.T) {
	src := []Vec3{{1, 2, 3}}
	c := New(src)
	src[0] = Vec3{99, 99, 99} // caller mutates
	got := c.Recompute()
	if got[0] != (Vec3{1, 2, 3}) {
		t.Errorf("New should defensively copy base; vertex 0 = %+v", got[0])
	}
}

func TestCitizen_SetDressing(t *testing.T) {
	c := New([]Vec3{{0, 0, 0}})

	// First set on an empty slot — changed.
	if !c.SetDressing("helmets", "res://library/kenney/helmets/spartan.glb") {
		t.Error("first SetDressing should report a change")
	}
	if got := c.Dressing("helmets"); got != "res://library/kenney/helmets/spartan.glb" {
		t.Errorf("Dressing = %q, want spartan path", got)
	}

	// Setting the same value — not changed.
	if c.SetDressing("helmets", "res://library/kenney/helmets/spartan.glb") {
		t.Error("repeated SetDressing should report no change")
	}

	// Changing — changed.
	if !c.SetDressing("helmets", "res://library/kenney/helmets/viking.glb") {
		t.Error("changing SetDressing should report a change")
	}

	// Empty design unequips.
	if !c.SetDressing("helmets", "") {
		t.Error("clearing should report a change")
	}
	if got := c.Dressing("helmets"); got != "" {
		t.Errorf("after clearing, Dressing = %q, want empty", got)
	}
	if _, ok := c.dressings["helmets"]; ok {
		t.Error("empty design should delete the slot entry")
	}
}

func TestCitizen_DressingsSnapshot(t *testing.T) {
	c := New([]Vec3{{0, 0, 0}})
	c.SetDressing("helmets", "h")
	c.SetDressing("sandals", "s")

	snap := c.Dressings()
	if len(snap) != 2 || snap["helmets"] != "h" || snap["sandals"] != "s" {
		t.Errorf("snapshot = %+v, want {helmets: h, sandals: s}", snap)
	}

	// Snapshot should be a copy — mutating it should not affect Citizen.
	snap["helmets"] = "tampered"
	if c.Dressing("helmets") != "h" {
		t.Error("Dressings snapshot should be independent of internal state")
	}
}
