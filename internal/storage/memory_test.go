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

func TestMemorySessionStoreMetadataAndFilters(t *testing.T) {
	ctx := context.Background()
	store := NewMemorySessionStore()

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
	if created.Slug != "metadata" || created.ProjectID != "proj_1" || created.WorkspaceID != "wrk_1" || created.Path != "packages/opencode" {
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
	if len(list) != 1 || list[0].Model == nil || list[0].Model.ID != "gpt" || list[0].Tokens.Input != 10 {
		t.Fatalf("path list = %#v, want model/tokens metadata", list)
	}

	nextWorkspace := "wrk_2"
	compacting := int64(12345)
	updated, err := store.Update(ctx, created.ID, session.UpdateInput{WorkspaceID: &nextWorkspace})
	if err != nil {
		t.Fatalf("Update(workspaceID) error = %v", err)
	}
	if updated.WorkspaceID != "wrk_2" {
		t.Fatalf("updated workspace = %q, want wrk_2", updated.WorkspaceID)
	}
	updated, err = store.Update(ctx, created.ID, session.UpdateInput{Compacting: &compacting})
	if err != nil {
		t.Fatalf("Update(compacting) error = %v", err)
	}
	if updated.Time.Compacting == nil || *updated.Time.Compacting != compacting {
		t.Fatalf("compacting = %#v, want %d", updated.Time.Compacting, compacting)
	}
	updated, err = store.Update(ctx, created.ID, session.UpdateInput{ClearCompact: true})
	if err != nil {
		t.Fatalf("Update(clear compacting) error = %v", err)
	}
	if updated.Time.Compacting != nil {
		t.Fatalf("compacting = %#v, want nil", updated.Time.Compacting)
	}
}

func TestMemorySessionStoreTodoStatusAndDiff(t *testing.T) {
	ctx := context.Background()
	store := NewMemorySessionStore()
	info, err := store.Create(ctx, session.CreateInput{Title: "state"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	todos := []session.TodoInfo{{Content: "migrate", Status: "in_progress", Priority: "high"}}
	if err := store.SetTodos(ctx, info.ID, todos); err != nil {
		t.Fatalf("SetTodos() error = %v", err)
	}
	gotTodos, err := store.Todos(ctx, info.ID)
	if err != nil {
		t.Fatalf("Todos() error = %v", err)
	}
	if len(gotTodos) != 1 || gotTodos[0].Content != "migrate" {
		t.Fatalf("todos = %#v, want migrate", gotTodos)
	}

	status := session.StatusInfo{Type: "busy"}
	if err := store.SetStatus(ctx, info.ID, status); err != nil {
		t.Fatalf("SetStatus() error = %v", err)
	}
	statuses, err := store.Statuses(ctx)
	if err != nil {
		t.Fatalf("Statuses() error = %v", err)
	}
	if statuses[info.ID].Type != "busy" {
		t.Fatalf("statuses = %#v, want busy", statuses)
	}
	if err := store.SetStatus(ctx, info.ID, session.StatusInfo{Type: "idle"}); err != nil {
		t.Fatalf("SetStatus(idle) error = %v", err)
	}
	statuses, err = store.Statuses(ctx)
	if err != nil {
		t.Fatalf("Statuses() error = %v", err)
	}
	if len(statuses) != 0 {
		t.Fatalf("statuses after idle = %#v, want empty", statuses)
	}

	diffs := []map[string]any{{"file": "main.go", "additions": 2.0, "deletions": 1.0}}
	if err := store.SetDiff(ctx, info.ID, diffs); err != nil {
		t.Fatalf("SetDiff() error = %v", err)
	}
	gotDiffs, err := store.Diff(ctx, info.ID)
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}
	if len(gotDiffs) != 1 || gotDiffs[0]["file"] != "main.go" {
		t.Fatalf("diffs = %#v, want main.go", gotDiffs)
	}
	updated, err := store.Get(ctx, info.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if updated.Summary == nil || updated.Summary.Additions != 2 || updated.Summary.Deletions != 1 || updated.Summary.Files != 1 {
		t.Fatalf("summary = %#v, want diff summary", updated.Summary)
	}
	if err := store.SetDiff(ctx, info.ID, []map[string]any{}); err != nil {
		t.Fatalf("SetDiff(empty) error = %v", err)
	}
	updated, err = store.Get(ctx, info.ID)
	if err != nil {
		t.Fatalf("Get(empty diff summary) error = %v", err)
	}
	if updated.Summary == nil || updated.Summary.Additions != 0 || updated.Summary.Deletions != 0 || updated.Summary.Files != 0 {
		t.Fatalf("empty diff summary = %#v, want zero summary", updated.Summary)
	}
}

func TestMemorySessionStoreListRootsStartAndMessagePage(t *testing.T) {
	ctx := context.Background()
	store := NewMemorySessionStore()

	root, err := store.Create(ctx, session.CreateInput{Title: "root"})
	if err != nil {
		t.Fatalf("Create(root) error = %v", err)
	}
	child, err := store.Fork(ctx, root.ID, nil)
	if err != nil {
		t.Fatalf("Fork() error = %v", err)
	}

	roots, err := store.List(ctx, session.ListFilter{Roots: true})
	if err != nil {
		t.Fatalf("List(roots) error = %v", err)
	}
	if len(roots) != 1 || roots[0].ID != root.ID {
		t.Fatalf("roots = %#v, want only root", roots)
	}
	start := child.Time.Updated + 1
	started, err := store.List(ctx, session.ListFilter{Start: start})
	if err != nil {
		t.Fatalf("List(start) error = %v", err)
	}
	if len(started) != 0 {
		t.Fatalf("started = %#v, want empty after future start", started)
	}

	for _, text := range []string{"first", "second", "third"} {
		if _, err := store.CreatePrompt(ctx, root.ID, session.PromptInput{Parts: []session.Part{{Type: "text", Data: map[string]any{"text": text}}}}); err != nil {
			t.Fatalf("CreatePrompt(%s) error = %v", text, err)
		}
	}
	page, err := store.MessagePage(ctx, root.ID, session.MessageListFilter{Limit: 2})
	if err != nil {
		t.Fatalf("MessagePage() error = %v", err)
	}
	if !page.More || page.Cursor == nil || len(page.Items) != 2 || page.Items[0].Parts[0].Data["text"] != "second" || page.Items[1].Parts[0].Data["text"] != "third" {
		t.Fatalf("page = %#v, want second/third plus cursor", page)
	}
	next, err := store.MessagePage(ctx, root.ID, session.MessageListFilter{Limit: 2, Before: page.Cursor})
	if err != nil {
		t.Fatalf("MessagePage(before) error = %v", err)
	}
	if next.More || len(next.Items) != 1 || next.Items[0].Parts[0].Data["text"] != "first" {
		t.Fatalf("next page = %#v, want first", next)
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

	assistant, err := store.CreateAssistant(ctx, info.ID, session.AssistantInput{
		ParentID: message.Info.ID,
		Agent:    "build",
		Model:    session.ModelRef{ProviderID: "openai-compatible", ModelID: "mock-model"},
		Path:     session.PathInfo{CWD: "/tmp/project", Root: "/tmp/project"},
		Text:     "reply",
		Summary:  true,
		Finish:   "stop",
		Tokens:   session.TokenUsage{Input: 1, Output: 2, Cache: session.CacheUsage{}},
	})
	if err != nil {
		t.Fatalf("CreateAssistant() error = %v", err)
	}
	if assistant.Info.Role != "assistant" || assistant.Info.ParentID == nil || *assistant.Info.ParentID != message.Info.ID {
		t.Fatalf("assistant = %#v", assistant)
	}
	if assistant.Info.Summary == nil || !assistant.Info.Summary.Assistant {
		t.Fatalf("assistant summary = %#v, want assistant marker", assistant.Info.Summary)
	}

	messages, err := store.Messages(ctx, info.ID, 0)
	if err != nil {
		t.Fatalf("Messages() error = %v", err)
	}
	if len(messages) != 2 || messages[0].Info.ID != message.Info.ID || messages[1].Info.ID != assistant.Info.ID {
		t.Fatalf("Messages() = %#v, want user and assistant", messages)
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

func TestMemorySessionStoreForkChildrenAndRevertMetadata(t *testing.T) {
	ctx := context.Background()
	store := NewMemorySessionStore()
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
		Summary: &session.SummaryInfo{Files: 1},
		Share:   &session.ShareInfo{URL: "opencode://session/" + string(parent.ID)},
	})
	if err != nil {
		t.Fatalf("Update(revert/share) error = %v", err)
	}
	if updated.Revert == nil || updated.Revert.MessageID != message.Info.ID || updated.Summary == nil || updated.Share == nil {
		t.Fatalf("updated = %#v, want revert summary and share", updated)
	}
	cleared, err := store.Update(ctx, parent.ID, session.UpdateInput{ClearRevert: true, ClearShare: true})
	if err != nil {
		t.Fatalf("Update(clear) error = %v", err)
	}
	if cleared.Revert != nil || cleared.Summary != nil || cleared.Share != nil {
		t.Fatalf("cleared = %#v, want revert summary and share cleared", cleared)
	}
}

func TestMemorySessionStoreAssistantToolParts(t *testing.T) {
	ctx := context.Background()
	store := NewMemorySessionStore()
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
	if len(assistant.Parts) != 2 || assistant.Parts[0].Type != "tool" || assistant.Parts[1].Type != "step-finish" {
		t.Fatalf("assistant parts = %#v, want tool and step-finish", assistant.Parts)
	}
	tool := assistant.Parts[0]
	if tool.Data["callID"] != "call_1" || tool.Data["tool"] != "read" {
		t.Fatalf("tool part = %#v, want call/read", tool)
	}
	state, ok := tool.Data["state"].(map[string]any)
	if !ok {
		t.Fatalf("tool state = %#v, want object", tool.Data["state"])
	}
	if state["status"] != "completed" || state["output"] != "content" || state["title"] != "README.md" {
		t.Fatalf("tool state = %#v, want completed content", state)
	}
}
