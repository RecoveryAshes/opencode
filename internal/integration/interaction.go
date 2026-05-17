package integration

import (
	"fmt"
	"slices"
	"sync"
	"time"
)

// QuestionID identifies a pending question request.
type QuestionID string

// PermissionID identifies a pending permission request.
type PermissionID string

// QuestionOption is one selectable answer option.
type QuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

// QuestionInfo describes a prompt question.
type QuestionInfo struct {
	Question string           `json:"question"`
	Header   string           `json:"header"`
	Options  []QuestionOption `json:"options"`
	Multiple bool             `json:"multiple,omitempty"`
	Custom   *bool            `json:"custom,omitempty"`
}

// QuestionTool describes the tool call that requested a question.
type QuestionTool struct {
	MessageID string `json:"messageID"`
	CallID    string `json:"callID"`
}

// QuestionRequest is the HTTP/SSE contract for pending questions.
type QuestionRequest struct {
	ID        QuestionID     `json:"id"`
	SessionID string         `json:"sessionID"`
	Questions []QuestionInfo `json:"questions"`
	Tool      *QuestionTool  `json:"tool,omitempty"`
}

// QuestionAnswer contains selected labels for one question.
type QuestionAnswer []string

// PermissionRequest is the HTTP/SSE contract for pending tool permissions.
type PermissionRequest struct {
	ID         PermissionID   `json:"id"`
	SessionID  string         `json:"sessionID"`
	Permission string         `json:"permission"`
	Patterns   []string       `json:"patterns"`
	Metadata   map[string]any `json:"metadata"`
	Always     []string       `json:"always"`
	Tool       *QuestionTool  `json:"tool,omitempty"`
}

// InteractionManager stores local pending question and permission requests.
type InteractionManager struct {
	mu           sync.Mutex
	questions    map[QuestionID]QuestionRequest
	permissions  map[PermissionID]PermissionRequest
	nextQuestion int64
	nextPerm     int64
}

// NewInteractionManager creates an empty interaction queue.
func NewInteractionManager() *InteractionManager {
	return &InteractionManager{
		questions:   map[QuestionID]QuestionRequest{},
		permissions: map[PermissionID]PermissionRequest{},
	}
}

// AddQuestion queues a question request and assigns an id when missing.
func (manager *InteractionManager) AddQuestion(request QuestionRequest) QuestionRequest {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if request.ID == "" {
		manager.nextQuestion++
		request.ID = QuestionID(formatInteractionID("que", manager.nextQuestion))
	}
	manager.questions[request.ID] = cloneQuestionRequest(request)
	return cloneQuestionRequest(request)
}

// ListQuestions returns pending question requests.
func (manager *InteractionManager) ListQuestions() []QuestionRequest {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	result := make([]QuestionRequest, 0, len(manager.questions))
	for _, request := range manager.questions {
		result = append(result, cloneQuestionRequest(request))
	}
	slices.SortFunc(result, func(a QuestionRequest, b QuestionRequest) int { return compareString(string(a.ID), string(b.ID)) })
	return result
}

// ReplyQuestion removes and returns a question request if present.
func (manager *InteractionManager) ReplyQuestion(id QuestionID) (QuestionRequest, bool) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	request, ok := manager.questions[id]
	if !ok {
		return QuestionRequest{}, false
	}
	delete(manager.questions, id)
	return cloneQuestionRequest(request), true
}

// RejectQuestion removes and returns a question request if present.
func (manager *InteractionManager) RejectQuestion(id QuestionID) (QuestionRequest, bool) {
	return manager.ReplyQuestion(id)
}

// AddPermission queues a permission request and assigns an id when missing.
func (manager *InteractionManager) AddPermission(request PermissionRequest) PermissionRequest {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if request.ID == "" {
		manager.nextPerm++
		request.ID = PermissionID(formatInteractionID("per", manager.nextPerm))
	}
	if request.Metadata == nil {
		request.Metadata = map[string]any{}
	}
	manager.permissions[request.ID] = clonePermissionRequest(request)
	return clonePermissionRequest(request)
}

// ListPermissions returns pending permission requests.
func (manager *InteractionManager) ListPermissions() []PermissionRequest {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	result := make([]PermissionRequest, 0, len(manager.permissions))
	for _, request := range manager.permissions {
		result = append(result, clonePermissionRequest(request))
	}
	slices.SortFunc(result, func(a PermissionRequest, b PermissionRequest) int {
		return compareString(string(a.ID), string(b.ID))
	})
	return result
}

// ReplyPermission removes and returns a permission request if present.
func (manager *InteractionManager) ReplyPermission(id PermissionID) (PermissionRequest, bool) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	request, ok := manager.permissions[id]
	if !ok {
		return PermissionRequest{}, false
	}
	delete(manager.permissions, id)
	return clonePermissionRequest(request), true
}

func formatInteractionID(prefix string, value int64) string {
	return fmt.Sprintf("%s_%d_%d", prefix, time.Now().UnixMilli(), value)
}

func cloneQuestionRequest(input QuestionRequest) QuestionRequest {
	output := input
	output.Questions = append([]QuestionInfo{}, input.Questions...)
	for i := range output.Questions {
		output.Questions[i].Options = append([]QuestionOption{}, input.Questions[i].Options...)
	}
	return output
}

func clonePermissionRequest(input PermissionRequest) PermissionRequest {
	output := input
	output.Patterns = append([]string{}, input.Patterns...)
	output.Always = append([]string{}, input.Always...)
	output.Metadata = cloneMap(input.Metadata)
	return output
}

func compareString(a string, b string) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}
