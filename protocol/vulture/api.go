// Package vulture provides an API for collaborative and creative community spaces.
package vulture

import (
	"context"
	"io/fs"
	"unsafe"

	"runtime.link/api"
	"runtime.link/nix"
)

// API specification.
type API struct {
	api.Specification `api:"Vulture"
		provides access to collaborative and creative community spaces.
	`
	Vision func(context.Context, Vision) error `http:"POST /vulture/v0/vision"
		returns channels used to focus on and view the world.`
	Upload func(context.Context, Upload) (fs.File, error) `http:"GET /vulture/v0/upload/{upload=%v}"
		a file to the world.`
	Lookup func(context.Context, fs.File) (Upload, error) `http:"POST /vulture/v0/lookup"
		a file from the world.`
	Target func(context.Context, Target) error `http:"POST /vulture/v0/target"
		selects a target for the client to focus on.`
	Uplift func(context.Context, Uplift) ([]Territory, error) `http:"POST /vulture/v0/uplift"
		can be used to modify the surface of the world, and/or to control vision.`
	Raptor func(context.Context) (chan<- Raptor, error) `http:"GET /vulture/v0/raptor"
		is used to add a new view to the world.`
	Events func(context.Context) (<-chan Vision, error) `http:"GET /vulture/v0/events"
		can be used to animate a view.`
}

type Raptor struct {
	Pos   [3]float64
	Roll  float64
	Pitch float64
	Yaw   float64
}

type Territory struct {
	Area     Area            `json:"area"`
	Vertices [16 * 16]Vertex `json:"vertices"`
}

// Upload identifier.
type Upload uint16

// Area of the world.
type Area [2]int16

// Cell within an area.
type Cell uint8

// Ticks in 1/10 second intervals.
type Ticks uint16

// LookAt represents the client's current focus.
type LookAt struct {
	Area Area `json:"area"
		in the world to focus on.`
	Size uint8 `json:"size"
		of the area to focus on.`
}

// Escort a named view along a path.
type Escort struct {
	Name Name   `json:"name"`
	Walk []Path `json:"walk"`
	Loop bool   `json:"loop"`
}

type Target struct {
	Name Name `json:"name"`
}

// Focus represents the focal point of the client.
type Name uint16

// Uplift to apply to the surface of the world.
type Uplift struct {
	Time nix.Nanos `json:"time"`
	Area Area      `json:"area"`
	Cell Cell      `json:"cell"`
	Size uint8     `json:"size"`
	Lift int8      `json:"lift"`
}

// Vision represents an update to what the client can see.
type Vision struct {
	Period nix.Nanos          `json:"period"`
	Packet nix.Nanos          `json:"packet"`
	Region Area               `json:"region"`
	Screen bool               `json:"screen"`
	Offset int                `json:"offset"`
	Packed Elements           `json:"packed"`
	Sparse map[uint16]Element `json:"sparse"`
}

// Element can either be a [Thing], [Anime] or ?
type Element [16]byte
type Elements []byte

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

// Direction represents an angle mapped from 0 to 256.
type Direction uint8

type Render struct {
	Area Area
	Cell Cell
	Mesh Upload
}

type Tile struct {
	Height uint16 `json:"height"`
	Liquid uint8  `json:"liquid"`
	Cell   uint8
	Biome  uint16
	Show   Ticks `json:"show"
		when the view should be created.`
}

type View struct {
	Cell Cell `json:"cell"
		within the area where this view is located.`
	Face Direction `json:"face"
		is the direction the view is facing.`
	Jump int16 `json:"jump"
		up by the specified amount.`
	Bump uint8 `json:"bump"
		offsets the view within the cell by this amount,
		treat this as a nested cell.`
	Name Name `json:"name"
		of the view, used to identify the view across
		both temporal and territorial boundaries.`
	Mesh Upload `json:"mesh"
		identifies the mesh to use for the view.`
	Icon Upload `json:"icon"
		identifies the icon to use for the view.`
	Show Ticks `json:"show"
		when the view should be created.`
	Hide Ticks `json:"hide"
		when the view should be removed.`
}

type Link struct {
	View uint16 `json:"view"
		being linked.`
	Onto uint16 `json:"onto"
		being linked to.`
	Bone uint8 `json:"bone"
		identifies the bone to link the view onto.`
	Jump uint8 `json:"jump"
		height.`
	Bump uint8 `json:"bump"
		offsets the next location in the path by this amount.`
	_    uint8
	Show Ticks `json:"show"
		when the link should be created.`
	Hide Ticks `json:"hide"
		when the link should be removed.`
	_ uint32
}

type Path struct {
	Peer uint16 `json:"view"
		is the other side of the path.`
	Jump int16
	_    uint8 // always zero (Bone)
	Bump uint8 `json:"bump"
		offsets the next location in the path by this amount.`
	Show Ticks `json:"show"
		when the path should be created.`
	Hide Ticks `json:"hide"
		when the path should be removed.`
	Mesh Upload `json:"onto"
			being linked to.`
	Face uint8
	Walk uint8
	Cell Cell `json:"cell"
		identifies the bone to link the view onto.`
	Anim uint8
}

// Node represents either a single keyframe for the animation, an attachment
// of one render to another, or a spatial curve.
type Node struct {
	View uint16 `json:"view"
		identifies the view to animate`
	Span Ticks `json:"span"
		how long the path should take to animate.`
	Jump uint8 `json:"jump"
		height.`
	Bump uint8 `json:"bump"
		offsets the next location in the path by this amount.`
	From Ticks `json:"from"
		when the path should start animating.`
	Loop Ticks `json:"loop"
		how long the path should wait before animating again.`
	Area Area `json:"area"
		where the path should animate.`
	Cell Cell `json:"cell"
		within the area where the path should animate.`
	_ uint8
}
