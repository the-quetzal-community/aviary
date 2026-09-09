package internal

import (
	"encoding/json"
	"fmt"

	"graphics.gd/classdb/AnimationPlayer"
	"graphics.gd/classdb/Mesh"
	"graphics.gd/classdb/MeshInstance3D"
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/PackedScene"
	"graphics.gd/classdb/Skeleton3D"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Vector2"
	"graphics.gd/variant/Vector3"

	"the.quetzal.community/aviary/internal/citizen"
)

// Citizen rig asset paths. The animated GLB carries the mesh2motion-rigged
// citizen — a 66-bone skeleton, the straightened T-pose bind mesh, and the
// full Quaternius clip library. The transform sidecar records the base.obj→GLB
// similarity baked in during conversion.
const (
	citizenAnimatedGLB   = citizenLibraryRoot + "/citizen_animated.glb"
	citizenTransformPath = citizenLibraryRoot + "/citizen_mesh2motion.transform.json"
)

// buildCitizenRig assembles the pure-Go CitizenRig — the bind mesh, the
// canonical-vertex map, and the per-bone straighten rotations — from the
// already-instantiated animated scene.
//
// We read the geometry from the IMPORTED mesh (via Godot's SurfaceGetArrays)
// rather than parsing the raw .glb bytes: Godot scene-imports GLBs and strips
// the source file from the exported pck, so the bytes aren't FileAccess-
// readable. Reading the imported mesh's ARRAY_BONES has a bonus — those bone
// indices are exactly the ones the imported Skin (which we reuse) was built
// for, so the skinning can't be mis-indexed. The canonical map is recovered by
// UV (see BuildCitizenRig), so it needs only this one imported mesh.
func buildCitizenRig(scene *citizenRigScene, base *citizen.BaseMesh) (*citizen.CitizenRig, error) {
	anim, err := citizenGLBFromMesh(scene.bodyMI.Mesh())
	if err != nil {
		return nil, fmt.Errorf("citizen: extract animated mesh: %w", err)
	}
	scale, translate := loadCitizenTransform()
	return citizen.BuildCitizenRig(anim, base, scale, translate)
}

// citizenGLBFromMesh reads surface 0 of an imported mesh into the pure-Go GLB
// struct BuildCitizenRig consumes (positions/normals/UVs/joints/weights/
// indices).
func citizenGLBFromMesh(mesh Mesh.Instance) (*citizen.GLB, error) {
	if mesh == Mesh.Nil || mesh.GetSurfaceCount() == 0 {
		return nil, fmt.Errorf("citizen: imported mesh has no surfaces")
	}
	arr := mesh.SurfaceGetArrays(0)
	if len(arr) <= int(Mesh.ArrayIndex) {
		return nil, fmt.Errorf("citizen: surface arrays too short (%d)", len(arr))
	}
	// SurfaceGetArrays hands back plain Go slices per attribute.
	pos := packedVec3(arr[Mesh.ArrayVertex])
	if len(pos) == 0 {
		return nil, fmt.Errorf("citizen: no vertices in surface (vertex array is %T)", arr[Mesh.ArrayVertex])
	}
	g := &citizen.GLB{
		Positions: pos,
		Normals:   packedVec3(arr[Mesh.ArrayNormal]),
		UVs:       packedVec2(arr[Mesh.ArrayTexUv]),
		Indices:   packedInt32(arr[Mesh.ArrayIndex]),
	}
	g.Joints, g.Weights = packCitizenSkin(packedInt32(arr[Mesh.ArrayBones]), packedFloat32(arr[Mesh.ArrayWeights]), len(pos))
	return g, nil
}

// packCitizenSkin folds Godot's flat ARRAY_BONES/ARRAY_WEIGHTS (4 — or 8 with
// the 8-weights flag — influences per vertex) into the [4] form. When the mesh
// carries 8 influences the extra four are dropped and the kept four
// renormalised; mesh2motion human weights are 4-influence in practice.
func packCitizenSkin(bones []int32, weights []float32, vertCount int) ([][4]uint16, [][4]float32) {
	if vertCount == 0 || len(bones) == 0 || len(weights) == 0 {
		return nil, nil
	}
	stride := len(bones) / vertCount
	if stride < 4 {
		return nil, nil
	}
	j := make([][4]uint16, vertCount)
	w := make([][4]float32, vertCount)
	for i := 0; i < vertCount; i++ {
		o := i * stride
		var sum float32
		for k := 0; k < 4; k++ {
			j[i][k] = uint16(bones[o+k])
			w[i][k] = weights[o+k]
			sum += weights[o+k]
		}
		if stride > 4 && sum > 0 {
			for k := 0; k < 4; k++ {
				w[i][k] /= sum
			}
		}
	}
	return j, w
}

// packedVec3/packedVec2/packedInt32/packedFloat32 copy one SurfaceGetArrays
// element (graphics.gd hands the attributes back as plain Go slices:
// []Vector3.XYZ, []int32, …) into the slice types BuildCitizenRig wants. They
// return nil (not an error) for an absent/wrong-typed attribute so optional
// arrays (normals/UVs) degrade gracefully.
func packedVec3(a any) []citizen.Vec3 {
	p, ok := a.([]Vector3.XYZ)
	if !ok {
		return nil
	}
	out := make([]citizen.Vec3, 0, len(p))
	for _, v := range p {
		out = append(out, citizen.Vec3{X: float32(v.X), Y: float32(v.Y), Z: float32(v.Z)})
	}
	return out
}

func packedVec2(a any) []citizen.Vec2 {
	p, ok := a.([]Vector2.XY)
	if !ok {
		return nil
	}
	out := make([]citizen.Vec2, 0, len(p))
	for _, v := range p {
		out = append(out, citizen.Vec2{U: float32(v.X), V: float32(v.Y)})
	}
	return out
}

func packedInt32(a any) []int32 {
	p, ok := a.([]int32)
	if !ok {
		return nil
	}
	return append([]int32(nil), p...)
}

func packedFloat32(a any) []float32 {
	p, ok := a.([]float32)
	if !ok {
		return nil
	}
	return append([]float32(nil), p...)
}

// loadCitizenTransform returns the base.obj→GLB similarity (p_glb = scale·p_obj
// + translate). It prefers the conversion sidecar but falls back to the values
// convert_base_to_glb.py baked for the current base.obj, since the .json may
// not survive the pck export.
func loadCitizenTransform() (float32, citizen.Vec3) {
	scale := float32(0.10204755840381696)
	translate := citizen.Vec3{Y: 0.8334835767745972}
	if text, err := openCitizenText(citizenTransformPath); err == nil {
		var tf struct {
			Scale     float32    `json:"scale"`
			Translate [3]float32 `json:"translate"`
		}
		if json.Unmarshal([]byte(text), &tf) == nil && tf.Scale != 0 {
			scale = tf.Scale
			translate = citizen.Vec3{X: tf.Translate[0], Y: tf.Translate[1], Z: tf.Translate[2]}
		}
	}
	return scale, translate
}

// citizenRigScene bundles the live Godot nodes extracted from one
// instantiation of the animated GLB: the scene root, its imported Skeleton3D
// (66 bones, T-pose bind), the AnimationPlayer carrying the Quaternius clips,
// and the GLB's own skinned MeshInstance3D — which we keep (reusing its Skin
// + skeleton binding) but re-mesh with our procedural, morph-driven body.
type citizenRigScene struct {
	root     Node3D.Instance
	skeleton Skeleton3D.Instance
	player   AnimationPlayer.Instance
	bodyMI   MeshInstance3D.Instance
}

// instantiateCitizenRigScene loads + instantiates the animated GLB and pulls
// out the skeleton, animation player, and skinned mesh instance. The caller
// adds root to the scene tree and drives player. Returns an error if the GLB
// imported without the expected skeleton/mesh (e.g. import not yet run).
func instantiateCitizenRigScene() (*citizenRigScene, error) {
	ps := LoadSync[PackedScene.Instance](citizenAnimatedGLB)
	if ps == PackedScene.Nil {
		return nil, fmt.Errorf("citizen: cannot load scene %s", citizenAnimatedGLB)
	}
	root := Object.To[Node3D.Instance](ps.Instantiate())
	if root == Node3D.Nil {
		return nil, fmt.Errorf("citizen: rig scene instantiated nil root")
	}
	skel, ok := findSkeleton(root.AsNode())
	if !ok {
		return nil, fmt.Errorf("citizen: rig scene has no Skeleton3D")
	}
	mi, ok := findMeshInstance(root.AsNode())
	if !ok {
		return nil, fmt.Errorf("citizen: rig scene has no MeshInstance3D")
	}
	player, ok := findAnimationPlayer(root.AsNode())
	if !ok {
		return nil, fmt.Errorf("citizen: rig scene has no AnimationPlayer")
	}
	return &citizenRigScene{
		root:     root,
		skeleton: skel,
		player:   player,
		bodyMI:   mi,
	}, nil
}

func findMeshInstance(n Node.Instance) (MeshInstance3D.Instance, bool) {
	if mi, ok := Object.As[MeshInstance3D.Instance](n); ok {
		return mi, true
	}
	for _, child := range n.GetChildren() {
		if mi, ok := findMeshInstance(child); ok {
			return mi, ok
		}
	}
	return MeshInstance3D.Nil, false
}

func findAnimationPlayer(n Node.Instance) (AnimationPlayer.Instance, bool) {
	if p, ok := Object.As[AnimationPlayer.Instance](n); ok {
		return p, true
	}
	for _, child := range n.GetChildren() {
		if p, ok := findAnimationPlayer(child); ok {
			return p, ok
		}
	}
	return AnimationPlayer.Nil, false
}
