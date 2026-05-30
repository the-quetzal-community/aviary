package internal

import (
	"path"
	"strings"
	"time"

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

	// Water material shared by every tile. The same wave shader drives BOTH
	// water surfaces (plane + side walls) so the sides stay in sync with the
	// plane; the per-vertex terrain floor (CUSTOM0.r) clamps the water above
	// the terrain.
	water_shader ShaderMaterial.Instance

	// WaterLevel is the world-space Y of the water surface. The default of
	// -2 matches the bottom of the terrain skirt, so by default the water
	// sits hidden under flat terrain (i.e. there is no visible water until
	// the level is raised).
	WaterLevel Float.X

	// waterVisible toggles the per-tile water meshes; water is only shown
	// in the terrain and scenery editors.
	waterVisible bool

	// lastWaterSync throttles outgoing water-level slider mutations.
	lastWaterSync time.Time

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

	// TerrainBrush is the currently selected terrain sculpt tool when in
	// ModeGeometry. It is set by picking a builtin from the "terrain" tab
	// and cleared when the brush is cancelled (right-click, leaving the
	// editor, etc.). Values are the procedural:// sentinel strings or "".
	TerrainBrush string

	PaintActive bool

	//
	// Dressing brush parameters. ModeDressing scatters instanced meshes
	// (grasses, pebbles) across the terrain surface. A stroke is recorded
	// as one musical.Sculpt (Editor "terrain", Slider = the dressing tab,
	// Amount = density, Design = the scattered mesh) so the placement is
	// observable by, and deterministically reproducible on, every client —
	// the scatter is seeded purely from the sculpt's Author/Target/Radius.
	//
	DressActive  bool   // a dressing design is selected and the brush is armed
	DressDesign  string // selected mesh resource (res://...glb), local only
	DressTab     string // dressing category ("grasses"/"pebbles")
	BrushDensity Float.X

	// BrushRiverDepth is how far below the original ground the river brush
	// carves its channel (the water then fills back to the original ground).
	// Set via the river-depth slider; local-only (the depth rides in each
	// river Sculpt's Amount, so remote clients carve the same channel).
	BrushRiverDepth Float.X

	// dressLast/dressLastSet space out dressing strokes during a drag so a
	// stationary hold doesn't keep re-committing the same patch: a new
	// stroke is only emitted once the brush has moved ~half a radius.
	dressLast    Vector3.XYZ
	dressLastSet bool

	// brushStrokeActive is true only while the user is actively holding the
	// left mouse button over actual terrain geometry after a press that
	// landed on the world (not on 2D UI). This is the authoritative signal
	// for continuous painting / dressing / height sculpting. UI clicks in
	// the design explorer can never set this, preventing accidental strokes
	// when selecting a design.
	brushStrokeActive bool

	// grassPatches holds the rendered scatter for every committed dressing
	// sculpt, so height sculpts can re-project the instances back onto the
	// surface (see reprojectGrass). grassMeshes caches the Mesh pulled from
	// each dressing Design's .glb. pendingGrass holds sculpts whose mesh
	// hadn't finished importing when they arrived; Process retries them.
	grassPatches []*grassPatch
	grassMeshes  map[musical.Design]grassAsset
	pendingGrass []musical.Sculpt

	client *Client

	// lighting holds the shared world lighting used by both Terrain and
	// Scenery. All other editors that embed lighting get their own private
	// copy.
	lighting
}

// cardinalDirs are the four neighbour offsets used by the chunk
// machinery — both when spawning "extend the world" arrows and when
// pruning the matching arrow on an existing neighbour as a new tile
// fills its side.
var cardinalDirs = [4]tileCoord{{1, 0}, {-1, 0}, {0, 1}, {0, -1}}

// tileAt returns the tile at the given grid coord, creating it HIDDEN on
// demand. New tiles are positioned in world space at coord*size and share the
// editor's shader instances so the brush highlight + paint textures stay
// consistent across chunks. A freshly created tile holds + accumulates edits
// (so a brush spilling over an edge, or a replayed sculpt, builds it up) but
// renders nothing and is not pickable until reveal() promotes it into the
// world. Use revealTile to both create and reveal — the explicit "extend".
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
	tile.water_shader = tr.water_shader
	tile.brushEvents = tr.brushEvents
	tile.arrows = make(map[tileCoord]*TerrainTileArrow)
	tr.tiles[coord] = tile
	tr.AsNode().AddChild(tile.AsNode())
	tile.AsNode3D().SetPosition(Vector3.New(
		Float.X(coord.X*terrainDefaultSize),
		0,
		Float.X(coord.Z*terrainDefaultSize),
	))
	// Hidden until explicitly revealed: invisible, not pickable, and counted as
	// absent by neighbours (so they keep their edge wall + extend arrow).
	// generateBase (run synchronously on AddChild) already applied the hidden
	// state via applyRevealState; set the node hidden here too in case Ready
	// has not run yet, to avoid a one-frame flash of an ungenerated mesh.
	tile.AsNode3D().SetVisible(false)
	return tile
}

// revealTile creates (if needed) and reveals the tile at coord — the explicit
// "extend the world" step. Idempotent: revealing an already-revealed tile is a
// no-op.
func (tr *TerrainEditor) revealTile(coord tileCoord) *TerrainTile {
	tile := tr.tileAt(coord)
	tile.reveal()
	return tile
}

// tilesIntersecting returns every tile whose AABB intersects the given
// world-space brush sphere, creating any missing ones on demand (HIDDEN — a
// brush spilling over an edge builds up the neighbour's data without revealing
// it; see tileAt/reveal). Sculpts straddling a tile boundary apply to all
// overlapping chunks so the brush effect is continuous across the seam.
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

// coordForWorld returns the grid coord of the tile whose AABB contains the
// given world position, whether or not a tile exists there yet.
func (tr *TerrainEditor) coordForWorld(pos Vector3.XYZ) tileCoord {
	size := Float.X(terrainDefaultSize)
	half := size / 2
	return tileCoord{
		int(Float.Floor((pos.X + half) / size)),
		int(Float.Floor((pos.Z + half) / size)),
	}
}

// tileForWorld returns the tile whose AABB contains the given world
// position, or nil if no tile has yet been instantiated there. Used
// by HeightAt/NormalAt so scenery, action_renderer and the like land
// on the right chunk.
func (tr *TerrainEditor) tileForWorld(pos Vector3.XYZ) *TerrainTile {
	return tr.tiles[tr.coordForWorld(pos)]
}

func (fe *TerrainEditor) Name() string { return "terrain" }

func (fe *TerrainEditor) EnableEditor() {
	// Terrain is a brush editor: it sculpts/paints the ground rather than
	// selecting & transforming placed entities, so it offers only the brush
	// tool. Declaring it here also makes the brush the active gizmo (the
	// neutral Point tool is not in the set, so SetGizmos switches off it) —
	// the brush-size slider then anchors to this button.
	fe.client.SetGizmos([]Gizmo{GizmoBrush})
	fe.shader.SetShaderParameter("brush_active", true)
	fe.shader_buried.SetShaderParameter("brush_active", true)
	fe.setArrowsVisible(true)
	fe.lighting.apply(fe.client)
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
	fe.DressActive = false
	fe.TerrainBrush = ""
	fe.brushStrokeActive = false
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
		// The "terrain" tab provides the raise/lower/river height brushes as
		// builtin items. The brush-size slider lives in the gizmo toolbar;
		// water-level and river-depth are available as editing sliders.
		return []string{"terrain", "editing/water_level", "editing/river_depth"}
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

// BuiltinDesigns provides the raise/lower terrain brushes as builtin
// items in the "terrain" tab when in ModeGeometry. These appear as
// the primary content of the tab (no library preview directory is
// required for the tab to be shown).
func (fe *TerrainEditor) BuiltinDesigns(mode Mode, tab string) []BuiltinDesign {
	if mode != ModeGeometry || tab != "terrain" {
		return nil
	}
	return []BuiltinDesign{
		{
			Resource: BuiltinTerrainRaise,
			Icon:     "res://ui/terrain.svg",
			Label:    "Raise terrain",
		},
		{
			Resource: BuiltinTerrainLower,
			Icon:     "res://ui/editing.svg",
			Label:    "Lower terrain",
		},
		{
			Resource: BuiltinTerrainRiver,
			Icon:     "res://ui/editing.svg",
			Label:    "River",
		},
		{
			Resource: BuiltinTerrainRiverErase,
			Icon:     "res://ui/editing.svg",
			Label:    "River eraser",
		},
	}
}

func (fe *TerrainEditor) SelectDesign(mode Mode, design string) {
	if mode == ModeDressing {
		// Arm the dressing brush: the selected mesh scatters across the
		// surface on the next stroke. The tab (parent dir, e.g.
		// "grasses") is carried into the sculpt's Slider so the category
		// round-trips and remote clients route it the same way.
		fe.CancelPaint()
		fe.DressActive = true
		fe.DressDesign = design
		fe.DressTab = path.Base(path.Dir(design))
		fe.BrushDesign = design
		// Allow the very next user-initiated stroke after picking a design
		// to fire without the movement-spacing guard (original behaviour).
		fe.dressLastSet = false
		fe.EnableEditor()
		return
	}
	// Terrain brush builtins (raise/lower) in ModeGeometry: arm the
	// explicit height sculpt brush. This replaces the previous implicit
	// "any click in geometry mode sculpts height" behaviour.
	if mode == ModeGeometry && (design == BuiltinTerrainRaise || design == BuiltinTerrainLower || design == BuiltinTerrainRiver || design == BuiltinTerrainRiverErase) {
		fe.CancelPaint()
		fe.TerrainBrush = design
		fe.DressActive = false
		// The river brush is a drag-paint tool (like dressing): clear the
		// stroke-spacing guard so the first stroke after selecting it fires,
		// and seeds a flow direction from the very next movement.
		fe.dressLastSet = false
		// Enable the editor (ensures brush ring highlight is visible).
		fe.EnableEditor()
		// Do not set BrushActive yet; the first press on terrain will
		// drive the initial delta and arm the transient stroke state.
		return
	}
	fe.DressActive = false
	// Only clear a terrain brush selection when the user picks something
	// else while in geometry mode (e.g. they had a raise/lower tool picked
	// and then picked a different geometry action, if any). Picking a
	// texture in ModeMaterial should not disarm the geometry brush tool.
	if mode == ModeGeometry {
		fe.TerrainBrush = ""
	}
	select {
	case fe.texture <- Path.ToResource(String.New(design)):
		fe.EnableEditor()
	default:
	}
}
func (fe *TerrainEditor) SliderHandle(mode Mode, editing string, value float64, commit bool) {
	switch editing {
	case "editing/radius":
		// Brush radius is a local-only highlight control; not synced.
		fe.BrushRadius = Float.X(value)
		fe.shader.SetShaderParameter("radius", fe.BrushRadius)
	case "dressing/density":
		fe.BrushDensity = Float.X(value)
	case "editing/river_depth":
		// River channel depth is a local-only brush control (the depth is
		// carried per-stroke in the Sculpt's Amount, so it still reproduces
		// on every client). Not itself synced.
		fe.BrushRiverDepth = Float.X(value)
	case "editing/water_level":
		// The water level is a shared mutation: route it through the space
		// so every client observes the same level. Without a space (e.g.
		// before joining) apply it locally instead.
		if fe.client == nil {
			fe.applyWaterLevel(Float.X(value))
			return
		}
		if !commit && time.Since(fe.lastWaterSync) < time.Second/10 {
			return
		}
		fe.lastWaterSync = time.Now()
		fe.client.space.Sculpt(musical.Sculpt{
			Author: fe.client.id,
			Editor: "terrain",
			Slider: "editing/water_level",
			Amount: Float.X(value),
			Commit: commit,
		})
	}
}

func (fe *TerrainEditor) SliderConfig(mode Mode, editing string) (init, min, max, step float64) {
	switch editing {
	case "dressing/density":
		return float64(fe.BrushDensity), 0, 1, 0.01
	case "editing/river_depth":
		// value, min, max, step (world units of channel depth)
		return float64(fe.BrushRiverDepth), 0.5, 10, 0.1
	case "editing/water_level":
		// value, min, max, step
		return float64(fe.WaterLevel), -2, 10, 0.1
	default:
		return float64(fe.BrushRadius), 0, 10, 0.01
	}
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

	// Water surface material plus its scrolling normal maps, UV distortion
	// map and foam texture. The PNGs are raw (no .import files). The same wave
	// shader drives both water surfaces (plane + side walls); the side walls
	// share the plane edge's world XZ so they get the identical Gerstner
	// displacement and stay connected to the plane.
	water := LoadSync[Shader.Instance]("res://shader/water.gdshader")
	tr.water_shader = ShaderMaterial.New().
		SetShader(water).
		SetShaderParameter("normalmap_a_sampler", LoadSync[Texture2D.Instance]("res://terrain/water/Water_N_A.png")).
		SetShaderParameter("normalmap_b_sampler", LoadSync[Texture2D.Instance]("res://terrain/water/Water_N_B.png")).
		SetShaderParameter("uv_sampler", LoadSync[Texture2D.Instance]("res://terrain/water/Water_UV.png")).
		SetShaderParameter("foam_sampler", LoadSync[Texture2D.Instance]("res://terrain/water/Foam.png"))

	// Default level -2 == skirt bottom == hidden under flat terrain.
	tr.WaterLevel = -2
	tr.water_shader.SetShaderParameter("water_level", float64(tr.WaterLevel))

	tr.BrushRadius = 2.0
	tr.BrushDensity = 0.5
	tr.BrushRiverDepth = riverDefaultDepth

	tr.grassMeshes = make(map[musical.Design]grassAsset)
	tr.tiles = make(map[tileCoord]*TerrainTile)
	tr.mapper = make(map[musical.Design]int)
	tr.albedos = []Image.Instance{LoadSync[Texture2D.Instance]("res://terrain/alpine_grass.png").AsTexture2D().GetImage()}
	tr.normal_maps = []Image.Instance{LoadSync[Texture2D.Instance]("res://terrain/normal.png").AsTexture2D().GetImage()}
	tr.uploadTextureArrays()
	// Spawn + reveal the starter tile so the world is clickable before any
	// sculpt arrives (every other tile starts hidden until an explicit extend).
	tr.revealTile(tileCoord{0, 0})
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

// CancelPaint clears the active paint/dressing state — used by callers
// outside the editor (e.g. right-click in the world view) so they
// don't have to know to flip both the shader uniform and the
// PaintActive/DressActive flags. It also disarms any selected terrain
// height brush. Returns true if anything was cleared.
func (tr *TerrainEditor) CancelPaint() bool {
	active := false
	if tr.PaintActive {
		tr.shader.SetShaderParameter("paint_active", false)
		tr.PaintActive = false
		active = true
	}
	if tr.DressActive {
		tr.DressActive = false
		tr.brushStrokeActive = false
		active = true
	}
	if tr.TerrainBrush != "" {
		tr.TerrainBrush = ""
		tr.BrushActive = false
		tr.BrushAmount = 0
		tr.BrushDeltaV = 0
		active = true
	}
	if active {
		// No paint, dress or height brush armed: hide the ring highlight.
		tr.shader.SetShaderParameter("brush_active", false)
		tr.shader_buried.SetShaderParameter("brush_active", false)
	}
	return active
}

// PaintDressing commits the current dressing brush as one scatter
// stroke, recorded as a musical.Sculpt so every client reproduces the
// same instances deterministically. Called (throttled) from the client
// process loop while the left mouse is held in ModeDressing — mirroring
// how Paint() drives texture painting.
func (tr *TerrainEditor) PaintDressing() {
	if !tr.DressActive || tr.DressDesign == "" {
		return
	}
	// Skip re-committing while the brush is essentially stationary — the
	// scatter is deterministic per (Target, Radius), so a repeat at the
	// same spot would only duplicate identical grass and bloat the log.
	if tr.dressLastSet {
		dx := tr.BrushTarget.X - tr.dressLast.X
		dz := tr.BrushTarget.Z - tr.dressLast.Z
		spacing := tr.BrushRadius * 0.5
		if dx*dx+dz*dz < spacing*spacing {
			return
		}
	}
	tr.dressLast = tr.BrushTarget
	tr.dressLastSet = true
	tr.client.space.Sculpt(musical.Sculpt{
		Author: tr.client.id,
		Editor: "terrain",
		Slider: tr.DressTab,
		Target: tr.BrushTarget,
		Radius: tr.BrushRadius,
		Amount: tr.BrushDensity,
		Design: tr.client.MusicalDesign(tr.DressDesign),
		Commit: true,
	})
}

// EraseDressing commits an erase stroke for the current dressing brush.
// It removes any instances of the selected design whose centers fall
// inside the brush disc. Amount is sent negative so the receiver knows
// this is a removal rather than an add. Uses the same movement-spacing
// throttle as painting to keep the musical log from growing with
// redundant identical erases.
func (tr *TerrainEditor) EraseDressing() {
	if !tr.DressActive || tr.DressDesign == "" {
		return
	}
	if tr.dressLastSet {
		dx := tr.BrushTarget.X - tr.dressLast.X
		dz := tr.BrushTarget.Z - tr.dressLast.Z
		spacing := tr.BrushRadius * 0.5
		if dx*dx+dz*dz < spacing*spacing {
			return
		}
	}
	tr.dressLast = tr.BrushTarget
	tr.dressLastSet = true
	tr.client.space.Sculpt(musical.Sculpt{
		Author: tr.client.id,
		Editor: "terrain",
		Slider: tr.DressTab,
		Target: tr.BrushTarget,
		Radius: tr.BrushRadius,
		Amount: -tr.BrushDensity, // <=0 signals erase for this Design
		Design: tr.client.MusicalDesign(tr.DressDesign),
		Commit: true,
	})
}

// PaintRiver commits one segment of a river stroke as a SINGLE musical.Sculpt
// while the river brush is dragged. The stroke carves a channel (negative
// Amount, applied through the shared height summation) and, because it carries
// the riverSlider tag, Reload additionally records the local water surface
// ("the height where the terrain used to be") and the flow direction. Orient
// is the painted heading, so the water flows downstream. One sculpt per
// segment — there is deliberately no separate carve/water entry.
func (tr *TerrainEditor) PaintRiver() {
	if !tr.riverBrushActive() {
		return
	}
	erase := tr.TerrainBrush == BuiltinTerrainRiverErase
	// Movement-spacing throttle (shared with dressing): only emit once the
	// brush has moved ~half a radius, and use that travel as the flow heading.
	// The first segment of a drag has no previous point, so it seeds dressLast
	// and waits for the next move to establish a direction.
	if !tr.dressLastSet {
		tr.dressLast = tr.BrushTarget
		tr.dressLastSet = true
		return
	}
	dx := tr.BrushTarget.X - tr.dressLast.X
	dz := tr.BrushTarget.Z - tr.dressLast.Z
	spacing := tr.BrushRadius * 0.5
	if dx*dx+dz*dz < spacing*spacing {
		return
	}
	orient := Angle.Atan2(dz, dx) // painted heading => downstream flow
	tr.dressLast = tr.BrushTarget
	brush := musical.Sculpt{
		Author: tr.client.id,
		Editor: "terrain",
		Slider: riverSlider,
		Target: tr.BrushTarget,
		Radius: tr.BrushRadius,
		Orient: orient,
		Commit: true,
	}
	if erase {
		// The eraser fills the channel back in (target depth 0); Amount/Orient
		// are unused on the receiving side for an erase stroke.
		brush.Slider = riverEraseSlider
	} else {
		depth := tr.BrushRiverDepth
		if depth <= 0 {
			depth = riverDefaultDepth
		}
		brush.Amount = -depth // negative carve depth; overwrites on repaint
	}
	tr.client.space.Sculpt(brush)
}

// riverBrushActive reports whether a river paint or erase tool is selected.
func (tr *TerrainEditor) riverBrushActive() bool {
	return tr.TerrainBrush == BuiltinTerrainRiver || tr.TerrainBrush == BuiltinTerrainRiverErase
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

// WaterSurfaceAt returns the world-Y of the water surface at a world position:
// the local river surface where one has been carved, otherwise the global lake
// level. The underwater post-process samples this under the camera so the
// waterline tracks rivers (not just the flat global plane). Falls back to the
// global level where no tile is loaded.
func (tr *TerrainEditor) WaterSurfaceAt(pos Vector3.XYZ) Float.X {
	tile := tr.tileForWorld(pos)
	if tile == nil {
		return tr.WaterLevel
	}
	return tile.WaterSurfaceAt(pos)
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
			} else if vr.client.ui.mode == ModeDressing {
				// Dressing only needs the brush ring to track the cursor;
				// strokes are committed by the client's throttle loop
				// (PaintDressing). Never arm the height brush here.
				vr.BrushTarget = event.BrushTarget
				vr.BrushDeltaV = 0
			} else if !vr.PaintActive && vr.client.ui.mode != ModeMaterial {
				// Height sculpt input is only accepted when a terrain brush
				// tool has been explicitly selected in ModeGeometry, or we
				// are already mid-stroke (BrushActive). This replaces the
				// previous "any click in geometry arm the brush" behaviour.
				if vr.TerrainBrush != "" || vr.BrushActive {
					vr.BrushTarget = event.BrushTarget
					vr.BrushDeltaV = event.BrushDeltaV
					if event.BrushDeltaV != 0 {
						vr.BrushActive = true
					}
				} else {
					vr.BrushDeltaV = 0
				}
			} else {
				vr.BrushDeltaV = 0
			}
			continue
		default:
		}
		break
	}
	if vr.BrushActive && !vr.PaintActive && vr.client.ui.mode == ModeGeometry && vr.TerrainBrush != "" {
		vr.BrushAmount += dt * vr.BrushDeltaV
		vr.shader.SetShaderParameter("height", vr.BrushAmount)
		vr.shader_buried.SetShaderParameter("height", vr.BrushAmount)
	} else {
		vr.BrushAmount = 0.0
		vr.shader.SetShaderParameter("height", vr.BrushAmount)
		vr.shader_buried.SetShaderParameter("height", vr.BrushAmount)
	}
	vr.retryPendingGrass()
}

func (vr *TerrainEditor) Sculpt(brush musical.Sculpt) error {
	// Water-level slider sculpts are routed through the terrain editor but
	// are not height/paint/dressing edits, so handle them up front.
	if brush.Slider == "editing/water_level" {
		vr.applyWaterLevel(Float.X(brush.Amount))
		return nil
	}
	// "Extend the world" reveals a tile, and is the ONLY way the visible grid
	// grows. It is an explicit, observable mutation (emitted by an arrow click)
	// so every client and every replay reveals the same chunk; the tile's data
	// was already built up implicitly (brush spill-over / replayed sculpts), so
	// revealing it shows the accumulated edits seamlessly. Without routing this
	// through the log, a tile revealed only on the authoring client would be
	// missing elsewhere and its revealed neighbours would wall into the gap.
	if brush.Editor == "terrain" && brush.Slider == extendSlider {
		vr.revealTile(vr.coordForWorld(brush.Target))
		return nil
	}
	// Environment sliders for the shared world look (terrain + scenery).
	if strings.HasPrefix(brush.Slider, "environment/") {
		if vr.lighting.handleEnvironmentSlider(brush.Slider, Float.X(brush.Amount)) {
			vr.lighting.apply(vr.client)
			return nil
		}
	}
	// Terrain processes its own height/paint sculpts (legacy Editor "")
	// and the dressing sculpts it authors (Editor "terrain"). Anything
	// else was routed here only as the client's fallback and isn't ours.
	if brush.Editor != "" && brush.Editor != "terrain" {
		return nil
	}
	// A dressing stroke carries the category in Slider and the scattered
	// mesh in Design; positive Amount adds instances, <=0 erases any
	// matching instances whose centers fall inside the disc.
	if brush.Slider != "" && brush.Design != (musical.Design{}) {
		if brush.Amount <= 0 {
			vr.eraseGrass(brush)
		} else {
			vr.scatterGrass(brush)
		}
		return nil
	}
	if brush.Author == vr.client.id {
		vr.shader.SetShaderParameter("height", 0.0)
		vr.shader_buried.SetShaderParameter("height", 0.0)
	}
	for _, tile := range vr.tilesIntersecting(brush.Target, brush.Radius) {
		tile.Sculpt(brush)
	}
	// A height sculpt (no Design) reshapes the ground, so any grass already
	// scattered over the affected area must be re-planted on the new
	// surface. Defer it so the tiles' deferred Reload has refreshed their
	// heights before we re-sample HeightAt/NormalAt.
	if brush.Design == (musical.Design{}) && brush.Amount != 0 && len(vr.grassPatches) > 0 {
		target, radius := brush.Target, brush.Radius
		Callable.Defer(Callable.New(func() {
			vr.reprojectGrass(target, radius)
		}))
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

// Builtin terrain brush sentinels for the procedural:// convention used
// by BuiltinDesignProvider. These are the "builtin-aviary" items that
// appear in the "terrain" tab under ModeGeometry for the TerrainEditor.
const (
	BuiltinTerrainRaise      = "procedural://terrain/raise"
	BuiltinTerrainLower      = "procedural://terrain/lower"
	BuiltinTerrainRiver      = "procedural://terrain/river"
	BuiltinTerrainRiverErase = "procedural://terrain/river_erase"
)

// riverSlider / riverEraseSlider tag a Sculpt as a river paint or erase stroke.
// Both carry an empty Design and are handled by Reload's river layer (riverDepth
// + flow); the Slider also keeps them out of the dressing/water-level branches
// in TerrainEditor.Sculpt and out of the non-river ground accumulation.
const (
	riverSlider      = "river"
	riverEraseSlider = "river/erase"
)

// extendSlider tags the explicit "extend the world" mutation an arrow click
// emits. Target carries the new chunk's world-space center; TerrainEditor.Sculpt
// reveals exactly that tile on every client, so the visible world grows only
// through an observable, reproducible step (never silently from a brush
// spilling over an edge).
const extendSlider = "extend"

func isRiverSlider(s string) bool { return s == riverSlider || s == riverEraseSlider }

// waterFloorEps is the minimum carve depth (groundHeights-heights) below which
// a grid point is considered dry. Guards against float noise spawning slivers
// of water where no river was actually painted.
const waterFloorEps float32 = 0.02

// riverBankCollar is how many grid cells beyond the carved channel the water
// plane is extended onto the (un-dug) bank. The collar sits at the original
// ground height so the river laps flush onto its banks and connects to the
// neighbouring terrain down a slope, instead of ending in a clamped vertical
// edge (the "floating slab" look).
const riverBankCollar = 1

// waterSurfaceDrop sits the river surface slightly below the original ground
// so a small lip of bank shows above the waterline (rather than the water
// being exactly flush with where the terrain used to be).
const waterSurfaceDrop Float.X = 0.2

// riverDefaultDepth is the initial channel depth carved by the river brush,
// in world units below the original ground. Adjustable via the river-depth
// slider.
const riverDefaultDepth Float.X = 3.0

// terrainBrushDelta returns the signed delta to feed into the height brush
// for the given mouse button, according to the currently selected terrain
// brush tool. The "raise" tool makes the primary button (LMB) raise terrain;
// the "lower" tool makes the primary button lower terrain. The secondary
// button (RMB) always inverts the tool's direction.
func (tr *TerrainEditor) terrainBrushDelta(leftButton bool) Float.X {
	switch tr.TerrainBrush {
	case BuiltinTerrainRaise:
		if leftButton {
			return 2
		}
		return -2
	case BuiltinTerrainLower:
		if leftButton {
			return -2
		}
		return 2
	default:
		return 0
	}
}

type TerrainTile struct {
	StaticBody3D.Extension[TerrainTile] `gd:"AviaryTerrainTile"`

	brushEvents chan<- terrainBrushEvent

	Mesh MeshInstance3D.Instance
	// Water carries the water plane (surface 0) and the water-body
	// cross-section sides (surface 1); kept separate from Mesh so the
	// water and terrain geometry are never confused.
	Water       MeshInstance3D.Instance
	shader      ShaderMaterial.Instance
	side_shader ShaderMaterial.Instance

	// The same wave shader (water_shader) drives BOTH water surfaces (plane +
	// side walls) so the sides stay in sync with the plane; the per-vertex
	// terrain floor (CUSTOM0.r) clamps the water above the terrain.
	water_shader ShaderMaterial.Instance

	client    *Client
	editor    *TerrainEditor // back-pointer for shared mapper/albedos
	coord     tileCoord      // grid position; world center = coord * size
	generated bool
	// revealed reports whether this tile is an explicit part of the world.
	// A hidden (un-revealed) tile still holds + accumulates edits (brush
	// spill-over, replayed sculpts) but renders nothing, is not pickable, and
	// is treated as absent by its neighbours — so they keep their edge wall +
	// extend arrow. An explicit, observable "extend" mutation reveals it (see
	// reveal); the starter tile is revealed in Ready.
	revealed  bool
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

	// cached water geometry (plane + sides)
	vertices_water []Vector3.XYZ
	normals_water  []Vector3.XYZ
	uvs_water      []Vector2.XY

	vertices_water_side []Vector3.XYZ
	normals_water_side  []Vector3.XYZ
	uvs_water_side      []Vector2.XY

	// River state, indexed identically to heights on the (size+1)² grid
	// (idx = gx + gz*hm). These are persisted accumulators (like heights),
	// updated as river sculpts arrive and read by reloadWater:
	//   - groundHeights: the terrain height EXCLUDING river carves, i.e. the
	//     ground as it was before any river dug into it. The river's water
	//     surface sits here ("water level where the terrain used to be").
	//   - waterFlowX/Z: accumulated (unnormalised) flow direction in world XZ
	//     from each river stroke's Orient, weighted by the brush falloff;
	//     normalised at read time to drive the water shader's flow.
	// A grid point has river water where riverDepth > waterFloorEps (the channel
	// was dug below the original ground). riverDepth (>=0) is maintained with
	// paint-over/erase semantics (NOT accumulated) so repainting at a different
	// depth overwrites and the eraser digs it back out; the visible/collision
	// terrain is heights = groundHeights - riverDepth.
	groundHeights []float32
	riverDepth    []float32
	waterFlowX    []float32
	waterFlowZ    []float32
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
	// River accumulators parallel the heights grid. groundHeights starts as a
	// copy of heights so that, before any river is painted, the water surface
	// reference equals the terrain (no spurious river anywhere). Resize hands
	// us a resampled heights buffer; mirror it here.
	if len(tile.groundHeights) != hm*hm {
		tile.groundHeights = make([]float32, hm*hm)
		copy(tile.groundHeights, tile.heights)
		tile.riverDepth = make([]float32, hm*hm)
		tile.waterFlowX = make([]float32, hm*hm)
		tile.waterFlowZ = make([]float32, hm*hm)
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
	// Dedicated water MeshInstance child, positioned identically to Mesh so
	// its mesh-local coordinates line up exactly with the terrain geometry.
	if tile.Water == (MeshInstance3D.Instance{}) {
		tile.Water = MeshInstance3D.New()
		tile.Water.AsNode().SetName("Water")
		tile.AsNode().AddChild(tile.Water.AsNode())
	}
	tile.Water.AsNode3D().SetPosition(Vector3.New(-half, 0, -half))
	// Texture arrays are owned by the editor (shared across tiles).
	tile.reloadSides()
	tile.reloadWater()
	// Sync visibility + pickability to the reveal state (tiles start hidden;
	// applyRevealState also gates the water mesh on revealed && waterVisible).
	tile.applyRevealState()
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
		// sample_height_ground sums the pending NON-river height sculpts at a
		// grid point (raise/lower). Accumulated into groundHeights, it is the
		// terrain as if no river had ever been dug — the river's water-surface
		// reference ("where the terrain used to be"). River strokes are applied
		// separately to riverDepth below so they overwrite/erase rather than add.
		var sample_height_ground = func(x, y int) Float.X {
			pos := Vector3.Add(Vector3.XYZ{Float.X(x), 0, Float.X(y)}, offset)
			height := Float.X(0)
			for i := range tile.sculpts {
				sculpt := tile.sculpts[i]
				if sculpt.Design != (musical.Design{}) || isRiverSlider(sculpt.Slider) {
					continue
				}
				// A zero-radius stroke would make the falloff 0/0 = NaN at the
				// target vertex; that NaN accumulates into groundHeights (and
				// thus heights), and the GPU discards every triangle touching a
				// NaN vertex — a permanent hole in the mesh. Skip it, mirroring
				// the river path's `if r2 <= 0 { continue }` guard.
				if sculpt.Radius <= 0 {
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
		// Accumulate this batch's raise/lower into the original-ground field.
		for i := 0; i < hm*hm; i++ {
			tile.groundHeights[i] += float32(sample_height_ground(i%hm, i/hm))
		}
		// Apply this batch's river / river-erase strokes to the persisted river
		// depth + flow with PAINT-OVER semantics: within a stroke's disc each
		// value is lerped toward the stroke's target using the brush falloff as
		// the blend weight (1 at the centre, 0 at the rim). So repainting at a
		// different depth overwrites, and an erase stroke (target depth 0) digs
		// the channel back out. Applied in log order, so every client agrees.
		for si := range tile.sculpts {
			sculpt := tile.sculpts[si]
			if sculpt.Design != (musical.Design{}) || !isRiverSlider(sculpt.Slider) {
				continue
			}
			erase := sculpt.Slider == riverEraseSlider
			target := float32(0) // erase digs back toward zero depth
			if !erase {
				if d := float32(-sculpt.Amount); d > 0 {
					target = d // Amount is the negative carve depth
				}
			}
			fx := float32(Angle.Cos(sculpt.Orient))
			fz := float32(Angle.Sin(sculpt.Orient))
			r2 := sculpt.Radius * sculpt.Radius
			if r2 <= 0 {
				continue
			}
			for i := 0; i < hm*hm; i++ {
				pos := Vector3.Add(Vector3.XYZ{Float.X(i % hm), 0, Float.X(i / hm)}, offset)
				dx := pos.X - sculpt.Target.X
				dy := pos.Z - sculpt.Target.Z
				d2 := dx*dx + dy*dy
				if d2 > r2 {
					continue
				}
				w := float32(1 - d2/r2) // falloff / paint-over blend weight
				tile.riverDepth[i] += (target - tile.riverDepth[i]) * w
				if erase {
					tile.waterFlowX[i] -= tile.waterFlowX[i] * w
					tile.waterFlowZ[i] -= tile.waterFlowZ[i] * w
				} else {
					tile.waterFlowX[i] += (fx - tile.waterFlowX[i]) * w
					tile.waterFlowZ[i] += (fz - tile.waterFlowZ[i]) * w
				}
			}
		}
		// Visible/collision terrain is the original ground minus the river
		// channel; recomputed (not accumulated) so depth changes + erases apply.
		for i := 0; i < hm*hm; i++ {
			tile.heights[i] = tile.groundHeights[i] - tile.riverDepth[i]
		}
		inv := Float.X(1) / Float.X(n)
		update := func(index int, cell_x, cell_y int, x, y int) {
			tile.vertices[index].Y = tile.heights[x+y*hm]
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
		// If this batch changed terrain height (any empty-Design sculpt: raise,
		// lower, river or river-erase), keep placed items that are resting on
		// the ground sitting on the (new) ground. Floated objects (GizmoFloat)
		// keep their absolute Y. Use the editor-level HeightAt, which routes
		// each object to the tile it actually stands on via tileForWorld —
		// tile.HeightAt would clamp every object to THIS tile's local bounds
		// and snap items elsewhere to its edge height (≈0 on flat terrain),
		// which is the reset-to-0 bug.
		heightEdited := false
		for _, brush := range tile.sculpts {
			if brush.Design == (musical.Design{}) {
				heightEdited = true
				break
			}
		}
		if heightEdited {
			for id := range tile.client.object_to_entity {
				object, ok := id.Instance()
				if !ok {
					continue
				}
				pos := object.AsNode3D().GlobalPosition()
				terrainY := Float.X(0)
				if tile.editor != nil {
					terrainY = tile.editor.HeightAt(pos)
				} else {
					terrainY = tile.HeightAt(pos)
				}
				// Only auto-snap objects that are currently resting on (or very
				// near) the terrain. Intentionally floated objects (via
				// GizmoFloat) keep their absolute world Y offset on top of any
				// terrain changes.
				if Float.Abs(pos.Y-terrainY) < 0.25 {
					pos.Y = terrainY
					object.AsNode3D().SetGlobalPosition(pos)
				}
			}
		}
		tile.reloadSides()
		// Rebuild the water layer so it tracks terrain edits (the side walls
		// follow newly exposed/covered neighbour edges).
		tile.reloadWater()
		tile.sculpts = tile.sculpts[:0]
	}))
}

// hasNeighbour reports whether a REVEALED TerrainTile exists at the adjacent
// grid coord in the given direction. Used by reloadSides + spawnArrows to
// decide which vertical walls + extend arrows to emit: a hidden (un-revealed)
// neighbour counts as absent, so the tile keeps its edge wall + arrow there
// until that neighbour is explicitly revealed.
func (tile *TerrainTile) hasNeighbour(dir tileCoord) bool {
	if tile.editor == nil {
		return false
	}
	n, ok := tile.editor.tiles[tileCoord{tile.coord.X + dir.X, tile.coord.Z + dir.Z}]
	return ok && n.revealed
}

// reveal promotes a hidden tile into the world: it becomes visible + pickable,
// drops the edge walls it shares with now-adjacent revealed neighbours, takes
// over the extend arrows for its still-hidden sides, and clears the matching
// arrow on each revealed neighbour. Idempotent.
func (tile *TerrainTile) reveal() {
	if tile.revealed {
		return
	}
	tile.revealed = true
	tile.applyRevealState()
	// We now count as a neighbour: drop the shared wall + extend arrow on each
	// already-revealed neighbour and refresh its sides.
	for _, dir := range cardinalDirs {
		neighbour, ok := tile.editor.tiles[tileCoord{tile.coord.X + dir.X, tile.coord.Z + dir.Z}]
		if !ok || neighbour == tile || !neighbour.revealed {
			continue
		}
		neighbour.removeArrow(tileCoord{-dir.X, -dir.Z})
		neighbour.reloadSides()
	}
	// Recompute our own walls against the revealed neighbours and plant extend
	// arrows on the sides still facing a hidden/absent neighbour.
	tile.reloadSides()
	tile.spawnArrows()
}

// applyRevealState syncs the tile's render + pick + water state to whether it
// has been revealed. A hidden tile keeps its mesh + collision shape (so its
// data + edits survive) but renders nothing and is not pickable, so a brush
// spilling over an edge builds it up invisibly until an explicit extend.
func (tile *TerrainTile) applyRevealState() {
	tile.AsNode3D().SetVisible(tile.revealed)
	if tile.collision != (CollisionShape3D.Instance{}) {
		tile.collision.SetDisabled(!tile.revealed)
	}
	if tile.Water != (MeshInstance3D.Instance{}) {
		tile.Water.AsNode3D().SetVisible(tile.revealed && tile.editor != nil && tile.editor.waterVisible)
	}
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

// WaterSurfaceAt mirrors reloadWater's waterAt: where a river has been carved
// (riverDepth above the floor within the bank collar) the surface sits at the
// original ground minus the small lip; otherwise it is the global lake level.
// Used by the underwater post-process to place the waterline on rivers.
func (tile *TerrainTile) WaterSurfaceAt(pos Vector3.XYZ) Float.X {
	n := tile.size
	if n == 0 {
		n = terrainDefaultSize
	}
	hm := n + 1
	level := Float.X(0)
	if tile.editor != nil {
		level = tile.editor.WaterLevel
	}
	if len(tile.groundHeights) < hm*hm || len(tile.riverDepth) < hm*hm {
		return level
	}
	maxF := Float.X(n)
	local := Vector3.Sub(pos, tile.Mesh.AsNode3D().GlobalPosition())
	x := max(0.0, min(maxF, local.X))
	z := max(0.0, min(maxF, local.Z))
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
	// Water present if the nearest cell — or any neighbour within the bank
	// collar — has been carved into a channel (matches waterAt's dilation).
	present := false
	cx, cz := int(x+0.5), int(z+0.5)
	for dz := -riverBankCollar; dz <= riverBankCollar && !present; dz++ {
		for dx := -riverBankCollar; dx <= riverBankCollar; dx++ {
			gx, gz := cx+dx, cz+dz
			if gx < 0 || gz < 0 || gx >= hm || gz >= hm {
				continue
			}
			if tile.riverDepth[gx+gz*hm] > waterFloorEps {
				present = true
				break
			}
		}
	}
	if !present {
		return level
	}
	// Bilinear original ground, then the river surface (or the lake if higher).
	g00 := Float.X(tile.groundHeights[x0+z0*hm])
	g10 := Float.X(tile.groundHeights[x1+z0*hm])
	g01 := Float.X(tile.groundHeights[x0+z1*hm])
	g11 := Float.X(tile.groundHeights[x1+z1*hm])
	sx := x - Float.X(x0)
	sz := z - Float.X(z0)
	g0 := g00*(1-sx) + g10*sx
	g1 := g01*(1-sx) + g11*sx
	ground := g0*(1-sz) + g1*sz
	if level > ground {
		return level
	}
	return ground - waterSurfaceDrop
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
				// A left press that actually hit terrain geometry is the
				// only thing allowed to start a real paint/dress/height/river
				// stroke. This is what prevents clicks inside the 2D
				// design explorer from leaking into the world.
				if tile.editor.PaintActive || tile.editor.DressActive || tile.editor.riverBrushActive() {
					tile.editor.brushStrokeActive = true
				}
				deltaV := Float.X(0)
				if tile.editor.PaintActive || tile.editor.DressActive {
					deltaV = 2
				} else if tile.editor.client.ui.mode == ModeGeometry && tile.editor.TerrainBrush != "" {
					// Explicit terrain brush tool selected: the sign depends on
					// which brush ("raise" vs "lower") the user picked in the
					// terrain tab. LMB applies the tool's primary direction.
					deltaV = tile.editor.terrainBrushDelta(true)
				}
				select {
				case tile.brushEvents <- terrainBrushEvent{
					BrushTarget: pos,
					BrushDeltaV: deltaV,
				}:
				default:
				}
			} else {
				// Release clears any active stroke, even if the pointer
				// is currently over UI (the global release will also be
				// observed in client.Process as a belt-and-suspenders).
				tile.editor.brushStrokeActive = false
			}
		}
		if event.ButtonIndex() == Input.MouseButtonRight {
			if event.AsInputEvent().IsPressed() {
				deltaV := Float.X(0)
				if tile.editor.PaintActive {
					// RMB during paint is handled as cancel in UnhandledInput.
					// We still send a position update (deltaV 0) so the
					// brush target tracks if needed.
				} else if tile.editor.DressActive {
					// RMB during dressing is erase (negative density).
					deltaV = -2
				} else if tile.editor.client.ui.mode == ModeGeometry && tile.editor.TerrainBrush != "" {
					// RMB with an explicit terrain brush applies the inverse of
					// the selected tool's primary direction.
					deltaV = tile.editor.terrainBrushDelta(false)
				}
				select {
				case tile.brushEvents <- terrainBrushEvent{
					BrushTarget: pos,
					BrushDeltaV: deltaV,
				}:
				default:
				}
			}
		}
	} else if !Input.IsKeyPressed(Input.KeyShift) || Input.IsKeyPressed(Input.KeyCtrl) {
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

// spawnArrows creates an extend-the-world arrow on each cardinal side that does
// not have a revealed neighbour. Called when a tile is revealed; idempotent, so
// it skips sides that already carry an arrow.
func (tile *TerrainTile) spawnArrows() {
	for _, dir := range cardinalDirs {
		if tile.hasNeighbour(dir) {
			continue
		}
		if _, exists := tile.arrows[dir]; exists {
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
	coord := tileCoord{
		X: a.tile.coord.X + a.direction.X,
		Z: a.tile.coord.Z + a.direction.Z,
	}
	// Extending the world is a shared mutation, so route it through the space
	// rather than revealing the tile locally — that is what makes the new chunk
	// observable by every client and reproducible on replay (the local client
	// reveals it when the mutation loops back through Sculpt). Before a space
	// exists (e.g. not yet joined) fall back to a direct local reveal.
	ed := a.tile.editor
	if ed.client == nil || ed.client.space == nil {
		ed.revealTile(coord)
		return
	}
	ed.client.space.Sculpt(musical.Sculpt{
		Author: ed.client.id,
		Editor: "terrain",
		Slider: extendSlider,
		Target: Vector3.New(
			Float.X(coord.X*terrainDefaultSize),
			0,
			Float.X(coord.Z*terrainDefaultSize),
		),
		Commit: true,
	})
}

// applyWaterLevel sets the shared water surface level, pushes it to the water
// materials and rebuilds every tile's water plane + side walls so they sit at
// the new Y. Driven both locally (no space) and by incoming water-level sculpts.
func (vr *TerrainEditor) applyWaterLevel(level Float.X) {
	vr.WaterLevel = level
	// Push the base level to the single water shader (used by both the plane
	// and the side walls) so the in-shader clamp uses the new surface Y.
	if vr.water_shader != (ShaderMaterial.Instance{}) {
		vr.water_shader.SetShaderParameter("water_level", float64(level))
	}
	for _, tile := range vr.tiles {
		tile.reloadWater()
	}
}

// SetWaterVisible toggles the per-tile water meshes and remembers the state so
// tiles spawned later get the matching default (mirrors setArrowsVisible).
func (tr *TerrainEditor) SetWaterVisible(v bool) {
	tr.waterVisible = v
	for _, tile := range tr.tiles {
		if tile.Water != (MeshInstance3D.Instance{}) {
			// Hidden tiles never show water, even when the layer is on.
			tile.Water.AsNode3D().SetVisible(v && tile.revealed)
		}
	}
}

// reloadWater rebuilds the tile's water MeshInstance with two surfaces:
//   - surface 0: a flat n*n subdivided plane at y = editor.WaterLevel,
//   - surface 1: vertical cross-section walls on the exposed cardinal sides,
//     spanning from y = WaterLevel down to y = -2.0.
//
// The plane mirrors the terrain top's 6-verts-per-cell layout; the sides mirror
// reloadSides()'s exposed-side selection + winding exactly so the water body
// lines up with the terrain skirt. World-Y values are used directly (the water
// shader does not apply the buried.gdshader -2.2 offset).
//
// BOTH surfaces use the same wave shader (water.gdshader) so the side walls
// stay in sync with the plane, and BOTH carry a CUSTOM0 (RGBA-float) attribute
// whose .r channel is the world-space terrain floor at each vertex's XZ. The
// shader clamps the (waved) water Y to [terrain_floor, water_level], which
// makes the shoreline meet the terrain cleanly and turns the sides into a real
// water column that collapses to nothing where terrain rises above the water.
func (tile *TerrainTile) reloadWater() {
	if tile.Water == (MeshInstance3D.Instance{}) {
		return
	}
	n := tile.size
	if n == 0 {
		n = terrainDefaultSize
	}
	level := Float.X(0)
	if tile.editor != nil {
		level = tile.editor.WaterLevel
	}

	// hm is the heightmap stride: heights live on an (n+1)x(n+1) grid and the
	// floor at grid (gx,gz) is tile.heights[gx + gz*hm].
	hm := n + 1
	hasHeights := len(tile.heights) >= hm*hm

	// --- surface 0: the flat water plane -------------------------------------
	tile.vertices_water = make([]Vector3.XYZ, n*n*6)
	tile.normals_water = make([]Vector3.XYZ, n*n*6)
	tile.uvs_water = make([]Vector2.XY, n*n*6)
	// floors_water carries CUSTOM0 (RGBA-float, 4 per vertex); only .r is used,
	// holding the world-space terrain floor at this vertex's grid point. The
	// wave shader clamps the water above this value so the shoreline meets the
	// terrain cleanly. BOTH water surfaces must supply CUSTOM0 since the shader
	// reads it for every vertex — a missing attribute would read 0 and wrongly
	// clamp the water down to y=0.
	floors_water := make([]float32, n*n*6*4)
	inv := Float.X(1) / Float.X(n)
	hasRiver := hasHeights && len(tile.groundHeights) >= hm*hm && len(tile.riverDepth) >= hm*hm
	// waterAt returns the per-vertex water-surface Y and the normalised world
	// XZ flow direction at grid point (x,y). Where a river has carved below the
	// original ground (groundHeights-heights > eps) the surface sits at the
	// original ground ("where the terrain used to be") and flows; everywhere
	// else it falls back to the global lake level with no flow — so a project
	// with no rivers renders exactly as before.
	waterAt := func(x, y int) (surface Float.X, fx, fz float32) {
		surface = level
		if !hasRiver {
			return surface, 0, 0
		}
		idx := x + y*hm
		ground := tile.groundHeights[idx]
		// A point carries river water if it — or, so the water laps onto the
		// bank rather than ending in a clamped wall, any neighbour within
		// riverBankCollar cells — was carved below the original ground.
		present := tile.riverDepth[idx] > waterFloorEps
		for dz := -riverBankCollar; dz <= riverBankCollar && !present; dz++ {
			for dx := -riverBankCollar; dx <= riverBankCollar; dx++ {
				gx, gz := x+dx, y+dz
				if gx < 0 || gz < 0 || gx >= hm || gz >= hm {
					continue
				}
				if tile.riverDepth[gx+gz*hm] > waterFloorEps {
					present = true
					break
				}
			}
		}
		if !present {
			return surface, 0, 0
		}
		// Sit the surface at the ORIGINAL ground ("as if the terrain were still
		// there") so the river follows the slope and connects to its banks; the
		// shader clamps it to the carved floor, so the collar lies flush on the
		// dry bank while the channel itself fills with visible water.
		if float32(level) > ground {
			surface = level // global lake is higher than this bed; keep the lake
		} else {
			// Sit slightly below the original ground for a small bank lip.
			surface = Float.X(ground) - waterSurfaceDrop
		}
		nx, nz := tile.waterFlowX[idx], tile.waterFlowZ[idx]
		if mag := Float.Sqrt(Float.X(nx*nx + nz*nz)); mag > 1e-6 {
			fx, fz = nx/float32(mag), nz/float32(mag)
		}
		return surface, fx, fz
	}
	// slopeAlongFlow returns how steeply the water surface drops in the flow
	// direction at grid (x,y): a central-difference gradient of the surface
	// height (which follows groundHeights) projected onto the (already
	// normalised) flow vector. Cells are ~1 world unit, so this is rise/run
	// (a tangent); 0 on flat or off-river points. The shader uses it to speed
	// up the current and grow whitewater on steeper, faster stretches.
	slopeAlongFlow := func(x, y int, fx, fz float32) float32 {
		if !hasRiver || (fx == 0 && fz == 0) {
			return 0
		}
		xm, xp, ym, yp := x-1, x+1, y-1, y+1
		if xm < 0 {
			xm = 0
		}
		if xp >= hm {
			xp = hm - 1
		}
		if ym < 0 {
			ym = 0
		}
		if yp >= hm {
			yp = hm - 1
		}
		gradX := (tile.groundHeights[xp+y*hm] - tile.groundHeights[xm+y*hm]) / float32(xp-xm)
		gradZ := (tile.groundHeights[x+yp*hm] - tile.groundHeights[x+ym*hm]) / float32(yp-ym)
		// Flow runs downhill, so the surface drops along it: the downhill
		// component is the negated gradient·flow. Clamp away uphill noise.
		s := -(gradX*fx + gradZ*fz)
		if s < 0 {
			s = 0
		}
		return s
	}
	addw := func(index int, x, y int) {
		// Node is at local y=0, so mesh-local y == world Y. The waves/normal
		// maps key off world position, so a simple UV is fine.
		surface, fx, fz := waterAt(x, y)
		tile.vertices_water[index] = Vector3.XYZ{Float.X(x), surface, Float.X(y)}
		tile.normals_water[index] = Vector3.XYZ{0, 1, 0}
		tile.uvs_water[index] = Vector2.XY{Float.X(x) * inv, Float.X(y) * inv}
		var floor float32 = -2.0
		if hasHeights {
			floor = tile.heights[x+y*hm]
		}
		floors_water[index*4+0] = floor                        // .r = terrain floor (channel bottom)
		floors_water[index*4+1] = fx                           // .g = flow X (world, normalised)
		floors_water[index*4+2] = fz                           // .b = flow Z (world, normalised)
		floors_water[index*4+3] = slopeAlongFlow(x, y, fx, fz) // .a = downhill slope along flow
	}
	for x := 0; x < n; x++ {
		for y := 0; y < n; y++ {
			addw(6*(x+n*y)+0, x, y)     // top left
			addw(6*(x+n*y)+1, x+1, y)   // top right
			addw(6*(x+n*y)+2, x, y+1)   // bottom left
			addw(6*(x+n*y)+3, x+1, y)   // top right
			addw(6*(x+n*y)+4, x+1, y+1) // bottom right
			addw(6*(x+n*y)+5, x, y+1)   // bottom left
		}
	}
	mesh := ArrayMesh.New()
	plane := [Mesh.ArrayMax]any{
		Mesh.ArrayVertex:  tile.vertices_water,
		Mesh.ArrayTexUv:   tile.uvs_water,
		Mesh.ArrayNormal:  tile.normals_water,
		Mesh.ArrayCustom0: floors_water,
	}
	// CUSTOM0 is RGBA-float; declare that in the surface format exactly as
	// generateBase does for the terrain splat channels.
	mesh.MoreArgs().AddSurfaceFromArrays(Mesh.PrimitiveTriangles, plane[:], nil, nil,
		Mesh.ArrayFormatVertex|Mesh.ArrayFormatNormal|Mesh.ArrayFormatTexUv|
			Mesh.ArrayFormat(Mesh.ArrayCustomRgbaFloat)<<Mesh.ArrayFormatCustom0Shift,
	)

	// --- surface 1: the water-body cross-section sides -----------------------
	// Mirror reloadSides()'s exposed-side selection + winding exactly, but the
	// wall spans from y=level (top, tl/tr) down to y=-2.0 (bottom, bl/br); no
	// +2.2 offset (the water side shader does not apply the buried offset).
	tile_size := float32(1.0)
	sideNeighbourDirs := [4]tileCoord{
		{0, -1}, // South (Z fixed at 0)
		{0, 1},  // North (Z fixed at n)
		{-1, 0}, // West  (X fixed at 0)
		{1, 0},  // East  (X fixed at n)
	}
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
	var active []sideParam
	for i, sp := range sides {
		if !tile.hasNeighbour(sideNeighbourDirs[i]) {
			active = append(active, sp)
		}
	}
	sideVertCount := len(active) * n * 6
	if sideVertCount > 0 {
		if cap(tile.vertices_water_side) < sideVertCount {
			tile.vertices_water_side = make([]Vector3.XYZ, sideVertCount)
			tile.normals_water_side = make([]Vector3.XYZ, sideVertCount)
			tile.uvs_water_side = make([]Vector2.XY, sideVertCount)
		} else {
			tile.vertices_water_side = tile.vertices_water_side[:sideVertCount]
			tile.normals_water_side = tile.normals_water_side[:sideVertCount]
			tile.uvs_water_side = tile.uvs_water_side[:sideVertCount]
		}
		vertices_side := tile.vertices_water_side
		normals_side := tile.normals_water_side
		uvs_side := tile.uvs_water_side
		// floors_side carries CUSTOM0 (RGBA-float, 4 per vertex); .r = the
		// terrain floor at that wall vertex's edge grid point. The top edge
		// shares the plane edge's world XZ + floor, so the shader clamps both
		// identically and the wall stays connected to the plane; the wall
		// collapses where the terrain rises above the water.
		floors_side := make([]float32, sideVertCount*4)
		index_base := 0
		for _, sp := range active {
			for i := 0; i < n; i++ {
				pos_near := float32(i)
				pos_far := float32(i + 1)
				// Edge grid points (near = i, far = i+1) for this side.
				var gnx, gnz, gfx, gfz int
				if sp.isZFixed {
					gnx, gnz = i, sp.fixedIndex
					gfx, gfz = i+1, sp.fixedIndex
				} else {
					gnx, gnz = sp.fixedIndex, i
					gfx, gfz = sp.fixedIndex, i+1
				}
				// Terrain floor at the two edge grid points. Read exactly like
				// reloadSides()'s h_near/h_far but WITHOUT the +2.2 buried offset
				// (water uses raw world heights).
				var floorNear, floorFar float32 = -2.0, -2.0
				if hasHeights {
					floorNear = tile.heights[gnx+gnz*hm]
					floorFar = tile.heights[gfx+gfz*hm]
				}
				// Per-edge water surface (matches the plane edge so the wall
				// stays connected) and flow, so a river that reaches the tile
				// boundary raises the wall to its surface and flows along it.
				topNearS, fnx, fnz := waterAt(gnx, gnz)
				topFarS, ffx, ffz := waterAt(gfx, gfz)
				topNear := float32(topNearS)
				topFar := float32(topFarS)
				// Drop the wall bottom to the channel floor when it is dug below
				// the skirt bottom so the column always reaches the bed.
				bottom := min(float32(-2.0), min(floorNear, floorFar))
				var tl, tr, bl, br Vector3.XYZ
				if sp.isZFixed {
					tl = Vector3.XYZ{pos_near, topNear, sp.fixed}
					tr = Vector3.XYZ{pos_far, topFar, sp.fixed}
					bl = Vector3.XYZ{pos_near, bottom, sp.fixed}
					br = Vector3.XYZ{pos_far, bottom, sp.fixed}
				} else {
					tl = Vector3.XYZ{sp.fixed, topNear, pos_near}
					tr = Vector3.XYZ{sp.fixed, topFar, pos_far}
					bl = Vector3.XYZ{sp.fixed, bottom, pos_near}
					br = Vector3.XYZ{sp.fixed, bottom, pos_far}
				}
				var v1, v2 Vector3.XYZ
				if sp.flippedWinding {
					v1 = Vector3.Sub(tr, bl)
					v2 = Vector3.Sub(tl, bl)
				} else {
					v1 = Vector3.Sub(tl, bl)
					v2 = Vector3.Sub(tr, bl)
				}
				nrm := Vector3.Normalized(Vector3.Cross(v1, v2))
				// Triangle 1
				vertices_side[index_base+0] = bl
				normals_side[index_base+0] = nrm
				uvs_side[index_base+0] = Vector2.XY{float32(i) / tile_size, 0 / tile_size}
				if sp.flippedWinding {
					vertices_side[index_base+1] = tr
					normals_side[index_base+1] = nrm
					uvs_side[index_base+1] = Vector2.XY{float32(i+1) / tile_size, topFar / tile_size}
					vertices_side[index_base+2] = tl
					normals_side[index_base+2] = nrm
					uvs_side[index_base+2] = Vector2.XY{float32(i) / tile_size, topNear / tile_size}
				} else {
					vertices_side[index_base+1] = tl
					normals_side[index_base+1] = nrm
					uvs_side[index_base+1] = Vector2.XY{float32(i) / tile_size, topNear / tile_size}
					vertices_side[index_base+2] = tr
					normals_side[index_base+2] = nrm
					uvs_side[index_base+2] = Vector2.XY{float32(i+1) / tile_size, topFar / tile_size}
				}
				// Triangle 2
				vertices_side[index_base+3] = bl
				normals_side[index_base+3] = nrm
				uvs_side[index_base+3] = Vector2.XY{float32(i) / tile_size, 0 / tile_size}
				if sp.flippedWinding {
					vertices_side[index_base+4] = br
					normals_side[index_base+4] = nrm
					uvs_side[index_base+4] = Vector2.XY{float32(i+1) / tile_size, 0 / tile_size}
					vertices_side[index_base+5] = tr
					normals_side[index_base+5] = nrm
					uvs_side[index_base+5] = Vector2.XY{float32(i+1) / tile_size, topFar / tile_size}
				} else {
					vertices_side[index_base+4] = tr
					normals_side[index_base+4] = nrm
					uvs_side[index_base+4] = Vector2.XY{float32(i+1) / tile_size, topFar / tile_size}
					vertices_side[index_base+5] = br
					normals_side[index_base+5] = nrm
					uvs_side[index_base+5] = Vector2.XY{float32(i+1) / tile_size, 0 / tile_size}
				}
				// CUSTOM0 per emitted vertex, matching the vertex order above.
				// .r = terrain floor; .g/.b = flow X/Z. bl is always slot 0/3
				// (near). The winding swaps which of tl(near)/tr(far)/br(far)
				// fill slots 1,2,4,5.
				setSideCustom := func(slot int, floor, fx, fz float32) {
					floors_side[slot*4+0] = floor
					floors_side[slot*4+1] = fx
					floors_side[slot*4+2] = fz
				}
				if sp.flippedWinding {
					// [bl, tr, tl, bl, br, tr]
					setSideCustom(index_base+0, floorNear, fnx, fnz) // bl
					setSideCustom(index_base+1, floorFar, ffx, ffz)  // tr
					setSideCustom(index_base+2, floorNear, fnx, fnz) // tl
					setSideCustom(index_base+3, floorNear, fnx, fnz) // bl
					setSideCustom(index_base+4, floorFar, ffx, ffz)  // br
					setSideCustom(index_base+5, floorFar, ffx, ffz)  // tr
				} else {
					// [bl, tl, tr, bl, tr, br]
					setSideCustom(index_base+0, floorNear, fnx, fnz) // bl
					setSideCustom(index_base+1, floorNear, fnx, fnz) // tl
					setSideCustom(index_base+2, floorFar, ffx, ffz)  // tr
					setSideCustom(index_base+3, floorNear, fnx, fnz) // bl
					setSideCustom(index_base+4, floorFar, ffx, ffz)  // tr
					setSideCustom(index_base+5, floorFar, ffx, ffz)  // br
				}
				index_base += 6
			}
		}
		water_sides := [Mesh.ArrayMax]any{
			Mesh.ArrayVertex:  vertices_side,
			Mesh.ArrayNormal:  normals_side,
			Mesh.ArrayTexUv:   uvs_side,
			Mesh.ArrayCustom0: floors_side,
		}
		// Same RGBA-float CUSTOM0 declaration as the plane surface so the
		// shader reads the terrain floor for the wall vertices too.
		mesh.MoreArgs().AddSurfaceFromArrays(Mesh.PrimitiveTriangles, water_sides[:], nil, nil,
			Mesh.ArrayFormatVertex|Mesh.ArrayFormatNormal|Mesh.ArrayFormatTexUv|
				Mesh.ArrayFormat(Mesh.ArrayCustomRgbaFloat)<<Mesh.ArrayFormatCustom0Shift,
		)
	}

	tile.Water.SetMesh(mesh.AsMesh())
	if tile.water_shader != (ShaderMaterial.Instance{}) {
		// Both surfaces share the wave shader so the side walls stay in sync
		// with the plane. Push the current base level for the in-shader clamp.
		tile.water_shader.SetShaderParameter("water_level", float64(level))
		tile.Water.SetSurfaceOverrideMaterial(0, tile.water_shader.AsMaterial())
		if tile.Water.GetSurfaceOverrideMaterialCount() > 1 {
			tile.Water.SetSurfaceOverrideMaterial(1, tile.water_shader.AsMaterial())
		}
	}
}
