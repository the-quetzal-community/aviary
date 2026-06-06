package internal

import (
	"fmt"
	"strings"

	"graphics.gd/classdb/DirAccess"
	"graphics.gd/classdb/FileAccess"

	"the.quetzal.community/aviary/internal/citizen"
)

const citizenLibraryRoot = "res://library/makehuman"

// LoadCitizenAssets loads the parsed citizen base mesh and target deltas
// from the asset library. File IO uses Godot's FileAccess so it works
// against both preview.pck (production) and the live filesystem (dev
// mode). The .obj is parsed directly rather than going through Godot's
// glTF/OBJ importers because those split vertices at UV seams; the raw
// vertex order must stay 1:1 with what MakeHuman's .target files
// reference, or applied deltas hit the wrong vertices.
func LoadCitizenAssets() (*citizen.BaseMesh, []*citizen.Target, error) {
	base, err := loadCitizenBase(citizenLibraryRoot + "/base.obj")
	if err != nil {
		return nil, nil, err
	}
	targets, err := loadCitizenTargets(citizenLibraryRoot + "/targets")
	if err != nil {
		return nil, nil, err
	}
	return base, targets, nil
}

// openCitizenText opens a citizen asset (res:// in production, the live
// filesystem in dev mode) via Godot's FileAccess and returns its full text.
// Centralises the open + nil-check + "citizen: cannot open" error that every
// citizen .obj/.target/.mhclo loader repeated.
func openCitizenText(path string) (string, error) {
	f := FileAccess.Open(path, FileAccess.Read)
	if f == FileAccess.Nil {
		return "", fmt.Errorf("citizen: cannot open %s", path)
	}
	return f.GetAsText(), nil
}

func loadCitizenBase(path string) (*citizen.BaseMesh, error) {
	text, err := openCitizenText(path)
	if err != nil {
		return nil, err
	}
	return citizen.ParseOBJ(path, strings.NewReader(text))
}

func loadCitizenTargets(root string) ([]*citizen.Target, error) {
	var out []*citizen.Target
	var walk func(dir string) error
	walk = func(dir string) error {
		d := DirAccess.Open(dir)
		if d == DirAccess.Nil {
			return fmt.Errorf("citizen: cannot open %s", dir)
		}
		for _, file := range d.GetFiles() {
			if !strings.HasSuffix(file, ".target") {
				continue
			}
			path := dir + "/" + file
			content, err := openCitizenText(path)
			if err != nil {
				return err
			}
			rel := strings.TrimSuffix(strings.TrimPrefix(path, root+"/"), ".target")
			t, err := citizen.ParseTarget(rel, strings.NewReader(content))
			if err != nil {
				return err
			}
			out = append(out, t)
		}
		for _, sub := range d.GetDirectories() {
			if err := walk(dir + "/" + sub); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(root); err != nil {
		return nil, err
	}
	return out, nil
}
