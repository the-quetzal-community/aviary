package internal

import (
	"time"

	"graphics.gd/classdb/Engine"
	"graphics.gd/classdb/Node3D"
	"the.quetzal.community/aviary/internal/musical"
)

// undoKind tags which flavour of mutation an UndoEntry reverses. Scenery edits
// are paired forward/inverse Changes; terrain edits are committed Sculpts that
// undo/redo by re-emitting themselves with Revert:true (a toggle).
type undoKind uint8

const (
	undoChange undoKind = iota // Do/Undo are paired musical.Change records.
	undoSculpt                 // Sculpt is a committed terrain stroke (toggled).
)

// UndoEntry reverses one committed mutation. For a Change edit it pairs the
// forward operation with the operation that reverses it; both are dispatched
// through client.space.Change. For a Sculpt edit it stores the committed stroke
// (with its stamped (Author, Timing) identity); undo and redo both re-emit a
// copy of it with Revert:true, which the terrain editor toggles. Either way the
// rest of the world (peers, the .mus3 log) sees an undo as just another
// committed mutation — there's no separate undo channel and the musical format
// doesn't change.
type UndoEntry struct {
	kind undoKind

	// change path
	Do, Undo musical.Change

	// sculpt path
	Sculpt musical.Sculpt
}

// UndoStack is a per-client (local-only) history of reversible operations. The
// stack itself is NOT shared with peers — each client tracks its own undo cursor
// — but the inverse Changes / Revert Sculpts dispatched on Undo() ARE shared, so
// when you press Undo your peers see the resulting state change just as if you'd
// performed it manually.
//
// Linear history semantics: pushing a new entry while the cursor is mid-stack
// discards everything past the cursor (the classic "doing something new loses
// your future"). This matches what every editor since MacPaint has done.
//
// Change and Sculpt entries share one cursor, ordered by commit time, so Ctrl+Z
// walks back through interleaved scenery + terrain edits in true commit order.
type UndoStack struct {
	history []UndoEntry
	cursor  int // 0..len(history); next index for Push / Redo
}

// Push appends a new entry, truncating any redo entries past the cursor.
func (s *UndoStack) Push(e UndoEntry) {
	if s.cursor < len(s.history) {
		s.history = s.history[:s.cursor]
	}
	s.history = append(s.history, e)
	s.cursor++
}

// PopUndo returns the entry to reverse and moves the cursor back.
func (s *UndoStack) PopUndo() (UndoEntry, bool) {
	if s.cursor == 0 {
		return UndoEntry{}, false
	}
	s.cursor--
	return s.history[s.cursor], true
}

// PopRedo returns the entry at the cursor to re-apply and advances.
func (s *UndoStack) PopRedo() (UndoEntry, bool) {
	if s.cursor >= len(s.history) {
		return UndoEntry{}, false
	}
	e := s.history[s.cursor]
	s.cursor++
	return e, true
}

func (s *UndoStack) CanUndo() bool { return s.cursor > 0 }
func (s *UndoStack) CanRedo() bool { return s.cursor < len(s.history) }

// RecordChange registers a scenery op with its inverse. Use at the point where
// the forward operation is dispatched, immediately after the space.Change(do)
// call — passing the same Change for `do` and a constructed inverse for `undo`.
func (world *Client) RecordChange(do, undo musical.Change) {
	// Both sides must commit when replayed via undo/redo.
	do.Commit = true
	undo.Commit = true
	world.undo.Push(UndoEntry{kind: undoChange, Do: do, Undo: undo})
}

// RecordSculpt registers a committed terrain stroke for undo/redo. The brush
// must already carry its stamped (Author, Timing) identity (see commitSculpt).
func (world *Client) RecordSculpt(brush musical.Sculpt) {
	world.undo.Push(UndoEntry{kind: undoSculpt, Sculpt: brush})
}

// nextTiming returns the next strictly-increasing Timing for stamping a locally
// authored sculpt. Wall-clock based so values never repeat across sessions, but
// forced monotonic within a session.
func (world *Client) nextTiming() musical.Timing {
	t := musical.Timing(time.Now().UnixNano())
	if t <= world.lastTiming {
		t = world.lastTiming + 1
	}
	world.lastTiming = t
	return t
}

// noteTiming raises the local Timing high-water mark to cover an already-stamped
// sculpt — e.g. one replayed from the .mus3 log that this client authored in a
// previous session — so nextTiming never reissues a value across sessions.
func (world *Client) noteTiming(t musical.Timing) {
	if t > world.lastTiming {
		world.lastTiming = t
	}
}

// commitSculpt is the single chokepoint for emitting a locally authored terrain
// mutation. For a committed, non-Revert stroke it stamps a fresh (Author, Timing)
// identity and records an undo entry; previews (Commit==false) and Revert sculpts
// pass straight through unstamped/unrecorded. Every terrain edit site calls this
// instead of space.Sculpt directly so identity + undo are wired uniformly.
func (world *Client) commitSculpt(brush musical.Sculpt) error {
	if world.space == nil {
		return nil
	}
	if brush.Commit && !brush.Revert {
		brush.Timing = world.nextTiming()
		if err := world.space.Sculpt(brush); err != nil {
			return err
		}
		world.RecordSculpt(brush)
		return nil
	}
	return world.space.Sculpt(brush)
}

// Undo reverses the most recent recorded operation. Returns false if there's
// nothing to undo. Safe to call on an empty stack — it's just a no-op.
func (world *Client) Undo() bool {
	if world.space == nil {
		return false
	}
	entry, ok := world.undo.PopUndo()
	if !ok {
		return false
	}
	return world.dispatchUndo(entry)
}

// Redo re-applies the operation at the cursor.
func (world *Client) Redo() bool {
	if world.space == nil {
		return false
	}
	entry, ok := world.undo.PopRedo()
	if !ok {
		return false
	}
	return world.dispatchRedo(entry)
}

func (world *Client) dispatchUndo(entry UndoEntry) bool {
	switch entry.kind {
	case undoSculpt:
		// A Revert sculpt toggles the stored stroke off; re-emitted through the
		// normal pipeline so peers + the .mus3 log observe the undo.
		return world.emitRevert(entry.Sculpt)
	default:
		if err := world.space.Change(entry.Undo); err != nil {
			Engine.Raise(err)
			return false
		}
		return true
	}
}

func (world *Client) dispatchRedo(entry UndoEntry) bool {
	switch entry.kind {
	case undoSculpt:
		// Redo re-emits the same Revert, toggling the stroke back on.
		return world.emitRevert(entry.Sculpt)
	default:
		if err := world.space.Change(entry.Do); err != nil {
			Engine.Raise(err)
			return false
		}
		return true
	}
}

// emitRevert re-emits a stored stroke with Revert:true (and Commit:true so it is
// persisted + broadcast). The terrain editor toggles the matching stroke's
// reverted flag and recomputes. Sent directly (not via commitSculpt) so it is
// neither re-stamped nor re-recorded.
func (world *Client) emitRevert(brush musical.Sculpt) bool {
	brush.Revert = true
	brush.Commit = true
	if err := world.space.Sculpt(brush); err != nil {
		Engine.Raise(err)
		return false
	}
	return true
}

// findDesignForObject walks the design_to_entity map looking for the Design that
// owns the given Node3D.ID. O(N) over distinct designs — N is small in practice
// (a few dozen library entries referenced per scene) so the linear scan is fine.
// Returns the zero Design + false if the node isn't tracked anywhere; callers use
// that as the signal to skip recording an undo (we can't reconstruct what to redo
// without knowing the design).
func (world *Client) findDesignForObject(id Node3D.ID) (musical.Design, bool) {
	for design, ids := range world.design_to_entity {
		for _, candidate := range ids {
			if candidate == id {
				return design, true
			}
		}
	}
	return musical.Design{}, false
}
