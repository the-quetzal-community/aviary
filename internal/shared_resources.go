package internal

import (
	"graphics.gd/classdb/Node3D"
	"graphics.gd/classdb/PackedScene"
	"graphics.gd/classdb/Texture2D"
	"the.quetzal.community/aviary/internal/musical"
)

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
