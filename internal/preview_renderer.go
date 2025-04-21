package internal

import (
	"context"
	"time"

	"graphics.gd/classdb"
	"graphics.gd/classdb/Engine"
	"graphics.gd/classdb/Input"
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/PackedScene"
	"graphics.gd/classdb/Resource"
	"graphics.gd/classdb/Texture2D"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Path"
	"graphics.gd/variant/Vector3"
	"the.quetzal.community/aviary/protocol/vulture"
)

// PreviewRenderer is responsible for rendering items when the user
// is planning where to place it. As such, these items will follow
// the cursor and will be submitted to the Vulture API on click.
type PreviewRenderer struct {
	classdb.Extension[PreviewRenderer, Node3D.Instance]

	mouseOver chan Vector3.XYZ

	preview chan Path.ToResource // resource name

	Vulture *Vulture
	terrain *Renderer

	current vulture.Upload
}

func (pr *PreviewRenderer) AsNode() Node.Instance { return pr.Super().AsNode() }

func (pr *PreviewRenderer) Process(dt Float.X) {
	for {
		select {
		case resource := <-pr.preview:
			scene := Resource.Load[PackedScene.Instance](resource)
			instance, ok := classdb.As[Node3D.Instance](Node.Instance(scene.Instantiate()))
			if ok {
				if pr.Super().AsNode().GetChildCount() > 0 {
					Node.Instance(pr.Super().AsNode().GetChild(0)).QueueFree()
				}
				instance.AsNode3D().SetScale(Vector3.New(0.3, 0.3, 0.3))
				pr.Super().AsNode().AddChild(instance.AsNode())
			}
			upload, ok := pr.Vulture.name2upload[resource.String()]
			if !ok {
				pr.Vulture.uploads++
				upload = pr.Vulture.uploads
				pr.Vulture.name2upload[resource.String()] = upload
				pr.Vulture.upload2name[upload] = resource.String()
			}
			pr.current = upload
		case pos := <-pr.mouseOver:
			pr.Super().AsNode3D().SetPosition(pr.Vulture.vultureToWorld(pr.Vulture.worldToVulture(pos)))
			continue
		default:

		}
		break
	}
	if Input.IsMouseButtonPressed(Input.MouseButtonLeft) {
		if pr.Super().AsNode().GetChildCount() > 0 {
			Node.Instance(pr.Super().AsNode().GetChild(0)).QueueFree()
			pos := pr.Super().AsNode3D().Position()
			area, cell, bump := pr.Vulture.worldToVulture(pos)
			packed := vulture.Elements{}
			packed.Add(vulture.ElementMarker{
				Cell: cell,
				Mesh: pr.current,
				Bump: bump,
			})
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := pr.Vulture.api.Reform(ctx, []vulture.Deltas{{
					Region: area,
					Packet: vulture.Time(time.Now().UnixNano()),
					Append: packed,
				}}); err != nil {
					Engine.Raise(err)
				}
			}()
		}
	}
	pos := pr.Super().AsNode3D().Position()
	pos.Y = (pr.terrain.HeightAt(pos))
	pr.Super().AsNode3D().SetPosition(pos)
}

func (pr *PreviewRenderer) Ready() {

}

func (pr *PreviewRenderer) GenerateTexture2D(scene PackedScene.Instance) Texture2D.Instance {
	return Texture2D.Instance{}
}
