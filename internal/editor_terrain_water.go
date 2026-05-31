package internal

import (
	"graphics.gd/classdb/ArrayMesh"
	"graphics.gd/classdb/Mesh"
	"graphics.gd/classdb/MeshInstance3D"
	"graphics.gd/classdb/ShaderMaterial"
	"graphics.gd/variant/Angle"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Vector2"
	"graphics.gd/variant/Vector3"
	"the.quetzal.community/aviary/internal/musical"
)

// This file holds the water + river half of the terrain editor: the river
// brush, the water-surface queries, and the per-tile water mesh. The standard
// terrain (heightfield, paint, dressing, chunk/arrow management) lives in
// editor_terrain.go. Both halves are the same package, so the split is purely
// organisational.

// riverSlider / riverEraseSlider tag a Sculpt as a river paint or erase stroke.
// Both carry an empty Design and are handled by Reload's river layer (riverDepth
// + flow); the Slider also keeps them out of the dressing/water-level branches
// in TerrainEditor.Sculpt and out of the non-river ground accumulation.
const (
	riverSlider      = "river"
	riverEraseSlider = "river/erase"
)

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

// waterSideZFightOffset nudges the water-body side walls fractionally inboard
// of the terrain skirt (both otherwise sit on the exact tile edge) so the two
// coplanar walls don't z-fight. Inboard + tiny keeps the wall tucked just under
// the water plane, so no gap shows at the surface (outboard pulled it away from
// the plane edge and left a visible seam).
const waterSideZFightOffset float32 = 0.005

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

// riverDepthAt returns the accumulated river depth at grid point (gx, gz),
// reaching one cell into the adjacent tile when the point lies just past this
// tile's edge. The bank-collar dilation (here and in reloadWater) uses it so two
// tiles sharing an edge test the SAME cells around the seam and agree on whether
// the edge carries water. Without it each tile's collar stops at its own
// boundary, the two can disagree, and the water side walls open a gap at the
// seam. Returns 0 when there is no such tile or its river data isn't ready.
func (tile *TerrainTile) riverDepthAt(gx, gz int) float32 {
	n := tile.size
	if n == 0 {
		n = terrainDefaultSize
	}
	t := tile
	cx, cz := tile.coord.X, tile.coord.Z
	// This tile's x == n is the seam shared with the +X neighbour's x == 0, so
	// x == n+1 maps to that neighbour's x == 1 (and x == -1 to the -X
	// neighbour's x == n-1); likewise for z.
	if gx < 0 {
		cx, gx = cx-1, gx+n
	} else if gx > n {
		cx, gx = cx+1, gx-n
	}
	if gz < 0 {
		cz, gz = cz-1, gz+n
	} else if gz > n {
		cz, gz = cz+1, gz-n
	}
	if cx != tile.coord.X || cz != tile.coord.Z {
		if tile.editor == nil {
			return 0
		}
		nb, ok := tile.editor.tiles[tileCoord{cx, cz}]
		if !ok {
			return 0
		}
		t = nb
		n = t.size
		if n == 0 {
			n = terrainDefaultSize
		}
	}
	hm := n + 1
	if gx < 0 || gz < 0 || gx >= hm || gz >= hm || len(t.riverDepth) < hm*hm {
		return 0
	}
	return t.riverDepth[gx+gz*hm]
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
			// riverDepthAt reaches across the tile seam so the waterline agrees
			// with the neighbour at a shared edge (matches reloadWater).
			if tile.riverDepthAt(cx+dx, cz+dz) > waterFloorEps {
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
				// riverDepthAt reaches across the tile seam so adjacent tiles test
				// the same cells here and agree on water presence at a shared edge
				// — without it their side walls disagree and a gap opens.
				if tile.riverDepthAt(x+dx, y+dz) > waterFloorEps {
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
	active := tile.exposedSides()
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
				// Terrain floor at the two edge grid points, clamped to the world
				// floor (a carved bed can fall below it) so the column bottoms out
				// where the skirt + rendered terrain top also stop, rather than
				// hanging in the void below the world. Like reloadSides()'s
				// h_near/h_far but WITHOUT the +2.2 buried offset (raw world Y).
				floorNear, floorFar := worldFloorY, worldFloorY
				if hasHeights {
					floorNear = max(worldFloorY, tile.heights[gnx+gnz*hm])
					floorFar = max(worldFloorY, tile.heights[gfx+gfz*hm])
				}
				// Per-edge water surface (matches the plane edge so the wall
				// stays connected) and flow, so a river that reaches the tile
				// boundary raises the wall to its surface and flows along it.
				topNearS, fnx, fnz := waterAt(gnx, gnz)
				topFarS, ffx, ffz := waterAt(gfx, gfz)
				topNear := float32(topNearS)
				topFar := float32(topFarS)
				// The wall bottom is PER-VERTEX (bl on the near floor, br on the
				// far floor) so it follows the bed exactly: a flat min() bottom
				// dipped below the terrain on the higher side of a sloping edge —
				// the translucent wall then hung in front of the rock — and put
				// adjacent cells' bottoms at different heights so they didn't line
				// up. Per-vertex, neighbouring cells share the same floor at their
				// shared edge, so they meet.
				//
				// The TOP sits on the EXACT tile edge so it meets the water plane
				// (and the neighbouring tiles' walls) seamlessly at the waterline —
				// nudging the top inboard left a visible gap at water level. Only
				// the BOTTOM is nudged inboard, where the wall would otherwise be
				// coplanar with the rock skirt's buried +2.2 lip and z-fight; that
				// overlap is down near the bed. fixed is 0 or n; inboard is toward
				// the centre.
				fixedPos := sp.fixed - waterSideZFightOffset
				if sp.fixed == 0 {
					fixedPos = sp.fixed + waterSideZFightOffset
				}
				var tl, tr, bl, br Vector3.XYZ
				if sp.isZFixed {
					tl = Vector3.XYZ{pos_near, topNear, sp.fixed}
					tr = Vector3.XYZ{pos_far, topFar, sp.fixed}
					bl = Vector3.XYZ{pos_near, floorNear, fixedPos}
					br = Vector3.XYZ{pos_far, floorFar, fixedPos}
				} else {
					tl = Vector3.XYZ{sp.fixed, topNear, pos_near}
					tr = Vector3.XYZ{sp.fixed, topFar, pos_far}
					bl = Vector3.XYZ{fixedPos, floorNear, pos_near}
					br = Vector3.XYZ{fixedPos, floorFar, pos_far}
				}
				// One uniform OUTWARD normal for the whole side, so the cut-through
				// wall is lit as a single flat face. A per-quad cross product tilts
				// slightly with the sloping bed/surface (and the small inboard
				// slant), and that faceting catches specular as bright vertical
				// seams between cells — worst at tile boundaries, where the two
				// tiles' meshes are flat-shaded with the most-divergent normals.
				var nrm Vector3.XYZ
				if sp.isZFixed {
					nrm = Vector3.XYZ{0, 0, 1}
					if sp.fixed == 0 {
						nrm = Vector3.XYZ{0, 0, -1}
					}
				} else {
					nrm = Vector3.XYZ{1, 0, 0}
					if sp.fixed == 0 {
						nrm = Vector3.XYZ{-1, 0, 0}
					}
				}
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
