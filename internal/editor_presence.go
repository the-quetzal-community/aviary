package internal

import (
	"sort"

	"the.quetzal.community/aviary/internal/musical"
)

// subjectEditorNames is the canonical editor name for each [Subject], in the
// SAME order as the Subject enum, the EditorTypes buttons in editor.tscn, and
// the keys of Client.editors. It is the string broadcast in LookAt.Editor so
// peers learn which editor each player is currently in (presence), and the
// routing key the receive path already keys on (see Client.LookAt). Keep this
// in sync with the Subject enum declared in editor.go.
var subjectEditorNames = []string{
	"scenery", // 0 Scenery
	"terrain", // 1 Terrain
	"foliage", // 2 Foliage
	"mineral", // 3 Mineral
	"shelter", // 4 Shelter
	"vehicle", // 5 Vehicle
	"citizen", // 6 Citizen
	"critter", // 7 Critter
	"coaster", // 8 Coaster
}

// subjectEditorName returns the LookAt.Editor presence string for a Subject, or
// "" when it is out of range.
func subjectEditorName(s Subject) string {
	if i := s.Int(); i >= 0 && i < len(subjectEditorNames) {
		return subjectEditorNames[i]
	}
	return ""
}

// editorNameSubject is the inverse of [subjectEditorName]: it maps an editor
// name (as received in LookAt.Editor) back to the [Subject] whose switcher
// button represents it. "boulder" is an alias for the mineral editor (see
// Client.editors), so it resolves to Mineral.
func editorNameSubject(name string) (Subject, bool) {
	if name == "boulder" {
		name = "mineral"
	}
	for i, n := range subjectEditorNames {
		if n == name {
			var s Subject
			s.SetInt(i)
			return s, true
		}
	}
	var zero Subject
	return zero, false
}

// peerPresence is a snapshot of one remote player for the editor switcher's
// presence chips: which editor they are in (Subject, valid only when Known) and
// the res:// preview thumbnail of their chosen avatar.
type peerPresence struct {
	Author  musical.Author
	Subject Subject // editor the peer is in; valid only when Known
	Known   bool    // whether we have learned the peer's editor yet
	Preview string  // res:// avatar preview png
}

// peerPresenceSnapshot returns every known remote player for the switcher's
// presence overlay, sorted by Author for stable chip stacking. Safe to call
// from the main thread (Process): the author_* maps are only mutated by the
// LookAt replay closure, which also runs on the main thread.
func (world *Client) peerPresenceSnapshot() []peerPresence {
	if len(world.authors) == 0 {
		return nil
	}
	out := make([]peerPresence, 0, len(world.authors))
	for author := range world.authors {
		if author == world.id {
			continue
		}
		p := peerPresence{Author: author}
		if name, ok := world.author_editors[author]; ok {
			if s, ok := editorNameSubject(name); ok {
				p.Subject, p.Known = s, true
			}
		}
		uri := defaultAvatarURI
		if design, ok := world.author_designs[author]; ok {
			if u, ok := world.design_to_string[design]; ok && u != "" {
				uri = u
			}
		}
		p.Preview = avatarPreviewURI(uri)
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Author < out[j].Author })
	return out
}
