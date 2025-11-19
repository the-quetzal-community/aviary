package internal

import "graphics.gd/classdb/Node3D"

type SceneryEditor struct {
	Node3D.Extension[SceneryEditor]
}

func (es *SceneryEditor) Tabs() []string {
	return []string{
		"foliage",
		"mineral",
		"housing",
		"village",
		"farming",
		"factory",
		"defense",
		"obelisk",
		"citizen",
		"trinket",
		"critter",
		"special",
		"pathway",
		"fencing",
		"vehicle",
	}
}
