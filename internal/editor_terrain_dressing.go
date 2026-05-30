package internal

import (
	"math"

	"graphics.gd/classdb/Material"
	"graphics.gd/classdb/Mesh"
	"graphics.gd/classdb/MeshInstance3D"
	"graphics.gd/classdb/MultiMesh"
	"graphics.gd/classdb/MultiMeshInstance3D"
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/Node3D"
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

	mm MultiMesh.Instance

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
	mm := MultiMesh.New()
	mm.SetTransformFormat(MultiMesh.Transform3d)
	mm.SetMesh(asset.mesh)
	mm.SetInstanceCount(count)
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
		mm.SetInstanceTransform(i, vr.grassTransform(base, yaw, scale, asset.xform))
	}
	node := MultiMeshInstance3D.New()
	node.SetMultimesh(mm)
	vr.AsNode().AddChild(node.AsNode())
	patch.mm = mm
	vr.grassPatches = append(vr.grassPatches, patch)
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
		for i := range patch.bases {
			// We don't have the asset here; re-use the xform that was baked
			// into the patch at creation time? For reproject we must look it
			// up or store the xform per-patch too. For simplicity we store
			// the xform on the patch when building (but to avoid schema
			// change we recompute from the cache — safe because the cache
			// is stable once loaded).
			if a, ok := vr.grassMeshes[patch.design]; ok {
				patch.mm.SetInstanceTransform(i, vr.grassTransform(patch.bases[i], patch.yaws[i], patch.scales[i], a.xform))
			}
		}
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

	// Fixed correction that maps a model's local +Z direction to world +Y.
	// The yughues grasses (and similar packs) export with blades along Z;
	// without this they lie flat ("sideways") when only Y-yaw is applied.
	zToY := Basis.FromEuler(Euler.Radians{X: Angle.Pi / 2}, Angle.OrderYXZ)

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

// grassMeshFor resolves (and caches) the Mesh to instance for a dressing
// Design. Returns ok=false until the Design's .glb has been imported.
func (vr *TerrainEditor) grassMeshFor(design musical.Design) (Mesh.Instance, bool) {
	if m, ok := vr.grassMeshes[design]; ok {
		return m, m != Mesh.Nil
	}
	sceneID, ok := vr.client.packed_scenes[design]
	if !ok {
		return Mesh.Nil, false
	}
	scene, ok := sceneID.Instance()
	if !ok {
		return Mesh.Nil, false
	}
	root := Object.To[Node3D.Instance](scene.Instantiate())
	mi, found := firstMeshInstance(root.AsNode())
	if !found {
		root.AsNode().QueueFree()
		return Mesh.Nil, false
	}
	mesh := Object.Leak(mi.Mesh())
	// Promote any surface override materials onto the shared Mesh so the
	// MultiMesh — which has no per-instance MeshInstance3D to carry
	// overrides — still renders the grass with its textures.
	for i := 0; i < mesh.GetSurfaceCount(); i++ {
		if ov := mi.GetSurfaceOverrideMaterial(i); ov != Material.Nil {
			mesh.SurfaceSetMaterial(i, ov)
		}
	}
	root.AsNode().QueueFree()
	vr.grassMeshes[design] = mesh
	return mesh, true
}

// firstMeshInstance returns the first MeshInstance3D found in a (possibly
// nested) imported scene tree — .glb roots usually wrap the mesh a couple
// of nodes deep.
func firstMeshInstance(n Node.Instance) (MeshInstance3D.Instance, bool) {
	if mi, ok := Object.As[MeshInstance3D.Instance](n); ok {
		return mi, true
	}
	for i := 0; i < n.GetChildCount(); i++ {
		if mi, ok := firstMeshInstance(n.GetChild(i)); ok {
			return mi, true
		}
	}
	return MeshInstance3D.Nil, false
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
