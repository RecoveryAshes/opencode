// Package session contains the core session contracts shared by Go storage,
// server, runtime, and UI adapters.
package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ID is the serialized session identifier. The legacy TypeScript contract
// accepts strings beginning with "ses".
type ID string

// MessageID is the serialized message identifier.
type MessageID string

// PartID is the serialized message part identifier.
type PartID string

// TimeInfo stores millisecond timestamps, matching the TypeScript API shape.
type TimeInfo struct {
	Created  int64  `json:"created"`
	Updated  int64  `json:"updated"`
	Archived *int64 `json:"archived,omitempty"`
}

// Info is the public session DTO used by the HTTP API.
type Info struct {
	ID         ID       `json:"id"`
	ParentID   *ID      `json:"parentID,omitempty"`
	Title      string   `json:"title,omitempty"`
	Time       TimeInfo `json:"time"`
	Permission []string `json:"permission,omitempty"`
}

// CreateInput is the session creation payload accepted by the HTTP API.
type CreateInput struct {
	Title    string `json:"title,omitempty"`
	ParentID *ID    `json:"parentID,omitempty"`
}

// UpdateInput is the mutable subset of a session.
type UpdateInput struct {
	Title *string `json:"title,omitempty"`
}

// ListFilter represents the currently migrated list query fields.
type ListFilter struct {
	Search string
	Limit  int
}

// Repository is the storage boundary for migrated session routes.
type Repository interface {
	List(context.Context, ListFilter) ([]Info, error)
	Create(context.Context, CreateInput) (Info, error)
	Get(context.Context, ID) (Info, error)
	Update(context.Context, ID, UpdateInput) (Info, error)
	Remove(context.Context, ID) error
}

// ErrNotFound is returned when a session does not exist.
var ErrNotFound = errors.New("session not found")

// NewID creates a new session identifier with the legacy "ses" prefix.
func NewID() (ID, error) {
	random := make([]byte, 10)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generate session id: %w", err)
	}
	return ID(fmt.Sprintf("ses_%x_%s", time.Now().UnixMilli(), hex.EncodeToString(random))), nil
}

// ParseID validates a serialized session ID.
func ParseID(value string) (ID, error) {
	if !strings.HasPrefix(value, "ses") {
		return "", fmt.Errorf("invalid session id %q", value)
	}
	return ID(value), nil
}

// NowMillis returns the current Unix timestamp in milliseconds.
func NowMillis() int64 {
	return time.Now().UnixMilli()
}
