package internal

import (
	"graphics.gd/classdb/Input"
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/PackedScene"
	"graphics.gd/classdb/Resource"
	"graphics.gd/classdb/Texture2D"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Path"
	"graphics.gd/variant/Vector3"
	"the.quetzal.community/aviary/internal/community"
)

// PreviewRenderer is responsible for rendering items when the user
// is planning where to place it. As such, these items will follow
// the cursor and will be submitted to the Vulture API on click.
type PreviewRenderer struct {
	Node3D.Extension[PreviewRenderer]

	mouseOver chan Vector3.XYZ

	preview chan Path.ToResource // resource name

	terrain *Renderer

	current string

	client *Client
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
	if Input.IsMouseButtonPressed(Input.MouseButtonLeft) {
		if pr.AsNode().GetChildCount() > 0 {
			Node.Instance(pr.AsNode().GetChild(0)).QueueFree()
			pr.client.api.InsertObject(pr.current, community.Object{
				Offset: pr.AsNode3D().Position(),
				Angles: pr.AsNode3D().Rotation(),
			})
		}
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
