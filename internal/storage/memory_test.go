package storage

import (
	"context"
	"testing"

	"github.com/RecoveryAshes/opencode/internal/domain/session"
)

func TestMemorySessionStoreCreateListUpdateRemove(t *testing.T) {
	ctx := context.Background()
	store := NewMemorySessionStore()

	first, err := store.Create(ctx, session.CreateInput{Title: "first"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	second, err := store.Create(ctx, session.CreateInput{Title: "second"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	list, err := store.List(ctx, session.ListFilter{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(list) != 2 || list[0].ID != second.ID || list[1].ID != first.ID {
		t.Fatalf("List() = %#v, want reverse creation order", list)
	}

	title := "updated"
	updated, err := store.Update(ctx, first.ID, session.UpdateInput{Title: &title})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if updated.Title != title {
		t.Fatalf("updated title = %q, want %q", updated.Title, title)
	}

	if err := store.Remove(ctx, second.ID); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if _, err := store.Get(ctx, second.ID); !IsNotFound(err) {
		t.Fatalf("Get() error = %v, want not found", err)
	}
}
