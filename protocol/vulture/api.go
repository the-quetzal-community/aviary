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
	Vision func(context.Context) (<-chan Vision, error) `http:"GET /vulture/v0/vision"
		returns channels used to focus on and view the world.`
	Upload func(context.Context, Upload, fs.File) error `http:"PUT /vulture/v0/upload/{design=%v}"
		a file to the world.`
	Lookup func(context.Context, Upload) (fs.File, error) `http:"GET /vulture/v0/upload/{design=%v}"
		a file from the world.`
	Target func(context.Context, Target) error `http:"PUT /vulture/v0/target"
		selects a target for the client to focus on.`
	Uplift func(context.Context, Uplift) ([]Territory, error) `http:"POST /vulture/v0/liftup"
		can be used to modify the surface of the world, and/or to control vision.`
	Render func(context.Context, Render) error `http:"POST /vulture/v0/render"
		is used to add a new view to the world.`
	Escort func(context.Context, Escort) error `http:"POST /vulture/v0/escort"
		can be used to animate a view.`
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

type Path struct {
	Area Area `json:"area"`
	Cell Cell `json:"cell"`
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
	Time nix.Nanos  `json:"time"`
	Area Area       `json:"area"`
	View []Render   `json:"view,omitempty"`
	Node []Node     `json:"node,omitempty"`
	Chat []Chat     `json:"chat,omitempty"`
	User *Interface `json:"info,omitempty"`
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

// Direction represents an angle mapped from 0 to 256.
type Direction uint8

type Render struct {
	Area Area
	Cell Cell
	Mesh Upload
}

type View struct {
	Cell Cell `json:"cell"
		within the area where this view is located.`
	Face Direction `json:"face"
		is the direction the view is facing.`
	Size uint8 `json:"size"
		of the view.`
	Jump uint8 `json:"jump"
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

// Node represents either a single keyframe for the animation, an attachment
// of one render to another, or a spatial curve.
type Node struct {
	View uint16 `json:"view"
		identifies the view to animate`
	Jump uint8 `json:"jump"
		height.`
	Bump uint8 `json:"bump"
		offsets the next location in the path by this amount.`
	From Ticks `json:"from"
		when the path should start animating.`
	Span Ticks `json:"span"
		how long the path should take to animate.`
	Loop Ticks `json:"loop"
		how long the path should wait before animating again.`
	Area Area `json:"area"
		where the path should animate.`
	Cell Cell `json:"cell"
		within the area where the path should animate.`
}

// Interface available to the user.
type Interface struct {
	Name string `json:"name"
		of the view.`
	Icon Upload `json:"icon"
		is the primary icon for the view.`
	Flag []Upload `json:"flag"
		in order of importance.`
	Fact []Fact `json:"fact"
		known about the view.`
	Stat []Stat `json:"stat"
		information that may change over time.`
	Menu []Menu `json:"menu"
		of actions that can be taken.`
	Note string `json:"note"
		about the view.`
}

// Menu within an interface.
type Menu struct {
	Name string `json:"name"
		of the action.`
	Icon Upload `json:"icon"
		that represents the action.`
	Hint string `json:"hint"
		about the action.`
	List []Item `json:"list"
		of items that can be added.`
}

// Fact about a view.
type Fact struct {
	Name string `json:"name"
		of the fact.`
	Icon Upload `json:"icon"
		that represents the fact.`
	Text string `json:"text"
		of the fact.`
}

// Item within a menu.
type Item struct {
	Name string `json:"name"
		of the item.`
	Icon Upload `json:"icon"
		that represents the item.`
	Hint string `json:"hint"
		about the item.`
	Mesh Upload `json:"mesh"
		that can be rendered into the world.`
}

// Stat about a view.
type Stat struct {
	Name string `json:"name"
		of the stat.`
	Icon Upload `json:"icon"
		that represents the stat.`
	Hint string `json:"hint"
		about the stat.`
	Plot []Plot `json:"plot"
		of the stat.`
}

// Plot of a stat over time.
type Plot struct {
	From Ticks `json:"from"
		when the data starts.`
	Span Ticks `json:"span"
		of the data.`
	Data float64 `json:"data"
		value of the data.`
}

// Chat message.
type Chat struct {
	Name string `json:"name"
		of the chat.`
	Icon Upload `json:"icon"
		that represents the chat.`
	Text string `json:"text"
		of the chat.`
}
