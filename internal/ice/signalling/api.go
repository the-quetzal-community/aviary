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

	LookupUser func(context.Context) (string, error)                          `rest:"GET /account user_id"`
	CreateRoom func(context.Context, webrtc.SessionDescription) (Code, error) `rest:"POST /room (offer) code"`
	UpdateRoom func(context.Context, Code, webrtc.SessionDescription) error   `rest:"PUT /room/{code=%v}/offer"`
	ListenRoom func(context.Context, Code) (chan Message, error)              `rest:"GET /room/{code=%v}"`

	LookupRoom func(context.Context, Code) (webrtc.SessionDescription, error) `rest:"GET /room/{code=%v}/offer"`
	AnswerRoom func(context.Context, Code, webrtc.SessionDescription) error   `rest:"POST /room/{code=%v}/answer"`
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
