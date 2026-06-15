package internal

import "graphics.gd/classdb/DisplayServer"

// FitWindowToScreen guards against the configured 4K default window (3840×2160 —
// see graphics/project.godot's window/size/viewport_*) opening larger than the
// display's work area on smaller screens. When it does, the window's top (its
// title bar and the app's own top panel) is pushed above the screen and the rest
// sits under the taskbar — the "no top panel, taskbar covers it" report on 1080p
// Windows. When the window, decorations included, doesn't fit the usable area,
// maximize it so the OS sizes it to the work area with the taskbar respected; on
// screens large enough for the 4K window it stays windowed and untouched. Called
// once at startup for the game window only (not the editor or web — see main.go).
func FitWindowToScreen() {
	usable := DisplayServer.ScreenGetUsableRect()          // work area, excludes the taskbar
	total := DisplayServer.WindowGetSizeWithDecorations(0) // window incl. title bar / borders
	if total.X > usable.Size.X || total.Y > usable.Size.Y {
		DisplayServer.WindowSetMode(DisplayServer.WindowModeMaximized, 0)
	}
}
