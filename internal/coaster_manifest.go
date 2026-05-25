package internal

import (
	"path"
	"strings"

	"graphics.gd/variant/Angle"
	"graphics.gd/variant/Euler"
	"graphics.gd/variant/Vector3"
)

// coasterPiece describes how a track piece advances the placement
// cursor: entry is where the previous piece's exit connects (in the
// piece's local frame), and exit is where the next piece's entry
// will sit. exitRotation rotates the cursor's heading after the
// piece is placed (left turns set Y; ramps set X).
//
// All values are in the piece's local frame at scale 1. The editor
// renders pieces at coasterPieceScale (0.5), so translations are
// multiplied by that scale at placement time; rotations are not
// scaled.
//
// Measured from Kenney Coaster Kit GLBs (wood theme — all themes
// share the same per-shape geometry). Left-curving assets are stored
// as-shipped; right turns reuse the same asset and are X-mirrored at
// render time when the design path's parent folder is "track_r".
type coasterPiece struct {
	entry        Vector3.XYZ
	exit         Vector3.XYZ
	exitRotation Euler.Radians
	// startable is true for pieces that can begin a new track. Only
	// pieces in the "station" category can start one in V1.
	startable bool
	// mirror is set by coasterPieceForPath to true when the design is
	// in the track_r category — the editor flips X scale at render
	// time to convert a left-curving asset into a right-curving piece.
	mirror bool
}

// coasterPieces is the manifest keyed by shape name (filename stem
// minus the theme prefix). The same shape is used regardless of which
// theme tile the player picked or whether the path is track_l vs
// track_r — directional handedness is applied by coasterPieceForPath.
var coasterPieces = map[string]coasterPiece{
	"station": {
		entry:     Vector3.XYZ{0, 0, -0.5},
		exit:      Vector3.XYZ{0, 0, 0.5},
		startable: true,
	},
	"straight": {
		entry: Vector3.XYZ{0, 0, 0},
		exit:  Vector3.XYZ{0, 0, 4},
	},
	"segment": {
		entry: Vector3.XYZ{0, 0, -0.1},
		exit:  Vector3.XYZ{0, 0, 0.1},
	},
	"corner-small": {
		entry:        Vector3.XYZ{0, 0, 0},
		exit:         Vector3.XYZ{-2, 0, 2},
		exitRotation: Euler.Radians{Y: Angle.Pi / 2},
	},
	"corner-large": {
		entry:        Vector3.XYZ{0, 0, 0},
		exit:         Vector3.XYZ{-4, 0, 4},
		exitRotation: Euler.Radians{Y: Angle.Pi / 2},
	},
	"corner-small-ramp": {
		entry:        Vector3.XYZ{0, 0, 0},
		exit:         Vector3.XYZ{-2, 1.2, 2},
		exitRotation: Euler.Radians{Y: Angle.Pi / 2, X: -Angle.Atan2(1.2, 2)},
	},
	"corner-large-ramp": {
		entry:        Vector3.XYZ{0, 0, 0},
		exit:         Vector3.XYZ{-4, 1.2, 4},
		exitRotation: Euler.Radians{Y: Angle.Pi / 2, X: -Angle.Atan2(1.2, 4)},
	},
	"looping": {
		entry: Vector3.XYZ{0, 0, -2},
		exit:  Vector3.XYZ{0, 0, 2},
	},
	"hill-beginning": {
		entry:        Vector3.XYZ{0, 0, 0},
		exit:         Vector3.XYZ{0, 1.2, 4},
		exitRotation: Euler.Radians{X: -Angle.Atan2(1.2, 4)},
	},
	"hill-complete": {
		entry: Vector3.XYZ{0, 1.2, 0},
		exit:  Vector3.XYZ{0, 1.2, 4},
	},
	"hill-end": {
		entry:        Vector3.XYZ{0, 1.2, 0},
		exit:         Vector3.XYZ{0, 0, 4},
		exitRotation: Euler.Radians{X: Angle.Atan2(1.2, 4)},
	},
	"bump-up": {
		entry: Vector3.XYZ{0, 0, 0},
		exit:  Vector3.XYZ{0, 0, 4},
	},
	"bump-down": {
		entry: Vector3.XYZ{0, 0, 0},
		exit:  Vector3.XYZ{0, 0, 4},
	},
}

// coasterCategories lists the editor tab names that hold coaster
// track pieces. Park-prop dressing tabs aren't in this set — they
// fall through to free terrain placement.
var coasterCategories = map[string]string{
	"track_f": "f", // forward (straights, hills, bumps, skews)
	"track_l": "l", // left turns
	"track_r": "r", // right turns (left assets, X-mirrored at render)
	"track_s": "s", // special (loops, corkscrews)
	"station": "station",
}

// coasterParsePath splits a design path of the form
// "res://library/<author>/<category>/<file>.glb" into (category code,
// theme, shape). For the four track_* categories the filename is
// "<theme>-<shape>" (theme is wood/steel/monorail/hanging/mouse/flume,
// no hyphens). The "station" category is theme-less; the filename is
// the shape directly.
func coasterParsePath(design string) (category, theme, shape string, ok bool) {
	folder := path.Base(path.Dir(design))
	cat, isCoaster := coasterCategories[folder]
	if !isCoaster {
		return "", "", "", false
	}
	base := strings.TrimSuffix(path.Base(design), ".glb")
	if cat == "station" {
		return cat, "", base, true
	}
	idx := strings.Index(base, "-")
	if idx < 0 {
		return cat, "", base, true
	}
	return cat, base[:idx], base[idx+1:], true
}

// coasterPieceForPath returns the manifest entry for a design,
// applying right-handed mirroring when the path is in track_r. For
// the station category, any shape falls back to the "station" entry
// when no specific shape match exists, since all station-tab items
// (station, park-entrance, ride-entrance) behave like a 1m start
// piece.
func coasterPieceForPath(design string) (coasterPiece, bool) {
	category, _, shape, ok := coasterParsePath(design)
	if !ok {
		return coasterPiece{}, false
	}
	piece, ok := coasterPieces[shape]
	if !ok {
		if category == "station" {
			return coasterPieces["station"], true
		}
		return coasterPiece{}, false
	}
	if category == "r" {
		piece.entry.X = -piece.entry.X
		piece.exit.X = -piece.exit.X
		piece.exitRotation.Y = -piece.exitRotation.Y
		piece.exitRotation.Z = -piece.exitRotation.Z
		piece.mirror = true
	}
	return piece, true
}
