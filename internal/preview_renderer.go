package internal

import (
	"context"
	"time"

	"grow.graphics/gd"
	"the.quetzal.community/aviary/protocol/vulture"
)

// PreviewRenderer is responsible for rendering items when the user
// is planning where to place it. As such, these items will follow
// the cursor and will be submitted to the Vulture API on click.
type PreviewRenderer struct {
	gd.Class[PreviewRenderer, gd.Node3D]

	mouseOver chan gd.Vector3

	preview chan string // resource name

	Vulture *Vulture
	terrain *Renderer
}

func (pr *PreviewRenderer) AsNode() gd.Node { return pr.Super().AsNode() }

func (pr *PreviewRenderer) Process(dt gd.Float) {
	tmp := pr.Temporary
	Input := gd.Input(tmp)
	for {
		select {
		case resource := <-pr.preview:
			scene, ok := gd.Load[gd.PackedScene](tmp, resource)
			if ok {
				instance, ok := gd.As[gd.Node3D](tmp, scene.Instantiate(pr.KeepAlive, 0))
				if ok {
					if pr.Super().AsNode().GetChildCount(false) > 0 {
						pr.Super().AsNode().GetChild(tmp, 0, false).QueueFree()
					}
					instance.AsNode3D().SetScale(gd.Vector3{0.3, 0.3, 0.3})
					pr.Super().AsNode().AddChild(instance.Super().AsNode(), false, 0)
				}
			}
		case pos := <-pr.mouseOver:
			pr.Super().AsNode3D().SetPosition(pr.Vulture.vultureToWorld(pr.Vulture.worldToVulture(pos)))
			continue
		default:

		}
		break
	}
	if Input.IsMouseButtonPressed(gd.MouseButtonLeft) {
		if pr.Super().AsNode().GetChildCount(false) > 0 {
			pr.Super().AsNode().GetChild(tmp, 0, false).QueueFree()
			pos := pr.Super().AsNode3D().GetPosition()
			area, cell, bump := pr.Vulture.worldToVulture(pos)
			packed := vulture.Elements{}
			packed.Add(vulture.ElementMarker{
				Cell: cell,
				Mesh: 1,
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
					tmp.Printerr(tmp.Variant(tmp.String(err.Error())))
				}
			}()
		}
	}
	pos := pr.Super().AsNode3D().GetPosition()
	pos.SetY(pr.terrain.HeightAt(pos))
	pr.Super().AsNode3D().SetPosition(pos)
}
