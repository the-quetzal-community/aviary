package internal

import (
	"context"
	"fmt"
	"sync/atomic"

	"grow.graphics/gd"
	"the.quetzal.community/aviary/protocol/vulture"
)

// Renderer will open a Vulture Events stream and render all
// neighboring regions around the focal point.
type Renderer struct {
	gd.Class[Renderer, gd.Node3D] `gd:"VultureRenderer"`

	ActiveContent gd.Node
	CachedContent gd.Node

	ActiveRegions gd.Node
	CachedRegions gd.Node

	heightMapping map[vulture.Region][17 * 17]vulture.Height

	Vulture *Vulture

	listening atomic.Bool
	events    <-chan []vulture.Deltas

	regions map[vulture.Region]vulture.Elements
	reloads map[vulture.Region]bool

	mouseOver chan gd.Vector3

	shader gd.ShaderMaterial

	//
	// Terrain Brush parameters are used to represent modifications
	// to the terrain. Either for texturing or height map adjustments.
	//
	BrushActive bool
	BrushTarget gd.Vector3
	BrushRadius gd.Float
	BrushAmount gd.Float
	BrushDeltaV gd.Float
	brushEvents chan terrainBrushEvent
}

func (tr *Renderer) Ready() {
	tmp := tr.Temporary
	shader, ok := gd.Load[gd.Shader](tmp, "res://shader/terrain.gdshader")
	if !ok {
		return
	}
	grass, ok := gd.Load[gd.Texture2D](tmp, "res://terrain/alpine_grass.png")
	if !ok {
		return
	}
	tr.shader = *gd.Create(tr.KeepAlive, new(gd.ShaderMaterial))
	tr.shader.SetShader(shader)
	tr.shader.SetShaderParameter(tmp.StringName("albedo"), tmp.Variant(gd.Color{1, 1, 1, 1}))
	tr.shader.SetShaderParameter(tmp.StringName("uv1_scale"), tmp.Variant(gd.Vector2{8, 8}))
	tr.shader.SetShaderParameter(tmp.StringName("texture_albedo"), tmp.Variant(grass))
	tr.shader.SetShaderParameter(tmp.StringName("radius"), tmp.Variant(2.0))
	tr.shader.SetShaderParameter(tmp.StringName("height"), tmp.Variant(0.0))
	tr.BrushRadius = 2.0
}

func (vr *Renderer) AsNode() gd.Node { return vr.Super().AsNode() }

func (vr *Renderer) start() {
	tmp := gd.NewLifetime(vr.Temporary)
	vr.reloads = make(map[vulture.Region]bool)
	vr.regions = make(map[vulture.Region]vulture.Elements)
	go vr.listenForEvents(tmp)
}

func (vr *Renderer) listenForEvents(tmp gd.Lifetime) {
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
	vr.Vulture.load()
}

func (vr *Renderer) Process(dt gd.Float) {
	tmp := vr.Temporary
	Input := gd.Input(tmp)
	if !vr.listening.Load() {
		return
	}
	for {
		select {
		case deltas := <-vr.events:
			vr.apply(deltas)
		case event := <-vr.brushEvents:
			if !Input.IsKeyPressed(gd.KeyShift) {
				vr.mouseOver <- event.BrushTarget
			} else {
				event.BrushTarget = event.BrushTarget.Round()
				vr.BrushTarget = event.BrushTarget
				vr.BrushDeltaV = event.BrushDeltaV
				if event.BrushDeltaV != 0 {
					vr.BrushActive = true
				}
				vr.shader.SetShaderParameter(tmp.StringName("uplift"), tmp.Variant(event.BrushTarget))
			}
			continue
		default:
			break
		}
		break
	}
	if vr.BrushActive {
		vr.BrushAmount += dt * vr.BrushDeltaV
		vr.shader.SetShaderParameter(tmp.StringName("height"), tmp.Variant(vr.BrushAmount))
	}
}

func (vr *Renderer) apply(deltas []vulture.Deltas) {
	tmp := vr.Temporary
	for _, delta := range deltas {
		buf, ok := vr.regions[delta.Region]
		if !ok {
			vr.reloads[delta.Region] = true
		}
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
			switch element.Type() {
			case vulture.ElementIsMarker:
				vr.assertMarker(delta.Region, node, buf, offset, element.Marker())
			case vulture.ElementIsPoints:
				vr.reloads[delta.Region] = true
			}
		}
	}
	for region := range vr.reloads {
		vr.reload(region)
	}
}

func (vr *Renderer) assertMarker(regionID vulture.Region, region gd.Node, buf vulture.Elements, offset vulture.Offset, element *vulture.ElementMarker) {
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
	world.SetY(vr.HeightAt(world))
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

func (vr *Renderer) reload(region vulture.Region) {
	vr.reloads[region] = false
	tmp := vr.Temporary
	name := fmt.Sprint(region)
	existing := vr.ActiveRegions.AsNode().GetNodeOrNull(tmp, tmp.String(name).NodePath(tmp))
	if existing == (gd.Node{}) {
		area := gd.Create(vr.KeepAlive, new(TerrainTile))
		area.buffer = vr.regions[region]
		area.region = region
		area.brushEvents = vr.brushEvents
		area.Shader = vr.shader
		area.Super().AsNode().SetName(tmp.String(name))
		vr.ActiveRegions.AsNode().AddChild(area.Super().AsNode(), false, 0)
	}
	tile, ok := gd.As[*TerrainTile](tmp, existing)
	if ok {
		tile.buffer = vr.regions[region]
		tile.Reload()
	}
}

// SetFocalPoint3D sets the focal point of the terrain renderer, this should be
// where the camera is focused on. The [TerrainRenderer] will then fetch all
// nearby [vulture.Territory] enabling it to be rendered. The point should
// be in world space.
func (tr *Renderer) SetFocalPoint3D(world gd.Vector3) {
	focal_point, _, _ := tr.Vulture.worldToVulture(world)

	/*if _, ok := tr.loadedTerritory[vulture.Area{}]; ok {
		return
	}
	go tr.downloadArea(vulture.Area{})
	return*/

	// we need to load all 9 neighboring areas
	for x := int8(-1); x <= 1; x++ {
		for y := int8(-1); y <= 1; y++ {
			area := vulture.Region{focal_point[0] + x, focal_point[1] + y}
			if _, ok := tr.reloads[area]; ok {
				continue
			}
			tr.reloads[area] = true
			tr.reload(area)
		}
	}
}
