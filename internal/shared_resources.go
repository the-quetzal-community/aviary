package internal

import (
	"reflect"
	"strconv"
	"strings"

	"graphics.gd/classdb/Input"
	"graphics.gd/classdb/InputEventKey"
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/PackedScene"
	"graphics.gd/classdb/Texture2D"
	"the.quetzal.community/aviary/internal/musical"
)

// isDeletePress is true for a non-echo, pressed-state key event whose
// keycode is Delete or Backspace. Shared by every editor's
// remove-selected-entity handler.
func isDeletePress(event InputEventKey.Instance) bool {
	if !event.AsInputEvent().IsPressed() || event.AsInputEvent().IsEcho() {
		return false
	}
	code := event.Keycode()
	return code == Input.KeyDelete || code == Input.KeyBackspace
}

// SharedResources is a singleton responsible for coordinating resource caching and entities for
// a [musical.UsersScene3D] instance.
type SharedResources struct {
	entity_ids map[musical.Author]uint16
	design_ids map[musical.Author]uint16

	design_to_entity map[musical.Design][]Node3D.ID
	entity_to_object map[musical.Entity]Node3D.ID
	object_to_entity map[Node3D.ID]musical.Entity

	packed_scenes    map[musical.Design]PackedScene.ID
	textures         map[musical.Design]Texture2D.ID
	design_to_string map[musical.Design]string
	loaded           map[string]musical.Design
}

func (client *Client) MusicalDesign(resource string) musical.Design {
	design, ok := client.loaded[resource]
	if !ok {
		client.design_ids[client.id]++
		design = musical.Design{
			Author: client.id,
			Number: client.design_ids[client.id],
		}
		client.space.Import(musical.Import{
			Design: design,
			Import: resource,
		})
	}
	return design
}

// NextEntity reserves the next Entity id authored by this client and
// returns the full musical.Entity. Replaces the
// `client.entity_ids[client.id]++ ; Entity{Author, Number}` pattern
// every placement editor repeated inline.
func (client *Client) NextEntity() musical.Entity {
	client.entity_ids[client.id]++
	return musical.Entity{
		Author: client.id,
		Number: client.entity_ids[client.id],
	}
}

// applyReflectSlider finds the gd-tagged field `prop` on container
// (a *T pointer) and stores value into it, then calls regenerate.
// Returns true if a matching field was found. Shared by the
// procedural editors (foliage/boulder) whose Sculpt handlers all
// reach into a struct via reflection.
func applyReflectSlider(container any, rtype reflect.Type, prop string, value float64, regenerate func()) bool {
	for i := 0; i < rtype.NumField(); i++ {
		field := rtype.Field(i)
		if field.Tag.Get("gd") != prop {
			continue
		}
		v := reflect.ValueOf(container).Elem().Field(i)
		switch field.Type.Kind() {
		case reflect.Int:
			v.SetInt(int64(value))
		case reflect.Float32, reflect.Float64:
			v.SetFloat(value)
		default:
			return false
		}
		regenerate()
		return true
	}
	return false
}

// reflectSliderConfig reads the gd-tagged field `prop` on rtype and
// returns the slider bounds derived from its `default` and `range`
// struct tags. ok=false when no matching field exists; caller fills
// in its own defaults.
func reflectSliderConfig(rtype reflect.Type, prop string) (init, from, upto, step float64, ok bool) {
	for i := 0; i < rtype.NumField(); i++ {
		field := rtype.Field(i)
		if field.Tag.Get("gd") != prop {
			continue
		}
		init, _ = strconv.ParseFloat(field.Tag.Get("default"), 64)
		ranges := strings.Split(field.Tag.Get("range"), ",")
		if len(ranges) >= 2 {
			from, _ = strconv.ParseFloat(ranges[0], 64)
			upto, _ = strconv.ParseFloat(ranges[1], 64)
		}
		step = 0.001
		if field.Type.Kind() == reflect.Int {
			step = 1
		}
		return init, from, upto, step, true
	}
	return 0, 0, 0, 0, false
}
