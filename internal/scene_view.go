package internal

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"

	"graphics.gd/variant/Euler"
	"graphics.gd/variant/Vector3"
	"the.quetzal.community/aviary/internal/musical"
)

// scene_view.go persists, per scene (WorkID), the editing camera rig pose so a
// reload returns you to the view you left. It lives next to that scene's snap
// thumbnail (user://snaps/<WorkID>.png) as a sibling .json and, like the snap,
// is a private, local, per-WorkID artifact — NOT a musical mutation, so it stays
// out of the shared .mus3 stroke log (a camera pose is per-user view state, not
// a scene edit observable by peers). Written on save (Ctrl+S, alongside the
// snap); restored in Client.finishLoading.

const sceneViewVersion = 1

// sceneView is the orbit-rig pose: Focus is the world focus point (with yaw in
// its FocalAngle.Y), LensAngle carries pitch (X), and Camera's local Z is the
// zoom distance. The fields are plain float32 structs, so they JSON-marshal as
// {"X":..,"Y":..,"Z":..} with no conversion. See the rig in Client.FocalPoint.
type sceneView struct {
	Version    int
	Focus      Vector3.XYZ   // FocalPoint local position (its parent is at origin → world focus)
	FocalAngle Euler.Radians // FocalPoint rotation (yaw)
	LensAngle  Euler.Radians // Lens rotation (pitch)
	Camera     Vector3.XYZ   // Camera local position (Z = zoom distance)
}

func sceneViewPath(work musical.WorkID) string {
	name := base64.RawURLEncoding.EncodeToString(work[:])
	return UserDataDir + "/snaps/" + name + ".json"
}

// writeSceneView atomically writes the pose alongside the scene's snap (temp +
// rename, so a crash mid-write never leaves a torn file).
func writeSceneView(work musical.WorkID, v sceneView) error {
	if err := os.MkdirAll(UserDataDir+"/snaps", 0777); err != nil {
		return err
	}
	buf, err := json.Marshal(v)
	if err != nil {
		return err
	}
	path := sceneViewPath(work)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf, 0666); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// readSceneView loads the saved pose for a scene, or an error if it is missing /
// unreadable / a different version (callers treat any error as "no saved view").
func readSceneView(work musical.WorkID) (sceneView, error) {
	var v sceneView
	buf, err := os.ReadFile(sceneViewPath(work))
	if err != nil {
		return v, err
	}
	if err := json.Unmarshal(buf, &v); err != nil {
		return v, err
	}
	if v.Version != sceneViewVersion {
		return v, fmt.Errorf("scene view version %d != %d", v.Version, sceneViewVersion)
	}
	return v, nil
}

// captureSceneView reads the current orbit-rig pose. Main-thread only.
func (world *Client) captureSceneView() sceneView {
	return sceneView{
		Version:    sceneViewVersion,
		Focus:      world.FocalPoint.AsNode3D().Position(),
		FocalAngle: world.FocalPoint.AsNode3D().Rotation(),
		LensAngle:  world.FocalPoint.Lens.AsNode3D().Rotation(),
		Camera:     world.FocalPoint.Lens.Camera.AsNode3D().Position(),
	}
}

// applySceneView restores a saved orbit-rig pose (mirrors the possess-exit view
// restore in client_possess.go). The per-frame camera-terrain collision re-lifts
// FocalPoint.Y next frame, so a stale ground offset self-corrects. Main-thread only.
func (world *Client) applySceneView(v sceneView) {
	world.FocalPoint.AsNode3D().SetPosition(v.Focus)
	world.FocalPoint.AsNode3D().SetRotation(v.FocalAngle)
	world.FocalPoint.Lens.AsNode3D().SetRotation(v.LensAngle)
	world.FocalPoint.Lens.Camera.AsNode3D().SetPosition(v.Camera)
}
