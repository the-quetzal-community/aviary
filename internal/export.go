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
	"graphics.gd/classdb/Node3D"
)

// Exporter is an optional capability an editor implements to
// hand-pick what gets included in a glTF export. Default behavior
// (no Exporter implementation) is to pack the editor's entire
// AsNode3D() subtree, which includes context-only props like the
// ground plate the vehicle/critter editors render under the body.
//
// Implementations typically Duplicate() the relevant in-editor
// nodes onto a fresh Node3D root and return that — the caller
// QueueFrees the returned tree once the .glb has been written,
// so it's safe to assemble a one-shot subtree for the export
// without worrying about cleanup.
type Exporter interface {
	// ExportSubtree builds and returns a Node3D tree to pack into
	// the glTF. The caller owns it. The export uses the returned
	// node's transform AS-IS, so editors can apply recenter offsets
	// (e.g. zero out a body's float-above-ground rise) by setting
	// the root or its children's positions accordingly.
	ExportSubtree() Node3D.Instance
}

// Export prompts the user with an OS-native file save dialog and
// writes the *current editor's* 3D subtree to the chosen path as
// a glTF binary (.glb). glTF is the lingua-franca interchange
// format for 3D — the resulting file opens in Blender, Three.js,
// Unreal, Unity, and any web viewer that speaks the spec.
//
// Only the active editor's content is exported, not the whole
// world. When the editor implements [Exporter] the returned
// subtree is exported; otherwise it falls back to AsNode3D().
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
			root, dispose := exportRootFor(editor)
			defer dispose()
			if err := writeNodeAsGLTF(root, dst); err != nil {
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

// exportRootFor resolves the Node to pass to glTF packing. If the
// editor implements [Exporter] the returned subtree is used and
// the dispose closure queue-frees it after the write. Otherwise
// the editor's AsNode3D() is used directly (no cleanup needed).
func exportRootFor(editor Editor) (root Node.Instance, dispose func()) {
	if exp, ok := editor.(Exporter); ok {
		sub := exp.ExportSubtree()
		return sub.AsNode(), func() { sub.AsNode().QueueFree() }
	}
	return editor.AsNode3D().AsNode(), func() {}
}
