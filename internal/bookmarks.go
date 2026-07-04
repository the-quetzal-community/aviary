package internal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"graphics.gd/classdb/Engine"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/variant/Angle"
	"graphics.gd/variant/Euler"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Vector3"

	"github.com/google/uuid"

	"the.quetzal.community/aviary/internal/critter"
	"the.quetzal.community/aviary/internal/musical"
)

// bookmarks.go backs the "user designs" feature: a creation built in an editor
// (a critter for now) is captured and saved as a local bookmark, then shown in
// the design explorer's "user" theme and placeable like any library design.
//
// A bookmark is two files under user://user/ (== UserDataDir/user): <id>.crt is
// the creation serialised as MUSICAL DATA — a self-contained sequence of musical
// entries (Sculpts for the skeleton, Import+Change per part), the same stable
// encoding used on the wire and in saves (see musical.MarshalEntries) — and
// <id>.json holds UI metadata (display name, source editor). The .crt bytes are
// exactly what a musical.Upload carries, so a placed bookmark is transported to
// peers and embedded in the save in the canonical format (no ad-hoc encoding).
//
// Resolution mirrors the mod:// pattern (internal/mods.go): listing, preview and
// placement all read from disk with no client wiring. A placed bookmark mints a
// per-space musical.Design bound to the creation (Client.MusicalCreation), which
// also emits the Upload.

const creationScheme = "creation://"

// userBookmarksDir is the Godot-vfs path; userBookmarksOSDir is the same
// location as an OS path (user:// maps onto UserDataDir).
const userBookmarksDir = "user://user"

func userBookmarksOSDir() string { return UserDataDir + "/user" }

func isCreationPath(uri string) bool { return strings.HasPrefix(uri, creationScheme) }
func creationURI(id string) string   { return creationScheme + id }

func creationID(uri string) (string, bool) {
	if !isCreationPath(uri) {
		return "", false
	}
	return strings.TrimPrefix(uri, creationScheme), true
}

func bookmarkCreationPath(id string) string { return filepath.Join(userBookmarksOSDir(), id+".crt") }
func bookmarkMetaPath(id string) string     { return filepath.Join(userBookmarksOSDir(), id+".json") }

// bookmarkMeta is the UI sidecar (display name, source editor) kept next to the
// musical .crt bundle. JSON, not gob: it's plain UI metadata, human-readable,
// and decoupled from the creation's musical encoding.
type bookmarkMeta struct {
	Name   string `json:"name"`
	Editor string `json:"editor"`
}

// saveBookmark writes a creation's musical bundle (<id>.crt) and metadata
// (<id>.json) and returns the new id.
func saveBookmark(name, editor string, cc CritterCreation) (string, error) {
	if err := os.MkdirAll(userBookmarksOSDir(), 0777); err != nil {
		return "", err
	}
	id := uuid.NewString()
	if err := os.WriteFile(bookmarkCreationPath(id), encodeCreation(cc), 0644); err != nil {
		return "", err
	}
	meta, err := json.Marshal(bookmarkMeta{Name: name, Editor: editor})
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(bookmarkMetaPath(id), meta, 0644); err != nil {
		return "", err
	}
	return id, nil
}

// readBookmarkCreation loads and decodes a bookmark's musical bundle.
func readBookmarkCreation(id string) (CritterCreation, bool) {
	data, err := os.ReadFile(bookmarkCreationPath(id))
	if err != nil {
		return CritterCreation{}, false
	}
	return decodeCreation(data)
}

func readBookmarkMeta(id string) (bookmarkMeta, bool) {
	data, err := os.ReadFile(bookmarkMetaPath(id))
	if err != nil {
		return bookmarkMeta{}, false
	}
	var m bookmarkMeta
	if json.Unmarshal(data, &m) != nil {
		return bookmarkMeta{}, false
	}
	return m, true
}

// bookmarkRef is a lightweight listing entry (id + display name) for the design
// explorer.
type bookmarkRef struct {
	ID   string
	Name string
}

func listBookmarks() []bookmarkRef {
	entries, err := os.ReadDir(userBookmarksOSDir())
	if err != nil {
		return nil
	}
	var out []bookmarkRef
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".crt") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".crt")
		ref := bookmarkRef{ID: id, Name: id}
		if m, ok := readBookmarkMeta(id); ok && m.Name != "" {
			ref.Name = m.Name
		}
		out = append(out, ref)
	}
	return out
}

func hasBookmarks() bool {
	entries, _ := os.ReadDir(userBookmarksOSDir())
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".crt") {
			return true
		}
	}
	return false
}

// loadCreationSceneNode reconstructs a creation:// design into a node tree, for
// the PreviewRenderer (mirrors loadModSceneNode for mod:// designs).
func loadCreationSceneNode(uri string) (Node3D.Instance, bool) {
	id, ok := creationID(uri)
	if !ok {
		return Node3D.Nil, false
	}
	cc, ok := readBookmarkCreation(id)
	if !ok {
		return Node3D.Nil, false
	}
	// Preview render — no terrain to measure jump altitude against.
	node, err := buildCritterInstance(cc, nil)
	if err != nil {
		return Node3D.Nil, false
	}
	return node, true
}

// encodeCreation serialises a CritterCreation into a self-contained musical
// bundle: one Sculpt per bone ("bone/<i>"), four per leg ("leg/<i>/attach|hip|
// knee|foot"), one per macro weight ("weight/<name>"), and an Import+Change pair
// per attached part (the part's library URI and its on-body anchor, encoded
// exactly as a real critter part Change — see anchorFromChange / changeFromAnchor).
// The result is canonical musical data (stable encode/decode), carried verbatim
// by Upload and persisted in the save.
func encodeCreation(cc CritterCreation) []byte {
	var entries []any
	for i, b := range cc.Bones {
		entries = append(entries, musical.Sculpt{
			Slider: "bone/" + strconv.Itoa(i),
			Target: vec3ToXYZ(b.Pos),
			Amount: Float.X(b.Radius),
		})
	}
	for i, leg := range cc.Legs {
		p := "leg/" + strconv.Itoa(i) + "/"
		entries = append(entries,
			musical.Sculpt{Slider: p + "attach", Amount: Float.X(leg.Attach)},
			musical.Sculpt{Slider: p + "hip", Target: vec3ToXYZ(leg.Hip), Amount: Float.X(leg.HipRadius)},
			musical.Sculpt{Slider: p + "knee", Target: vec3ToXYZ(leg.Knee), Amount: Float.X(leg.KneeRadius)},
			musical.Sculpt{Slider: p + "foot", Target: vec3ToXYZ(leg.Foot), Amount: Float.X(leg.FootRadius)},
		)
		// Kind is emitted only when non-default so bundles produced
		// before leg kinds existed stay byte-identical on re-encode;
		// old decoders skip the unrecognised joint name harmlessly.
		if leg.Kind != critter.LegKindMammal {
			entries = append(entries, musical.Sculpt{Slider: p + "kind", Amount: Float.X(leg.Kind)})
		}
	}
	for name, w := range cc.Weights {
		entries = append(entries, musical.Sculpt{Slider: "weight/" + name, Amount: Float.X(w)})
	}
	for i, part := range cc.Parts {
		design := musical.Design{Number: uint16(i + 1)} // bundle-local design id
		change := changeFromAnchor(part.Anchor)
		change.Design = design
		entries = append(entries, musical.Import{Design: design, Import: part.URI}, change)
	}
	data, err := musical.MarshalEntries(entries)
	if err != nil {
		return nil
	}
	return data
}

// decodeCreation rebuilds a CritterCreation from a musical bundle. Bone/leg
// slices are sized by the highest index seen, so structural extent is carried by
// which entries are present (no separate count record needed).
func decodeCreation(data []byte) (CritterCreation, bool) {
	entries, err := musical.UnmarshalEntries(data)
	if err != nil {
		return CritterCreation{}, false
	}
	bones := map[int]critter.Bone{}
	legs := map[int]critter.Leg{}
	weights := map[string]float32{}
	partURIs := map[musical.Design]string{}
	var partChanges []musical.Change
	maxBone, maxLeg := -1, -1
	for _, e := range entries {
		switch v := e.(type) {
		case musical.Sculpt:
			switch {
			case strings.HasPrefix(v.Slider, "bone/"):
				i, err := strconv.Atoi(strings.TrimPrefix(v.Slider, "bone/"))
				if err != nil {
					continue
				}
				bones[i] = critter.Bone{Pos: xyzToVec3(v.Target), Radius: float32(v.Amount)}
				maxBone = max(maxBone, i)
			case strings.HasPrefix(v.Slider, "leg/"):
				idxStr, joint, _ := strings.Cut(strings.TrimPrefix(v.Slider, "leg/"), "/")
				i, err := strconv.Atoi(idxStr)
				if err != nil {
					continue
				}
				leg := legs[i]
				switch joint {
				case "attach":
					leg.Attach = int(v.Amount)
				case "kind":
					leg.Kind = critter.LegKind(v.Amount)
				case "hip":
					leg.Hip, leg.HipRadius = xyzToVec3(v.Target), float32(v.Amount)
				case "knee":
					leg.Knee, leg.KneeRadius = xyzToVec3(v.Target), float32(v.Amount)
				case "foot":
					leg.Foot, leg.FootRadius = xyzToVec3(v.Target), float32(v.Amount)
				}
				legs[i] = leg
				maxLeg = max(maxLeg, i)
			case strings.HasPrefix(v.Slider, "weight/"):
				weights[strings.TrimPrefix(v.Slider, "weight/")] = float32(v.Amount)
			}
		case musical.Import:
			partURIs[v.Design] = v.Import
		case musical.Change:
			partChanges = append(partChanges, v)
		}
	}
	var cc CritterCreation
	if maxBone >= 0 {
		cc.Bones = make([]critter.Bone, maxBone+1)
		for i, b := range bones {
			cc.Bones[i] = b
		}
	}
	if maxLeg >= 0 {
		cc.Legs = make([]critter.Leg, maxLeg+1)
		for i, l := range legs {
			cc.Legs[i] = l
		}
	}
	if len(weights) > 0 {
		cc.Weights = weights
	}
	for _, ch := range partChanges {
		if uri := partURIs[ch.Design]; uri != "" {
			cc.Parts = append(cc.Parts, CritterPartRef{URI: uri, Anchor: anchorFromChange(ch)})
		}
	}
	return cc, true
}

// changeFromAnchor is the inverse of anchorFromChange (editor_critter.go): it
// encodes a PartAnchor into a musical.Change exactly as the critter editor does
// when it places a part, so a bundle's part records are genuine critter Changes.
func changeFromAnchor(a PartAnchor) musical.Change {
	ch := musical.Change{
		Offset: Vector3.New(a.T, a.Theta, a.Offset),
		Angles: Euler.Radians{Y: Angle.Radians(a.Twist)},
		Bounds: Vector3.New(float32(0), float32(0), a.Scale),
	}
	if a.OnLeg {
		ch.Bounds.X = Float.X(a.LegFoot + 1)
		ch.Bounds.Y = Float.X(a.LegSide)
	}
	return ch
}

func vec3ToXYZ(v critter.Vec3) Vector3.XYZ { return Vector3.New(v.X, v.Y, v.Z) }
func xyzToVec3(v Vector3.XYZ) critter.Vec3 {
	return critter.Vec3{X: float32(v.X), Y: float32(v.Y), Z: float32(v.Z)}
}

// CreatableEditor is implemented by editors whose current creation can be
// captured into a reusable user-design bookmark. (Critter for now; vehicle /
// foliage generalise later — P4.)
type CreatableEditor interface {
	CaptureCreation() CritterCreation
}

// bookmarkActiveCreation saves the active editor's creation as a user design
// (when that editor supports capture), then shows the "user" theme so the new
// bookmark is immediately visible. Wired to the MyStuff button.
func (world *Client) bookmarkActiveCreation() {
	if world.ui == nil || world.ui.Editor == nil {
		return
	}
	if ed, ok := world.ui.Editor.editor.(CreatableEditor); ok {
		name := world.ui.Editor.editor.Name()
		if _, err := saveBookmark(name, name, ed.CaptureCreation()); err != nil {
			Engine.Raise(err)
			return
		}
	}
	// Show the user theme (whether we just saved, or are only browsing because
	// the active editor can't be captured).
	world.ui.Editor.Refresh(world.Editing, "user", world.ui.mode)
}
