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
}

// NewMemorySessionStore creates an empty session store.
func NewMemorySessionStore() *MemorySessionStore {
	return &MemorySessionStore{
		sessions: map[session.ID]session.Info{},
		order:    []session.ID{},
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
		if filter.Search != "" && !strings.Contains(strings.ToLower(info.Title), strings.ToLower(filter.Search)) {
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
		ID:       id,
		ParentID: input.ParentID,
		Title:    input.Title,
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
	}
	info.Time.Updated = session.NowMillis()
	store.sessions[id] = info
	store.moveToFront(id)
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
	store.order = slices.DeleteFunc(store.order, func(candidate session.ID) bool {
		return candidate == id
	})
	return nil
}

func (store *MemorySessionStore) moveToFront(id session.ID) {
	store.order = slices.DeleteFunc(store.order, func(candidate session.ID) bool {
		return candidate == id
	})
	store.order = append([]session.ID{id}, store.order...)
}

// IsNotFound reports whether an error represents a missing record.
func IsNotFound(err error) bool {
	return errors.Is(err, session.ErrNotFound)
}
