package signalling

import (
	"context"
	"encoding/json"

	"runtime.link/api"
	"runtime.link/xyz"

	"the.quetzal.community/aviary/internal/ice"
)

type API struct {
	api.Specification

	IceServers func(context.Context, Code) ([]ice.Server, error) `rest:"GET /ice-servers?room=%v"`

	CreateRoom func(context.Context, string) (Code, error)       `rest:"POST /room (offer) code"`
	ListenRoom func(context.Context, Code) (chan Message, error) `rest:"GET /room/%v"`
}

type Code string

type Message struct {
	Type      MessageType     `json:"type"`
	Role      MessageRole     `json:"role,omitzero"`
	SDP       json.RawMessage `json:"sdp,omitempty"`
	Candidate json.RawMessage `json:"candidate,omitempty"`
	Message   string          `json:"message,omitempty"`
}

type MessageType xyz.Switch[string, struct {
	Join      MessageType `json:"join"`
	Answer    MessageType `json:"answer"`
	Candidate MessageType `json:"candidate"`
	Error     MessageType `json:"error" default:"error"`
}]

var MessageTypes = xyz.AccessorFor(MessageType.Values)

type MessageRole xyz.Switch[string, struct {
	Host  MessageRole `json:"host"`
	Guest MessageRole `json:"guest"`
}]

var MessageRoles = xyz.AccessorFor(MessageRole.Values)
