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

// waterRiseRate is the exponential ease rate (1/s) for the displayed water level
// gliding to a changed WaterLevel, and waterRiseEps the gap (world units) at
// which it snaps. At rate 8 roughly 95% of the change is covered in ~0.4s — fast
// enough to feel responsive, slow enough to read as a rise/fall rather than a
// jump. Frame-rate independent (see processWaterRise).
const (
	waterRiseRate Float.X = 8.0
	waterRiseEps  Float.X = 0.002
)

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
		Author: tr.recorder.localAuthor(),
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
	tr.recorder.commitSculpt(brush)
}

// riverBrushActive reports whether a river paint or erase tool is selected.
func (tr *TerrainEditor) riverBrushActive() bool {
	return tr.TerrainBrush == BuiltinTerrainRiver || tr.TerrainBrush == BuiltinTerrainRiverErase
}

// WaterSurfaceAt returns the world-Y of the water surface at a world position:
// the local river surface where one has been carved, otherwise the global lake
// level. The underwater post-process samples this under the camera so the
// waterline tracks rivers (not just the flat global plane). Uses the currently
// DISPLAYED level (waterDisplayed) so the waterline glides with the rendered
// surface during a level change rather than snapping ahead of it. Falls back to
// the displayed level where no tile is loaded.
func (tr *TerrainEditor) WaterSurfaceAt(pos Vector3.XYZ) Float.X {
	tile := tr.tileForWorld(pos)
	if tile == nil {
		return tr.waterDisplayed
	}
	return tile.WaterSurfaceAt(pos)
}

// swimWaterMargin keeps a swimmer a little clear of the surface and seabed, so a
// placed/controlled fish doesn't poke through the water surface or clip into the
// ground at the extremes of its water column.
const swimWaterMargin = Float.X(0.05)

// MidWaterAt is the default depth for a freshly placed swimmer at pos: halfway
// between the water surface and the seabed (the terrain floor) — a fish hovering
// in the middle of the water column. Where there is no column (the surface is at
// or below the floor) it returns the surface so the fish sits at water level.
func (tr *TerrainEditor) MidWaterAt(pos Vector3.XYZ) Float.X {
	surface := tr.WaterSurfaceAt(pos)
	floor := tr.HeightAt(pos)
	if surface <= floor {
		return surface
	}
	return (surface + floor) / 2
}

// ClampToWater clamps y into the swimmable water column at pos — between the
// seabed and the water surface, less swimWaterMargin at each end — so a dragged
// or controlled swimmer can't be pushed through the surface or into the ground.
// A degenerate (too-thin) column collapses to its midpoint.
func (tr *TerrainEditor) ClampToWater(pos Vector3.XYZ, y Float.X) Float.X {
	surface := tr.WaterSurfaceAt(pos)
	floor := tr.HeightAt(pos)
	lo := floor + swimWaterMargin
	hi := surface - swimWaterMargin
	if hi < lo {
		return (surface + floor) / 2
	}
	return min(max(y, lo), hi)
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

// riverSurfaceAt reports whether grid point (gx, gz) was carved into a river
// channel and, if so, its river surface (original ground minus the lip) and
// flow. Like riverDepthAt it reaches one cell into the adjacent tile past an
// edge, so the cross-section wall can find the channel's level across a tile
// seam/corner. carved is false off-channel or off-grid.
func (tile *TerrainTile) riverSurfaceAt(gx, gz int) (carved bool, surface, fx, fz float32) {
	n := tile.size
	if n == 0 {
		n = terrainDefaultSize
	}
	t := tile
	cx, cz := tile.coord.X, tile.coord.Z
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
			return false, 0, 0, 0
		}
		nb, ok := tile.editor.tiles[tileCoord{cx, cz}]
		if !ok {
			return false, 0, 0, 0
		}
		t = nb
		n = t.size
		if n == 0 {
			n = terrainDefaultSize
		}
	}
	hm := n + 1
	if gx < 0 || gz < 0 || gx >= hm || gz >= hm {
		return false, 0, 0, 0
	}
	if len(t.riverDepth) < hm*hm || len(t.groundHeights) < hm*hm {
		return false, 0, 0, 0
	}
	i := gx + gz*hm
	if t.riverDepth[i] <= waterFloorEps {
		return false, 0, 0, 0
	}
	return true, t.groundHeights[i] - float32(waterSurfaceDrop), t.waterFlowX[i], t.waterFlowZ[i]
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
		// Displayed (rendered) level, not the committed target, so the waterline
		// glides with the surface during a level change (see WaterSurfaceAt doc).
		level = tile.editor.waterDisplayed
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
	// Lip first, then clamp up to the global level — matches reloadWater's
	// waterAt (and the wall) so the post-process waterline tracks the rendered
	// surface, including the band where the level sits just above the lip.
	surface := ground - waterSurfaceDrop
	if level > surface {
		return level
	}
	return surface
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
	// Leave the DISPLAYED level where it is so processWaterRise eases it up/down
	// to the new level — the change glides instead of teleporting through the
	// slider's discrete steps. During the join replay there are no frames to ease
	// over and a glide would just read as a load-time animation (cf. the bomb
	// explosion guard), so snap the displayed level to match instead.
	if vr.recorder != nil && vr.recorder.isJoining() {
		vr.waterDisplayed = level
	}
	// Rebuild the mesh at the NEW level, then push the residual offset so the
	// freshly-rebuilt surface still renders at the displayed (old) height — no
	// pop — and decays to 0 over the next frames.
	for _, tile := range vr.tiles {
		tile.reloadWater()
	}
	vr.pushWaterRise()
}

// pushWaterRise feeds the current displayed-vs-committed gap to the shared water
// shader (water_rise in water.gdshader). One push covers every tile because they
// all share vr.water_shader.
func (vr *TerrainEditor) pushWaterRise() {
	if vr.water_shader != (ShaderMaterial.Instance{}) {
		vr.water_shader.SetShaderParameter("water_rise", float64(vr.waterDisplayed-vr.WaterLevel))
	}
}

// processWaterRise eases the displayed water level toward the committed
// WaterLevel each frame so a level change glides rather than stepping. Driven
// from Client.Process (which ticks every frame regardless of which editor is
// active, unlike TerrainEditor.Process), so a remote/undo level change animates
// even when this client isn't in the terrain editor. Purely cosmetic and
// client-local: the displayed level always converges to the observed WaterLevel.
func (vr *TerrainEditor) processWaterRise(dt Float.X) {
	if vr.waterDisplayed == vr.WaterLevel {
		return
	}
	diff := vr.WaterLevel - vr.waterDisplayed
	if Float.Abs(diff) <= waterRiseEps {
		vr.waterDisplayed = vr.WaterLevel
	} else {
		// Frame-rate-independent exponential approach: the fraction of the
		// remaining gap closed this frame is 1-e^(-rate*dt), so the glide takes
		// the same wall-clock time at any framerate.
		vr.waterDisplayed += diff * (1 - Float.Exp(-waterRiseRate*dt))
	}
	vr.pushWaterRise()
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
	// waterAt is the SINGLE source of truth for the per-vertex water-surface Y and
	// normalised world-XZ flow at grid point (x,y) — used by BOTH the flat plane
	// and the cross-section wall top, so the two surfaces are welded by
	// construction. (When the plane and the wall computed their tops from
	// different rules they diverged at the channel edge: either the wall climbed
	// the bank above the plane — the stray triangle — or it sat below the plane —
	// a hole. One function removes that whole class of bug. The shader applies NO
	// floor clamp to the geometry, so a difference here is a real gap, not
	// something occlusion hides.)
	//
	// The surface sits a lip below the LOCAL original ground ("where the terrain
	// used to be") on any carved cell or its bank collar, and at the flat global
	// level on dry land away from a river. Following the local ground is what
	// makes the river slope naturally downstream and lap up to meet its banks (the
	// "river height" — flattening the collar to the nearby channel level instead
	// read as wrong); the lip keeps a sliver of bank above the waterline. It is
	// clamped UP to the global level so it never dips under the body of water it
	// connects to (oceans, sunken banks). Dry cells fall back to the flat level so
	// a project with no rivers renders exactly as before. riverSurfaceAt reaches
	// across tile seams so adjacent tiles agree at a shared edge/corner.
	//
	// The wall top uses this SAME function, so the cross-section wall and the flat
	// plane are one welded surface: where the river laps up a bank the wall climbs
	// WITH the plane (no disconnected stray triangle), and they can never open a
	// gap (no hole). The shader applies no floor clamp to the geometry, so this
	// per-vertex agreement is the only thing keeping the two surfaces together.
	waterAt := func(x, y int) (surface Float.X, fx, fz float32) {
		surface = level
		if !hasRiver {
			return surface, 0, 0
		}
		ground := tile.groundHeights[x+y*hm]
		carved, _, cfx, cfz := tile.riverSurfaceAt(x, y)
		found := carved
		if !carved {
			// Lap one cell onto the bank: present if any neighbour within the
			// collar is a channel; adopt that channel's flow direction.
			for dz := -riverBankCollar; dz <= riverBankCollar && !found; dz++ {
				for dx := -riverBankCollar; dx <= riverBankCollar; dx++ {
					if c, _, ffx, ffz := tile.riverSurfaceAt(x+dx, y+dz); c {
						found, cfx, cfz = true, ffx, ffz
						break
					}
				}
			}
		}
		if !found {
			return level, 0, 0 // dry, away from any river -> the flat global lake
		}
		surface = Float.X(ground) - waterSurfaceDrop // a lip below the local terrain
		if level > surface {
			surface = level // never under the global level
		}
		if mag := Float.Sqrt(Float.X(cfx*cfx + cfz*cfz)); mag > 1e-6 {
			fx, fz = cfx/float32(mag), cfz/float32(mag)
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
				// Per-edge water surface + flow from the SAME waterAt the plane
				// uses, so the wall top is identical to the plane's edge row of
				// vertices and the two weld seamlessly: where the river laps up a
				// bank the wall climbs WITH the plane as one surface (no
				// disconnected triangle), and they can never open a gap (no hole).
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
				// Never let the bottom rise above the surface: at a dry bank vertex
				// (bed above the channel level) the column collapses to nothing
				// rather than drawing a wedge up to the bank. Where there is water,
				// floor < top, so this is just the bed.
				botNear := min(floorNear, topNear)
				botFar := min(floorFar, topFar)
				var tl, tr, bl, br Vector3.XYZ
				if sp.isZFixed {
					tl = Vector3.XYZ{pos_near, topNear, sp.fixed}
					tr = Vector3.XYZ{pos_far, topFar, sp.fixed}
					bl = Vector3.XYZ{pos_near, botNear, fixedPos}
					br = Vector3.XYZ{pos_far, botFar, fixedPos}
				} else {
					tl = Vector3.XYZ{sp.fixed, topNear, pos_near}
					tr = Vector3.XYZ{sp.fixed, topFar, pos_far}
					bl = Vector3.XYZ{fixedPos, botNear, pos_near}
					br = Vector3.XYZ{fixedPos, botFar, pos_far}
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
				// .r = terrain floor; .g/.b = flow X/Z; .a = heave weight (1 on the
				// TOP row tl/tr so it follows the swell and welds to the plane,
				// 0 on the BED row bl/br so it stays pinned to the terrain — the
				// shader keys the wall's swell off this instead of the water depth,
				// so a shallow-shore top vertex still heaves in lockstep with the
				// coincident plane edge). bl is always slot 0/3 (near); the winding
				// swaps which of tl(near)/tr(far)/br(far) fill slots 1,2,4,5.
				setSideCustom := func(slot int, floor, fx, fz, up float32) {
					floors_side[slot*4+0] = floor
					floors_side[slot*4+1] = fx
					floors_side[slot*4+2] = fz
					floors_side[slot*4+3] = up
				}
				if sp.flippedWinding {
					// [bl, tr, tl, bl, br, tr]
					setSideCustom(index_base+0, floorNear, fnx, fnz, 0) // bl
					setSideCustom(index_base+1, floorFar, ffx, ffz, 1)  // tr
					setSideCustom(index_base+2, floorNear, fnx, fnz, 1) // tl
					setSideCustom(index_base+3, floorNear, fnx, fnz, 0) // bl
					setSideCustom(index_base+4, floorFar, ffx, ffz, 0)  // br
					setSideCustom(index_base+5, floorFar, ffx, ffz, 1)  // tr
				} else {
					// [bl, tl, tr, bl, tr, br]
					setSideCustom(index_base+0, floorNear, fnx, fnz, 0) // bl
					setSideCustom(index_base+1, floorNear, fnx, fnz, 1) // tl
					setSideCustom(index_base+2, floorFar, ffx, ffz, 1)  // tr
					setSideCustom(index_base+3, floorNear, fnx, fnz, 0) // bl
					setSideCustom(index_base+4, floorFar, ffx, ffz, 1)  // tr
					setSideCustom(index_base+5, floorFar, ffx, ffz, 0)  // br
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
