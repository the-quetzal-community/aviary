package musical

import (
	"encoding/binary"
	"errors"
	"io"
	"io/fs"
	"reflect"

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

// Record identifies a particular instance of a [UsersSpace3D].
type Record [16]byte

// UsersSpace3D (.mus3) represents creative contributions to a shared 3D space.
type UsersSpace3D interface {

	// Member is used to record authorship and as a caching key for uploads.
	// If [Requirements.Assign] is observed to be true, the corresponding
	// [Requirements.Author] should be used as the author in operations.
	Member(Orchestrator) error

	// Upload replaces a [Design] with the contents of a file. The design must be
	// within the associated author's memory requirements, else this is a noop.
	Upload(DesignUpload) error

	// Sculpt an area of the scene, using the given [Design] as a 'brush', the design
	// must be within the associated author's memory requirements, else this is a noop.
	Sculpt(AreaToSculpt) error

	// Import a design using its URI reference, the design must be within the associated
	// author's memory requirements, else this is a noop.
	Import(DesignImport) error

	// Create contributions to the scene, the entity and design must be within the
	// associated author's memory requirements, else this is a noop.
	Create(Contribution) error

	// Attach two entities together with the specified relationship, both entities must be
	// within the associated author's memory requirements, else this is a noop.
	Attach(Relationship) error

	// LookAt record's the author's perspective and viewpoint.
	LookAt(BirdsEyeView) error
}

type structOrchestrator uint16

const (
	structOrchestratorRecord structOrchestrator = 1 << iota
	structOrchestratorNumber
	structOrchestratorAuthor
	structOrchestratorAssign
)

type Orchestrator struct {
	Record Record // expected identifier for the user/scene.
	Number uint64 // a number of instructions observed for the scene.
	Author Author // author for the receiver to exclusively adopt.
	Assign bool   // if true, the receiver should adopt the specified [Author].
}

type DesignImport struct {
	Design Design // design to overwrite.
	Import string // URI of the design.
}

type DesignUpload struct {
	Design Design  // design to overwrite.
	Upload fs.File // file containing the design data.
}

type Contribution struct {
	Author Author        // author making the contribution.
	Entity Entity        // contribute to the entity
	Design Design        // design to add to the entity.
	Offset Vector3.XYZ   // position of the entity within the scene.
	Bounds Vector3.XYZ   // size of the entity within the scene.
	Angles Euler.Radians // orientation of the entity within the scene.
	Colour Color.RGBA    // colour tint of the entity within the scene.
	Timing int64         // time offset for the contribution, in milliseconds.
	Remove bool          // if true, remove the design fom the entity, instead of adding it.
	Tweens bool          // if true, the contribution represents tweening velocities.
	Commit bool          // if false, then this is a preview (not persisted).
}

type Relationship struct {
	Author Author // author making the contribution.
	Entity Entity // child Entity
	Parent Entity // parent Entity
	Attach bool   // if true, attach; if false, detach.
	Follow bool   // if true, the child follows the parent, else the child is relative to the parent.
	Commit bool   // if false, then this is a preview (not persisted).
}

type AreaToSculpt struct {
	Author Author      // author making the contribution.
	Design Design      // design used as a 'brush' for sculpting.
	Target Vector3.XYZ // center point of the area to sculpt.
	Radius Float.X     // radius of the area to sculpt.
	Amount Float.X     // amount to sculpt, ie. its strength.
	Commit bool        // if false, then this is a preview.
}

type BirdsEyeView struct {
	Author Author        // author whose viewpoint is being recorded.
	Design Design        // design representing the author.
	Offset Vector3.XYZ   // position of the author.
	Angles Euler.Radians // orientation of the author.
	Bounds Vector3.XYZ   // size of the author.
	Colour Color.RGBA    // colour of the author.
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

func (Orchestrator) entryType() entryType { return entryTypeMember }
func (DesignImport) entryType() entryType { return entryTypeImport }
func (DesignUpload) entryType() entryType { return entryTypeUpload }
func (Contribution) entryType() entryType { return entryTypeCreate }
func (Relationship) entryType() entryType { return entryTypeAttach }
func (AreaToSculpt) entryType() entryType { return entryTypeSculpt }
func (BirdsEyeView) entryType() entryType { return entryTypeLookAt }
func (orc Orchestrator) validateAuthor(author Author) bool {
	return !orc.Assign && orc.Author == author
}
func (di DesignImport) validateAuthor(author Author) bool  { return true }
func (du DesignUpload) validateAuthor(author Author) bool  { return true }
func (con Contribution) validateAuthor(author Author) bool { return con.Author == author }
func (rel Relationship) validateAuthor(author Author) bool { return rel.Author == author }
func (ats AreaToSculpt) validateAuthor(author Author) bool { return ats.Author == author }
func (bev BirdsEyeView) validateAuthor(author Author) bool { return bev.Author == author }

func encode(v encodable) (buf []byte, err error) {
	rvalue := reflect.ValueOf(v)
	var layout uint16
	for i := 0; i < rvalue.NumField(); i++ {
		if !rvalue.Field(i).IsZero() {
			layout |= 1 << uint16(i)
		}
	}
	buf = append(buf, uint8(v.entryType()))
	buf = binary.LittleEndian.AppendUint16(buf, layout)
	for i := 0; i < rvalue.NumField(); i++ {
		if layout&(1<<uint16(i)) == 0 {
			continue
		}
		switch rvalue.Field(i).Kind() {
		case reflect.Bool:
		case reflect.String:
			str := rvalue.Field(i).String()
			strLen := uint16(len(str))
			buf = binary.LittleEndian.AppendUint16(buf, strLen)
			buf = append(buf, []byte(str)...)
		case reflect.Interface:
		default:
			buf, err = binary.Append(buf, binary.LittleEndian, rvalue.Field(i).Interface())
			if err != nil {
				return nil, xray.New(err)
			}
		}
	}
	return buf, nil
}

func decodeT[T encodable](r io.Reader) (T, error) {
	v, err := decode(r)
	if err != nil {
		return [1]T{}[0], err
	}
	asserted, ok := reflect.TypeAssert[T](reflect.ValueOf(v))
	if !ok {
		return [1]T{}[0], errors.New("decoded type does not match expected type")
	}
	return asserted, nil
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
	var v reflect.Value
	switch et {
	case entryTypeMember:
		v = reflect.New(reflect.TypeOf(Orchestrator{})).Elem()
	case entryTypeImport:
		v = reflect.New(reflect.TypeOf(DesignImport{})).Elem()
	case entryTypeUpload:
		v = reflect.New(reflect.TypeOf(DesignUpload{})).Elem()
	case entryTypeCreate:
		v = reflect.New(reflect.TypeOf(Contribution{})).Elem()
	case entryTypeAttach:
		v = reflect.New(reflect.TypeOf(Relationship{})).Elem()
	case entryTypeSculpt:
		v = reflect.New(reflect.TypeOf(AreaToSculpt{})).Elem()
	case entryTypeLookAt:
		v = reflect.New(reflect.TypeOf(BirdsEyeView{})).Elem()
	default:
		return nil, nil
	}
	for i := 0; i < v.NumField(); i++ {
		if layout&(1<<uint16(i)) == 0 {
			continue
		}
		field := v.Field(i)
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
	asserted, _ := reflect.TypeAssert[encodable](v)
	return asserted, nil
}
