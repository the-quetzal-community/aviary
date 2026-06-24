package citizen_test

import (
	"math"
	"os"
	"testing"

	"the.quetzal.community/aviary/internal/citizen"
)

// TestUVMatchCrossRegion flags GLB verts whose UV match landed on a canonical
// vertex in a very different part of the body — the signature of a UV-seam
// mismatch (e.g. a neck vert matched to a leg vertex), which would stretch
// clothing/morphs across the body. The legs were barely straightened and the
// head/neck/torso not at all, so a large vertical gap between a vert's own
// (inverse-transformed) position and its matched canonical's position there is
// a bad match, not the straighten.
func TestUVMatchCrossRegion(t *testing.T) {
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
	translate := citizen.Vec3{Y: 0.8334835767745972}
	rig, err := citizen.BuildCitizenRig(anim, base, scale, translate)
	if err != nil {
		t.Fatal(err)
	}

	inv := float32(1) / scale
	// After the height-jump rejection, no MATCHED vert should sit more than the
	// arm-straighten range (~4 units) from its canonical in height — the
	// cross-body UV-seam collisions are dropped to unmatched instead.
	bad := 0
	for i, c := range rig.Canonical {
		if c < 0 || int(c) >= len(base.Verts) {
			continue
		}
		objY := (anim.Positions[i].Y - translate.Y) * inv
		dY := objY - base.Verts[c].Y
		if dY < 0 {
			dY = -dY
		}
		if dY > 5.0 {
			bad++
			if bad <= 5 {
				t.Logf("GLB vert %d objY=%.2f matched canon %d Y=%.2f Δ=%.2f", i, objY, c, base.Verts[c].Y, dY)
			}
		}
	}
	if bad != 0 {
		t.Fatalf("%d matched verts jump >5 units in height (UV-seam collisions not rejected)", bad)
	}
	_ = math.Abs
}
