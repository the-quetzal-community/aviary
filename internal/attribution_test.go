package internal

import (
	"slices"
	"testing"
)

func TestResolveAttribution(t *testing.T) {
	records := map[string]Attribution{
		"makehuman": {
			Name: "MakeHuman Community", URL: "http://www.makehumancommunity.org/", License: ccZero,
			Items: map[string]ItemAttribution{
				"helmets/elvs_tophat1.obj": {Author: "Elvaerwyn", License: ccBY, URL: "http://www.makehumancommunity.org/node/1"},
				"jackets/plain.obj":        {Author: "MRT"}, // inherits folder license + URL
			},
		},
		"everything": {Name: "David OReilly", URL: "https://www.davidoreilly.com/", License: ccBY},
	}
	cases := []struct {
		uri         string
		author, url string
		license     ccLicense
		ok          bool
	}{
		{"res://library/makehuman/helmets/elvs_tophat1.obj", "Elvaerwyn", "http://www.makehumancommunity.org/node/1", ccBY, true},
		{"res://library/makehuman/jackets/plain.obj", "MRT", "http://www.makehumancommunity.org/", ccZero, true},
		{"res://library/makehuman/jackets/other.obj", "MakeHuman Community", "http://www.makehumancommunity.org/", ccZero, true},
		{"res://library/everything/critter/1.glb", "David OReilly", "https://www.davidoreilly.com/", ccBY, true},
		{"mod://somebody/critter/thing.glb", "", "", "", false},
		{"res://library/unknown/x.glb", "", "", "", false},
	}
	for _, c := range cases {
		author, url, license, ok := resolveAttribution(records, c.uri)
		if author != c.author || url != c.url || license != c.license || ok != c.ok {
			t.Errorf("%s: got (%q, %q, %q, %v), want (%q, %q, %q, %v)", c.uri, author, url, license, ok, c.author, c.url, c.license, c.ok)
		}
	}
	got := licensesOf(records, "makehuman")
	slices.Sort(got)
	if want := []ccLicense{ccBY, ccZero}; !slices.Equal(got, want) {
		t.Errorf("licensesOf(makehuman) = %v, want %v", got, want)
	}
	if licensesOf(records, "mods") != nil {
		t.Errorf("licensesOf(unknown) should be nil")
	}
}
