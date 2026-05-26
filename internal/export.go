package internal

import (
	"fmt"
	"os"
	"strings"

	"graphics.gd/classdb/DisplayServer"
	"graphics.gd/classdb/Engine"
	"graphics.gd/classdb/GLTFDocument"
	"graphics.gd/classdb/GLTFState"
	"graphics.gd/classdb/Node"
)

// Export prompts the user with an OS-native file save dialog and
// writes the *current editor's* 3D subtree to the chosen path as
// a glTF binary (.glb). glTF is the lingua-franca interchange
// format for 3D — the resulting file opens in Blender, Three.js,
// Unreal, Unity, and any web viewer that speaks the spec.
//
// Only the active editor's node is exported, not the whole world:
// when the user is editing a vehicle they expect to get just the
// vehicle, not the terrain + every other editor's placed content.
// If no editor is active the call is a no-op with a stderr note.
//
// Falls back to a stderr message on platforms without a native
// file dialog (hypothetical headless builds only); on supported
// targets — Linux X11/Wayland, Windows, macOS, Android — Godot
// surfaces the platform's own picker.
func (world *Client) Export() {
	if !DisplayServer.HasFeature(DisplayServer.FeatureNativeDialogFile) {
		fmt.Fprintln(os.Stderr, "Export: native file dialog unavailable on this platform")
		return
	}
	if world.ui == nil || world.ui.Editor == nil || world.ui.Editor.editor == nil {
		fmt.Fprintln(os.Stderr, "Export: no active editor to export")
		return
	}
	editor := world.ui.Editor.editor

	startDir, _ := os.UserHomeDir()
	suggested := "aviary-" + editor.Name() + ".glb"

	err := DisplayServer.FileDialogShow(
		"Export "+editor.Name()+" as glTF",
		startDir,
		suggested,
		false, // show_hidden
		DisplayServer.FileDialogModeSaveFile,
		// Two filters: .glb (single binary file, recommended) and
		// .gltf (JSON + sidecar buffer/textures). Godot's GLTFDocument
		// picks the format from the chosen extension.
		[]string{
			"*.glb;glTF Binary",
			"*.gltf;glTF (JSON + buffers)",
		},
		func(status bool, selected_paths []string, _ int) {
			if !status || len(selected_paths) == 0 {
				return // user cancelled
			}
			dst := selected_paths[0]
			// Append .glb if the user didn't type an extension —
			// matches typical OS picker behaviour where the filter
			// hint isn't always auto-appended.
			if !strings.HasSuffix(strings.ToLower(dst), ".glb") &&
				!strings.HasSuffix(strings.ToLower(dst), ".gltf") {
				dst += ".glb"
			}
			if err := writeNodeAsGLTF(editor.AsNode3D().AsNode(), dst); err != nil {
				Engine.Raise(fmt.Errorf("export failed: %w", err))
				return
			}
			fmt.Println("Exported", dst)
		},
		DisplayServer.MainWindowId,
	)
	if err != nil {
		Engine.Raise(fmt.Errorf("file dialog failed: %w", err))
	}
}

// writeNodeAsGLTF packs the supplied node subtree into a GLTFState
// via GLTFDocument.AppendFromScene, then writes it to disk.
// WriteToFilesystem picks the on-disk format (.glb binary vs .gltf
// JSON-plus-buffers) from the path's extension.
func writeNodeAsGLTF(root Node.Instance, path string) error {
	doc := GLTFDocument.New()
	state := GLTFState.New()
	if err := doc.AppendFromScene(root, state); err != nil {
		return fmt.Errorf("append_from_scene: %w", err)
	}
	if err := doc.WriteToFilesystem(state, path); err != nil {
		return fmt.Errorf("write_to_filesystem: %w", err)
	}
	return nil
}
