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
	// BrushPower is the height-sculpt strength one click applies with the
	// raise/lower tools — set by the GizmoPower slider in the gizmo toolbar.
	// A press applies ±BrushPower in a single shot (sign from the tool +
	// button); holding no longer keeps increasing the effect. Local-only:
	// the resulting amount rides in each height Sculpt, so remote clients
	// reshape the terrain identically.
	BrushPower  Float.X
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

	// Undo/redo histories for the editor-level mutations (those not held per
	// tile): dressing scatter/erase, water level, and tile extend/hide. Each
	// committed stroke is appended; a Revert sculpt toggles the matching entry's
	// reverted flag and the subsystem recomputes from the survivors. Per-tile
	// height/paint/river strokes live in TerrainTile.history instead.
	grassHistory  []editStroke
	waterHistory  []editStroke
	revealHistory []editStroke

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
	tile.hideArrows = make(map[tileCoord]*TerrainTileArrow)
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

// hideTile hides the revealed tile at coord — the inverse of revealTile, the
// explicit "retract the world" step. The tile's data + edits are preserved (it
// just stops rendering + becomes un-pickable, exactly like a never-revealed
// tile); a later extend restores it. Idempotent, and refuses to hide the last
// revealed tile so the world never becomes empty + unclickable.
func (tr *TerrainEditor) hideTile(coord tileCoord) {
	tile, ok := tr.tiles[coord]
	if !ok || !tile.revealed {
		return
	}
	if tr.revealedCount() <= 1 {
		return
	}
	tile.hide()
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

// tileRevealedAt reports whether the TerrainTile under the given world
// position has been explicitly revealed (and is therefore rendering).
// Used by grass instancing to omit individual instances that land on
// currently-hidden tiles.
func (tr *TerrainEditor) tileRevealedAt(pos Vector3.XYZ) bool {
	tile := tr.tileForWorld(pos)
	return tile != nil && tile.revealed
}

func (fe *TerrainEditor) Name() string { return "terrain" }

func (fe *TerrainEditor) EnableEditor() {
	// Terrain is a brush editor: it sculpts/paints the ground rather than
	// selecting & transforming placed entities, so it offers only the brush
	// tools. Declaring it here also makes the brush the active gizmo (the
	// neutral Point tool is not in the set, so SetGizmos switches off it) —
	// the brush-size slider anchors to GizmoBrush and the height-sculpt
	// power slider anchors to GizmoPower.
	fe.client.SetGizmos([]Gizmo{GizmoBrush, GizmoPower})
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
	fe.water_shader.
		SetShaderParameter("height", 0.0).
		SetShaderParameter("river_preview", 0.0)
	fe.BrushActive = false
	fe.PaintActive = false
	fe.DressActive = false
	fe.TerrainBrush = ""
	fe.brushStrokeActive = false
	fe.setArrowsVisible(false)
}

// setArrowsVisible toggles every existing chunk's extend + hide arrows and
// remembers the state so tiles spawned later get the matching default.
func (tr *TerrainEditor) setArrowsVisible(v bool) {
	tr.arrowsVisible = v
	for _, tile := range tr.tiles {
		for _, arrow := range tile.arrows {
			arrow.AsNode3D().SetVisible(v)
		}
		for _, arrow := range tile.hideArrows {
			arrow.AsNode3D().SetVisible(v)
		}
	}
}

// revealedCount reports how many tiles are currently part of the visible
// world. Used to refuse hiding the last revealed tile (which would leave the
// world empty + unclickable).
func (tr *TerrainEditor) revealedCount() int {
	n := 0
	for _, tile := range tr.tiles {
		if tile.revealed {
			n++
		}
	}
	return n
}

func (*TerrainEditor) Views() []string          { return nil }
func (*TerrainEditor) SwitchToView(view string) {}

func (fe *TerrainEditor) Tabs(mode Mode) []string {
	switch mode {
	case ModeGeometry:
		// The "terrain" tab provides the raise/lower/river height brushes as
		// builtin items. The per-brush sliders live on the gizmo toolbar: brush
		// size on GizmoBrush, and the active brush's strength on GizmoPower
		// (sculpt power for raise/lower, channel depth for the river tools).
		// water-level stays a tab — it is a property of the whole terrain, not
		// the active brush.
		return []string{
			"terrain",
			"streams",
			"lagoons",
			"editing/water_level",
		}
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
	if mode != ModeGeometry {
		return nil
	}
	switch tab {
	case "streams":
		return []BuiltinDesign{
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
	case "terrain":
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
		}
	default:
		return nil
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
		// The GizmoPower slider follows the active brush, so re-sync it to the
		// parameter this tool exposes (sculpt power vs river-channel depth).
		fe.refreshGizmoPowerSlider()
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
		// Brush radius is a local-only highlight control; not synced. Push it
		// to BOTH the top-surface and side-wall (buried) shaders so the brush
		// preview lifts the exposed wall vertices over the same disc. Without
		// the side-shader update the walls previewed with the stale initial
		// radius (too small) until the real sculpt arrived and rebuilt them.
		fe.BrushRadius = Float.X(value)
		fe.shader.SetShaderParameter("radius", fe.BrushRadius)
		fe.shader_buried.SetShaderParameter("radius", fe.BrushRadius)
		fe.water_shader.SetShaderParameter("radius", fe.BrushRadius)
	case "editing/power":
		// Height-sculpt strength: a local-only brush control (the resulting
		// amount is carried per-stroke in the Sculpt's Amount, so it still
		// reproduces on every client). Not itself synced.
		fe.BrushPower = Float.X(value)
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
		fe.client.commitSculpt(musical.Sculpt{
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
	case "editing/power":
		// value, min, max, step (height units applied per click)
		return float64(fe.BrushPower), 0.1, 10, 0.1
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

// brushPowerScrollStep is how much one mouse-wheel notch changes the terrain
// GizmoPower parameter when Ctrl is held (see Client.handleScroll).
const brushPowerScrollStep Float.X = 0.5

// GizmoPowerEditing returns the editing/* slider key the GizmoPower toolbar
// slider drives for the currently selected terrain brush: the channel depth
// for the river tools, otherwise the height-sculpt power (raise/lower, and the
// neutral default). Water level is deliberately NOT here — it is a terrain-wide
// property exposed as its own tab, not a per-brush parameter.
func (fe *TerrainEditor) GizmoPowerEditing() string {
	switch fe.TerrainBrush {
	case BuiltinTerrainRiver, BuiltinTerrainRiverErase:
		return "editing/river_depth"
	default:
		return "editing/power"
	}
}

// refreshGizmoPowerSlider re-syncs the GizmoPower toolbar slider to the
// parameter the active brush exposes (its range + current value), so switching
// terrain tools updates it. No-op unless the terrain editor is active in
// ModeGeometry (where the slider is shown).
func (fe *TerrainEditor) refreshGizmoPowerSlider() {
	if fe.client == nil || fe.client.ui == nil || fe.client.ui.CloudControl == nil {
		return
	}
	if fe.client.Editing != Editing.Terrain || fe.client.ui.mode != ModeGeometry {
		return
	}
	fe.client.ui.CloudControl.setPowerSliderVisible(true)
}

// NudgeGizmoPower changes the active brush's GizmoPower parameter (sculpt power
// or river-channel depth, per GizmoPowerEditing) by delta, clamped to that
// slider's configured range. It returns the new value so callers can sync the
// gizmo-toolbar slider. Used by the Ctrl+wheel shortcut.
func (fe *TerrainEditor) NudgeGizmoPower(delta Float.X) Float.X {
	editing := fe.GizmoPowerEditing()
	cur, min, max, _ := fe.SliderConfig(ModeGeometry, editing)
	v := Float.X(cur) + delta
	if v < Float.X(min) {
		v = Float.X(min)
	}
	if v > Float.X(max) {
		v = Float.X(max)
	}
	fe.SliderHandle(ModeGeometry, editing, float64(v), false)
	return v
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
		SetShaderParameter("foam_sampler", LoadSync[Texture2D.Instance]("res://terrain/water/Foam.png")).
		// Mirror the terrain shaders' brush-preview uniforms so the water tracks
		// the raise/lower height preview before it commits (see water.gdshader).
		SetShaderParameter("radius", 2.0).
		SetShaderParameter("height", 0.0).
		SetShaderParameter("river_preview", 0.0)

	// Default level -2 == skirt bottom == hidden under flat terrain.
	tr.WaterLevel = -2
	tr.water_shader.SetShaderParameter("water_level", float64(tr.WaterLevel))

	tr.BrushRadius = 2.0
	tr.BrushPower = 2.0
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
	tr.client.commitSculpt(musical.Sculpt{
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
	clearedBrush := false
	if tr.TerrainBrush != "" {
		tr.TerrainBrush = ""
		tr.BrushActive = false
		tr.BrushAmount = 0
		active = true
		clearedBrush = true
	}
	if active {
		// No paint, dress or height brush armed: hide the ring highlight.
		tr.shader.SetShaderParameter("brush_active", false)
		tr.shader_buried.SetShaderParameter("brush_active", false)
	}
	if clearedBrush {
		// The active brush drove the GizmoPower slider; with none selected it
		// falls back to the sculpt-power parameter.
		tr.refreshGizmoPowerSlider()
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
	tr.client.commitSculpt(musical.Sculpt{
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
	tr.client.commitSculpt(musical.Sculpt{
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
			vr.water_shader.SetShaderParameter("uplift", event.BrushTarget)
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
			} else if !vr.PaintActive && vr.client.ui.mode != ModeMaterial {
				// Height sculpt input is only accepted when a terrain brush
				// tool has been explicitly selected in ModeGeometry, or we
				// are already mid-stroke (BrushActive). A press carries the
				// signed GizmoPower amount in BrushDeltaV; apply it in one
				// shot. Holding no longer keeps increasing the effect — a
				// motion event (BrushDeltaV 0) only moves the brush preview.
				if vr.TerrainBrush != "" || vr.BrushActive {
					vr.BrushTarget = event.BrushTarget
					if event.BrushDeltaV != 0 {
						vr.BrushAmount = event.BrushDeltaV
						vr.BrushActive = true
					}
				}
			}
			continue
		default:
		}
		break
	}
	if !vr.PaintActive && vr.client.ui.mode == ModeGeometry && vr.TerrainBrush != "" {
		// Height-sculpt preview: show the terrain shifted by the brush amount
		// before the user clicks to confirm it. While a press is held, preview
		// the actual pressed amount (BrushAmount); otherwise preview the tool's
		// primary (LMB) GizmoPower amount so the shift is visible on hover.
		// terrainBrushDelta is 0 for non-height tools (e.g. the river brush),
		// so those show no height preview.
		preview := vr.BrushAmount
		if !vr.BrushActive {
			preview = vr.terrainBrushDelta(true)
		}
		// The river brush isn't a height bump (terrainBrushDelta is 0 for it), so
		// without this it previewed nothing. Preview its channel as a negative
		// carve (height) plus a still-water fill at the cursor (river_preview, the
		// channel depth) so the river's water shows before the live stroke commits.
		riverPreview := Float.X(0)
		if vr.TerrainBrush == BuiltinTerrainRiver {
			depth := vr.BrushRiverDepth
			if depth <= 0 {
				depth = riverDefaultDepth
			}
			preview = -depth
			riverPreview = depth
		}
		vr.shader.SetShaderParameter("height", preview)
		vr.shader_buried.SetShaderParameter("height", preview)
		vr.water_shader.SetShaderParameter("height", preview)
		vr.water_shader.SetShaderParameter("river_preview", riverPreview)
	} else {
		vr.BrushAmount = 0.0
		vr.shader.SetShaderParameter("height", 0.0)
		vr.shader_buried.SetShaderParameter("height", 0.0)
		vr.water_shader.SetShaderParameter("height", 0.0)
		vr.water_shader.SetShaderParameter("river_preview", 0.0)
	}
	vr.retryPendingGrass()
}

func (vr *TerrainEditor) Sculpt(brush musical.Sculpt) error {
	// A Revert sculpt carries the (Author, Timing) identity of a previously
	// committed stroke; undo/redo toggle that stroke off/on and recompute the
	// affected subsystem from the survivors. It carries the original routing
	// fields, so dispatch it by the same keys the forward stroke used.
	if brush.Revert {
		vr.revertSculpt(brush)
		return nil
	}
	// Seed the local Timing counter from our own replayed strokes so a fresh
	// session never reissues an identity an undo could later collide with.
	if brush.Commit && vr.client != nil && brush.Author == vr.client.id {
		vr.client.noteTiming(brush.Timing)
	}
	// Water-level slider sculpts are routed through the terrain editor but
	// are not height/paint/dressing edits, so handle them up front.
	if brush.Slider == "editing/water_level" {
		vr.applyWaterLevel(Float.X(brush.Amount))
		if brush.Commit {
			vr.waterHistory = append(vr.waterHistory, editStroke{brush: brush})
		}
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
		if brush.Commit {
			vr.revealHistory = append(vr.revealHistory, editStroke{brush: brush})
		}
		return nil
	}
	// "Retract the world" hides a tile — the inverse of extend, and likewise
	// the ONLY way the visible grid shrinks. Routed through the log for the
	// same reason: every client + replay hides the same chunk. The tile's data
	// survives (it just stops rendering), so a later extend restores it.
	if brush.Editor == "terrain" && brush.Slider == hideSlider {
		vr.hideTile(vr.coordForWorld(brush.Target))
		if brush.Commit {
			vr.revealHistory = append(vr.revealHistory, editStroke{brush: brush})
		}
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
		if brush.Commit {
			vr.grassHistory = append(vr.grassHistory, editStroke{brush: brush})
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

// editStroke is one committed editor-level mutation (dressing / water level /
// extend-hide) retained for undo, mirroring tileStroke for the per-tile ops.
type editStroke struct {
	brush    musical.Sculpt
	reverted bool
}

// revertEdit toggles the reverted flag of the entry in hist matching
// (author, timing) and reports whether one was found. The slice header is passed
// by value but its elements share the backing array, so the toggle is visible to
// the caller.
func revertEdit(hist []editStroke, author musical.Author, timing musical.Timing) bool {
	for i := range hist {
		if hist[i].brush.Author == author && hist[i].brush.Timing == timing {
			hist[i].reverted = !hist[i].reverted
			return true
		}
	}
	return false
}

// revertSculpt applies a Revert sculpt: it routes by the same keys the forward
// stroke used, toggles the matching committed stroke's reverted flag in the
// subsystem that owns it, and recomputes that subsystem from the survivors. Undo
// and redo emit the identical Revert (a toggle), so replaying the persisted
// Revert log reproduces the final state on any client.
func (vr *TerrainEditor) revertSculpt(brush musical.Sculpt) {
	switch {
	case brush.Slider == "editing/water_level":
		if revertEdit(vr.waterHistory, brush.Author, brush.Timing) {
			vr.recomputeWater()
		}
	case brush.Editor == "terrain" && (brush.Slider == extendSlider || brush.Slider == hideSlider):
		if revertEdit(vr.revealHistory, brush.Author, brush.Timing) {
			vr.recomputeReveal()
		}
	case brush.Slider != "" && brush.Design != (musical.Design{}):
		if revertEdit(vr.grassHistory, brush.Author, brush.Timing) {
			vr.recomputeGrass()
		}
	default:
		// Tile op (height / paint / river): toggle the stroke in every tile it
		// touched and recompute those tiles from their surviving history.
		reverted := false
		for _, tile := range vr.tilesIntersecting(brush.Target, brush.Radius) {
			if tile.revert(brush.Author, brush.Timing) {
				tile.recompute()
				reverted = true
			}
		}
		// A height change moves grass; re-plant it after the tiles recompute
		// (deferred so the new heights are in place), mirroring the forward path.
		if reverted && len(vr.grassPatches) > 0 {
			target, radius := brush.Target, brush.Radius
			Callable.Defer(Callable.New(func() {
				vr.reprojectGrass(target, radius)
			}))
		}
	}
}

// recomputeWater re-derives the water level from the surviving water-level
// strokes (last writer wins; default −2 — the Ready baseline).
func (vr *TerrainEditor) recomputeWater() {
	level := Float.X(-2)
	for i := range vr.waterHistory {
		if !vr.waterHistory[i].reverted {
			level = Float.X(vr.waterHistory[i].brush.Amount)
		}
	}
	vr.applyWaterLevel(level)
}

// recomputeReveal returns the grid to the starter baseline and replays the
// surviving extend/hide strokes in commit order.
func (vr *TerrainEditor) recomputeReveal() {
	vr.resetReveal()
	for i := range vr.revealHistory {
		e := vr.revealHistory[i]
		if e.reverted {
			continue
		}
		coord := vr.coordForWorld(e.brush.Target)
		if e.brush.Slider == hideSlider {
			vr.hideTile(coord)
		} else {
			vr.revealTile(coord)
		}
	}
}

// resetReveal returns the visible grid to the baseline — only the starter tile
// {0,0} revealed (see Ready). recomputeReveal then replays the active log.
// Coords are collected before hiding so a tile.hide() that touches neighbours
// can't disturb the map mid-range.
func (vr *TerrainEditor) resetReveal() {
	var toHide []tileCoord
	for coord, tile := range vr.tiles {
		if coord != (tileCoord{0, 0}) && tile.revealed {
			toHide = append(toHide, coord)
		}
	}
	for _, coord := range toHide {
		if tile, ok := vr.tiles[coord]; ok {
			tile.hide()
		}
	}
	vr.revealTile(tileCoord{0, 0})
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
				tr.client.commitSculpt(musical.Sculpt{
					Author: tr.client.id,
					Target: tr.BrushTarget,
					Radius: tr.BrushRadius,
					Amount: tr.BrushAmount,
					Commit: true,
				})
			}
			tr.BrushAmount = 0.0
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

// worldFloorY is the single Y at which the world bottoms out: the terrain skirt
// and the water-body walls both stop here, and the terrain top is clamped to it
// in terrain.gdshader (max(VERTEX.y, -2.0)). A river can carve heights below it,
// but nothing renders past it — so clamping the skirt + water column to this
// floor keeps them aligned and stops the water hanging in the void below the
// terrain where a channel is dug to the minimum.
const worldFloorY float32 = -2.0

// Builtin terrain brush sentinels for the procedural:// convention used
// by BuiltinDesignProvider. These are the "builtin-aviary" items that
// appear in the "terrain" tab under ModeGeometry for the TerrainEditor.
const (
	BuiltinTerrainRaise      = "procedural://terrain/raise"
	BuiltinTerrainLower      = "procedural://terrain/lower"
	BuiltinTerrainRiver      = "procedural://terrain/river"
	BuiltinTerrainRiverErase = "procedural://terrain/river_erase"
)

// extendSlider tags the explicit "extend the world" mutation an arrow click
// emits. Target carries the new chunk's world-space center; TerrainEditor.Sculpt
// reveals exactly that tile on every client, so the visible world grows only
// through an observable, reproducible step (never silently from a brush
// spilling over an edge).
const extendSlider = "extend"

// hideSlider tags the explicit "retract the world" mutation a red hide-arrow
// click emits — the inverse of extendSlider. Target carries the world-space
// center of the chunk to hide; TerrainEditor.Sculpt hides exactly that tile on
// every client, so the visible world shrinks only through an observable,
// reproducible step (the tile's data + edits are preserved, so a later extend
// restores it seamlessly).
const hideSlider = "hide"

// terrainBrushDelta returns the signed height amount one click applies for
// the given mouse button, according to the currently selected terrain brush
// tool and the GizmoPower slider. The "raise" tool makes the primary button
// (LMB) raise terrain; the "lower" tool makes the primary button lower it.
// The secondary button (RMB) always inverts the tool's direction. The
// magnitude is the GizmoPower strength (BrushPower), applied in one shot.
func (tr *TerrainEditor) terrainBrushDelta(leftButton bool) Float.X {
	switch tr.TerrainBrush {
	case BuiltinTerrainRaise:
		if leftButton {
			return tr.BrushPower
		}
		return -tr.BrushPower
	case BuiltinTerrainLower:
		if leftButton {
			return -tr.BrushPower
		}
		return tr.BrushPower
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
	// fullReload, when set before the deferred rebuild runs, makes it reset the
	// accumulators and replay the active history (recompute) rather than just
	// accumulating the pending batch. Set by recompute(); consumed once per frame.
	fullReload bool
	// sculpts is the pending batch: committed strokes that have arrived but not
	// yet been folded into the persisted accumulators. Cleared each rebuild.
	sculpts []musical.Sculpt
	// history is the full ordered log of committed strokes that touch THIS tile
	// (height/paint/river), each flagged reverted or not. Unlike sculpts it is
	// never cleared — it is what recompute() replays to restore exact state when
	// a stroke is reverted (undo) or restored (redo). Paint + river are
	// destructive paint-over, so they can only be undone by replaying what
	// survives, not by an arithmetic inverse.
	history []tileStroke

	// arrows tracks the "extend the world" markers for the four
	// cardinal sides that don't yet have a neighbour. Keyed by the
	// unit direction (e.g. (1,0) for the +X side).
	arrows map[tileCoord]*TerrainTileArrow

	// hideArrows tracks the smaller red "retract the world" markers that
	// sit beside each extend arrow but point the opposite way (inward).
	// Clicking one hides this tile. Keyed identically to arrows so the two
	// are spawned + removed together.
	hideArrows map[tileCoord]*TerrainTileArrow

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

// tileStroke is one committed terrain stroke (height/paint/river) retained for
// undo. reverted marks it as currently undone; recompute skips reverted strokes
// when re-deriving the tile, and a redo clears the flag again. The brush keeps
// its stamped (Author, Timing) identity so a Revert sculpt can find it.
type tileStroke struct {
	brush    musical.Sculpt
	reverted bool
}

func (tile *TerrainTile) Sculpt(brush musical.Sculpt) {
	// Retain the committed stroke for undo (recompute replays the survivors) and
	// queue it in the pending batch for the incremental forward rebuild.
	tile.history = append(tile.history, tileStroke{brush: brush})
	tile.sculpts = append(tile.sculpts, brush)
	tile.Reload()
}

// revert toggles the reverted flag of the history stroke with the given
// (Author, Timing) identity and returns true if a match was found. The caller
// recompute()s the tile afterwards so the change takes effect. Toggling (rather
// than setting) lets a single Revert sculpt express both undo and redo: each
// Revert flips the stroke, and replaying the persisted Revert log reproduces the
// final parity on a freshly joined client.
func (tile *TerrainTile) revert(author musical.Author, timing musical.Timing) bool {
	for i := range tile.history {
		if tile.history[i].brush.Author == author && tile.history[i].brush.Timing == timing {
			tile.history[i].reverted = !tile.history[i].reverted
			return true
		}
	}
	return false
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

// Reload folds any pending sculpt batch into the tile — the fast, incremental
// forward path taken on every edit.
func (tile *TerrainTile) Reload() { tile.scheduleReload(false) }

// recompute re-derives the tile from scratch over the non-reverted strokes in
// its history. Used on undo/redo: a Revert sculpt toggles a stroke's reverted
// flag and the tile rebuilds its exact state from the survivors. Paint + river
// are destructive paint-over, so they can only be reverted by replay, not by an
// arithmetic inverse.
func (tile *TerrainTile) recompute() { tile.scheduleReload(true) }

// activeStrokes returns the brushes of every non-reverted history stroke, in
// commit order — the input to a full recompute.
func (tile *TerrainTile) activeStrokes() []musical.Sculpt {
	out := make([]musical.Sculpt, 0, len(tile.history))
	for i := range tile.history {
		if !tile.history[i].reverted {
			out = append(out, tile.history[i].brush)
		}
	}
	return out
}

// resetAccumulators zeroes the persisted terrain/river/paint fields so a
// recompute can replay the active history onto a clean slate.
func (tile *TerrainTile) resetAccumulators() {
	for i := range tile.groundHeights {
		tile.groundHeights[i] = 0
		tile.riverDepth[i] = 0
		tile.waterFlowX[i] = 0
		tile.waterFlowZ[i] = 0
	}
	for i := range tile.textures {
		tile.textures[i] = 0
	}
}

// scheduleReload defers a single end-of-frame rebuild, coalescing repeat
// requests via the reloading guard. If anything this frame asked for a full
// rebuild (recompute), the deferred body resets the accumulators and replays the
// active history (a superset of the pending batch); otherwise it accumulates
// only the pending batch incrementally.
func (tile *TerrainTile) scheduleReload(full bool) {
	if !tile.generated {
		tile.generateBase()
	}
	if full {
		tile.fullReload = true
	}
	if tile.reloading {
		return // we only want to reload once per frame.
	}
	tile.reloading = true
	Callable.Defer(Callable.New(func() {
		tile.reloading = false
		// Forward edits accumulate just the pending batch; a recompute resets the
		// accumulators and replays the full active history so reverted strokes
		// leave no trace and survivors are reproduced exactly.
		reset := tile.fullReload
		tile.fullReload = false
		strokes := tile.sculpts
		if reset {
			strokes = tile.activeStrokes()
			tile.resetAccumulators()
		}
		// Ensure every paint Design in the strokes has a
		// layer index in the editor's shared texture array.
		if tile.editor != nil {
			for _, sculpt := range strokes {
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
			for i := len(strokes) - 1; i >= 0; i-- {
				sculpt := strokes[i]
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
		// Accumulate each non-river height stroke (raise/lower) into the
		// original-ground field — the terrain as if no river had ever been dug
		// (the river's water-surface reference "where the terrain used to be").
		// River strokes are applied separately to riverDepth below so they
		// overwrite/erase rather than add.
		//
		// The -2 floor is applied PER STROKE (not to an accumulated per-batch sum)
		// so reverting one stroke removes exactly the contribution it added, and a
		// from-scratch recompute over the surviving strokes matches the
		// incremental forward path stroke for stroke.
		for si := range strokes {
			sculpt := strokes[si]
			if sculpt.Design != (musical.Design{}) || isRiverSlider(sculpt.Slider) {
				continue
			}
			// A zero-radius stroke would make the falloff 0/0 = NaN at the target
			// vertex; that NaN accumulates into groundHeights (and thus heights),
			// and the GPU discards every triangle touching a NaN vertex — a
			// permanent hole in the mesh. Skip it, mirroring the river path's
			// `if r2 <= 0 { continue }` guard.
			if sculpt.Radius <= 0 {
				continue
			}
			r2 := sculpt.Radius * sculpt.Radius
			for i := 0; i < hm*hm; i++ {
				pos := Vector3.Add(Vector3.XYZ{Float.X(i % hm), 0, Float.X(i / hm)}, offset)
				dx := pos.X - sculpt.Target.X
				dy := pos.Z - sculpt.Target.Z
				d2 := dx*dx + dy*dy
				if d2 > r2 {
					continue
				}
				tile.groundHeights[i] += float32(max(-2, sculpt.Amount*(1-d2/r2)))
			}
		}
		// Apply this batch's river / river-erase strokes to the persisted river
		// depth + flow with PAINT-OVER semantics: within a stroke's disc each
		// value is lerped toward the stroke's target using the brush falloff as
		// the blend weight (1 at the centre, 0 at the rim). So repainting at a
		// different depth overwrites, and an erase stroke (target depth 0) digs
		// the channel back out. Applied in log order, so every client agrees.
		for si := range strokes {
			sculpt := strokes[si]
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
		for _, brush := range strokes {
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
	// already-revealed neighbour and refresh BOTH its terrain skirt and its
	// water sides. Both select exposed edges via exposedSides(), so the water
	// wall on the now-internal edge must be rebuilt too — otherwise it lingers
	// (the old implicit-creation path hid this because the neighbour was also
	// sculpted, which triggered a full Reload).
	for _, dir := range cardinalDirs {
		neighbour, ok := tile.editor.tiles[tileCoord{tile.coord.X + dir.X, tile.coord.Z + dir.Z}]
		if !ok || neighbour == tile || !neighbour.revealed {
			continue
		}
		neighbour.removeArrow(tileCoord{-dir.X, -dir.Z})
		neighbour.reloadSides()
		neighbour.reloadWater()
	}
	// Recompute our own terrain + water walls against the revealed neighbours
	// and plant extend arrows on the sides still facing a hidden/absent one.
	tile.reloadSides()
	tile.reloadWater()
	tile.spawnArrows()

	// Refresh grass instancing so that individual MultiMesh instances
	// whose terrain tile is now revealed become visible (and vice-versa
	// for hides on other tiles).
	if tile.editor != nil {
		tile.editor.refreshGrassVisibility()
	}
}

// hide retracts a revealed tile back out of the world — the inverse of reveal.
// It becomes invisible + un-pickable (its data survives, exactly like a
// never-revealed tile), drops all of its own extend + hide arrows, and re-walls
// + re-arrows each revealed neighbour so the edge it used to seal is exposed
// again. Idempotent.
func (tile *TerrainTile) hide() {
	if !tile.revealed {
		return
	}
	tile.revealed = false
	tile.applyRevealState()
	// A hidden tile shows no markers: drop our own extend + hide arrows (each
	// removeArrow clears both for that side).
	for dir := range tile.arrows {
		tile.removeArrow(dir)
	}
	// Each revealed neighbour now faces an absent tile again: rebuild its
	// terrain skirt + water walls on the newly-exposed edge and re-plant the
	// extend (+ hide) arrow there.
	for _, dir := range cardinalDirs {
		neighbour, ok := tile.editor.tiles[tileCoord{tile.coord.X + dir.X, tile.coord.Z + dir.Z}]
		if !ok || neighbour == tile || !neighbour.revealed {
			continue
		}
		neighbour.reloadSides()
		neighbour.reloadWater()
		neighbour.spawnArrows()
	}

	// Refresh grass instancing so that individual MultiMesh instances
	// on this tile are removed from the instancing (and instances on
	// newly-revealed neighbours are added).
	if tile.editor != nil {
		tile.editor.refreshGrassVisibility()
	}
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

// sideParam describes one of a tile's four cardinal edges for side-wall
// generation: whether Z is the fixed axis, the fixed coordinate value and its
// grid index, and the triangle winding. Shared by the terrain skirt
// (reloadSides) and the water-body walls (reloadWater) so the two stay aligned.
type sideParam struct {
	isZFixed       bool
	fixed          float32
	fixedIndex     int
	flippedWinding bool
}

// exposedSides returns the cardinal edges with no revealed neighbour, in a
// fixed order (South, North, West, East). Both the terrain skirt and the water
// body emit walls only on these edges, so the seam between two revealed tiles
// is never walled.
func (tile *TerrainTile) exposedSides() []sideParam {
	n := tile.size
	neighbourDirs := [4]tileCoord{
		{0, -1}, // South (Z fixed at 0)
		{0, 1},  // North (Z fixed at n)
		{-1, 0}, // West  (X fixed at 0)
		{1, 0},  // East  (X fixed at n)
	}
	sides := [4]sideParam{
		{true, 0, 0, true},           // South
		{true, float32(n), n, false}, // North
		{false, 0, 0, false},         // West
		{false, float32(n), n, true}, // East
	}
	var active []sideParam
	for i, sp := range sides {
		if !tile.hasNeighbour(neighbourDirs[i]) {
			active = append(active, sp)
		}
	}
	return active
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

	// Sides mesh data
	index_base := 0

	active := tile.exposedSides()

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
	for _, sp := range active {
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
			// Clamp the skirt top to the world floor so a deeply-carved river bed
			// (heights can fall below it) can't invert the wall; matches the
			// terrain top's max(VERTEX.y, worldFloorY) clamp in terrain.gdshader.
			h_near = max(worldFloorY, h_near) + 2.2
			h_far = max(worldFloorY, h_far) + 2.2
			pos_near := float32(i)
			pos_far := float32(i + 1)
			var tl, tr, bl, br Vector3.XYZ
			if sp.isZFixed {
				tl = Vector3.XYZ{pos_near, h_near, sp.fixed}
				tr = Vector3.XYZ{pos_far, h_far, sp.fixed}
				bl = Vector3.XYZ{pos_near, worldFloorY, sp.fixed}
				br = Vector3.XYZ{pos_far, worldFloorY, sp.fixed}
			} else {
				tl = Vector3.XYZ{sp.fixed, h_near, pos_near}
				tr = Vector3.XYZ{sp.fixed, h_far, pos_far}
				bl = Vector3.XYZ{sp.fixed, worldFloorY, pos_near}
				br = Vector3.XYZ{sp.fixed, worldFloorY, pos_far}
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

// addArrow plants the pair of markers on `dir`: the white "extend the world"
// arrow pointing outward, and beside it the smaller red "retract the world"
// arrow pointing back inward (clicking it hides this tile).
func (tile *TerrainTile) addArrow(dir tileCoord) {
	tile.spawnArrow(dir, false)
	tile.spawnArrow(dir, true)
}

// spawnArrow builds one marker on `dir`. When hide is false it is the white
// extend arrow centred on the open edge; when true it is the smaller red hide
// arrow, offset to one side along the edge and yawed 180° so it points inward.
func (tile *TerrainTile) spawnArrow(dir tileCoord, hide bool) {
	arrow := new(TerrainTileArrow)
	arrow.tile = tile
	arrow.direction = dir
	arrow.hide = hide
	half := Float.X(terrainDefaultSize) / 2
	// Both markers sit just past the open edge, sunk below ground level so they
	// read as rim markers tucked under the edge rather than floating up high.
	// The hide arrow is shifted sideways (perpendicular to dir) so it sits
	// beside the extend arrow instead of overlapping it.
	var perp Float.X
	if hide {
		perp = 2
	}
	arrow_pos := Vector3.New(
		Float.X(dir.X)*(half+1)+Float.X(dir.Z)*perp,
		-1.5,
		Float.X(dir.Z)*(half+1)+Float.X(dir.X)*perp,
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
	if hide {
		yaw += Angle.Pi // point back inward, opposite the extend arrow
	}
	arrow.AsNode3D().Rotate(Vector3.XYZ{0, 1, 0}, yaw)
	if hide {
		arrow.AsNode3D().SetScale(Vector3.New(0.6, 0.6, 0.6))
	}
	arrow.AsNode3D().SetVisible(tile.editor.arrowsVisible)
	if hide {
		tile.hideArrows[dir] = arrow
	} else {
		tile.arrows[dir] = arrow
	}
}

// removeArrow frees both markers on `dir` (the extend arrow and its paired hide
// arrow) and forgets them. Safe to call for a side that has neither.
func (tile *TerrainTile) removeArrow(dir tileCoord) {
	if arrow, ok := tile.arrows[dir]; ok {
		arrow.AsNode().QueueFree()
		delete(tile.arrows, dir)
	}
	if arrow, ok := tile.hideArrows[dir]; ok {
		arrow.AsNode().QueueFree()
		delete(tile.hideArrows, dir)
	}
}

// TerrainTileArrow is a click marker spawned by a tile on each cardinal side
// that doesn't yet have a neighbour. It comes in two flavours (see hide): the
// white outward arrow extends the world (reveals the adjacent chunk), and the
// smaller red inward arrow beside it retracts the world (hides this tile).
type TerrainTileArrow struct {
	StaticBody3D.Extension[TerrainTileArrow] `gd:"AviaryTerrainTileArrow"`

	tile      *TerrainTile
	direction tileCoord
	// hide flips this marker from a white outward "extend" arrow into a red
	// inward "retract" arrow: clicking it hides tile rather than revealing the
	// neighbour in direction.
	hide bool
}

func (a *TerrainTileArrow) Ready() {
	colour := Color.RGBA{R: 1, G: 1, B: 1, A: 1}
	if a.hide {
		colour = Color.RGBA{R: 0.9, G: 0.1, B: 0.1, A: 1}
	}
	material := StandardMaterial3D.New().AsBaseMaterial3D().
		SetAlbedoColor(colour)

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
	ed := a.tile.editor
	// The red hide arrow retracts the tile it sits on; the white extend arrow
	// reveals the neighbour in `direction`. Both are shared mutations, so route
	// them through the space rather than acting locally — that is what makes the
	// change observable by every client and reproducible on replay (the local
	// client applies it when the mutation loops back through Sculpt). Before a
	// space exists (e.g. not yet joined) fall back to a direct local apply.
	slider, coord := extendSlider, tileCoord{
		X: a.tile.coord.X + a.direction.X,
		Z: a.tile.coord.Z + a.direction.Z,
	}
	if a.hide {
		// Don't bother emitting a hide that would empty the world; hideTile
		// would refuse it anyway.
		if ed.revealedCount() <= 1 {
			return
		}
		slider, coord = hideSlider, a.tile.coord
	}
	if ed.client == nil || ed.client.space == nil {
		if a.hide {
			ed.hideTile(coord)
		} else {
			ed.revealTile(coord)
		}
		return
	}
	ed.client.commitSculpt(musical.Sculpt{
		Author: ed.client.id,
		Editor: "terrain",
		Slider: slider,
		Target: Vector3.New(
			Float.X(coord.X*terrainDefaultSize),
			0,
			Float.X(coord.Z*terrainDefaultSize),
		),
		Commit: true,
	})
}
