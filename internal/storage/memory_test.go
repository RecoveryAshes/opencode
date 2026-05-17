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

func TestMemorySessionStoreMessages(t *testing.T) {
	ctx := context.Background()
	store := NewMemorySessionStore()
	info, err := store.Create(ctx, session.CreateInput{Title: "chat"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	message, err := store.CreatePrompt(ctx, info.ID, session.PromptInput{
		Agent: "build",
		Parts: []session.Part{{
			Type: "text",
			Data: map[string]any{"text": "hello"},
		}},
	})
	if err != nil {
		t.Fatalf("CreatePrompt() error = %v", err)
	}
	if message.Info.Role != "user" || len(message.Parts) != 1 || message.Parts[0].MessageID != message.Info.ID {
		t.Fatalf("message = %#v, want user with one part", message)
	}

	messages, err := store.Messages(ctx, info.ID, 0)
	if err != nil {
		t.Fatalf("Messages() error = %v", err)
	}
	if len(messages) != 1 || messages[0].Info.ID != message.Info.ID {
		t.Fatalf("Messages() = %#v, want created message", messages)
	}

	message.Parts[0].Data["text"] = "updated"
	updated, err := store.UpdatePart(ctx, message.Parts[0])
	if err != nil {
		t.Fatalf("UpdatePart() error = %v", err)
	}
	if updated.Data["text"] != "updated" {
		t.Fatalf("updated part = %#v", updated)
	}

	if err := store.RemovePart(ctx, info.ID, message.Info.ID, message.Parts[0].ID); err != nil {
		t.Fatalf("RemovePart() error = %v", err)
	}
	if err := store.RemoveMessage(ctx, info.ID, message.Info.ID); err != nil {
		t.Fatalf("RemoveMessage() error = %v", err)
	}
}
