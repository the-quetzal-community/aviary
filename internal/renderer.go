package internal

import (
	"context"
	"fmt"
	"sync/atomic"

	"graphics.gd/classdb"
	"graphics.gd/classdb/Engine"
	"graphics.gd/classdb/Input"
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/PackedScene"
	"graphics.gd/classdb/Resource"
	"graphics.gd/classdb/Shader"
	"graphics.gd/classdb/ShaderMaterial"
	"graphics.gd/classdb/Texture2D"
	"graphics.gd/classdb/Texture2DArray"
	"graphics.gd/variant"
	"graphics.gd/variant/Color"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Path"
	"graphics.gd/variant/Vector2"
	"graphics.gd/variant/Vector3"
	"the.quetzal.community/aviary/protocol/vulture"
)

// Renderer will open a Vulture Events stream and render all
// neighboring regions around the focal point.
type Renderer struct {
	classdb.Extension[Renderer, Node3D.Instance] `gd:"VultureRenderer"`

	ActiveContent Node.Instance
	CachedContent Node.Instance

	ActiveRegions Node.Instance
	CachedRegions Node.Instance

	heightMapping map[vulture.Region][16 * 16][4]vulture.Height

	Vulture *Vulture

	listening atomic.Bool
	events    <-chan []vulture.Deltas

	regions map[vulture.Region]vulture.Elements
	reloads map[vulture.Region]bool

	mouseOver chan Vector3.XYZ

	shader ShaderMaterial.Instance

	texture chan Path.ToResource

	//
	// Terrain Brush parameters are used to represent modifications
	// to the terrain. Either for texturing or height map adjustments.
	//
	BrushActive bool
	BrushTarget Vector3.XYZ
	BrushRadius Float.X
	BrushAmount Float.X
	BrushDeltaV Float.X
	brushEvents chan terrainBrushEvent

	PaintActive bool
}

func (tr *Renderer) Ready() {
	shader := Resource.Load[Shader.Instance]("res://shader/terrain.gdshader")
	grass := Resource.Load[Texture2D.Instance]("res://terrain/alpine_grass.png")
	cliff := Resource.Load[Texture2D.Instance]("res://library/wildfire_games/texture/alpine_cliff.png")
	textures := Texture2DArray.New()
	textures.AsImageTextureLayered().CreateFromImages([]classdb.Image{
		grass.AsTexture2D().GetImage(),
		cliff.AsTexture2D().GetImage(),
	})
	tr.shader = ShaderMaterial.New()
	tr.shader.SetShader(shader)
	tr.shader.SetShaderParameter("albedo", Color.RGBA{1, 1, 1, 1})
	tr.shader.SetShaderParameter("uv1_scale", Vector2.New(8, 8))
	tr.shader.SetShaderParameter("texture_albedo", textures)
	tr.shader.SetShaderParameter("radius", 2.0)
	tr.shader.SetShaderParameter("height", 0.0)
	tr.BrushRadius = 2.0
}

func (vr *Renderer) AsNode() Node.Instance { return vr.Super().AsNode() }

func (vr *Renderer) start() {
	vr.reloads = make(map[vulture.Region]bool)
	vr.regions = make(map[vulture.Region]vulture.Elements)
	go vr.listenForEvents()
}

func (vr *Renderer) listenForEvents() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	deltas, err := vr.Vulture.api.Events(ctx)
	if err != nil {
		Engine.Raise(err)
		return
	}
	vr.events = deltas
	vr.listening.Store(true)
	vr.Vulture.load()
}

func (vr *Renderer) Process(dt Float.X) {
	variant.Use(vr.shader[0])

	if !vr.listening.Load() {
		return
	}
	for {
		select {
		case deltas := <-vr.events:
			vr.apply(deltas)
		case res := <-vr.texture:
			texture := Resource.Load[Texture2D.Instance](res)
			vr.shader.SetShaderParameter("paint_texture", texture)
			vr.shader.SetShaderParameter("paint_active", true)
			vr.PaintActive = true
		case event := <-vr.brushEvents:
			if vr.PaintActive && Input.IsMouseButtonPressed(Input.MouseButtonLeft) {
				vr.BrushTarget = Vector3.Round(event.BrushTarget)
				vr.shader.SetShaderParameter("uplift", Vector3.Sub(event.BrushTarget, Vector3.New(0.5, 0.5, 0.5)))
				vr.uploadEdits(vulture.Uplift{
					Draw: 1, // TODO upload ID
				})
			} else if !Input.IsKeyPressed(Input.KeyShift) {
				vr.mouseOver <- event.BrushTarget
				vr.BrushTarget = Vector3.Round(event.BrushTarget)
				vr.shader.SetShaderParameter("uplift", Vector3.Sub(event.BrushTarget, Vector3.New(0.5, 0.5, 0.5)))
			} else {
				event.BrushTarget = Vector3.Round(event.BrushTarget)
				vr.BrushTarget = event.BrushTarget
				vr.BrushDeltaV = event.BrushDeltaV
				if event.BrushDeltaV != 0 {
					vr.BrushActive = true
				}
				vr.shader.SetShaderParameter("uplift", Vector3.Sub(event.BrushTarget, Vector3.New(0.5, 0.5, 0.5)))
			}
			continue
		default:
			break
		}
		break
	}
	if vr.BrushActive {
		vr.BrushAmount += dt * vr.BrushDeltaV
		vr.shader.SetShaderParameter("height", vr.BrushAmount)
	}
}

func (vr *Renderer) apply(deltas []vulture.Deltas) {
	for _, delta := range deltas {
		buf, ok := vr.regions[delta.Region]
		if !ok {
			vr.reloads[delta.Region] = true
		}
		end := buf.Len()
		buf.Apply(delta)
		vr.regions[delta.Region] = buf
		name := fmt.Sprint(delta.Region)
		node := vr.ActiveContent.AsNode().GetNodeOrNull(name)
		if node == (Node.Instance{}) {
			area := Node.New()
			area.SetName(name)
			vr.ActiveContent.AsNode().AddChild(area)
			node = vr.ActiveContent.AsNode().GetNodeOrNull(name)
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

func (vr *Renderer) assertMarker(regionID vulture.Region, region Node.Instance, buf vulture.Elements, offset vulture.Offset, element *vulture.ElementMarker) {
	name := fmt.Sprint(offset)
	node := Node.Instance(region.AsNode().GetNodeOrNull(name))
	if node == (Node.Instance{}) {
		area := Node3D.New()
		area.AsNode().SetName(name)
		region.AsNode().AddChild(area.AsNode())
		node = region.AsNode().GetNodeOrNull(name)
	}
	parent, ok := classdb.As[Node3D.Instance](node)
	if !ok {
		return
	}
	world := vr.Vulture.vultureToWorld(regionID, element.Cell, element.Bump)
	world.Y = (vr.HeightAt(world))
	parent.SetPosition(world)
	parent.SetScale(Vector3.XYZ{0.3, 0.3, 0.3})

	resource, ok := vr.Vulture.upload2name[element.Mesh]
	if !ok {
		resource = "res://library/wildfire_games/foliage/acacia.glb"
	}

	scene := Resource.Load[PackedScene.Instance](resource)
	instance, ok := classdb.As[Node3D.Instance](Node.Instance(scene.Instantiate()))
	if ok {
		if parent.AsNode().GetChildCount() > 0 {
			Node.Instance(parent.AsNode().GetChild(0)).QueueFree()
		}
		parent.AsNode().AddChild(instance.AsNode())
	}
}

func (vr *Renderer) reload(region vulture.Region) {
	vr.reloads[region] = false
	name := fmt.Sprint(region)
	existing := Node.Instance(vr.ActiveRegions.AsNode().GetNodeOrNull(name))
	if existing == (Node.Instance{}) {
		area := new(TerrainTile)
		area.buffer = vr.regions[region]
		area.region = region
		area.heightMapping = vr.heightMapping
		area.brushEvents = vr.brushEvents
		area.Shader = vr.shader
		area.Super().AsNode().SetName(name)
		vr.ActiveRegions.AsNode().AddChild(area.Super().AsNode())
		existing = Node.Instance(vr.ActiveRegions.AsNode().GetNodeOrNull(name))
	}
	tile, ok := classdb.As[*TerrainTile](existing)
	if ok {
		tile.buffer = vr.regions[region]
		tile.Reload()
	}
}

// SetFocalPoint3D sets the focal point of the terrain renderer, this should be
// where the camera is focused on. The [TerrainRenderer] will then fetch all
// nearby [vulture.Territory] enabling it to be rendered. The point should
// be in world space.
func (tr *Renderer) SetFocalPoint3D(world Vector3.XYZ) {
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
