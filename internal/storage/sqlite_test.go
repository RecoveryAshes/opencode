package storage

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RecoveryAshes/opencode/internal/domain/session"
)

func TestSQLiteSessionStoreCreateListUpdateRemove(t *testing.T) {
	ctx := context.Background()
	store, err := OpenSQLiteSessionStore(filepath.Join(t.TempDir(), "opencode.db"))
	if err != nil {
		t.Fatalf("OpenSQLiteSessionStore() error = %v", err)
	}
	defer closeStore(t, store)

	created, err := store.Create(ctx, session.CreateInput{Title: "Hello SQLite"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if !strings.HasPrefix(string(created.ID), "ses") {
		t.Fatalf("id = %q, want ses prefix", created.ID)
	}

	list, err := store.List(ctx, session.ListFilter{Search: "sqlite", Limit: 10})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(list) != 1 || list[0].ID != created.ID {
		t.Fatalf("List() = %#v, want created session", list)
	}

	title := "Renamed Session"
	updated, err := store.Update(ctx, created.ID, session.UpdateInput{Title: &title})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if updated.Title != title {
		t.Fatalf("updated title = %q, want %q", updated.Title, title)
	}

	if err := store.Remove(ctx, created.ID); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if _, err := store.Get(ctx, created.ID); !IsNotFound(err) {
		t.Fatalf("Get() error = %v, want not found", err)
	}
}

func TestSQLiteSessionSchemaMatchesCoreLegacyColumns(t *testing.T) {
	store, err := OpenSQLiteSessionStore(filepath.Join(t.TempDir(), "opencode.db"))
	if err != nil {
		t.Fatalf("OpenSQLiteSessionStore() error = %v", err)
	}
	defer closeStore(t, store)

	rows, err := store.db.Query(`PRAGMA table_info(session)`)
	if err != nil {
		t.Fatalf("table_info(session): %v", err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Errorf("close rows: %v", err)
		}
	}()

	columns := map[string]bool{}
	for rows.Next() {
		var cid int
		var name string
		var columnType string
		var notNull int
		var defaultValue sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			t.Fatalf("scan table_info: %v", err)
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate table_info: %v", err)
	}

	for _, column := range []string{
		"id",
		"project_id",
		"workspace_id",
		"parent_id",
		"slug",
		"directory",
		"path",
		"title",
		"version",
		"permission",
		"agent",
		"model",
		"time_created",
		"time_updated",
		"time_archived",
		"tokens_input",
		"tokens_output",
		"tokens_reasoning",
		"tokens_cache_read",
		"tokens_cache_write",
	} {
		if !columns[column] {
			t.Fatalf("session column %q missing", column)
		}
	}
}

func TestSQLiteSessionStoreMessages(t *testing.T) {
	ctx := context.Background()
	store, err := OpenSQLiteSessionStore(filepath.Join(t.TempDir(), "opencode.db"))
	if err != nil {
		t.Fatalf("OpenSQLiteSessionStore() error = %v", err)
	}
	defer closeStore(t, store)

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

	got, err := store.GetMessage(ctx, info.ID, message.Info.ID)
	if err != nil {
		t.Fatalf("GetMessage() error = %v", err)
	}
	if got.Parts[0].Data["text"] != "hello" {
		t.Fatalf("GetMessage() = %#v, want hello text part", got)
	}

	got.Parts[0].Data["text"] = "updated"
	if _, err := store.UpdatePart(ctx, got.Parts[0]); err != nil {
		t.Fatalf("UpdatePart() error = %v", err)
	}
	got, err = store.GetMessage(ctx, info.ID, message.Info.ID)
	if err != nil {
		t.Fatalf("GetMessage() after update error = %v", err)
	}
	if got.Parts[0].Data["text"] != "updated" {
		t.Fatalf("updated part = %#v", got.Parts[0])
	}

	if err := store.RemovePart(ctx, info.ID, message.Info.ID, got.Parts[0].ID); err != nil {
		t.Fatalf("RemovePart() error = %v", err)
	}
	if err := store.RemoveMessage(ctx, info.ID, message.Info.ID); err != nil {
		t.Fatalf("RemoveMessage() error = %v", err)
	}
}

func closeStore(t *testing.T, store *SQLiteSessionStore) {
	t.Helper()
	if err := store.Close(); err != nil {
		t.Errorf("close store: %v", err)
	}
}
