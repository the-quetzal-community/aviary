package internal

import (
	"path"
	"strings"

	"graphics.gd/variant/Angle"
	"graphics.gd/variant/Basis"
	"graphics.gd/variant/Euler"
	"graphics.gd/variant/Transform3D"
	"graphics.gd/variant/Vector3"
)

// coasterPiece describes how a track piece advances the placement
// cursor: entry is where the previous piece's exit connects (in the
// piece's local frame), and exit is where the next piece's entry
// will sit. entryRotation and exitRotation give the track's heading
// at those points relative to the mesh's own frame: left turns set Y
// at the exit, a piece that starts or ends on a climb sets X at that
// end (a hill-end's rails enter pitched up, so the mesh is laid level
// under a pitched cursor, not tilted).
//
// All values are in the piece's local frame at scale 1. The editor
// renders pieces at coasterPieceScale (0.5), so translations are
// multiplied by that scale at placement time; rotations are not
// scaled.
//
// Measured 2026-09-08 from the rail tops of the wood Kenney Coaster
// Kit GLBs (all themes share the same per-shape geometry): the origin
// sits at the rail bottom of the entry face, the ties are inset 0.2
// and the rail top (0.3 above the origin) is the surface that reaches
// the end faces. The kit is grid-based: every 4-unit piece rises a
// whole unit (the half hill half a unit), turns are quarter circles
// of radius 2 or 4, and the loop is a full circle tangent to the
// ground at z=0 that lands one lane to the left. hill-end overhangs
// its origin by 0.21 so its rail tucks into the piece before it.
//
// Left-curving assets are stored as-shipped; right turns reuse the
// same asset and are X-mirrored at render time when the design path's
// parent folder is "track_r". Descents reuse the climbing assets
// turned around (folder "track_d"): the cursor enters at the mesh's
// exit and leaves at its entry, see coasterPiece.atEntry.
type coasterPiece struct {
	entry         Vector3.XYZ
	exit          Vector3.XYZ
	entryRotation Euler.Radians
	exitRotation  Euler.Radians
	// entryPitch and exitPitch classify the track's grade at each end
	// (0 level, +coasterHillPitch climbing, negative descending). A
	// piece can only follow a cursor whose pitch matches its entry.
	entryPitch, exitPitch Angle.Radians
	// startable is true for pieces that can begin a new track. Only
	// pieces in the "station" category can start one in V1.
	startable bool
	// mirror is set by coasterPieceForPath to true when the design is
	// in the track_r category — the editor flips X scale at render
	// time to convert a left-curving asset into a right-curving piece.
	mirror bool
	// reversed is set by coasterPieceForPath for the track_d category:
	// the asset is laid turned around so a climb becomes a descent.
	reversed bool
}

// coasterHillPitch is the grade a hill-beginning leaves the track at
// and a hill-end expects: about 1 in 2, matching the rails at the join
// (the kit is loose here; anything close reads as continuous).
var coasterHillPitch = Angle.Atan2(1.0, 2.0)

// coasterPieces is the manifest keyed by shape name (filename stem
// minus the theme prefix). The same shape is used regardless of which
// theme tile the player picked or which track_* folder it came from —
// handedness and direction are applied by coasterPieceForPath.
var coasterPieces = map[string]coasterPiece{
	// The station is a library composite (compose_glb.py in the
	// library repo): one straight of the theme's track with four
	// platform tiles along it, so it chains exactly like a straight and
	// the rails are inside the platforms from the moment it is placed.
	"station": {
		entry:     Vector3.XYZ{0, 0, 0},
		exit:      Vector3.XYZ{0, 0, 4},
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
	// The corner ramps climb a unit through the turn and flatten out
	// before the exit, so they chain level to level like hill-complete.
	"corner-small-ramp": {
		entry:        Vector3.XYZ{0, 0, 0},
		exit:         Vector3.XYZ{-2, 1, 2},
		exitRotation: Euler.Radians{Y: Angle.Pi / 2},
	},
	"corner-large-ramp": {
		entry:        Vector3.XYZ{0, 0, 0},
		exit:         Vector3.XYZ{-4, 1, 4},
		exitRotation: Euler.Radians{Y: Angle.Pi / 2},
	},
	// A full circle tangent to the ground at the origin: the track
	// enters at z=0, goes up and over, and comes back down at z=0 one
	// lane to the left, heading the same way.
	"looping": {
		entry: Vector3.XYZ{0, 0, 0},
		exit:  Vector3.XYZ{-1, 0, 0},
	},
	// A hill is a hill-beginning (level to climbing) followed by a
	// hill-end (climbing to level), each rising a unit.
	"hill-beginning": {
		entry:        Vector3.XYZ{0, 0, 0},
		exit:         Vector3.XYZ{0, 1, 4},
		exitRotation: Euler.Radians{X: -coasterHillPitch},
		exitPitch:    coasterHillPitch,
	},
	"hill-end": {
		entry:         Vector3.XYZ{0, 0, 0},
		exit:          Vector3.XYZ{0, 1, 4},
		entryRotation: Euler.Radians{X: -coasterHillPitch},
		entryPitch:    coasterHillPitch,
	},
	// Whole climbs in one piece, level at both ends.
	"hill-complete": {
		entry: Vector3.XYZ{0, 0, 0},
		exit:  Vector3.XYZ{0, 1, 4},
	},
	"hill-complete-half": {
		entry: Vector3.XYZ{0, 0, 0},
		exit:  Vector3.XYZ{0, 0.5, 4},
	},
	"bump-up": {
		entry: Vector3.XYZ{0, 0, 0},
		exit:  Vector3.XYZ{0, 0, 4},
	},
	"bump-down": {
		entry: Vector3.XYZ{0, 0, 0},
		exit:  Vector3.XYZ{0, 0, 4},
	},
	// "curve" is a lane change, not a turn: the track shifts two units
	// left over four and exits parallel to the entry. The "skew" pieces
	// bank the track without turning or climbing, so they chain as
	// level straights (the roll itself is cosmetic until a banked-turn
	// set exists).
	"curve": {
		entry: Vector3.XYZ{0, 0, 0},
		exit:  Vector3.XYZ{-2, 0, 4},
	},
	"skew-left": {
		entry: Vector3.XYZ{0, 0, 0},
		exit:  Vector3.XYZ{0, 0, 4},
	},
	"skew-left-side": {
		entry: Vector3.XYZ{0, 0, 0},
		exit:  Vector3.XYZ{0, 0, 4},
	},
	"skew-right": {
		entry: Vector3.XYZ{0, 0, 0},
		exit:  Vector3.XYZ{0, 0, 4},
	},
	"skew-right-side": {
		entry: Vector3.XYZ{0, 0, 0},
		exit:  Vector3.XYZ{0, 0, 4},
	},
}

// coasterTrackLift is how far (in piece units) a track piece's origin
// sits above the ground it is started on. The Kenney track meshes carry
// a -1 node offset (the rails hang a unit below the piece origin, the
// height of the kit's support columns), so a piece whose origin sits
// one unit up has its rails resting on the ground.
const coasterTrackLift = 1.0

// coasterTurn is a half turn about Y: the frame of a track direction
// reversed in place.
var coasterTurn = Basis.FromEuler(Euler.Radians{Y: Angle.Pi}, Angle.OrderXYZ)

// turned reverses a track pose in place: same point, opposite way
// along the track.
func coasterTurned(pose Transform3D.BasisOrigin) Transform3D.BasisOrigin {
	return Transform3D.BasisOrigin{Basis: Basis.Mul(pose.Basis, coasterTurn), Origin: pose.Origin}
}

// meshEnds returns the world poses of the mesh's own entry and exit
// faces for a piece instantiated at transform.
func (piece coasterPiece) meshEnds(transform Transform3D.BasisOrigin) (entry, exit Transform3D.BasisOrigin) {
	entry = Transform3D.BasisOrigin{
		Basis:  Basis.Mul(transform.Basis, Basis.FromEuler(piece.entryRotation, Angle.OrderXYZ)),
		Origin: Vector3.Add(transform.Origin, Basis.Transform(Vector3.MulX(piece.entry, coasterPieceScale), transform.Basis)),
	}
	exit = Transform3D.BasisOrigin{
		Basis:  Basis.Mul(transform.Basis, Basis.FromEuler(piece.exitRotation, Angle.OrderXYZ)),
		Origin: Vector3.Add(transform.Origin, Basis.Transform(Vector3.MulX(piece.exit, coasterPieceScale), transform.Basis)),
	}
	return entry, exit
}

// ends returns the world poses where the track enters and leaves a
// piece instantiated at transform (+Z along the direction of travel).
// A reversed piece is travelled from the mesh's exit face to its entry
// face.
func (piece coasterPiece) ends(transform Transform3D.BasisOrigin) (entry, exit Transform3D.BasisOrigin) {
	entry, exit = piece.meshEnds(transform)
	if piece.reversed {
		return coasterTurned(exit), coasterTurned(entry)
	}
	return entry, exit
}

// atEntry returns the transform to instantiate the piece at so the
// track enters it at pose, and the pose where the track leaves it.
func (piece coasterPiece) atEntry(pose Transform3D.BasisOrigin) (transform, exit Transform3D.BasisOrigin) {
	if piece.reversed {
		transform, entry := piece.meshAtExit(coasterTurned(pose))
		return transform, coasterTurned(entry)
	}
	return piece.meshAtEntry(pose)
}

// atExit returns the transform to instantiate the piece at so the
// track leaves it at pose, and the pose where the track enters it.
func (piece coasterPiece) atExit(pose Transform3D.BasisOrigin) (transform, entry Transform3D.BasisOrigin) {
	if piece.reversed {
		transform, exit := piece.meshAtEntry(coasterTurned(pose))
		return transform, coasterTurned(exit)
	}
	return piece.meshAtExit(pose)
}

// meshAtEntry instantiates the mesh with its entry face on pose.
func (piece coasterPiece) meshAtEntry(pose Transform3D.BasisOrigin) (transform, exit Transform3D.BasisOrigin) {
	basis := Basis.Mul(pose.Basis, Basis.Inverse(Basis.FromEuler(piece.entryRotation, Angle.OrderXYZ)))
	transform = Transform3D.BasisOrigin{
		Basis:  basis,
		Origin: Vector3.Sub(pose.Origin, Basis.Transform(Vector3.MulX(piece.entry, coasterPieceScale), basis)),
	}
	_, exit = piece.meshEnds(transform)
	return transform, exit
}

// meshAtExit instantiates the mesh with its exit face on pose.
func (piece coasterPiece) meshAtExit(pose Transform3D.BasisOrigin) (transform, entry Transform3D.BasisOrigin) {
	basis := Basis.Mul(pose.Basis, Basis.Inverse(Basis.FromEuler(piece.exitRotation, Angle.OrderXYZ)))
	transform = Transform3D.BasisOrigin{
		Basis:  basis,
		Origin: Vector3.Sub(pose.Origin, Basis.Transform(Vector3.MulX(piece.exit, coasterPieceScale), basis)),
	}
	entry, _ = piece.meshEnds(transform)
	return transform, entry
}

// coasterCategories lists the editor tab names that hold coaster
// track pieces. Park-prop dressing tabs aren't in this set — they
// fall through to free terrain placement.
var coasterCategories = map[string]string{
	"track_f": "f", // forward (straights, hills, bumps, skews)
	"track_d": "d", // descents (the hill assets, laid turned around)
	"track_l": "l", // left turns
	"track_r": "r", // right turns (left assets, X-mirrored at render)
	"track_s": "s", // special (loops, corkscrews)
	"station": "station",
}

// coasterParsePath splits a design path of the form
// "res://library/<author>/<category>/<file>.glb" into (category code,
// theme, shape). In every coaster category the filename is
// "<theme>-<shape>" (theme is wood/steel/monorail/hanging/mouse/flume,
// no hyphens); a filename without a hyphen is a theme-less shape.
func coasterParsePath(design string) (category, theme, shape string, ok bool) {
	folder := designCategory(design)
	cat, isCoaster := coasterCategories[folder]
	if !isCoaster {
		return "", "", "", false
	}
	base := strings.TrimSuffix(path.Base(design), ".glb")
	idx := strings.Index(base, "-")
	if idx < 0 {
		return cat, "", base, true
	}
	return cat, base[:idx], base[idx+1:], true
}

// coasterTheme returns the theme of a coaster track design ("" for park
// props and theme-less shapes).
func coasterTheme(design string) string {
	_, theme, _, ok := coasterParsePath(design)
	if !ok {
		return ""
	}
	return theme
}

// coasterRetheme returns the same shape in another theme: the sibling
// file "<theme>-<shape>.glb" in the design's folder. Designs that carry
// no theme are returned unchanged.
func coasterRetheme(design, theme string) string {
	_, current, shape, ok := coasterParsePath(design)
	if !ok || current == "" || current == theme {
		return design
	}
	return path.Dir(design) + "/" + theme + "-" + shape + ".glb"
}

// coasterPieceForPath returns the manifest entry for a design,
// applying right-handed mirroring when the path is in track_r. Any
// shape in the station category falls back to the "station" entry.
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
	switch category {
	case "r":
		piece.entry.X = -piece.entry.X
		piece.exit.X = -piece.exit.X
		piece.entryRotation.Y = -piece.entryRotation.Y
		piece.entryRotation.Z = -piece.entryRotation.Z
		piece.exitRotation.Y = -piece.exitRotation.Y
		piece.exitRotation.Z = -piece.exitRotation.Z
		piece.mirror = true
	case "d":
		// Travelled backwards a climb is a descent: the ends swap and
		// their grades flip sign.
		piece.reversed = true
		piece.entryPitch, piece.exitPitch = -piece.exitPitch, -piece.entryPitch
	}
	return piece, true
}
