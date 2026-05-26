package internal

import (
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"graphics.gd/classdb/DisplayServer"
	"graphics.gd/classdb/Engine"
	"graphics.gd/classdb/OS"
)

// Export prompts the user with an OS-native file save dialog and
// copies this client's .mus3 stream (the per-device musical log
// for the current scene) to the chosen path. The .mus3 is the
// canonical exportable artifact — handing it to a collaborator
// (or restoring it later) lets them reconstruct the exact scene
// state, since musical is an append-only log of every committed
// mutation.
//
// Falls back to a stderr message on platforms without a native
// file dialog (e.g. a hypothetical headless build); on supported
// targets — Linux X11/Wayland, Windows, macOS, Android — Godot
// surfaces the platform's own picker.
func (world *Client) Export() {
	if !DisplayServer.HasFeature(DisplayServer.FeatureNativeDialogFile) {
		fmt.Fprintln(os.Stderr, "Export: native file dialog unavailable on this platform")
		return
	}

	workName := base64.RawURLEncoding.EncodeToString(world.record[:])
	src := filepath.Join(OS.GetUserDataDir(), "saves", workName, UserState.Device+".mus3")
	if _, err := os.Stat(src); err != nil {
		// Nothing committed yet — no .mus3 exists. Warn instead of
		// popping a dialog that would silently write nothing.
		fmt.Fprintln(os.Stderr, "Export: nothing to export yet (", err, ")")
		return
	}

	startDir, _ := os.UserHomeDir()
	suggested := "aviary-" + workName + ".mus3"

	err := DisplayServer.FileDialogShow(
		"Export Aviary Project",
		startDir,
		suggested,
		false, // show_hidden
		DisplayServer.FileDialogModeSaveFile,
		[]string{"*.mus3;Aviary Save"},
		func(status bool, selected_paths []string, _ int) {
			if !status || len(selected_paths) == 0 {
				// User cancelled.
				return
			}
			dst := selected_paths[0]
			if err := exportCopy(src, dst); err != nil {
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

// exportCopy is a plain copy through Go's stdlib — we don't go
// through FileAccess here because the destination is outside of
// res:// / user:// and the user has explicitly chosen it through
// the OS picker, so the OS already vetted the permission.
func exportCopy(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}
