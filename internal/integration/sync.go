package integration

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	syncdomain "github.com/RecoveryAshes/opencode/internal/domain/sync"
)

// SyncEvent is the serialized sync event contract.
type SyncEvent = syncdomain.Event

// SyncHistoryEvent is the storage row returned by /sync/history.
type SyncHistoryEvent = syncdomain.HistoryEvent

// SyncReplayInput is the /sync/replay payload.
type SyncReplayInput struct {
	Directory string      `json:"directory"`
	Events    []SyncEvent `json:"events"`
}

// SyncReplayResult is returned after replaying a session event stream.
type SyncReplayResult struct {
	SessionID string `json:"sessionID"`
}

// SyncSessionInput is the /sync/steal payload.
type SyncSessionInput struct {
	SessionID string `json:"sessionID"`
}

// SyncStore is an in-memory event log for the migrated local sync API.
type SyncStore struct {
	mu        sync.RWMutex
	events    map[string][]SyncEvent
	ownerByID map[string]string
	persist   syncdomain.Store
}

// NewSyncStore creates an empty sync event log.
func NewSyncStore() *SyncStore {
	return NewSyncStoreWithPersistence(nil)
}

// NewSyncStoreWithPersistence creates a sync event log backed by optional storage.
func NewSyncStoreWithPersistence(persist syncdomain.Store) *SyncStore {
	return &SyncStore{
		events:    map[string][]SyncEvent{},
		ownerByID: map[string]string{},
		persist:   persist,
	}
}

// Start records that local workspace sync can begin. The current Go sidecar sync
// loop is local-only, so start is currently a successful no-op.
func (store *SyncStore) Start(context.Context, string) (bool, error) {
	return true, nil
}

// Replay validates and stores a complete event stream.
func (store *SyncStore) Replay(_ context.Context, input SyncReplayInput) (SyncReplayResult, error) {
	if len(input.Events) == 0 {
		return SyncReplayResult{}, fmt.Errorf("events are required")
	}
	source := input.Events[0].AggregateID
	if source == "" {
		return SyncReplayResult{}, fmt.Errorf("aggregateID is required")
	}
	start := input.Events[0].Seq
	for index, event := range input.Events {
		if event.AggregateID != source {
			return SyncReplayResult{}, fmt.Errorf("replay events must belong to the same session")
		}
		if event.Seq != start+int64(index) {
			return SyncReplayResult{}, fmt.Errorf("replay sequence mismatch at index %d: expected %d, got %d", index, start+int64(index), event.Seq)
		}
		if event.ID == "" {
			return SyncReplayResult{}, fmt.Errorf("event id is required")
		}
		if event.Type == "" {
			return SyncReplayResult{}, fmt.Errorf("event type is required")
		}
		if event.Data == nil {
			event.Data = map[string]any{}
			input.Events[index] = event
		}
	}

	appended := []SyncEvent{}
	if err := func() error {
		store.mu.Lock()
		defer store.mu.Unlock()
		existing := store.events[source]
		latest := int64(-1)
		if len(existing) > 0 {
			latest = existing[len(existing)-1].Seq
		}
		for _, event := range input.Events {
			if event.Seq <= latest {
				continue
			}
			if event.Seq != latest+1 {
				return fmt.Errorf("sequence mismatch for aggregate %q: expected %d, got %d", source, latest+1, event.Seq)
			}
			existing = append(existing, event)
			appended = append(appended, event)
			latest = event.Seq
		}
		store.events[source] = existing
		return nil
	}(); err != nil {
		return SyncReplayResult{}, err
	}
	if store.persist != nil && len(appended) > 0 {
		if err := store.persist.AppendEvents(source, appended); err != nil {
			return SyncReplayResult{}, err
		}
	}
	return SyncReplayResult{SessionID: source}, nil
}

// Steal marks a session as owned by a workspace in the local event log.
func (store *SyncStore) Steal(_ context.Context, workspaceID string, sessionID string) (SyncSessionInput, error) {
	if sessionID == "" {
		return SyncSessionInput{}, fmt.Errorf("sessionID is required")
	}
	if workspaceID == "" {
		return SyncSessionInput{}, fmt.Errorf("workspace is required")
	}
	store.mu.Lock()
	store.ownerByID[sessionID] = workspaceID
	next := SyncEvent{
		ID:          fmt.Sprintf("evt_sync_%d", len(store.events[sessionID])+1),
		AggregateID: sessionID,
		Seq:         int64(len(store.events[sessionID])),
		Type:        "session.updated.1",
		Data: map[string]any{
			"sessionID": sessionID,
			"info": map[string]any{
				"workspaceID": workspaceID,
			},
		},
	}
	store.events[sessionID] = append(store.events[sessionID], next)
	store.mu.Unlock()
	if store.persist != nil {
		if err := store.persist.AppendEvents(sessionID, []SyncEvent{next}); err != nil {
			return SyncSessionInput{}, err
		}
	}
	return SyncSessionInput{SessionID: sessionID}, nil
}

// History returns all events newer than the client-provided sequence map.
func (store *SyncStore) History(_ context.Context, cursor map[string]int64) ([]SyncHistoryEvent, error) {
	if store.persist != nil {
		return store.persist.EventsAfter(cursor)
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	result := []SyncHistoryEvent{}
	for aggregateID, events := range store.events {
		after, ok := cursor[aggregateID]
		for _, event := range events {
			if ok && event.Seq <= after {
				continue
			}
			result = append(result, SyncHistoryEvent{
				ID:          event.ID,
				AggregateID: event.AggregateID,
				Seq:         event.Seq,
				Type:        event.Type,
				Data:        cloneAnyMap(event.Data),
			})
		}
	}
	slices.SortFunc(result, func(a SyncHistoryEvent, b SyncHistoryEvent) int {
		if a.Seq != b.Seq {
			if a.Seq < b.Seq {
				return -1
			}
			return 1
		}
		if cmp := strings.Compare(a.AggregateID, b.AggregateID); cmp != 0 {
			return cmp
		}
		return strings.Compare(a.ID, b.ID)
	})
	return result, nil
}

func cloneAnyMap(input map[string]any) map[string]any {
	if input == nil {
		return map[string]any{}
	}
	result := make(map[string]any, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}
