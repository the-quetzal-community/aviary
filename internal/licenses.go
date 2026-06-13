package internal

import (
	"slices"
	"strings"
)

// ccLicense identifies one of the Creative Commons licenses that the
// authors in The Quetzal Community Library publish their artwork under.
// The string value doubles as the badge asset suffix: the Settings menu
// loads "res://ui/license_<license>.svg" for each one.
type ccLicense string

const (
	ccZero ccLicense = "cc-zero"
	ccBY   ccLicense = "by"
	ccBYSA ccLicense = "by-sa"
)

// ccLicenses lists the license badges shown in the Settings menu, ordered
// from least to most demanding on the user (public domain, attribution,
// attribution + share-alike).
var ccLicenses = []ccLicense{ccZero, ccBY, ccBYSA}

// authorLicense maps each library author folder (res://library/<author>)
// to the license their assets are distributed under, mirroring the
// License.txt shipped alongside each author's folder in the library
// project (which isn't exported into library.pck, so the mapping lives
// here). Authors missing from this map are never hidden by the license
// toggles.
var authorLicense = map[string]ccLicense{
	"everything":     ccBY,
	"kenney":         ccZero,
	"makehuman":      ccZero,
	"splizard":       ccZero,
	"wildfire_games": ccBYSA,
	"yughues":        ccZero,
}

// licenseHidden reports whether the user has toggled this license's badge
// off in the Settings menu.
func licenseHidden(license ccLicense) bool {
	return slices.Contains(UserState.HiddenLicenses, string(license))
}

// setLicenseHidden records the badge toggle in UserState. The caller is
// responsible for persisting (saveUserState) and refreshing the design
// explorer so the change takes effect.
func setLicenseHidden(license ccLicense, hidden bool) {
	UserState.HiddenLicenses = slices.DeleteFunc(UserState.HiddenLicenses, func(s string) bool {
		return s == string(license)
	})
	if hidden {
		UserState.HiddenLicenses = append(UserState.HiddenLicenses, string(license))
	}
}

// authorHidden reports whether an author's artwork should be hidden from
// the design explorer because the user toggled off the badge for that
// author's license. Authors with no known license stay visible.
func authorHidden(name string) bool {
	license, ok := authorLicense[name]
	return ok && licenseHidden(license)
}

// designAuthor extracts the library author from a design resource URI of
// the form "res://library/<author>/<category>/<file>". Returns "" (never
// hidden) for non-library resources such as procedural builtin designs.
func designAuthor(uri string) string {
	rest, ok := strings.CutPrefix(uri, "res://library/")
	if !ok {
		return ""
	}
	author, _, _ := strings.Cut(rest, "/")
	return author
}

// applyLicenseVisibility walks every placed entity and shows/hides it
// according to the license badges toggled in the Settings menu. Hiding is
// strictly render-local — the entities stay in the scene graph and the
// musical log, so nothing about the shared mutation history changes and
// peers are unaffected. Entities placed while a badge is off are hidden
// on arrival by the Change creation path.
func (world *Client) applyLicenseVisibility() {
	for design, ids := range world.design_to_entity {
		uri, ok := world.design_to_string[design]
		if !ok {
			continue
		}
		hidden := authorHidden(designAuthor(uri))
		for _, id := range ids {
			if node, ok := id.Instance(); ok {
				node.SetVisible(!hidden)
			}
		}
	}
}
