// Package vulture provides an API for collaborative and creative community spaces.
package vulture

import (
	"context"
	"io/fs"
	"iter"
	"unsafe"

	"runtime.link/api"
)

// API specification.
type API struct {
	api.Specification `api:"Vulture"
		provides access to collaborative and creative community spaces.
	`
	Vision func(context.Context) (chan<- Vision, error) `http:"GET /vulture/v0/vision"
		can be used to project your vision onto the space, if successful, will
		generate an [Event].`
	Upload func(context.Context, Upload) (fs.File, error) `http:"GET /vulture/v0/upload/{upload=%v}"
		can be used to download an uploaded visual resource by ID.`
	Lookup func(context.Context, fs.File) (Upload, error) `http:"POST /vulture/v0/lookup"
		returns the ID associated with the specified visual resource.`
	Target func(context.Context, Target) error `http:"POST /vulture/v0/target"
		returns a write-only stream for the client to report their
		focus.`
	Uplift func(context.Context, Uplift) error `http:"POST /vulture/v0/uplift"
		can be used to modify the surface of a region.`
	Reform func(context.Context, []Deltas) error `http:"POST /vulture/v0/reform"
		can be used to undo/redo changes.`
	Events func(context.Context) (<-chan []Deltas, error) `http:"GET /vulture/v0/events"
		returns events visible to the client.`
}

// Deprecated
type Territory struct {
	Area     Area            `json:"area"`
	Vertices [17 * 17]Vertex `json:"vertices"`
}

// Upload identifier.
type Upload uint16

// Area of the world.
type Area [2]int16

// Cell within an area.
type Cell uint8

// Ticks in 1/10 second intervals, relative to the regional period.
type Ticks int16

// Time in unix nanoseconds.
type Time int64

// Vision represents a focal point or view.
type Vision struct {
	Grid     Region
	Cell     Cell
	Jump     uint16
	Bump     uint8
	Size     [2]uint16
	Zoom     uint16 // if 0, then FPS
	Roll     uint16
	Pitch    uint16
	Yaw      uint16
	Mesh     Upload
	Anim     uint8
	Flag     uint8 // teleport, preview, mouse, etc.
	Fovy     uint16
	Controls uint16
	Select   Target
	_        uint16
}

// Offset of an element within a [Region].
type Offset uint16

// Target represents the focal point of the client.
type Target struct {
	Region Region
	Offset Offset
}

// Uplift to apply to the surface of the world.
type Uplift struct {
	Time Time   `json:"time"`
	Area Region `json:"area"`
	Cell Cell   `json:"cell"`
	Size uint8  `json:"size"`
	Lift int8   `json:"lift"`
}

// Deltas represents an update to what the client can see.
// Will either be packed or sparse (not both).
type Deltas struct {
	Region Region `json:"region"
		uniquely identifies the region.`
	Period Time `json:"period"
		for which this vision should take effect.`
	Packet Time `json:"packet"
		used for ping pong.`
	Global *Global `json:"global,omitempty"
		are the global settings for the space.`
	Vision *Vision `json:"vision,omitempty"
		is an optional request to adjust the camera.`
	Append Elements `json:"append,omitempty"
			elements to the space.`
	Offset Offset `json:"offset,omitempty"
		is the first element to write from.`
	Packed Elements `json:"packed,omitempty"
		elements to display.`
	Sparse map[Offset]Element `json:"sparse,omitempty"
		elements to display.`
}

type Global struct {
	Sun [6]float32 `json:"sun,omitempty"
		intensity and then direction and angular velocity in XYZ order (euler rotations).`
	Fog float32 `json:"fog,omitempty"
		distance.`
	Sky Upload `json:"sky,omitempty"
		box.`
}

// Region uniquely identifies a grid in the space.
type Region [2]int8

// Element can either be a [Thing], [Anime] or ?
type Element [16]byte

func (el Element) Element() Element { return el }

type Elements []byte

type isElement interface {
	Element() Element
}

func (el *Elements) Add(element isElement) {
	add := element.Element()
	*el = append(*el, add[:]...)
}

func (el *Elements) Apply(delta Deltas) {
	if delta.Append != nil {
		*el = append(*el, delta.Append...)
	}
	if delta.Packed != nil {
		if len(*el) < int(delta.Offset*16)+len(delta.Packed) {
			*el = append(*el, make([]byte, int(delta.Offset*16)+len(delta.Packed)-len(*el))...)
		}
		copy((*el)[delta.Offset*16:], delta.Packed)
	}
	if delta.Sparse != nil {
		for offset, element := range delta.Sparse {
			if len(*el) < int(offset*16)+16 {
				*el = append(*el, make([]byte, int(offset*16)+16-len(*el))...)
			}
			copy((*el)[offset*16:], element[:])
		}
	}
}

func (el Elements) Iter(off Offset) iter.Seq2[Offset, *Element] {
	return func(yield func(Offset, *Element) bool) {
		for i := 0; i < len(el); i += 16 {
			if !yield(off+Offset(i/16), (*Element)(el[i:i+16])) {
				break
			}
		}
	}
}

func (el *Elements) Len() Offset {
	return Offset(len(*el) / 16)
}

func (dt Deltas) Iter(append Offset) iter.Seq2[Offset, *Element] {
	if dt.Packed != nil {
		return dt.Packed.Iter(dt.Offset)
	}
	if dt.Append != nil {
		return dt.Append.Iter(append)
	}
	return func(yield func(Offset, *Element) bool) {
		for offset, element := range dt.Sparse {
			if !yield(offset, &element) {
				break
			}
		}
	}
}

type ElementType uint8

const (
	ElementIsVacant ElementType = iota << 5
	ElementIsPoints
	ElementIsMarker
	ElementIsFuture
	ElementIsTether
	ElementIsBeizer
	ElementIsSprite
	_ // reserved
)

func (el Element) Type() ElementType {
	return ElementType(el[0] & 0b11100000)
}

func (el *Element) Points() *ElementPoints {
	if el.Type() != ElementIsPoints {
		panic("element is not a sample")
	}
	return (*ElementPoints)(unsafe.Pointer(el))
}

func (el *Element) Marker() *ElementMarker {
	if el.Type() != ElementIsMarker {
		panic("element is not a marker")
	}
	return (*ElementMarker)(unsafe.Pointer(el))
}

func (el *Element) Future() *ElementFuture {
	if el.Type() != ElementIsFuture {
		panic("element is not a future")
	}
	return (*ElementFuture)(unsafe.Pointer(el))
}

func (el *Element) Beizer() *ElementBeizer {
	if el.Type() != ElementIsBeizer {
		panic("element is not a sample")
	}
	return (*ElementBeizer)(unsafe.Pointer(el))
}

func (el *Element) Tether() *ElementTether {
	if el.Type() != ElementIsTether {
		panic("element is not a sample")
	}
	return (*ElementTether)(unsafe.Pointer(el))
}

func (el *Element) Sprite() *ElementSprite {
	if el.Type() != ElementIsSprite {
		panic("element is not a sample")
	}
	return (*ElementSprite)(unsafe.Pointer(el))
}

// Hexagon represents the height and terrain type for
// a hexagon in the world-space.
type Vertex uint64

const vertexHeight = 0x000000000000FFFF

func (v Vertex) Height() int16 {
	bits := uint16(v & vertexHeight)
	return *(*int16)(unsafe.Pointer(&bits))
}

func (v *Vertex) SetHeight(height int16) {
	bits := *(*uint16)(unsafe.Pointer(&height))
	*v = (*v &^ vertexHeight) | Vertex(bits)
}

// Angle represents an angle mapped from 0 to 256.
type Angle uint8

// Height in 1/32 units.
type Height int16

// ElementSample represents a grid 'cell' sample.
type ElementPoints struct {
	Motion int8
	Liquid Upload
	Offset int8
	Cell   Cell
	Upload Upload    // Upload identifies the cell's texture.
	Height [4]Height // top left, top right, bottom left, bottom right
}

func (sample ElementPoints) Element() Element {
	el := *(*Element)(unsafe.Pointer(&sample))
	el[0] |= uint8(ElementIsPoints)
	return el
}

// ElementMarker describes a fixed point within the region.
type ElementMarker struct {
	Pose uint8  // animation number.
	Size uint8  // in 1/2 units
	Link Offset // element with an relationship to this one.
	Cell Cell   // within the area where this view is located.
	Face Angle  // is the direction the view is facing (z axiz).
	Flip Angle  // around the x-axis.
	Spin Angle  // around the z-axis.
	Jump Height // up by the specified height.
	Bump uint8  // offsets the view within the cell by this amount.
	Mesh Upload // identifies the mesh that represents this anchor.
	Time Ticks  // when the anchor is active from.
}

func (marker ElementMarker) Element() Element {
	el := *(*Element)(unsafe.Pointer(&marker))
	el[0] |= uint8(ElementIsMarker)
	return el
}

// ElementTether used to attach one element to another.
type ElementTether struct {
	Type uint8
	Bone uint8  // identifies the bone to link the view onto.
	Onto Offset // Element to link onto
	Next Offset // Next link.
	Jump Height // height.
	Bump uint8  // offsets the next location in the path by this amount.
	Time Ticks  // when the link is active from.`
	_    uint32
}

func (tether ElementTether) Element() Element {
	el := *(*Element)(unsafe.Pointer(&tether))
	el[0] |= uint8(ElementIsTether)
	return el
}

// ElementBeizer can be used to represent curves.
type ElementBeizer struct {
	Walk uint16 // Length to the closest control point in 1/16 units.
	Peer Offset // is the other half of the beizer.
	Jump Height // height.
	Bump uint8  // offsets the next location in the path by this amount.
	Show Ticks  // when the path should be created.
	Mesh Upload // being linked to.
	Face Angle  // is the direction the view is facing (z axiz).
	Cell Cell   // within the area where the path should animate.
	Flip Angle  // around the x-axis.
	Spin Angle  // around the z-axis.
}

func (beizer ElementBeizer) Element() Element {
	el := *(*Element)(unsafe.Pointer(&beizer))
	el[0] |= uint8(ElementIsBeizer)
	return el
}

// ElementFuture represents a future change to a marker.
type ElementFuture struct {
	Pose uint8  // always 0.
	Bump uint8  // offsets the next location in the path by this amount.
	Span Ticks  // how long to reach the future.
	Jump Height // height.
	Face Angle  // around the y-axis.
	Lerp uint8  // linear, quadratic, cubic, etc.
	Loop Ticks  // double ticks used to represent when the path should loop.
	Flip Angle  // around the x-axis.
	Spin Angle  // around the z-axis.
	Area Region // where the path should animate.
	Cell Cell   // within the area where the path should animate.
}

func (future ElementFuture) Element() Element {
	el := *(*Element)(unsafe.Pointer(&future))
	el[0] |= uint8(ElementIsFuture)
	return el
}

// ElementSprite represents a sprite.
type ElementSprite struct {
	Fade uint8    // transparency
	Tint [3]uint8 // color
	Cell Cell     // within the area where the sprite is located.
	Bump uint8    // offsets the sprite within the cell by this amount.
	Turn uint8    // around the y-axis.
	Cuts uint8    // atlas
	Uses uint8    // atlas index
	Icon Upload   // identifies the sprite.
	Time Ticks    // when the sprite is active from.
	Next Offset   // Next sprite.
}

func (sprite ElementSprite) Element() Element {
	el := *(*Element)(unsafe.Pointer(&sprite))
	el[0] |= uint8(ElementIsSprite)
	return el
}
