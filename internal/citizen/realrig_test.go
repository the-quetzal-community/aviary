package citizen_test

import (
	"os"
	"testing"

	"the.quetzal.community/aviary/internal/citizen"
)

const libRoot = "/run/media/quentin/CreativeCommons/library/graphics/library/makehuman"

// TestRealCitizenRigStats validates the rig build against the actual library
// GLBs. Skipped when the external library drive isn't mounted.
func TestRealCitizenRigStats(t *testing.T) {
	if _, err := os.Stat(libRoot + "/citizen_animated.glb"); err != nil {
		t.Skip("library drive not mounted")
	}
	anim, err := citizen.LoadGLB(libRoot + "/citizen_animated.glb")
	if err != nil {
		t.Fatal(err)
	}
	base, err := citizen.LoadBaseMesh(libRoot + "/base.obj")
	if err != nil {
		t.Fatal(err)
	}
	const scale = 0.10204755840381696
	translate := citizen.Vec3{X: 0, Y: 0.8334835767745972, Z: 0}

	rig, err := citizen.BuildCitizenRig(anim, base, scale, translate)
	if err != nil {
		t.Fatal(err)
	}

	matched, unmatched := 0, 0
	usedCanonical := map[int32]bool{}
	for _, c := range rig.Canonical {
		if c < 0 {
			unmatched++
			continue
		}
		matched++
		usedCanonical[c] = true
	}
	bones := map[uint16]int{}
	for _, j := range rig.Joints {
		bones[j[0]]++
	}
	t.Logf("GLB verts: %d  base canonical: %d", len(rig.Bind), len(base.Verts))
	t.Logf("matched: %d  unmatched: %d  canonical covered: %d  bones: %d  tris: %d",
		matched, unmatched, len(usedCanonical), len(bones), len(rig.Indices)/3)

	// Nearly every GLB vertex should resolve; only the handful of cross-body
	// UV-seam collisions are intentionally rejected (left unmatched so they
	// don't wire e.g. a neck vert to the legs).
	if unmatched > 50 {
		t.Fatalf("%d GLB verts unmatched — expected only a few UV-seam-collision rejects", unmatched)
	}
	if len(bones) < 60 {
		t.Fatalf("only %d dominant bones; expected ~64 of the 66-bone rig", len(bones))
	}

	// UV match quality: each animated vert's UV should land ~on its matched
	// canonical vert's UV. Seam verts (one canonical UV vs the welded vert's
	// UV) can drift a little; that's tolerable, so this only logs.
	var maxUV float32
	over := 0
	for i, c := range rig.Canonical {
		if c < 0 || int(c) >= len(base.UVs) {
			continue
		}
		du := anim.UVs[i].U - base.UVs[c].U
		dv := anim.UVs[i].V - base.UVs[c].V
		d := du*du + dv*dv
		if d > maxUV {
			maxUV = d
		}
		if d > 0.0004 { // > 0.02 in UV space
			over++
		}
	}
	t.Logf("max UV match dist: %.5f   matches >0.02uv: %d of %d", sqrt32(maxUV), over, len(rig.Canonical))
}

func sqrt32(v float32) float32 {
	if v <= 0 {
		return 0
	}
	x := v
	for i := 0; i < 30; i++ {
		x = 0.5 * (x + v/x)
	}
	return x
}
