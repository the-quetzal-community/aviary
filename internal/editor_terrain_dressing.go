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

// ModeDressing scatters instanced meshes (grasses, pebbles) across the
// terrain surface. Each committed stroke is a single musical.Sculpt — its
// Target/Radius are the record of where and how wide the patch is, its
// Amount is the density, and its Design is the scattered mesh. The scatter
// itself is NOT stored per-instance in the musical log: it's regenerated
// deterministically from the sculpt's (Author, Target, Radius), so every
// client that replays the same sculpt produces identical grass while the
// format stays tiny and fully backwards compatible.

type grassPatch struct {
	design musical.Design
	target Vector3.XYZ
	radius Float.X

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

const (
	grassScaleMin     Float.X = 0.7
	grassScaleMax     Float.X = 1.2
	grassPerArea              = 6.0  // instances per world unit² at density 1
	grassMaxInstances         = 3000 // safety cap per stroke
)

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
		patch.mm.SetInstanceTransform(k, vr.grassTransform(patch.bases[i], patch.yaws[i], patch.scales[i], asset.xform))
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
	asset, ok := vr.grassMeshFor(brush.Design)
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
		asset, ok := vr.grassMeshFor(brush.Design)
		if !ok {
			remaining = append(remaining, brush)
			continue
		}
		vr.buildGrassPatch(brush, asset)
	}
	vr.pendingGrass = remaining
}

// buildGrassPatch scatters count instances of mesh within the brush disc
// and registers a grassPatch so the placement can be re-projected later.
// All count positions are generated (RNG advances for the full set for
// cross-client determinism) but only those whose tile is revealed are
// added to the MultiMesh instancing.
func (vr *TerrainEditor) buildGrassPatch(brush musical.Sculpt, asset grassAsset) {
	count := grassCount(brush.Radius, brush.Amount)
	if count <= 0 {
		return
	}
	rng := &grassRNG{state: grassSeed(brush.Author, brush.Target, brush.Radius)}
	patch := &grassPatch{
		design: brush.Design,
		target: brush.Target,
		radius: brush.Radius,
		bases:  make([]Vector3.XYZ, count),
		yaws:   make([]Angle.Radians, count),
		scales: make([]Float.X, count),
	}
	// Sample the full set first (must consume RNG identically on all clients
	// regardless of which tiles are hidden at this moment).
	for i := 0; i < count; i++ {
		// Uniform disc sampling: sqrt keeps density even out to the rim.
		rad := float64(brush.Radius) * math.Sqrt(rng.float())
		theta := rng.float() * 2 * math.Pi
		base := Vector3.XYZ{
			X: brush.Target.X + Float.X(rad*math.Cos(theta)),
			Z: brush.Target.Z + Float.X(rad*math.Sin(theta)),
		}
		yaw := Angle.Radians(rng.float() * 2 * math.Pi)
		scale := grassScaleMin + Float.X(rng.float())*(grassScaleMax-grassScaleMin)
		patch.bases[i] = base
		patch.yaws[i] = yaw
		patch.scales[i] = scale
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
	vr.grassPatches = append(vr.grassPatches, patch)
	vr.populateGrassMM(patch)
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
	if _, ok := vr.grassMeshes[brush.Design]; !ok {
		// The mesh for this design hasn't arrived yet; we can't safely
		// rebuild the MultiMesh transforms. Skip — the erase will have
		// no visual effect until the asset is present (extremely rare
		// in normal play).
		return
	}
	center := brush.Target
	r2 := float64(brush.Radius) * float64(brush.Radius)

	for i := len(vr.grassPatches) - 1; i >= 0; i-- {
		p := vr.grassPatches[i]
		if p.design != brush.Design {
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

// grassTransform builds the instance transform for a blade: yaw about the
// world up, uniform scale, planted at the terrain height under base's X/Z.
// The optional source xform from the authoring .glb (and a fixed Z-up to
// Y-up correction for vegetation packs like yughues/grasses whose meshes
// author their vertical along +Z) are composed so the blades stand upright.
func (vr *TerrainEditor) grassTransform(base Vector3.XYZ, yaw Angle.Radians, scale Float.X, source Transform3D.BasisOrigin) Transform3D.BasisOrigin {
	yawB := Basis.FromEuler(Euler.Radians{Y: yaw}, Angle.OrderYXZ)
	scaledYaw := Basis.Scaled(yawB, Vector3.New(scale, scale, scale))

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

// grassMeshFor resolves (and caches) the Mesh (plus its source-scene
// transform) to instance for a dressing Design. Returns ok=false until
// the Design's .glb has been imported.
func (vr *TerrainEditor) grassMeshFor(design musical.Design) (grassAsset, bool) {
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
	// Promote each surface's material onto the shared Mesh — the MultiMesh has
	// no per-instance MeshInstance3D to carry overrides — and wrap it in the
	// wind-sway shader so the blades animate. The override (if any) wins over
	// the mesh's own material, matching how the source scene would have drawn.
	for i := 0; i < mesh.GetSurfaceCount(); i++ {
		src := mi.GetSurfaceOverrideMaterial(i)
		if src == Material.Nil {
			src = mesh.SurfaceGetMaterial(i)
		}
		if src == Material.Nil {
			continue
		}
		mesh.SurfaceSetMaterial(i, vr.grassWindMaterial(src))
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

// grassCount derives the instance count for a stroke from its disc area
// and density, capped so a huge brush at full density can't stall a frame.
func grassCount(radius, density Float.X) int {
	area := math.Pi * float64(radius) * float64(radius)
	c := int(area * float64(density) * grassPerArea)
	if c > grassMaxInstances {
		c = grassMaxInstances
	}
	return c
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
