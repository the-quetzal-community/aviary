package signalling

import (
	"context"
	"time"

	"runtime.link/api"
	"runtime.link/xyz"

	"the.quetzal.community/aviary/internal/ice"
)

type API struct {
	api.Specification

	LookupUser func(context.Context) (User, error) `rest:"GET /account"`
}

type User struct {
	ID            string    `json:"user_id"`
	TogetherUntil time.Time `json:"together_until"`
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
