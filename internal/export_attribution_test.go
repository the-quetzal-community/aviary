package internal

import (
	"strings"
	"testing"
)

// TestBuildAttributionManifest pins what an export's ATTRIBUTION.txt
// says: every author whose artwork is in the file, each under the
// licence that actually applies to their items, plus an honest count of
// what carries no record. The player's usage rights depend on this
// being right, so the cases here are the ones that could silently
// misattribute: a folder whose items differ in licence, and a mod with
// no record at all.
func TestBuildAttributionManifest(t *testing.T) {
	records := map[string]Attribution{
		"makehuman": {
			Name: "MakeHuman Community", URL: "http://www.makehumancommunity.org/", License: ccZero,
			Items: map[string]ItemAttribution{
				"helmets/elvs_tophat1.obj": {Author: "Elvaerwyn", License: ccBY, URL: "http://www.makehumancommunity.org/node/1"},
			},
		},
		"kenney": {Name: "Kenney", URL: "https://kenney.nl/", License: ccZero},
	}
	manifest := buildAttributionManifest(records, "aviary-coaster.glb", []string{
		"res://library/kenney/track_f/wood-straight.glb",
		"res://library/kenney/track_r/wood-corner-large.glb",
		"res://library/makehuman/helmets/elvs_tophat1.obj",
		"res://library/makehuman/jackets/plain.obj",
		"mod://somebody/thing.glb",
	})
	if manifest == "" {
		t.Fatal("manifest should not be empty when attributed artwork is present")
	}
	for _, want := range []string{
		"aviary-coaster.glb",
		"Kenney",
		"https://kenney.nl/",
		"track_f/wood-straight.glb",
		"kenney/track_f/wood-straight.glb", // path stays resolvable, res:// prefix trimmed
		"Elvaerwyn",
		"CC BY 4.0",
		"MakeHuman Community",
		"CC0 1.0",
		"1 further item(s)",
	} {
		if !strings.Contains(manifest, want) {
			t.Errorf("manifest missing %q:\n%s", want, manifest)
		}
	}
	if strings.Contains(manifest, "res://") {
		t.Errorf("manifest should not leak res:// paths:\n%s", manifest)
	}
	// The CC-BY hat must not drag the rest of MakeHuman's CC0 items into
	// a licence they aren't under: Elvaerwyn's entry carries the hat
	// alone, and the community's entry carries the jacket alone.
	elvaerwyn := section(manifest, "Elvaerwyn")
	if !strings.Contains(elvaerwyn, "helmets/elvs_tophat1.obj") || strings.Contains(elvaerwyn, "jackets/plain.obj") {
		t.Errorf("per-item licence override mixed up the items:\n%s", elvaerwyn)
	}
	community := section(manifest, "MakeHuman Community")
	if !strings.Contains(community, "jackets/plain.obj") || strings.Contains(community, "helmets/elvs_tophat1.obj") {
		t.Errorf("folder-licence entry mixed up the items:\n%s", community)
	}

	// Nothing attributable: no claim to make, so no file to write.
	if got := buildAttributionManifest(records, "x.glb", []string{"mod://somebody/thing.glb"}); got != "" {
		t.Errorf("unattributed-only export should produce no manifest, got:\n%s", got)
	}
}

// section returns the manifest block that starts with the given author,
// up to the next blank line.
func section(manifest, author string) string {
	for _, block := range strings.Split(manifest, "\n\n") {
		if strings.HasPrefix(block, author+" —") {
			return block
		}
	}
	return ""
}

func TestAttributionPathFor(t *testing.T) {
	for dst, want := range map[string]string{
		"/home/me/aviary-coaster.glb":    "/home/me/aviary-coaster-ATTRIBUTION.txt",
		"/home/me/my creation.gltf":      "/home/me/my creation-ATTRIBUTION.txt",
		"/home/me/no-extension":          "/home/me/no-extension-ATTRIBUTION.txt",
		"/home/me/dotted.name.thing.glb": "/home/me/dotted.name.thing-ATTRIBUTION.txt",
	} {
		if got := attributionPathFor(dst); got != want {
			t.Errorf("attributionPathFor(%q) = %q, want %q", dst, got, want)
		}
	}
}

// TestSortedUnique covers what collectDesignURIs does with the walked
// tree and an editor's Attributed list once they are put together: a
// design used a hundred times is credited once, nodes that came from no
// scene drop out, and the order doesn't depend on how the tree was
// walked.
func TestSortedUnique(t *testing.T) {
	got := sortedUnique([]string{
		"res://library/kenney/b.glb",
		"",
		"res://library/kenney/a.glb",
		"res://library/kenney/b.glb",
		"res://library/everything/c.glb",
		"",
	})
	want := []string{
		"res://library/everything/c.glb",
		"res://library/kenney/a.glb",
		"res://library/kenney/b.glb",
	}
	if len(got) != len(want) {
		t.Fatalf("sortedUnique = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sortedUnique = %v, want %v", got, want)
		}
	}
}
