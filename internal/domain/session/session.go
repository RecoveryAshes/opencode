// Package session contains the core session contracts shared by Go storage,
// server, runtime, and UI adapters.
package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
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
	Created    int64  `json:"created"`
	Updated    int64  `json:"updated"`
	Compacting *int64 `json:"compacting,omitempty"`
	Archived   *int64 `json:"archived,omitempty"`
}

// SummaryInfo stores lightweight session diff summary metadata.
type SummaryInfo struct {
	Additions int              `json:"additions"`
	Deletions int              `json:"deletions"`
	Files     int              `json:"files"`
	Diffs     []map[string]any `json:"diffs,omitempty"`
}

// ShareInfo stores a public session share URL.
type ShareInfo struct {
	URL string `json:"url"`
}

// RevertInfo stores the current reverted message/part marker.
type RevertInfo struct {
	MessageID MessageID `json:"messageID"`
	PartID    *PartID   `json:"partID,omitempty"`
	Snapshot  string    `json:"snapshot,omitempty"`
	Diff      string    `json:"diff,omitempty"`
}

// TodoInfo stores one session todo item.
type TodoInfo struct {
	Content  string `json:"content"`
	Status   string `json:"status"`
	Priority string `json:"priority"`
}

// StatusAction stores optional retry/action metadata for a session status.
type StatusAction struct {
	Reason   string `json:"reason"`
	Provider string `json:"provider"`
	Title    string `json:"title"`
	Message  string `json:"message"`
	Label    string `json:"label"`
	Link     string `json:"link,omitempty"`
}

// StatusInfo mirrors the legacy runtime status union.
type StatusInfo struct {
	Type    string        `json:"type"`
	Attempt int           `json:"attempt,omitempty"`
	Message string        `json:"message,omitempty"`
	Action  *StatusAction `json:"action,omitempty"`
	Next    int64         `json:"next,omitempty"`
}

// Info is the public session DTO used by the HTTP API.
type Info struct {
	ID          ID            `json:"id"`
	Slug        string        `json:"slug,omitempty"`
	ProjectID   string        `json:"projectID,omitempty"`
	WorkspaceID string        `json:"workspaceID,omitempty"`
	Directory   string        `json:"directory,omitempty"`
	Path        string        `json:"path,omitempty"`
	ParentID    *ID           `json:"parentID,omitempty"`
	Summary     *SummaryInfo  `json:"summary,omitempty"`
	Share       *ShareInfo    `json:"share,omitempty"`
	Title       string        `json:"title,omitempty"`
	Agent       string        `json:"agent,omitempty"`
	Model       *SessionModel `json:"model,omitempty"`
	Version     string        `json:"version,omitempty"`
	Cost        float64       `json:"cost,omitempty"`
	Tokens      *TokenUsage   `json:"tokens,omitempty"`
	Time        TimeInfo      `json:"time"`
	Permission  []string      `json:"permission,omitempty"`
	Revert      *RevertInfo   `json:"revert,omitempty"`
}

// ModelRef identifies a provider/model pair used by a message.
type ModelRef struct {
	ProviderID string `json:"providerID"`
	ModelID    string `json:"modelID"`
	Variant    string `json:"variant,omitempty"`
}

// SessionModel identifies the active provider/model pair stored on a session.
// The legacy TypeScript session row serializes the model ID as "id", while
// message DTOs serialize the same value as "modelID".
type SessionModel struct {
	ID         string `json:"id"`
	ProviderID string `json:"providerID"`
	Variant    string `json:"variant,omitempty"`
}

// PathInfo stores the local execution path captured on assistant messages.
type PathInfo struct {
	CWD  string `json:"cwd"`
	Root string `json:"root"`
}

// TokenUsage mirrors the legacy assistant token accounting JSON shape.
type TokenUsage struct {
	Total     *int       `json:"total,omitempty"`
	Input     int        `json:"input"`
	Output    int        `json:"output"`
	Reasoning int        `json:"reasoning"`
	Cache     CacheUsage `json:"cache"`
}

// CacheUsage stores cached token read/write counts.
type CacheUsage struct {
	Read  int `json:"read"`
	Write int `json:"write"`
}

// MessageInfo is the common serialized message contract.
type MessageInfo struct {
	ID         MessageID       `json:"id"`
	SessionID  ID              `json:"sessionID"`
	Role       string          `json:"role"`
	Time       MessageTime     `json:"time"`
	Agent      string          `json:"agent,omitempty"`
	Model      *ModelRef       `json:"model,omitempty"`
	Tools      map[string]bool `json:"tools,omitempty"`
	System     string          `json:"system,omitempty"`
	Format     map[string]any  `json:"format,omitempty"`
	ParentID   *MessageID      `json:"parentID,omitempty"`
	ModelID    string          `json:"modelID,omitempty"`
	ProviderID string          `json:"providerID,omitempty"`
	Mode       string          `json:"mode,omitempty"`
	Path       *PathInfo       `json:"path,omitempty"`
	Summary    bool            `json:"summary,omitempty"`
	Cost       *float64        `json:"cost,omitempty"`
	Tokens     *TokenUsage     `json:"tokens,omitempty"`
	Variant    string          `json:"variant,omitempty"`
	Finish     string          `json:"finish,omitempty"`
	Error      map[string]any  `json:"error,omitempty"`
}

// MessageTime stores message timestamps in milliseconds.
type MessageTime struct {
	Created   int64  `json:"created"`
	Completed *int64 `json:"completed,omitempty"`
}

// Part is a stored message part. Data carries the fields specific to the part
// type, matching the TypeScript MessageV2 part JSON shape.
type Part struct {
	ID        PartID         `json:"id"`
	SessionID ID             `json:"sessionID"`
	MessageID MessageID      `json:"messageID"`
	Type      string         `json:"type"`
	Data      map[string]any `json:"-"`
}

// MarshalJSON emits the TypeScript-compatible flattened part shape.
func (part Part) MarshalJSON() ([]byte, error) {
	result := map[string]any{}
	for key, value := range part.Data {
		result[key] = value
	}
	result["id"] = part.ID
	result["sessionID"] = part.SessionID
	result["messageID"] = part.MessageID
	result["type"] = part.Type
	return json.Marshal(result)
}

// UnmarshalJSON accepts the TypeScript-compatible flattened part shape.
func (part *Part) UnmarshalJSON(data []byte) error {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	part.ID = PartID(stringValue(raw["id"]))
	part.SessionID = ID(stringValue(raw["sessionID"]))
	part.MessageID = MessageID(stringValue(raw["messageID"]))
	part.Type = stringValue(raw["type"])
	delete(raw, "id")
	delete(raw, "sessionID")
	delete(raw, "messageID")
	delete(raw, "type")
	part.Data = raw
	return nil
}

// WithParts is the public message DTO returned by the session API.
type WithParts struct {
	Info  MessageInfo `json:"info"`
	Parts []Part      `json:"parts"`
}

// PromptInput is the migrated subset of SessionPrompt.PromptInput.
type PromptInput struct {
	MessageID *MessageID      `json:"messageID,omitempty"`
	Agent     string          `json:"agent,omitempty"`
	Model     *ModelRef       `json:"model,omitempty"`
	NoReply   bool            `json:"noReply,omitempty"`
	Tools     map[string]bool `json:"tools,omitempty"`
	System    string          `json:"system,omitempty"`
	Format    map[string]any  `json:"format,omitempty"`
	Parts     []Part          `json:"parts"`
}

// AssistantInput contains the migrated fields needed to persist one assistant turn.
type AssistantInput struct {
	ParentID MessageID
	Agent    string
	Model    ModelRef
	Path     PathInfo
	Text     string
	Tools    []ToolExecution
	Summary  bool
	Finish   string
	Tokens   TokenUsage
	Cost     float64
	Variant  string
}

// ToolExecution stores one provider-requested tool call and the Go execution
// result that should be persisted on the assistant message.
type ToolExecution struct {
	CallID    string
	Tool      string
	Input     map[string]any
	Raw       string
	Title     string
	Output    string
	Metadata  map[string]any
	Error     string
	StartTime int64
	EndTime   int64
}

// MessageRepository is the storage boundary for migrated message routes.
type MessageRepository interface {
	Messages(context.Context, ID, int) ([]WithParts, error)
	GetMessage(context.Context, ID, MessageID) (WithParts, error)
	CreatePrompt(context.Context, ID, PromptInput) (WithParts, error)
	CreateAssistant(context.Context, ID, AssistantInput) (WithParts, error)
	RemoveMessage(context.Context, ID, MessageID) error
	RemovePart(context.Context, ID, MessageID, PartID) error
	UpdatePart(context.Context, Part) (Part, error)
}

// MessageListFilter carries legacy HTTP message pagination fields.
type MessageListFilter struct {
	Limit  int
	Before *MessageCursor
}

// MessageCursor points at one message for backwards pagination.
type MessageCursor struct {
	ID   MessageID `json:"id"`
	Time int64     `json:"time"`
}

// MessagePage contains one legacy paginated message response.
type MessagePage struct {
	Items  []WithParts
	More   bool
	Cursor *MessageCursor
}

// MessagePager is implemented by stores that support legacy cursor pagination.
type MessagePager interface {
	MessagePage(context.Context, ID, MessageListFilter) (MessagePage, error)
}

// TodoRepository stores the per-session todo list used by the TUI and tools.
type TodoRepository interface {
	SetTodos(context.Context, ID, []TodoInfo) error
	Todos(context.Context, ID) ([]TodoInfo, error)
}

// StatusRepository stores runtime-only session status values.
type StatusRepository interface {
	SetStatus(context.Context, ID, StatusInfo) error
	Status(context.Context, ID) (StatusInfo, error)
	Statuses(context.Context) (map[ID]StatusInfo, error)
}

// DiffRepository stores the current session diff snapshot.
type DiffRepository interface {
	SetDiff(context.Context, ID, []map[string]any) error
	Diff(context.Context, ID) ([]map[string]any, error)
}

// CreateInput is the session creation payload accepted by the HTTP API.
type CreateInput struct {
	Title       string        `json:"title,omitempty"`
	ParentID    *ID           `json:"parentID,omitempty"`
	ProjectID   string        `json:"projectID,omitempty"`
	WorkspaceID string        `json:"workspaceID,omitempty"`
	Directory   string        `json:"directory,omitempty"`
	Path        string        `json:"path,omitempty"`
	Agent       string        `json:"agent,omitempty"`
	Model       *SessionModel `json:"model,omitempty"`
	Version     string        `json:"version,omitempty"`
	Tokens      *TokenUsage   `json:"tokens,omitempty"`
	Cost        float64       `json:"cost,omitempty"`
}

// UpdateInput is the mutable subset of a session.
type UpdateInput struct {
	Title        *string       `json:"title,omitempty"`
	ProjectID    *string       `json:"projectID,omitempty"`
	WorkspaceID  *string       `json:"workspaceID,omitempty"`
	Directory    *string       `json:"directory,omitempty"`
	Path         *string       `json:"path,omitempty"`
	Agent        *string       `json:"agent,omitempty"`
	Model        *SessionModel `json:"model,omitempty"`
	Version      *string       `json:"version,omitempty"`
	Cost         *float64      `json:"cost,omitempty"`
	Tokens       *TokenUsage   `json:"tokens,omitempty"`
	Archived     *int64        `json:"-"`
	Compacting   *int64        `json:"-"`
	ClearCompact bool          `json:"-"`
	Permission   *[]string     `json:"permission,omitempty"`
	Revert       *RevertInfo   `json:"revert,omitempty"`
	ClearRevert  bool          `json:"-"`
	Summary      *SummaryInfo  `json:"summary,omitempty"`
	Share        *ShareInfo    `json:"share,omitempty"`
	ClearShare   bool          `json:"-"`
}

// ListFilter represents the currently migrated list query fields.
type ListFilter struct {
	Search      string
	Limit       int
	ProjectID   string
	WorkspaceID string
	Directory   string
	Path        *string
	Roots       bool
	Start       int64
	Scope       string
}

// Repository is the storage boundary for migrated session routes.
type Repository interface {
	List(context.Context, ListFilter) ([]Info, error)
	Create(context.Context, CreateInput) (Info, error)
	Get(context.Context, ID) (Info, error)
	Update(context.Context, ID, UpdateInput) (Info, error)
	Children(context.Context, ID) ([]Info, error)
	Fork(context.Context, ID, *MessageID) (Info, error)
	Remove(context.Context, ID) error
}

// ErrNotFound is returned when a session does not exist.
var ErrNotFound = errors.New("session not found")

// NewID creates a new session identifier with the legacy "ses" prefix.
func NewID() (ID, error) {
	random, err := randomHex(10)
	if err != nil {
		return "", fmt.Errorf("generate session id: %w", err)
	}
	return ID(fmt.Sprintf("ses_%x_%s", time.Now().UnixMilli(), random)), nil
}

// NewMessageID creates a new message identifier with the legacy "msg" prefix.
func NewMessageID() (MessageID, error) {
	random, err := randomHex(10)
	if err != nil {
		return "", fmt.Errorf("generate message id: %w", err)
	}
	return MessageID(fmt.Sprintf("msg_%x_%s", time.Now().UnixMilli(), random)), nil
}

// NewPartID creates a new message part identifier with the legacy "prt" prefix.
func NewPartID() (PartID, error) {
	random, err := randomHex(10)
	if err != nil {
		return "", fmt.Errorf("generate part id: %w", err)
	}
	return PartID(fmt.Sprintf("prt_%x_%s", time.Now().UnixMilli(), random)), nil
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

func randomHex(size int) (string, error) {
	random := make([]byte, size)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return hex.EncodeToString(random), nil
}

func stringValue(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}
