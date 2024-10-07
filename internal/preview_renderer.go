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
}

func (pr *PreviewRenderer) AsNode() gd.Node { return pr.Super().AsNode() }

func (pr *PreviewRenderer) Process(dt gd.Float) {
	tmp := pr.Temporary
	select {
	case resource := <-pr.preview:
		scene, ok := gd.Load[gd.PackedScene](tmp, resource)
		if ok {
			instance, ok := gd.As[gd.Node3D](tmp, scene.Instantiate(pr.KeepAlive, 0))
			if ok {
				if pr.Super().AsNode().GetChildCount(false) > 0 {
					pr.Super().AsNode().GetChild(tmp, 0, false).QueueFree()
				}
				pr.Super().AsNode().AddChild(instance.Super().AsNode(), false, 0)
			}
		}
	case pos := <-pr.mouseOver:
		pr.Super().AsNode3D().SetPosition(pos)
	default:
	}
}

func (pr *PreviewRenderer) UnhandledInput(event gd.InputEvent) {
	tmp := pr.Temporary
	switch event := tmp.Variant(event).Interface(tmp).(type) {
	case gd.InputEventMouseButton:
		if event.GetButtonIndex() == gd.MouseButtonLeft {
			if pr.Super().AsNode().GetChildCount(false) > 0 {
				pr.Super().AsNode().GetChild(tmp, 0, false).QueueFree()

				pos := pr.Super().AsNode3D().GetPosition()
				area := pr.Vulture.WorldSpaceToVultureSpace(pos)
				cell := pr.Vulture.WorldSpaceToVultureCell(pos)
				render := vulture.Render{
					Area: vulture.Area{int16(area[0]), int16(area[1])},
					Cell: vulture.Cell(cell[1]*16 + cell[0]),
					Mesh: 1,
				}
				go func() {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					if err := pr.Vulture.api.Render(ctx, render); err != nil {
						tmp.Printerr(tmp.Variant(tmp.String(err.Error())))
					}
				}()
			}
		}
	}
}
