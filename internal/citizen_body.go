package internal

import (
	"errors"
	"fmt"
	"math"
	"strings"

	"graphics.gd/classdb/ArrayMesh"
	"graphics.gd/classdb/BaseMaterial3D"
	"graphics.gd/classdb/FileAccess"
	"graphics.gd/classdb/Material"
	"graphics.gd/classdb/Mesh"
	"graphics.gd/classdb/MeshInstance3D"
	"graphics.gd/classdb/StandardMaterial3D"
	"graphics.gd/classdb/Texture2D"
	"graphics.gd/variant/AABB"
	"graphics.gd/variant/Color"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Vector2"
	"graphics.gd/variant/Vector3"

	"the.quetzal.community/aviary/internal/citizen"
)

// CitizenBody bridges a pure-Go citizen.Citizen to a Godot MeshInstance3D.
// It owns a persistent ArrayMesh that gets re-surfaced in place on every
// slider tick, and a pool of per-slot CitizenDressing instances for
// equipped clothing — each kept fitted to the body via MakeHuman's
// .mhclo barycentric vertex anchors.
type CitizenBody struct {
	citizen *citizen.Citizen
	// mesh is the editor-owned MeshInstance3D rendering this citizen.
	// Stored as Instance (not ID) and unexported so graphics.gd's
	// keepalive walker visits it via CitizenEditor → CitizenBody → this
	// field and calls Object.Use to mark it alive each frame.
	mesh MeshInstance3D.Instance
	// arrayMesh is the Resource backing the displayed mesh. It's
	// updated in place across slider events via ClearSurfaces +
	// AddSurfaceFromArrays. Kept alive via the same keepalive path so
	// graphics.gd's GC doesn't drop our last Unreference, which would
	// take Godot's refcount to 0 and destroy the mesh.
	arrayMesh ArrayMesh.Instance
	// baseIndices is the unmodified triangle index buffer from the
	// parsed base.obj — never mutated. `indices` is derived from
	// it on dressing change by culling triangles whose all-three
	// corners are deep enough inside the covered region that we
	// trust they'll never need to render.
	baseIndices []int32
	// indices is what surface() actually uses. Equal to baseIndices
	// when nothing is equipped; otherwise a subset that drops the
	// deep-interior triangles, leaving a one-vertex fringe at every
	// clothing boundary so the shrink-based fallback can handle the
	// edge without a halo.
	indices   []int32
	vertexBuf []Vector3.XYZ
	// uvBuf is the per-vertex UV coords from base.obj, in the same
	// position-indexed space as vertexBuf. Static across the body's
	// lifetime (sliders only move positions, not UVs). Passed into
	// every surface so the eye material can sample its iris/sclera
	// texture; harmless on surfaces with no texture bound.
	uvBuf []Vector2.XY
	// neighbours is per-vertex adjacency derived from baseIndices,
	// built lazily on first need and cached. Used to erode the
	// covered set inward by one vertex spacing before deciding which
	// triangles to drop.
	neighbours [][]int32
	// dressings tracks the equipped clothing per slot. Each entry is a
	// pointer so refitDressing can mutate the buffer in place; the
	// graphics.gd keepalive walker follows pointer values in maps.
	dressings map[string]*CitizenDressing
	// shrinkDirs[i] is the unit-length direction to push body vert i
	// inward when clothing is equipped, derived from the averaged
	// .mhclo anchor offsets that reference this vert (negated, since
	// offsets point body→clothing and we want clothing→body). This is
	// more reliable than per-vertex body normals: MakeHuman's base.obj
	// has regions with inverted face winding (limbs vs. torso), so
	// computed body normals flip sign and a uniform "push along
	// normal" produces inconsistent results. Anchor offsets, by
	// contrast, are authored data and point consistently outward
	// across the whole mesh. Zero vec at verts no clothing anchors
	// into; those are skipped during shrink.
	shrinkDirs []citizen.Vec3
	// shrinkDirty triggers a re-shrink + re-surface on the next
	// CommitVisibility: AttachDressing flips it, CommitVisibility
	// runs the rebuild. Coalesces N startup-replay AttachDressings
	// into one rebuild per frame.
	shrinkDirty bool
	// eyeIndices is the second-surface index buffer for the eye
	// helper groups (helper-l-eye + helper-r-eye in MakeHuman's
	// base.obj). Shares vertexBuf with the body surface — eyes use
	// the same global vertex space as everything else, they just
	// get rendered as a separate surface so they can carry a
	// distinct (tintable) material.
	eyeIndices []int32
	// skinMaterial is the StandardMaterial3D applied to surface 0
	// (body). Its albedo is driven by the "pigment" slider.
	skinMaterial StandardMaterial3D.Instance
	// eyeMaterial is the StandardMaterial3D applied to surface 1
	// (eyes). Its albedo is driven by the "eyetint" slider.
	eyeMaterial StandardMaterial3D.Instance

	// rig is non-nil when this body is skinned to the mesh2motion rig:
	// the body then renders the GLB's straightened T-pose mesh (54k verts,
	// bound to skeleton via skin) instead of the raw base.obj mesh, with
	// morph .target deltas scattered onto the GLB verts through the rig's
	// canonical map. Nil keeps the legacy unskinned base.obj path.
	rig *citizen.CitizenRig
	// canonicalBase is the unmodified base.obj vertex array (parallel to
	// the Citizen's private base), used to recover the per-canonical-vertex
	// morph displacement (Recompute()[c] - canonicalBase[c]) the rig
	// scatters onto the GLB mesh.
	canonicalBase []citizen.Vec3
	// glbVerts/glbNormals/glbUVs are the GLB-space (≈1.7 m) render buffers,
	// one entry per GLB vertex. glbVerts is rewritten every rebuild from the
	// bind pose + scattered morph; the others are static (bind normals/UVs).
	glbVerts   []Vector3.XYZ
	glbNormals []Vector3.XYZ
	glbUVs     []Vector2.XY
	// bones/weights are the flat 4-influences-per-vertex skinning arrays
	// (ArrayBones int32 / ArrayWeights float32), built once from the rig and
	// reused across re-surfaces — morphs move positions, never bindings.
	bones   []int32
	weights []float32
	// bodyIdxGLB/eyeIdxGLB partition the GLB triangle list into the body
	// surface and the eye surface (a triangle is an eye triangle when all
	// three of its GLB verts map to base.obj eye-group canonical verts), so
	// the two surfaces can carry distinct (pigment / eyetint) materials.
	bodyIdxGLB []int32
	eyeIdxGLB  []int32
	// bodyIdxCulled is what surfaceRigged actually draws for the body: equal
	// to bodyIdxGLB with nothing equipped, otherwise the subset with the
	// deep-under-clothing triangles dropped (boundary fringe kept + shrunk,
	// same hybrid as the legacy path).
	bodyIdxCulled []int32
	// riggedDressings is the equipped clothing in the rigged path. Unlike the
	// legacy CitizenDressing (its own MeshInstance3D), these are merged as
	// extra surfaces on the body's ArrayMesh so they share the body MI's
	// imported Skin + skeleton — each clothing vertex skinned by the
	// barycentric blend of its three .mhclo body anchors' bone weights, so it
	// follows the animation for free.
	riggedDressings map[string]*riggedDressing
	// riggedSurfaceMats records the override material per surface index in
	// the order surfaceRigged adds them (body, eyes?, then each clothing
	// item), so applySurfaceMaterials can rebind them after every re-surface.
	riggedSurfaceMats []Material.Instance
}

// bodyShrinkAmount is how far we push anchored body verts inward
// along their shrinkDir. Typical .mhclo anchor offsets are 0.05–0.15
// in body-space units (where the hm08 mesh spans ±8), so 0.05 keeps
// the body strictly inside every reasonable clothing surface without
// being visually noticeable in regions where no clothing is equipped
// — those verts aren't shrunk at all.
const bodyShrinkAmount = 0.05

// CitizenDressing is one equipped clothing item — its own
// MeshInstance3D in the scene tree, an ArrayMesh resource we re-surface
// when the body deforms, and the MHClo fit data that maps each clothing
// vertex onto three body vertices. material is a per-item
// StandardMaterial3D applied as a surface override so it survives the
// ArrayMesh.ClearSurfaces/AddSurfaceFromArrays cycle in refit().
type CitizenDressing struct {
	mi        MeshInstance3D.Instance
	arrayMesh ArrayMesh.Instance
	material  StandardMaterial3D.Instance
	mhclo     *citizen.MHClo
	indices   []int32
	buf       []Vector3.XYZ
	// uvs is nil when the source .obj had no `vt` references; otherwise
	// one UV per vertex in buf. Static across body deformations — refit
	// only moves positions.
	uvs []Vector2.XY
	// restNormals is the per-vertex outward normal computed from the
	// clothing's rest (pre-fit) geometry. We use these in the body
	// shrink direction calculation: clothing surface normals point
	// outward, so negating them gives a reliable inward direction
	// even for 1-field .mhclo anchors that have no offset. Static —
	// clothing topology doesn't change with body deformation.
	restNormals []citizen.Vec3
	// fitBuf is the per-refit scratch slice handed back to MHClo.Fit
	// so each rebuild reuses the existing backing array instead of
	// allocating a fresh []Vec3 every sculpt frame.
	fitBuf []citizen.Vec3
}

// AttachCitizenBody creates a fresh ArrayMesh from the parsed base mesh,
// sets it on the supplied MeshInstance3D, and returns a body that drives
// it from the runtime delta application.
func AttachCitizenBody(mi MeshInstance3D.Instance, base *citizen.BaseMesh, targets []*citizen.Target) (CitizenBody, error) {
	if mi == MeshInstance3D.Nil {
		return CitizenBody{}, errors.New("citizen: nil MeshInstance3D")
	}
	if base == nil || len(base.Verts) == 0 {
		return CitizenBody{}, errors.New("citizen: empty base mesh")
	}
	vbuf := make([]Vector3.XYZ, len(base.Verts))
	for i, v := range base.Verts {
		vbuf[i] = Vector3.XYZ{X: Float.X(v.X), Y: Float.X(v.Y), Z: Float.X(v.Z)}
	}
	var uvbuf []Vector2.XY
	if len(base.UVs) == len(base.Verts) {
		uvbuf = make([]Vector2.XY, len(base.UVs))
		for i, uv := range base.UVs {
			uvbuf[i] = Vector2.XY{X: Float.X(uv.U), Y: Float.X(uv.V)}
		}
	}
	baseCopy := make([]citizen.Vec3, len(base.Verts))
	copy(baseCopy, base.Verts)
	c := citizen.New(baseCopy)
	c.AddTargets(targets)
	body := CitizenBody{
		citizen:      c,
		mesh:         mi,
		arrayMesh:    ArrayMesh.New(),
		baseIndices:  base.Indices,
		indices:      base.Indices,
		vertexBuf:    vbuf,
		uvBuf:        uvbuf,
		dressings:    make(map[string]*CitizenDressing),
		eyeIndices:   base.EyeIndices,
		skinMaterial: StandardMaterial3D.New(),
		eyeMaterial:  StandardMaterial3D.New(),
	}
	body.skinMaterial.AsBaseMaterial3D().SetAlbedoColor(pigmentColor(defaultPigment))
	body.eyeMaterial.AsBaseMaterial3D().SetAlbedoColor(eyeTintColor(defaultEyeTint))
	// Iris/sclera texture (CC0, originally MakeHuman's brown_eye.png,
	// copied into the library by import_makehuman.sh). Combined with
	// the eyetint slider's albedo colour via StandardMaterial3D's
	// implicit modulation, so the slider tints the texture rather
	// than replacing it.
	if eyeTex := LoadSync[Texture2D.Instance]("res://library/makehuman/citizen_eye.png"); eyeTex != Texture2D.Nil {
		body.eyeMaterial.AsBaseMaterial3D().SetAlbedoTexture(eyeTex)
	}
	// The hm08 eye spheres come from the helper-l-eye / helper-r-eye
	// groups, which appear to have opposite winding from the body
	// group — Godot's default backface cull then hides them.
	// Disabling cull on the eye material renders both sides of every
	// triangle, which is harmless for closed spheres (we never see
	// their interior anyway) and robust against per-group winding
	// inconsistencies in the source mesh.
	body.eyeMaterial.AsBaseMaterial3D().SetCullMode(BaseMaterial3D.CullDisabled)
	body.surface()
	body.applySurfaceMaterials()
	mi.SetMesh(body.arrayMesh.AsMesh())
	return body, nil
}

// AttachRiggedCitizenBody builds a skinned citizen body from the mesh2motion
// rig. mi is the animated GLB's own skinned MeshInstance3D — we keep its Skin
// + skeleton binding and replace only its mesh resource with our procedural,
// morph-driven ArrayMesh (the straightened T-pose bind in GLB ≈1.7 m space,
// 54k verts). The skeleton + AnimationPlayer ride along from the same scene,
// so the imported Quaternius clips drive this mesh directly.
func AttachRiggedCitizenBody(mi MeshInstance3D.Instance, base *citizen.BaseMesh, targets []*citizen.Target, rig *citizen.CitizenRig) (CitizenBody, error) {
	if mi == MeshInstance3D.Nil {
		return CitizenBody{}, errors.New("citizen: nil MeshInstance3D")
	}
	if base == nil || len(base.Verts) == 0 {
		return CitizenBody{}, errors.New("citizen: empty base mesh")
	}
	if rig == nil || len(rig.Bind) == 0 {
		return CitizenBody{}, errors.New("citizen: empty rig")
	}
	baseCopy := make([]citizen.Vec3, len(base.Verts))
	copy(baseCopy, base.Verts)
	c := citizen.New(baseCopy)
	c.AddTargets(targets)

	n := len(rig.Bind)
	body := CitizenBody{
		citizen:         c,
		mesh:            mi,
		arrayMesh:       ArrayMesh.New(),
		dressings:       make(map[string]*CitizenDressing),
		skinMaterial:    StandardMaterial3D.New(),
		eyeMaterial:     StandardMaterial3D.New(),
		rig:             rig,
		canonicalBase:   baseCopy,
		baseIndices:     base.Indices, // canonical adjacency for coverage erosion
		glbVerts:        make([]Vector3.XYZ, n),
		glbNormals:      make([]Vector3.XYZ, n),
		riggedDressings: make(map[string]*riggedDressing),
	}
	for i, nrm := range rig.Normals {
		body.glbNormals[i] = Vector3.XYZ{X: Float.X(nrm.X), Y: Float.X(nrm.Y), Z: Float.X(nrm.Z)}
	}
	if len(rig.UVs) == n {
		body.glbUVs = make([]Vector2.XY, n)
		for i, uv := range rig.UVs {
			body.glbUVs[i] = Vector2.XY{X: Float.X(uv.U), Y: Float.X(uv.V)}
		}
	}
	body.bones = make([]int32, 4*n)
	body.weights = make([]float32, 4*n)
	for i := 0; i < n; i++ {
		for k := 0; k < 4; k++ {
			body.bones[i*4+k] = int32(rig.Joints[i][k])
			body.weights[i*4+k] = rig.Weights[i][k]
		}
	}
	body.splitRiggedSurfaces(base)

	body.skinMaterial.AsBaseMaterial3D().SetAlbedoColor(pigmentColor(defaultPigment))
	body.eyeMaterial.AsBaseMaterial3D().SetAlbedoColor(eyeTintColor(defaultEyeTint))
	body.eyeMaterial.AsBaseMaterial3D().SetCullMode(BaseMaterial3D.CullDisabled)
	if eyeTex := LoadSync[Texture2D.Instance]("res://library/makehuman/citizen_eye.png"); eyeTex != Texture2D.Nil {
		body.eyeMaterial.AsBaseMaterial3D().SetAlbedoTexture(eyeTex)
	}

	body.writeRiggedVerts(c.Recompute())
	body.surface()
	body.applySurfaceMaterials()
	// Generous custom AABB (GLB ≈1.7 m space) so the renderer skips the
	// per-frame skinned-AABB recompute (and its "bs > sbs" warning), and so
	// big clips — backflip, flying, jumps — aren't frustum-culled at the
	// body's rest extents.
	body.arrayMesh.SetCustomAabb(AABB.PositionSize{
		Position: Vector3.New(-1.5, -1, -1.5),
		Size:     Vector3.New(3, 4, 3),
	})
	// Swap in our mesh; the MeshInstance3D keeps its imported Skin + skeleton
	// path (both live on the node, not the Mesh resource), and our ArrayBones
	// index the same skin binds the GLB's JOINTS_0 did.
	mi.SetMesh(body.arrayMesh.AsMesh())
	return body, nil
}

// splitRiggedSurfaces partitions the GLB triangle list into the body surface
// and the eye surface: a triangle is an eye triangle when all three of its
// GLB verts map to base.obj eye-group canonical verts. Each gets its own
// surface so the pigment and eyetint materials stay independent.
func (b *CitizenBody) splitRiggedSurfaces(base *citizen.BaseMesh) {
	eyeCanon := make(map[int32]bool, len(base.EyeIndices))
	for _, idx := range base.EyeIndices {
		eyeCanon[idx] = true
	}
	rig := b.rig
	isEye := make([]bool, len(rig.Bind))
	for i := range rig.Bind {
		if c := rig.Canonical[i]; c >= 0 && eyeCanon[c] {
			isEye[i] = true
		}
	}
	bodyIdx := make([]int32, 0, len(rig.Indices))
	var eyeIdx []int32
	for t := 0; t+2 < len(rig.Indices); t += 3 {
		a, bb, cc := rig.Indices[t], rig.Indices[t+1], rig.Indices[t+2]
		if int(a) < len(isEye) && int(bb) < len(isEye) && int(cc) < len(isEye) &&
			isEye[a] && isEye[bb] && isEye[cc] {
			eyeIdx = append(eyeIdx, a, bb, cc)
		} else {
			bodyIdx = append(bodyIdx, a, bb, cc)
		}
	}
	b.bodyIdxGLB = bodyIdx
	b.eyeIdxGLB = eyeIdx
	b.bodyIdxCulled = bodyIdx // nothing equipped yet → draw the whole body
}

// writeRiggedVerts rewrites the GLB-space render positions from the bind pose
// plus the active morph: for each GLB vert, the displacement of its canonical
// base.obj vertex (Recompute − base) is rotated into the straightened T-pose
// (rig.Disp, which folds in the base→GLB scale) and added to the bind. With
// no sliders active every displacement is zero, so the mesh is exactly the
// GLB bind.
func (b *CitizenBody) writeRiggedVerts(body []citizen.Vec3) {
	rig := b.rig
	hasShrink := len(b.shrinkDirs) > 0
	for i := range rig.Bind {
		bind := rig.Bind[i]
		c := rig.Canonical[i]
		var x, y, z float32 = bind.X, bind.Y, bind.Z
		if c >= 0 && int(c) < len(body) && int(c) < len(b.canonicalBase) {
			cb := b.canonicalBase[c]
			d := citizen.Vec3{X: body[c].X - cb.X, Y: body[c].Y - cb.Y, Z: body[c].Z - cb.Z}
			// Shrink: a body vert covered by clothing is pushed inward (still in
			// base.obj space) so clothing renders in front; fold it into the
			// morph delta so a single Disp rotation maps both to GLB space.
			if hasShrink && c >= 0 && int(c) < len(b.shrinkDirs) {
				if sd := b.shrinkDirs[c]; sd.X != 0 || sd.Y != 0 || sd.Z != 0 {
					d.X += sd.X * bodyShrinkAmount
					d.Y += sd.Y * bodyShrinkAmount
					d.Z += sd.Z * bodyShrinkAmount
				}
			}
			dd := rig.Disp[i].Mul3Vec(d)
			x, y, z = bind.X+dd.X, bind.Y+dd.Y, bind.Z+dd.Z
		}
		b.glbVerts[i] = Vector3.XYZ{X: Float.X(x), Y: Float.X(y), Z: Float.X(z)}
	}
}

// surfaceRigged builds the skinned surfaces on the body ArrayMesh: surface 0
// the body, surface 1 the eyes (if any), then one surface per equipped
// clothing item. The body + eyes share the GLB vertex/normal/UV/bone/weight
// buffers (differing only in index); each clothing item carries its own.
// All surfaces are skinned by the single imported Skin on the body MI, so
// clothing rides the animation alongside the body. The per-surface override
// material is recorded into riggedSurfaceMats in add order.
func (b *CitizenBody) surfaceRigged() {
	b.riggedSurfaceMats = b.riggedSurfaceMats[:0]
	add := func(verts, normals []Vector3.XYZ, uvs []Vector2.XY, bones []int32, weights []float32, indices []int32, mat Material.Instance) {
		var arrays [Mesh.ArrayMax]any
		arrays[Mesh.ArrayVertex] = verts
		if len(normals) == len(verts) {
			arrays[Mesh.ArrayNormal] = normals
		}
		if len(uvs) == len(verts) {
			arrays[Mesh.ArrayTexUv] = uvs
		}
		arrays[Mesh.ArrayBones] = bones
		arrays[Mesh.ArrayWeights] = weights
		arrays[Mesh.ArrayIndex] = indices
		b.arrayMesh.AddSurfaceFromArrays(Mesh.PrimitiveTriangles, arrays[:])
		b.riggedSurfaceMats = append(b.riggedSurfaceMats, mat)
	}
	add(b.glbVerts, b.glbNormals, b.glbUVs, b.bones, b.weights, b.bodyIdxCulled, b.skinMaterial.AsMaterial())
	if len(b.eyeIdxGLB) > 0 {
		add(b.glbVerts, b.glbNormals, b.glbUVs, b.bones, b.weights, b.eyeIdxGLB, b.eyeMaterial.AsMaterial())
	}
	for _, slot := range sortedDressingSlots(b.riggedDressings) {
		d := b.riggedDressings[slot]
		var mat Material.Instance
		if d.material != StandardMaterial3D.Nil {
			mat = d.material.AsMaterial()
		}
		add(d.verts, d.normals, d.uvs, d.bones, d.weights, d.indices, mat)
	}
}

// hasEyeSurface reports whether surface 1 (eyes) exists, across both the
// rigged (eyeIdxGLB) and legacy (eyeIndices) paths.
func (b *CitizenBody) hasEyeSurface() bool {
	if b.rig != nil {
		return len(b.eyeIdxGLB) > 0
	}
	return len(b.eyeIndices) > 0
}

// defaultPigment / defaultEyeTint pick the neutral palette index for
// citizens with no explicit slider value yet — a fair-skin and a
// hazel-eye starting point.
const (
	defaultPigment = 0.25
	defaultEyeTint = 0.65
)

// Citizen exposes the underlying pure-Go state for direct querying
// (current weights, target catalogue, etc.).
func (b *CitizenBody) Citizen() *citizen.Citizen { return b.citizen }

// SetWeight updates a slider and rebuilds the displayed mesh if the value
// actually changed. Pass 0 to clear.
func (b *CitizenBody) SetWeight(name string, weight float32) {
	if !b.citizen.SetWeight(name, weight) {
		return
	}
	b.rebuild()
}

func (b *CitizenBody) rebuild() {
	if b.arrayMesh == ArrayMesh.Nil {
		return
	}
	body := b.citizen.Recompute()
	if b.rig != nil {
		b.writeRiggedVerts(body)
		// Refit clothing against the freshly-morphed T-pose body BEFORE
		// surfaceRigged reads d.verts to build the clothing surfaces.
		for _, d := range b.riggedDressings {
			d.refit(b)
		}
	} else {
		b.writeShrunkVertexBuf(body)
	}
	b.arrayMesh.ClearSurfaces()
	b.surface()
	b.applySurfaceMaterials()
	for _, d := range b.dressings {
		d.refit(body)
	}
}

// writeShrunkVertexBuf copies the pure body positions into vertexBuf,
// pushing any vert with a non-zero shrinkDir inward by
// bodyShrinkAmount along that direction. Clothing items still fit
// against the unmodified body, so the gap between body surface and
// clothing surface in anchored regions becomes (authored offset +
// bodyShrinkAmount) along the same direction — strictly positive
// even on items with small authored offsets, so the depth test
// always renders clothing in front of body.
func (b *CitizenBody) writeShrunkVertexBuf(body []citizen.Vec3) {
	if len(b.shrinkDirs) == 0 {
		for i, v := range body {
			b.vertexBuf[i] = Vector3.XYZ{
				X: Float.X(v.X), Y: Float.X(v.Y), Z: Float.X(v.Z),
			}
		}
		return
	}
	for i, v := range body {
		x, y, z := v.X, v.Y, v.Z
		if i < len(b.shrinkDirs) {
			d := b.shrinkDirs[i]
			if d.X != 0 || d.Y != 0 || d.Z != 0 {
				x += d.X * bodyShrinkAmount
				y += d.Y * bodyShrinkAmount
				z += d.Z * bodyShrinkAmount
			}
		}
		b.vertexBuf[i] = Vector3.XYZ{X: Float.X(x), Y: Float.X(y), Z: Float.X(z)}
	}
}

// clothRestNormals computes smooth per-vertex outward normals from a
// clothing item's rest-pose geometry (the .obj verts and the
// triangulated index buffer we parsed). Each triangle contributes its
// (b-a) × (c-a) face normal to its three vertices; we then renormalise.
// CCW winding from outside ⇒ outward normal — the citizen OBJ parser
// flips winding so this assumption holds for everything that goes
// through it. Computed once per item at load and reused; clothing
// topology doesn't change with body deformation, so the rest-pose
// direction stays a fair proxy for the deformed-pose direction even
// after refitting against a slider-modified body.
func clothRestNormals(verts []citizen.Vec3, indices []int32) []citizen.Vec3 {
	n := make([]citizen.Vec3, len(verts))
	for i := 0; i+2 < len(indices); i += 3 {
		ia, ib, ic := indices[i], indices[i+1], indices[i+2]
		if int(ia) >= len(verts) || int(ib) >= len(verts) || int(ic) >= len(verts) {
			continue
		}
		a, b, c := verts[ia], verts[ib], verts[ic]
		ex, ey, ez := b.X-a.X, b.Y-a.Y, b.Z-a.Z
		fx, fy, fz := c.X-a.X, c.Y-a.Y, c.Z-a.Z
		nx := ey*fz - ez*fy
		ny := ez*fx - ex*fz
		nz := ex*fy - ey*fx
		n[ia].X += nx
		n[ia].Y += ny
		n[ia].Z += nz
		n[ib].X += nx
		n[ib].Y += ny
		n[ib].Z += nz
		n[ic].X += nx
		n[ic].Y += ny
		n[ic].Z += nz
	}
	for i, v := range n {
		l := float32(math.Sqrt(float64(v.X*v.X + v.Y*v.Y + v.Z*v.Z)))
		if l > 0 {
			n[i] = citizen.Vec3{X: v.X / l, Y: v.Y / l, Z: v.Z / l}
		}
	}
	// Auto-detect normal orientation per item. Different clothing
	// authors export with different winding conventions; my fan-
	// triangulation flip assumes CW-from-outside (true for MakeHuman
	// base.obj and most MakeClothes exports), but some items end up
	// with CCW-from-outside originally and consequently inverted
	// normals after the flip. To detect, compute the mesh centroid
	// and check at each vert whether (vert - centroid) · normal is
	// positive: a correctly-outward normal points away from the
	// centroid, so the majority sign tells us the convention. If
	// negative dominates, flip every normal.
	if len(verts) > 0 {
		var cx, cy, cz float32
		for _, v := range verts {
			cx += v.X
			cy += v.Y
			cz += v.Z
		}
		inv := float32(1) / float32(len(verts))
		cx *= inv
		cy *= inv
		cz *= inv
		var pos, neg int
		for i, v := range verts {
			nrm := n[i]
			if nrm.X == 0 && nrm.Y == 0 && nrm.Z == 0 {
				continue
			}
			if (v.X-cx)*nrm.X+(v.Y-cy)*nrm.Y+(v.Z-cz)*nrm.Z > 0 {
				pos++
			} else {
				neg++
			}
		}
		if neg > pos {
			for i := range n {
				n[i] = citizen.Vec3{X: -n[i].X, Y: -n[i].Y, Z: -n[i].Z}
			}
		}
	}
	return n
}

// surface (re)constructs the arrayMesh's surfaces from the current
// vertexBuf: surface 0 is the body (using b.indices, possibly culled
// under clothing); surface 1 is the eyes if base.obj contained any.
// Caller should ClearSurfaces() before calling this on a mesh that
// already has surfaces.
func (b *CitizenBody) surface() {
	if b.rig != nil {
		b.surfaceRigged()
		return
	}
	// Both surfaces share the vertex (and UV) buffer and differ only in their
	// index buffer — surface 0 the body, surface 1 the eyes — so build them the
	// same way to keep the UV-presence guard in one place.
	addSurface := func(indices any) {
		var arrays [Mesh.ArrayMax]any
		arrays[Mesh.ArrayVertex] = b.vertexBuf
		arrays[Mesh.ArrayIndex] = indices
		if len(b.uvBuf) == len(b.vertexBuf) {
			arrays[Mesh.ArrayTexUv] = b.uvBuf
		}
		b.arrayMesh.AddSurfaceFromArrays(Mesh.PrimitiveTriangles, arrays[:])
	}
	addSurface(b.indices)
	if len(b.eyeIndices) > 0 {
		addSurface(b.eyeIndices)
	}
}

// applySurfaceMaterials rebinds the per-surface override materials
// on the MeshInstance3D. Called after every surface(): Godot stores
// overrides on the MI not the Mesh, but only honours an override
// whose surface index existed when set — so binding after
// AddSurfaceFromArrays is the safe path.
func (b *CitizenBody) applySurfaceMaterials() {
	if b.rig != nil {
		// Rigged: one override per surface in surfaceRigged's add order
		// (body, eyes?, clothing…).
		for i, mat := range b.riggedSurfaceMats {
			if mat != Material.Nil {
				b.mesh.SetSurfaceOverrideMaterial(i, mat)
			}
		}
		return
	}
	if b.skinMaterial != StandardMaterial3D.Nil {
		b.mesh.SetSurfaceOverrideMaterial(0, b.skinMaterial.AsMaterial())
	}
	if b.hasEyeSurface() && b.eyeMaterial != StandardMaterial3D.Nil {
		b.mesh.SetSurfaceOverrideMaterial(1, b.eyeMaterial.AsMaterial())
	}
}

// SetPigment maps a 0..1 slider value through the skin-tone palette
// and sets the body material's albedo. Stored on the citizen's
// weights map too so it serialises alongside shape sliders.
func (b *CitizenBody) SetPigment(value float32) {
	b.citizen.SetWeight("pigment", value)
	if b.skinMaterial == StandardMaterial3D.Nil {
		return
	}
	b.skinMaterial.AsBaseMaterial3D().SetAlbedoColor(pigmentColor(value))
}

// SetEyeTint maps a 0..1 slider value through the eye-colour palette
// and sets the eye material's albedo.
func (b *CitizenBody) SetEyeTint(value float32) {
	b.citizen.SetWeight("eyetint", value)
	if b.eyeMaterial == StandardMaterial3D.Nil {
		return
	}
	b.eyeMaterial.AsBaseMaterial3D().SetAlbedoColor(eyeTintColor(value))
}

// pigmentColor interpolates between five hand-picked skin tones,
// from pale-peach (0) to deep-brown (1). Two-segment lerp keeps the
// extremes from being washed-out 1D averages.
func pigmentColor(t float32) Color.RGBA {
	if t < 0 {
		t = 0
	} else if t > 1 {
		t = 1
	}
	stops := [...]Color.RGBA{
		{R: 1.00, G: 0.86, B: 0.76, A: 1},
		{R: 0.93, G: 0.76, B: 0.62, A: 1},
		{R: 0.81, G: 0.62, B: 0.45, A: 1},
		{R: 0.55, G: 0.37, B: 0.24, A: 1},
		{R: 0.27, G: 0.17, B: 0.10, A: 1},
	}
	return lerpPalette(stops[:], t)
}

// eyeTintColor interpolates between five hand-picked iris colours,
// from blue (0) through green/hazel to deep brown (1).
func eyeTintColor(t float32) Color.RGBA {
	if t < 0 {
		t = 0
	} else if t > 1 {
		t = 1
	}
	stops := [...]Color.RGBA{
		{R: 0.20, G: 0.40, B: 0.78, A: 1},
		{R: 0.25, G: 0.55, B: 0.55, A: 1},
		{R: 0.40, G: 0.55, B: 0.30, A: 1},
		{R: 0.46, G: 0.33, B: 0.18, A: 1},
		{R: 0.22, G: 0.13, B: 0.07, A: 1},
	}
	return lerpPalette(stops[:], t)
}

func lerpPalette(stops []Color.RGBA, t float32) Color.RGBA {
	if len(stops) == 0 {
		return Color.RGBA{A: 1}
	}
	if len(stops) == 1 || t <= 0 {
		return stops[0]
	}
	if t >= 1 {
		return stops[len(stops)-1]
	}
	scaled := t * float32(len(stops)-1)
	i := int(scaled)
	if i >= len(stops)-1 {
		return stops[len(stops)-1]
	}
	u := scaled - float32(i)
	a, b := stops[i], stops[i+1]
	return Color.RGBA{
		R: Float.X(float32(a.R) + (float32(b.R)-float32(a.R))*u),
		G: Float.X(float32(a.G) + (float32(b.G)-float32(a.G))*u),
		B: Float.X(float32(a.B) + (float32(b.B)-float32(a.B))*u),
		A: 1,
	}
}

// AttachDressing equips or replaces clothing in a slot. The design is a
// res:// path to a .obj file (the design explorer points at this via
// the preview .obj.png convention). The sibling .mhclo file is loaded
// to drive runtime fitting against body deformations. Pass design ""
// to unequip the slot.
func (b *CitizenBody) AttachDressing(slot, design string) {
	if !b.citizen.SetDressing(slot, design) {
		return
	}
	if b.rig != nil {
		delete(b.riggedDressings, slot)
		if design != "" {
			d, err := newRiggedDressing(design, b)
			if err != nil {
				fmt.Println("citizen: rigged dressing load failed:", err)
			} else {
				b.riggedDressings[slot] = d
			}
		}
		// Defer the body-cull + re-surface to CommitVisibility so a burst of
		// AttachDressings (history replay) collapses to one coverage pass.
		b.shrinkDirty = true
		return
	}
	if existing, ok := b.dressings[slot]; ok {
		existing.mi.AsNode().QueueFree()
		delete(b.dressings, slot)
	}
	if design != "" {
		d, err := loadCitizenDressing(design)
		if err != nil {
			fmt.Println("citizen: dressing load failed:", err)
			return
		}
		d.refit(b.citizen.Recompute())
		b.mesh.AsNode().AddChild(d.mi.AsNode())
		b.dressings[slot] = d
	}
	b.shrinkDirty = true
}

// CommitVisibility re-runs the dressing-derived state if any
// AttachDressing happened since the last commit: shrink directions
// for the boundary, culled index buffer for the deep interior, and
// a full rebuild to apply both. Called once per editor frame so a
// burst of AttachDressings (e.g. replaying scene history at startup)
// collapses to one update.
func (b *CitizenBody) CommitVisibility() {
	if !b.shrinkDirty {
		return
	}
	b.shrinkDirty = false
	if b.rig != nil {
		b.updateRiggedCoverage()
	} else {
		b.updateCoverageAndShrink()
	}
	b.rebuild()
}

// updateRiggedCoverage computes which body verts are hidden under the equipped
// clothing and culls the deep-interior GLB body triangles, leaving (and
// shrinking) a one-vertex fringe at every clothing boundary — the same hybrid
// as the legacy path, but reusing the canonical-space coverage helpers and
// mapping the result onto the GLB triangle list via rig.Canonical.
func (b *CitizenBody) updateRiggedCoverage() {
	if b.rig == nil {
		return
	}
	if len(b.riggedDressings) == 0 {
		b.bodyIdxCulled = b.bodyIdxGLB
		b.shrinkDirs = nil
		return
	}
	body := b.citizen.Recompute() // canonical, base.obj space
	covered := make([]bool, len(body))
	dirs := make([]citizen.Vec3, len(body))
	var clothBuf []citizen.Vec3
	for _, d := range b.riggedDressings {
		if d.mhclo == nil || len(d.restNormals) == 0 {
			continue
		}
		clothBuf = d.mhclo.Fit(body, clothBuf)
		markCoveredWithDirs(covered, dirs, body, clothBuf, d.restNormals)
	}
	for i, v := range dirs {
		l := float32(math.Sqrt(float64(v.X*v.X + v.Y*v.Y + v.Z*v.Z)))
		if l > 0 {
			dirs[i] = citizen.Vec3{X: v.X / l, Y: v.Y / l, Z: v.Z / l}
		}
	}
	b.shrinkDirs = dirs
	eroded := b.erodeCovered(covered)
	rig := b.rig
	deep := func(glbVert int32) bool {
		c := rig.Canonical[glbVert]
		return c >= 0 && int(c) < len(eroded) && eroded[c]
	}
	out := make([]int32, 0, len(b.bodyIdxGLB))
	for t := 0; t+2 < len(b.bodyIdxGLB); t += 3 {
		a, b1, c := b.bodyIdxGLB[t], b.bodyIdxGLB[t+1], b.bodyIdxGLB[t+2]
		if deep(a) && deep(b1) && deep(c) {
			continue
		}
		out = append(out, a, b1, c)
	}
	// Backstop only against a coverage/mapping bug that would erase essentially
	// the WHOLE body — a real outfit always leaves face/hands/feet, and the
	// eyes are a separate always-on surface, so legitimate full-body coverage
	// is fine. ~2% floor.
	if len(out) < len(b.bodyIdxGLB)/50 {
		coveredN := 0
		for _, on := range covered {
			if on {
				coveredN++
			}
		}
		fmt.Printf("citizen: clothing cull would erase nearly the whole body (covered %d/%d canon) — keeping full body\n",
			coveredN, len(covered))
		b.bodyIdxCulled = b.bodyIdxGLB
		b.shrinkDirs = nil
		return
	}
	b.bodyIdxCulled = out
}

// updateCoverageAndShrink walks every equipped item's fitted clothing
// and, for each body vert in any item's AABB, finds all clothing
// verts within a per-item threshold. Each hit marks the body vert
// covered AND accumulates the negated clothing surface normal at the
// hit clothing vert into shrinkDirs[bodyVert]. Result: every body
// vert covered by proximity also gets a non-zero shrink direction —
// including covered verts that no .mhclo anchor weights into (the
// heel-of-shoe case where 1-field anchors are sparse).
//
// We then erode the covered set by one vertex spacing and use the
// eroded interior to cull the body's index buffer: triangles whose
// all-three corners are deep-interior disappear, leaving a one-
// vertex fringe at the clothing boundary that the shrink moves
// inward.
func (b *CitizenBody) updateCoverageAndShrink() {
	dirs := make([]citizen.Vec3, len(b.vertexBuf))
	if len(b.dressings) == 0 {
		b.shrinkDirs = dirs
		b.indices = b.baseIndices
		return
	}
	body := b.citizen.Recompute()
	covered := make([]bool, len(body))
	var clothBuf []citizen.Vec3
	for _, d := range b.dressings {
		if d.mhclo == nil || len(d.restNormals) == 0 {
			continue
		}
		clothBuf = d.mhclo.Fit(body, clothBuf)
		markCoveredWithDirs(covered, dirs, body, clothBuf, d.restNormals)
	}
	for i, v := range dirs {
		l := float32(math.Sqrt(float64(v.X*v.X + v.Y*v.Y + v.Z*v.Z)))
		if l > 0 {
			dirs[i] = citizen.Vec3{X: v.X / l, Y: v.Y / l, Z: v.Z / l}
		}
	}
	b.shrinkDirs = dirs
	eroded := b.erodeCovered(covered)
	out := make([]int32, 0, len(b.baseIndices))
	for i := 0; i+2 < len(b.baseIndices); i += 3 {
		a, b1, c := b.baseIndices[i], b.baseIndices[i+1], b.baseIndices[i+2]
		if int(a) < len(eroded) && int(b1) < len(eroded) && int(c) < len(eroded) &&
			eroded[a] && eroded[b1] && eroded[c] {
			continue
		}
		out = append(out, a, b1, c)
	}
	b.indices = out
}

// erodeCovered returns covered with all boundary verts unset (any
// hidden vert with a non-hidden neighbour). Single pass — combined
// with the all-three-hidden triangle rule, that leaves a single body
// triangle fringe past the culled interior for the shrink to deal
// with.
func (b *CitizenBody) erodeCovered(covered []bool) []bool {
	neigh := b.vertexNeighbours()
	if neigh == nil {
		return covered
	}
	out := make([]bool, len(covered))
	for v, h := range covered {
		if !h {
			continue
		}
		interior := true
		for _, n := range neigh[v] {
			if !covered[n] {
				interior = false
				break
			}
		}
		out[v] = interior
	}
	return out
}

// vertexNeighbours builds and caches the per-vertex adjacency list.
// Each entry lists every other body vertex that shares a triangle
// with it, deduplicated. Derived from baseIndices (not the culled
// indices) so adjacency reflects the body's actual topology.
func (b *CitizenBody) vertexNeighbours() [][]int32 {
	if b.neighbours != nil {
		return b.neighbours
	}
	// Canonical vertex count: the legacy path has vertexBuf; the rigged path
	// keeps the base verts in canonicalBase instead.
	n := len(b.vertexBuf)
	if n == 0 {
		n = len(b.canonicalBase)
	}
	if len(b.baseIndices) == 0 || n == 0 {
		return nil
	}
	sets := make([]map[int32]struct{}, n)
	add := func(a, c int32) {
		if int(a) >= len(sets) {
			return
		}
		if sets[a] == nil {
			sets[a] = map[int32]struct{}{}
		}
		sets[a][c] = struct{}{}
	}
	for i := 0; i+2 < len(b.baseIndices); i += 3 {
		a, b1, c := b.baseIndices[i], b.baseIndices[i+1], b.baseIndices[i+2]
		add(a, b1)
		add(a, c)
		add(b1, a)
		add(b1, c)
		add(c, a)
		add(c, b1)
	}
	out := make([][]int32, len(sets))
	for i, s := range sets {
		if len(s) == 0 {
			continue
		}
		ns := make([]int32, 0, len(s))
		for k := range s {
			ns = append(ns, k)
		}
		out[i] = ns
	}
	b.neighbours = out
	return out
}

// markCoveredWithDirs scans each body vert that falls in the
// clothing's AABB and accumulates `-clothNormal` for every clothing
// vert within the per-item threshold. Body verts with ≥1 hit get
// covered[bi] = true and a non-zero dirs[bi]. Threshold is
// bbox_diagonal/sqrt(N), so sparse hats and dense pants each get a
// sensible inclusion radius. Caller renormalises dirs after all
// items are processed.
//
// Crucially this does NOT early-exit on first hit: covered verts
// with a single nearby clothing vert get one normal added, ones
// surrounded by many get a smoothed average — which is exactly the
// right behaviour for boundary shrink direction.
func markCoveredWithDirs(covered []bool, dirs, body, cloth, clothNormals []citizen.Vec3) {
	if len(cloth) == 0 || len(clothNormals) != len(cloth) {
		return
	}
	minP, maxP := cloth[0], cloth[0]
	for _, v := range cloth[1:] {
		if v.X < minP.X {
			minP.X = v.X
		} else if v.X > maxP.X {
			maxP.X = v.X
		}
		if v.Y < minP.Y {
			minP.Y = v.Y
		} else if v.Y > maxP.Y {
			maxP.Y = v.Y
		}
		if v.Z < minP.Z {
			minP.Z = v.Z
		} else if v.Z > maxP.Z {
			maxP.Z = v.Z
		}
	}
	sx, sy, sz := maxP.X-minP.X, maxP.Y-minP.Y, maxP.Z-minP.Z
	diag := float32(math.Sqrt(float64(sx*sx + sy*sy + sz*sz)))
	threshold := diag / float32(math.Sqrt(float64(len(cloth))))
	thresholdSq := threshold * threshold
	bbMinX, bbMaxX := minP.X-threshold, maxP.X+threshold
	bbMinY, bbMaxY := minP.Y-threshold, maxP.Y+threshold
	bbMinZ, bbMaxZ := minP.Z-threshold, maxP.Z+threshold
	for bi, bv := range body {
		if bv.X < bbMinX || bv.X > bbMaxX ||
			bv.Y < bbMinY || bv.Y > bbMaxY ||
			bv.Z < bbMinZ || bv.Z > bbMaxZ {
			continue
		}
		for ci, cv := range cloth {
			dx := bv.X - cv.X
			dy := bv.Y - cv.Y
			dz := bv.Z - cv.Z
			if dx*dx+dy*dy+dz*dz < thresholdSq {
				covered[bi] = true
				n := clothNormals[ci]
				dirs[bi].X -= n.X
				dirs[bi].Y -= n.Y
				dirs[bi].Z -= n.Z
			}
		}
	}
}

// loadCitizenDressing loads a clothing item's .obj geometry and .mhclo
// fit data from the asset library, builds a fresh MeshInstance3D +
// ArrayMesh pair, and returns the runtime state needed to refit it on
// every body rebuild. The caller positions the MeshInstance3D in the
// scene tree.
func loadCitizenDressing(objPath string) (*CitizenDressing, error) {
	mhcloPath := mhcloSidecarPath(objPath)
	objText, err := openCitizenText(objPath)
	if err != nil {
		return nil, err
	}
	base, err := citizen.ParseOBJ(objPath, strings.NewReader(objText))
	if err != nil {
		return nil, err
	}
	mhcloText, err := openCitizenText(mhcloPath)
	if err != nil {
		return nil, err
	}
	mhclo, err := citizen.ParseMHClo(mhcloPath, strings.NewReader(mhcloText))
	if err != nil {
		return nil, err
	}
	if len(mhclo.Anchors) != len(base.Verts) {
		return nil, fmt.Errorf("citizen: %s has %d verts but %s has %d anchors",
			objPath, len(base.Verts), mhcloPath, len(mhclo.Anchors))
	}
	d := &CitizenDressing{
		mi:          MeshInstance3D.New(),
		arrayMesh:   ArrayMesh.New(),
		mhclo:       mhclo,
		indices:     base.Indices,
		buf:         make([]Vector3.XYZ, len(base.Verts)),
		restNormals: clothRestNormals(base.Verts, base.Indices),
	}
	if len(base.UVs) == len(base.Verts) {
		d.uvs = make([]Vector2.XY, len(base.UVs))
		for i, uv := range base.UVs {
			d.uvs[i] = Vector2.XY{X: Float.X(uv.U), Y: Float.X(uv.V)}
		}
	}
	d.mi.SetMesh(d.arrayMesh.AsMesh())
	// Load the material but defer applying it as a surface override
	// until after refit() adds surface 0 — setting an override for a
	// surface index that doesn't yet exist gets dropped on the floor.
	d.material = loadDressingMaterial(objPath)
	return d, nil
}

// loadDressingMaterial looks for a sibling diffuse texture written by
// import_makehuman_clothes.sh as `<item>.diffuse.<ext>` and returns a
// StandardMaterial3D using it as albedo. Returns Nil if no texture is
// present — the import script only writes one when the source asset
// shipped an .mhmat with a diffuseTexture. We probe via `.import`
// sidecars rather than the .png itself: Godot's exporter strips the
// original .png from PCKs (keeping only the imported .ctex), so a
// FileAccess check on the .png would miss assets that load fine via
// Resource.Load.
func loadDressingMaterial(objPath string) StandardMaterial3D.Instance {
	base := strings.TrimSuffix(objPath, ".obj")
	for _, ext := range []string{"png", "jpg", "jpeg"} {
		path := base + ".diffuse." + ext
		if !FileAccess.FileExists(path + ".import") {
			continue
		}
		tex := LoadSync[Texture2D.Instance](path)
		if tex == Texture2D.Nil {
			continue
		}
		mat := StandardMaterial3D.New()
		mat.AsBaseMaterial3D().SetAlbedoTexture(tex)
		return mat
	}
	return StandardMaterial3D.Nil
}

// refit recomputes this clothing's vertex positions from the current
// body vertices and rebuilds its surface in place.
func (d *CitizenDressing) refit(body []citizen.Vec3) {
	d.fitBuf = d.mhclo.Fit(body, d.fitBuf)
	fitted := d.fitBuf
	if len(d.buf) != len(fitted) {
		d.buf = make([]Vector3.XYZ, len(fitted))
	}
	for i, v := range fitted {
		d.buf[i] = Vector3.XYZ{
			X: Float.X(v.X), Y: Float.X(v.Y), Z: Float.X(v.Z),
		}
	}
	d.arrayMesh.ClearSurfaces()
	var arrays [Mesh.ArrayMax]any
	arrays[Mesh.ArrayVertex] = d.buf
	arrays[Mesh.ArrayIndex] = d.indices
	if d.uvs != nil {
		arrays[Mesh.ArrayTexUv] = d.uvs
	}
	d.arrayMesh.AddSurfaceFromArrays(Mesh.PrimitiveTriangles, arrays[:])
	// Re-apply the surface override every refit. Surface overrides are
	// stored on the MeshInstance3D (not the Mesh) so they survive
	// ClearSurfaces in principle, but only if the surface index exists
	// when set — by rebinding after AddSurfaceFromArrays we don't have
	// to think about whether overrides survived or were dropped.
	if d.material != StandardMaterial3D.Nil {
		d.mi.SetSurfaceOverrideMaterial(0, d.material.AsMaterial())
	}
}
