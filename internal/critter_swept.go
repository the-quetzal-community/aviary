package internal

import (
	"graphics.gd/classdb/ArrayMesh"
	"graphics.gd/classdb/BoxShape3D"
	"graphics.gd/classdb/CollisionShape3D"
	"graphics.gd/classdb/MeshInstance3D"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/StandardMaterial3D"
	"graphics.gd/classdb/StaticBody3D"
	"graphics.gd/variant/Color"
	"graphics.gd/variant/Vector3"

	"the.quetzal.community/aviary/internal/critter"
)

// Parametric critter parts: a horn and a fin, both a section swept along
// an arc (critter.BuildSweptMesh) that the player sculpts with handles in
// the editor (editor_critter_shape.go). They start as neutral primitives —
// a plain cone, a plain blade — and take the body's second skin colour;
// what they become is the player's doing.
const (
	BuiltinHornDesign = "procedural://critter/horn"
	BuiltinFinDesign  = "procedural://critter/fin"
)

// shapeToWire / shapeFromWire carry a PartShape in Change.Mirror (a slot
// critter parts never used, zero in every older record — which decodes
// to the neutral shape).
func shapeToWire(s critter.PartShape) Vector3.XYZ {
	return Vector3.New(s.Length, s.Curl, s.Girth)
}

func shapeFromWire(v Vector3.XYZ) critter.PartShape {
	return critter.PartShape{Length: float32(v.X), Curl: float32(v.Y), Girth: float32(v.Z)}.Clamped()
}

// sweptPart is one placed parametric part.
type sweptPart struct {
	node     Node3D.Instance
	mesh     ArrayMesh.Instance
	box      BoxShape3D.Instance
	collider CollisionShape3D.Instance
	material StandardMaterial3D.Instance
	profile  critter.SweptProfile
	shape    critter.PartShape
	tint     Color.RGBA
	tinted   bool
}

func newSweptPart(profile critter.SweptProfile) *sweptPart {
	p := &sweptPart{
		node:     Node3D.New(),
		mesh:     ArrayMesh.New(),
		box:      BoxShape3D.New(),
		collider: CollisionShape3D.New(),
		material: StandardMaterial3D.New(),
		profile:  profile,
	}
	p.material.AsBaseMaterial3D().SetRoughness(0.6)
	mi := MeshInstance3D.New()
	mi.SetMesh(p.mesh.AsMesh())
	// The node holds the material, not the surface: every reshape clears
	// the surface, and a surface holding the engine's only reference
	// would free the material with it (a Go handle is not a refcount).
	mi.AsGeometryInstance3D().SetMaterialOverride(p.material.AsMaterial())
	p.node.AsNode().AddChild(mi.AsNode())
	// A box around the mesh makes the part selectable; it is resized
	// with every reshape.
	body := StaticBody3D.New()
	p.collider.SetShape(p.box.AsShape3D())
	body.AsNode().AddChild(p.collider.AsNode())
	p.node.AsNode().AddChild(body.AsNode())
	p.SetShape(critter.PartShape{})
	return p
}

func (p *sweptPart) Node() Node3D.Instance { return p.node }

// Process: a swept part has no idle animation of its own.
func (p *sweptPart) Process(delta float32) {}

func (p *sweptPart) Shape() critter.PartShape { return p.shape }

// SetShape rebuilds the mesh and its selection box for a new shape.
func (p *sweptPart) SetShape(shape critter.PartShape) {
	p.shape = shape.Clamped()
	m := critter.BuildSweptMesh(p.shape, p.profile, 12, 10)
	UploadCritterMesh(p.mesh, m)
	if len(m.Verts) == 0 {
		return
	}
	lo, hi := m.Verts[0], m.Verts[0]
	for _, v := range m.Verts {
		lo = critter.Vec3{X: min(lo.X, v.X), Y: min(lo.Y, v.Y), Z: min(lo.Z, v.Z)}
		hi = critter.Vec3{X: max(hi.X, v.X), Y: max(hi.Y, v.Y), Z: max(hi.Z, v.Z)}
	}
	p.box.SetSize(Vector3.New(hi.X-lo.X, hi.Y-lo.Y, hi.Z-lo.Z))
	p.collider.AsNode3D().SetPosition(Vector3.New((lo.X+hi.X)/2, (lo.Y+hi.Y)/2, (lo.Z+hi.Z)/2))
}

// SetSkinColours implements skinFollower: horns and fins wear the
// body's second colour.
func (p *sweptPart) SetSkinColours(primary, secondary Color.RGBA) {
	if p.tinted {
		secondary = p.tint
	}
	p.material.AsBaseMaterial3D().SetAlbedoColor(secondary)
}

// SetTint implements tintable: the part's own colour, overriding the
// skin's second colour while on.
func (p *sweptPart) SetTint(colour Color.RGBA, on bool) {
	p.tint, p.tinted = Color.RGBA{R: colour.R, G: colour.G, B: colour.B, A: 1}, on
	if on {
		p.material.AsBaseMaterial3D().SetAlbedoColor(p.tint)
	}
}
