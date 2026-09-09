package internal

import (
	"slices"
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

// authorHidden reports whether an author's theme button should disappear
// from the design explorer because the user toggled off the badge for
// every license used in that author's folder. An author whose folder mixes
// licenses (MakeHuman: CC0 base, CC-BY hats) stays listed while any of them
// is shown; designHidden then filters the individual tiles. Authors with no
// attribution record (mods, unknown folders) stay visible.
func authorHidden(name string) bool {
	licenses := authorLicenses(name)
	if len(licenses) == 0 {
		return false
	}
	for _, license := range licenses {
		if !licenseHidden(license) {
			return false
		}
	}
	return true
}

// designHidden reports whether one design should be hidden because the
// badge for its license — per-item where the author's attribution.json
// overrides the folder default — is toggled off. Non-library resources
// (procedural builtins, mods, user bookmarks) are never hidden.
func designHidden(uri string) bool {
	_, _, license, ok := designAttribution(uri)
	return ok && licenseHidden(license)
}

// applyLicenseVisibility walks every placed entity and shows/hides it
// according to the license badges toggled in the Settings menu (resolved
// per design, so a CC-BY hat hides while the CC0 citizen wearing it stays). Hiding is
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
		hidden := designHidden(uri)
		for _, id := range ids {
			if node, ok := id.Instance(); ok {
				node.SetVisible(!hidden)
			}
		}
	}
}
