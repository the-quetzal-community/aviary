package internal

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"graphics.gd/classdb/DirAccess"
	"graphics.gd/classdb/Engine"
	"graphics.gd/classdb/FileAccess"
)

// Attribution is the record shipped as res://library/<author>/attribution.json
// (written by write_attribution.py in the library repo) that says who made
// the artwork in an author folder and under which Creative Commons license.
// It is the single source of truth for the license badges in the design
// explorer, and later for the in-game credits and the ATTRIBUTION.txt that
// travels with a player's exports — the "full usage rights" promise is only
// honest if every asset can be traced back to its author and license.
//
// Items holds per-item overrides keyed by the design's path under the author
// folder (e.g. "helmets/elvs_tophat1.obj" for
// res://library/makehuman/helmets/elvs_tophat1.obj): MakeHuman's community
// clothing is per-item licensed, with CC-BY hats sitting next to CC0 shirts in
// the same folder.
type Attribution struct {
	Name    string                     `json:"name"`
	URL     string                     `json:"url"`
	Source  string                     `json:"source"`
	License ccLicense                  `json:"license"`
	Note    string                     `json:"note"`
	Items   map[string]ItemAttribution `json:"items"`
}

// ItemAttribution overrides the folder-level record for one design.
type ItemAttribution struct {
	Author  string    `json:"author"`
	License ccLicense `json:"license"`
	URL     string    `json:"url"`
}

// builtinAttribution is the fallback used when an author folder ships no
// attribution.json (a cached pck from before the records existed). It must
// only ever be *less* precise than the shipped file, never contradict it;
// authors missing from both are never hidden by the license toggles and are
// credited by folder name.
var builtinAttribution = map[string]Attribution{
	"everything":     {Name: "David OReilly", URL: "https://www.davidoreilly.com/", License: ccBY},
	"excog":          {Name: "Bligh Hedges (excog)", License: ccZero},
	"kenney":         {Name: "Kenney", URL: "https://kenney.nl/", License: ccZero},
	"makehuman":      {Name: "MakeHuman Community", URL: "http://www.makehumancommunity.org/", License: ccZero},
	"splizard":       {Name: "Quentin Quaadgras (Splizard)", URL: "https://quentinquaadgras.com", License: ccZero},
	"wildfire_games": {Name: "Wildfire Games (0 A.D.)", URL: "https://play0ad.com/", License: ccBYSA},
	"yughues":        {Name: "Nobiax / Yughues", URL: "https://opengameart.org/users/yughues", License: ccZero},
}

var (
	attributionOnce     sync.Once
	attributionByAuthor map[string]Attribution
)

// attributions returns the attribution record of every library author,
// loading res://library/<author>/attribution.json once (from the mounted
// preview.pck, or the live filesystem in dev mode) and falling back to
// builtinAttribution for folders that ship none.
func attributions() map[string]Attribution {
	attributionOnce.Do(loadAttributions)
	return attributionByAuthor
}

func loadAttributions() {
	attributionByAuthor = make(map[string]Attribution, len(builtinAttribution))
	for author, record := range builtinAttribution {
		attributionByAuthor[author] = record
	}
	dir := DirAccess.Open("res://library")
	if dir == DirAccess.Nil {
		return
	}
	for author := range dir.Iter() {
		if strings.Contains(author, ".") {
			continue
		}
		path := "res://library/" + author + "/attribution.json"
		file := FileAccess.Open(path, FileAccess.Read)
		if file == FileAccess.Nil {
			continue
		}
		var record Attribution
		if err := json.Unmarshal([]byte(file.GetAsText()), &record); err != nil {
			Engine.Raise(fmt.Errorf("attribution: %s: %w", path, err))
			continue
		}
		if record.Name == "" {
			record.Name = author
		}
		attributionByAuthor[author] = record
	}
}

// authorAttribution returns the record for a library author folder. ok is
// false for folders with no record at all (mods, unknown authors).
func authorAttribution(author string) (Attribution, bool) {
	record, ok := attributions()[author]
	return record, ok
}

// designAttribution resolves who made one design and under which license,
// applying the per-item override when the author folder has one. Returns
// ok=false for non-library resources (procedural builtins, mods, user
// bookmarks), which carry no license record and are never hidden.
func designAttribution(uri string) (author, url string, license ccLicense, ok bool) {
	return resolveAttribution(attributions(), uri)
}

// resolveAttribution is designAttribution over an explicit record set (unit
// tested without an engine).
func resolveAttribution(records map[string]Attribution, uri string) (author, url string, license ccLicense, ok bool) {
	folder, rest := splitDesignURI(uri)
	record, ok := records[folder]
	if !ok {
		return "", "", "", false
	}
	if item, ok := record.Items[rest]; ok {
		author = item.Author
		if author == "" {
			author = record.Name
		}
		url = item.URL
		if url == "" {
			url = record.URL
		}
		license = item.License
		if license == "" {
			license = record.License
		}
		return author, url, license, true
	}
	return record.Name, record.URL, record.License, true
}

// splitDesignURI splits "res://library/<author>/<rest>" into its author
// folder and the item key used by Attribution.Items. Both are "" for
// non-library URIs.
func splitDesignURI(uri string) (author, rest string) {
	tail, ok := strings.CutPrefix(uri, "res://library/")
	if !ok {
		return "", ""
	}
	author, rest, _ = strings.Cut(tail, "/")
	return author, rest
}

// authorLicenses returns the set of licenses used anywhere in an author's
// folder: the folder default plus every distinct per-item override.
func authorLicenses(author string) []ccLicense {
	return licensesOf(attributions(), author)
}

// licensesOf is authorLicenses over an explicit record set.
func licensesOf(records map[string]Attribution, author string) []ccLicense {
	record, ok := records[author]
	if !ok {
		return nil
	}
	set := []ccLicense{record.License}
	for _, item := range record.Items {
		if item.License != "" && !containsLicense(set, item.License) {
			set = append(set, item.License)
		}
	}
	return set
}

func containsLicense(set []ccLicense, license ccLicense) bool {
	for _, l := range set {
		if l == license {
			return true
		}
	}
	return false
}
