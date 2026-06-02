package internal

import (
	"graphics.gd/classdb/CanvasLayer"
	"graphics.gd/classdb/ProgressBar"
	"graphics.gd/variant/Float"
)

// SceneLoader is the full-screen overlay shown while a world is being replayed
// (see Client.beginLoading). It mirrors the LibraryDownloader splash: an opaque
// cover with a centred icon and a progress bar. It lives on its own high
// CanvasLayer so it always draws on top of the editor UI, and the Client
// suppresses 3D rendering (Viewport.SetDisable3d) while it is up so the
// half-built scene behind it is never drawn.
type SceneLoader struct {
	CanvasLayer.Extension[SceneLoader] `gd:"AviarySceneLoader"`

	Progress ProgressBar.Instance
}

func (s *SceneLoader) Ready() {
	s.Progress.AsRange().SetMaxValue(100)
}

// SetProgress shows a determinate bar at the given fraction (0..1). Used for
// single-player loads where the .mus3 size (and then the queued-mutation count)
// gives us a real proportion.
func (s *SceneLoader) SetProgress(frac float64) {
	if frac < 0 {
		frac = 0
	} else if frac > 1 {
		frac = 1
	}
	s.Progress.AsCanvasItem().SetVisible(true)
	s.Progress.AsRange().SetValue(Float.X(frac * 100))
}

// SetIndeterminate hides the bar (the icon pulse animation carries the "still
// working" feel). Used for multiplayer joins where state streams from the host
// with no known total.
func (s *SceneLoader) SetIndeterminate() {
	s.Progress.AsCanvasItem().SetVisible(false)
}
