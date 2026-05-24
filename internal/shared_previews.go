package internal

import (
	"strings"

	"graphics.gd/classdb/CollisionObject3D"
	"graphics.gd/classdb/MeshInstance3D"
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/PackedScene"
	"graphics.gd/classdb/Resource"
	"graphics.gd/variant/AABB"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Transform3D"
)

type PreviewRenderer struct {
	Node3D.Instance

	design string
}

func (preview *PreviewRenderer) Design() string {
	return preview.design
}

func (preview *PreviewRenderer) SetDesign(design string) *PreviewRenderer {
	preview.design = design
	var instance Node3D.Instance
	// MakeHuman clothing items (and other raw-OBJ assets) don't ship
	// with .import companions, so Godot's resource loader spews
	// "Resource file not found" errors before returning Nil. Detect
	// them by extension and use the static-mesh loader directly — it
	// uses FileAccess.Open against the same res:// path, which works
	// because the library is mounted into the resource filesystem
	// even though no PackedScene importer ever ran.
	if strings.HasSuffix(design, ".obj") {
		instance = loadStaticObjNode(design)
	} else {
		scene := Resource.Load[PackedScene.Is[Node3D.Instance]](design)
		if scene != (PackedScene.Is[Node3D.Instance]{}) {
			instance = scene.Instantiate()
		}
	}
	if preview.AsNode().GetChildCount() > 0 {
		Node.Instance(preview.AsNode().GetChild(0)).QueueFree()
	}
	if instance == Node3D.Nil {
		return preview
	}
	preview.remove_collisions(instance.AsNode())
	preview.AsNode().AddChild(instance.AsNode())
	return preview
}

func (preview *PreviewRenderer) Remove() {
	if preview.AsNode().GetChildCount() > 0 {
		Node.Instance(preview.AsNode().GetChild(0)).QueueFree()
	}
	preview.design = ""
}

func (preview *PreviewRenderer) AABB() (bounds AABB.PositionSize) {
	return preview.aabb(preview.AsNode3D())
}

func (preview *PreviewRenderer) aabb(node Node3D.Instance) (bounds AABB.PositionSize) {
	if instance, ok := Object.As[MeshInstance3D.Instance](node); ok {
		bounds = Transform3D.TransformAABB(instance.Mesh().GetAabb(), node.Transform())
	}
	for i := range node.AsNode().GetChildCount() {
		if node, ok := Object.As[Node3D.Instance](node.AsNode().GetChild(i)); ok {
			bounds = AABB.Merge(bounds, preview.aabb(node))
		}
	}
	return bounds
}

func (preview *PreviewRenderer) remove_collisions(node Node.Instance) {
	if body, ok := Object.As[CollisionObject3D.Instance](node); ok {
		body.SetCollisionLayer(0)
	}
	for _, child := range node.GetChildren() {
		preview.remove_collisions(child)
	}
}
