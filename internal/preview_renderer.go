package internal

import (
	"graphics.gd/classdb/CollisionObject3D"
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/PackedScene"
	"graphics.gd/classdb/Resource"
	"graphics.gd/classdb/Texture2D"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Path"
	"graphics.gd/variant/Vector3"
	"the.quetzal.community/aviary/internal/musical"
)

// PreviewRenderer is responsible for rendering items when the user
// is planning where to place it. As such, these items will follow
// the cursor and will be submitted to the Vulture API on click.
type PreviewRenderer struct {
	Node3D.Extension[PreviewRenderer]

	mouseOver chan Vector3.XYZ

	preview chan Path.ToResource // resource name

	terrain *TerrainRenderer

	current string

	client *Client
}

func (pr *PreviewRenderer) Enabled() bool {
	return pr.AsNode().GetChildCount() > 0
}

func (pr *PreviewRenderer) Discard() {
	if pr.AsNode().GetChildCount() > 0 {
		Node.Instance(pr.AsNode().GetChild(0)).QueueFree()
	}
}

func (pr *PreviewRenderer) Place() {
	if pr.AsNode().GetChildCount() > 0 {
		Node.Instance(pr.AsNode().GetChild(0)).QueueFree()

		design, ok := pr.client.loaded[pr.current]
		if !ok {
			pr.client.design_ids++
			design = musical.Design{
				Author: pr.client.id,
				Number: pr.client.design_ids,
			}
			pr.client.space.Import(musical.Import{
				Design: design,
				Import: pr.current,
			})
		}
		pr.client.entities++
		pr.client.space.Change(musical.Change{
			Author: pr.client.id,
			Entity: musical.Entity{
				Author: pr.client.id,
				Number: pr.client.entities,
			},
			Design: design,
			Offset: pr.AsNode3D().Position(),
			Angles: pr.AsNode3D().Rotation(),
			Commit: true,
		})
	}
}

func (pr *PreviewRenderer) remove_collisions(node Node.Instance) {
	if body, ok := Object.As[CollisionObject3D.Instance](node); ok {
		body.SetCollisionLayer(0)
	}
	for _, child := range node.GetChildren() {
		pr.remove_collisions(child)
	}
}

func (pr *PreviewRenderer) Process(dt Float.X) {
	for {
		select {
		case resource := <-pr.preview:
			scene := Resource.Load[PackedScene.Instance](resource)
			instance, ok := Object.As[Node3D.Instance](scene.Instantiate())
			if ok {
				if pr.AsNode().GetChildCount() > 0 {
					Node.Instance(pr.AsNode().GetChild(0)).QueueFree()
				}
				pr.remove_collisions(instance.AsNode())
				instance.AsNode3D().SetScale(Vector3.MulX(instance.AsNode3D().Scale(), 0.1))
				pr.AsNode().AddChild(instance.AsNode())
			}
			pr.current = resource.String()
		case pos := <-pr.mouseOver:
			pr.AsNode3D().SetPosition(pos)
			continue
		default:

		}
		break
	}
	pos := pr.AsNode3D().Position()
	pos.Y = (pr.terrain.HeightAt(pos))
	pr.AsNode3D().SetPosition(pos)
}

func (pr *PreviewRenderer) Ready() {

}

func (pr *PreviewRenderer) GenerateTexture2D(scene PackedScene.Instance) Texture2D.Instance {
	return Texture2D.Instance{}
}
