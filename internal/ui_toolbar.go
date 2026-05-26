package internal

import (
	"fmt"

	"graphics.gd/classdb"
	"graphics.gd/classdb/Control"
	"graphics.gd/classdb/TextureButton"
)

// Toolbar is the top-right wedge of action buttons sitting underneath
// the white EditorIndicator triangle. The dark-gray backing triangle
// is a separate Triangle child placed below in scene order; the four
// TextureButtons live along its visible hypotenuse.
//
// Buttons:
//   - Settings: opens (TODO) the settings panel
//   - Undo:     reverses the last committed change (TODO — musical
//     doesn't yet have an undo log; stubbed for now)
//   - Redo:     replays the most recently undone change (TODO)
//   - Export:   triggers a snapshot/save export (TODO — currently
//     just logs; wires into the existing Ctrl+S save flow
//     in a follow-up so the toolbar lands first)
type Toolbar struct {
	Control.Extension[Toolbar] `gd:"AviaryToolbar"`
	classdb.Tool

	Settings TextureButton.Instance
	Undo     TextureButton.Instance
	Redo     TextureButton.Instance
	Export   TextureButton.Instance

	client *Client
}

func (tb *Toolbar) Ready() {
	tb.Settings.AsBaseButton().OnPressed(func() {
		fmt.Println("toolbar: settings (TODO)")
	})
	tb.Undo.AsBaseButton().OnPressed(func() {
		if tb.client != nil {
			tb.client.Undo()
		}
	})
	tb.Redo.AsBaseButton().OnPressed(func() {
		if tb.client != nil {
			tb.client.Redo()
		}
	})
	tb.Export.AsBaseButton().OnPressed(func() {
		fmt.Println("toolbar: export (TODO — wire into save/snapshot flow)")
	})
}
