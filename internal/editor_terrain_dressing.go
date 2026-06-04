package internal

import (
	"math"
	"strings"

	"graphics.gd/classdb/BaseMaterial3D"
	"graphics.gd/classdb/GeometryInstance3D"
	"graphics.gd/classdb/Material"
	"graphics.gd/classdb/Mesh"
	"graphics.gd/classdb/MeshInstance3D"
	"graphics.gd/classdb/MultiMesh"
	"graphics.gd/classdb/MultiMeshInstance3D"
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/Resource"
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
	category string // dressing tab (grasses/foliage/boulder/...) for params + transform rules

	// Per-patch MultiMeshes, used ONLY by the transient hover preview
	// (dressPreview): a single previewed patch renders one MultiMesh per design
	// sub-mesh (see grassAsset.parts) so it can be torn down independently as the
	// brush moves. COMMITTED patches leave these empty and render through the
	// shared per-design grassRender instead (see grassRenders / repopulateDesign).
	mms     []MultiMesh.Instance
	mmNodes []MultiMeshInstance3D.Instance

	// Per-instance scatter, kept so a later height sculpt can re-plant the
	// instances on the reshaped surface. bases hold world X/Z (Y is
	// re-sampled from the terrain each time a transform is built).
	bases  []Vector3.XYZ
	yaws   []Angle.Radians
	scales []Float.X
}

// grassRender is the merged, per-design rendering of all committed dressing
// instances of one Design. It holds one MultiMesh (plus its node) per asset
// sub-mesh part; every grassPatch sharing the design feeds its visible
// instances into these buffers (see repopulateDesign). Aggregating across
// patches means the scene renders one MultiMeshInstance3D per (design, part)
// rather than one per (patch, part), collapsing hundreds of patch nodes into a
// handful and cutting the draw-call count to match.
type grassRender struct {
	mms     []MultiMesh.Instance
	mmNodes []MultiMeshInstance3D.Instance
}

// free queues every node of the merged render for deletion and clears its
// slices. The MultiMeshes themselves finalize with their bound nodes.
func (r *grassRender) free() {
	for _, n := range r.mmNodes {
		if n != MultiMeshInstance3D.Nil {
			n.AsNode().QueueFree()
		}
	}
	r.mms = r.mms[:0]
	r.mmNodes = r.mmNodes[:0]
}

// grassMeshPart is one renderable sub-mesh of a dressing design plus the
// transform that was applied to it inside its source .glb scene (product of
// the MeshInstance3D and ancestor nodes). Composing this into every MultiMesh
// instance transform preserves authored pivots and any orientation fixes the
// author put on the nodes (rather than assuming raw vertex data is Y-up at the
// origin).
type grassMeshPart struct {
	mesh  Mesh.Instance
	xform Transform3D.BasisOrigin
}

// grassAsset is the set of sub-meshes a dressing design instances. Library
// foliage props are frequently authored as several separate meshes (a trunk
// node plus distinct canopy / leaf nodes — see the glTF meshes), so a design
// resolves to one or more parts; instancing only the first dropped whole parts
// of the model (typically the leaves, which then went invisible).
type grassAsset struct {
	parts []grassMeshPart
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
	// The shrooms brush carries the wildfire_games mushroom props (also authored at
	// scenery-library scale) at baseScale 0.25 — a touch bigger than the 0.1 they'd
	// get under foliage, and static (windKind "") rather than swaying.
	baseScale Float.X
	// scaleMin..scaleMax is the per-instance random spread applied on top of
	// baseScale — a modest variation around 1.0 for natural variety, so a patch
	// doesn't mix wildly different sizes.
	scaleMin Float.X
	scaleMax Float.X
	// windKind selects how the scattered mesh sways: "grass" wraps the material
	// in grass_wind (every vertex leans by height — blades), "foliage" wraps it
	// in foliage_wind_mm (trunk planted, canopy flutters), "" leaves the imported
	// material as authored (no sway).
	windKind string
}

var dressingDefaults = map[string]dressingParams{
	"grasses":  {perArea: 6.0, maxInst: 3000, baseScale: 1.0, scaleMin: 0.7, scaleMax: 1.2, windKind: "grass"},
	"pebbles":  {perArea: 4.0, maxInst: 2000, baseScale: 1.0, scaleMin: 0.5, scaleMax: 1.4, windKind: ""},
	"foliage":  {perArea: 1.5, maxInst: 400, baseScale: sceneryLibraryScale, scaleMin: 0.85, scaleMax: 1.15, windKind: "foliage"},
	"shrooms":  {perArea: 2.0, maxInst: 1000, baseScale: 0.25, scaleMin: 0.7, scaleMax: 1.3, windKind: ""},
	"boulder":  {perArea: 1.5, maxInst: 600, baseScale: sceneryLibraryScale, scaleMin: 0.85, scaleMax: 1.15, windKind: ""},
	"boulders": {perArea: 0.8, maxInst: 500, baseScale: sceneryLibraryScale, scaleMin: 0.8, scaleMax: 1.2, windKind: ""},
	"rocks":    {perArea: 1.0, maxInst: 500, baseScale: sceneryLibraryScale, scaleMin: 0.8, scaleMax: 1.2, windKind: ""},
}

// ensureGrassRender returns (building on first use) the merged render for a
// design: one MultiMesh + MultiMeshInstance3D per asset sub-mesh part, parented
// under the editor. Rebuilt if the design's part count ever changes. The
// instance buffers are filled separately by repopulateDesign.
func (vr *TerrainEditor) ensureGrassRender(design musical.Design, asset grassAsset) *grassRender {
	if r, ok := vr.grassRenders[design]; ok && len(r.mms) == len(asset.parts) {
		return r
	} else if ok {
		r.free()
	}
	r := &grassRender{}
	for _, part := range asset.parts {
		mm := MultiMesh.New()
		mm.SetTransformFormat(MultiMesh.Transform3d)
		mm.SetMesh(part.mesh)
		mmi := MultiMeshInstance3D.New()
		mmi.SetMultimesh(mm)
		// Keep scattered dressing OUT of any VoxelGI/SDFGI bake: thousands of
		// instances feeding the GI voxelisation tanks the frame-rate the moment GI
		// is enabled, for negligible bounce. GiModeDisabled skips them in the GI pass.
		mmi.AsGeometryInstance3D().SetGiMode(GeometryInstance3D.GiModeDisabled)
		vr.AsNode().AddChild(mmi.AsNode())
		r.mms = append(r.mms, mm)
		r.mmNodes = append(r.mmNodes, mmi)
	}
	vr.grassRenders[design] = r
	return r
}

// repopulateDesign rebuilds the merged MultiMesh buffers for one design from
// EVERY committed grassPatch sharing it, including only the instances whose
// terrain tile is currently revealed. Patches keep their full bases/yaws/scales
// (the complete sculpt data) so a later reveal re-adds the omitted instances
// without re-sampling RNG. Two passes over the patches (count, then fill) avoid
// any temporary allocation: SetInstanceCount needs the total up front.
func (vr *TerrainEditor) repopulateDesign(design musical.Design) {
	asset, ok := vr.grassMeshes[design]
	if !ok || len(asset.parts) == 0 {
		if r, ok := vr.grassRenders[design]; ok {
			for _, mm := range r.mms {
				if mm != MultiMesh.Nil {
					mm.SetInstanceCount(0)
				}
			}
		}
		return
	}
	n := 0
	for _, p := range vr.grassPatches {
		if p.design != design {
			continue
		}
		for i := range p.bases {
			if vr.tileRevealedAt(p.bases[i]) {
				n++
			}
		}
	}
	r := vr.ensureGrassRender(design, asset)
	// Each part's MultiMesh gets the SAME scatter, composed with that part's own
	// authored sub-mesh transform so trunk / canopy / leaves stay registered.
	for pi := range r.mms {
		mm := r.mms[pi]
		if mm == MultiMesh.Nil || pi >= len(asset.parts) {
			continue
		}
		xform := asset.parts[pi].xform
		mm.SetInstanceCount(n)
		k := 0
		for _, p := range vr.grassPatches {
			if p.design != design {
				continue
			}
			for i := range p.bases {
				if vr.tileRevealedAt(p.bases[i]) {
					mm.SetInstanceTransform(k, vr.grassTransform(p.bases[i], p.yaws[i], p.scales[i], xform))
					k++
				}
			}
		}
	}
}

// markGrassDirty flags a design's merged render for rebuild. Unless rendering is
// deferred (a bulk replay / multi-patch erase / height-drag reproject batching
// many marks), it repopulates immediately so a single live stroke shows at once.
func (vr *TerrainEditor) markGrassDirty(design musical.Design) {
	vr.grassDirty[design] = true
	if !vr.grassDeferRender {
		vr.flushGrassRenders()
	}
}

// flushGrassRenders repopulates every design marked dirty, then clears the set.
func (vr *TerrainEditor) flushGrassRenders() {
	for d := range vr.grassDirty {
		vr.repopulateDesign(d)
		delete(vr.grassDirty, d)
	}
}

// deferGrassRender coalesces a burst of markGrassDirty calls into one flush. It
// sets grassDeferRender and returns a restore func that, only if it owned the
// defer (no outer batch already active), flushes the accumulated dirty designs.
// Nest-safe: an inner batch inside an outer one is a no-op on restore. Use as
// `defer vr.deferGrassRender()()`.
func (vr *TerrainEditor) deferGrassRender() func() {
	if vr.grassDeferRender {
		return func() {}
	}
	vr.grassDeferRender = true
	return func() {
		vr.grassDeferRender = false
		vr.flushGrassRenders()
	}
}

// populateGrassMM rebuilds the per-patch MultiMesh for the hover PREVIEW patch
// so that it only contains the instances whose terrain tile is currently
// revealed. The patch keeps its full bases/yaws/scales (the complete sculpt
// data) so that later reveals can add the omitted instances without re-sampling
// RNG. Committed patches do NOT use this — they render via repopulateDesign.
func (vr *TerrainEditor) populateGrassMM(patch *grassPatch) {
	asset, ok := vr.grassMeshes[patch.design]
	if !ok || len(asset.parts) == 0 {
		for _, mm := range patch.mms {
			if mm != MultiMesh.Nil {
				mm.SetInstanceCount(0)
			}
		}
		return
	}
	var visible []int
	for i := range patch.bases {
		if vr.tileRevealedAt(patch.bases[i]) {
			visible = append(visible, i)
		}
	}
	n := len(visible)
	// Each part's MultiMesh gets the SAME scatter, composed with that part's own
	// authored sub-mesh transform so trunk / canopy / leaves stay registered.
	for pi := range patch.mms {
		mm := patch.mms[pi]
		if mm == MultiMesh.Nil || pi >= len(asset.parts) {
			continue
		}
		xform := asset.parts[pi].xform
		mm.SetInstanceCount(n)
		for k, i := range visible {
			mm.SetInstanceTransform(k, vr.grassTransform(patch.bases[i], patch.yaws[i], patch.scales[i], xform))
		}
	}
}

// refreshGrassVisibility repopulates every design's merged render from its
// patches' full data, filtering to only the instances on revealed tiles. Used
// after any tile reveal/hide so grasses appear/disappear with their terrain
// chunk. The dirty set dedupes designs, so each is repopulated once.
func (vr *TerrainEditor) refreshGrassVisibility() {
	defer vr.deferGrassRender()()
	for _, p := range vr.grassPatches {
		vr.markGrassDirty(p.design)
	}
}

// clearGrass removes every rendered grass patch (and any pending scatters whose
// mesh had not yet loaded). Used by recomputeGrass before replaying the active
// dressing history on undo/redo. The committed patches own no nodes; the merged
// per-design renders do, so those are freed here.
func (vr *TerrainEditor) clearGrass() {
	for d, r := range vr.grassRenders {
		r.free()
		delete(vr.grassRenders, d)
		delete(vr.grassDirty, d)
	}
	vr.grassPatches = vr.grassPatches[:0]
	vr.pendingGrass = vr.pendingGrass[:0]
}

// recomputeGrass rebuilds all dressing from scratch over the non-reverted strokes
// in grassHistory, in commit order so each erase lands after the scatters it
// trims. Used on undo/redo of a dressing stroke: a region-based erase can only be
// reverted by replaying what survives, not by an arithmetic inverse.
func (vr *TerrainEditor) recomputeGrass() {
	defer vr.deferGrassRender()()
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
	defer timeIn(&bucketDressing)()
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
	patch.category = canonicalDressingCategory(brush.Slider)
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

// freeNodes queues every MultiMeshInstance3D node of the patch for deletion and
// clears the patch's node/MultiMesh slices. Shared by clearGrass, the empty-
// patch path of eraseGrass, clearDressPreview and node rebuilds.
func (p *grassPatch) freeNodes() {
	for _, n := range p.mmNodes {
		if n != MultiMeshInstance3D.Nil {
			n.AsNode().QueueFree()
		}
	}
	p.mms = p.mms[:0]
	p.mmNodes = p.mmNodes[:0]
}

// freeDressCaches releases every resource the dressing system keeps alive for the
// session: the merged per-design MultiMeshes, plus the grass meshes and shared materials (no
// longer Object.Leak'd, so they're freeable). Registered as a shutdown cleanup (see
// Ready) so they don't report as leaks at exit. Object.Free only decrements, so a
// MultiMesh still bound to a live MultiMeshInstance3D — or a mesh/material still
// referenced by a patch — is destroyed for real only when those nodes finalize during
// teardown. nil-guarded so a second run is a no-op.
func (vr *TerrainEditor) freeDressCaches() {
	for d, r := range vr.grassRenders {
		for _, mm := range r.mms {
			if mm != MultiMesh.Nil {
				Object.Free(mm)
			}
		}
		r.mms = nil
		delete(vr.grassRenders, d)
	}
	vr.grassPatches = nil
	for _, asset := range vr.grassMeshes {
		for _, part := range asset.parts {
			if part.mesh != Mesh.Nil {
				Object.Free(part.mesh)
			}
		}
	}
	vr.grassMeshes = nil
	for _, mat := range vr.dressSharedMats {
		if mat != Material.Nil {
			Object.Free(mat)
		}
	}
	vr.dressSharedMats = nil
}

// buildPatchNodes (re)creates one MultiMeshInstance3D per asset part, parents
// them under the editor and stores them on the patch (aligned with
// asset.parts). Any pre-existing nodes are freed first, so it doubles as the
// design-swap rebuild for the hover preview. Instance counts/transforms are
// filled separately by populateGrassMM (only the revealed-tile subset).
func (vr *TerrainEditor) buildPatchNodes(patch *grassPatch, asset grassAsset) {
	patch.freeNodes()
	for _, part := range asset.parts {
		mm := MultiMesh.New()
		mm.SetTransformFormat(MultiMesh.Transform3d)
		mm.SetMesh(part.mesh)
		mmi := MultiMeshInstance3D.New()
		mmi.SetMultimesh(mm)
		// Keep the scattered grass OUT of any VoxelGI/SDFGI bake. Each patch can
		// hold thousands of instances and there may be many patches, so feeding
		// them to the GI voxelisation tanks the frame-rate the moment GI is turned
		// on for negligible visual gain (grass barely bounces light). GiModeDisabled
		// makes the renderer skip these instances entirely during the GI pass.
		mmi.AsGeometryInstance3D().SetGiMode(GeometryInstance3D.GiModeDisabled)
		vr.AsNode().AddChild(mmi.AsNode())
		patch.mms = append(patch.mms, mm)
		patch.mmNodes = append(patch.mmNodes, mmi)
	}
}

// makeGrassPatch builds a scatter patch plus its own per-patch MultiMesh node
// per design sub-mesh for one dressing stroke, populated for the currently
// revealed tiles. Used ONLY by the transient hover preview (dressPreview), which
// must render and tear down a single patch independently; committed strokes go
// through buildGrassPatch (data only) and the shared per-design grassRender.
// Returns nil when the stroke scatters nothing.
func (vr *TerrainEditor) makeGrassPatch(brush musical.Sculpt, asset grassAsset) *grassPatch {
	patch := &grassPatch{}
	if !vr.fillPatch(patch, brush, asset) {
		return nil
	}
	vr.buildPatchNodes(patch, asset)
	vr.populateGrassMM(patch)
	return patch
}

// buildGrassPatch scatters one committed dressing stroke and registers the
// resulting grassPatch so the placement can be re-projected later. The patch
// only holds scatter DATA (no per-patch nodes); its instances render through
// the shared per-design grassRender, which markGrassDirty repopulates.
func (vr *TerrainEditor) buildGrassPatch(brush musical.Sculpt, asset grassAsset) {
	patch := &grassPatch{}
	if !vr.fillPatch(patch, brush, asset) {
		return
	}
	vr.grassPatches = append(vr.grassPatches, patch)
	vr.markGrassDirty(patch.design)
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
	} else {
		prevDesign := vr.dressPreview.design // fillPatch overwrites patch.design
		if !vr.fillPatch(vr.dressPreview, brush, asset) {
			vr.clearDressPreview()
			return
		}
		// Reuse the existing nodes on a plain move (no per-move node churn), but
		// rebuild them when the design changed — its part set (trunk/canopy/leaf
		// MultiMeshes) may differ from the one the preview nodes were built for.
		if prevDesign != brush.Design || len(vr.dressPreview.mms) != len(asset.parts) {
			vr.buildPatchNodes(vr.dressPreview, asset)
		}
		vr.populateGrassMM(vr.dressPreview)
	}
	vr.dressPreviewKey = key
}

// clearDressPreview removes the transient hover scatter, if any.
func (vr *TerrainEditor) clearDressPreview() {
	if vr.dressPreview == nil {
		return
	}
	vr.dressPreview.freeNodes()
	vr.dressPreview = nil
	vr.dressPreviewKey = dressKey{}
}

// reprojectGrass re-plants the instances of every patch overlapping the
// given sculpt disc back onto the (just reshaped) terrain surface.
func (vr *TerrainEditor) reprojectGrass(target Vector3.XYZ, radius Float.X) {
	defer vr.deferGrassRender()()
	for _, patch := range vr.grassPatches {
		dx := float64(patch.target.X - target.X)
		dz := float64(patch.target.Z - target.Z)
		if Float.X(math.Hypot(dx, dz)) > patch.radius+radius {
			continue
		}
		// Mark the design dirty; the deferred flush repopulates each affected
		// design's merged buffer once, recomputing grassTransform (which re-queries
		// HeightAt) for the revealed-tile instances of every patch sharing it.
		vr.markGrassDirty(patch.design)
	}
}

// eraseGrass removes instances of the given Design whose scatter centers
// lie inside the brush disc. It filters the per-patch instance lists in
// place and rebuilds the corresponding MultiMeshes so the deletion is
// immediately visible and is reproduced identically on every client that
// replays the (negative-Amount) sculpt.
func (vr *TerrainEditor) eraseGrass(brush musical.Sculpt) {
	defer timeIn(&bucketDressing)()
	// One flush for the whole erase: a bomb/category clear can trim many patches
	// across several designs, and each survivor/removal only mutates Go data here.
	defer vr.deferGrassRender()()
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
		} else if p.category != canonicalDressingCategory(brush.Slider) {
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
			// Patch wiped out: drop it and rebuild its design's merged buffer
			// without these instances.
			design := p.design
			vr.grassPatches = append(vr.grassPatches[:i], vr.grassPatches[i+1:]...)
			vr.markGrassDirty(design)
			continue
		}

		p.bases = keptB
		p.yaws = keptY
		p.scales = keptS
		// Rebuild the design's merged buffer from the kept (full) data; populate
		// only adds the subset whose tiles are revealed.
		vr.markGrassDirty(p.design)
	}
}

// grassTransform builds the instance transform for a dressing instance:
// yaw about world up, uniform scale, planted at terrain height under the
// base X/Z. Every dressing source mesh is authored/imported Y-up (the grass
// packs are now baked upright at import time — see library/import_yughues.py —
// matching the foliage/mineral/boulder scenery props), so the authored
// orientation is preserved and only the random yaw+scale is applied on top.
func (vr *TerrainEditor) grassTransform(base Vector3.XYZ, yaw Angle.Radians, scale Float.X, source Transform3D.BasisOrigin) Transform3D.BasisOrigin {
	yawB := Basis.FromEuler(Euler.Radians{Y: yaw}, Angle.OrderYXZ)
	scaledYaw := Basis.Scaled(yawB, Vector3.New(scale, scale, scale))

	corrected := Basis.Mul(scaledYaw, source.Basis)
	rotatedOrigin := Basis.Transform(source.Origin, scaledYaw)
	ground := Vector3.XYZ{X: base.X, Y: vr.HeightAt(base), Z: base.Z}
	finalOrigin := Vector3.Add(ground, Vector3.XYZ(rotatedOrigin))

	return Transform3D.BasisOrigin{Basis: corrected, Origin: finalOrigin}
}

// grassMeshFor resolves (and caches) the set of sub-meshes (each plus its
// source-scene transform) to instance for a dressing Design. Returns ok=false
// until the Design's .glb has been imported AND every material-sharing surface
// has finished downloading. The category determines whether we force the grass
// wind wrapper (only for grasses) or leave the mesh's own materials
// (foliage/mineral reuse their authored shaders).
func (vr *TerrainEditor) grassMeshFor(design musical.Design, category string) (grassAsset, bool) {
	if a, ok := vr.grassMeshes[design]; ok {
		return a, len(a.parts) > 0
	}
	scene, ok := vr.client.sceneFor(design)
	if !ok {
		return grassAsset{}, false
	}
	root := Object.To[Node3D.Instance](scene.Instantiate())
	parts := allMeshInstances(root.AsNode())
	if len(parts) == 0 {
		root.AsNode().QueueFree()
		return grassAsset{}, false
	}
	// Scenery library props (foliage/mineral/boulders) are
	// MaterialSharingMeshInstance3D nodes whose surface material streams from
	// library.pck. grassMeshFor extracts the bare meshes without ever adding the
	// instances to the tree, so those nodes' Ready — which is what loads and
	// assigns the shared material — never runs, leaving surface 0 on Godot's grey
	// default. We resolve the materials ourselves. First make sure EVERY part's
	// material has downloaded before committing to (and leaking) any mesh, so a
	// retry frame doesn't leak the parts that happened to be ready already; until
	// then report not-ready so the stroke parks in pendingGrass / the hover
	// preview retries next frame instead of baking grey meshes that never refresh.
	// We probe every part (rather than bailing on the first) so all of a
	// multi-part prop's materials start downloading at once instead of one per
	// retry frame.
	allReady := true
	for _, part := range parts {
		if ms, ok := Object.As[*MaterialSharingMeshInstance3D](part.mi); ok {
			if _, ready := vr.sharedDressMaterial(ms); !ready {
				allReady = false
			}
		}
	}
	if !allReady {
		root.AsNode().QueueFree()
		return grassAsset{}, false
	}
	// MultiMesh cannot carry per-instance material overrides, so any sway has to
	// live in the surface material. Grasses wrap in grass_wind (every vertex leans
	// by height), foliage wraps in foliage_wind_mm (trunk planted, canopy
	// flutters); everything else (boulders, mineral, pebbles) keeps the imported
	// material exactly as authored so scattered instances match the same mesh
	// placed as scenery.
	kind := vr.windKindFor(category)
	var asset grassAsset
	for _, part := range parts {
		mi := part.mi
		// Kept alive for the session by grassMeshes (a TerrainEditor field, walked by
		// keepalive) and released in freeDressCaches at shutdown — not Object.Leak'd,
		// which would pin it un-freeably and report as a leak at exit.
		mesh := mi.Mesh()
		if mesh == Mesh.Nil {
			continue
		}
		if ms, ok := Object.As[*MaterialSharingMeshInstance3D](mi); ok {
			if mat, ready := vr.sharedDressMaterial(ms); ready && mat != Material.Nil {
				mesh.SurfaceSetMaterial(0, mat)
			}
		}
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
				mesh.SurfaceSetMaterial(i, vr.foliageWindMaterial(src, 1.0))
			default:
				// Rigid scatter (pebbles / shrooms / boulders): no wind sway, but
				// still wrap in the foliage shader so the instances RIDE the live
				// height-brush preview (the grass_brush_* block) like grass and
				// foliage do — otherwise raise/lower/plateau visibly "ignore" them
				// until the stroke commits. sway 0 keeps them perfectly still.
				mesh.SurfaceSetMaterial(i, vr.foliageWindMaterial(src, 0.0))
			}
		}
		asset.parts = append(asset.parts, grassMeshPart{mesh: mesh, xform: part.xform})
	}
	root.AsNode().QueueFree()
	if len(asset.parts) == 0 {
		return grassAsset{}, false
	}
	vr.grassMeshes[design] = asset
	return asset, true
}

// sharedDressMaterial resolves the surface material for a material-sharing
// scenery mesh used by the dressing brushes, off the main thread so a
// not-yet-downloaded library.pck material doesn't stall the editor/VR. The
// first call kicks off the load and reports ready=false; subsequent calls
// return ready=false while it's in flight, then the cached material once it
// has arrived. AO overrides are duplicated onto the material exactly as the
// synchronous MaterialSharingMeshInstance3D path does. A failed load caches
// Material.Nil with ready=true, so the caller stops retrying and falls back
// to the (untextured) mesh rather than spinning forever.
func (vr *TerrainEditor) sharedDressMaterial(ms *MaterialSharingMeshInstance3D) (Material.Instance, bool) {
	key := sharingKey{Identity: ms.Identity, Material: ms.Material}
	if mat, ok := vr.dressSharedMats[key]; ok {
		return mat, true
	}
	if vr.dressMatPending[key] {
		return Material.Nil, false
	}
	vr.dressMatPending[key] = true
	overrideAO := ms.OverrideAO
	LoadAsync(ms.Material, func(mat Material.Instance) {
		final := mat
		if mat != Material.Nil && overrideAO != Texture2D.Nil {
			// Held by dressSharedMats (walked by keepalive) and freed in freeDressCaches
			// at shutdown — not Object.Leak'd, which would make it un-freeable.
			dup := Resource.Duplicate(Object.To[BaseMaterial3D.Instance](mat))
			dup.SetAoTexture(overrideAO)
			final = dup.AsMaterial()
		}
		vr.dressSharedMats[key] = final
		delete(vr.dressMatPending, key)
	})
	return Material.Nil, false
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
//
// sway scales ALL wind motion (1.0 for foliage proper). The rigid dressing
// categories — pebbles, shrooms, boulders — pass sway 0 so they keep the
// shader's height-brush preview (riding the live raise/lower/flatten ghost) but
// never wobble, matching the same .glb placed as static scenery.
func (vr *TerrainEditor) foliageWindMaterial(src Material.Instance, sway Float.X) Material.Instance {
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
		SetShaderParameter("sway_amount", sway).
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

// meshInstancePart pairs a MeshInstance3D found in an imported scene with the
// transform (relative to the scene root) that the glTF nodes applied to it.
type meshInstancePart struct {
	mi    MeshInstance3D.Instance
	xform Transform3D.BasisOrigin
}

// allMeshInstances returns EVERY MeshInstance3D in a (possibly nested) imported
// scene tree, each with its accumulated transform relative to the scene root.
// .glb roots usually wrap the meshes a couple of nodes deep, and a single prop
// is frequently several mesh nodes (trunk / canopy / leaves); preserving each
// node's transform keeps authored pivots and orientation fixes intact when the
// raw meshes are fed to MultiMeshes, and collecting all of them (not just the
// first) means no part of the model goes missing.
func allMeshInstances(n Node.Instance) []meshInstancePart {
	identity := Transform3D.BasisOrigin{Basis: Basis.Identity, Origin: Vector3.Zero}
	var out []meshInstancePart
	collectMeshInstances(n, identity, &out)
	return out
}

func collectMeshInstances(n Node.Instance, parentXform Transform3D.BasisOrigin, out *[]meshInstancePart) {
	// Compose this node's local transform (if it is a Node3D) onto the parent;
	// this is the global xform for a MeshInstance3D here and the parent for any
	// descendants.
	nodeXform := parentXform
	if n3d, ok := Object.As[Node3D.Instance](n); ok {
		nodeXform = Transform3D.Mul(parentXform, n3d.Transform())
	}
	if mi, ok := Object.As[MeshInstance3D.Instance](n); ok {
		*out = append(*out, meshInstancePart{mi: mi, xform: nodeXform})
	}
	for i := 0; i < n.GetChildCount(); i++ {
		collectMeshInstances(n.GetChild(i), nodeXform, out)
	}
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

// canonicalDressingCategory folds legacy dressing category names onto their current
// spelling so musicals recorded before a rename keep working. The wildfire_games
// "mineral" category was renamed to "boulder" (its assets moved to the boulder/
// library dir); pre-rename sculpts carry Slider "mineral", so it maps to "boulder"
// wherever a category string is consumed — params, wind, patch identity, and the
// category-clear match. New sculpts already use "boulder".
func canonicalDressingCategory(s string) string {
	if s == "mineral" {
		return "boulder"
	}
	return s
}

// boulderCompatPath redirects a legacy "mineral" resource path to its post-rename
// "boulder" location. Design paths baked into older musicals still point at
// .../mineral/...; when the original no longer resolves we retry under boulder/
// (guarded by ExistsSync so any path that still exists is left untouched).
func boulderCompatPath(p string) string {
	if strings.Contains(p, "/mineral/") && !ExistsSync(p) {
		if alt := strings.Replace(p, "/mineral/", "/boulder/", 1); ExistsSync(alt) {
			return alt
		}
	}
	return p
}

// paramsFor returns the scatter tuning for the given dressing tab (category).
// Unknown categories fall back to the grasses defaults so old sculpts and
// any future tabs continue to work without special cases everywhere.
func (vr *TerrainEditor) paramsFor(category string) dressingParams {
	if p, ok := dressingDefaults[canonicalDressingCategory(category)]; ok {
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
	_, ok := dressingDefaults[canonicalDressingCategory(s)]
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
