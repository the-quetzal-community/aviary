package signalling

import (
	"context"
	"io"
	"time"

	"runtime.link/api"
	"runtime.link/xyz"

	"the.quetzal.community/aviary/internal/ice"
)

type API struct {
	api.Specification

	LookupUser func(context.Context) (User, error) `rest:"GET /account"`

	CloudSaves func(context.Context) ([]WorkID, error)                `rest:"GET /saves"`
	CloudParts func(context.Context, WorkID) (map[PartID]Part, error) `rest:"GET /saves/{work_id=%v}"`

	InsertSave func(context.Context, WorkID, PartID, io.ReadCloser) error   `rest:"POST(application/octet-stream) /saves/{work_id=%v}/{part_id=%v}"`
	LookupSave func(context.Context, WorkID, PartID) (io.ReadCloser, error) `rest:"GET /saves/{work_id=%v}/{part_id=%v}" mime:"application/octet-stream"`

	InsertSnap func(context.Context, WorkID, io.ReadCloser) error   `rest:"POST(application/octet-stream) /snaps/{work_id=%v}"`      // image
	LookupSnap func(context.Context, WorkID) (io.ReadCloser, error) `rest:"GET /snaps/{work_id=%v}" mime:"application/octet-stream"` // image
}

type WorkID string
type PartID string
type UserID string

type Part struct {
	Size int64     `json:"size"`
	Time time.Time `json:"time"`
}

type User struct {
	ID            UserID    `json:"user_id"`
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
