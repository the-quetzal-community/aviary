package citizen_test

import (
	"os"
	"testing"

	"the.quetzal.community/aviary/internal/citizen"
)

// TestHatAnchorResolution checks whether a hat's .mhclo anchor verts resolve to
// GLB verts via the rig's CanonicalToGLB map. A flat/collapsed clothing render
// is the signature of anchors falling through (CanonicalToGLB == -1), leaving
// only the offset. Skipped without the library drive.
func TestHatAnchorResolution(t *testing.T) {
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
	rig, err := citizen.BuildCitizenRig(anim, base,
		0.10204755840381696, citizen.Vec3{Y: 0.8334835767745972})
	if err != nil {
		t.Fatal(err)
	}

	hat := libRoot + "/helmets/aethelraed_unraed_cloche_hat.mhclo"
	f, err := os.Open(hat)
	if err != nil {
		t.Skip("hat mhclo not found")
	}
	defer f.Close()
	mh, err := citizen.ParseMHClo(hat, f)
	if err != nil {
		t.Fatal(err)
	}

	resolved, partial, none := 0, 0, 0
	maxAnchor := int32(-1)
	for _, a := range mh.Anchors {
		hit := 0
		for k := 0; k < 3; k++ {
			c := a.Verts[k]
			if c > maxAnchor {
				maxAnchor = c
			}
			if int(c) >= 0 && int(c) < len(rig.CanonicalToGLB) && rig.CanonicalToGLB[c] >= 0 {
				hit++
			}
		}
		switch hit {
		case 3:
			resolved++
		case 0:
			none++
		default:
			partial++
		}
	}
	covered := 0
	for _, g := range rig.CanonicalToGLB {
		if g >= 0 {
			covered++
		}
	}
	t.Logf("hat anchors=%d  resolved(3/3)=%d partial=%d none=%d", len(mh.Anchors), resolved, partial, none)
	t.Logf("max anchor vert index=%d  base verts=%d  CanonicalToGLB covered=%d/%d",
		maxAnchor, len(base.Verts), covered, len(rig.CanonicalToGLB))
}
