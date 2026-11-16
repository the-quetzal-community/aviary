package critter

import "math"

// PartShape is the player-sculpted shape of a parametric part (horn,
// fin, …): a tapered section swept along a circular arc. The three
// values are stored as offsets from a neutral primitive so the zero
// value IS the default part — a short straight cone — and a record
// that predates a parameter decodes to the neutral shape.
//
//	Length — log2 multiplier on the neutral length
//	Curl   — total bend of the arc in radians (0 = straight)
//	Girth  — log2 multiplier on the neutral base radius
//
// The part's local frame matches CritterBody.positionPart: +Z points
// out of the body surface, and the arc bends toward +Y.
type PartShape struct {
	Length, Curl, Girth float32
}

const (
	sweptNeutralLength = float32(0.25)
	sweptNeutralGirth  = float32(0.06)
	sweptMaxExponent   = float32(3)
	sweptMaxCurl       = float32(5.5)
)

func clampf(v, lo, hi float32) float32 {
	if v != v {
		return 0 // NaN from a corrupt record
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// Clamped returns s with every parameter inside its supported range,
// so a hostile or corrupt record can't build a degenerate mesh.
func (s PartShape) Clamped() PartShape {
	return PartShape{
		Length: clampf(s.Length, -sweptMaxExponent, sweptMaxExponent),
		Curl:   clampf(s.Curl, -sweptMaxCurl, sweptMaxCurl),
		Girth:  clampf(s.Girth, -sweptMaxExponent, sweptMaxExponent),
	}
}

// Dims resolves the stored offsets into local-space measurements: arc
// length, total bend angle and base radius.
func (s PartShape) Dims() (length, curl, girth float32) {
	s = s.Clamped()
	length = sweptNeutralLength * float32(math.Exp2(float64(s.Length)))
	girth = sweptNeutralGirth * float32(math.Exp2(float64(s.Girth)))
	return length, s.Curl, girth
}

// arcPoint evaluates the centre line at u ∈ [0,1]: position, plus the
// in-plane normal (perpendicular to the tangent, toward the outside
// of the bend when curl > 0 … the side the section's Y axis rides).
func arcPoint(length, curl, u float32) (pos, normal Vec3) {
	a := float64(curl * u)
	if math.Abs(float64(curl)) < 1e-4 {
		return Vec3{Z: length * u}, Vec3{Y: 1}
	}
	r := float64(length / curl)
	pos = Vec3{Y: float32(r * (1 - math.Cos(a))), Z: float32(r * math.Sin(a))}
	normal = Vec3{Y: float32(math.Cos(a)), Z: float32(-math.Sin(a))}
	return pos, normal
}

// Tip is the local-space end point of the part's centre line — where
// the editor puts the sculpt handle.
func (s PartShape) Tip() Vec3 {
	length, curl, _ := s.Dims()
	pos, _ := arcPoint(length, curl, 1)
	return pos
}

// WithTip returns s re-solved so its centre line ends at the local
// point (y, z) in the bend plane: dragging the tip handle sets length
// and curl together. A circular arc leaving the origin along +Z
// reaches a chord at angle α after bending 2α, so curl = 2α and the
// arc length follows from the chord.
func (s PartShape) WithTip(y, z float32) PartShape {
	chord := math.Hypot(float64(y), float64(z))
	if chord < 1e-5 {
		return s
	}
	alpha := math.Atan2(float64(y), float64(z))
	curl := clampf(float32(2*alpha), -sweptMaxCurl, sweptMaxCurl)
	half := float64(curl) / 2
	length := chord
	if math.Abs(half) > 1e-3 {
		length = chord * half / math.Sin(half)
	}
	s.Curl = curl
	s.Length = float32(math.Log2(length / float64(sweptNeutralLength)))
	return s.Clamped()
}

// WithGirth returns s with its base radius set to r (local units).
func (s PartShape) WithGirth(r float32) PartShape {
	if r < 1e-5 {
		r = 1e-5
	}
	s.Girth = float32(math.Log2(float64(r / sweptNeutralGirth)))
	return s.Clamped()
}

// SweptProfile is the cross-section a parametric part sweeps along its
// arc. Side scales the section across the bend plane (local X),
// Depth within it; Taper is the exponent of the falloff toward the
// tip (1 = straight cone, <1 = stays broad then closes late).
type SweptProfile struct {
	Side, Depth, Taper float32
}

var (
	// ProfileHorn is a round cone.
	ProfileHorn = SweptProfile{Side: 1, Depth: 1, Taper: 1}
	// ProfileFin is a thin blade lying in the bend plane.
	ProfileFin = SweptProfile{Side: 0.18, Depth: 2.4, Taper: 0.6}
)

// BuildSweptMesh builds the part: `rings` cross-sections of
// `segmentsAround` vertices each along the arc, closed by a single
// tip vertex. The base is left open and sunk a little below the
// origin so it stays buried in a curved body surface.
func BuildSweptMesh(s PartShape, p SweptProfile, rings, segmentsAround int) Mesh {
	if rings < 2 {
		rings = 2
	}
	if segmentsAround < 3 {
		segmentsAround = 3
	}
	length, curl, girth := s.Dims()
	verts := make([]Vec3, 0, (rings+1)*segmentsAround+1)
	normals := make([]Vec3, 0, cap(verts))
	ring := func(pos, normal Vec3, radius float32) {
		for j := 0; j < segmentsAround; j++ {
			a := 2 * math.Pi * float64(j) / float64(segmentsAround)
			cs, sn := float32(math.Cos(a)), float32(math.Sin(a))
			sx, sy := radius*p.Side*cs, radius*p.Depth*sn
			verts = append(verts, Vec3{
				X: pos.X + sx,
				Y: pos.Y + normal.Y*sy,
				Z: pos.Z + normal.Z*sy,
			})
			// Ellipse normal: the gradient of the implicit section.
			nx, ny := cs/p.Side, sn/p.Depth
			normals = append(normals, normalise(Vec3{X: nx, Y: normal.Y * ny, Z: normal.Z * ny}))
		}
	}
	// Buried skirt ring, then the rings along the arc (the last ring
	// stops short of the tip, which is a single shared vertex).
	ring(Vec3{Z: -girth * 0.5}, Vec3{Y: 1}, girth)
	for i := 0; i < rings; i++ {
		u := float32(i) / float32(rings)
		pos, normal := arcPoint(length, curl, u)
		ring(pos, normal, girth*float32(math.Pow(float64(1-u), float64(p.Taper))))
	}
	tip := int32(len(verts))
	tipPos, tipNormal := arcPoint(length, curl, 1)
	verts = append(verts, tipPos)
	// Tip normal points along the tangent (perpendicular to the
	// in-plane normal, within the YZ plane).
	normals = append(normals, Vec3{Y: -tipNormal.Z, Z: tipNormal.Y})

	indices := make([]int32, 0, rings*segmentsAround*6+segmentsAround*3)
	for i := 0; i < rings; i++ {
		base0 := int32(i * segmentsAround)
		base1 := int32((i + 1) * segmentsAround)
		for j := 0; j < segmentsAround; j++ {
			jn := int32((j + 1) % segmentsAround)
			a, b := base0+int32(j), base0+jn
			c, d := base1+int32(j), base1+jn
			indices = append(indices, a, c, d, a, d, b)
		}
	}
	last := int32(rings * segmentsAround)
	for j := 0; j < segmentsAround; j++ {
		jn := int32((j + 1) % segmentsAround)
		indices = append(indices, last+int32(j), tip, last+jn)
	}
	return Mesh{Verts: verts, Normals: normals, Indices: indices}
}
