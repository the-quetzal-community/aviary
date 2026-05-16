//go:build smoke

package citizen

import (
	"fmt"
	"testing"
)

// Run with: go test -tags smoke ./internal/citizen/ -run TestSmokeParseBaseOBJ -v
func TestSmokeParseBaseOBJ(t *testing.T) {
	m, err := LoadBaseMesh("/run/media/quentin/CreativeCommons/library/graphics/library/makehuman/base.obj")
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("base.obj: %d verts, %d indices (%d triangles)\n",
		len(m.Verts), len(m.Indices), len(m.Indices)/3)
}
