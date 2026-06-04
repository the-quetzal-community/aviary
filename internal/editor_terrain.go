package internal

import (
	"path"
	"slices"
	"sort"
	"strings"
	"time"

	"graphics.gd/classdb/ArrayMesh"
	"graphics.gd/classdb/BoxShape3D"
	"graphics.gd/classdb/Camera3D"
	"graphics.gd/classdb/CollisionShape3D"
	"graphics.gd/classdb/Control"
	"graphics.gd/classdb/CylinderMesh"
	"graphics.gd/classdb/FileAccess"
	"graphics.gd/classdb/GPUParticles3D"
	"graphics.gd/classdb/GeometryInstance3D"
	"graphics.gd/classdb/HeightMapShape3D"
	"graphics.gd/classdb/Image"
	"graphics.gd/classdb/Input"
	"graphics.gd/classdb/InputEvent"
	"graphics.gd/classdb/InputEventMouseButton"
	"graphics.gd/classdb/Material"
	"graphics.gd/classdb/Mesh"
	"graphics.gd/classdb/MeshInstance3D"
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/ParticleProcessMaterial"
	"graphics.gd/classdb/QuadMesh"
	"graphics.gd/classdb/RenderingServer"
	"graphics.gd/classdb/Shader"
	"graphics.gd/classdb/ShaderMaterial"
	"graphics.gd/classdb/StandardMaterial3D"
	"graphics.gd/classdb/StaticBody3D"
	"graphics.gd/classdb/Texture2D"
	"graphics.gd/classdb/Texture2DArray"
	"graphics.gd/classdb/TextureRect"
	"graphics.gd/variant/AABB"
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

	// bulkReplay is set while the client is replaying the .mus3 log under the
	// loading splash. In this mode scheduleReload does NOT rebuild per frame —
	// it just records the touched tile in pendingReload. flushBulkReloads then
	// does a single full recompute per tile at the end (see finishLoading).
	// Replaying ~61k sculpts incrementally meant one mesh/normal/collision/sides/
	// water regen per frame (~83-148 of them) which dominated load time; this
	// collapses that to one per tile (see project_load_time_profile).
	bulkReplay    bool
	pendingReload map[*TerrainTile]struct{}
	// lightingApplyPending coalesces environment-slider sculpts during a bulk
	// replay: the state is updated per-stroke but lighting.apply (a burst of ~20
	// Godot setters) is fired once at the flush, avoiding a command-ring overflow.
	lightingApplyPending bool

	// waterRecomputePending / revealRecomputePending coalesce water-level and
	// reveal/hide edits the same way during a bulk replay. Applying them per
	// stroke loops every tile (applyWaterLevel → reloadWater) or rebuilds the
	// whole grass set (a revert's recomputeGrass) — thousands of cgo calls
	// against graphics.gd's command ring. While replaying we only record the
	// stroke / toggle the reverted flag and set these; flushBulkReloads applies
	// the final state once (recomputeWater / recomputeReveal) after the tiles are
	// rebuilt. Water is LWW and reveal is a deterministic replay of the surviving
	// strokes, so the once-at-flush result is identical to per-stroke folding.
	waterRecomputePending  bool
	revealRecomputePending bool

	// arrowsVisible toggles every existing extend-the-world arrow
	// when entering/leaving the terrain editor — arrows shouldn't be
	// clickable from the coaster or scenery editors.
	arrowsVisible bool

	// mapper, albedos, normal_maps, spec_maps are shared across all tiles so
	// the shader's Texture2DArray layer index for a given paint Design is
	// consistent everywhere it gets painted. Mutated in Sculpt's upload step;
	// tiles read mapper[Design] when sampling textures. The three image slices
	// stay index-aligned: layer i holds a design's albedo / normal / gloss.
	mapper      map[musical.Design]int
	albedos     []Image.Instance
	normal_maps []Image.Instance
	spec_maps   []Image.Instance

	// defaultSpec is the shared all-black 2048² R8 gloss layer used for the base
	// layer and every design lacking a "_spec" sibling. Built ONCE and reused:
	// Image.CreateFromData marshals its 4.19M-byte data argument into a Godot
	// PackedByteArray element-by-element (a per-byte cgo cost — see the load
	// profile), so building a fresh identical image per design cost seconds during
	// replay. Reusing one handle as multiple Texture2DArray layers is safe — the
	// layers are read-only copies of the source image's data.
	defaultSpec Image.Instance

	shader        ShaderMaterial.Instance
	shader_buried ShaderMaterial.Instance

	// Water material shared by every tile. The same wave shader drives BOTH
	// water surfaces (plane + side walls) so the sides stay in sync with the
	// plane; the per-vertex terrain floor (CUSTOM0.r) clamps the water above
	// the terrain.
	water_shader ShaderMaterial.Instance

	// The two Shader resources water_shader can be bound to, swapped by
	// Client.applyWaterQuality per graphics tier (see GraphicsQuality.simpleWater):
	// waterShaderFull is water.gdshader (foam, refraction, swell, reflections);
	// waterShaderSimple is water_simple.gdshader (flat blue + basic normals) for
	// the lowest tier. Both honour the same geometry contract and brush-preview
	// uniforms, so the swap is invisible to every other water code path.
	waterShaderFull   Shader.Instance
	waterShaderSimple Shader.Instance

	// WaterLevel is the world-space Y of the water surface. The default of
	// -2 matches the bottom of the terrain skirt, so by default the water
	// sits hidden under flat terrain (i.e. there is no visible water until
	// the level is raised). This is the committed/observed value every client
	// converges to.
	WaterLevel Float.X

	// waterDisplayed is the currently-RENDERED water level, eased toward
	// WaterLevel each frame by processWaterRise so a level change glides instead
	// of teleporting (the mesh is rebuilt at WaterLevel and the shader offsets the
	// still water by waterDisplayed-WaterLevel; see water.gdshader's water_rise).
	// Purely cosmetic and client-local — it always settles back to WaterLevel.
	waterDisplayed Float.X

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
	// (grasses, pebbles, foliage, mineral/boulders) across the terrain
	// surface. A stroke is recorded as one musical.Sculpt (Editor "terrain",
	// Slider = the dressing tab, Amount = density, Design = the scattered
	// mesh) so the placement is observable by, and deterministically
	// reproducible on, every client — the scatter is seeded purely from the
	// sculpt's Author/Target/Radius.
	//
	DressActive  bool   // a dressing design is selected and the brush is armed
	DressDesign  string // selected mesh resource (res://...glb), local only
	DressTab     string // dressing category ("grasses"/"pebbles"/"foliage"/"shrooms"/"boulder")
	BrushDensity Float.X
	// dressDesignID is the musical.Design for DressDesign, resolved ONCE in
	// SelectDesign. MusicalDesign reserves an id + emits an Import the first time
	// it sees a resource, so the per-frame preview and per-segment paint must use
	// this cached value rather than re-resolving (which would spam imports).
	dressDesignID musical.Design
	// dressSeed is the scatter seed for the NEXT dressing stroke. The relative
	// arrangement is derived purely from this seed (Target only translates it),
	// so the hover preview slides smoothly with the brush instead of reshuffling
	// every frame. It is held fixed while hovering, committed verbatim into each
	// stroke's Sculpt.Random (so the placement is predetermined + reproduced
	// identically on every client), and advanced after each committed segment so
	// a drag scatters varied patches rather than stamping one repeatedly.
	dressSeed uint64

	//
	// Clear brush state (armed by picking a clearer from the "removal" tab
	// in ModeDressing). These tools emit negative-Amount dressing sculpts
	// that erase *all* instances of the chosen category (Slider) inside the
	// disc, regardless of Design. This replaces the legacy Ctrl+Shift gesture.
	//
	ClearActive   bool   // a removal tool is armed
	ClearCategory string // "grasses", "pebbles", "foliage", "shrooms", or "boulder"

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

	// Plateau/smooth are drag-paint brushes (PaintTerrainSculpt): while held they
	// emit one Sculpt per spaced segment. sculptStroke marks a stroke in progress,
	// sculptLast spaces the segments, and sculptLockY is the plateau target height
	// captured at the stroke's start — every segment flattens to THIS level (what
	// the preview showed) instead of re-sampling the shifting terrain under the
	// cursor, so a drag carves one continuous flat terrace. Reset on stroke end.
	sculptStroke bool
	sculptLast   Vector3.XYZ
	sculptLockY  Float.X

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

	// grassRenders holds the MERGED rendering: one grassRender per unique
	// dressing Design, each with one MultiMesh per asset sub-mesh part, into
	// which EVERY committed grassPatch of that design feeds its visible
	// instances. So the scene carries a handful of MultiMeshInstance3D nodes
	// (one set per design) instead of one set per patch — far fewer nodes and
	// draw calls for the same instances. grassDirty marks the designs whose
	// merged buffers need rebuilding; flushGrassRenders rebuilds them (deferred
	// to one pass when grassDeferRender is set, so a bulk replay / multi-patch
	// erase / height-drag reproject coalesces into a single repopulate per
	// design). The transient hover dressPreview keeps its own per-patch
	// MultiMeshes (it is a single patch and must tear down independently).
	grassRenders     map[musical.Design]*grassRender
	grassDirty       map[musical.Design]bool
	grassDeferRender bool

	// dressSharedMats caches the resolved surface material for scenery library
	// props scattered by the foliage/boulder brushes. Those are
	// MaterialSharingMeshInstance3D nodes whose material streams from
	// library.pck, but grassMeshFor extracts the bare mesh without adding the
	// instance to the tree, so the node's Ready (which would load the material)
	// never runs. We load it ourselves off the main thread; dressMatPending
	// dedupes the in-flight load while the stroke parks in pendingGrass.
	dressSharedMats map[sharingKey]Material.Instance
	dressMatPending map[sharingKey]bool

	// dressPreview is the transient hover scatter shown in ModeDressing before a
	// stroke commits — the dressing analogue of the height/paint brush previews.
	// It renders the EXACT instances a click would place (same seed the commit
	// stores in Sculpt.Random), is local-only (never committed, broadcast, or
	// added to grassPatches), and is rebuilt when the brush moves/re-tunes and
	// torn down on commit / on leaving the dressing tool. dressPreviewKey is the
	// quantised brush state it was built for, so an unchanged hover skips rebuild.
	dressPreview    *grassPatch
	dressPreviewKey dressKey

	// grassWindShader is the shared wind-sway shader applied to every grass
	// blade mesh (see grassWindMaterial). Loaded once in Ready; the imported
	// grass material's albedo texture is copied onto a per-design instance.
	grassWindShader Shader.Instance

	// foliageWindShader is the sibling sway shader for scattered foliage (see
	// foliageWindMaterial): trunk stays planted, canopy flutters. Driven by the
	// same global wind uniforms as grassWindShader.
	foliageWindShader Shader.Instance

	// Undo/redo histories for the editor-level mutations (those not held per
	// tile): dressing scatter/erase, water level, and tile extend/hide. Each
	// committed stroke is appended; a Revert sculpt toggles the matching entry's
	// reverted flag and the subsystem recomputes from the survivors. Per-tile
	// height/paint/river strokes live in TerrainTile.history instead.
	grassHistory  []editStroke
	waterHistory  []editStroke
	revealHistory []editStroke

	// objectPreviewOffsets holds the transient Y displacement currently applied to
	// each placed scenery object by the live height-brush hover preview (the CPU
	// analogue of the grass shader's grass_brush_* preview — placed objects are
	// arbitrary meshes we don't own a shader for, so we nudge their node Y). The
	// invariant is node.Y == committedY + offset, so the committed base is always
	// node.Y − offset: captureObjectHeights subtracts it to measure against the
	// real surface and reprojectObjects re-adds it, keeping the model correct
	// across a commit. Empty when nothing is being previewed.
	objectPreviewOffsets map[Node3D.ID]Float.X

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
// freeTerrainResources releases the terrain editor's session-lifetime resources at
// shutdown (registered in Ready) so they don't report as leaks at exit. Object.Free only
// decrements (and is a no-op on an unset handle), so anything still bound to a live node
// is destroyed for real only when that node is finalized in teardown. The shared terrain/
// water materials are freed ONCE — every tile aliases tr.shader/tr.shader_buried/
// tr.water_shader (see revealTile), so freeing them per tile would over-decrement.
func (tr *TerrainEditor) freeTerrainResources() {
	for _, tile := range tr.tiles {
		if tile != nil {
			Object.Free(tile.heightmapShape) // per-tile, uniquely held
		}
	}
	tr.tiles = nil
	Object.Free(tr.shader)
	Object.Free(tr.shader_buried)
	Object.Free(tr.water_shader)
	Object.Free(tr.waterShaderFull)
	Object.Free(tr.waterShaderSimple)
	Object.Free(tr.grassWindShader)
	Object.Free(tr.foliageWindShader)
	for _, img := range tr.albedos {
		Object.Free(img)
	}
	for _, img := range tr.normal_maps {
		Object.Free(img)
	}
	for _, img := range tr.spec_maps {
		// defaultSpec is a single shared handle that appears in spec_maps once per
		// design lacking a _spec sibling; free it exactly once (below), not per slot.
		if img == tr.defaultSpec {
			continue
		}
		Object.Free(img)
	}
	if tr.defaultSpec != (Image.Instance{}) {
		Object.Free(tr.defaultSpec)
		tr.defaultSpec = Image.Instance{}
	}
	tr.albedos, tr.normal_maps, tr.spec_maps = nil, nil, nil
}

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

// brushGizmosForCurrentMode returns the set of gizmo buttons that should be
// present in the toolbar for the terrain editor, based on which brush tool
// (if any) is currently armed and the active mode. Texture painting and
// dressing only expose the brush-size gizmo; terrain/river height tools
// also expose the power gizmo.
func (fe *TerrainEditor) brushGizmosForCurrentMode() []Gizmo {
	if fe.client == nil || fe.client.ui == nil {
		return nil
	}
	mode := fe.client.ui.mode

	// Height/river sculpt tools (only relevant while in Geometry mode).
	if mode == ModeGeometry && fe.TerrainBrush != "" {
		return []Gizmo{GizmoBrush, GizmoPower}
	}
	// Texture painting: only the size control.
	if fe.PaintActive {
		return []Gizmo{GizmoBrush}
	}
	// Dressing (add): the brush radius/size control plus the density control,
	// which the GizmoPower button hosts (density is the dressing brush's "power").
	if fe.DressActive {
		return []Gizmo{GizmoBrush, GizmoPower}
	}
	// Removal tools (category clears + bomb): just the brush radius/size; there
	// is no density to tune when erasing.
	if fe.ClearActive {
		return []Gizmo{GizmoBrush}
	}
	// A height brush may still be "parked" (see SelectDesign comment about
	// not disarming it on texture pick). While not in Geometry it does not
	// need the power gizmo; just expose size if anything brush-like is armed.
	if fe.TerrainBrush != "" {
		return []Gizmo{GizmoBrush}
	}
	return nil
}

func (fe *TerrainEditor) EnableEditor() {
	fe.setArrowsVisible(true)
	fe.lighting.apply(fe.client)
	// Only show the terrain brush highlight overlay (ring) and the appropriate
	// brush gizmo(s) when an actual brush tool has been selected. Texture
	// painting and dressing only need the size (Brush) gizmo; height/river
	// sculpt tools also get the power gizmo.
	gizmos := fe.brushGizmosForCurrentMode()
	if len(gizmos) > 0 {
		fe.client.SetGizmos(gizmos)
		fe.shader.SetShaderParameter("brush_active", true)
		fe.shader_buried.SetShaderParameter("brush_active", true)
	} else {
		fe.client.SetGizmos(nil)
	}
	fe.syncBrushSliders()
}

// syncBrushSliders shows or hides the brush-related toolbar sliders
// (size, density, power) according to the current brush state and mode.
// It also toggles the terrain brush ring highlight (brush_active) so the
// overlay only appears when a brush tool is selected for the active mode.
func (fe *TerrainEditor) syncBrushSliders() {
	if fe.client == nil || fe.client.ui == nil || fe.client.ui.CloudControl == nil {
		return
	}
	ui := fe.client.ui
	cc := ui.CloudControl
	editing := fe.client.Editing == Editing.Terrain
	mode := ui.mode
	// A brush tool is "active for the current task" only when its mode matches.
	// TerrainBrush (height/river) is only relevant in ModeGeometry.
	// Paint is relevant in ModeMaterial once a texture has been selected (PaintActive).
	// Dress is relevant in ModeDressing once a design has been selected.
	hasPaint := fe.PaintActive
	hasDress := (mode == ModeDressing) && fe.DressActive
	hasClear := (mode == ModeDressing) && fe.ClearActive
	hasGeomBrush := (mode == ModeGeometry) && (fe.TerrainBrush != "")
	hasBrushForMode := hasPaint || hasDress || hasClear || hasGeomBrush
	cc.setSizeSliderVisible(editing && hasBrushForMode)
	cc.setDensitySliderVisible(editing && mode == ModeDressing && hasDress)
	cc.setPowerSliderVisible(editing && mode == ModeGeometry && hasGeomBrush)
	// Keep the shader brush ring in sync with the current mode's brush state.
	if editing && fe.shader != ShaderMaterial.Nil && fe.shader_buried != ShaderMaterial.Nil {
		fe.shader.SetShaderParameter("brush_active", hasBrushForMode)
		fe.shader_buried.SetShaderParameter("brush_active", hasBrushForMode)
	}
	// Keep the brush-related gizmo buttons (Brush size, and Power for height
	// tools) correct for the current mode + armed brush. This makes tabbing
	// between Geometry/Material/Dressing while a brush is armed show/hide the
	// power button at the right times (e.g. no power button while painting
	// textures, but it appears when you switch back to Geometry with a height
	// brush selected).
	if editing && fe.client.ui.CloudControl != nil {
		fe.client.SetGizmos(fe.brushGizmosForCurrentMode())
	}
}

func (fe *TerrainEditor) ChangeEditor() {
	fe.shader.
		SetShaderParameter("height", 0.0).
		SetShaderParameter("river_fill", 0.0).
		SetShaderParameter("river_carve", 0.0).
		SetShaderParameter("brush_mode", 0.0).
		SetShaderParameter("brush_active", false).
		SetShaderParameter("paint_active", false)
	fe.shader_buried.
		SetShaderParameter("height", 0.0).
		SetShaderParameter("brush_mode", 0.0).
		SetShaderParameter("brush_active", false)
	fe.water_shader.
		SetShaderParameter("height", 0.0).
		SetShaderParameter("river_preview", 0.0).
		SetShaderParameter("brush_mode", 0.0)
	fe.BrushActive = false
	fe.PaintActive = false
	fe.DressActive = false
	fe.ClearActive = false
	fe.ClearCategory = ""
	fe.TerrainBrush = ""
	fe.brushStrokeActive = false
	fe.sculptStroke = false
	fe.setArrowsVisible(false)
	// Settle any objects/grass left displaced by a hover preview — Process (which
	// otherwise resets the preview each frame) won't run for this editor anymore.
	fe.clearObjectPreview()
	fe.clearDressPreview()

	// Make sure the toolbar gizmos + sliders reflect the cleared brush state.
	// (CancelPaint has explicit teardown; ChangeEditor must also keep the UI
	// chrome consistent when the editor itself is resetting brush state.)
	if fe.client != nil && fe.client.Editing == Editing.Terrain {
		fe.syncBrushSliders()
	}
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
			"liquids",
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
			"foliage",
			"shrooms",
			"boulder",
			"removal",
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
	if mode != ModeGeometry && !(mode == ModeDressing && tab == "removal") {
		return nil
	}
	switch tab {
	case "liquids":
		return []BuiltinDesign{
			{
				Resource: BuiltinTerrainRiver,
				Icon:     "res://ui/streams.svg",
				Label:    "River",
			},
			{
				Resource: BuiltinTerrainRiverErase,
				Icon:     "res://ui/nowater.svg",
				Label:    "River eraser",
			},
		}
	case "terrain":
		return []BuiltinDesign{
			{
				Resource: BuiltinTerrainRaise,
				Icon:     "res://ui/terrain/editing/uplift.svg",
				Label:    "Raise terrain",
			},
			{
				Resource: BuiltinTerrainLower,
				Icon:     "res://ui/terrain/editing/canyon.svg",
				Label:    "Lower terrain",
			},
			{
				Resource: BuiltinTerrainPlateau,
				Icon:     "res://ui/terrain/editing/plateau.svg",
				Label:    "Plateau",
			},
			{
				Resource: BuiltinTerrainSmooth,
				Icon:     "res://ui/terrain/editing/smooths.svg",
				Label:    "Smooth",
			},
		}
	case "removal":
		// Category-wide erasers for ModeDressing (plus the bomb that clears
		// everything). These replace the old Ctrl+Shift + armed-design gesture.
		return []BuiltinDesign{
			{
				Resource: BuiltinDressingClearAll,
				Icon:     "res://ui/bomb.svg",
				Label:    "Bomb — clear ALL dressing",
			},
			{
				Resource: BuiltinDressingClearGrasses,
				Icon:     "res://ui/scythe.svg",
				Label:    "Scythe — clear grasses",
			},
			{
				Resource: BuiltinDressingClearFoliage,
				Icon:     "res://ui/axe.svg",
				Label:    "Axe — clear foliage",
			},
			{
				Resource: BuiltinDressingClearShrooms,
				Icon:     "res://ui/rake.svg",
				Label:    "Rake — clear shrooms",
			},
			{
				Resource: BuiltinDressingClearBoulder,
				Icon:     "res://ui/pickaxe.svg",
				Label:    "Pickaxe — clear boulder",
			},
		}
	default:
		return nil
	}
}

func (fe *TerrainEditor) SelectDesign(mode Mode, design string) {
	if mode == ModeDressing {
		// Clear-sentinel tools from the "removal" tab — arm a category-wide
		// eraser (or the bomb for everything) instead of a normal placement brush.
		switch design {
		case BuiltinDressingClearAll:
			fe.ArmClearBrush(ClearAllDressingCategory)
			return
		case BuiltinDressingClearGrasses:
			fe.ArmClearBrush("grasses")
			return
		case BuiltinDressingClearFoliage:
			fe.ArmClearBrush("foliage")
			return
		case BuiltinDressingClearShrooms:
			fe.ArmClearBrush("shrooms")
			return
		case BuiltinDressingClearBoulder:
			fe.ArmClearBrush("boulder")
			return
		}

		// Arm the dressing brush: the selected mesh scatters across the
		// surface on the next stroke. The tab (parent dir, e.g.
		// "grasses" or "foliage") is carried into the sculpt's Slider so the
		// category round-trips and remote clients route it the same way.
		fe.CancelPaint()
		fe.DressActive = true
		fe.DressDesign = design
		fe.DressTab = path.Base(path.Dir(design))
		fe.BrushDesign = design
		// Resolve (and kick off the import of) the design ONCE here, so the live
		// preview and each paint segment reuse the same id instead of re-importing.
		if fe.client != nil {
			fe.dressDesignID = fe.client.MusicalDesign(design)
		}
		// Roll a fresh scatter seed for this tool selection so the previewed
		// arrangement is fixed (no per-move reshuffle) until it commits.
		fe.dressSeed = nextSeed(fe.dressSeed)
		// Allow the very next user-initiated stroke after picking a design
		// to fire without the movement-spacing guard (original behaviour).
		fe.dressLastSet = false
		fe.EnableEditor()
		return
	}
	// Terrain brush builtins (raise/lower) in ModeGeometry: arm the
	// explicit height sculpt brush. This replaces the previous implicit
	// "any click in geometry mode sculpts height" behaviour.
	if mode == ModeGeometry && (design == BuiltinTerrainRaise || design == BuiltinTerrainLower || design == BuiltinTerrainPlateau || design == BuiltinTerrainSmooth || design == BuiltinTerrainRiver || design == BuiltinTerrainRiverErase) {
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
		// Mark paint as armed so EnableEditor shows the brush ring and
		// brush/power gizmos immediately (the actual texture loads async
		// in Process and sets the paint_active shader + BrushDesign).
		fe.PaintActive = true
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
	if fe.client.Editing != Editing.Terrain || fe.client.ui.mode != ModeGeometry || fe.TerrainBrush == "" {
		fe.client.ui.CloudControl.setPowerSliderVisible(false)
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
		SetShaderParameter("height", 0.0).
		SetShaderParameter("brush_mode", 0.0).
		// 1 while the river-erase ("no water") brush previews filling the channel
		// back to the original ground; see terrain.gdshader's river_fill block.
		SetShaderParameter("river_fill", 0.0).
		// >0 (channel depth) while the river carve brush previews; see river_carve
		// in terrain.gdshader (paint-over bed carve).
		SetShaderParameter("river_carve", 0.0)

	rock := LoadSync[Texture2D.Instance]("res://default/mineral.jpg")
	buried := LoadSync[Shader.Instance]("res://shader/buried.gdshader")
	tr.shader_buried = ShaderMaterial.New().
		SetShader(buried).
		SetShaderParameter("texture_albedo", rock).
		SetShaderParameter("radius", 2.0).
		SetShaderParameter("height", 0.0).
		SetShaderParameter("brush_mode", 0.0)

	// Water surface material plus its scrolling normal maps, UV distortion
	// map and foam texture. The PNGs are raw (no .import files). The same wave
	// shader drives both water surfaces (plane + side walls); the side walls
	// share the plane edge's world XZ so they get the identical Gerstner
	// displacement and stay connected to the plane.
	// Load both water shaders; applyWaterQuality binds the one matching the
	// active tier onto the shared material. Default to the full shader — startup
	// applyWaterQuality (and any later Settings move) swaps in the simple one for
	// the lowest tier.
	tr.waterShaderFull = LoadSync[Shader.Instance]("res://shader/water.gdshader")
	tr.waterShaderSimple = LoadSync[Shader.Instance]("res://shader/water_simple.gdshader")
	tr.water_shader = ShaderMaterial.New().
		SetShader(tr.waterShaderFull).
		SetShaderParameter("normalmap_a_sampler", LoadSync[Texture2D.Instance]("res://terrain/water/Water_N_A.png")).
		SetShaderParameter("normalmap_b_sampler", LoadSync[Texture2D.Instance]("res://terrain/water/Water_N_B.png")).
		SetShaderParameter("uv_sampler", LoadSync[Texture2D.Instance]("res://terrain/water/Water_UV.png")).
		SetShaderParameter("foam_sampler", LoadSync[Texture2D.Instance]("res://terrain/water/Foam.png")).
		// Mirror the terrain shaders' brush-preview uniforms so the water tracks
		// the raise/lower height preview before it commits (see water.gdshader).
		SetShaderParameter("radius", 2.0).
		SetShaderParameter("height", 0.0).
		SetShaderParameter("brush_mode", 0.0).
		SetShaderParameter("river_preview", 0.0).
		// Start glassy — wave height is driven by the environment Wind slider
		// (see updateWeatherIntensity); no wind means no swell.
		SetShaderParameter("wave_height", 0.0).
		// Off until the quality tier turns it on (Client.applyWaterQuality); the
		// shader skips the screen-space-reflection march entirely when this is 0.
		SetShaderParameter("reflection_strength", 0.0)

	// Default level -2 == skirt bottom == hidden under flat terrain.
	tr.WaterLevel = -2
	tr.waterDisplayed = tr.WaterLevel // settled: no glide until the level changes
	tr.water_shader.SetShaderParameter("water_level", float64(tr.WaterLevel))

	tr.BrushRadius = 2.0
	tr.BrushPower = 2.0
	tr.BrushDensity = 0.5
	tr.BrushRiverDepth = riverDefaultDepth

	tr.grassWindShader = LoadSync[Shader.Instance]("res://shader/grass_wind.gdshader")
	tr.foliageWindShader = LoadSync[Shader.Instance]("res://shader/foliage_wind_mm.gdshader")
	// Make sure the grass wind global shader parameters exist before any grass
	// blade (and thus the shader) can render; updateWeatherIntensity writes
	// their live values from the environment Wind slider.
	ensureGrassWindGlobals()
	tr.grassMeshes = make(map[musical.Design]grassAsset)
	tr.grassRenders = make(map[musical.Design]*grassRender)
	tr.grassDirty = make(map[musical.Design]bool)
	tr.dressSharedMats = make(map[sharingKey]Material.Instance)
	tr.dressMatPending = make(map[sharingKey]bool)
	// Release the session-lifetime dressing caches (MultiMeshes, grass meshes, shared
	// materials) at shutdown so they don't report as leaks at exit (see freeDressCaches).
	OnShutdown(tr.freeDressCaches)
	// Likewise the terrain editor's own resources (tile collision shapes, the shared
	// terrain/water materials and shaders, and the texture-array source images).
	OnShutdown(tr.freeTerrainResources)
	tr.objectPreviewOffsets = make(map[Node3D.ID]Float.X)
	tr.tiles = make(map[tileCoord]*TerrainTile)
	tr.mapper = make(map[musical.Design]int)
	tr.albedos = []Image.Instance{LoadSync[Texture2D.Instance]("res://terrain/alpine_grass.png").AsTexture2D().GetImage()}
	tr.normal_maps = []Image.Instance{LoadSync[Texture2D.Instance]("res://terrain/normal.png").AsTexture2D().GetImage()}
	tr.spec_maps = []Image.Instance{tr.defaultTerrainSpec()}
	tr.uploadTextureArrays()
	// Spawn + reveal the starter tile so the world is clickable before any
	// sculpt arrives (every other tile starts hidden until an explicit extend).
	tr.revealTile(tileCoord{0, 0})
}

// uploadTextureArrays rebuilds the albedo / normal / gloss Texture2DArrays
// from the editor-level image slices and pushes them to the shared shader.
// Called both at startup and when a new paint Design first appears via
// uploadDesign. All layers within an array must share size+format+mipmaps:
// albedos and normal_maps rely on every terrain texture importing identically
// (see normal.png.import, aligned to the pack's compressed-normal settings),
// and spec_maps are normalised to R8 in loadTerrainSpec.
func (tr *TerrainEditor) uploadTextureArrays() {
	defer timeIn(&bucketTexArray)()
	terrains := Texture2DArray.New()
	terrains.AsImageTextureLayered().CreateFromImages(tr.albedos)
	bumpmaps := Texture2DArray.New()
	bumpmaps.AsImageTextureLayered().CreateFromImages(tr.normal_maps)
	specmaps := Texture2DArray.New()
	specmaps.AsImageTextureLayered().CreateFromImages(tr.spec_maps)
	tr.shader.
		SetShaderParameter("texture_albedo", terrains).
		SetShaderParameter("texture_normal", bumpmaps).
		SetShaderParameter("texture_spec", specmaps)
}

// uploadDesign assigns the given paint Design a layer index in the shared
// texture arrays, loading the texture and its `_norm`/`_spec` siblings (if
// present) the first time it appears. Returns the layer index; 0 is reserved
// for the default base layer.
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
	dt := timeIn(&bucketTexDecode)
	tr.albedos = append(tr.albedos, texture.GetImage())
	dt()
	resPath := texture.AsResource().ResourcePath()
	ext := path.Ext(resPath)
	base := strings.TrimSuffix(resPath, ext)
	// Normal map sibling. The wildfire_games terrain pack ships these as
	// "<name>_norm.png" (NOT "_normal"); the old "_normal" lookup never
	// matched, so every painted design silently fell back to the flat
	// default normal. Load the real map so the surface shows relief.
	normal_path := base + "_norm" + ext
	if !FileAccess.FileExists(normal_path) {
		normal_path = "res://terrain/normal.png"
	}
	lt := timeIn(&bucketTexLoad)
	normTex := LoadSync[Texture2D.Instance](normal_path)
	lt()
	dt = timeIn(&bucketTexDecode)
	tr.normal_maps = append(tr.normal_maps, normTex.AsTexture2D().GetImage())
	dt()
	// Specular/gloss sibling ("<name>_spec.png"): feeds the shader's roughness.
	spec_path := base + "_spec" + ext
	if FileAccess.FileExists(spec_path) {
		tr.spec_maps = append(tr.spec_maps, tr.loadTerrainSpec(spec_path))
	} else {
		tr.spec_maps = append(tr.spec_maps, tr.defaultTerrainSpec())
	}
	// During bulk replay, many designs are uploaded back-to-back; rebuilding all
	// three Texture2DArrays per design is O(designs²) GPU work. Skip it here and
	// rebuild once in flushBulkReloads. (mapper/albedos are still populated, so
	// the deferred sample_texture finds the layer index.)
	if !tr.bulkReplay {
		tr.uploadTextureArrays()
	}
	return idx
}

// terrainTextureSize is the edge length of every wildfire_games terrain
// texture (diffuse/normal/spec are all 2048²); the generated default gloss
// layer matches it so it slots into the same Texture2DArray as loaded specs.
const terrainTextureSize = 2048

// loadTerrainSpec loads a "<name>_spec.png" gloss map and normalises it to a
// single-channel, mip-free R8 Image. The pack's spec PNGs come in mixed source
// formats (L / RGB / RGBA / paletted / 16-bit) which would otherwise import to
// inconsistent GPU formats and break CreateFromImages; flattening to R8 here
// guarantees every gloss layer shares one format. Falls back to the neutral
// default if the image can't be decompressed.
func (tr *TerrainEditor) loadTerrainSpec(spec_path string) Image.Instance {
	lt := timeIn(&bucketTexLoad)
	specTex := LoadSync[Texture2D.Instance](spec_path)
	lt()
	defer timeIn(&bucketTexDecode)()
	img := specTex.AsTexture2D().GetImage()
	if img.IsCompressed() {
		if err := img.Decompress(); err != nil {
			return tr.defaultTerrainSpec()
		}
	}
	img.Convert(Image.FormatR8)
	img.ClearMipmaps()
	return img
}

// defaultTerrainSpec is the gloss layer used for the base layer and for any
// design that ships no `_spec` sibling: fully black, i.e. ROUGHNESS = 1.0 in
// the shader, matching the terrain's prior uniformly-matte look. The image is
// built once and cached (tr.defaultSpec) — every design's default gloss is the
// same all-black 2048² R8, so there's no reason to allocate and CreateFromData a
// fresh identical 4.19MB image per design (graphics.gd now bulk-marshals the
// data, so the per-image cost is small, but this still avoids the redundant
// allocations entirely). Reusing one handle across Texture2DArray layers is safe.
func (tr *TerrainEditor) defaultTerrainSpec() Image.Instance {
	if tr.defaultSpec == (Image.Instance{}) {
		tr.defaultSpec = Image.CreateFromData(terrainTextureSize, terrainTextureSize, false,
			Image.FormatR8, make([]byte, terrainTextureSize*terrainTextureSize))
	}
	return tr.defaultSpec
}

func (tr *TerrainEditor) Paint() {
	if tr.BrushDesign == "" {
		return
	}
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
		tr.brushStrokeActive = false
		active = true
		// Drain any in-flight texture selection. If the user right-clicks to
		// cancel texturing before the async load in Process completes, a
		// late receive would otherwise re-arm PaintActive (and the paint
		// preview) after the cancel, causing the brush ring/gizmo to reappear.
		select {
		case <-tr.texture:
		default:
		}
		// Also clear the design so no stale reference remains.
		tr.BrushDesign = ""
		// Immediately tear down the brush gizmo button and size slider for
		// the painting case. This guarantees the toolbar button disappears
		// on right-click cancel even if later sync/force logic has ordering
		// subtleties with parked brushes or UI refresh.
		if tr.client != nil && tr.client.ui != nil && tr.client.ui.CloudControl != nil {
			cc := tr.client.ui.CloudControl
			cc.setSizeSliderVisible(false)
			tr.client.SetGizmos(nil)
			if cc.GizmoIndicator != TextureRect.Nil {
				cc.GizmoIndicator.AsCanvasItem().SetVisible(false)
			}
			// Extra-targeted removal for the exact brush button.
			if btn, ok := cc.gizmoButtons[GizmoBrush]; ok && btn != Control.Nil {
				btn.AsCanvasItem().SetVisible(false)
				if p := btn.AsNode().GetParent(); p != Node.Nil {
					p.RemoveChild(btn.AsNode())
				}
				delete(cc.gizmoButtons, GizmoBrush)
			}
			// Sweep for any leftover "brush" named nodes.
			vbox := cc.GizmoTypes.AsNode()
			if vbox != Node.Nil {
				for i := vbox.GetChildCount() - 1; i >= 0; i-- {
					child := vbox.GetChild(i)
					n := strings.ToLower(child.Name())
					if strings.Contains(n, "brush") {
						vbox.RemoveChild(child)
						child.QueueFree()
					}
				}
			}
		}
		// Also make sure the ring is off on the water shader for painting.
		if tr.water_shader != ShaderMaterial.Nil {
			tr.water_shader.SetShaderParameter("brush_active", false)
		}
	}
	if tr.DressActive {
		tr.DressActive = false
		tr.brushStrokeActive = false
		tr.clearDressPreview()
		active = true
	}
	if tr.ClearActive {
		tr.ClearActive = false
		tr.ClearCategory = ""
		tr.brushStrokeActive = false
		active = true
	}
	clearedBrush := false
	if tr.TerrainBrush != "" {
		tr.TerrainBrush = ""
		tr.BrushActive = false
		tr.BrushAmount = 0
		tr.sculptStroke = false // drop any in-progress plateau/smooth drag lock
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
		tr.shader.SetShaderParameter("brush_mode", 0.0)
		tr.shader_buried.SetShaderParameter("brush_mode", 0.0)
		tr.water_shader.SetShaderParameter("brush_mode", 0.0)
	}
	if active {
		tr.syncBrushSliders()
	}
	// When the user explicitly cancels the current brush task (right-click)
	// while in the terrain editor, remove any brush-related gizmos and the
	// active-gizmo indicator. This ensures that after right-clicking to cancel
	// a just-selected terrain texture (or dressing/height brush), the overlay
	// and buttons go away even if another brush type remains "parked" in the
	// background state.
	if active && tr.client != nil && tr.client.Editing == Editing.Terrain {
		if tr.client.ui != nil && tr.client.ui.CloudControl != nil {
			cc := tr.client.ui.CloudControl
			tr.client.SetGizmos(nil)
			if cc.GizmoIndicator != TextureRect.Nil {
				cc.GizmoIndicator.AsCanvasItem().SetVisible(false)
			}
			// Targeted removal of the brush button in the general cancel path too.
			if btn, ok := cc.gizmoButtons[GizmoBrush]; ok && btn != Control.Nil {
				btn.AsCanvasItem().SetVisible(false)
				if p := btn.AsNode().GetParent(); p != Node.Nil {
					p.RemoveChild(btn.AsNode())
				}
				delete(cc.gizmoButtons, GizmoBrush)
			}
		}
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
		Design: tr.dressDesignID,
		// Lock the scatter seed (the exact one the hover preview showed) into the
		// stroke so the placement is predetermined: every client and every replay
		// regenerates the identical arrangement from Random, translated to Target,
		// independent of position. 0 (legacy) falls back to grassSeed in fillPatch.
		Random: int64(tr.dressSeed),
		Commit: true,
	})
	// Advance the seed so the next segment of a drag (and the next hover preview)
	// scatters a different patch rather than stamping this one repeatedly.
	tr.dressSeed = nextSeed(tr.dressSeed)
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
		Design: tr.dressDesignID,
		Commit: true,
	})
}

// EraseDressingCategory is the category-wide analogue of EraseDressing.
// It is driven by the "removal" tab tools (scythe, axe, bomb, etc.).
// It emits a negative-Amount sculpt; the Sculpt handler + eraseGrass
// perform the deletion.
//
// For normal categories the Slider carries the category name and Design==zero.
// For the bomb (ClearAllDressingCategory) the Slider is the special all-marker
// and eraseGrass removes patches from every dressing category inside the disc.
func (tr *TerrainEditor) EraseDressingCategory(category string) {
	if !tr.ClearActive || category == "" {
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
		Slider: category, // either a real category or ClearAllDressingCategory ("*")
		Target: tr.BrushTarget,
		Radius: tr.BrushRadius,
		Amount: -0.5,             // negative signals erase
		Design: musical.Design{}, // zero + special Slider → all categories (bomb)
		Commit: true,
	})

	// The bomb ("*") also nukes individually placed scenery props inside the
	// same disc. We do the sweep only on the authoring client (here); the
	// resulting Remove Changes are normal musical messages so everyone sees
	// the props disappear, and RecordChange gives proper per-prop undo.
	if category == ClearAllDressingCategory && tr.client != nil {
		tr.client.clearSceneryInDisc(tr.BrushTarget, tr.BrushRadius)
	}
}

// ArmClearBrush is called from SelectDesign when one of the four
// procedural://dressing/clear_* sentinels is chosen from the "removal" tab.
// It puts the TerrainEditor into category-erase mode (ClearActive) and
// prepares the shared brush UI affordances (ring, density slider).
func (tr *TerrainEditor) ArmClearBrush(category string) {
	tr.CancelPaint()
	tr.DressActive = false
	tr.ClearActive = true
	tr.ClearCategory = category
	tr.BrushDesign = "" // not a real design
	tr.dressLastSet = false
	tr.EnableEditor()
	tr.refreshGizmoPowerSlider() // re-use the machinery that shows brush controls
}

// CancelClearBrush turns off the removal brush state. Safe to call even
// if nothing was armed.
func (tr *TerrainEditor) CancelClearBrush() {
	if !tr.ClearActive {
		return
	}
	tr.ClearActive = false
	tr.ClearCategory = ""
	tr.brushStrokeActive = false
	// Hide any brush ring / size slider that the clear tool may have enabled.
	if tr.client != nil && tr.client.ui != nil && tr.client.ui.CloudControl != nil {
		tr.client.ui.CloudControl.setSizeSliderVisible(false)
	}
}

// triggerBombExplosion spawns a short-lived GPU particle burst at the given
// world position (snapped to terrain) with radius-based scale. It is the
// visual "kaboom" for the bomb removal tool. The effect is purely local
// eye-candy; the actual dressing removal is already handled by the musical
// Sculpt and is fully undo/redo/replication safe.
func (vr *TerrainEditor) triggerBombExplosion(target Vector3.XYZ, radius Float.X) {
	if radius < 0.1 {
		radius = 3.0
	}
	// Ground the explosion slightly above the surface.
	y := vr.HeightAt(target) + 0.6
	pos := Vector3.XYZ{X: target.X, Y: y, Z: target.Z}

	// Scale factor: bigger brush = bigger, more particles, faster boom.
	scale := Float.X(1.0 + (float64(radius)-3.0)*0.12)
	if scale < 0.6 {
		scale = 0.6
	}
	if scale > 3.5 {
		scale = 3.5
	}

	particles := GPUParticles3D.New()
	particles.AsNode().SetName("BombExplosion")
	particles.SetAmount(int(90 + radius*18)) // more debris for bigger radius
	particles.SetLifetime(1.15)
	particles.SetOneShot(true)
	particles.SetPreprocess(0.0)
	particles.SetDrawOrder(GPUParticles3D.DrawOrderViewDepth)
	particles.SetLocalCoords(false)
	particles.AsGeometryInstance3D().SetCastShadow(GeometryInstance3D.ShadowCastingSettingOff)

	// Small quad billboard for each "debris" particle.
	debris := QuadMesh.New().AsPlaneMesh().SetSize(Vector2.New(0.28*float32(scale), 0.28*float32(scale))).AsMesh()
	particles.SetDrawPass1(debris)

	// Generous visibility box so the burst isn't culled early.
	particles.SetVisibilityAabb(AABB.PositionSize{
		Position: Vector3.New(-80, -40, -80),
		Size:     Vector3.New(160, 90, 160),
	})

	mat := ParticleProcessMaterial.New()

	// Explosion: outward + upward burst from a sphere.
	mat.SetEmissionShape(ParticleProcessMaterial.EmissionShapeSphere)
	mat.SetEmissionSphereRadius(float32(radius) * 0.55)

	// Strong chaotic initial velocity.
	mat.SetDirection(Vector3.New(0, 0.6, 0))
	mat.SetSpread(115)
	mat.SetInitialVelocityMin(28 * float32(scale))
	mat.SetInitialVelocityMax(62 * float32(scale))
	mat.SetRadialVelocityMin(18 * float32(scale))
	mat.SetRadialVelocityMax(45 * float32(scale))

	// Gravity pulls debris back down realistically.
	mat.SetGravity(Vector3.New(0, -26, 0))
	mat.SetDampingMin(1.8)
	mat.SetDampingMax(3.2)

	// Nice explosive color progression (bright flash -> fire -> dust).
	mat.SetColor(Color.RGBA{R: 1.0, G: 0.92, B: 0.55, A: 0.95})
	mat.SetHueVariationMin(-0.08)
	mat.SetHueVariationMax(0.12)

	// Size over life: start decent, grow a little as it "expands", then fade.
	mat.SetScaleMin(0.7 * float32(scale))
	mat.SetScaleMax(1.45 * float32(scale))

	particles.SetProcessMaterial(mat.AsMaterial())

	vr.AsNode().AddChild(particles.AsNode())
	particles.AsNode3D().SetGlobalPosition(pos)
	particles.SetEmitting(true)

	// Fire-and-forget self-cleanup (one-shot particles). Safe across Go + Godot
	// binding because we marshal the QueueFree back to the main thread via Defer.
	life := particles.Lifetime()
	go func(p GPUParticles3D.Instance, seconds float32) {
		time.Sleep(time.Duration(seconds*1.7*1000) * time.Millisecond)
		Callable.Defer(Callable.New(func() {
			if p != GPUParticles3D.Nil {
				p.AsNode().QueueFree()
			}
		}))
	}(particles, life)
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
		// Height-sculpt preview: deform the terrain the way a click will, before
		// the user commits. brush_mode selects the deformation so the preview
		// matches the committed stroke: 0 additive (raise/lower), 1 plateau
		// (flatten toward the cursor height), 2 smooth (gentle pull toward it).
		brushMode := 0.0
		switch vr.TerrainBrush {
		case BuiltinTerrainPlateau:
			brushMode = 1.0
		case BuiltinTerrainSmooth:
			brushMode = 2.0
		}
		// heightParam drives the terrain + side-wall shader preview: the additive
		// bump for raise/lower (and the river carve), or the convergence fraction
		// for plateau/smooth (the shader pulls toward the cursor height by
		// fraction*falloff, matching the committed pull). surfaceDelta is the
		// additive world-Y displacement for props/water that ride an ADDITIVE bump
		// (zero for the flatten brushes — those ride flattenTarget via flatten=true).
		var heightParam, surfaceDelta Float.X
		flatten := false                  // plateau/smooth: pull toward flattenTarget rather than add
		flatTop := false                  // plateau only: hold a flat top (plateauFalloff) vs a soft cone
		flattenTarget := vr.BrushTarget.Y // plateau/smooth pull toward the cursor height
		riverPreview := Float.X(0)
		riverFill := Float.X(0)
		riverCarve := Float.X(0)
		switch vr.TerrainBrush {
		case BuiltinTerrainPlateau:
			heightParam = Float.X(terrainSculptFactor(vr.BrushPower, plateauStrengthScale))
			flatten = true
			flatTop = true
		case BuiltinTerrainSmooth:
			// Smooth pulls the disc toward its mean ground height with a soft cone —
			// the preview shows exactly that (flattenTarget = disc mean, set below).
			heightParam = Float.X(terrainSculptFactor(vr.BrushPower, smoothStrengthScale))
			flatten = true
		case BuiltinTerrainRiver:
			depth := vr.BrushRiverDepth
			if depth <= 0 {
				depth = riverDefaultDepth
			}
			heightParam = -depth
			surfaceDelta = -depth
			riverPreview = depth
			// Carve the bed with PAINT-OVER semantics (matching the commit) instead
			// of the additive `height` bump, so an overlapping drag segment doesn't
			// dig the channel additively deeper than it will actually be.
			riverCarve = depth
		case BuiltinTerrainRiverErase:
			// The eraser fills the channel back to the original ground rather
			// than carving (riverDepth -> 0 on commit). Keep the uniform height
			// preview neutral and instead flag the terrain shader to raise the
			// bed per-vertex toward the original ground (CUSTOM2.r) inside the
			// disc, so the river shoals and the (separately rendered) water is
			// occluded where the channel fills — matching one committed stroke.
			riverFill = 1
		default: // raise / lower: additive. While a press is held preview the
			// pressed amount (BrushAmount); otherwise the tool's primary (LMB)
			// GizmoPower amount so the shift is visible on hover.
			amount := vr.BrushAmount
			if !vr.BrushActive {
				amount = vr.terrainBrushDelta(true)
			}
			heightParam = amount
			surfaceDelta = amount
		}
		// Flatten brushes pull toward a target the preview must match. Plateau: the
		// LOCKED level once a drag is in progress (so the hover matches the committed
		// terrace), else the live cursor height. Smooth: the disc-mean ground height
		// (the same scalar the commit stores in Target.Y). uplift is the flatten target
		// the terrain/buried/water shaders pull toward; override its Y to match (XZ
		// stays the cursor for the disc centre), and feed flattenTarget to grass/objects.
		switch vr.TerrainBrush {
		case BuiltinTerrainPlateau:
			if vr.sculptStroke {
				flattenTarget = vr.sculptLockY
			}
		case BuiltinTerrainSmooth:
			flattenTarget = vr.discGroundMean(vr.BrushTarget, vr.BrushRadius, vr.BrushTarget.Y)
		}
		if flatten {
			up := Vector3.New(vr.BrushTarget.X, flattenTarget, vr.BrushTarget.Z)
			vr.shader.SetShaderParameter("uplift", up)
			vr.shader_buried.SetShaderParameter("uplift", up)
			vr.water_shader.SetShaderParameter("uplift", up)
		}
		vr.shader.SetShaderParameter("height", heightParam)
		vr.shader.SetShaderParameter("river_fill", riverFill)
		vr.shader.SetShaderParameter("river_carve", riverCarve)
		vr.shader.SetShaderParameter("brush_mode", brushMode)
		vr.shader_buried.SetShaderParameter("height", heightParam)
		vr.shader_buried.SetShaderParameter("brush_mode", brushMode)
		// Water reacts to the preview too: additive brushes (raise/lower/river) shift
		// the bed by `height` and ride river surfaces; the flatten brushes (brush_mode
		// >0.5) pull the bed toward uplift.y so the lake shoals over a raised mesa /
		// covers a lowered one. heightParam carries the additive amount OR the flatten
		// fraction, matching the terrain shader.
		vr.water_shader.SetShaderParameter("height", heightParam)
		vr.water_shader.SetShaderParameter("river_preview", riverPreview)
		vr.water_shader.SetShaderParameter("brush_mode", brushMode)
		// Carry the previewed surface onto the things sitting on the terrain: grass
		// rides it in its own shader (GPU), placed objects we don't own a shader for
		// are nudged on the CPU. Both reset on commit (reprojectGrass /
		// reprojectObjects re-seat on the real surface). Flatten brushes ride toward
		// flattenTarget by the strength fraction; additive brushes ride surfaceDelta.
		grassParam := surfaceDelta
		if flatten {
			grassParam = heightParam // the strength fraction the shader pulls by
		}
		vr.setGrassBrushPreview(vr.BrushTarget, vr.BrushRadius, grassParam, brushMode, flattenTarget)
		// Placed scenery objects (trees, props) ride the preview ONLY while a
		// stroke is actively being applied — a held raise/lower press (BrushActive)
		// or a plateau/smooth/river drag (brushStrokeActive). On a passive hover we
		// pass 0 so they settle on the COMMITTED surface instead of leaping to the
		// would-be-clicked height, which read as props floating high above the
		// solid ground. They re-seat for real on commit via reprojectObjects. The
		// scattered dressing above keeps riding the live ghost every frame — it is
		// part of the terrain skin, not a thing standing on it.
		objParam := grassParam
		if !vr.BrushActive && !vr.brushStrokeActive {
			objParam = 0
		}
		vr.updateObjectPreview(vr.BrushTarget, vr.BrushRadius, objParam, flatten, flatTop, flattenTarget)
	} else {
		vr.BrushAmount = 0.0
		vr.shader.SetShaderParameter("height", 0.0)
		vr.shader.SetShaderParameter("river_fill", 0.0)
		vr.shader.SetShaderParameter("river_carve", 0.0)
		vr.shader_buried.SetShaderParameter("height", 0.0)
		vr.water_shader.SetShaderParameter("height", 0.0)
		vr.water_shader.SetShaderParameter("river_preview", 0.0)
		vr.shader.SetShaderParameter("brush_mode", 0.0)
		vr.shader_buried.SetShaderParameter("brush_mode", 0.0)
		vr.water_shader.SetShaderParameter("brush_mode", 0.0)
		vr.setGrassBrushPreview(vr.BrushTarget, vr.BrushRadius, 0, 0, 0)
		vr.updateObjectPreview(vr.BrushTarget, vr.BrushRadius, 0, false, false, 0)
	}
	// Live dressing-brush preview: render the scatter a click would place while
	// hovering in ModeDressing (cleared while stroking / off-tool). Independent of
	// the height/paint preview branch above.
	vr.updateDressPreview()
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
		if brush.Commit {
			vr.waterHistory = append(vr.waterHistory, editStroke{brush: brush})
		}
		// During the bulk replay just record the stroke; recomputeWater applies
		// the final (last-writer-wins) level once at the flush. Per-stroke
		// applyWaterLevel loops every tile's reloadWater — thousands of cgo
		// water-mesh rebuilds across the ~60k-stroke replay (see flushBulkReloads).
		if vr.bulkReplay {
			vr.waterRecomputePending = true
			return nil
		}
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
			// Lighting is last-writer-wins, so during the bulk replay just update
			// the state and apply ONCE at the flush. Applying per-sculpt fired ~20
			// Godot setters each; draining many in one frame overflowed graphics.gd's
			// command ring and deadlocked the load (the ring flush blocks waiting for
			// Godot, which can't run until Process returns). See flushBulkReloads.
			if vr.bulkReplay {
				vr.lightingApplyPending = true
			} else {
				vr.lighting.apply(vr.client)
			}
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
	// matching instances whose centers fall inside the disc. Newer tabs
	// (foliage, mineral) reuse the same deterministic scatter path and
	// history as grasses/pebbles.
	//
	// Category-clear tools (the "removal" tab) and the bomb emit with
	// Design==zero + negative Amount (Slider is either a real category or
	// the special ClearAllDressingCategory marker). They must route to
	// eraseGrass.
	isDressing := brush.Slider != "" &&
		(brush.Design != (musical.Design{}) ||
			(brush.Amount <= 0 && (isDressingCategory(brush.Slider) || brush.Slider == ClearAllDressingCategory)))
	if isDressing {
		// During a bulk replay, defer ALL grass scatter/erase to the flush
		// (recomputeGrass rebuilds it from grassHistory once). The per-stroke
		// scatterGrass creates/populates Godot MultiMesh nodes — thousands of cgo
		// calls that pressure graphics.gd's command ring during the replay. Just
		// record the stroke here; the bomb explosion is also skipped (it must not
		// re-fire when replaying the log).
		if vr.bulkReplay {
			if brush.Commit {
				vr.grassHistory = append(vr.grassHistory, editStroke{brush: brush})
			}
			return nil
		}
		if brush.Amount <= 0 {
			vr.eraseGrass(brush)
			if brush.Slider == ClearAllDressingCategory && brush.Commit && !brush.Revert &&
				(vr.client == nil || !vr.client.joining) {
				// Live bomb usage (by self or remote peers) triggers the visual explosion.
				// We deliberately skip it:
				// - on Revert (undo/redo)
				// - while the client is still replaying history during initial scene load/join
				//   (joining == true). This prevents old bombs from the log from exploding
				//   every time someone loads the world.
				t, r := brush.Target, brush.Radius
				Callable.Defer(Callable.New(func() {
					vr.triggerBombExplosion(t, r)
				}))
			}
		} else {
			vr.scatterGrass(brush)
		}
		if brush.Commit {
			vr.grassHistory = append(vr.grassHistory, editStroke{brush: brush})
		}
		return nil
	}
	// These five SetShaderParameter calls clear the live height-brush GHOST
	// preview after one's own committed stroke. During the bulk replay there is
	// no preview (3D rendering is suppressed under the splash) and the uniforms
	// default to 0 until the first live brush arms them — so firing them per
	// stroke is pure graphics.gd command-ring traffic for ~60k self-authored
	// sculpts (the very ring pressure that gates the load). Skip while replaying;
	// apply state to Godot at the flush, not per buffered mutation.
	if !vr.bulkReplay && brush.Author == vr.client.id {
		vr.shader.SetShaderParameter("height", 0.0)
		vr.shader.SetShaderParameter("river_carve", 0.0)
		vr.shader.SetShaderParameter("brush_mode", 0.0)
		vr.shader_buried.SetShaderParameter("height", 0.0)
		vr.shader_buried.SetShaderParameter("brush_mode", 0.0)
	}
	// A height sculpt (no Design, nonzero Amount — raise/lower or a river carve)
	// reshapes the ground, so anything sitting on the affected area must follow.
	// Snapshot each placed object's height-above-terrain against the CURRENT
	// surface before the tiles reload, so the deferred reprojection can re-seat
	// them on the new surface (see reprojectObjects).
	isHeight := brush.Design == (musical.Design{}) && (brush.Amount != 0 || isSpecialTerrainSlider(brush.Slider))
	// During bulk replay the terrain is deferred to a single flush, which re-seats
	// objects (the heightEdited object-snap in performReload) and grass
	// (refreshGrassVisibility) exactly once. So the per-stroke captureObjectHeights
	// + reprojectGrass/reprojectObjects below is pure redundant work here — and a
	// no-op anyway, since HeightAt reads the flat deferred field, so capture+reproject
	// restore the object's original Y. Skip it all while replaying; the flush gives
	// identical object/grass placement. (Live editing keeps the per-stroke path.)
	bulk := vr.bulkReplay
	var objectCaps []objectHeightCapture
	if isHeight && !bulk {
		cap := timeIn(&bucketTerrainCapture)
		objectCaps = vr.captureObjectHeights(brush.Target, brush.Radius)
		cap()
	}
	ti := timeIn(&bucketTilesIntersect)
	hitTiles := vr.tilesIntersecting(brush.Target, brush.Radius)
	ti()
	ts := timeIn(&bucketTerrainTiles)
	for _, tile := range hitTiles {
		tile.Sculpt(brush)
	}
	ts()
	if bulk {
		return nil
	}
	// Grass + placed objects that were sitting on the old surface must be
	// re-planted on the new one. Defer both so the tiles' deferred Reload has
	// refreshed their heights before we re-sample HeightAt/NormalAt.
	if isHeight && len(vr.grassPatches) > 0 {
		target, radius := brush.Target, brush.Radius
		Callable.Defer(Callable.New(func() {
			vr.reprojectGrass(target, radius)
		}))
	}
	if len(objectCaps) > 0 {
		Callable.Defer(Callable.New(func() {
			vr.reprojectObjects(objectCaps)
		}))
	}
	return nil
}

// objectHeightCapture records one placed object's height above the terrain at the
// moment a height sculpt (or its revert) begins. reprojectObjects re-seats the
// object at terrain + delta once the reshaped heights are in place, so objects
// ride raise/lower/river edits the same way grass does — keeping their relative
// offset (sitting flush, embedded, or floating). Like grass, this is a
// deterministic local consequence of the observable sculpt, applied identically
// on every client, so it needs no separate musical mutation.
type objectHeightCapture struct {
	id    Node3D.ID
	delta Float.X
}

// captureObjectHeights snapshots the height-above-terrain of every placed object
// whose XZ lies within the brush disc, sampled against the CURRENT (pre-sculpt)
// terrain. Call it before the tiles reload, then pair it with a deferred
// reprojectObjects so the new surface is in place when the offsets are
// re-applied. Objects at exactly the rim see no terrain change (the brush falloff
// is 0 there), so including them is harmless.
func (vr *TerrainEditor) captureObjectHeights(target Vector3.XYZ, radius Float.X) []objectHeightCapture {
	if vr.client == nil || radius <= 0 {
		return nil
	}
	r2 := float64(radius) * float64(radius)
	var caps []objectHeightCapture
	for _, id := range vr.client.entity_to_object {
		node, ok := id.Instance()
		if !ok {
			continue
		}
		pos := node.Position()
		dx := float64(pos.X - target.X)
		dz := float64(pos.Z - target.Z)
		if dx*dx+dz*dz > r2 {
			continue
		}
		// Measure against the committed base, not the live position: a hover
		// preview may have nudged node.Y by objectPreviewOffsets[id] (see
		// updateObjectPreview). Subtract it so the captured delta is the object's
		// true height above the real surface.
		base := pos.Y - vr.objectPreviewOffsets[id]
		caps = append(caps, objectHeightCapture{id: id, delta: base - vr.HeightAt(pos)})
	}
	return caps
}

// reprojectObjects re-seats each captured object on the freshly reshaped terrain,
// preserving the height-above-terrain recorded by captureObjectHeights. Only the
// Y is touched, so an object's X/Z (and any deliberate user lift) are kept exactly.
// Any live hover-preview offset is re-added so the node.Y == committedY + offset
// invariant survives the commit (the next Process frame then settles the preview).
func (vr *TerrainEditor) reprojectObjects(caps []objectHeightCapture) {
	for _, c := range caps {
		node, ok := c.id.Instance()
		if !ok {
			continue
		}
		pos := node.Position()
		pos.Y = vr.HeightAt(pos) + c.delta + vr.objectPreviewOffsets[c.id]
		node.SetPosition(pos)
	}
}

// setGrassBrushPreview pushes the live height-brush hover preview (centre XZ,
// strength, radius) to the grass shader's global uniforms so every grass blade
// rides the previewed surface on the GPU. mode mirrors the terrain shader's
// brush_mode (0 additive raise/lower/river, 1 plateau, 2 smooth): in mode 0
// `height` is the disc lift; in the flatten modes it is the strength fraction and
// the blade is pulled toward targetY (plateau with a flat top, smooth with a soft
// cone). height 0 (passed when no height brush is hovering) makes the shader's
// effect inert. See grass_wind.gdshader.
func (vr *TerrainEditor) setGrassBrushPreview(target Vector3.XYZ, radius, height Float.X, mode float64, targetY Float.X) {
	RenderingServer.GlobalShaderParameterSet("grass_brush_uplift", Vector2.New(target.X, target.Z))
	RenderingServer.GlobalShaderParameterSet("grass_brush_height", float64(height))
	RenderingServer.GlobalShaderParameterSet("grass_brush_radius", float64(radius))
	RenderingServer.GlobalShaderParameterSet("grass_brush_mode", mode)
	RenderingServer.GlobalShaderParameterSet("grass_brush_target_y", float64(targetY))
}

// updateObjectPreview is the CPU counterpart of setGrassBrushPreview for placed
// scenery objects (arbitrary meshes we don't own a shader for): each frame it
// nudges their node Y so they ride the live height-brush preview, matching
// terrain.gdshader's disc falloff (amount * (1 − d²/r²), surface floored at
// worldFloorY). It maintains the node.Y == committedY + offset invariant via
// objectPreviewOffsets, adjusting only by the change in offset so repeated frames
// (and an overlapping commit's reprojectObjects) never accumulate or drift.
// In flatten mode (plateau/smooth) `amount` is the strength fraction and each
// object is pulled toward targetY rather than lifted additively; flatTop selects
// the plateau's flat-topped falloff vs smooth's soft cone. Pass amount 0 to
// settle every object back to its committed position.
func (vr *TerrainEditor) updateObjectPreview(target Vector3.XYZ, radius, amount Float.X, flatten, flatTop bool, targetY Float.X) {
	if vr.client == nil {
		return
	}
	r2 := float64(radius) * float64(radius)
	if amount == 0 || r2 <= 0 {
		// Not previewing: only the already-displaced objects need settling, so
		// walk the (usually empty) offsets map rather than every placed object —
		// this path also runs every frame while OTHER editors are active.
		vr.restoreObjectPreview()
		return
	}
	for _, id := range vr.client.entity_to_object {
		node, ok := id.Instance()
		if !ok {
			// Node gone (e.g. deleted while previewing); drop its stale offset.
			delete(vr.objectPreviewOffsets, id)
			continue
		}
		old := vr.objectPreviewOffsets[id]
		var want Float.X
		pos := node.Position()
		dx := float64(pos.X - target.X)
		dz := float64(pos.Z - target.Z)
		d2 := dx*dx + dz*dz
		if d2 <= r2 {
			// Match the GPU exactly: the rendered surface shift at this object is the
			// triangle-interpolated per-vertex deformation (previewDisplacementAt), not
			// a pointwise mix(HeightAt, target, fac) — the latter drifts within a cell
			// (the per-vertex falloff is nonlinear) enough to float a prop on a steep
			// flatten. node.Y's old offset is moot since this is a pure XZ-keyed shift.
			if tile := vr.tileForWorld(pos); tile != nil {
				want = tile.previewDisplacementAt(pos, target, radius, amount, flatten, flatTop, targetY)
			}
		}
		if want == old {
			continue
		}
		pos.Y += want - old
		node.SetPosition(pos)
		if want == 0 {
			delete(vr.objectPreviewOffsets, id)
		} else {
			vr.objectPreviewOffsets[id] = want
		}
	}
}

// restoreObjectPreview settles every currently-displaced object back to its
// committed position (node.Y − offset) and empties the offsets map.
func (vr *TerrainEditor) restoreObjectPreview() {
	for id, off := range vr.objectPreviewOffsets {
		if node, ok := id.Instance(); ok && off != 0 {
			pos := node.Position()
			pos.Y -= off
			node.SetPosition(pos)
		}
		delete(vr.objectPreviewOffsets, id)
	}
}

// clearObjectPreview settles objects back to committed AND resets the grass
// shader preview. Called when leaving the terrain editor, where Process (which
// otherwise resets the preview each frame) stops running and would leave objects
// stranded at their previewed height.
func (vr *TerrainEditor) clearObjectPreview() {
	vr.restoreObjectPreview()
	vr.setGrassBrushPreview(Vector3.Zero, 0, 0, 0, 0)
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
	// During the bulk replay, a revert only toggles the stroke's reverted flag
	// and lets the flush recompute the affected subsystem ONCE from the
	// survivors — exactly like the forward strokes. The eager recomputes below
	// (recomputeWater loops every tile, recomputeGrass rebuilds the whole grass
	// set) are the same cgo bursts we defer on the forward path; firing them per
	// revert during a replay with many undo records was a measurable load cost.
	bulk := vr.bulkReplay
	switch {
	case brush.Slider == "editing/water_level":
		if revertEdit(vr.waterHistory, brush.Author, brush.Timing) {
			if bulk {
				vr.waterRecomputePending = true
			} else {
				vr.recomputeWater()
			}
		}
	case brush.Editor == "terrain" && (brush.Slider == extendSlider || brush.Slider == hideSlider):
		if revertEdit(vr.revealHistory, brush.Author, brush.Timing) {
			if bulk {
				vr.revealRecomputePending = true
			} else {
				vr.recomputeReveal()
			}
		}
	case brush.Slider != "" && (brush.Design != (musical.Design{}) ||
		isDressingCategory(brush.Slider) || brush.Slider == ClearAllDressingCategory):
		if revertEdit(vr.grassHistory, brush.Author, brush.Timing) {
			// Grass is already recomputed once at the flush (recomputeGrass folds
			// the surviving grassHistory); toggling the flag above is enough.
			if !bulk {
				vr.recomputeGrass()
			}
		}
	default:
		// Tile op (height / paint / river): toggle the stroke in every tile it
		// touched and recompute those tiles from their surviving history.
		// Snapshot object heights against the current surface before the recompute
		// so a reverted height change re-seats them on the restored surface
		// (mirrors the forward path in Sculpt; see reprojectObjects). Skipped
		// during the bulk replay where the deferred per-tile flush re-seats objects
		// anyway (the capture would read the flat deferred height — pure redundant
		// work, matching the forward bulk path).
		var objectCaps []objectHeightCapture
		if !bulk {
			objectCaps = vr.captureObjectHeights(brush.Target, brush.Radius)
		}
		reverted := false
		for _, tile := range vr.tilesIntersecting(brush.Target, brush.Radius) {
			if tile.revert(brush.Author, brush.Timing) {
				tile.recompute() // during bulk this only records the tile in pendingReload
				reverted = true
			}
		}
		// A height change moves grass + placed objects; re-seat them after the
		// tiles recompute (deferred so the new heights are in place), mirroring
		// the forward path. During bulk the flush re-seats both once.
		if !bulk && reverted && len(vr.grassPatches) > 0 {
			target, radius := brush.Target, brush.Radius
			Callable.Defer(Callable.New(func() {
				vr.reprojectGrass(target, radius)
			}))
		}
		if !bulk && reverted && len(objectCaps) > 0 {
			Callable.Defer(Callable.New(func() {
				vr.reprojectObjects(objectCaps)
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
			// Single-shot raise/lower commit on release. Plateau/smooth are drag-paint
			// (PaintTerrainSculpt) and never arm BrushActive, so they don't reach here.
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
// appear in the "terrain" tab under ModeGeometry (raise/lower/river) or
// the "removal" tab under ModeDressing (category erasers for dressing).
const (
	BuiltinTerrainRaise      = "procedural://terrain/raise"
	BuiltinTerrainLower      = "procedural://terrain/lower"
	BuiltinTerrainPlateau    = "procedural://terrain/plateau"
	BuiltinTerrainSmooth     = "procedural://terrain/smooth"
	BuiltinTerrainRiver      = "procedural://terrain/river"
	BuiltinTerrainRiverErase = "procedural://terrain/river_erase"

	// Dressing category clearers (armed from the "removal" tab in ModeDressing).
	// These arm a brush that emits negative-Amount sculpts for the whole category
	// (all designs under that Slider), replacing the old Ctrl+Shift hack.
	BuiltinDressingClearGrasses = "procedural://dressing/clear_grasses"
	BuiltinDressingClearFoliage = "procedural://dressing/clear_foliage"
	BuiltinDressingClearShrooms = "procedural://dressing/clear_shrooms"
	BuiltinDressingClearBoulder = "procedural://dressing/clear_boulder"

	// BuiltinDressingClearAll (the "bomb") clears every dressing category at once.
	BuiltinDressingClearAll = "procedural://dressing/clear_all"
)

// ClearAllDressingCategory is the special Slider value carried by bomb sculpts.
// When a negative-Amount sculpt arrives with this Slider and Design==zero,
// eraseGrass removes instances from *all* categories inside the brush disc.
const ClearAllDressingCategory = "*"

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

// plateauSlider / smoothSlider tag Sculpt entries for the new terrain brushes.
// Amount is deliberately left 0 (and thus often absent from the wire record)
// so that older clients decode them as a no-op height stroke (+0) and never
// corrupt a world that contains plateau/smooth edits. The brush strength is
// carried in Orient; for plateau the click-point surface height is the
// already-present Target.Y.
const (
	plateauSlider = "terrain/plateau"
	smoothSlider  = "terrain/smooth"
)

func isSpecialTerrainSlider(s string) bool {
	return s == plateauSlider || s == smoothSlider
}

// plateauStrengthScale / smoothStrengthScale map a special terrain brush's power
// (BrushPower, the GizmoPower slider; range 0.1–10, default 2) to the convergence
// fraction one pass applies at the disc centre. Plateau is strong — at the default
// power a single click fully flattens the disc centre to the click height — so it
// shapes mesas in a click or two. Smooth is lighter per pass but runs
// smoothIterations passes, easing broad bumps without snapping them to one level.
const (
	plateauStrengthScale float32 = 0.5
	smoothStrengthScale  float32 = 0.3
)

// plateauFlatCore is the fraction of the brush radius that the plateau brush
// holds DEAD FLAT at the target height; beyond it the disc eases back to the
// original terrain over the remaining skirt. A large core gives the harsh,
// flat-topped mesa a plateau should make (vs the soft dome a plain 1−d²/r² cone
// produces). It is replicated as PLATEAU_FLAT_CORE in terrain/buried/grass_wind
// shaders so the committed flatten, the hover preview, and the props riding it
// all agree — keep them in sync. Inside the flat core every point (vertices,
// grass, objects) clamps to exactly the target, so nothing drifts there.
const plateauFlatCore float32 = 0.6

// plateauFalloff is the plateau brush's flat-topped weight profile: 1 across the
// flat core, eased to 0 at the rim with a smoothstep skirt. t is the normalised
// distance from the brush centre (d/radius, in [0,1]). Shared by the committed
// apply and the CPU object preview; the shaders inline the identical curve.
func plateauFalloff(t float32) float32 {
	if t <= plateauFlatCore {
		return 1
	}
	if t >= 1 {
		return 0
	}
	u := (t - plateauFlatCore) / (1 - plateauFlatCore) // 0 at core edge, 1 at rim
	return 1 - u*u*(3-2*u)                             // 1 − smoothstep(core,1,t)
}

// terrainSculptFactor converts a plateau/smooth stroke's stored strength (the
// raw brush power carried in Orient) into the centre convergence fraction for the
// given scale, clamped to [0,1]. Shared by the committed apply (per-tile Reload)
// and the hover preview (the shader `height` uniform) so the previewed deformation
// matches exactly what a click commits. Smooth uses the smaller smoothStrengthScale
// (a softer pull toward the disc mean, since a drag re-applies it many times);
// plateau the larger plateauStrengthScale (full clamp to the level in one click).
func terrainSculptFactor(power Float.X, scale float32) float32 {
	f := float32(power) * scale
	if f < 0 {
		f = 0
	}
	if f > 1 {
		f = 1
	}
	return f
}

// discGroundMean returns the mean original-ground height over the brush disc,
// sampled across every intersecting tile's groundHeights. The smooth brush stores
// this in its Sculpt.Target.Y and pulls the ground toward it — ONE shared scalar,
// so the deformation is a pure function of world position (like plateau) and thus
// SEAMLESS across tile boundaries and reproducible on recompute, unlike a
// per-vertex neighbour average (which each tile would compute differently at a
// shared edge, tearing the seam). Returns fallback if the disc covers no
// generated tile.
func (tr *TerrainEditor) discGroundMean(target Vector3.XYZ, radius, fallback Float.X) Float.X {
	r2 := float64(radius) * float64(radius)
	if r2 <= 0 {
		return fallback
	}
	var sum float64
	var count int
	for _, tile := range tr.tilesIntersecting(target, radius) {
		n := tile.size
		if n == 0 {
			n = terrainDefaultSize
		}
		hm := n + 1
		if len(tile.groundHeights) < hm*hm {
			continue // not generated yet
		}
		half := float64(n) / 2
		ox := float64(tile.coord.X*n) - half
		oz := float64(tile.coord.Z*n) - half
		for gz := 0; gz <= n; gz++ {
			dz := oz + float64(gz) - float64(target.Z)
			for gx := 0; gx <= n; gx++ {
				dx := ox + float64(gx) - float64(target.X)
				if dx*dx+dz*dz > r2 {
					continue
				}
				sum += float64(tile.groundHeights[gx+gz*hm])
				count++
			}
		}
	}
	if count == 0 {
		return fallback
	}
	return Float.X(sum / float64(count))
}

// specialTerrainBrushActive reports whether the plateau or smooth brush is armed.
// These are drag-paint tools (PaintTerrainSculpt), unlike the single-shot
// raise/lower brushes, so they share the river/dressing continuous-stroke path.
func (tr *TerrainEditor) specialTerrainBrushActive() bool {
	return tr.TerrainBrush == BuiltinTerrainPlateau || tr.TerrainBrush == BuiltinTerrainSmooth
}

// PaintTerrainSculpt commits one segment of a plateau/smooth drag stroke as a
// musical.Sculpt (so every client reproduces it). Called throttled from the
// client loop while the left button is held — mirroring PaintRiver. The plateau
// target height is LOCKED at the stroke's start (sculptLockY) and reused for every
// segment, so dragging carves one continuous flat terrace at the height the
// preview showed rather than chasing the terrain under each segment. Smooth needs
// no lock (it pulls toward the local neighbour average; Target.Y is unused).
func (tr *TerrainEditor) PaintTerrainSculpt() {
	if !tr.specialTerrainBrushActive() || tr.client == nil {
		return
	}
	if !tr.sculptStroke {
		// Stroke start: lock the plateau level and commit the first segment now.
		tr.sculptStroke = true
		tr.sculptLockY = tr.BrushTarget.Y
		tr.sculptLast = tr.BrushTarget
	} else {
		// Movement spacing (shared idiom with dressing/river): emit only once the
		// brush has moved ~half a radius, so a stationary hold doesn't spam segments.
		dx := tr.BrushTarget.X - tr.sculptLast.X
		dz := tr.BrushTarget.Z - tr.sculptLast.Z
		spacing := tr.BrushRadius * 0.5
		if dx*dx+dz*dz < spacing*spacing {
			return
		}
		tr.sculptLast = tr.BrushTarget
	}
	s := musical.Sculpt{
		Author: tr.client.id,
		Target: tr.BrushTarget,
		Radius: tr.BrushRadius,
		Orient: Angle.Radians(tr.BrushPower),
		Commit: true,
	}
	switch tr.TerrainBrush {
	case BuiltinTerrainPlateau:
		s.Slider = plateauSlider
		s.Target.Y = tr.sculptLockY // flatten to the locked level, not the live terrain
	case BuiltinTerrainSmooth:
		s.Slider = smoothSlider
		// Pull toward the disc's mean ground height (a single shared scalar → seamless
		// across tiles). Recomputed per segment, so a drag follows the terrain's
		// large-scale shape while shaving local bumps toward the local average.
		s.Target.Y = tr.discGroundMean(tr.BrushTarget, tr.BrushRadius, tr.BrushTarget.Y)
	}
	tr.client.commitSculpt(s)
}

// terrainBrushDelta returns the signed height amount one click applies for the
// given mouse button for the SINGLE-SHOT raise/lower brushes: the "raise" tool
// makes the primary button (LMB) raise terrain, "lower" makes it lower; the
// secondary button (RMB) inverts. The magnitude is the GizmoPower strength
// (BrushPower), applied in one shot. Plateau/smooth are drag-paint
// (PaintTerrainSculpt), not single-shot, so they're deliberately absent here.
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
	Water MeshInstance3D.Instance
	// Sides holds the vertical rock skirt (terrain side walls) on exposed
	// cardinal edges. It lives on its own MeshInstance3D so it can disable
	// both shadow casting and shadow receiving independently of the top
	// terrain surface (which must continue to cast and receive normally).
	Sides       MeshInstance3D.Instance
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
	// ground is the per-vertex original ground height (groundHeights, i.e. the
	// terrain as if no river had been carved), uploaded as CUSTOM2.r. The
	// river-erase preview reads it to raise the bed back toward the original
	// ground inside the brush disc (see terrain.gdshader's river_fill block).
	ground []float32

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

// sculptOrder is the deterministic total order over committed strokes: ascending
// by Timing (wall-clock-ns, so chronological), with Author breaking exact-Timing
// ties. Returns <0 if a precedes b. Every client folds its history in this order,
// so terrain converges regardless of the order parts/peers deliver strokes in.
func sculptOrder(a, b musical.Sculpt) int {
	switch {
	case a.Timing < b.Timing:
		return -1
	case a.Timing > b.Timing:
		return 1
	case a.Author < b.Author:
		return -1
	case a.Author > b.Author:
		return 1
	default:
		return 0
	}
}

func (tile *TerrainTile) Sculpt(brush musical.Sculpt) {
	// History is kept in canonical sculptOrder so the folded result is a pure
	// function of the SET of strokes, independent of arrival/part order.
	//
	// During bulk replay we append unsorted and sort the whole history once in
	// flushBulkReloads (O(n log n)) rather than insert-sorting each of ~60k
	// strokes (O(n²)); the rebuild is deferred there too. In the bulk window a
	// paint design's imported texture ID is only briefly resolvable, so
	// uploadDesign must copy its image NOW (the live path does this in
	// performReload, when the texture is still up).
	if tile.editor != nil && tile.editor.bulkReplay {
		tile.history = append(tile.history, tileStroke{brush: brush})
		tile.sculpts = append(tile.sculpts, brush)
		if brush.Design != (musical.Design{}) {
			tile.editor.uploadDesign(brush.Design)
		}
		tile.Reload()
		return
	}
	// Live: insert at the sorted position. A stroke at the end is the newest (the
	// common case) and folds forward incrementally; one landing earlier is an
	// out-of-order arrival the forward accumulators can't express, so rebuild from
	// the survivors in order.
	idx := sort.Search(len(tile.history), func(i int) bool {
		return sculptOrder(tile.history[i].brush, brush) > 0
	})
	tile.history = slices.Insert(tile.history, idx, tileStroke{brush: brush})
	if idx == len(tile.history)-1 {
		tile.sculpts = append(tile.sculpts, brush)
		tile.Reload()
	} else {
		tile.recompute()
	}
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

// recomputeNormals fills tile.normals from the current heightfield so the
// terrain shades by its real slope (hills catch and lose the sun) instead of
// being lit as a flat plane. Each of the 6 duplicated vertices in a cell keys
// off its grid point and gets that point's heightfield-gradient normal — the
// same (-dh/dx, 1, -dh/dz) convention NormalAt uses for object placement — so
// coincident vertices share a normal and the surface shades smoothly. Samples
// one cell beyond the tile border read the neighbouring tile via the editor's
// cross-tile lookup so normals stay continuous across tile seams; with no
// neighbour there (world edge / unrevealed tile) they clamp to the border.
func (tile *TerrainTile) recomputeNormals() {
	n := tile.size
	hm := n + 1
	if len(tile.normals) != n*n*6 || len(tile.heights) != hm*hm {
		return
	}
	origin := tile.Mesh.AsNode3D().GlobalPosition()
	h := func(gx, gy int) Float.X {
		if gx >= 0 && gx <= n && gy >= 0 && gy <= n {
			return Float.X(tile.heights[gx+gy*hm])
		}
		// Ghost cell beyond this tile's edge: read the neighbour so the gradient
		// (and thus the normal) is continuous across the seam.
		if tile.editor != nil {
			world := Vector3.Add(origin, Vector3.XYZ{X: Float.X(gx), Z: Float.X(gy)})
			if nb := tile.editor.tileForWorld(world); nb != nil && nb != tile && len(nb.heights) == (nb.size+1)*(nb.size+1) {
				return nb.HeightAt(world)
			}
		}
		// No neighbour: clamp to this tile's border so the edge normal stays flat
		// there rather than diving toward a phantom 0 height off the world's rim.
		cx, cy := gx, gy
		if cx < 0 {
			cx = 0
		} else if cx > n {
			cx = n
		}
		if cy < 0 {
			cy = 0
		} else if cy > n {
			cy = n
		}
		return Float.X(tile.heights[cx+cy*hm])
	}
	// One normal per grid vertex (computed once), then scattered to the 6
	// duplicated triangle vertices per cell. n = normalize(-dh/dx, 1, -dh/dz);
	// central differences over a 1-unit cell give 2·dh/dx = h(x+1)-h(x-1), and the
	// constant factor drops under normalize, so build (h(x-1)-h(x+1), 2, h(y-1)-h(y+1)).
	grid := make([]Vector3.XYZ, hm*hm)
	for gy := 0; gy <= n; gy++ {
		for gx := 0; gx <= n; gx++ {
			grid[gx+gy*hm] = Vector3.Normalized(Vector3.XYZ{
				X: h(gx-1, gy) - h(gx+1, gy),
				Y: 2,
				Z: h(gx, gy-1) - h(gx, gy+1),
			})
		}
	}
	for x := 0; x < n; x++ {
		for y := 0; y < n; y++ {
			base := 6 * (x + n*y)
			tile.normals[base+0] = grid[x+y*hm]
			tile.normals[base+1] = grid[(x+1)+y*hm]
			tile.normals[base+2] = grid[x+(y+1)*hm]
			tile.normals[base+3] = grid[(x+1)+y*hm]
			tile.normals[base+4] = grid[(x+1)+(y+1)*hm]
			tile.normals[base+5] = grid[x+(y+1)*hm]
		}
	}
}

// generateBase mesh, textures and the collision shape, these will change whenever a [musical.Sculpt] arrives.
func (tile *TerrainTile) generateBase() {
	defer timeIn(&bucketGenerateBase)()
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
	tile.ground = make([]float32, n*n*6) // CUSTOM2.r: original ground per vertex
	inv := Float.X(1) / Float.X(n)
	add := func(index int, x, y int, w1, w2, w3, w4 Float.X) {
		tile.vertices[index] = Vector3.XYZ{Float.X(x), Float.X(tile.heights[x+y*hm]), Float.X(y)}
		tile.normals[index] = Vector3.XYZ{0, 1, 0}
		tile.uvs[index] = Vector2.XY{Float.X(x) * inv, Float.X(y) * inv}
		tile.weights[index*4] = float32(w1)
		tile.weights[index*4+1] = float32(w2)
		tile.weights[index*4+2] = float32(w3)
		tile.weights[index*4+3] = float32(w4)
		tile.ground[index] = tile.groundHeights[x+y*hm]
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
	// Replace the flat seed normals with real heightfield-gradient normals so the
	// terrain self-shades by slope (see recomputeNormals).
	wasted := timeIn(&bucketGenBaseWaste)
	tile.recomputeNormals()
	attributes := [Mesh.ArrayMax]any{
		Mesh.ArrayVertex:  tile.vertices,
		Mesh.ArrayTexUv:   tile.uvs,
		Mesh.ArrayNormal:  tile.normals,
		Mesh.ArrayCustom0: tile.textures,
		Mesh.ArrayCustom1: tile.weights,
		Mesh.ArrayCustom2: tile.ground,
	}
	mesh.MoreArgs().AddSurfaceFromArrays(Mesh.PrimitiveTriangles, attributes[:], nil, nil,
		Mesh.ArrayFormatVertex|
			Mesh.ArrayFormat(Mesh.ArrayCustomRgbaFloat)<<Mesh.ArrayFormatCustom0Shift|
			Mesh.ArrayFormat(Mesh.ArrayCustomRgbaFloat)<<Mesh.ArrayFormatCustom1Shift|
			Mesh.ArrayFormat(Mesh.ArrayCustomRFloat)<<Mesh.ArrayFormatCustom2Shift,
	)
	// Regenerate tangents for the now-varying normals so the terrain normal map
	// still applies correctly on slopes (matches the recompute path).
	mesh.RegenNormalMaps()
	wasted()
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
	// Dedicated sides MeshInstance child for the rock skirt. Positioned
	// identically so local coordinates match the terrain top and water.
	// We immediately disable shadow casting; the buried shader will also
	// suppress shadow *receiving* via a custom light() function.
	if tile.Sides == (MeshInstance3D.Instance{}) {
		tile.Sides = MeshInstance3D.New()
		tile.Sides.AsNode().SetName("Sides")
		tile.AsNode().AddChild(tile.Sides.AsNode())
	}
	tile.Sides.AsNode3D().SetPosition(Vector3.New(-half, 0, -half))
	tile.Sides.AsGeometryInstance3D().SetCastShadow(GeometryInstance3D.ShadowCastingSettingOff)

	// Set the material override early (safe even with no mesh yet).
	// We deliberately avoid SetSurfaceOverrideMaterial here because the
	// internal surface_override_materials array is still size 0 and would
	// produce "Index p_surface = 0 is out of bounds" errors.
	if tile.side_shader != (ShaderMaterial.Instance{}) {
		mat := tile.side_shader.AsMaterial()
		tile.Sides.AsGeometryInstance3D().SetMaterialOverride(mat)
	}

	// Texture arrays are owned by the editor (shared across tiles).
	wasted2 := timeIn(&bucketGenBaseWaste)
	tile.reloadSides()
	tile.reloadWater()
	wasted2()
	// Sync visibility + pickability to the reveal state (tiles start hidden;
	// applyRevealState also gates the water mesh on revealed && waterVisible).
	tile.applyRevealState()
}

// Reload folds any pending sculpt batch into the tile — the fast, incremental
// forward path taken on every edit.
func (tile *TerrainTile) Reload() { tile.scheduleReload(false) }

// debugTerrainSignature logs an order-independent checksum of every generated
// tile's heights and texture indices. Two loads of the same world must produce
// identical sums once history is folded in the canonical (Timing, Author) order —
// a determinism check for the merge.
func (vr *TerrainEditor) debugTerrainSignature() {
	var hSum, tSum float64
	var nH, nT, tiles int
	for _, tile := range vr.tiles {
		if !tile.generated {
			continue
		}
		tiles++
		for _, h := range tile.heights {
			hSum += float64(h)
			nH++
		}
		for _, t := range tile.textures {
			tSum += float64(t)
			nT++
		}
	}
	var instances int
	for _, p := range vr.grassPatches {
		instances += len(p.bases)
	}
	// Merged rendering: one grassRender (a handful of MultiMeshInstance3D nodes)
	// per unique design, vs. the old one-set-per-patch. mmNodes is the total
	// MultiMeshInstance3D count actually in the scene.
	var mmNodes int
	for _, r := range vr.grassRenders {
		mmNodes += len(r.mmNodes)
	}
	profMark("[sig] tiles=%d heightSum=%.4f (%d) textureSum=%.0f (%d) | grassPatches=%d instances=%d designs=%d mmNodes=%d",
		tiles, hSum, nH, tSum, nT, len(vr.grassPatches), instances, len(vr.grassRenders), mmNodes)
	// What ARE the paint designs in the mapper? (answer: preview vs library vs region)
	if vr.client != nil {
		byCat := map[string]int{}
		unique := map[string]bool{}
		var sample []string
		for d := range vr.mapper {
			uri := vr.client.design_to_string[d]
			unique[uri] = true
			lu := strings.ToLower(uri)
			cat := "other"
			switch {
			case strings.Contains(lu, "preview/"):
				cat = "preview"
			case strings.HasSuffix(lu, ".region"):
				cat = "region"
			case strings.Contains(lu, "wildfire_games/terrain"):
				cat = "terrain"
			case strings.Contains(lu, "/texture/"):
				cat = "texture-md5"
			}
			byCat[cat]++
			if len(sample) < 6 {
				sample = append(sample, uri)
			}
		}
		profMark("[mapper] designs=%d uniquePaths=%d albedos=%d byCategory=%v", len(vr.mapper), len(unique), len(vr.albedos), byCat)
		for _, s := range sample {
			profMark("[mapper] sample: %s", s)
		}
	}
}

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
	// During the initial bulk replay we suppress per-frame rebuilds entirely:
	// just remember the touched tile and rebuild every pending tile exactly once
	// at the end (flushBulkReloads). The final rebuild is always a full recompute
	// over the active history, so accumulating the strokes here (tile.Sculpt
	// already appended them) without a forward fold is correct.
	if tile.editor != nil && tile.editor.bulkReplay {
		if tile.editor.pendingReload == nil {
			tile.editor.pendingReload = map[*TerrainTile]struct{}{}
		}
		tile.editor.pendingReload[tile] = struct{}{}
		return
	}
	if tile.reloading {
		return // we only want to reload once per frame.
	}
	tile.reloading = true
	Callable.Defer(Callable.New(tile.performReload))
}

// performReload rebuilds the tile's heightfield, mesh, normals, collision, side
// walls and water from its stroke history. Normally invoked end-of-frame via
// scheduleReload's Callable.Defer; flushBulkReloads calls it directly (twice) at
// the end of a bulk replay.
func (tile *TerrainTile) performReload() {
	{
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
		// Height strokes reshape the original-ground field — the terrain as if no
		// river had ever been dug (the river's water-surface reference "where the
		// terrain used to be"). Two kinds share this single pass because both
		// mutate groundHeights and so MUST be replayed in chronological order
		// relative to each other (a plateau flattens whatever raise/lower strokes
		// preceded it; running them in separate grouped passes made undo/redo,
		// which replays history, reshape the terrain differently than live
		// editing): raise/lower add a falloff bump, plateau/smooth pull the ground
		// toward a target. River strokes are a separate channel (riverDepth),
		// applied below so they overwrite/erase rather than add.
		//
		// The -2 floor on raise/lower is applied PER STROKE (not to an accumulated
		// per-batch sum) so reverting one stroke removes exactly the contribution
		// it added, and a from-scratch recompute over the surviving strokes matches
		// the incremental forward path stroke for stroke.
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
			// Plateau / smooth: convergent pull toward a SINGLE target height carried
			// in Target.Y (Amount stays 0 for back-compat). Plateau's target is the
			// locked click level (a flat-topped mesa); smooth's is the mean ground
			// height over the disc, computed once at commit (see discGroundMean) so it
			// reduces local relief toward the local average. Using one shared scalar —
			// rather than a per-vertex neighbour average — keeps the result a pure
			// function of world position, so adjacent tiles agree exactly at a shared
			// edge (no seam tear) and a recompute reproduces it. Falloff differs:
			// plateau holds a flat top (plateauFalloff), smooth uses a soft 1−d²/r²
			// cone. Strength = terrainSculptFactor(Orient), shared with the preview.
			if isSpecialTerrainSlider(sculpt.Slider) {
				smooth := sculpt.Slider == smoothSlider
				scale := plateauStrengthScale
				if smooth {
					scale = smoothStrengthScale
				}
				strength := terrainSculptFactor(Float.X(sculpt.Orient), scale)
				if strength <= 0 {
					continue
				}
				targetH := float32(sculpt.Target.Y)
				for i := 0; i < hm*hm; i++ {
					pos := Vector3.Add(Vector3.XYZ{Float.X(i % hm), 0, Float.X(i / hm)}, offset)
					dx := pos.X - sculpt.Target.X
					dy := pos.Z - sculpt.Target.Z
					d2 := dx*dx + dy*dy
					if d2 > r2 {
						continue
					}
					var ff float32
					if smooth {
						ff = float32(1 - d2/r2)
					} else {
						ff = plateauFalloff(float32(Float.Sqrt(d2) / sculpt.Radius))
					}
					fac := strength * ff
					if fac <= 0 {
						continue
					}
					if fac > 1 {
						fac = 1
					}
					tile.groundHeights[i] = tile.groundHeights[i]*(1-fac) + targetH*fac
				}
				continue
			}
			// Raise / lower: additive falloff bump.
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
		if len(tile.ground) != n*n*6 {
			tile.ground = make([]float32, n*n*6)
		}
		update := func(index int, cell_x, cell_y int, x, y int) {
			tile.vertices[index].Y = tile.heights[x+y*hm]
			tile.uvs[index] = Vector2.XY{Float.X(x) * inv, Float.X(y) * inv}
			tile.ground[index] = tile.groundHeights[x+y*hm]
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
		// Re-derive the slope normals from the edited heights so the relit terrain
		// shades by its new shape (RegenNormalMaps below rebuilds tangents to suit).
		tile.recomputeNormals()
		tile.heightmapShape.SetMapData(tile.heights)
		attributes := [Mesh.ArrayMax]any{
			Mesh.ArrayVertex:  tile.vertices,
			Mesh.ArrayTexUv:   tile.uvs,
			Mesh.ArrayNormal:  tile.normals,
			Mesh.ArrayCustom0: tile.textures,
			Mesh.ArrayCustom1: tile.weights,
			Mesh.ArrayCustom2: tile.ground,
		}
		mesh := Object.To[ArrayMesh.Instance](tile.Mesh.Mesh())
		mesh.ClearSurfaces()
		mesh.MoreArgs().AddSurfaceFromArrays(Mesh.PrimitiveTriangles, attributes[:], nil, nil,
			Mesh.ArrayFormatVertex|
				Mesh.ArrayFormat(Mesh.ArrayCustomRgbaFloat)<<Mesh.ArrayFormatCustom0Shift|
				Mesh.ArrayFormat(Mesh.ArrayCustomRgbaFloat)<<Mesh.ArrayFormatCustom1Shift|
				Mesh.ArrayFormat(Mesh.ArrayCustomRFloat)<<Mesh.ArrayFormatCustom2Shift,
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
	}
}

// flushBulkReloads ends a bulk replay (see scheduleReload) by rebuilding every
// tile that was touched, exactly once each, instead of the per-frame rebuild
// that ran while bulkReplay was set. Two passes: the first folds each tile's
// full stroke history into its heightfield + mesh; the second re-derives the
// geometry (normals, side walls, water) now that every tile's heights are final,
// so cross-tile seam normals sample neighbours' final surface rather than a
// stale one. Called from finishLoading before 3D rendering resumes.
func (vr *TerrainEditor) flushBulkReloads() {
	if !vr.bulkReplay {
		return
	}
	vr.bulkReplay = false
	pending := vr.pendingReload
	vr.pendingReload = nil
	// Apply the coalesced final lighting state once (see the environment branch in
	// Sculpt) — replaces the per-sculpt apply that overflowed the command ring.
	if vr.lightingApplyPending {
		vr.lightingApplyPending = false
		vr.lighting.apply(vr.client)
	}
	// Build the shared paint-texture arrays once, now that every design seen
	// during the replay has been registered (uploadDesign skipped the per-design
	// rebuild while bulkReplay was set).
	vr.uploadTextureArrays()
	// Sort each touched tile's history into the canonical (Timing, Author) order
	// before the from-scratch rebuild, so the loaded terrain is independent of the
	// order the device parts were concatenated in. SortStable keeps the original
	// (append) order among strokes that share a key — e.g. legacy strokes with an
	// unset Timing — so old single-part saves are unaffected.
	for tile := range pending {
		slices.SortStableFunc(tile.history, func(a, b tileStroke) int {
			return sculptOrder(a.brush, b.brush)
		})
	}
	// Pass 1: restore the rasterised <=Cutoff grids from a valid snapshot and fold
	// only the newer strokes; otherwise full recompute (reset + replay history).
	restored := false
	if snapshotEnabled && vr.client != nil {
		if snap, err := readTerrainSnapshot(vr.client.record); err == nil {
			restored = vr.applySnapshot(snap, pending)
		}
	}
	if !restored {
		for tile := range pending {
			tile.fullReload = true
			tile.reloading = false
			tile.performReload()
		}
	}
	// Pass 2: geometry-only rebuild (fullReload already cleared, sculpts drained)
	// against now-final neighbour heights.
	for tile := range pending {
		tile.reloading = false
		tile.performReload()
	}
	// Water level + tile reveal/hide were deferred during the replay too (forward
	// strokes recorded to history, reverts only toggled the reverted flag). Apply
	// their final state once now that every tile's heights are rebuilt:
	// recomputeWater re-derives the last-writer-wins level and reloads each tile's
	// water surface against the final heights; recomputeReveal replays the
	// surviving extend/hide strokes. Both are no-ops if nothing was deferred.
	// MUST run BEFORE recomputeGrass — grass is filtered to instances on REVEALED
	// tiles, so the reveal set has to be final before grass scatters (otherwise a
	// reverted reveal leaves that tile's blades scattered).
	if vr.waterRecomputePending {
		vr.waterRecomputePending = false
		vr.recomputeWater()
	}
	if vr.revealRecomputePending {
		vr.revealRecomputePending = false
		vr.recomputeReveal()
	}

	// Grass scatter was deferred during the replay (see the dressing branch in
	// Sculpt): build every patch now, once, from grassHistory. The terrain is
	// already rebuilt above, so grassTransform samples the final HeightAt and each
	// blade lands on the real surface.
	vr.recomputeGrass()

	// Bake a fresh snapshot of the just-built terrain so the NEXT load can skip
	// re-folding these strokes (see snapshot.go).
	if snapshotEnabled && vr.client != nil {
		if snap := vr.buildSnapshot(pending); snap != nil {
			if err := writeTerrainSnapshot(vr.client.record, snap); err != nil {
				profMark("snapshot: write failed: %v", err)
			} else {
				profMark("snapshot: wrote cutoff=%d %d strokes %d tiles", snap.Cutoff, snap.StrokeCount, len(snap.Tiles))
			}
		}
	}
}

// hashActiveStrokes is the order-independent digest (XOR of per-stroke mixes) +
// count of the active (non-reverted) strokes with Timing <= cutoff across the
// touched tiles. A snapshot is valid iff this matches what was baked.
func (vr *TerrainEditor) hashActiveStrokes(pending map[*TerrainTile]struct{}, cutoff musical.Timing) (uint64, int) {
	var h uint64
	var n int
	for tile := range pending {
		for _, s := range tile.history {
			if s.reverted || s.brush.Timing > cutoff {
				continue
			}
			h ^= strokeHashMix(s.brush)
			n++
		}
	}
	return h, n
}

// applySnapshot validates snap against the current strokes and, if it matches,
// restores each tile's grids and folds only the strokes newer than the cutoff.
// Returns false (→ caller does a full replay) on any mismatch.
func (vr *TerrainEditor) applySnapshot(snap *terrainSnapshot, pending map[*TerrainTile]struct{}) bool {
	h, n := vr.hashActiveStrokes(pending, snap.Cutoff)
	if h != snap.StrokeHash || n != snap.StrokeCount {
		profMark("snapshot: stale (hash/count %x/%d vs %x/%d) — full replay", h, n, snap.StrokeHash, snap.StrokeCount)
		return false
	}
	// Map each snapshot texture layer to this session's layer for the same Design
	// (uploadDesign registers it if new); layer 0 is the untextured base.
	translate := make([]float32, len(snap.LayerDesigns))
	for i, d := range snap.LayerDesigns {
		if d != (musical.Design{}) {
			translate[i] = float32(vr.uploadDesign(d))
		}
	}
	vr.uploadTextureArrays()
	snapByCoord := make(map[tileCoord]*tileSnapshot, len(snap.Tiles))
	for i := range snap.Tiles {
		t := &snap.Tiles[i]
		snapByCoord[tileCoord{t.X, t.Z}] = t
	}
	for tile := range pending {
		st, ok := snapByCoord[tile.coord]
		if !ok || !tile.restoreFromSnapshot(st, translate, snap.Cutoff) {
			// New / mismatched tile: fold it fully from history.
			tile.fullReload = true
			tile.reloading = false
			tile.performReload()
		}
	}
	profMark("snapshot: restored cutoff=%d %d strokes %d tiles", snap.Cutoff, n, len(snap.Tiles))
	return true
}

// restoreFromSnapshot loads the cached accumulators (remapping texture layers)
// and folds only this tile's strokes newer than cutoff onto them. Returns false
// on a shape mismatch so the caller falls back to a full fold.
func (tile *TerrainTile) restoreFromSnapshot(st *tileSnapshot, translate []float32, cutoff musical.Timing) bool {
	if !tile.generated {
		tile.generateBase()
	}
	n := tile.size
	hm := n + 1
	if st.Size != n || len(st.GroundHeights) != hm*hm || len(st.RiverDepth) != hm*hm ||
		len(st.Textures) != n*n*6*4 {
		return false
	}
	tile.revealed = st.Revealed
	copy(tile.groundHeights, st.GroundHeights)
	copy(tile.riverDepth, st.RiverDepth)
	copy(tile.waterFlowX, st.WaterFlowX)
	copy(tile.waterFlowZ, st.WaterFlowZ)
	if len(tile.heights) == len(st.Heights) {
		copy(tile.heights, st.Heights)
	}
	if len(tile.textures) != len(st.Textures) {
		tile.textures = make([]float32, len(st.Textures))
	}
	for i, v := range st.Textures {
		if idx := int(v); idx >= 0 && idx < len(translate) {
			tile.textures[i] = translate[idx]
		} else {
			tile.textures[i] = 0
		}
	}
	// Fold the post-cutoff strokes forward onto the restored grids (no reset).
	tile.sculpts = tile.sculpts[:0]
	for _, s := range tile.history {
		if !s.reverted && s.brush.Timing > cutoff {
			tile.sculpts = append(tile.sculpts, s.brush)
		}
	}
	tile.fullReload = false
	tile.reloading = false
	tile.performReload()
	return true
}

// buildSnapshot captures the just-built terrain as a snapshot covering every
// active stroke (cutoff = the newest stroke's Timing).
func (vr *TerrainEditor) buildSnapshot(pending map[*TerrainTile]struct{}) *terrainSnapshot {
	var cutoff musical.Timing
	for tile := range pending {
		for _, s := range tile.history {
			if !s.reverted && s.brush.Timing > cutoff {
				cutoff = s.brush.Timing
			}
		}
	}
	h, n := vr.hashActiveStrokes(pending, cutoff)
	layerDesigns := make([]musical.Design, len(vr.albedos))
	for d, idx := range vr.mapper {
		if idx >= 0 && idx < len(layerDesigns) {
			layerDesigns[idx] = d
		}
	}
	snap := &terrainSnapshot{
		Version:      terrainSnapshotVersion,
		Cutoff:       cutoff,
		StrokeHash:   h,
		StrokeCount:  n,
		LayerDesigns: layerDesigns,
	}
	for tile := range pending {
		if !tile.generated {
			continue
		}
		snap.Tiles = append(snap.Tiles, tileSnapshot{
			X: tile.coord.X, Z: tile.coord.Z, Size: tile.size, Revealed: tile.revealed,
			Heights:       append([]float32(nil), tile.heights...),
			GroundHeights: append([]float32(nil), tile.groundHeights...),
			RiverDepth:    append([]float32(nil), tile.riverDepth...),
			WaterFlowX:    append([]float32(nil), tile.waterFlowX...),
			WaterFlowZ:    append([]float32(nil), tile.waterFlowZ...),
			Textures:      append([]float32(nil), tile.textures...),
		})
	}
	return snap
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
	if tile.Sides != (MeshInstance3D.Instance{}) {
		tile.Sides.AsNode3D().SetVisible(tile.revealed)
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
//
// The skirt lives on its own MeshInstance3D (tile.Sides) so it can have
// shadow casting and receiving independently disabled from the top terrain.
//
// NOTE: This can be called before generateBase has created the Sides child
// (e.g. from reveal() when first touching a tile, or from neighbour updates).
// We guard on the node existing, exactly like reloadWater does for its child.
func (tile *TerrainTile) reloadSides() {
	if tile.Sides == (MeshInstance3D.Instance{}) {
		// Not created yet (generateBase will create it and call us again).
		return
	}

	tile_size := float32(1.0) // Adjust for texture tiling scale
	n := tile.size
	hm := n + 1

	active := tile.exposedSides()
	sideVertCount := len(active) * n * 6

	if sideVertCount == 0 {
		// Fully surrounded tile: clear any previous skirt mesh and material.
		if tile.Sides != (MeshInstance3D.Instance{}) {
			// Clear per-surface override while the mesh is still attached (safe).
			tile.Sides.SetSurfaceOverrideMaterial(0, Material.Nil)
			tile.Sides.SetMesh(Mesh.Nil)
			tile.Sides.AsGeometryInstance3D().SetMaterialOverride(Material.Nil)
		}
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

	index_base := 0
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
			norm := Vector3.Normalized(Vector3.Cross(v1, v2))
			// Triangle 1
			vertices_side[index_base+0] = bl
			normals_side[index_base+0] = norm
			uvs_side[index_base+0] = Vector2.XY{float32(i) / tile_size, 0 / tile_size}
			if sp.flippedWinding {
				vertices_side[index_base+1] = tr
				normals_side[index_base+1] = norm
				uvs_side[index_base+1] = Vector2.XY{float32(i+1) / tile_size, h_far / tile_size}
				vertices_side[index_base+2] = tl
				normals_side[index_base+2] = norm
				uvs_side[index_base+2] = Vector2.XY{float32(i) / tile_size, h_near / tile_size}
			} else {
				vertices_side[index_base+1] = tl
				normals_side[index_base+1] = norm
				uvs_side[index_base+1] = Vector2.XY{float32(i) / tile_size, h_near / tile_size}
				vertices_side[index_base+2] = tr
				normals_side[index_base+2] = norm
				uvs_side[index_base+2] = Vector2.XY{float32(i+1) / tile_size, h_far / tile_size}
			}
			// Triangle 2
			vertices_side[index_base+3] = bl
			normals_side[index_base+3] = norm
			uvs_side[index_base+3] = Vector2.XY{float32(i) / tile_size, 0 / tile_size}
			if sp.flippedWinding {
				vertices_side[index_base+4] = br
				normals_side[index_base+4] = norm
				uvs_side[index_base+4] = Vector2.XY{float32(i+1) / tile_size, 0 / tile_size}
				vertices_side[index_base+5] = tr
				normals_side[index_base+5] = norm
				uvs_side[index_base+5] = Vector2.XY{float32(i+1) / tile_size, h_far / tile_size}
			} else {
				vertices_side[index_base+4] = tr
				normals_side[index_base+4] = norm
				uvs_side[index_base+4] = Vector2.XY{float32(i+1) / tile_size, h_far / tile_size}
				vertices_side[index_base+5] = br
				normals_side[index_base+5] = norm
				uvs_side[index_base+5] = Vector2.XY{float32(i+1) / tile_size, 0 / tile_size}
			}
			index_base += 6
		}
	}

	// Build a dedicated mesh for the skirt (never attached to the main
	// terrain MeshInstance, which keeps exactly one surface).
	sideMesh := ArrayMesh.New()
	arrays_side := [Mesh.ArrayMax]any{
		Mesh.ArrayVertex: vertices_side,
		Mesh.ArrayNormal: normals_side,
		Mesh.ArrayTexUv:  uvs_side,
	}
	sideMesh.MoreArgs().AddSurfaceFromArrays(Mesh.PrimitiveTriangles, arrays_side[:], nil, nil,
		Mesh.ArrayFormatVertex|Mesh.ArrayFormatNormal|Mesh.ArrayFormatTexUv,
	)

	// Assign the freshly built side mesh, then the material overrides.
	// This mirrors exactly how reloadWater assigns to the Water MeshInstance3D
	// (which works reliably). We create a new ArrayMesh each time (like Water),
	// so a direct SetMesh is sufficient; no Nil dance needed (the dance was
	// only required when mutating the *same* ArrayMesh resource's surface count).
	tile.Sides.SetMesh(sideMesh.AsMesh())

	if tile.side_shader != (ShaderMaterial.Instance{}) {
		mat := tile.side_shader.AsMaterial()
		// Both the global override (widely used in the project) and the
		// per-surface override. Water does the per-surface version successfully
		// right after SetMesh.
		tile.Sides.AsGeometryInstance3D().SetMaterialOverride(mat)
		tile.Sides.SetSurfaceOverrideMaterial(0, mat)
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
	// Interpolate within the SAME triangle the mesh renders (Reload's update():
	// TL,TR,BL + TR,BR,BL, split along the TR–BL anti-diagonal), not a bilinear
	// average. The bilinear "twist" can sit ~1 unit off the rendered surface on a
	// saddle/edited cell, which floats every prop that rests via HeightAt (placement,
	// reprojectObjects, floats, grass). Triangle interp puts them on what's drawn.
	if sx+sz <= 1 {
		return h00 + sx*(h10-h00) + sz*(h01-h00)
	}
	return h11 + (1-sx)*(h01-h11) + (1-sz)*(h10-h11)
}

// previewDisplacementAt returns how far the height-brush HOVER preview shifts the
// rendered terrain surface at pos (previewedHeight − committedHeight), so a prop
// resting on the surface rides it exactly. It mirrors terrain.gdshader to the
// pixel: each committed corner vertex of pos's cell is deformed the way the shader
// does — additively lifted by amount*(1−d²/r²) for raise/lower/river, pulled toward
// targetY by a 1−d²/r² cone for smooth (flatTop=false), or by the flat-topped
// plateauFalloff for plateau (flatTop=true) — floored per-vertex at the world
// floor, and the four per-corner shifts are interpolated with the SAME triangle
// split the mesh uses (Reload's update()), matching the GPU's linear-per-triangle
// rasterisation rather than a bilinear guess that drifts within a cell.
func (tile *TerrainTile) previewDisplacementAt(pos, target Vector3.XYZ, radius, amount Float.X, flatten, flatTop bool, targetY Float.X) Float.X {
	n := tile.size
	hm := n + 1
	maxF := Float.X(n)
	base := tile.Mesh.AsNode3D().GlobalPosition()
	local := Vector3.Sub(pos, base)
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
	r2 := radius * radius
	floor := Float.X(worldFloorY)
	// shift returns the rendered Y change at one committed grid vertex.
	shift := func(gx, gz int) Float.X {
		h := Float.X(tile.heights[gx+gz*hm])
		d := h
		if r2 > 0 {
			dx := base.X + Float.X(gx) - target.X
			dz := base.Z + Float.X(gz) - target.Z
			dd := dx*dx + dz*dz
			if dd < r2 {
				if flatten {
					var ff float32
					if flatTop {
						ff = plateauFalloff(float32(Float.Sqrt(dd) / radius))
					} else {
						ff = float32(1 - dd/r2)
					}
					fac := Float.X(float32(amount) * ff)
					if fac > 1 {
						fac = 1
					}
					if fac < 0 {
						fac = 0
					}
					d = h*(1-fac) + targetY*fac
				} else {
					d = h + amount*(1-dd/r2)
				}
			}
		}
		return max(d, floor) - max(h, floor)
	}
	d00 := shift(x0, z0)
	d10 := shift(x1, z0)
	d01 := shift(x0, z1)
	d11 := shift(x1, z1)
	sx := x - Float.X(x0)
	sz := z - Float.X(z0)
	// Match the mesh triangulation (Reload's update()): each cell splits along the
	// TR–BL anti-diagonal into triangles TL,TR,BL (sx+sz≤1) and TR,BR,BL (sx+sz>1).
	if sx+sz <= 1 {
		return d00 + sx*(d10-d00) + sz*(d01-d00)
	}
	return d11 + (1-sx)*(d01-d11) + (1-sz)*(d10-d11)
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
				if tile.editor.PaintActive || tile.editor.DressActive || tile.editor.riverBrushActive() || tile.editor.ClearActive || tile.editor.specialTerrainBrushActive() {
					tile.editor.brushStrokeActive = true
				}
				deltaV := Float.X(0)
				if tile.editor.PaintActive || tile.editor.DressActive || tile.editor.ClearActive {
					deltaV = 2
				} else if tile.editor.client.ui.mode == ModeGeometry && tile.editor.TerrainBrush != "" && !tile.editor.specialTerrainBrushActive() {
					// Single-shot raise/lower: LMB applies the tool's primary direction
					// (raise vs lower) in one shot. Plateau/smooth are drag-paint
					// (brushStrokeActive above drives PaintTerrainSculpt), so they send
					// deltaV 0 here and never arm the single-shot release commit.
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
				tile.editor.sculptStroke = false // unlock the plateau level for the next stroke
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
				} else if tile.editor.client.ui.mode == ModeGeometry && tile.editor.TerrainBrush != "" && !tile.editor.specialTerrainBrushActive() {
					// RMB with a single-shot raise/lower brush applies the inverse of
					// the tool's primary direction. Plateau/smooth (drag-paint) ignore
					// RMB — it just tracks the brush position.
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
