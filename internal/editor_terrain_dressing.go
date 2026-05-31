package internal

import (
	"math"

	"graphics.gd/classdb/BaseMaterial3D"
	"graphics.gd/classdb/Material"
	"graphics.gd/classdb/Mesh"
	"graphics.gd/classdb/MeshInstance3D"
	"graphics.gd/classdb/MultiMesh"
	"graphics.gd/classdb/MultiMeshInstance3D"
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/Shader"
	"graphics.gd/classdb/ShaderMaterial"
	"graphics.gd/classdb/Texture2D"
	"graphics.gd/variant/Angle"
	"graphics.gd/variant/Basis"
	"graphics.gd/variant/Euler"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Transform3D"
	"graphics.gd/variant/Vector3"
	"the.quetzal.community/aviary/internal/musical"
)

// ModeDressing scatters instanced meshes (grasses, pebbles, foliage,
// boulders/mineral) across the terrain surface. Each committed stroke is a
// single musical.Sculpt — its Target/Radius are the record of where and how
// wide the patch is, its Amount is the density, and its Design is the
// scattered mesh. The scatter itself is NOT stored per-instance in the
// musical log: it's regenerated deterministically from the sculpt's
// (Author, Target, Radius), so every client that replays the same sculpt
// produces identical placement while the format stays tiny and fully
// backwards compatible. Foliage and boulder brushes reuse the same meshes
// and scatter logic as the grasses brush, with category-specific variance
// and no grass-specific Z-up correction.

type grassPatch struct {
	design   musical.Design
	target   Vector3.XYZ
	radius   Float.X
	category string // dressing tab (grasses/foliage/mineral/...) for params + transform rules

	mm     MultiMesh.Instance
	mmNode MultiMeshInstance3D.Instance

	// Per-instance scatter, kept so a later height sculpt can re-plant the
	// instances on the reshaped surface. bases hold world X/Z (Y is
	// re-sampled from the terrain each time a transform is built).
	bases  []Vector3.XYZ
	yaws   []Angle.Radians
	scales []Float.X
}

// grassAsset holds the extracted mesh plus the transform that was applied
// to it inside its source .glb scene (product of the MeshInstance3D and
// ancestor nodes). Composing this into every MultiMesh instance transform
// preserves authored pivots and any orientation fixes the author put on
// the nodes (rather than assuming raw vertex data is Y-up at the origin).
type grassAsset struct {
	mesh  Mesh.Instance
	xform Transform3D.BasisOrigin
}

// dressingParams configures the scatter behaviour per dressing category
// (tab). This lets foliage and boulder brushes use much lower densities
// and wider scale variance than the fine grasses/pebbles while sharing
// all the deterministic RNG, history, reprojection and MultiMesh logic.
type dressingParams struct {
	perArea float64
	maxInst int
	// baseScale is the fixed factor the mesh's authored size is multiplied by
	// BEFORE the per-instance random spread. For the scenery-library categories
	// (foliage/mineral/boulders/rocks) this is sceneryLibraryScale (0.1) so a
	// scattered prop matches the size that the scenery editor places the same
	// .glb at; grass/pebble packs are authored at world size already (baseScale 1).
	baseScale Float.X
	// scaleMin..scaleMax is the per-instance random spread applied on top of
	// baseScale — a modest variation around 1.0 for natural variety, so a patch
	// doesn't mix wildly different sizes.
	scaleMin  Float.X
	scaleMax  Float.X
	needsZToY bool // only the legacy grass packs need the Z-up correction
	// windKind selects how the scattered mesh sways: "grass" wraps the material
	// in grass_wind (every vertex leans by height — blades), "foliage" wraps it
	// in foliage_wind_mm (trunk planted, canopy flutters), "" leaves the imported
	// material as authored (no sway).
	windKind string
}

var dressingDefaults = map[string]dressingParams{
	"grasses":  {perArea: 6.0, maxInst: 3000, baseScale: 1.0, scaleMin: 0.7, scaleMax: 1.2, needsZToY: true, windKind: "grass"},
	"pebbles":  {perArea: 4.0, maxInst: 2000, baseScale: 1.0, scaleMin: 0.5, scaleMax: 1.4, needsZToY: false, windKind: ""},
	"foliage":  {perArea: 1.5, maxInst: 400, baseScale: sceneryLibraryScale, scaleMin: 0.85, scaleMax: 1.15, needsZToY: false, windKind: "foliage"},
	"mineral":  {perArea: 1.5, maxInst: 600, baseScale: sceneryLibraryScale, scaleMin: 0.85, scaleMax: 1.15, needsZToY: false, windKind: ""},
	"boulders": {perArea: 0.8, maxInst: 500, baseScale: sceneryLibraryScale, scaleMin: 0.8, scaleMax: 1.2, needsZToY: false, windKind: ""},
	"rocks":    {perArea: 1.0, maxInst: 500, baseScale: sceneryLibraryScale, scaleMin: 0.8, scaleMax: 1.2, needsZToY: false, windKind: ""},
}

// populateGrassMM rebuilds the MultiMesh for a grassPatch so that it only
// contains the instances whose terrain tile is currently revealed. The
// patch keeps its full bases/yaws/scales (the complete sculpt data) so that
// later reveals can add the omitted instances without re-sampling RNG.
// Called from build/erase/reproject and when tiles change revealed state.
func (vr *TerrainEditor) populateGrassMM(patch *grassPatch) {
	if patch.mm == MultiMesh.Nil {
		return
	}
	asset, ok := vr.grassMeshes[patch.design]
	if !ok || asset.mesh == Mesh.Nil {
		patch.mm.SetInstanceCount(0)
		return
	}
	var visible []int
	for i := range patch.bases {
		if vr.tileRevealedAt(patch.bases[i]) {
			visible = append(visible, i)
		}
	}
	n := len(visible)
	patch.mm.SetInstanceCount(n)
	for k, i := range visible {
		patch.mm.SetInstanceTransform(k, vr.grassTransform(patch.bases[i], patch.yaws[i], patch.scales[i], asset.xform, patch.category))
	}
}

// refreshGrassVisibility repopulates every active grass patch's MultiMesh
// from its full data, filtering to only the instances on revealed tiles.
// Used after any tile reveal/hide so grasses appear/disappear with their
// terrain chunk.
func (vr *TerrainEditor) refreshGrassVisibility() {
	for _, p := range vr.grassPatches {
		vr.populateGrassMM(p)
	}
}

// clearGrass removes every rendered grass patch (and any pending scatters whose
// mesh had not yet loaded). Used by recomputeGrass before replaying the active
// dressing history on undo/redo.
func (vr *TerrainEditor) clearGrass() {
	for _, p := range vr.grassPatches {
		if p.mmNode != MultiMeshInstance3D.Nil {
			p.mmNode.AsNode().QueueFree()
		}
	}
	vr.grassPatches = vr.grassPatches[:0]
	vr.pendingGrass = vr.pendingGrass[:0]
}

// recomputeGrass rebuilds all dressing from scratch over the non-reverted strokes
// in grassHistory, in commit order so each erase lands after the scatters it
// trims. Used on undo/redo of a dressing stroke: a region-based erase can only be
// reverted by replaying what survives, not by an arithmetic inverse.
func (vr *TerrainEditor) recomputeGrass() {
	vr.clearGrass()
	for i := range vr.grassHistory {
		e := vr.grassHistory[i]
		if e.reverted {
			continue
		}
		if e.brush.Amount <= 0 {
			vr.eraseGrass(e.brush)
		} else {
			vr.scatterGrass(e.brush)
		}
	}
	vr.refreshGrassVisibility()
}

// scatterGrass renders one dressing stroke. If the stroke's mesh hasn't
// finished importing yet (common on a freshly joined client replaying the
// log before the .glb has loaded), it's parked in pendingGrass and retried
// from Process once the resource is available.
func (vr *TerrainEditor) scatterGrass(brush musical.Sculpt) {
	asset, ok := vr.grassMeshFor(brush.Design, brush.Slider)
	if !ok {
		vr.pendingGrass = append(vr.pendingGrass, brush)
		return
	}
	vr.buildGrassPatch(brush, asset)
}

// retryPendingGrass re-attempts any dressing strokes whose mesh wasn't
// loaded when they first arrived. Called once per frame from Process.
func (vr *TerrainEditor) retryPendingGrass() {
	if len(vr.pendingGrass) == 0 {
		return
	}
	remaining := vr.pendingGrass[:0]
	for _, brush := range vr.pendingGrass {
		asset, ok := vr.grassMeshFor(brush.Design, brush.Slider)
		if !ok {
			remaining = append(remaining, brush)
			continue
		}
		vr.buildGrassPatch(brush, asset)
	}
	vr.pendingGrass = remaining
}

// fillPatch (re)samples the deterministic scatter for `brush` into `patch`,
// overwriting its bases/yaws/scales and identity fields. Shared by the committed
// builder and the hover preview so both produce IDENTICAL layouts for the same
// brush. The seed is taken from brush.Random (the predetermined scatter seed),
// falling back to the derived grassSeed for legacy sculpts that predate the
// field (Random == 0). All `count` positions are generated regardless of which
// tiles are revealed, so the RNG advances identically on every client. Returns
// false when the stroke scatters nothing.
func (vr *TerrainEditor) fillPatch(patch *grassPatch, brush musical.Sculpt, asset grassAsset) bool {
	prms := vr.paramsFor(brush.Slider)
	count := dressingCount(prms, brush.Radius, brush.Amount)
	if count <= 0 {
		return false
	}
	seed := uint64(brush.Random)
	if seed == 0 {
		seed = grassSeed(brush.Author, brush.Target, brush.Radius)
	}
	rng := &grassRNG{state: seed}
	patch.design = brush.Design
	patch.target = brush.Target
	patch.radius = brush.Radius
	patch.category = brush.Slider
	patch.bases = make([]Vector3.XYZ, count)
	patch.yaws = make([]Angle.Radians, count)
	patch.scales = make([]Float.X, count)
	for i := 0; i < count; i++ {
		// Uniform disc sampling: sqrt keeps density even out to the rim.
		rad := float64(brush.Radius) * math.Sqrt(rng.float())
		theta := rng.float() * 2 * math.Pi
		patch.bases[i] = Vector3.XYZ{
			X: brush.Target.X + Float.X(rad*math.Cos(theta)),
			Z: brush.Target.Z + Float.X(rad*math.Sin(theta)),
		}
		patch.yaws[i] = Angle.Radians(rng.float() * 2 * math.Pi)
		// Scale the mesh's authored size by the category's library factor (so the
		// dressed prop matches the size the scenery editor would place it at) times
		// a small random spread for variety.
		base := prms.baseScale
		if base == 0 {
			base = 1
		}
		patch.scales[i] = base * (prms.scaleMin + Float.X(rng.float())*(prms.scaleMax-prms.scaleMin))
	}
	return true
}

// makeGrassPatch builds (but does not register) a scatter patch plus its
// MultiMesh node for one dressing stroke, populated for the currently revealed
// tiles. buildGrassPatch registers it as committed scenery; the hover preview
// holds it transiently instead. Returns nil when the stroke scatters nothing.
func (vr *TerrainEditor) makeGrassPatch(brush musical.Sculpt, asset grassAsset) *grassPatch {
	patch := &grassPatch{}
	if !vr.fillPatch(patch, brush, asset) {
		return nil
	}
	mm := MultiMesh.New()
	mm.SetTransformFormat(MultiMesh.Transform3d)
	mm.SetMesh(asset.mesh)
	// Instance count and transforms are set by populateGrassMM (only the
	// subset on revealed tiles).
	mmi := MultiMeshInstance3D.New()
	mmi.SetMultimesh(mm)
	vr.AsNode().AddChild(mmi.AsNode())
	patch.mm = mm
	patch.mmNode = mmi
	vr.populateGrassMM(patch)
	return patch
}

// buildGrassPatch scatters one committed dressing stroke and registers the
// resulting grassPatch so the placement can be re-projected later.
func (vr *TerrainEditor) buildGrassPatch(brush musical.Sculpt, asset grassAsset) {
	if patch := vr.makeGrassPatch(brush, asset); patch != nil {
		vr.grassPatches = append(vr.grassPatches, patch)
	}
}

// dressKey is the quantised brush state a hover preview was built for. Quantising
// to the millimetre matches grassSeed's own rounding, so a stationary hover keeps
// the same key (no rebuild) and a click lands on the same key the preview showed
// (so the committed scatter is byte-identical to the preview).
type dressKey struct {
	design       musical.Design
	tab          string
	tx, tz       int64
	radius, dens int64
	seed         uint64
}

func qmm(f Float.X) int64 { return int64(math.Round(float64(f) * 1000)) }

// updateDressPreview keeps the transient hover scatter in sync with the dressing
// brush. It renders the EXACT instances a click would commit (the dressing
// analogue of the height/paint shader previews) whenever a dressing tool is armed
// and the user is NOT mid-stroke; otherwise it tears the preview down. Called
// once per frame from Process.
func (vr *TerrainEditor) updateDressPreview() {
	if vr.client == nil || vr.client.ui.mode != ModeDressing ||
		(!vr.DressActive && !vr.ClearActive) ||
		(vr.DressActive && vr.DressDesign == "") ||
		vr.brushStrokeActive ||
		(vr.DressActive && vr.dressDesignID == (musical.Design{})) ||
		vr.ClearActive {
		// Mid-stroke the real (committed) scatter is what shows; off-tool there is
		// nothing to preview; a removal tool never shows an "add" preview; and
		// the old Ctrl+Shift erase modifier path is being removed.
		vr.clearDressPreview()
		return
	}
	design := vr.dressDesignID
	key := dressKey{
		design: design, tab: vr.DressTab,
		tx: qmm(vr.BrushTarget.X), tz: qmm(vr.BrushTarget.Z),
		radius: qmm(vr.BrushRadius), dens: qmm(vr.BrushDensity),
		seed: vr.dressSeed,
	}
	if vr.dressPreview != nil && vr.dressPreviewKey == key {
		return // unchanged hover — keep the standing preview
	}
	asset, ok := vr.grassMeshFor(design, vr.DressTab)
	if !ok {
		// Mesh still importing; drop any stale preview and retry next frame.
		vr.clearDressPreview()
		return
	}
	// Seed the preview from the SAME fixed dressSeed the next commit will store in
	// Random, so what is shown here is exactly what PaintDressing lands — and so
	// the arrangement only translates with the brush (no per-move reshuffle).
	brush := musical.Sculpt{
		Author: vr.client.id,
		Editor: "terrain",
		Slider: vr.DressTab,
		Target: vr.BrushTarget,
		Radius: vr.BrushRadius,
		Amount: vr.BrushDensity,
		Design: design,
		Random: int64(vr.dressSeed),
	}
	if vr.dressPreview == nil {
		vr.dressPreview = vr.makeGrassPatch(brush, asset)
	} else if !vr.fillPatch(vr.dressPreview, brush, asset) {
		vr.clearDressPreview()
		return
	} else {
		// Reuse the existing node (no per-move node churn): swap the mesh in case
		// the design changed and repopulate the revealed-tile subset.
		vr.dressPreview.mm.SetMesh(asset.mesh)
		vr.populateGrassMM(vr.dressPreview)
	}
	vr.dressPreviewKey = key
}

// clearDressPreview removes the transient hover scatter, if any.
func (vr *TerrainEditor) clearDressPreview() {
	if vr.dressPreview == nil {
		return
	}
	if vr.dressPreview.mmNode != MultiMeshInstance3D.Nil {
		vr.dressPreview.mmNode.AsNode().QueueFree()
	}
	vr.dressPreview = nil
	vr.dressPreviewKey = dressKey{}
}

// reprojectGrass re-plants the instances of every patch overlapping the
// given sculpt disc back onto the (just reshaped) terrain surface.
func (vr *TerrainEditor) reprojectGrass(target Vector3.XYZ, radius Float.X) {
	for _, patch := range vr.grassPatches {
		dx := float64(patch.target.X - target.X)
		dz := float64(patch.target.Z - target.Z)
		if Float.X(math.Hypot(dx, dz)) > patch.radius+radius {
			continue
		}
		// Repopulate the (filtered) MultiMesh; this recomputes grassTransform
		// (which re-queries HeightAt) only for the instances on revealed tiles.
		vr.populateGrassMM(patch)
	}
}

// eraseGrass removes instances of the given Design whose scatter centers
// lie inside the brush disc. It filters the per-patch instance lists in
// place and rebuilds the corresponding MultiMeshes so the deletion is
// immediately visible and is reproduced identically on every client that
// replays the (negative-Amount) sculpt.
func (vr *TerrainEditor) eraseGrass(brush musical.Sculpt) {
	// For a normal (per-Design) erase we need the mesh loaded so we can
	// safely repopulate the MultiMesh after filtering instances.
	// Category-clear strokes (Design zero, Slider = category) do not need
	// any single mesh; they operate on patches by category only.
	if brush.Design != (musical.Design{}) {
		if _, ok := vr.grassMeshes[brush.Design]; !ok {
			// The mesh for this design hasn't arrived yet; we can't safely
			// rebuild the MultiMesh transforms. Skip — the erase will have
			// no visual effect until the asset is present (extremely rare
			// in normal play).
			return
		}
	}
	center := brush.Target
	r2 := float64(brush.Radius) * float64(brush.Radius)

	for i := len(vr.grassPatches) - 1; i >= 0; i-- {
		p := vr.grassPatches[i]
		// Match rules:
		// - Exact Design (normal per-design erase while a dressing brush is armed)
		// - Design==zero + real Slider → category clear (scythe, axe, etc.)
		// - Design==zero + ClearAllDressingCategory ("*") → bomb: every category
		if brush.Design != (musical.Design{}) {
			if p.design != brush.Design {
				continue
			}
		} else if brush.Slider == ClearAllDressingCategory {
			// Bomb: delete from every dressing patch that overlaps the disc.
			// No category filter.
		} else if p.category != brush.Slider {
			continue
		}

		// Cheap disc-overlap reject before per-instance work.
		dx := float64(p.target.X - center.X)
		dz := float64(p.target.Z - center.Z)
		sumR := float64(p.radius) + float64(brush.Radius)
		if dx*dx+dz*dz > sumR*sumR {
			continue
		}

		keptB := p.bases[:0]
		keptY := p.yaws[:0]
		keptS := p.scales[:0]
		for j := range p.bases {
			dbx := float64(p.bases[j].X - center.X)
			dbz := float64(p.bases[j].Z - center.Z)
			if dbx*dbx+dbz*dbz > r2 {
				keptB = append(keptB, p.bases[j])
				keptY = append(keptY, p.yaws[j])
				keptS = append(keptS, p.scales[j])
			}
		}

		if len(keptB) == 0 {
			if p.mmNode != MultiMeshInstance3D.Nil {
				p.mmNode.AsNode().QueueFree()
			}
			vr.grassPatches = append(vr.grassPatches[:i], vr.grassPatches[i+1:]...)
			continue
		}

		p.bases = keptB
		p.yaws = keptY
		p.scales = keptS
		// Rebuild the MultiMesh from the kept (full) data, but populate only
		// adds the subset whose tiles are revealed.
		vr.populateGrassMM(p)
	}
}

// grassTransform builds the instance transform for a dressing instance:
// yaw about world up, uniform scale, planted at terrain height under the
// base X/Z. For legacy grasses it applies the Z-to-Y correction so that
// yughues-style packs stand upright; for foliage/mineral/boulders the
// source mesh is assumed to be authored Y-up (matching scenery placement)
// and only the random yaw+scale is applied on top.
func (vr *TerrainEditor) grassTransform(base Vector3.XYZ, yaw Angle.Radians, scale Float.X, source Transform3D.BasisOrigin, category string) Transform3D.BasisOrigin {
	yawB := Basis.FromEuler(Euler.Radians{Y: yaw}, Angle.OrderYXZ)
	scaledYaw := Basis.Scaled(yawB, Vector3.New(scale, scale, scale))

	p := vr.paramsFor(category)
	if p.needsZToY {
		// Fixed correction that maps a model's local +Z direction to world +Y
		// (after the source glb's own node transforms have been applied).
		// The yughues grasses export blades primarily along Z (with a 180° Y
		// node flip that does not change the up axis); the sign here is chosen
		// so that after the source basis the tips point +Y.
		zToY := Basis.FromEuler(Euler.Radians{X: -Angle.Pi / 2}, Angle.OrderYXZ)

		// Compose order: source (authored node xform) first, then the Z-to-Y
		// stand-up, then the per-instance yaw+scale. This makes authored
		// adjustments and the category correction both take effect before the
		// random yaw spins the now-vertical blade.
		corrected := Basis.Mul(scaledYaw, Basis.Mul(zToY, source.Basis))

		// The source origin (pivot offset inside the glb) rotated into the final
		// oriented frame, then added to the terrain planting point.
		rotatedOrigin := Basis.Transform(source.Origin, Basis.Mul(scaledYaw, zToY))
		ground := Vector3.XYZ{X: base.X, Y: vr.HeightAt(base), Z: base.Z}
		finalOrigin := Vector3.Add(ground, Vector3.XYZ(rotatedOrigin))

		return Transform3D.BasisOrigin{Basis: corrected, Origin: finalOrigin}
	}

	// Foliage, boulders, pebbles etc: preserve the authored orientation from
	// the glb (Y-up for scenery-placed props) and only add the random yaw.
	corrected := Basis.Mul(scaledYaw, source.Basis)
	rotatedOrigin := Basis.Transform(source.Origin, scaledYaw)
	ground := Vector3.XYZ{X: base.X, Y: vr.HeightAt(base), Z: base.Z}
	finalOrigin := Vector3.Add(ground, Vector3.XYZ(rotatedOrigin))

	return Transform3D.BasisOrigin{Basis: corrected, Origin: finalOrigin}
}

// grassMeshFor resolves (and caches) the Mesh (plus its source-scene
// transform) to instance for a dressing Design. Returns ok=false until
// the Design's .glb has been imported. The category determines whether
// we force the grass wind wrapper (only for grasses) or leave the mesh's
// own materials (foliage/mineral reuse their authored shaders).
func (vr *TerrainEditor) grassMeshFor(design musical.Design, category string) (grassAsset, bool) {
	if a, ok := vr.grassMeshes[design]; ok {
		return a, a.mesh != Mesh.Nil
	}
	sceneID, ok := vr.client.packed_scenes[design]
	if !ok {
		return grassAsset{}, false
	}
	scene, ok := sceneID.Instance()
	if !ok {
		return grassAsset{}, false
	}
	root := Object.To[Node3D.Instance](scene.Instantiate())
	mi, sourceXform, found := firstMeshInstance(root.AsNode())
	if !found {
		root.AsNode().QueueFree()
		return grassAsset{}, false
	}
	mesh := Object.Leak(mi.Mesh())
	// MultiMesh cannot carry per-instance material overrides, so any sway has to
	// live in the one surface material. Grasses wrap in grass_wind (every vertex
	// leans by height), foliage wraps in foliage_wind_mm (trunk planted, canopy
	// flutters); everything else (boulders, mineral, pebbles) keeps the imported
	// material exactly as authored so scattered instances match the same mesh
	// placed as scenery.
	kind := vr.windKindFor(category)
	for i := 0; i < mesh.GetSurfaceCount(); i++ {
		src := mi.GetSurfaceOverrideMaterial(i)
		if src == Material.Nil {
			src = mesh.SurfaceGetMaterial(i)
		}
		if src == Material.Nil {
			continue
		}
		switch kind {
		case "grass":
			mesh.SurfaceSetMaterial(i, vr.grassWindMaterial(src))
		case "foliage":
			mesh.SurfaceSetMaterial(i, vr.foliageWindMaterial(src))
		default:
			mesh.SurfaceSetMaterial(i, src)
		}
	}
	root.AsNode().QueueFree()
	asset := grassAsset{mesh: mesh, xform: sourceXform}
	vr.grassMeshes[design] = asset
	return asset, true
}

// grassWindMaterial wraps a grass surface's imported material in the shared
// wind-sway ShaderMaterial so the MultiMesh blades animate. To keep the look
// identical to the source (the wind shader can't subclass a BaseMaterial3D),
// the surface's albedo, normal map, roughness and metallic are copied across.
// Wind itself is a global shader parameter (see updateWeatherIntensity), not a
// per-material uniform. If the shader isn't loaded, the source isn't a
// BaseMaterial3D, or it carries no albedo texture, the original material is
// returned unchanged so the grass still renders — just without wind.
func (vr *TerrainEditor) grassWindMaterial(src Material.Instance) Material.Instance {
	if vr.grassWindShader == Shader.Nil {
		return src
	}
	base, ok := Object.As[BaseMaterial3D.Instance](src)
	if !ok {
		return src
	}
	tex := base.AlbedoTexture()
	if tex == Texture2D.Nil {
		return src
	}
	mat := ShaderMaterial.New().
		SetShader(vr.grassWindShader).
		SetShaderParameter("albedo_texture", tex).
		SetShaderParameter("albedo", base.AlbedoColor()).
		SetShaderParameter("roughness", base.Roughness()).
		SetShaderParameter("metallic", base.Metallic())
	// Carry the normal map across so the blades keep their shaded relief
	// (the source grass materials are normal-mapped; without this they look
	// flat). has_normal_texture gates sampling so an unset sampler — which
	// Godot fills with white — never tilts the normal.
	if base.NormalEnabled() {
		if nrm := base.NormalTexture(); nrm != Texture2D.Nil {
			mat.SetShaderParameter("normal_texture", nrm)
			mat.SetShaderParameter("normal_scale", base.NormalScale())
			mat.SetShaderParameter("has_normal_texture", true)
		}
	}
	return mat.AsMaterial()
}

// foliageWindMaterial is the foliage counterpart of grassWindMaterial: it wraps
// a scattered foliage surface's imported material in foliage_wind_mm so the
// MultiMesh instances sway (trunk planted, canopy fluttering) while keeping the
// authored albedo/normal/roughness/metallic look. Wind is global (see
// updateWeatherIntensity), shared with the grass. Surfaces that aren't a
// textured BaseMaterial3D (e.g. an untextured trunk) are returned unchanged so
// they still render — just static, which is what a rigid stem wants anyway.
func (vr *TerrainEditor) foliageWindMaterial(src Material.Instance) Material.Instance {
	if vr.foliageWindShader == Shader.Nil {
		return src
	}
	base, ok := Object.As[BaseMaterial3D.Instance](src)
	if !ok {
		return src
	}
	tex := base.AlbedoTexture()
	if tex == Texture2D.Nil {
		return src
	}
	mat := ShaderMaterial.New().
		SetShader(vr.foliageWindShader).
		SetShaderParameter("albedo_texture", tex).
		SetShaderParameter("albedo", base.AlbedoColor()).
		SetShaderParameter("roughness", base.Roughness()).
		SetShaderParameter("metallic", base.Metallic())
	if base.NormalEnabled() {
		if nrm := base.NormalTexture(); nrm != Texture2D.Nil {
			mat.SetShaderParameter("normal_texture", nrm)
			mat.SetShaderParameter("normal_scale", base.NormalScale())
			mat.SetShaderParameter("has_normal_texture", true)
		}
	}
	return mat.AsMaterial()
}

// firstMeshInstance returns the first MeshInstance3D found in a (possibly
// nested) imported scene tree together with the accumulated transform
// (relative to the scene root) that was applied to it by the glTF nodes.
// .glb roots usually wrap the mesh a couple of nodes deep; preserving the
// transform ensures authored pivots and orientation fixes are not lost
// when the raw mesh is fed to a MultiMesh.
func firstMeshInstance(n Node.Instance) (MeshInstance3D.Instance, Transform3D.BasisOrigin, bool) {
	identity := Transform3D.BasisOrigin{Basis: Basis.Identity, Origin: Vector3.Zero}
	return firstMeshRecursive(n, identity)
}

func firstMeshRecursive(n Node.Instance, parentXform Transform3D.BasisOrigin) (MeshInstance3D.Instance, Transform3D.BasisOrigin, bool) {
	// Compute the transform of *this* node (if it is a Node3D) composed
	// onto the parent. This becomes the global xform for any MI here.
	nodeXform := parentXform
	if n3d, ok := Object.As[Node3D.Instance](n); ok {
		local := n3d.Transform()
		nodeXform = Transform3D.Mul(parentXform, local)
	}

	if mi, ok := Object.As[MeshInstance3D.Instance](n); ok {
		return mi, nodeXform, true
	}
	for i := 0; i < n.GetChildCount(); i++ {
		if mi, xf, ok := firstMeshRecursive(n.GetChild(i), nodeXform); ok {
			return mi, xf, true
		}
	}
	return MeshInstance3D.Nil, Transform3D.BasisOrigin{}, false
}

// grassSeed derives a deterministic 64-bit seed from the sculpt's identity
// so the scatter is identical on every client. Coordinates are quantised
// to the millimetre so float round-tripping through the musical log can't
// perturb the layout.
func grassSeed(author musical.Author, target Vector3.XYZ, radius Float.X) uint64 {
	q := func(f Float.X) uint64 { return uint64(int64(math.Round(float64(f) * 1000))) }
	h := uint64(0x9E3779B97F4A7C15)
	mix := func(x uint64) {
		h ^= x
		h *= 0xff51afd7ed558ccd
		h ^= h >> 33
	}
	mix(uint64(author))
	mix(q(target.X))
	mix(q(target.Y))
	mix(q(target.Z))
	mix(q(radius))
	return h
}

// grassRNG is a small splitmix64 generator — deterministic across
// platforms and self-contained so the scatter never depends on Go's
// global rand (whose state isn't reproducible between clients).
type grassRNG struct{ state uint64 }

func (r *grassRNG) next() uint64 {
	r.state += 0x9E3779B97F4A7C15
	z := r.state
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}

// float returns a value in [0, 1). 9007199254740992 == 2^53 is the largest
// exactly representable power of two in a float64 mantissa.
func (r *grassRNG) float() float64 { return float64(r.next()>>11) / 9007199254740992.0 }

// nextSeed advances a scatter seed to a new, well-distributed value (one
// splitmix64 step). Used to roll the dressing brush's seed forward when a tool
// is armed and after each committed segment, so successive patches differ.
func nextSeed(s uint64) uint64 { return (&grassRNG{state: s}).next() }

// paramsFor returns the scatter tuning for the given dressing tab (category).
// Unknown categories fall back to the grasses defaults so old sculpts and
// any future tabs continue to work without special cases everywhere.
func (vr *TerrainEditor) paramsFor(category string) dressingParams {
	if p, ok := dressingDefaults[category]; ok {
		return p
	}
	return dressingDefaults["grasses"]
}

// windKindFor reports how meshes for this category should sway: "grass" (blade
// lean), "foliage" (planted trunk, fluttering canopy) or "" (no wind wrap).
func (vr *TerrainEditor) windKindFor(category string) string {
	return vr.paramsFor(category).windKind
}

// isDressingCategory reports whether the string is one of the known dressing
// Slider values used for both normal dressing strokes and the category-clear
// tools in the "removal" tab.
func isDressingCategory(s string) bool {
	_, ok := dressingDefaults[s]
	return ok
}

// dressingCount derives instance count from disc area × density × perArea
// (category specific) with the category's safety cap.
func dressingCount(prms dressingParams, radius, density Float.X) int {
	area := math.Pi * float64(radius) * float64(radius)
	c := int(area * float64(density) * prms.perArea)
	// Guarantee at least one instance for any positive-density stroke so
	// foliage/boulder brushes (very low perArea) still produce visible
	// results on a normal click instead of requiring a huge brush first.
	if c == 0 && density > 0 && radius > 0.01 {
		c = 1
	}
	if c > prms.maxInst {
		c = prms.maxInst
	}
	return c
}
