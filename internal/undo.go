package internal

import (
	"graphics.gd/classdb/Engine"
	"graphics.gd/classdb/Node3D"
	"the.quetzal.community/aviary/internal/musical"
)

// UndoEntry pairs the forward operation with the operation that
// would reverse it. Both are musical.Change records and both go
// through the normal client.space.Change pipeline, so the rest of
// the world (peers, the .mus3 log) sees an undo as just another
// committed Change — there's no separate undo channel and the
// musical format doesn't change.
type UndoEntry struct {
	Do, Undo musical.Change
}

// UndoStack is a per-client (local-only) history of reversible
// operations. The stack itself is NOT shared with peers — each
// client tracks its own undo cursor — but the inverse Changes
// dispatched on Undo() ARE shared, so when you press Undo your
// peers see the resulting state change just as if you'd performed
// it manually.
//
// Linear history semantics: pushing a new entry while the cursor
// is mid-stack discards everything past the cursor (the classic
// "doing something new loses your future"). This matches what
// every editor since Mac MacPaint has done.
type UndoStack struct {
	history []UndoEntry
	cursor  int // 0..len(history); next index for Push / Redo
}

// Push appends a new entry, truncating any redo entries past the
// cursor.
func (s *UndoStack) Push(e UndoEntry) {
	if s.cursor < len(s.history) {
		s.history = s.history[:s.cursor]
	}
	s.history = append(s.history, e)
	s.cursor++
}

// PopUndo returns the inverse to apply and moves the cursor back.
// Caller dispatches the returned Change through space.Change.
func (s *UndoStack) PopUndo() (musical.Change, bool) {
	if s.cursor == 0 {
		return musical.Change{}, false
	}
	s.cursor--
	return s.history[s.cursor].Undo, true
}

// PopRedo returns the forward op at the cursor and advances.
func (s *UndoStack) PopRedo() (musical.Change, bool) {
	if s.cursor >= len(s.history) {
		return musical.Change{}, false
	}
	e := s.history[s.cursor]
	s.cursor++
	return e.Do, true
}

func (s *UndoStack) CanUndo() bool { return s.cursor > 0 }
func (s *UndoStack) CanRedo() bool { return s.cursor < len(s.history) }

// RecordChange registers an op with its inverse. Use at the point
// where the forward operation is dispatched, immediately after the
// space.Change(do) call — passing the same Change for `do` and a
// constructed inverse for `undo`.
func (world *Client) RecordChange(do, undo musical.Change) {
	// Both sides must commit when replayed via undo/redo.
	do.Commit = true
	undo.Commit = true
	world.undo.Push(UndoEntry{Do: do, Undo: undo})
}

// Undo dispatches the inverse of the most recent recorded operation.
// Returns false if there's nothing to undo. Safe to call on an
// empty stack — it's just a no-op.
func (world *Client) Undo() bool {
	if world.space == nil {
		return false
	}
	op, ok := world.undo.PopUndo()
	if !ok {
		return false
	}
	if err := world.space.Change(op); err != nil {
		Engine.Raise(err)
		return false
	}
	return true
}

// Redo re-dispatches the forward operation at the cursor.
func (world *Client) Redo() bool {
	if world.space == nil {
		return false
	}
	op, ok := world.undo.PopRedo()
	if !ok {
		return false
	}
	if err := world.space.Change(op); err != nil {
		Engine.Raise(err)
		return false
	}
	return true
}

// findDesignForObject walks the design_to_entity map looking for
// the Design that owns the given Node3D.ID. O(N) over distinct
// designs — N is small in practice (a few dozen library entries
// referenced per scene) so the linear scan is fine. Returns the
// zero Design + false if the node isn't tracked anywhere; callers
// use that as the signal to skip recording an undo (we can't
// reconstruct what to redo without knowing the design).
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
