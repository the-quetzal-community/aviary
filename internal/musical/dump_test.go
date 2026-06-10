package musical

import (
	"errors"
	"fmt"
	"io"
	"os"
	"testing"
)

// TestDumpMus3 decodes a .mus3 save part and prints every entry. Point it at a
// file with MUS3=/path/to/part.mus3 and run: gd test ./internal/musical/ -run TestDumpMus3 -v
func TestDumpMus3(t *testing.T) {
	path := os.Getenv("MUS3")
	if path == "" {
		t.Skip("set MUS3=/path/to/part.mus3")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var header [len(MagicHeader)]byte
	if _, err := io.ReadFull(f, header[:]); err != nil {
		t.Fatal("header:", err)
	}
	if string(header[:]) != MagicHeader {
		t.Fatalf("bad header: %q", header[:])
	}
	imports := map[Design]string{}
	changeCount := map[Entity]int{}
	n := 0
	for {
		entry, err := decode(f)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("decode at entry %d: %v", n, err)
		}
		n++
		switch e := entry.(type) {
		case Import:
			imports[e.Design] = e.Import
		case Change:
			uri := imports[e.Design]
			changeCount[e.Entity]++
			fmt.Printf("[%4d] CHANGE entity={%d,%d} design={%d,%d} remove=%v commit=%v editor=%q offset=(%.2f,%.2f,%.2f) bounds=(%.3f,%.3f,%.3f) timing=%d uri=%s\n",
				n, e.Entity.Author, e.Entity.Number, e.Design.Author, e.Design.Number,
				e.Remove, e.Commit, e.Editor, e.Offset.X, e.Offset.Y, e.Offset.Z,
				e.Bounds.X, e.Bounds.Y, e.Bounds.Z, int64(e.Timing), uri)
		}
	}
	fmt.Printf("\n==== total entries: %d ====\n", n)
	// Summarise which entities reference critter designs.
	fmt.Println("---- entities placing everything/critter designs ----")
	critterDesigns := map[Design]string{}
	for d, uri := range imports {
		if len(uri) >= 18 && containsSub(uri, "everything/critter") {
			critterDesigns[d] = uri
			fmt.Printf("  critter design {%d,%d} = %s\n", d.Author, d.Number, uri)
		}
	}
}

func containsSub(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
