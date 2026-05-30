package internal

import (
	"path"
	"strings"

	"graphics.gd/classdb/ArrayMesh"
	"graphics.gd/classdb/BoxShape3D"
	"graphics.gd/classdb/Camera3D"
	"graphics.gd/classdb/CollisionShape3D"
	"graphics.gd/classdb/CylinderMesh"
	"graphics.gd/classdb/FileAccess"
	"graphics.gd/classdb/HeightMapShape3D"
	"graphics.gd/classdb/Image"
	"graphics.gd/classdb/Input"
	"graphics.gd/classdb/InputEvent"
	"graphics.gd/classdb/InputEventMouseButton"
	"graphics.gd/classdb/Mesh"
	"graphics.gd/classdb/MeshInstance3D"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/Shader"
	"graphics.gd/classdb/ShaderMaterial"
	"graphics.gd/classdb/StandardMaterial3D"
	"graphics.gd/classdb/StaticBody3D"
	"graphics.gd/classdb/Texture2D"
	"graphics.gd/classdb/Texture2DArray"
	"graphics.gd/variant/Angle"
	"graphics.gd/variant/Callable"
	"graphics.gd/variant/Color"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Path"
	"graphics.gd/variant/String"
	"graphics.gd/variant/Vector2"
	"graphics.gd/variant/Vector3"
	"the.quetzal.community/aviary/internal/musical"
)

// tileCoord identifies one chunk in the infinite grid of terrain
// tiles. The tile at coord (cx, cz) is centred at world position
// (cx * terrainDefaultSize, 0, cz * terrainDefaultSize) and covers
// the world AABB [coord*size - size/2, coord*size + size/2] in both
// X and Z.
type tileCoord struct {
	X, Z int
}

// TerrainEditor is responsible for rendering and managing the terrain in the 3D environment.
type TerrainEditor struct {
	Node3D.Extension[TerrainEditor] `gd:"TerrainEditor"`
	musical.Stubbed

	// tiles is the live set of terrain chunks indexed by their grid
	// coord. Tiles are auto-created the first time a sculpt's brush
	// AABB touches them (see tilesIntersecting). The "starter" tile
	// at (0, 0) is allocated in Ready so the world has something to
	// click on before the first sculpt.
	tiles map[tileCoord]*TerrainTile

	// arrowsVisible toggles every existing extend-the-world arrow
	// when entering/leaving the terrain editor — arrows shouldn't be
	// clickable from the coaster or scenery editors.
	arrowsVisible bool

	// mapper, albedos, normal_maps are shared across all tiles so the
	// shader's Texture2DArray layer index for a given paint Design is
	// consistent everywhere it gets painted. Mutated in Sculpt's
	// upload step; tiles read mapper[Design] when sampling textures.
	mapper      map[musical.Design]int
	albedos     []Image.Instance
	normal_maps []Image.Instance

	shader        ShaderMaterial.Instance
	shader_buried ShaderMaterial.Instance

	texture chan Path.ToResource

	//
	// Terrain Brush parameters are used to represent modifications
	// to the terrain. Either for texturing or height map adjustments.
	//
	BrushDesign string
	BrushActive bool
	BrushTarget Vector3.XYZ
	BrushRadius Float.X
	BrushAmount Float.X
	BrushDeltaV Float.X
	brushEvents chan terrainBrushEvent

	PaintActive bool

	client *Client
}

// cardinalDirs are the four neighbour offsets used by the chunk
// machinery — both when spawning "extend the world" arrows and when
// pruning the matching arrow on an existing neighbour as a new tile
// fills its side.
var cardinalDirs = [4]tileCoord{{1, 0}, {-1, 0}, {0, 1}, {0, -1}}

// tileAt returns the tile at the given grid coord, creating it on
// demand. New tiles are positioned in world space at coord*size and
// share the editor's shader instances so the brush highlight + paint
// textures stay consistent across chunks. After creating a tile we
// also clear the "extend" arrow on each existing neighbour that
// pointed toward this coord, and spawn arrows on the new tile's open
// sides.
func (tr *TerrainEditor) tileAt(coord tileCoord) *TerrainTile {
	if tile, ok := tr.tiles[coord]; ok {
		return tile
	}
	tile := new(TerrainTile)
	tile.coord = coord
	tile.client = tr.client
	tile.editor = tr
	tile.shader = tr.shader
	tile.side_shader = tr.shader_buried
	tile.brushEvents = tr.brushEvents
	tile.arrows = make(map[tileCoord]*TerrainTileArrow)
	tr.tiles[coord] = tile
	tr.AsNode().AddChild(tile.AsNode())
	tile.AsNode3D().SetPosition(Vector3.New(
		Float.X(coord.X*terrainDefaultSize),
		0,
		Float.X(coord.Z*terrainDefaultSize),
	))
	// Remove arrows on existing neighbours that pointed at us.
	// Also drop any now-internal side wall on the neighbour.
	for _, dir := range cardinalDirs {
		neighbour, ok := tr.tiles[tileCoord{coord.X + dir.X, coord.Z + dir.Z}]
		if !ok || neighbour == tile {
			continue
		}
		neighbour.removeArrow(tileCoord{-dir.X, -dir.Z})
		neighbour.reloadSides()
	}
	// Spawn arrows for our own open sides.
	tile.spawnArrows()
	return tile
}

// tilesIntersecting returns every tile whose AABB intersects the
// given world-space brush sphere, creating tiles on demand. Sculpts
// straddling a tile boundary apply to all overlapping chunks so the
// brush effect is continuous across the seam.
//
// Tile (cx, _) covers world X in [cx*size - half, cx*size + half].
// Solving for overlap with [target.X - radius, target.X + radius]
// gives cx ∈ [ceil((minX - half) / size), floor((maxX + half) / size)].
func (tr *TerrainEditor) tilesIntersecting(target Vector3.XYZ, radius Float.X) []*TerrainTile {
	size := Float.X(terrainDefaultSize)
	half := size / 2
	minX, maxX := target.X-radius, target.X+radius
	minZ, maxZ := target.Z-radius, target.Z+radius
	minCx := int(Float.Ceil((minX - half) / size))
	maxCx := int(Float.Floor((maxX + half) / size))
	minCz := int(Float.Ceil((minZ - half) / size))
	maxCz := int(Float.Floor((maxZ + half) / size))
	var tiles []*TerrainTile
	for cx := minCx; cx <= maxCx; cx++ {
		for cz := minCz; cz <= maxCz; cz++ {
			tiles = append(tiles, tr.tileAt(tileCoord{cx, cz}))
		}
	}
	return tiles
}

// tileForWorld returns the tile whose AABB contains the given world
// position, or nil if no tile has yet been instantiated there. Used
// by HeightAt/NormalAt so scenery, action_renderer and the like land
// on the right chunk.
func (tr *TerrainEditor) tileForWorld(pos Vector3.XYZ) *TerrainTile {
	size := Float.X(terrainDefaultSize)
	half := size / 2
	cx := int(Float.Floor((pos.X + half) / size))
	cz := int(Float.Floor((pos.Z + half) / size))
	return tr.tiles[tileCoord{cx, cz}]
}

func (fe *TerrainEditor) Name() string { return "terrain" }

func (fe *TerrainEditor) EnableEditor() {
	fe.shader.SetShaderParameter("brush_active", true)
	fe.shader_buried.SetShaderParameter("brush_active", true)
	fe.setArrowsVisible(true)
}

func (fe *TerrainEditor) ChangeEditor() {
	fe.shader.
		SetShaderParameter("height", 0.0).
		SetShaderParameter("brush_active", false).
		SetShaderParameter("paint_active", false)
	fe.shader_buried.
		SetShaderParameter("height", 0.0).
		SetShaderParameter("brush_active", false)
	fe.BrushActive = false
	fe.PaintActive = false
	fe.setArrowsVisible(false)
}

// setArrowsVisible toggles every existing chunk's extend arrows and
// remembers the state so tiles spawned later get the matching default.
func (tr *TerrainEditor) setArrowsVisible(v bool) {
	tr.arrowsVisible = v
	for _, tile := range tr.tiles {
		for _, arrow := range tile.arrows {
			arrow.AsNode3D().SetVisible(v)
		}
	}
}

func (*TerrainEditor) Views() []string          { return nil }
func (*TerrainEditor) SwitchToView(view string) {}

func (fe *TerrainEditor) Tabs(mode Mode) []string {
	switch mode {
	case ModeGeometry:
		// The brush-size ("radius") slider used to be rendered here as an
		// "editing/radius" tab in the design explorer; it now lives in the
		// gizmo toolbar (CloudControl.sizeSlider) instead.
		return nil
	case ModeMaterial:
		return []string{
			"terrain/aquatic",
			"terrain/deserts",
			"terrain/dryland",
			"terrain/forests",
			"terrain/glacial",
			"terrain/manmade",
			"terrain/organic",
			"terrain/volcano",
		}
	case ModeDressing:
		return []string{
			"grasses",
			"pebbles",
		}
	default:
		return nil
	}
}

func (fe *TerrainEditor) SelectDesign(mode Mode, design string) {
	select {
	case fe.texture <- Path.ToResource(String.New(design)):
		fe.EnableEditor()
	default:
	}
}
func (fe *TerrainEditor) SliderHandle(mode Mode, editing string, value float64, commit bool) {
	switch editing {
	case "editing/radius":
		fe.BrushRadius = Float.X(value)
		fe.shader.SetShaderParameter("radius", fe.BrushRadius)
	}
}

func (fe *TerrainEditor) SliderConfig(mode Mode, editing string) (init, min, max, step float64) {
	return float64(fe.BrushRadius), 0, 10, 0.01
}

// brushRadiusScrollStep is how much one mouse-wheel notch changes the
// terrain brush radius when Shift is held (see Client.handleScroll).
const brushRadiusScrollStep Float.X = 0.5

// NudgeBrushRadius changes the brush radius by delta, clamped to the
// slider's configured range, and pushes it to the shader. It returns the
// new radius so callers can sync the gizmo-toolbar size slider. Used by
// the Shift+wheel shortcut.
func (fe *TerrainEditor) NudgeBrushRadius(delta Float.X) Float.X {
	_, min, max, _ := fe.SliderConfig(ModeGeometry, "editing/radius")
	r := fe.BrushRadius + delta
	if r < Float.X(min) {
		r = Float.X(min)
	}
	if r > Float.X(max) {
		r = Float.X(max)
	}
	fe.SliderHandle(ModeGeometry, "editing/radius", float64(r), false)
	return fe.BrushRadius
}

func (tr *TerrainEditor) Ready() {
	shader := LoadSync[Shader.Instance]("res://shader/terrain.gdshader")
	grass := LoadSync[Texture2D.Instance]("res://terrain/alpine_grass.png")
	textures := Texture2DArray.New()
	textures.AsImageTextureLayered().CreateFromImages([]Image.Instance{
		grass.AsTexture2D().GetImage(),
	})
	tr.shader = ShaderMaterial.New().
		SetShader(shader).
		SetShaderParameter("albedo", Color.RGBA{1, 1, 1, 1}).
		SetShaderParameter("uv1_scale", Vector2.New(8, 8)).
		SetShaderParameter("texture_albedo", textures).
		SetShaderParameter("radius", 2.0).
		SetShaderParameter("height", 0.0)

	rock := LoadSync[Texture2D.Instance]("res://default/mineral.jpg")
	buried := LoadSync[Shader.Instance]("res://shader/buried.gdshader")
	tr.shader_buried = ShaderMaterial.New().
		SetShader(buried).
		SetShaderParameter("texture_albedo", rock).
		SetShaderParameter("radius", 2.0).
		SetShaderParameter("height", 0.0)

	tr.BrushRadius = 2.0

	tr.tiles = make(map[tileCoord]*TerrainTile)
	tr.mapper = make(map[musical.Design]int)
	tr.albedos = []Image.Instance{LoadSync[Texture2D.Instance]("res://terrain/alpine_grass.png").AsTexture2D().GetImage()}
	tr.normal_maps = []Image.Instance{LoadSync[Texture2D.Instance]("res://terrain/normal.png").AsTexture2D().GetImage()}
	tr.uploadTextureArrays()
	// Spawn the starter tile so the world is clickable before any
	// sculpt arrives.
	tr.tileAt(tileCoord{0, 0})
}

// uploadTextureArrays rebuilds the Texture2DArray (and bumpmap
// counterpart) from editor-level albedos/normal_maps and pushes them
// to the shared shader. Called both at startup and when a new paint
// Design first appears via uploadDesign.
func (tr *TerrainEditor) uploadTextureArrays() {
	terrains := Texture2DArray.New()
	terrains.AsImageTextureLayered().CreateFromImages(tr.albedos)
	bumpmaps := Texture2DArray.New()
	bumpmaps.AsImageTextureLayered().CreateFromImages(tr.normal_maps)
	tr.shader.
		SetShaderParameter("texture_albedo", terrains).
		SetShaderParameter("texture_normal", bumpmaps)
}

// uploadDesign assigns the given paint Design a layer index in the
// shared texture array, loading the texture (and its `_normal` sibling
// if one exists) the first time it appears. Returns the layer index;
// 0 is reserved for the default base layer.
func (tr *TerrainEditor) uploadDesign(design musical.Design) int {
	if idx, ok := tr.mapper[design]; ok {
		return idx
	}
	texture, ok := tr.client.textures[design].Instance()
	if !ok {
		return 0
	}
	idx := len(tr.albedos)
	tr.mapper[design] = idx
	tr.albedos = append(tr.albedos, texture.GetImage())
	ext := path.Ext(texture.AsResource().ResourcePath())
	normal_path := strings.TrimSuffix(texture.AsResource().ResourcePath(), ext) + "_normal" + ext
	if FileAccess.FileExists(normal_path) {
		tr.normal_maps = append(tr.normal_maps, LoadSync[Texture2D.Instance](normal_path).AsTexture2D().GetImage())
	} else {
		tr.normal_maps = append(tr.normal_maps, LoadSync[Texture2D.Instance]("res://terrain/normal.png").AsTexture2D().GetImage())
	}
	tr.uploadTextureArrays()
	return idx
}

func (tr *TerrainEditor) Paint() {
	tr.client.space.Sculpt(musical.Sculpt{
		Author: tr.client.id,
		Target: tr.BrushTarget,
		Radius: tr.BrushRadius,
		Amount: tr.BrushAmount,
		Design: tr.client.MusicalDesign(tr.BrushDesign),
		Commit: true,
	})
}

// CancelPaint clears the active paint state — used by callers
// outside the editor (e.g. right-click in the world view) so they
// don't have to know to flip both the shader uniform and the
// PaintActive flag.
func (tr *TerrainEditor) CancelPaint() bool {
	if !tr.PaintActive {
		return false
	}
	tr.shader.SetShaderParameter("paint_active", false)
	tr.PaintActive = false
	return true
}

// HeightAt looks up the terrain height at the given world position
// by finding the chunk containing it. Returns 0 if no tile has been
// instantiated at that location yet.
func (tr *TerrainEditor) HeightAt(pos Vector3.XYZ) Float.X {
	tile := tr.tileForWorld(pos)
	if tile == nil {
		return 0
	}
	return tile.HeightAt(pos)
}

// NormalAt looks up the terrain normal at the given world position.
// Returns world-up if no tile has been instantiated there.
func (tr *TerrainEditor) NormalAt(pos Vector3.XYZ) Vector3.XYZ {
	tile := tr.tileForWorld(pos)
	if tile == nil {
		return Vector3.XYZ{Y: 1}
	}
	return tile.NormalAt(pos)
}

func (vr *TerrainEditor) Process(dt Float.X) {
	for {
		select {
		case res := <-vr.texture:
			texture := LoadSync[Texture2D.Instance](res)
			vr.BrushDesign = res.String()
			vr.shader.
				SetShaderParameter("paint_texture", texture).
				SetShaderParameter("paint_active", true)
			vr.PaintActive = true
		case event := <-vr.brushEvents:
			vr.BrushTarget = event.BrushTarget
			vr.shader.SetShaderParameter("uplift", event.BrushTarget)
			vr.shader_buried.SetShaderParameter("uplift", event.BrushTarget)
			if vr.client.Editing != Editing.Terrain {
				vr.BrushActive = false
				break
			}
			if vr.PaintActive && Input.IsMouseButtonPressed(Input.MouseButtonLeft) {
				vr.BrushTarget = Vector3.Round(event.BrushTarget)
			} else if !vr.PaintActive && vr.client.ui.mode != ModeMaterial {
				vr.BrushTarget = event.BrushTarget
				vr.BrushDeltaV = event.BrushDeltaV
				if event.BrushDeltaV != 0 {
					vr.BrushActive = true
				}
			} else {
				vr.BrushDeltaV = 0
			}
			continue
		default:
		}
		break
	}
	if vr.BrushActive && !vr.PaintActive && vr.client.ui.mode == ModeGeometry {
		vr.BrushAmount += dt * vr.BrushDeltaV
		vr.shader.SetShaderParameter("height", vr.BrushAmount)
		vr.shader_buried.SetShaderParameter("height", vr.BrushAmount)
	} else {
		vr.BrushAmount = 0.0
		vr.shader.SetShaderParameter("height", vr.BrushAmount)
		vr.shader_buried.SetShaderParameter("height", vr.BrushAmount)
	}
}

func (vr *TerrainEditor) Sculpt(brush musical.Sculpt) error {
	if brush.Editor != "" {
		return nil
	}
	if brush.Author == vr.client.id {
		vr.shader.SetShaderParameter("height", 0.0)
		vr.shader_buried.SetShaderParameter("height", 0.0)
	}
	for _, tile := range vr.tilesIntersecting(brush.Target, brush.Radius) {
		tile.Sculpt(brush)
	}
	return nil
}

type terrainBrushEvent struct {
	BrushTarget Vector3.XYZ
	BrushDeltaV Float.X
}

func (tr *TerrainEditor) OnCreate() {
	tr.brushEvents = make(chan terrainBrushEvent, 100)
}

func (tr *TerrainEditor) UnhandledInput(event InputEvent.Instance) {
	if tr.client.Editing != Editing.Terrain {
		return
	}
	if event, ok := Object.As[InputEventMouseButton.Instance](event); ok {
		if !tr.PaintActive && tr.BrushActive && (event.ButtonIndex() == Input.MouseButtonLeft || event.ButtonIndex() == Input.MouseButtonRight) && event.AsInputEvent().IsReleased() {
			if tr.BrushAmount != 0 {
				tr.client.space.Sculpt(musical.Sculpt{
					Author: tr.client.id,
					Target: tr.BrushTarget,
					Radius: tr.BrushRadius,
					Amount: tr.BrushAmount,
					Commit: true,
				})
			}
			tr.BrushAmount = 0.0
			tr.BrushDeltaV = 0.0
			tr.BrushActive = false
		}
		if event.ButtonIndex() == Input.MouseButtonRight && tr.PaintActive {
			tr.ChangeEditor()
		}
	}
}

// terrainDefaultSize is the cell count along each side of every
// terrain chunk. Chunks tile the world on a (size × size) grid and
// are spawned lazily as sculpts land in them.
const terrainDefaultSize = 16

type TerrainTile struct {
	StaticBody3D.Extension[TerrainTile] `gd:"AviaryTerrainTile"`

	brushEvents chan<- terrainBrushEvent

	Mesh        MeshInstance3D.Instance
	shader      ShaderMaterial.Instance
	side_shader ShaderMaterial.Instance

	client    *Client
	editor    *TerrainEditor // back-pointer for shared mapper/albedos
	coord     tileCoord      // grid position; world center = coord * size
	generated bool
	reloading bool
	sculpts   []musical.Sculpt

	// arrows tracks the "extend the world" markers for the four
	// cardinal sides that don't yet have a neighbour. Keyed by the
	// unit direction (e.g. (1,0) for the +X side).
	arrows map[tileCoord]*TerrainTileArrow

	// size is the cell count along each side. The tile is size×size
	// cells, with heights stored on a (size+1)×(size+1) grid.
	size int

	heights []float32

	// collision is the per-tile CollisionShape3D wrapping the
	// HeightMapShape3D used for picking + physics; cached at
	// generateBase so per-frame Reload doesn't have to walk
	// children to find it again.
	collision      CollisionShape3D.Instance
	heightmapShape HeightMapShape3D.Instance

	// cached geometry — top surface
	vertices []Vector3.XYZ
	normals  []Vector3.XYZ
	uvs      []Vector2.XY
	textures []float32
	weights  []float32

	// cached geometry — side walls (only exposed cardinal sides × size
	// segments × 6 verts per quad). Internal sides (where a neighbour
	// tile exists) are omitted.
	verticesSide []Vector3.XYZ
	normalsSide  []Vector3.XYZ
	uvsSide      []Vector2.XY
}

func (tile *TerrainTile) Ready() {
	if tile.size == 0 {
		tile.size = terrainDefaultSize
	}
	tile.Reload()
}

func (tile *TerrainTile) Sculpt(brush musical.Sculpt) {
	tile.sculpts = append(tile.sculpts, brush)
	tile.Reload()
}

// generateBase mesh, textures and the collision shape, these will change whenever a [musical.Sculpt] arrives.
func (tile *TerrainTile) generateBase() {
	tile.generated = true
	if tile.size == 0 {
		tile.size = terrainDefaultSize
	}
	n := tile.size // cells along each side
	hm := n + 1    // heightmap dim (per-vertex grid)
	half := Float.X(n) / 2
	//
	// The mesh is an n×n plane grid; each cell has a texture associated with each neighbouring vertex
	// and is blended together using the weights set up here (weights identify where in the cell each
	// vertex sits and therefore which textures contribute).
	//
	// Allocate or keep the heights buffer first so the vertex Y can
	// be seeded from any pre-existing terrain (Resize hands us a
	// resampled heights buffer).
	if tile.heights == nil || len(tile.heights) != hm*hm {
		tile.heights = make([]float32, hm*hm)
	}
	var mesh = ArrayMesh.New()
	tile.vertices = make([]Vector3.XYZ, n*n*6)
	tile.normals = make([]Vector3.XYZ, n*n*6)
	tile.uvs = make([]Vector2.XY, n*n*6)
	tile.textures = make([]float32, n*n*6*4)
	tile.weights = make([]float32, n*n*6*4)
	inv := Float.X(1) / Float.X(n)
	add := func(index int, x, y int, w1, w2, w3, w4 Float.X) {
		tile.vertices[index] = Vector3.XYZ{Float.X(x), Float.X(tile.heights[x+y*hm]), Float.X(y)}
		tile.normals[index] = Vector3.XYZ{0, 1, 0}
		tile.uvs[index] = Vector2.XY{Float.X(x) * inv, Float.X(y) * inv}
		tile.weights[index*4] = float32(w1)
		tile.weights[index*4+1] = float32(w2)
		tile.weights[index*4+2] = float32(w3)
		tile.weights[index*4+3] = float32(w4)
	}
	for x := 0; x < n; x++ {
		for y := 0; y < n; y++ {
			add(6*(x+n*y)+0, x, y, 1, 0, 0, 0)     // top left
			add(6*(x+n*y)+1, x+1, y, 0, 1, 0, 0)   // top right
			add(6*(x+n*y)+2, x, y+1, 0, 0, 1, 0)   // bottom left
			add(6*(x+n*y)+3, x+1, y, 0, 1, 0, 0)   // top right
			add(6*(x+n*y)+4, x+1, y+1, 0, 0, 0, 1) // bottom right
			add(6*(x+n*y)+5, x, y+1, 0, 0, 1, 0)   // bottom left
		}
	}
	attributes := [Mesh.ArrayMax]any{
		Mesh.ArrayVertex:  tile.vertices,
		Mesh.ArrayTexUv:   tile.uvs,
		Mesh.ArrayNormal:  tile.normals,
		Mesh.ArrayCustom0: tile.textures,
		Mesh.ArrayCustom1: tile.weights,
	}
	mesh.MoreArgs().AddSurfaceFromArrays(Mesh.PrimitiveTriangles, attributes[:], nil, nil,
		Mesh.ArrayFormatVertex|
			Mesh.ArrayFormat(Mesh.ArrayCustomRgbaFloat)<<Mesh.ArrayFormatCustom0Shift|
			Mesh.ArrayFormat(Mesh.ArrayCustomRgbaFloat)<<Mesh.ArrayFormatCustom1Shift,
	)
	tile.Mesh.
		SetMesh(mesh.AsMesh()).
		AsNode3D().SetPosition(Vector3.New(-half, 0, -half))
	tile.Mesh.SetSurfaceOverrideMaterial(0, tile.shader.AsMaterial())
	//
	// Set up the collision shape, which is what mouse picking and physics queries hit.
	//
	collision_shape := CollisionShape3D.Nil
	for i := 0; i < tile.AsNode().GetChildCount(); i++ {
		child := tile.AsNode().GetChild(i)
		if shape, ok := Object.As[CollisionShape3D.Instance](child); ok {
			collision_shape = shape
			break
		}
	}
	if collision_shape == CollisionShape3D.Nil {
		collision_shape = CollisionShape3D.New()
		tile.AsNode().AddChild(collision_shape.AsNode())
	}
	shape := HeightMapShape3D.New().
		SetMapDepth(hm).
		SetMapWidth(hm).
		SetMapData(tile.heights)
	collision_shape.SetShape(shape.AsShape3D())
	tile.collision = collision_shape
	tile.heightmapShape = shape
	// Texture arrays are owned by the editor (shared across tiles).
	tile.reloadSides()
}

// Reload applies any pending sculpt operations to the terrain tile.
func (tile *TerrainTile) Reload() {
	if !tile.generated {
		tile.generateBase()
	}
	if tile.reloading {
		return // we only want to reload once per frame.
	}
	tile.reloading = true
	Callable.Defer(Callable.New(func() {
		tile.reloading = false
		// Ensure every paint Design in the pending sculpts has a
		// layer index in the editor's shared texture array.
		if tile.editor != nil {
			for _, sculpt := range tile.sculpts {
				if sculpt.Design == (musical.Design{}) {
					continue
				}
				tile.editor.uploadDesign(sculpt.Design)
			}
		}
		n := tile.size
		hm := n + 1
		half := Float.X(n) / 2
		offset := Vector3.XYZ{
			Float.X(tile.coord.X*n) - half,
			0,
			Float.X(tile.coord.Z*n) - half,
		}
		var sample_texture = func(x, y int) int {
			pos := Vector3.Add(Vector3.XYZ{Float.X(x), 0, Float.X(y)}, offset)
			for i := len(tile.sculpts) - 1; i >= 0; i-- {
				sculpt := tile.sculpts[i]
				if sculpt.Design == (musical.Design{}) {
					continue
				}
				dx := pos.X - sculpt.Target.X
				dy := pos.Z - sculpt.Target.Z
				dist := Float.Sqrt(dx*dx + dy*dy)
				if dist <= sculpt.Radius {
					if tile.editor == nil {
						return 0
					}
					return tile.editor.mapper[sculpt.Design]
				}
			}
			return 0
		}
		var sample_height = func(x, y int) Float.X {
			pos := Vector3.Add(Vector3.XYZ{Float.X(x), 0, Float.X(y)}, offset)
			height := Float.X(0)
			for i := range tile.sculpts {
				sculpt := tile.sculpts[i]
				if sculpt.Design != (musical.Design{}) {
					continue
				}
				dx := pos.X - sculpt.Target.X
				dy := pos.Z - sculpt.Target.Z
				if dx*dx+dy*dy <= sculpt.Radius*sculpt.Radius {
					height += sculpt.Amount * (1 - (dx*dx+dy*dy)/(sculpt.Radius*sculpt.Radius))
				}
			}
			return max(-2, height)
		}
		inv := Float.X(1) / Float.X(n)
		update := func(index int, cell_x, cell_y int, x, y int) {
			tile.vertices[index].Y += sample_height(x, y)
			tile.uvs[index] = Vector2.XY{Float.X(x) * inv, Float.X(y) * inv}
			if sample := sample_texture(cell_x, cell_y); sample != 0 {
				tile.textures[index*4+0] = float32(sample) // top left
			}
			if sample := sample_texture(cell_x+1, cell_y); sample != 0 {
				tile.textures[index*4+1] = float32(sample) // top right
			}
			if sample := sample_texture(cell_x, cell_y+1); sample != 0 {
				tile.textures[index*4+2] = float32(sample) // bottom left
			}
			if sample := sample_texture(cell_x+1, cell_y+1); sample != 0 {
				tile.textures[index*4+3] = float32(sample) // bottom right
			}
		}
		for x := 0; x < n; x++ {
			for y := 0; y < n; y++ {
				update(6*(x+n*y)+0, x, y, x, y)     // top left
				update(6*(x+n*y)+1, x, y, x+1, y)   // top right
				update(6*(x+n*y)+2, x, y, x, y+1)   // bottom left
				update(6*(x+n*y)+3, x, y, x+1, y)   // top right
				update(6*(x+n*y)+4, x, y, x+1, y+1) // bottom right
				update(6*(x+n*y)+5, x, y, x, y+1)   // bottom left
			}
		}
		for i := 0; i < hm*hm; i++ {
			tile.heights[i] += float32(sample_height(i%hm, i/hm))
		}
		tile.heightmapShape.SetMapData(tile.heights)
		attributes := [Mesh.ArrayMax]any{
			Mesh.ArrayVertex:  tile.vertices,
			Mesh.ArrayTexUv:   tile.uvs,
			Mesh.ArrayNormal:  tile.normals,
			Mesh.ArrayCustom0: tile.textures,
			Mesh.ArrayCustom1: tile.weights,
		}
		mesh := Object.To[ArrayMesh.Instance](tile.Mesh.Mesh())
		mesh.ClearSurfaces()
		mesh.MoreArgs().AddSurfaceFromArrays(Mesh.PrimitiveTriangles, attributes[:], nil, nil,
			Mesh.ArrayFormatVertex|
				Mesh.ArrayFormat(Mesh.ArrayCustomRgbaFloat)<<Mesh.ArrayFormatCustom0Shift|
				Mesh.ArrayFormat(Mesh.ArrayCustomRgbaFloat)<<Mesh.ArrayFormatCustom1Shift,
		)
		mesh.RegenNormalMaps()
		for _, brush := range tile.sculpts {
			if brush.Design == (musical.Design{}) {
				// raise any existing assets affected by the sculpt
				for id := range tile.client.object_to_entity {
					object, ok := id.Instance()
					if !ok {
						continue
					}
					pos := object.AsNode3D().GlobalPosition()
					pos.Y = tile.HeightAt(pos)
					object.AsNode3D().SetGlobalPosition(pos)
				}
			}
		}
		tile.reloadSides()
		tile.sculpts = tile.sculpts[:0]
	}))
}

// hasNeighbour reports whether another TerrainTile exists at the
// adjacent grid coord in the given direction. Used by reloadSides
// to decide which vertical walls to emit (only the outer-most sides,
// i.e. those with no neighbour).
func (tile *TerrainTile) hasNeighbour(dir tileCoord) bool {
	if tile.editor == nil {
		return false
	}
	_, ok := tile.editor.tiles[tileCoord{tile.coord.X + dir.X, tile.coord.Z + dir.Z}]
	return ok
}

// reloadSides updates the side meshes to match the current terrain heights.
// Only the outer-most (exposed) sides are emitted; sides that have a
// neighbour tile are omitted so that internal walls are never drawn.
func (tile *TerrainTile) reloadSides() {
	tile_size := float32(1.0) // Adjust for texture tiling scale
	n := tile.size
	hm := n + 1

	// Remove any previous side surface(s) (index 1+) so we can append
	// a fresh one (or none at all). This makes reloadSides safe to call
	// at any time, including when a neighbour is added later.
	mesh := tile.Mesh.Mesh()
	am := ArrayMesh.Nil
	if mesh != Mesh.Nil {
		am = Object.To[ArrayMesh.Instance](mesh)
		for mesh.GetSurfaceCount() > 1 {
			am.SurfaceRemove(mesh.GetSurfaceCount() - 1)
		}
		// Force the MeshInstance3D to observe the removal so its
		// internal surface_override_materials array shrinks to match.
		// Without this, later SetSurfaceOverrideMaterial can fail.
		mi := tile.Mesh
		m := mi.Mesh()
		mi.SetMesh(Mesh.Nil)
		mi.SetMesh(m)
	}

	// Map the four sideParam entries (in the order defined below) to the
	// cardinal direction of the neighbour that would sit on that side.
	sideNeighbourDirs := [4]tileCoord{
		{0, -1}, // South (Z fixed at 0)
		{0, 1},  // North (Z fixed at n)
		{-1, 0}, // West  (X fixed at 0)
		{1, 0},  // East  (X fixed at n)
	}

	// Sides mesh data
	index_base := 0

	type sideParam struct {
		isZFixed       bool
		fixed          float32
		fixedIndex     int
		flippedWinding bool
	}

	sides := [4]sideParam{
		{true, 0, 0, true},           // South
		{true, float32(n), n, false}, // North
		{false, 0, 0, false},         // West
		{false, float32(n), n, true}, // East
	}

	type sideEntry struct {
		sp sideParam
	}
	var active []sideEntry
	for i, sp := range sides {
		if !tile.hasNeighbour(sideNeighbourDirs[i]) {
			active = append(active, sideEntry{sp})
		}
	}

	sideVertCount := len(active) * n * 6
	if sideVertCount == 0 {
		// Completely surrounded tile: nothing to do (no side surface).
		return
	}
	if cap(tile.verticesSide) < sideVertCount {
		tile.verticesSide = make([]Vector3.XYZ, sideVertCount)
		tile.normalsSide = make([]Vector3.XYZ, sideVertCount)
		tile.uvsSide = make([]Vector2.XY, sideVertCount)
	} else {
		tile.verticesSide = tile.verticesSide[:sideVertCount]
		tile.normalsSide = tile.normalsSide[:sideVertCount]
		tile.uvsSide = tile.uvsSide[:sideVertCount]
	}
	vertices_side := tile.verticesSide
	normals_side := tile.normalsSide
	uvs_side := tile.uvsSide
	for _, entry := range active {
		sp := entry.sp
		for i := 0; i < n; i++ {
			coord := i
			var h_near, h_far float32
			if sp.isZFixed {
				h_near = tile.heights[coord+sp.fixedIndex*hm]
				h_far = tile.heights[coord+1+sp.fixedIndex*hm]
			} else {
				h_near = tile.heights[sp.fixedIndex+coord*hm]
				h_far = tile.heights[sp.fixedIndex+(coord+1)*hm]
			}
			h_near += 2.2
			h_far += 2.2
			pos_near := float32(i)
			pos_far := float32(i + 1)
			var tl, tr, bl, br Vector3.XYZ
			if sp.isZFixed {
				tl = Vector3.XYZ{pos_near, h_near, sp.fixed}
				tr = Vector3.XYZ{pos_far, h_far, sp.fixed}
				bl = Vector3.XYZ{pos_near, 0, sp.fixed}
				br = Vector3.XYZ{pos_far, 0, sp.fixed}
			} else {
				tl = Vector3.XYZ{sp.fixed, h_near, pos_near}
				tr = Vector3.XYZ{sp.fixed, h_far, pos_far}
				bl = Vector3.XYZ{sp.fixed, 0, pos_near}
				br = Vector3.XYZ{sp.fixed, 0, pos_far}
			}
			var v1, v2 Vector3.XYZ
			if sp.flippedWinding {
				v1 = Vector3.Sub(tr, bl)
				v2 = Vector3.Sub(tl, bl)
			} else {
				v1 = Vector3.Sub(tl, bl)
				v2 = Vector3.Sub(tr, bl)
			}
			n := Vector3.Normalized(Vector3.Cross(v1, v2))
			// Triangle 1
			vertices_side[index_base+0] = bl
			normals_side[index_base+0] = n
			uvs_side[index_base+0] = Vector2.XY{float32(i) / tile_size, 0 / tile_size}
			if sp.flippedWinding {
				vertices_side[index_base+1] = tr
				normals_side[index_base+1] = n
				uvs_side[index_base+1] = Vector2.XY{float32(i+1) / tile_size, h_far / tile_size}
				vertices_side[index_base+2] = tl
				normals_side[index_base+2] = n
				uvs_side[index_base+2] = Vector2.XY{float32(i) / tile_size, h_near / tile_size}
			} else {
				vertices_side[index_base+1] = tl
				normals_side[index_base+1] = n
				uvs_side[index_base+1] = Vector2.XY{float32(i) / tile_size, h_near / tile_size}
				vertices_side[index_base+2] = tr
				normals_side[index_base+2] = n
				uvs_side[index_base+2] = Vector2.XY{float32(i+1) / tile_size, h_far / tile_size}
			}
			// Triangle 2
			vertices_side[index_base+3] = bl
			normals_side[index_base+3] = n
			uvs_side[index_base+3] = Vector2.XY{float32(i) / tile_size, 0 / tile_size}
			if sp.flippedWinding {
				vertices_side[index_base+4] = br
				normals_side[index_base+4] = n
				uvs_side[index_base+4] = Vector2.XY{float32(i+1) / tile_size, 0 / tile_size}
				vertices_side[index_base+5] = tr
				normals_side[index_base+5] = n
				uvs_side[index_base+5] = Vector2.XY{float32(i+1) / tile_size, h_far / tile_size}
			} else {
				vertices_side[index_base+4] = tr
				normals_side[index_base+4] = n
				uvs_side[index_base+4] = Vector2.XY{float32(i+1) / tile_size, h_far / tile_size}
				vertices_side[index_base+5] = br
				normals_side[index_base+5] = n
				uvs_side[index_base+5] = Vector2.XY{float32(i+1) / tile_size, 0 / tile_size}
			}
			index_base += 6
		}
	}
	// Prepare mesh arrays for side surface
	arrays_side := [Mesh.ArrayMax]any{
		Mesh.ArrayVertex: vertices_side,
		Mesh.ArrayNormal: normals_side,
		Mesh.ArrayTexUv:  uvs_side,
	}
	am.MoreArgs().AddSurfaceFromArrays(Mesh.PrimitiveTriangles, arrays_side[:], nil, nil,
		Mesh.ArrayFormatVertex|Mesh.ArrayFormatNormal|Mesh.ArrayFormatTexUv,
	)

	// Force the MeshInstance3D to rebind the mesh so its
	// surface_override_materials array grows to match the new
	// surface count on the resource (we just added surface 1).
	// Without this, SetSurfaceOverrideMaterial can fail with
	// "index out of bounds".
	mi := tile.Mesh
	m := mi.Mesh()
	mi.SetMesh(Mesh.Nil)
	mi.SetMesh(m)

	if mi.GetSurfaceOverrideMaterialCount() > 0 {
		mi.SetSurfaceOverrideMaterial(0, tile.shader.AsMaterial())
	}
	if mi.GetSurfaceOverrideMaterialCount() > 1 {
		mi.SetSurfaceOverrideMaterial(1, tile.side_shader.AsMaterial())
	}
}

// HeightAt returns the height of the terrain mesh at the given position, taking into account the mesh.
func (tile *TerrainTile) HeightAt(pos Vector3.XYZ) Float.X {
	n := tile.size
	hm := n + 1
	maxF := Float.X(n)
	local := Vector3.Sub(pos, tile.Mesh.AsNode3D().GlobalPosition())
	x := local.X
	z := local.Z
	x = max(0.0, min(maxF, x))
	z = max(0.0, min(maxF, z))
	x0 := int(x)
	z0 := int(z)
	x1 := x0 + 1
	z1 := z0 + 1
	if x1 > n {
		x1 = n
	}
	if z1 > n {
		z1 = n
	}
	h00 := Float.X(tile.heights[x0+z0*hm])
	h10 := Float.X(tile.heights[x1+z0*hm])
	h01 := Float.X(tile.heights[x0+z1*hm])
	h11 := Float.X(tile.heights[x1+z1*hm])
	sx := x - Float.X(x0)
	sz := z - Float.X(z0)
	h0 := h00*(1-sx) + h10*sx
	h1 := h01*(1-sx) + h11*sx
	return (h0*(1-sz) + h1*sz)
}

// NormalAt returns the surface normal of the terrain mesh at the given position.
func (tile *TerrainTile) NormalAt(pos Vector3.XYZ) Vector3.XYZ {
	size := tile.size
	hm := size + 1
	maxF := Float.X(size)
	local := Vector3.Sub(pos, tile.Mesh.AsNode3D().GlobalPosition())
	x := local.X
	z := local.Z
	x = max(0.0, min(maxF, x))
	z = max(0.0, min(maxF, z))
	x0 := int(x)
	z0 := int(z)
	x1 := x0 + 1
	z1 := z0 + 1
	if x1 > size {
		x1 = size
	}
	if z1 > size {
		z1 = size
	}
	h00 := Float.X(tile.heights[x0+z0*hm])
	h10 := Float.X(tile.heights[x1+z0*hm])
	h01 := Float.X(tile.heights[x0+z1*hm])
	h11 := Float.X(tile.heights[x1+z1*hm])
	sx := x - Float.X(x0)
	sz := z - Float.X(z0)
	fx := (1-sz)*(h10-h00) + sz*(h11-h01)
	fz := (1-sx)*(h01-h00) + sx*(h11-h10)
	n := Vector3.XYZ{
		X: -fx,
		Y: 1,
		Z: -fz,
	}
	length := Float.Sqrt(n.X*n.X + n.Y*n.Y + n.Z*n.Z)
	if length == 0 {
		length = 1
	}
	n.X /= Float.X(length)
	n.Y /= Float.X(length)
	n.Z /= Float.X(length)
	return n
}

func (tile *TerrainTile) InputEvent(camera Camera3D.Instance, event InputEvent.Instance, pos, normal Vector3.XYZ, shape int) {
	if event, ok := Object.As[InputEventMouseButton.Instance](event); ok && tile.client.Editing == Editing.Terrain {
		if event.ButtonIndex() == Input.MouseButtonLeft {
			if event.AsInputEvent().IsPressed() {
				select {
				case tile.brushEvents <- terrainBrushEvent{
					BrushTarget: pos,
					BrushDeltaV: 2,
				}:
				default:
				}
			}
		}
		if event.ButtonIndex() == Input.MouseButtonRight {
			if event.AsInputEvent().IsPressed() {
				select {
				case tile.brushEvents <- terrainBrushEvent{
					BrushTarget: pos,
					BrushDeltaV: -2,
				}:
				default:
				}
			}
		}
	} else if !Input.IsKeyPressed(Input.KeyShift) {
		// Holding Shift freezes the brush: skip mouse-motion tracking so
		// the brush target (and its highlight) stays put instead of
		// following the cursor, letting the user keep sculpting a fixed
		// spot. Button presses handled above are unaffected.
		select {
		case tile.brushEvents <- terrainBrushEvent{
			BrushTarget: pos,
		}:
		default:
		}
	}
}

// spawnArrows creates an extend-the-world arrow on each of the four
// cardinal sides that doesn't already have a neighbour tile. Called
// once when a tile is first created.
func (tile *TerrainTile) spawnArrows() {
	for _, dir := range cardinalDirs {
		if _, ok := tile.editor.tiles[tileCoord{tile.coord.X + dir.X, tile.coord.Z + dir.Z}]; ok {
			continue
		}
		tile.addArrow(dir)
	}
}

func (tile *TerrainTile) addArrow(dir tileCoord) {
	arrow := new(TerrainTileArrow)
	arrow.tile = tile
	arrow.direction = dir
	half := Float.X(terrainDefaultSize) / 2
	// Position the arrow just past the open edge, sunk below ground
	// level so it reads as a "grow here" marker tucked under the rim
	// rather than floating up high.
	arrow_pos := Vector3.New(
		Float.X(dir.X)*(half+1),
		-1.5,
		Float.X(dir.Z)*(half+1),
	)
	tile.AsNode().AddChild(arrow.AsNode())
	arrow.AsNode3D().SetPosition(arrow_pos)
	// Rotate so the cone's tip points along `dir`. The mesh inside is
	// pre-rotated to point along -Z; we yaw by π for +Z and 0 for -Z
	// so the final world-space direction matches the unit vector.
	var yaw Angle.Radians
	switch dir {
	case tileCoord{1, 0}:
		yaw = -Angle.Pi / 2
	case tileCoord{-1, 0}:
		yaw = Angle.Pi / 2
	case tileCoord{0, 1}:
		yaw = Angle.Pi
	case tileCoord{0, -1}:
		yaw = 0
	}
	arrow.AsNode3D().Rotate(Vector3.XYZ{0, 1, 0}, yaw)
	arrow.AsNode3D().SetVisible(tile.editor.arrowsVisible)
	tile.arrows[dir] = arrow
}

func (tile *TerrainTile) removeArrow(dir tileCoord) {
	arrow, ok := tile.arrows[dir]
	if !ok {
		return
	}
	arrow.AsNode().QueueFree()
	delete(tile.arrows, dir)
}

// TerrainTileArrow is a click-to-extend marker spawned by a tile on
// each cardinal side that doesn't yet have a neighbour. Clicking the
// arrow asks the editor to instantiate the adjacent chunk and the
// arrow self-destructs.
type TerrainTileArrow struct {
	StaticBody3D.Extension[TerrainTileArrow] `gd:"AviaryTerrainTileArrow"`

	tile      *TerrainTile
	direction tileCoord
}

func (a *TerrainTileArrow) Ready() {
	material := StandardMaterial3D.New().AsBaseMaterial3D().
		SetAlbedoColor(Color.RGBA{R: 1, G: 1, B: 1, A: 1})

	// Arrowhead cone at the far end, half the old size. Its apex points
	// +Y by default; rotate so it points along the tile's local -Z,
	// away from the shaft (the parent's yaw then aims it outward).
	cone := CylinderMesh.New().
		SetTopRadius(0).
		SetBottomRadius(0.5).
		SetHeight(0.6)
	cone.AsPrimitiveMesh().SetMaterial(material.AsMaterial())
	head := MeshInstance3D.New().SetMesh(cone.AsMesh())
	head.AsNode3D().Rotate(Vector3.XYZ{1, 0, 0}, -Angle.Pi/2)
	head.AsNode3D().SetPosition(Vector3.New(0, 0, -0.6))
	a.AsNode().AddChild(head.AsNode())

	// Cylinder shaft (the handle) behind the head.
	stem := CylinderMesh.New().
		SetTopRadius(0.18).
		SetBottomRadius(0.18).
		SetHeight(0.7)
	stem.AsPrimitiveMesh().SetMaterial(material.AsMaterial())
	shaft := MeshInstance3D.New().SetMesh(stem.AsMesh())
	shaft.AsNode3D().Rotate(Vector3.XYZ{1, 0, 0}, -Angle.Pi/2)
	shaft.AsNode3D().SetPosition(Vector3.New(0, 0, 0.05))
	a.AsNode().AddChild(shaft.AsNode())

	col := CollisionShape3D.New()
	box := BoxShape3D.New().SetSize(Vector3.New(1.25, 1.25, 1.25))
	col.SetShape(box.AsShape3D())
	a.AsNode().AddChild(col.AsNode())
}

func (a *TerrainTileArrow) InputEvent(_ Camera3D.Instance, event InputEvent.Instance, _, _ Vector3.XYZ, _ int) {
	mouse, ok := Object.As[InputEventMouseButton.Instance](event)
	if !ok {
		return
	}
	if mouse.ButtonIndex() != Input.MouseButtonLeft || !mouse.AsInputEvent().IsPressed() {
		return
	}
	if a.tile == nil || a.tile.editor == nil {
		return
	}
	a.tile.editor.tileAt(tileCoord{
		X: a.tile.coord.X + a.direction.X,
		Z: a.tile.coord.Z + a.direction.Z,
	})
}
