package signalling

import (
	"context"

	"runtime.link/api"
	"runtime.link/xyz"

	"the.quetzal.community/aviary/internal/ice"
)

type API struct {
	api.Specification

	LookupUser func(context.Context) (string, error) `rest:"GET /account user_id"`
}

type Code string

type Message struct {
	Type MessageType  `json:"type"`
	Data []ice.Server `json:"data,omitempty"`
}

type MessageType xyz.Switch[string, struct {
	IceServers MessageType `json:"ice-servers"`
}]

var MessageTypes = xyz.AccessorFor(MessageType.Values)
