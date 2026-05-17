// Package storage contains persistence adapters for the migrated Go runtime.
package storage

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"

	"github.com/RecoveryAshes/opencode/internal/domain/session"
)

// MemorySessionStore is a deterministic in-memory session repository used by
// the first Go server slice and parity tests.
type MemorySessionStore struct {
	mu       sync.RWMutex
	sessions map[session.ID]session.Info
	order    []session.ID
	messages map[session.ID][]session.WithParts
}

// NewMemorySessionStore creates an empty session store.
func NewMemorySessionStore() *MemorySessionStore {
	return &MemorySessionStore{
		sessions: map[session.ID]session.Info{},
		order:    []session.ID{},
		messages: map[session.ID][]session.WithParts{},
	}
}

// List returns sessions sorted by most recently updated, matching the legacy
// HTTP API's observable ordering.
func (store *MemorySessionStore) List(_ context.Context, filter session.ListFilter) ([]session.Info, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()

	result := make([]session.Info, 0, len(store.order))
	for _, id := range store.order {
		info := store.sessions[id]
		if !matchesSessionFilter(info, filter) {
			continue
		}
		result = append(result, info)
		if filter.Limit > 0 && len(result) >= filter.Limit {
			break
		}
	}
	return result, nil
}

// Create stores a new session.
func (store *MemorySessionStore) Create(_ context.Context, input session.CreateInput) (session.Info, error) {
	id, err := session.NewID()
	if err != nil {
		return session.Info{}, err
	}
	now := session.NowMillis()
	info := session.Info{
		ID:          id,
		Slug:        slug(input.Title),
		ProjectID:   defaultString(input.ProjectID, defaultProjectID),
		WorkspaceID: input.WorkspaceID,
		Directory:   input.Directory,
		Path:        input.Path,
		ParentID:    input.ParentID,
		Title:       input.Title,
		Agent:       input.Agent,
		Model:       cloneModel(input.Model),
		Version:     defaultString(input.Version, "go-migration"),
		Cost:        input.Cost,
		Tokens:      cloneTokens(input.Tokens),
		Time: session.TimeInfo{
			Created: now,
			Updated: now,
		},
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	store.sessions[id] = info
	store.order = append([]session.ID{id}, store.order...)
	return info, nil
}

// Get returns one session.
func (store *MemorySessionStore) Get(_ context.Context, id session.ID) (session.Info, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()

	info, ok := store.sessions[id]
	if !ok {
		return session.Info{}, session.ErrNotFound
	}
	return info, nil
}

// Update changes mutable session fields.
func (store *MemorySessionStore) Update(_ context.Context, id session.ID, input session.UpdateInput) (session.Info, error) {
	store.mu.Lock()
	defer store.mu.Unlock()

	info, ok := store.sessions[id]
	if !ok {
		return session.Info{}, session.ErrNotFound
	}
	if input.Title != nil {
		info.Title = *input.Title
		info.Slug = slug(*input.Title)
	}
	if input.ProjectID != nil {
		info.ProjectID = *input.ProjectID
	}
	if input.WorkspaceID != nil {
		info.WorkspaceID = *input.WorkspaceID
	}
	if input.Directory != nil {
		info.Directory = *input.Directory
	}
	if input.Path != nil {
		info.Path = *input.Path
	}
	if input.Agent != nil {
		info.Agent = *input.Agent
	}
	if input.Model != nil {
		info.Model = cloneModel(input.Model)
	}
	if input.Version != nil {
		info.Version = *input.Version
	}
	if input.Cost != nil {
		info.Cost = *input.Cost
	}
	if input.Tokens != nil {
		info.Tokens = cloneTokens(input.Tokens)
	}
	if input.Archived != nil {
		info.Time.Archived = input.Archived
	}
	if input.Permission != nil {
		info.Permission = append([]string(nil), (*input.Permission)...)
	}
	if input.Revert != nil {
		revert := *input.Revert
		info.Revert = &revert
	}
	if input.ClearRevert {
		info.Revert = nil
		info.Summary = nil
	}
	if input.Summary != nil {
		summary := *input.Summary
		info.Summary = &summary
	}
	if input.Share != nil {
		share := *input.Share
		info.Share = &share
	}
	if input.ClearShare {
		info.Share = nil
	}
	info.Time.Updated = session.NowMillis()
	store.sessions[id] = info
	store.moveToFront(id)
	return info, nil
}

// Children lists child sessions by parent id.
func (store *MemorySessionStore) Children(_ context.Context, parentID session.ID) ([]session.Info, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()

	if _, ok := store.sessions[parentID]; !ok {
		return nil, session.ErrNotFound
	}
	result := []session.Info{}
	for _, id := range store.order {
		info := store.sessions[id]
		if info.ParentID != nil && *info.ParentID == parentID {
			result = append(result, info)
		}
	}
	return result, nil
}

// Fork creates a child session and copies messages up to the optional message id.
func (store *MemorySessionStore) Fork(_ context.Context, parentID session.ID, messageID *session.MessageID) (session.Info, error) {
	store.mu.Lock()
	defer store.mu.Unlock()

	parent, ok := store.sessions[parentID]
	if !ok {
		return session.Info{}, session.ErrNotFound
	}
	id, err := session.NewID()
	if err != nil {
		return session.Info{}, err
	}
	now := session.NowMillis()
	info := session.Info{
		ID:          id,
		Slug:        parent.Slug,
		ProjectID:   parent.ProjectID,
		WorkspaceID: parent.WorkspaceID,
		Directory:   parent.Directory,
		Path:        parent.Path,
		ParentID:    &parentID,
		Title:       parent.Title,
		Agent:       parent.Agent,
		Model:       cloneModel(parent.Model),
		Version:     parent.Version,
		Cost:        parent.Cost,
		Tokens:      cloneTokens(parent.Tokens),
		Time: session.TimeInfo{
			Created: now,
			Updated: now,
		},
		Permission: append([]string(nil), parent.Permission...),
	}
	store.sessions[id] = info
	store.order = append([]session.ID{id}, store.order...)
	store.messages[id] = copyMessagesForFork(id, store.messages[parentID], messageID)
	return info, nil
}

// Remove deletes one session.
func (store *MemorySessionStore) Remove(_ context.Context, id session.ID) error {
	store.mu.Lock()
	defer store.mu.Unlock()

	if _, ok := store.sessions[id]; !ok {
		return session.ErrNotFound
	}
	delete(store.sessions, id)
	delete(store.messages, id)
	store.order = slices.DeleteFunc(store.order, func(candidate session.ID) bool {
		return candidate == id
	})
	return nil
}

// Messages returns messages in creation order.
func (store *MemorySessionStore) Messages(_ context.Context, sessionID session.ID, limit int) ([]session.WithParts, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()

	if _, ok := store.sessions[sessionID]; !ok {
		return nil, session.ErrNotFound
	}
	items := append([]session.WithParts(nil), store.messages[sessionID]...)
	if limit > 0 && len(items) > limit {
		items = items[len(items)-limit:]
	}
	return items, nil
}

// GetMessage returns a message with its parts.
func (store *MemorySessionStore) GetMessage(_ context.Context, sessionID session.ID, messageID session.MessageID) (session.WithParts, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()

	for _, message := range store.messages[sessionID] {
		if message.Info.ID == messageID {
			return message, nil
		}
	}
	return session.WithParts{}, session.ErrNotFound
}

// CreatePrompt creates a user message and prompt parts.
func (store *MemorySessionStore) CreatePrompt(_ context.Context, sessionID session.ID, input session.PromptInput) (session.WithParts, error) {
	store.mu.Lock()
	defer store.mu.Unlock()

	info, ok := store.sessions[sessionID]
	if !ok {
		return session.WithParts{}, session.ErrNotFound
	}
	message, err := createPromptMessage(sessionID, input)
	if err != nil {
		return session.WithParts{}, err
	}
	info.Time.Updated = session.NowMillis()
	store.sessions[sessionID] = info
	store.moveToFront(sessionID)
	store.messages[sessionID] = append(store.messages[sessionID], message)
	return message, nil
}

// CreateAssistant creates one assistant message with text and finish parts.
func (store *MemorySessionStore) CreateAssistant(_ context.Context, sessionID session.ID, input session.AssistantInput) (session.WithParts, error) {
	store.mu.Lock()
	defer store.mu.Unlock()

	info, ok := store.sessions[sessionID]
	if !ok {
		return session.WithParts{}, session.ErrNotFound
	}
	message, err := createAssistantMessage(sessionID, input)
	if err != nil {
		return session.WithParts{}, err
	}
	info.Time.Updated = session.NowMillis()
	store.sessions[sessionID] = info
	store.moveToFront(sessionID)
	store.messages[sessionID] = append(store.messages[sessionID], message)
	return message, nil
}

// RemoveMessage deletes one message and its parts.
func (store *MemorySessionStore) RemoveMessage(_ context.Context, sessionID session.ID, messageID session.MessageID) error {
	store.mu.Lock()
	defer store.mu.Unlock()

	items := store.messages[sessionID]
	next := slices.DeleteFunc(items, func(message session.WithParts) bool {
		return message.Info.ID == messageID
	})
	if len(next) == len(items) {
		return session.ErrNotFound
	}
	store.messages[sessionID] = next
	return nil
}

// RemovePart deletes one part from a message.
func (store *MemorySessionStore) RemovePart(_ context.Context, sessionID session.ID, messageID session.MessageID, partID session.PartID) error {
	store.mu.Lock()
	defer store.mu.Unlock()

	items := store.messages[sessionID]
	for messageIndex, message := range items {
		if message.Info.ID != messageID {
			continue
		}
		next := slices.DeleteFunc(message.Parts, func(part session.Part) bool {
			return part.ID == partID
		})
		if len(next) == len(message.Parts) {
			return session.ErrNotFound
		}
		message.Parts = next
		items[messageIndex] = message
		store.messages[sessionID] = items
		return nil
	}
	return session.ErrNotFound
}

// UpdatePart replaces one stored part.
func (store *MemorySessionStore) UpdatePart(_ context.Context, part session.Part) (session.Part, error) {
	store.mu.Lock()
	defer store.mu.Unlock()

	items := store.messages[part.SessionID]
	for messageIndex, message := range items {
		if message.Info.ID != part.MessageID {
			continue
		}
		for partIndex, existing := range message.Parts {
			if existing.ID == part.ID {
				message.Parts[partIndex] = part
				items[messageIndex] = message
				store.messages[part.SessionID] = items
				return part, nil
			}
		}
		return session.Part{}, session.ErrNotFound
	}
	return session.Part{}, session.ErrNotFound
}

func (store *MemorySessionStore) moveToFront(id session.ID) {
	store.order = slices.DeleteFunc(store.order, func(candidate session.ID) bool {
		return candidate == id
	})
	store.order = append([]session.ID{id}, store.order...)
}

func createPromptMessage(sessionID session.ID, input session.PromptInput) (session.WithParts, error) {
	messageID := session.MessageID("")
	if input.MessageID != nil {
		messageID = *input.MessageID
	} else {
		next, err := session.NewMessageID()
		if err != nil {
			return session.WithParts{}, err
		}
		messageID = next
	}
	now := session.NowMillis()
	message := session.WithParts{
		Info: session.MessageInfo{
			ID:        messageID,
			SessionID: sessionID,
			Role:      "user",
			Time:      session.MessageTime{Created: now},
			Agent:     defaultString(input.Agent, "build"),
			Model:     input.Model,
			Tools:     input.Tools,
			System:    input.System,
			Format:    input.Format,
		},
		Parts: make([]session.Part, 0, len(input.Parts)),
	}
	for _, part := range input.Parts {
		part.SessionID = sessionID
		part.MessageID = messageID
		if part.ID == "" {
			next, err := session.NewPartID()
			if err != nil {
				return session.WithParts{}, err
			}
			part.ID = next
		}
		message.Parts = append(message.Parts, part)
	}
	return message, nil
}

func createAssistantMessage(sessionID session.ID, input session.AssistantInput) (session.WithParts, error) {
	messageID, err := session.NewMessageID()
	if err != nil {
		return session.WithParts{}, err
	}
	now := session.NowMillis()
	completed := now
	parentID := input.ParentID
	cost := input.Cost
	finish := defaultString(input.Finish, "stop")
	agent := defaultString(input.Agent, "build")
	model := input.Model
	if model.ProviderID == "" {
		model.ProviderID = "openai-compatible"
	}
	if model.ModelID == "" {
		model.ModelID = "gpt-4o-mini"
	}
	path := input.Path
	if path.CWD == "" {
		path.CWD = "."
	}
	if path.Root == "" {
		path.Root = path.CWD
	}

	message := session.WithParts{
		Info: session.MessageInfo{
			ID:         messageID,
			SessionID:  sessionID,
			Role:       "assistant",
			Time:       session.MessageTime{Created: now, Completed: &completed},
			Agent:      agent,
			ParentID:   &parentID,
			ModelID:    model.ModelID,
			ProviderID: model.ProviderID,
			Mode:       agent,
			Path:       &path,
			Cost:       &cost,
			Tokens:     &input.Tokens,
			Variant:    model.Variant,
			Finish:     finish,
		},
		Parts: []session.Part{},
	}
	if input.Text != "" {
		partID, partErr := session.NewPartID()
		if partErr != nil {
			return session.WithParts{}, partErr
		}
		message.Parts = append(message.Parts, session.Part{
			ID:        partID,
			SessionID: sessionID,
			MessageID: messageID,
			Type:      "text",
			Data: map[string]any{
				"text": input.Text,
				"time": map[string]any{
					"start": now,
					"end":   completed,
				},
			},
		})
	}
	for _, tool := range input.Tools {
		partID, partErr := session.NewPartID()
		if partErr != nil {
			return session.WithParts{}, partErr
		}
		start := tool.StartTime
		if start == 0 {
			start = now
		}
		end := tool.EndTime
		if end == 0 {
			end = now
		}
		state := map[string]any{
			"input": tool.Input,
			"time": map[string]any{
				"start": start,
				"end":   end,
			},
		}
		if tool.Error != "" {
			state["status"] = "error"
			state["error"] = tool.Error
		} else {
			state["status"] = "completed"
			state["title"] = tool.Title
			state["output"] = tool.Output
			state["metadata"] = tool.Metadata
		}
		if tool.Metadata != nil && tool.Error != "" {
			state["metadata"] = tool.Metadata
		}
		message.Parts = append(message.Parts, session.Part{
			ID:        partID,
			SessionID: sessionID,
			MessageID: messageID,
			Type:      "tool",
			Data: map[string]any{
				"callID": tool.CallID,
				"tool":   tool.Tool,
				"state":  state,
			},
		})
	}

	partID, err := session.NewPartID()
	if err != nil {
		return session.WithParts{}, err
	}
	message.Parts = append(message.Parts, session.Part{
		ID:        partID,
		SessionID: sessionID,
		MessageID: messageID,
		Type:      "step-finish",
		Data: map[string]any{
			"reason": finish,
			"cost":   input.Cost,
			"tokens": input.Tokens,
		},
	})
	return message, nil
}

func copyMessagesForFork(nextSessionID session.ID, messages []session.WithParts, until *session.MessageID) []session.WithParts {
	result := []session.WithParts{}
	messageIDs := map[session.MessageID]session.MessageID{}
	for _, message := range messages {
		copied := message
		originalID := message.Info.ID
		nextMessageID, err := session.NewMessageID()
		if err == nil {
			copied.Info.ID = nextMessageID
			messageIDs[originalID] = nextMessageID
		}
		copied.Info.SessionID = nextSessionID
		if copied.Info.ParentID != nil {
			if mapped, ok := messageIDs[*copied.Info.ParentID]; ok {
				copied.Info.ParentID = &mapped
			}
		}
		parts := make([]session.Part, len(message.Parts))
		for i, part := range message.Parts {
			if nextPartID, err := session.NewPartID(); err == nil {
				part.ID = nextPartID
			}
			part.SessionID = nextSessionID
			part.MessageID = copied.Info.ID
			parts[i] = part
		}
		copied.Parts = parts
		result = append(result, copied)
		if until != nil && message.Info.ID == *until {
			break
		}
	}
	return result
}

func defaultString(value string, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func matchesSessionFilter(info session.Info, filter session.ListFilter) bool {
	if filter.Search != "" && !strings.Contains(strings.ToLower(info.Title), strings.ToLower(filter.Search)) {
		return false
	}
	if filter.ProjectID != "" && info.ProjectID != filter.ProjectID {
		return false
	}
	if filter.WorkspaceID != "" && info.WorkspaceID != filter.WorkspaceID {
		return false
	}
	if filter.Directory != "" && info.Directory != filter.Directory {
		return false
	}
	if filter.Path != nil {
		if *filter.Path == "" {
			return info.Path == ""
		}
		return info.Path == *filter.Path || strings.HasPrefix(info.Path, *filter.Path+"/")
	}
	return true
}

func cloneModel(input *session.SessionModel) *session.SessionModel {
	if input == nil {
		return nil
	}
	model := *input
	return &model
}

func cloneTokens(input *session.TokenUsage) *session.TokenUsage {
	if input == nil {
		return nil
	}
	tokens := *input
	return &tokens
}

// IsNotFound reports whether an error represents a missing record.
func IsNotFound(err error) bool {
	return errors.Is(err, session.ErrNotFound)
}
