package internal

import (
	"fmt"
	"os"
	"strings"

	"graphics.gd/classdb/DisplayServer"
	"graphics.gd/classdb/Engine"
	"graphics.gd/classdb/GLTFDocument"
	"graphics.gd/classdb/GLTFState"
)

// Export prompts the user with an OS-native file save dialog and
// writes the current 3D scene to the chosen path as a glTF binary
// (.glb). glTF is the lingua-franca interchange format for 3D —
// the resulting file opens in Blender, Three.js, Unreal, Unity,
// and any web viewer that speaks the spec, so this is what most
// users mean by "export my work".
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

	startDir, _ := os.UserHomeDir()

	err := DisplayServer.FileDialogShow(
		"Export Scene as glTF",
		startDir,
		"aviary-scene.glb",
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
			if err := world.writeSceneAsGLTF(dst); err != nil {
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

// writeSceneAsGLTF packs the entire AviaryWorld node tree into a
// GLTFState via GLTFDocument.AppendFromScene, then writes it to
// disk. WriteToFilesystem picks the on-disk format (.glb binary vs
// .gltf JSON-plus-buffers) from the path's extension.
//
// The whole world Node3D is exported: terrain tiles, every editor's
// placed children, lighting, environment. The UI overlay is a
// CanvasItem subtree (not a Node3D) so it doesn't get walked.
// Camera and FocalPoint are Node3Ds though — they'll appear in
// the exported file as empty transforms, which is harmless in
// downstream tools but something we could strip later if it
// turns into a real annoyance.
func (world *Client) writeSceneAsGLTF(path string) error {
	doc := GLTFDocument.New()
	state := GLTFState.New()
	if err := doc.AppendFromScene(world.AsNode(), state); err != nil {
		return fmt.Errorf("append_from_scene: %w", err)
	}
	if err := doc.WriteToFilesystem(state, path); err != nil {
		return fmt.Errorf("write_to_filesystem: %w", err)
	}
	return nil
}
