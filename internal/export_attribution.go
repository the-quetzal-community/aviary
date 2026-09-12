package internal

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"graphics.gd/classdb/Node"
)

// export_attribution.go writes the ATTRIBUTION.txt that travels with an
// exported creation. The promise on the store page is that you keep full
// usage rights to what you make here — that is only honest if the export
// carries the record needed to exercise them: who made each piece of
// artwork in it, where it came from, and under which Creative Commons
// licence. A CC-BY model is only usable downstream by someone who knows
// whom to credit.
//
// The manifest is derived from the exported subtree itself rather than
// from what the editor happens to have on screen: every node
// instantiated from a library scene remembers the path it came from
// (Godot sets scene_file_path on instantiation, and preserves it
// through Node.Duplicate's default flags, which is what the vehicle
// editor's ExportSubtree uses), so walking the tree that is about to be
// packed lists exactly the artwork the .glb contains.
//
// Two kinds of artwork are invisible to that walk, because they are
// never scene instances: the terrain's instanced dressing (grass and
// scatter, rendered as MultiMeshes) and the painted terrain materials
// (textures uploaded into a shader array). An editor holding artwork
// like that implements [Attributed] to name it — see TerrainEditor.
//
// Library designs with no attribution record are counted but not
// credited to anyone: nothing is invented here. An unrecorded author is
// a gap in the library records to fix there, not something to paper
// over in the export. Drop-in mods are packed from runtime-loaded glTF
// and carry no resource path at all, so they do not appear even in that
// count — attributing mods is still open (see the roadmap).

// exportAttribution is one author's entry in the manifest: the record
// from attribution.json plus the item paths of theirs that were used.
type exportAttribution struct {
	author  string
	url     string
	license ccLicense
	items   []string
}

// Attributed is the optional capability an editor implements when its
// export contains library artwork that is not a scene instance, and so
// cannot be recovered by walking the exported tree. Implementations
// return library resource URIs ("res://library/<author>/..."); anything
// else is ignored.
//
// Parallel to [Exporter]: an editor without it is fully covered by the
// node walk.
type Attributed interface {
	ExportedDesignURIs() []string
}

// collectDesignURIs walks a subtree and returns the library resource
// path of every node instantiated from one, plus whatever the editor
// names through [Attributed], de-duplicated and sorted. Nodes that were
// not instantiated from a scene (plain Node3D groupings, procedural
// geometry) contribute nothing.
func collectDesignURIs(root Node.Instance, extra ...string) []string {
	return sortedUnique(append(sceneFilePaths(root), extra...))
}

// sceneFilePaths walks a subtree and returns the resource path recorded
// on every node instantiated from a scene, in tree order and with
// repeats (sortedUnique tidies up). Nodes that were not instantiated
// from a scene — plain Node3D groupings, procedural geometry, a mod's
// runtime-packed scene — report "" and contribute nothing.
func sceneFilePaths(root Node.Instance) []string {
	var paths []string
	var walk func(node Node.Instance)
	walk = func(node Node.Instance) {
		if path := node.SceneFilePath(); path != "" {
			paths = append(paths, path)
		}
		for _, child := range node.GetChildren() {
			walk(child)
		}
	}
	walk(root)
	return paths
}

// sortedUnique drops empties and duplicates and sorts what is left, so
// a design used a hundred times is credited once and the manifest reads
// the same for the same export.
func sortedUnique(uris []string) []string {
	seen := make(map[string]bool, len(uris))
	out := make([]string, 0, len(uris))
	for _, uri := range uris {
		if uri == "" || seen[uri] {
			continue
		}
		seen[uri] = true
		out = append(out, uri)
	}
	sort.Strings(out)
	return out
}

// buildAttributionManifest renders the manifest text for a set of design
// URIs against a set of attribution records. Pure, so the shape of the
// file is pinned by a test rather than by an export nobody reads.
// Returns "" when nothing in the export carries an attribution record —
// there is then no claim to make, and no file worth writing.
func buildAttributionManifest(records map[string]Attribution, creation string, uris []string) string {
	byAuthor := map[string]*exportAttribution{}
	var order []string
	unrecorded := 0
	for _, uri := range uris {
		author, url, license, ok := resolveAttribution(records, uri)
		if !ok {
			unrecorded++
			continue
		}
		// One entry per (author, licence): an author who publishes some
		// items CC0 and others CC-BY must not have the stricter licence
		// quietly applied to everything of theirs.
		key := author + "\x00" + string(license)
		entry, seen := byAuthor[key]
		if !seen {
			entry = &exportAttribution{author: author, url: url, license: license}
			byAuthor[key] = entry
			order = append(order, key)
		}
		entry.items = append(entry.items, strings.TrimPrefix(uri, "res://library/"))
	}
	if len(byAuthor) == 0 {
		return ""
	}
	slices.Sort(order)
	var out strings.Builder
	fmt.Fprintf(&out, "Artwork used in %s\n", creation)
	out.WriteString("Made with Aviary — https://the.quetzal.community\n\n")
	out.WriteString("The artwork below was made by these authors and is used under the\n")
	out.WriteString("Creative Commons licences named. Keep this file with the export:\n")
	out.WriteString("a CC-BY or CC-BY-SA work must carry its author's credit wherever it\n")
	out.WriteString("goes.\n")
	for _, key := range order {
		entry := byAuthor[key]
		out.WriteString("\n")
		fmt.Fprintf(&out, "%s — %s\n", entry.author, licenseName(entry.license))
		if entry.url != "" {
			fmt.Fprintf(&out, "  %s\n", entry.url)
		}
		sort.Strings(entry.items)
		for _, item := range entry.items {
			fmt.Fprintf(&out, "  %s\n", item)
		}
	}
	if unrecorded > 0 {
		// Said plainly rather than silently: an export that quietly
		// dropped a mod's artwork would misrepresent what is in it.
		fmt.Fprintf(&out, "\n%d further item(s) in this export carry no attribution record\n", unrecorded)
		out.WriteString("(drop-in mods, or your own creations).\n")
	}
	return out.String()
}

// licenseName spells out a licence id for people reading the manifest
// outside the game, where the badge icons aren't there to explain it.
func licenseName(license ccLicense) string {
	switch license {
	case ccZero:
		return "CC0 1.0 (public domain dedication) — https://creativecommons.org/publicdomain/zero/1.0/"
	case ccBY:
		return "CC BY 4.0 (attribution required) — https://creativecommons.org/licenses/by/4.0/"
	case ccBYSA:
		return "CC BY-SA 4.0 (attribution, share alike) — https://creativecommons.org/licenses/by-sa/4.0/"
	}
	return string(license)
}

// attributionPathFor is where the manifest lands for an export written
// to dst: alongside it, named after it, so two exports in the same
// folder keep their own records.
func attributionPathFor(dst string) string {
	ext := filepath.Ext(dst)
	return strings.TrimSuffix(dst, ext) + "-ATTRIBUTION.txt"
}

// writeExportAttribution writes the manifest for an exported subtree
// next to the exported file. Does nothing when the export contains no
// attributed artwork.
func writeExportAttribution(editor Editor, root Node.Instance, dst string) error {
	var extra []string
	if attributed, ok := editor.(Attributed); ok {
		extra = attributed.ExportedDesignURIs()
	}
	name := filepath.Base(dst)
	text := buildAttributionManifest(attributions(), name, collectDesignURIs(root, extra...))
	if text == "" {
		return nil
	}
	return os.WriteFile(attributionPathFor(dst), []byte(text), 0o644)
}
