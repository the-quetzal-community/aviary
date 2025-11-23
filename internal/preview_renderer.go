package internal

import (
	"graphics.gd/classdb/CollisionObject3D"
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/PackedScene"
	"graphics.gd/classdb/Resource"
	"graphics.gd/variant/Object"
)

type PreviewRenderer struct {
	Node3D.Instance

	design string
}

func (preview *PreviewRenderer) Design() string {
	return preview.design
}

func (preview *PreviewRenderer) SetDesign(design string) {
	preview.design = design
	instance := Resource.Load[PackedScene.Is[Node3D.Instance]](design).Instantiate()
	preview.remove_collisions(instance.AsNode())
	if preview.AsNode().GetChildCount() > 0 {
		Node.Instance(preview.AsNode().GetChild(0)).QueueFree()
	}
	preview.AsNode().AddChild(instance.AsNode())
}

func (preview *PreviewRenderer) Remove() {
	if preview.AsNode().GetChildCount() > 0 {
		Node.Instance(preview.AsNode().GetChild(0)).QueueFree()
	}
	preview.design = ""
}

func (preview *PreviewRenderer) remove_collisions(node Node.Instance) {
	if body, ok := Object.As[CollisionObject3D.Instance](node); ok {
		body.SetCollisionLayer(0)
	}
	for _, child := range node.GetChildren() {
		preview.remove_collisions(child)
	}
}
