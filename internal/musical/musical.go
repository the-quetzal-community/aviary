package musical

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"reflect"
	"time"

	"graphics.gd/variant/Angle"
	"graphics.gd/variant/Color"
	"graphics.gd/variant/Euler"
	"graphics.gd/variant/Float"
	"graphics.gd/variant/Vector3"
	"runtime.link/api/xray"
)

// Entity is a design placed within a musical user's 3D scene.
type Entity struct {
	Author Author
	Number uint16
}

// Design within a musical user's 3D scene.
type Design struct {
	Author Author
	Number uint16
}

// Author of contributions within a musical user's 3D scene.
type Author uint16

// WorkID identifies a particular instance of a [UsersSpace3D].
type WorkID [16]byte

// Record indentifier.
type Record struct {
	Author Author
	Number uint16
}

type (
	Timing int64
	Period time.Duration
)

// UsersSpace3D (.mus3) represents creative contributions to a shared 3D space.
type UsersSpace3D interface {

	// Member is used to record authorship and as a caching key for uploads.
	// If [Requirements.Assign] is observed to be true, the corresponding
	// [Requirements.Author] should be used as the author in operations.
	Member(Member) error

	// Upload replaces a [Design] with the contents of a file. The design must be
	// within the associated author's memory requirements, else this is a noop.
	Upload(Upload) error

	// Sculpt an area of the scene, using the given [Design] as a 'brush', the design
	// must be within the associated author's memory requirements, else this is a noop.
	Sculpt(Sculpt) error

	// Import a design using its URI reference, the design must be within the associated
	// author's memory requirements, else this is a noop.
	Import(Import) error

	// Change the scene, the entity and design must be within the
	// associated author's memory requirements, else this is a noop.
	Change(Change) error

	// Action requests an entity within the scene to take an action, the entity must be
	// within the associated author's memory requirements, else this is a noop.
	Action(Action) error

	// LookAt record's the author's perspective and viewpoint.
	LookAt(LookAt) error
}

type Member struct {
	Record WorkID // expected identifier for the user/scene.
	Number uint64 // a number of instructions observed for the scene.
	Author Author // author for the receiver to exclusively adopt.
	Server string // server identifier.

	Assign bool // if true, the receiver should adopt the specified [Author].
}

type Import struct {
	Design Design // design to overwrite.
	Import string // URI of the design.
}

type Upload struct {
	Design Design  // design to overwrite.
	Upload fs.File // file containing the design data.
}

type Change struct {
	Author Author        // author making the contribution.
	Entity Entity        // contribute to the entity
	Design Design        // design to add to the entity.
	Offset Vector3.XYZ   // position of the entity within the scene.
	Bounds Vector3.XYZ   // size of the entity within the scene.
	Angles Euler.Radians // orientation of the entity within the scene.
	Colour Color.RGBA    // colour tint of the entity within the scene.
	Speeds Speeds        // when taking actions

	Record Record // to record.
	Timing Timing // timing of the record.

	Editor string // editor that is being used.

	// Mirror the entity, the difference between this and the Offset is
	// used to determine the axis that is being mirrored on and angles
	// will be inverted accordingly.
	Mirror Vector3.XYZ

	Remove bool // if true, removes the design/record from the entity.
	Commit bool // if false, then this is a preview (not persisted).
}

type Speeds struct {
	Offset Float.X
	Angles Float.X
}

type Action struct {
	Author Author      // author making the contribution.
	Entity Entity      // entity taking the action.
	Target Vector3.XYZ // target position, in global space.
	Timing Timing      // time of the action.
	Period Period      // duration of the action.

	Design Design // design to apply to the entity for the period of the action.
	Record Record // to playback.

	Editor string // editor that is being used.

	Cancel bool // if true, clears any previous actions.
	Repeat bool // if true, any subsequently queued, repeating actions will cycle in alternate directions.
	Commit bool // if false, then this is a preview (not persisted).
}

type Sculpt struct {
	Author Author      // author making the contribution.
	Design Design      // design used as a 'brush' for sculpting.
	Target Vector3.XYZ // center point of the area to sculpt.
	Radius Float.X     // radius of the area to sculpt.
	Amount Float.X     // amount to sculpt, ie. its strength.

	Editor string // editor that is being used.
	Slider string // slider that is being adjusted.
	Timing Timing // timing of the sculpt.

	Orient Angle.Radians
	Random int64 // entropy

	Commit bool // if false, then this is a preview.
	Revert bool // revert this sculpt.
}

type LookAt struct {
	Author Author        // author whose viewpoint is being recorded.
	Design Design        // design representing the author.
	Offset Vector3.XYZ   // position of the author.
	Angles Euler.Radians // orientation of the author.
	Bounds Vector3.XYZ   // size of the author.
	Colour Color.RGBA    // colour of the author.
	Editor string        // editor that is being used.
	Timing Timing        // timing of the viewer.
}

type entryType uint8

const (
	entryTypeMember entryType = iota + 1
	entryTypeUpload
	entryTypeSculpt
	entryTypeImport
	entryTypeCreate
	entryTypeAttach
	entryTypeLookAt
)

type encodable interface {
	entryType() entryType
	validateAuthor(Author) bool
}

func (Member) entryType() entryType { return entryTypeMember }
func (Import) entryType() entryType { return entryTypeImport }
func (Upload) entryType() entryType { return entryTypeUpload }
func (Change) entryType() entryType { return entryTypeCreate }
func (Action) entryType() entryType { return entryTypeAttach }
func (Sculpt) entryType() entryType { return entryTypeSculpt }
func (LookAt) entryType() entryType { return entryTypeLookAt }
func (orc Member) validateAuthor(author Author) bool {
	return !orc.Assign && orc.Author == author
}
func (di Import) validateAuthor(author Author) bool  { return true }
func (du Upload) validateAuthor(author Author) bool  { return true }
func (con Change) validateAuthor(author Author) bool { return con.Author == author }
func (rel Action) validateAuthor(author Author) bool { return rel.Author == author }
func (ats Sculpt) validateAuthor(author Author) bool { return ats.Author == author }
func (bev LookAt) validateAuthor(author Author) bool { return bev.Author == author }

// encode/decode pack a struct's "which fields are present" mask
// into a uint16 layout word.
//
// Non-bool fields claim bit `i` (their struct index, counting up
// from 0). Bool fields claim bit `(1 << 14) >> j` where `j` is the
// bool counter — i.e. the FIRST bool in the struct uses bit 14,
// the second uses bit 13, etc. This is the original design intent
// — pack bools into high bits so they stay out of non-bool
// territory — but the legacy implementation slipped and used the
// struct field index in the bool formula `(1 << 15) >> i` instead
// of the bool counter. With 12 non-bool fields preceding two bools
// (Change.Remove at struct index 12, Commit at 13) the legacy
// formula mapped them onto bits 3 and 2 — exactly where Offset
// and Design were already living. Result: every persisted Add
// silently decoded as a Remove.
//
// Bit 15 is reserved as a format marker. Legacy records never set
// it (no struct had a bool at struct index 0, which is the only
// way the old formula could have produced bit 15); new records
// always set it, so the decoder can branch and apply the legacy
// collision-fixup when reading older `.mu3s` files.
const (
	newFormatMarker uint16 = 1 << 15
	boolBitTop      uint16 = 14 // first bool occupies bit 14, then 13, …
)

func encode(v encodable) (buf []byte, err error) {
	rvalue := reflect.ValueOf(v)
	layout := newFormatMarker
	boolN := uint16(0)
	for i := 0; i < rvalue.NumField(); i++ {
		if rvalue.Field(i).IsZero() {
			if rvalue.Field(i).Kind() == reflect.Bool {
				boolN++
			}
			continue
		}
		if rvalue.Field(i).Kind() == reflect.Bool {
			layout |= 1 << (boolBitTop - boolN)
			boolN++
		} else {
			layout |= 1 << uint16(i)
		}
	}
	buf = append(buf, uint8(v.entryType()))
	buf = binary.LittleEndian.AppendUint16(buf, layout)
	boolN = 0
	for i := 0; i < rvalue.NumField(); i++ {
		field := rvalue.Field(i)
		if field.Kind() == reflect.Bool {
			set := layout&(1<<(boolBitTop-boolN)) != 0
			boolN++
			_ = set // bools carry no payload; bit alone is the value
			continue
		}
		if layout&(1<<uint16(i)) == 0 {
			continue
		}
		switch field.Kind() {
		case reflect.String:
			str := field.String()
			strLen := uint16(len(str))
			buf = binary.LittleEndian.AppendUint16(buf, strLen)
			buf = append(buf, []byte(str)...)
		case reflect.Interface:
		default:
			buf, err = binary.Append(buf, binary.LittleEndian, field.Interface())
			if err != nil {
				return nil, xray.New(err)
			}
		}
	}
	return buf, nil
}

func decode(r io.Reader) (encodable, error) {
	var et entryType
	if err := binary.Read(r, binary.LittleEndian, &et); err != nil {
		return nil, xray.New(err)
	}
	var layout uint16
	if err := binary.Read(r, binary.LittleEndian, &layout); err != nil {
		return nil, xray.New(err)
	}
	// Discriminate new vs legacy format. New records set bit 15;
	// legacy records never set it (no struct has a bool at index 0,
	// which is the only field that would have populated bit 15 in
	// the old scheme).
	newFormat := layout&newFormatMarker != 0
	layout &^= newFormatMarker
	var v reflect.Value
	switch et {
	case entryTypeMember:
		v = reflect.New(reflect.TypeOf(Member{})).Elem()
	case entryTypeImport:
		v = reflect.New(reflect.TypeOf(Import{})).Elem()
	case entryTypeUpload:
		v = reflect.New(reflect.TypeOf(Upload{})).Elem()
	case entryTypeCreate:
		v = reflect.New(reflect.TypeOf(Change{})).Elem()
	case entryTypeAttach:
		v = reflect.New(reflect.TypeOf(Action{})).Elem()
	case entryTypeSculpt:
		v = reflect.New(reflect.TypeOf(Sculpt{})).Elem()
	case entryTypeLookAt:
		v = reflect.New(reflect.TypeOf(LookAt{})).Elem()
	default:
		return nil, xray.New(errors.New("unknown entry type " + fmt.Sprint(et)))
	}
	boolN := uint16(0)
	for i := 0; i < v.NumField(); i++ {
		field := v.Field(i)
		var bitset bool
		if field.Kind() == reflect.Bool {
			if newFormat {
				bitset = layout&(1<<(boolBitTop-boolN)) != 0
			} else {
				// Legacy formula used the struct field index where it
				// should have used the bool counter, collapsing bool
				// bits into the non-bool range — that's the collision.
				bitset = layout&((1<<15)>>uint16(i)) != 0
			}
			boolN++
		} else {
			bitset = layout&(1<<uint16(i)) != 0
		}
		if !bitset {
			continue
		}
		switch field.Kind() {
		case reflect.Bool:
			field.SetBool(true)
		case reflect.String:
			var strLen uint16
			if err := binary.Read(r, binary.LittleEndian, &strLen); err != nil {
				return nil, xray.New(err)
			}
			data := make([]byte, strLen)
			if _, err := io.ReadFull(r, data); err != nil {
				return nil, xray.New(err)
			}
			field.SetString(string(data))
		case reflect.Interface:
		default:
			err := binary.Read(r, binary.LittleEndian, field.Addr().Interface())
			if err != nil {
				return nil, xray.New(err)

			}
		}
	}
	if !newFormat {
		fixLegacyBoolCollisions(v)
	}
	asserted, _ := reflect.TypeAssert[encodable](v)
	return asserted, nil
}

// fixLegacyBoolCollisions repairs the bool/non-bool bit collision
// in legacy records. For each bool field at index i_bool, the
// non-bool field at index `15 - i_bool` shares the layout bit. If
// that non-bool field decoded with a meaningful (non-zero) value,
// the bool was almost certainly a phantom set by the collision —
// our writers never deliberately mixed both (e.g. a Change is
// either an Add with Offset and Commit, or a Remove with neither).
// The heuristic is safe for the only structs that have bool fields
// in collision range: Change, Action, Sculpt.
func fixLegacyBoolCollisions(v reflect.Value) {
	n := v.NumField()
	for i := 0; i < n; i++ {
		field := v.Field(i)
		if field.Kind() != reflect.Bool || !field.Bool() {
			continue
		}
		partner := 15 - i
		if partner < 0 || partner >= n {
			continue
		}
		pf := v.Field(partner)
		if pf.Kind() == reflect.Bool {
			continue
		}
		if !pf.IsZero() {
			field.SetBool(false)
		}
	}
}
