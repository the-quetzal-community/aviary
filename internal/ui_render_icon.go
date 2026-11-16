package internal

import (
	"math"

	"graphics.gd/classdb/Camera3D"
	"graphics.gd/classdb/DirectionalLight3D"
	"graphics.gd/classdb/MeshInstance3D"
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/ShaderMaterial"
	"graphics.gd/classdb/SphereMesh"
	"graphics.gd/classdb/SubViewport"
	"graphics.gd/classdb/Texture2D"
	"graphics.gd/variant/Basis"
	"graphics.gd/variant/Euler"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Transform3D"
	"graphics.gd/variant/Vector2i"
	"graphics.gd/variant/Vector3"

	"the.quetzal.community/aviary/internal/critter"
)

// renderIcon renders `subject` once into an icon texture: the thing the
// tile or tab stands for, drawn by the game itself, in place of a
// hand-drawn icon. The little scene (its own world, a camera framing the
// subject's bounds, two lights) lives in a SubViewport parented to `host`
// — the control showing the icon — so the texture lives exactly as long as
// what displays it.
func renderIcon(host Node.Instance, subject Node3D.Instance, width, height int) Texture2D.Instance {
	if host == Node.Nil || subject == Node3D.Nil {
		return Texture2D.Nil
	}
	view := SubViewport.New()
	view.SetSize(Vector2i.New(width, height))
	view.SetRenderTargetUpdateMode(SubViewport.UpdateOnce)
	view.AsViewport().SetOwnWorld3d(true).SetTransparentBg(true)
	view.AsNode().AddChild(subject.AsNode())

	lo, hi, found := localVisualBounds(subject.AsNode(), Transform3D.Identity)
	if !found {
		lo, hi = Vector3.New(-0.5, -0.5, -0.5), Vector3.New(0.5, 0.5, 0.5)
	}
	centre := Vector3.MulX(Vector3.Add(lo, hi), 0.5)
	// Frame on the largest half-extent with a margin, so the subject
	// fills about three quarters of the icon — the share a drawn glyph
	// takes of its canvas.
	extent := Vector3.Sub(hi, lo)
	radius := max(extent.X, extent.Y, extent.Z) / 2
	const fov = 30.0
	distance := radius / Float.X(math.Sin(fov/2*math.Pi/180)) * 1.3

	lookFrom := func(dir Vector3.XYZ) Transform3D.BasisOrigin {
		z := Vector3.Normalized(dir) // a camera/light looks down its −Z
		x := Vector3.Normalized(Vector3.Cross(Vector3.New(0, 1, 0), z))
		y := Vector3.Cross(z, x)
		return Transform3D.BasisOrigin{
			Basis:  Basis.XYZ{x, y, z},
			Origin: Vector3.Add(centre, Vector3.MulX(z, distance)),
		}
	}
	cam := Camera3D.New()
	cam.SetFov(fov).SetCurrent(true)
	cam.AsNode3D().SetTransform(lookFrom(Vector3.New(0.8, 0.25, 1)))
	view.AsNode().AddChild(cam.AsNode())
	for _, light := range []struct {
		from   Vector3.XYZ
		energy Float.X
	}{{Vector3.New(-0.6, 1, 0.8), 1.3}, {Vector3.New(1, -0.3, -0.6), 0.5}} {
		l := DirectionalLight3D.New()
		l.AsLight3D().SetLightEnergy(light.energy)
		l.AsNode3D().SetTransform(lookFrom(light.from))
		view.AsNode().AddChild(l.AsNode())
	}
	host.AddChild(view.AsNode())
	return view.AsViewport().GetTexture().AsTexture2D()
}

// sweptIconSubject is a parametric part posed for its tile: the neutral
// primitive standing upright (its outward +Z turned to +Y).
func sweptIconSubject(design string) Node3D.Instance {
	part := newProceduralPart(design)
	if part == nil {
		return Node3D.Nil
	}
	root := Node3D.New()
	node := part.Node()
	node.SetRotation(Euler.Radians{X: -math.Pi / 2})
	root.AsNode().AddChild(node.AsNode())
	return root
}

// skinIconSubject is a ball wearing the critter's skin with `weights`
// laid over it — what a skin slider's tab shows, e.g. the current
// colours with spots on for the pattern slider.
func skinIconSubject(c *critter.Critter, weights map[string]float32) Node3D.Instance {
	sample := critter.New()
	if c != nil {
		sample.Restore(nil, nil, c.Weights())
	}
	for name, w := range weights {
		sample.SetWeight(name, w)
	}
	body := CritterBody{critter: sample, skinMaterial: newSkinMaterial()}
	body.applySkin()
	// A SphereMesh's UVs stand in for the body's rest coordinates: the
	// pattern is drawn across the unwrapped ball.
	body.skinMaterial.SetShaderParameter("pattern_scale", 7*math.Exp2(2*float64(sample.Weight("skin/scale"))))
	ball := SphereMesh.New()
	mi := MeshInstance3D.New()
	mi.SetMesh(ball.AsMesh())
	// The node holds the material for the icon's lifetime.
	mi.AsGeometryInstance3D().SetMaterialOverride(ShaderMaterial.Instance(body.skinMaterial).AsMaterial())
	return mi.AsNode3D()
}
