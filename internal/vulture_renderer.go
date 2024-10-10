package internal

import (
	"context"
	"fmt"
	"sync/atomic"

	"grow.graphics/gd"
	"the.quetzal.community/aviary/protocol/vulture"
)

// VultureRenderer will open a Vulture Events stream and render all
// neighboring regions around the focal point.
type VultureRenderer struct {
	gd.Class[VultureRenderer, gd.Node3D] `gd:"VultureRenderer"`

	ActiveContent gd.Node
	CachedContent gd.Node

	ActiveRegions gd.Node
	CachedRegions gd.Node

	Vulture *Vulture

	listening atomic.Bool
	events    <-chan []vulture.Deltas

	regions map[vulture.Region]vulture.Elements

	terrain *TerrainRenderer
}

func (vr *VultureRenderer) AsNode() gd.Node { return vr.Super().AsNode() }

func (vr *VultureRenderer) start() {
	tmp := gd.NewLifetime(vr.Temporary)
	vr.regions = make(map[vulture.Region]vulture.Elements)
	go vr.listenForEvents(tmp)
}

func (vr *VultureRenderer) listenForEvents(tmp gd.Lifetime) {
	defer tmp.End()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	deltas, err := vr.Vulture.api.Events(ctx)
	if err != nil {
		tmp.Printerr(tmp.Variant(tmp.String(err.Error())))
		return
	}
	vr.events = deltas
	vr.listening.Store(true)
}

func (vr *VultureRenderer) Process(dt gd.Float) {
	if !vr.listening.Load() {
		return
	}
	for {
		select {
		case deltas := <-vr.events:
			vr.apply(deltas)
		default:
			break
		}
		break
	}
}

func (vr *VultureRenderer) apply(deltas []vulture.Deltas) {
	tmp := vr.Temporary
	for _, delta := range deltas {
		buf := vr.regions[delta.Region]
		end := buf.Len()
		buf.Apply(delta)
		vr.regions[delta.Region] = buf
		name := fmt.Sprint(delta.Region)
		node := vr.ActiveContent.AsNode().GetNodeOrNull(tmp, tmp.String(name).NodePath(tmp))
		if node == (gd.Node{}) {
			area := *gd.Create(vr.KeepAlive, new(gd.Node))
			area.SetName(tmp.String(name))
			vr.ActiveContent.AsNode().AddChild(area, false, 0)
			node = vr.ActiveContent.AsNode().GetNodeOrNull(tmp, tmp.String(name).NodePath(tmp))
		}
		for offset, element := range delta.Iter(end) {
			if element.Type() != vulture.ElementIsMarker {
				continue
			}
			vr.assert(delta.Region, node, buf, offset, element.Marker())
		}
	}
}

func (vr *VultureRenderer) assert(regionID vulture.Region, region gd.Node, buf vulture.Elements, offset vulture.Offset, element *vulture.ElementMarker) {
	tmp := vr.Temporary
	name := fmt.Sprint(offset)
	node := region.AsNode().GetNodeOrNull(tmp, tmp.String(name).NodePath(tmp))
	if node == (gd.Node{}) {
		area := gd.Create(vr.KeepAlive, new(gd.Node3D))
		area.Super().AsNode().SetName(tmp.String(name))
		region.AsNode().AddChild(area.Super().AsNode(), false, 0)
		node = region.AsNode().GetNodeOrNull(tmp, tmp.String(name).NodePath(tmp))
	}
	parent, ok := gd.As[gd.Node3D](tmp, node)
	if !ok {
		return
	}
	world := vr.Vulture.vultureToWorld(regionID, element.Cell, element.Bump)
	world.SetY(vr.terrain.HeightAt(world))
	parent.SetPosition(world)
	parent.SetScale(gd.Vector3{0.3, 0.3, 0.3})
	scene, ok := gd.Load[gd.PackedScene](tmp, "res://library/wildfire_games/foliage/acacia.glb")
	if ok {
		instance, ok := gd.As[gd.Node3D](tmp, scene.Instantiate(vr.KeepAlive, 0))
		if ok {
			if parent.Super().AsNode().GetChildCount(false) > 0 {
				parent.Super().AsNode().GetChild(tmp, 0, false).QueueFree()
			}
			parent.Super().AsNode().AddChild(instance.Super().AsNode(), false, 0)
		}
	}
}
