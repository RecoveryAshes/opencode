package storage

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RecoveryAshes/opencode/internal/domain/session"
	syncdomain "github.com/RecoveryAshes/opencode/internal/domain/sync"
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

func TestSQLiteSessionMetadataAndFilters(t *testing.T) {
	ctx := context.Background()
	store, err := OpenSQLiteSessionStore(filepath.Join(t.TempDir(), "opencode.db"))
	if err != nil {
		t.Fatalf("OpenSQLiteSessionStore() error = %v", err)
	}
	defer closeStore(t, store)

	created, err := store.Create(ctx, session.CreateInput{
		Title:       "Metadata",
		ProjectID:   "proj_1",
		WorkspaceID: "wrk_1",
		Directory:   "/tmp/project",
		Path:        "packages/opencode",
		Agent:       "build",
		Model:       &session.SessionModel{ProviderID: "openai", ID: "gpt", Variant: "fast"},
		Version:     "v1",
		Cost:        1.25,
		Tokens:      &session.TokenUsage{Input: 10, Output: 20, Reasoning: 3, Cache: session.CacheUsage{Read: 4, Write: 5}},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created.ProjectID != "proj_1" || created.WorkspaceID != "wrk_1" || created.Path != "packages/opencode" {
		t.Fatalf("created metadata = %#v", created)
	}

	list, err := store.List(ctx, session.ListFilter{ProjectID: "proj_1", WorkspaceID: "wrk_1", Directory: "/tmp/project"})
	if err != nil {
		t.Fatalf("List(metadata) error = %v", err)
	}
	if len(list) != 1 || list[0].ID != created.ID {
		t.Fatalf("metadata list = %#v, want created session", list)
	}
	pathPrefix := "packages"
	list, err = store.List(ctx, session.ListFilter{Path: &pathPrefix})
	if err != nil {
		t.Fatalf("List(path) error = %v", err)
	}
	if len(list) != 1 || list[0].Model == nil || list[0].Model.ProviderID != "openai" || list[0].Model.ID != "gpt" || list[0].Tokens.Input != 10 {
		t.Fatalf("path list = %#v, want model/tokens metadata", list)
	}

	nextWorkspace := "wrk_2"
	nextProject := "proj_2"
	updated, err := store.Update(ctx, created.ID, session.UpdateInput{ProjectID: &nextProject, WorkspaceID: &nextWorkspace})
	if err != nil {
		t.Fatalf("Update(projectID/workspaceID) error = %v", err)
	}
	if updated.ProjectID != "proj_2" || updated.WorkspaceID != "wrk_2" {
		t.Fatalf("updated project/workspace = %q/%q, want proj_2/wrk_2", updated.ProjectID, updated.WorkspaceID)
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

func TestSQLiteSyncEventStore(t *testing.T) {
	store, err := OpenSQLiteSessionStore(filepath.Join(t.TempDir(), "opencode.db"))
	if err != nil {
		t.Fatalf("OpenSQLiteSessionStore() error = %v", err)
	}
	defer closeStore(t, store)

	events := []syncdomain.Event{{
		ID:          "evt_1",
		AggregateID: "ses_sql_sync",
		Seq:         0,
		Type:        "session.created.1",
		Data:        map[string]any{"sessionID": "ses_sql_sync"},
	}, {
		ID:          "evt_2",
		AggregateID: "ses_sql_sync",
		Seq:         1,
		Type:        "session.updated.1",
		Data:        map[string]any{"sessionID": "ses_sql_sync", "info": map[string]any{"title": "updated"}},
	}}
	if err := store.AppendEvents("ses_sql_sync", events); err != nil {
		t.Fatalf("AppendEvents() error = %v", err)
	}
	if err := store.AppendEvents("ses_sql_sync", events[:1]); err != nil {
		t.Fatalf("AppendEvents() duplicate error = %v", err)
	}

	history, err := store.EventsAfter(map[string]int64{})
	if err != nil {
		t.Fatalf("EventsAfter() error = %v", err)
	}
	if len(history) != 2 || history[0].ID != "evt_1" || history[1].Data["sessionID"] != "ses_sql_sync" {
		t.Fatalf("history = %#v, want inserted events", history)
	}

	history, err = store.EventsAfter(map[string]int64{"ses_sql_sync": 0})
	if err != nil {
		t.Fatalf("EventsAfter(cursor) error = %v", err)
	}
	if len(history) != 1 || history[0].ID != "evt_2" {
		t.Fatalf("cursor history = %#v, want evt_2", history)
	}

	var seq int
	if err := store.db.QueryRow(`SELECT seq FROM event_sequence WHERE aggregate_id = ?`, "ses_sql_sync").Scan(&seq); err != nil {
		t.Fatalf("read event_sequence: %v", err)
	}
	if seq != 1 {
		t.Fatalf("event_sequence seq = %d, want 1", seq)
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
	if got.Parts[0].Type != "text" {
		t.Fatalf("part type = %q, want text", got.Parts[0].Type)
	}

	assistant, err := store.CreateAssistant(ctx, info.ID, session.AssistantInput{
		ParentID: message.Info.ID,
		Agent:    "build",
		Model:    session.ModelRef{ProviderID: "openai-compatible", ModelID: "mock-model"},
		Path:     session.PathInfo{CWD: "/tmp/project", Root: "/tmp/project"},
		Text:     "reply",
		Finish:   "stop",
		Tokens:   session.TokenUsage{Input: 1, Output: 2, Cache: session.CacheUsage{}},
	})
	if err != nil {
		t.Fatalf("CreateAssistant() error = %v", err)
	}
	gotAssistant, err := store.GetMessage(ctx, info.ID, assistant.Info.ID)
	if err != nil {
		t.Fatalf("GetMessage(assistant) error = %v", err)
	}
	if gotAssistant.Info.Role != "assistant" || gotAssistant.Info.ParentID == nil || *gotAssistant.Info.ParentID != message.Info.ID {
		t.Fatalf("assistant = %#v", gotAssistant)
	}
	if len(gotAssistant.Parts) != 2 || gotAssistant.Parts[0].Type != "text" || gotAssistant.Parts[1].Type != "step-finish" {
		t.Fatalf("assistant parts = %#v", gotAssistant.Parts)
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

func TestSQLiteSessionStoreForkChildrenAndRevertMetadata(t *testing.T) {
	ctx := context.Background()
	store, err := OpenSQLiteSessionStore(filepath.Join(t.TempDir(), "opencode.db"))
	if err != nil {
		t.Fatalf("OpenSQLiteSessionStore() error = %v", err)
	}
	defer closeStore(t, store)

	parent, err := store.Create(ctx, session.CreateInput{Title: "parent"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	message, err := store.CreatePrompt(ctx, parent.ID, session.PromptInput{
		Parts: []session.Part{{Type: "text", Data: map[string]any{"text": "hello"}}},
	})
	if err != nil {
		t.Fatalf("CreatePrompt() error = %v", err)
	}

	child, err := store.Fork(ctx, parent.ID, &message.Info.ID)
	if err != nil {
		t.Fatalf("Fork() error = %v", err)
	}
	if child.ParentID == nil || *child.ParentID != parent.ID {
		t.Fatalf("child parent = %#v, want parent id", child.ParentID)
	}
	children, err := store.Children(ctx, parent.ID)
	if err != nil {
		t.Fatalf("Children() error = %v", err)
	}
	if len(children) != 1 || children[0].ID != child.ID {
		t.Fatalf("children = %#v, want forked child", children)
	}
	childMessages, err := store.Messages(ctx, child.ID, 0)
	if err != nil {
		t.Fatalf("Messages(child) error = %v", err)
	}
	if len(childMessages) != 1 || childMessages[0].Info.SessionID != child.ID {
		t.Fatalf("child messages = %#v, want copied message with child session id", childMessages)
	}

	partID := message.Parts[0].ID
	updated, err := store.Update(ctx, parent.ID, session.UpdateInput{
		Revert:  &session.RevertInfo{MessageID: message.Info.ID, PartID: &partID},
		Summary: &session.SummaryInfo{Files: 1, Diffs: []map[string]any{{"path": "file.txt"}}},
		Share:   &session.ShareInfo{URL: "opencode://session/" + string(parent.ID)},
	})
	if err != nil {
		t.Fatalf("Update(revert/share) error = %v", err)
	}
	if updated.Revert == nil || updated.Revert.MessageID != message.Info.ID || updated.Summary == nil || updated.Share == nil {
		t.Fatalf("updated = %#v, want revert summary and share", updated)
	}
	reloaded, err := store.Get(ctx, parent.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if reloaded.Revert == nil || reloaded.Summary == nil || len(reloaded.Summary.Diffs) != 1 || reloaded.Share == nil {
		t.Fatalf("reloaded = %#v, want persisted revert summary and share", reloaded)
	}
	cleared, err := store.Update(ctx, parent.ID, session.UpdateInput{ClearRevert: true, ClearShare: true})
	if err != nil {
		t.Fatalf("Update(clear) error = %v", err)
	}
	if cleared.Revert != nil || cleared.Summary != nil || cleared.Share != nil {
		t.Fatalf("cleared = %#v, want revert summary and share cleared", cleared)
	}
}

func TestSQLiteSessionStoreAssistantToolParts(t *testing.T) {
	ctx := context.Background()
	store, err := OpenSQLiteSessionStore(filepath.Join(t.TempDir(), "opencode.db"))
	if err != nil {
		t.Fatalf("OpenSQLiteSessionStore() error = %v", err)
	}
	defer closeStore(t, store)

	info, err := store.Create(ctx, session.CreateInput{Title: "tools"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	prompt, err := store.CreatePrompt(ctx, info.ID, session.PromptInput{
		Parts: []session.Part{{Type: "text", Data: map[string]any{"text": "read file"}}},
	})
	if err != nil {
		t.Fatalf("CreatePrompt() error = %v", err)
	}

	assistant, err := store.CreateAssistant(ctx, info.ID, session.AssistantInput{
		ParentID: prompt.Info.ID,
		Model:    session.ModelRef{ProviderID: "openai-compatible", ModelID: "mock-model"},
		Finish:   "tool-calls",
		Tools: []session.ToolExecution{{
			CallID:   "call_1",
			Tool:     "read",
			Input:    map[string]any{"filePath": "README.md"},
			Title:    "README.md",
			Output:   "content",
			Metadata: map[string]any{"preview": "content"},
		}},
	})
	if err != nil {
		t.Fatalf("CreateAssistant() error = %v", err)
	}
	got, err := store.GetMessage(ctx, info.ID, assistant.Info.ID)
	if err != nil {
		t.Fatalf("GetMessage() error = %v", err)
	}
	if len(got.Parts) != 2 || got.Parts[0].Type != "tool" {
		t.Fatalf("assistant parts = %#v, want tool and step-finish", got.Parts)
	}
	state, ok := got.Parts[0].Data["state"].(map[string]any)
	if !ok {
		t.Fatalf("tool state = %#v, want object", got.Parts[0].Data["state"])
	}
	if state["status"] != "completed" || state["output"] != "content" || state["title"] != "README.md" {
		t.Fatalf("tool state = %#v, want completed content", state)
	}
}

func closeStore(t *testing.T, store *SQLiteSessionStore) {
	t.Helper()
	if err := store.Close(); err != nil {
		t.Errorf("close store: %v", err)
	}
}
