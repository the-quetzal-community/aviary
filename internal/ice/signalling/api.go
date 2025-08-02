package signalling

import (
	"context"

	"github.com/pion/webrtc/v4"
	"runtime.link/api"
	"runtime.link/xyz"

	"the.quetzal.community/aviary/internal/ice"
)

type API struct {
	api.Specification

	CreateRoom func(context.Context, webrtc.SessionDescription) (Code, error) `rest:"POST /room (offer) code"`
	ListenRoom func(context.Context, Code) (chan Message, error)              `rest:"GET /room/%v"`
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
