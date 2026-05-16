//go:build smoke

package citizen

import (
	"fmt"
	"testing"
)

// Run with: go test -tags smoke ./internal/citizen/ -run TestSmokeLoadFromLibrary -v
// Requires the CC asset library mounted at /run/media/quentin/CreativeCommons.
func TestSmokeLoadFromLibrary(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"head/head-fat-incr",
		"nose/nose-flaring-incr",
		"mouth/mouth-cupidsbow-incr",
	} {
		path := "/run/media/quentin/CreativeCommons/library/graphics/library/makehuman/targets/" + name + ".target"
		tgt, err := LoadTarget(path, name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		fmt.Printf("%s: %d deltas; first idx=%d %+v\n",
			tgt.Name, len(tgt.Deltas), tgt.Deltas[0].Index, tgt.Deltas[0].Offset)
	}
}
