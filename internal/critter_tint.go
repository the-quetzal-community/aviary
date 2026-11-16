package internal

import (
	"math"

	"graphics.gd/classdb/BaseMaterial3D"
	"graphics.gd/classdb/Material"
	"graphics.gd/classdb/Mesh"
	"graphics.gd/classdb/MeshInstance3D"
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/StandardMaterial3D"
	"graphics.gd/variant/Color"
	"graphics.gd/variant/Object"
)

// Per-part colour. Any critter part — a library muzzle or foot, an eye, a
// horn — can be given its own colour, carried in the part's Change.Colour
// (PartAnchor.Tint). Alpha 0 means "not coloured": the part keeps the look
// it shipped with, or for procedural parts follows the body's skin. Every
// record that predates this has a zero Colour, so nothing changes for
// existing saves.
//
// The editor side is in editor_critter_tint.go: with a part selected, the
// material mode's colour sliders paint that part instead of the body.

// tintable is a procedural part that knows what its colour means (an
// eye's iris, a horn's surface). Library parts are tinted generically.
type tintable interface {
	SetTint(colour Color.RGBA, on bool)
}

// applyPartTint gives part id its colour (or clears it when tint.A is 0).
func (b *CritterBody) applyPartTint(id Node3D.ID, tint Color.RGBA) {
	node, ok := id.Instance()
	if !ok {
		return
	}
	on := tint.A > 0
	if part, ok := b.skinFollowers[id].(tintable); ok {
		part.SetTint(tint, on)
		if b.critter != nil {
			// Re-derive the skin-following look the tint just released.
			b.skinFollowers[id].SetSkinColours(skinColours(b.critter))
		}
		return
	}
	tintMeshes(node.AsNode(), tint, on)
}

// tintMeshes multiplies the albedo of every standard material under node
// by tint, through per-instance surface overrides — the mesh's own
// materials are shared by every instance of the design and are left
// alone, which also keeps the original to re-tint from. Clearing removes
// the overrides.
func tintMeshes(node Node.Instance, tint Color.RGBA, on bool) {
	if mi, ok := Object.As[MeshInstance3D.Instance](node); ok && mi.Mesh() != Mesh.Nil {
		mesh := mi.Mesh()
		for i := 0; i < mesh.GetSurfaceCount(); i++ {
			if !on {
				mi.SetSurfaceOverrideMaterial(i, Material.Nil)
				continue
			}
			base := mesh.SurfaceGetMaterial(i)
			var tinted Material.Instance
			if base == Material.Nil {
				plain := StandardMaterial3D.New()
				plain.AsBaseMaterial3D().SetAlbedoColor(Color.RGBA{R: tint.R, G: tint.G, B: tint.B, A: 1})
				tinted = plain.AsMaterial()
			} else if std, ok := Object.As[BaseMaterial3D.Instance](base.AsResource().Duplicate()); ok {
				c := std.AlbedoColor()
				std.SetAlbedoColor(Color.RGBA{R: c.R * tint.R, G: c.G * tint.G, B: c.B * tint.B, A: c.A})
				tinted = std.AsMaterial()
			} else {
				continue // a shader material: nothing generic to tint
			}
			mi.SetSurfaceOverrideMaterial(i, tinted)
		}
	}
	for _, child := range node.GetChildren() {
		tintMeshes(child, tint, on)
	}
}

// rgbToHSV is the inverse of hsv.
func rgbToHSV(c Color.RGBA) (h, s, v float32) {
	hi := max(c.R, c.G, c.B)
	lo := min(c.R, c.G, c.B)
	v = hi
	d := hi - lo
	if hi <= 0 || d <= 0 {
		return 0, 0, v
	}
	s = d / hi
	switch hi {
	case c.R:
		h = (c.G - c.B) / d
	case c.G:
		h = (c.B-c.R)/d + 2
	default:
		h = (c.R-c.G)/d + 4
	}
	h /= 6
	h -= float32(math.Floor(float64(h)))
	return h, s, v
}
