package internal

import (
	"math"

	"graphics.gd/classdb/ArrayMesh"
	"graphics.gd/classdb/Gradient"
	"graphics.gd/classdb/GradientTexture2D"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/Shader"
	"graphics.gd/classdb/ShaderMaterial"
	"graphics.gd/classdb/Texture2D"
	"graphics.gd/variant/Color"
	"graphics.gd/variant/Vector2"

	"the.quetzal.community/aviary/internal/critter"
)

// The critter's skin — two colours and a pattern — lives in the critter's
// weight map under "skin/*", so it replicates, persists, snapshots and
// travels in a bookmarked creation exactly like the macro shape sliders,
// with no change to the musical format. Every weight defaults to zero and
// zero is the uncoloured critter, so saves that predate skin look as they
// always did. critter_skin.gdshader draws it.

// skinSlider is one skin control: the explorer tab that shows it, the
// weight it writes, and its range.
type skinSlider struct {
	tab, weight    string
	min, max, step float64
}

// skinSliders is the critter editor's material mode, in display order:
// the main colour, the pattern, then the second colour the pattern and
// the underside are drawn in.
var skinSliders = []skinSlider{
	{"editing/hue", "skin/hue", 0, 1, 0.005},
	{"editing/sat", "skin/sat", 0, 1, 0.01},
	{"editing/shade", "skin/shade", 0, 1, 0.01},
	{"editing/pattern", "skin/pattern", 0, 4, 1},
	{"editing/scale", "skin/scale", -1, 1, 0.02},
	{"editing/hue2", "skin/hue2", 0, 1, 0.005},
	{"editing/sat2", "skin/sat2", 0, 1, 0.01},
	// Offset so the untouched second colour is a mid grey: a pattern
	// shows the moment it is picked, before any colour is chosen.
	{"editing/shade2", "skin/shade2", -0.6, 0.4, 0.01},
	{"editing/belly", "skin/belly", 0, 1, 0.01},
	{"editing/gloss", "skin/gloss", 0, 1, 0.01},
}

func skinSliderForTab(tab string) (skinSlider, bool) {
	for _, s := range skinSliders {
		if s.tab == tab {
			return s, true
		}
	}
	return skinSlider{}, false
}

// isSkinWeight reports whether a weight name is a skin control (which
// recolours the material) rather than a shape slider (which rebuilds
// the mesh).
func isSkinWeight(name string) bool {
	return len(name) > 5 && name[:5] == "skin/"
}

// skinFollower is a procedural part whose look tracks the body's skin
// (eyelids match the main colour, horns take the second).
type skinFollower interface {
	SetSkinColours(primary, secondary Color.RGBA)
}

// shapedPart is a parametric part the player sculpts (see
// critter.PartShape).
type shapedPart interface {
	SetShape(shape critter.PartShape)
	Shape() critter.PartShape
}

func hsv(h, s, v float32) Color.RGBA {
	h = h - float32(math.Floor(float64(h)))
	s = min(max(s, 0), 1)
	v = min(max(v, 0), 1)
	i := int(h * 6)
	f := h*6 - float32(i)
	p, q, t := v*(1-s), v*(1-f*s), v*(1-(1-f)*s)
	var r, g, b float32
	switch i % 6 {
	case 0:
		r, g, b = v, t, p
	case 1:
		r, g, b = q, v, p
	case 2:
		r, g, b = p, v, t
	case 3:
		r, g, b = p, q, v
	case 4:
		r, g, b = t, p, v
	default:
		r, g, b = v, p, q
	}
	return Color.RGBA{R: r, G: g, B: b, A: 1}
}

// skinColours resolves a critter's two skin colours from its weights.
func skinColours(c *critter.Critter) (primary, secondary Color.RGBA) {
	primary = hsv(c.Weight("skin/hue"), c.Weight("skin/sat"), 1-c.Weight("skin/shade"))
	secondary = hsv(c.Weight("skin/hue2"), c.Weight("skin/sat2"), 0.4-c.Weight("skin/shade2"))
	return primary, secondary
}

func newSkinMaterial() ShaderMaterial.Instance {
	mat := ShaderMaterial.New()
	mat.SetShader(LoadSync[Shader.Instance]("res://shader/critter_skin.gdshader"))
	return mat
}

// applySkin pushes the critter's skin weights into the skin material
// and onto every part that follows the skin.
func (b *CritterBody) applySkin() {
	if b.critter == nil || b.skinMaterial == ShaderMaterial.Nil {
		return
	}
	c := b.critter
	primary, secondary := skinColours(c)
	b.skinMaterial.SetShaderParameter("primary", primary)
	b.skinMaterial.SetShaderParameter("secondary", secondary)
	b.skinMaterial.SetShaderParameter("pattern", int(math.Round(float64(c.Weight("skin/pattern")))))
	b.skinMaterial.SetShaderParameter("pattern_scale", 5*math.Exp2(2*float64(c.Weight("skin/scale"))))
	b.skinMaterial.SetShaderParameter("belly", float64(c.Weight("skin/belly")))
	b.skinMaterial.SetShaderParameter("gloss", float64(c.Weight("skin/gloss")))
	for id, follower := range b.skinFollowers {
		if _, ok := id.Instance(); !ok {
			delete(b.skinFollowers, id)
			continue
		}
		follower.SetSkinColours(primary, secondary)
	}
}

// AttachProcedural attaches a code-built part at anchor, giving it the
// anchor's sculpted shape and the body's skin colours.
func (b *CritterBody) AttachProcedural(anchor PartAnchor, part proceduralParter) Node3D.Instance {
	if shaped, ok := part.(shapedPart); ok {
		shaped.SetShape(anchor.Shape)
	}
	node := b.AttachPartNode(anchor, part.Node())
	if node == Node3D.Nil {
		return node
	}
	if follower, ok := part.(skinFollower); ok {
		if b.skinFollowers == nil {
			b.skinFollowers = make(map[Node3D.ID]skinFollower)
		}
		b.skinFollowers[node.ID()] = follower
		if b.critter != nil {
			follower.SetSkinColours(skinColours(b.critter))
		}
	}
	return node
}

// skinCoords fills the UV/UV2 arrays the skin shader reads its pattern
// position from: the vertex's REST position (x, y in UV; z in UV2.x).
func skinCoords(rest []critter.Vec3, uv, uv2 *[]Vector2.XY) {
	if cap(*uv) < len(rest) {
		*uv = make([]Vector2.XY, len(rest))
		*uv2 = make([]Vector2.XY, len(rest))
	}
	*uv, *uv2 = (*uv)[:len(rest)], (*uv2)[:len(rest)]
	for i, v := range rest {
		(*uv)[i] = Vector2.XY{X: v.X, Y: v.Y}
		(*uv2)[i] = Vector2.XY{X: v.Z}
	}
}

// setSkinSurface puts the skin material on a freshly uploaded critter
// surface (ClearSurfaces drops it with the surface).
func (b *CritterBody) setSkinSurface(am ArrayMesh.Instance) {
	if b.skinMaterial != ShaderMaterial.Nil && am.AsMesh().GetSurfaceCount() > 0 {
		am.AsMesh().SurfaceSetMaterial(0, b.skinMaterial.AsMaterial())
	}
}

// skinTabIcon draws the tab icon for a skin slider. The colour sliders
// get a swatch of what they sweep through — generated, since a colour
// ramp is data rather than art — square for the main colour and round
// for the second; the rest reuse existing icons.
func skinTabIcon(c *critter.Critter, tab string) Texture2D.Instance {
	white := Color.RGBA{R: 1, G: 1, B: 1, A: 1}
	black := Color.RGBA{A: 1}
	var hue1, hue2 float32
	if c != nil {
		hue1, hue2 = c.Weight("skin/hue"), c.Weight("skin/hue2")
	}
	rainbow := make([]Color.RGBA, 7)
	for i := range rainbow {
		rainbow[i] = hsv(float32(i)/6, 1, 1)
	}
	switch tab {
	case "editing/hue":
		return rampIcon(rainbow, false)
	case "editing/sat":
		return rampIcon([]Color.RGBA{white, hsv(hue1, 1, 1)}, false)
	case "editing/shade":
		return rampIcon([]Color.RGBA{white, black}, false)
	case "editing/hue2":
		return rampIcon(rainbow, true)
	case "editing/sat2":
		return rampIcon([]Color.RGBA{white, hsv(hue2, 1, 1)}, true)
	case "editing/shade2":
		return rampIcon([]Color.RGBA{white, black}, true)
	case "editing/pattern":
		return LoadSync[Texture2D.Instance]("res://ui/texture.svg")
	case "editing/scale":
		return LoadSync[Texture2D.Instance]("res://ui/details.svg")
	case "editing/belly":
		return LoadSync[Texture2D.Instance]("res://ui/shading.svg")
	case "editing/gloss":
		return LoadSync[Texture2D.Instance]("res://ui/surface.svg")
	}
	return Texture2D.Nil
}

// rampIcon builds a colour-ramp swatch: a left-to-right bar, or (round)
// a disc sweeping from the centre out with a transparent surround.
func rampIcon(colours []Color.RGBA, round bool) Texture2D.Instance {
	offsets := make([]float32, len(colours))
	for i := range colours {
		offsets[i] = float32(i) / float32(len(colours)-1)
	}
	tex := GradientTexture2D.New().SetWidth(64).SetHeight(64)
	if round {
		for i := range offsets {
			offsets[i] *= 0.9
		}
		edge := colours[len(colours)-1]
		edge.A = 0
		offsets = append(offsets, 1)
		colours = append(append([]Color.RGBA(nil), colours...), edge)
		tex.SetFill(GradientTexture2D.FillRadial).SetFillFrom(Vector2.New(0.5, 0.5)).SetFillTo(Vector2.New(1, 0.5))
	}
	grad := Gradient.New()
	grad.SetOffsets(offsets)
	grad.SetColors(colours)
	tex.SetGradient(grad)
	return tex.AsTexture2D()
}
