package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RecoveryAshes/opencode/internal/domain/session"
	"github.com/RecoveryAshes/opencode/internal/integration"
	"github.com/RecoveryAshes/opencode/internal/llm"
	"github.com/RecoveryAshes/opencode/internal/runtime"
	"github.com/RecoveryAshes/opencode/internal/storage"
)

func TestHealth(t *testing.T) {
	server := httptest.NewServer(NewHandler(Options{Version: "test"}))
	defer server.Close()

	for _, path := range []string{"/health", "/global/health"} {
		resp, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatalf("GET %s error = %v", path, err)
		}
		defer closeBody(t, resp)

		var body map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if resp.StatusCode != http.StatusOK || body["ok"] != true || body["service"] != "opencode-go" {
			t.Fatalf("%s status/body = %d %#v, want health ok", path, resp.StatusCode, body)
		}
	}
}

func TestSessionCreateListGetUpdateDelete(t *testing.T) {
	server := httptest.NewServer(NewHandler(Options{Sessions: storage.NewMemorySessionStore()}))
	defer server.Close()

	resp, err := http.Post(server.URL+"/session", "application/json", strings.NewReader(`{"title":"hello"}`))
	if err != nil {
		t.Fatalf("POST /session error = %v", err)
	}
	defer closeBody(t, resp)

	var created session.Info
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode created: %v", err)
	}
	if created.Title != "hello" {
		t.Fatalf("created title = %q, want hello", created.Title)
	}

	resp, err = http.Get(server.URL + "/session")
	if err != nil {
		t.Fatalf("GET /session error = %v", err)
	}
	defer closeBody(t, resp)

	var sessions []session.Info
	if err := json.NewDecoder(resp.Body).Decode(&sessions); err != nil {
		t.Fatalf("decode sessions: %v", err)
	}
	if len(sessions) != 1 || sessions[0].ID != created.ID {
		t.Fatalf("sessions = %#v, want created session", sessions)
	}

	req, err := http.NewRequest(http.MethodPatch, server.URL+"/session/"+string(created.ID), bytes.NewBufferString(`{"title":"updated"}`))
	if err != nil {
		t.Fatalf("new patch request: %v", err)
	}
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH /session/id error = %v", err)
	}
	defer closeBody(t, resp)

	var updated session.Info
	if err := json.NewDecoder(resp.Body).Decode(&updated); err != nil {
		t.Fatalf("decode updated: %v", err)
	}
	if updated.Title != "updated" {
		t.Fatalf("updated title = %q, want updated", updated.Title)
	}

	req, err = http.NewRequest(http.MethodDelete, server.URL+"/session/"+string(created.ID), nil)
	if err != nil {
		t.Fatalf("new delete request: %v", err)
	}
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE /session/id error = %v", err)
	}
	defer closeBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete status = %d, want 200", resp.StatusCode)
	}
}

func TestSessionCreateListMetadataHTTPAPI(t *testing.T) {
	server := httptest.NewServer(NewHandler(Options{Sessions: storage.NewMemorySessionStore()}))
	defer server.Close()

	body := `{"title":"metadata","agent":"build","model":{"id":"gpt","providerID":"openai","variant":"fast"},"tokens":{"input":1,"output":2,"reasoning":3,"cache":{"read":4,"write":5}}}`
	resp, err := http.Post(server.URL+"/session?projectID=proj_1&workspaceID=wrk_1&directory=%2Ftmp%2Fproject&path=packages%2Fopencode", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /session metadata error = %v", err)
	}
	defer closeBody(t, resp)

	var created session.Info
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode created metadata: %v", err)
	}
	if created.ProjectID != "proj_1" || created.WorkspaceID != "wrk_1" || created.Directory != "/tmp/project" || created.Path != "packages/opencode" {
		t.Fatalf("created metadata = %#v", created)
	}
	if created.Model == nil || created.Model.ID != "gpt" || created.Model.ProviderID != "openai" || created.Tokens.Input != 1 {
		t.Fatalf("created model/tokens = %#v", created)
	}

	resp, err = http.Get(server.URL + "/session?workspaceID=wrk_1&path=packages")
	if err != nil {
		t.Fatalf("GET /session metadata error = %v", err)
	}
	defer closeBody(t, resp)

	var sessions []session.Info
	if err := json.NewDecoder(resp.Body).Decode(&sessions); err != nil {
		t.Fatalf("decode metadata sessions: %v", err)
	}
	if len(sessions) != 1 || sessions[0].ID != created.ID {
		t.Fatalf("metadata sessions = %#v, want created session", sessions)
	}
}

func TestSessionListRootsAndMessagesBeforeHTTPAPI(t *testing.T) {
	store := storage.NewMemorySessionStore()
	server := httptest.NewServer(NewHandler(Options{Sessions: store}))
	defer server.Close()

	resp, err := http.Post(server.URL+"/session", "application/json", strings.NewReader(`{"title":"root"}`))
	if err != nil {
		t.Fatalf("POST /session error = %v", err)
	}
	defer closeBody(t, resp)
	var root session.Info
	if err := json.NewDecoder(resp.Body).Decode(&root); err != nil {
		t.Fatalf("decode root: %v", err)
	}
	if _, err := store.Fork(context.Background(), root.ID, nil); err != nil {
		t.Fatalf("Fork() error = %v", err)
	}

	resp, err = http.Get(server.URL + "/session?roots=true")
	if err != nil {
		t.Fatalf("GET /session roots error = %v", err)
	}
	defer closeBody(t, resp)
	var roots []session.Info
	if err := json.NewDecoder(resp.Body).Decode(&roots); err != nil {
		t.Fatalf("decode roots: %v", err)
	}
	if len(roots) != 1 || roots[0].ID != root.ID {
		t.Fatalf("roots = %#v, want root only", roots)
	}

	for _, text := range []string{"first", "second", "third"} {
		if _, err := store.CreatePrompt(context.Background(), root.ID, session.PromptInput{Parts: []session.Part{{Type: "text", Data: map[string]any{"text": text}}}}); err != nil {
			t.Fatalf("CreatePrompt(%s) error = %v", text, err)
		}
	}

	resp, err = http.Get(server.URL + "/session/" + string(root.ID) + "/message?limit=2")
	if err != nil {
		t.Fatalf("GET /session/id/message limit error = %v", err)
	}
	defer closeBody(t, resp)
	cursor := resp.Header.Get("X-Next-Cursor")
	if cursor == "" || !strings.Contains(resp.Header.Get("Link"), "before=") {
		t.Fatalf("headers cursor=%q link=%q, want next cursor headers", cursor, resp.Header.Get("Link"))
	}
	var page []session.WithParts
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		t.Fatalf("decode page: %v", err)
	}
	if len(page) != 2 || page[0].Parts[0].Data["text"] != "second" || page[1].Parts[0].Data["text"] != "third" {
		t.Fatalf("page = %#v, want second/third", page)
	}

	resp, err = http.Get(server.URL + "/session/" + string(root.ID) + "/message?limit=2&before=" + url.QueryEscape(cursor))
	if err != nil {
		t.Fatalf("GET /session/id/message before error = %v", err)
	}
	defer closeBody(t, resp)
	var next []session.WithParts
	if err := json.NewDecoder(resp.Body).Decode(&next); err != nil {
		t.Fatalf("decode next: %v", err)
	}
	if len(next) != 1 || next[0].Parts[0].Data["text"] != "first" {
		t.Fatalf("next = %#v, want first", next)
	}
}

func TestV2SessionListFiltersAndCursorHTTPAPI(t *testing.T) {
	store := storage.NewMemorySessionStore()
	old, err := store.Create(context.Background(), session.CreateInput{
		Title:       "Alpha",
		WorkspaceID: "wrk_1",
		Directory:   "/tmp/project",
		Path:        "packages/opencode",
	})
	if err != nil {
		t.Fatalf("Create(old) error = %v", err)
	}
	newer, err := store.Create(context.Background(), session.CreateInput{
		Title:       "Beta",
		WorkspaceID: "wrk_1",
		Directory:   "/tmp/project",
		Path:        "packages/opencode/server",
	})
	if err != nil {
		t.Fatalf("Create(newer) error = %v", err)
	}
	child, err := store.Fork(context.Background(), newer.ID, nil)
	if err != nil {
		t.Fatalf("Fork() error = %v", err)
	}
	other, err := store.Create(context.Background(), session.CreateInput{
		Title:       "Gamma",
		WorkspaceID: "wrk_2",
		Directory:   "/tmp/other",
		Path:        "other",
	})
	if err != nil {
		t.Fatalf("Create(other) error = %v", err)
	}

	server := httptest.NewServer(NewHandler(Options{Sessions: store}))
	defer server.Close()

	resp, err := http.Get(server.URL + "/api/session?limit=10&workspace=wrk_1&directory=%2Ftmp%2Fproject&path=packages&roots=true&search=a")
	if err != nil {
		t.Fatalf("GET /api/session filters error = %v", err)
	}
	defer closeBody(t, resp)
	var page map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		t.Fatalf("decode v2 filter page: %v", err)
	}
	items := page["items"].([]any)
	if len(items) != 2 || items[0].(map[string]any)["id"] != string(newer.ID) || items[1].(map[string]any)["id"] != string(old.ID) {
		t.Fatalf("filtered items = %#v, want newer/old root sessions only", items)
	}
	for _, excluded := range []session.ID{child.ID, other.ID} {
		for _, item := range items {
			if item.(map[string]any)["id"] == string(excluded) {
				t.Fatalf("filtered items = %#v, unexpectedly included %s", items, excluded)
			}
		}
	}

	resp, err = http.Get(server.URL + "/api/session?limit=1&workspace=wrk_1&directory=%2Ftmp%2Fproject&path=packages&roots=true&search=a")
	if err != nil {
		t.Fatalf("GET /api/session cursor first page error = %v", err)
	}
	defer closeBody(t, resp)
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		t.Fatalf("decode v2 first page: %v", err)
	}
	items = page["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["id"] != string(newer.ID) {
		t.Fatalf("first page = %#v, want newer", page)
	}
	cursor := page["cursor"].(map[string]any)["next"].(string)

	resp, err = http.Get(server.URL + "/api/session?limit=1&cursor=" + url.QueryEscape(cursor) + "&workspace=wrk_1&directory=%2Ftmp%2Fproject")
	if err != nil {
		t.Fatalf("GET /api/session cursor next page error = %v", err)
	}
	defer closeBody(t, resp)
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		t.Fatalf("decode v2 next page: %v", err)
	}
	items = page["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["id"] != string(old.ID) {
		t.Fatalf("next page = %#v, want old session with cursor-preserved filters", page)
	}

	resp, err = http.Get(server.URL + "/api/session?limit=1&cursor=" + url.QueryEscape(cursor) + "&search=beta")
	if err != nil {
		t.Fatalf("GET /api/session cursor with filter error = %v", err)
	}
	defer closeBody(t, resp)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("cursor plus filter status = %d, want 400", resp.StatusCode)
	}
}

func TestSessionMessageHTTPAPI(t *testing.T) {
	server := httptest.NewServer(NewHandler(Options{Sessions: storage.NewMemorySessionStore()}))
	defer server.Close()

	resp, err := http.Post(server.URL+"/session", "application/json", strings.NewReader(`{"title":"chat"}`))
	if err != nil {
		t.Fatalf("POST /session error = %v", err)
	}
	defer closeBody(t, resp)
	var created session.Info
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode session: %v", err)
	}

	body := `{"agent":"build","parts":[{"type":"text","text":"hello"}],"noReply":true}`
	resp, err = http.Post(server.URL+"/session/"+string(created.ID)+"/message", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /session/id/message error = %v", err)
	}
	defer closeBody(t, resp)
	var message session.WithParts
	if err := json.NewDecoder(resp.Body).Decode(&message); err != nil {
		t.Fatalf("decode message: %v", err)
	}
	if message.Info.Role != "user" || len(message.Parts) != 1 {
		t.Fatalf("message = %#v, want user with part", message)
	}

	resp, err = http.Get(server.URL + "/session/" + string(created.ID) + "/message")
	if err != nil {
		t.Fatalf("GET /session/id/message error = %v", err)
	}
	defer closeBody(t, resp)
	var messages []session.WithParts
	if err := json.NewDecoder(resp.Body).Decode(&messages); err != nil {
		t.Fatalf("decode messages: %v", err)
	}
	if len(messages) != 1 || messages[0].Info.ID != message.Info.ID {
		t.Fatalf("messages = %#v, want created message", messages)
	}

	req, err := http.NewRequest(http.MethodDelete, server.URL+"/session/"+string(created.ID)+"/message/"+string(message.Info.ID), nil)
	if err != nil {
		t.Fatalf("new delete message request: %v", err)
	}
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE /session/id/message/id error = %v", err)
	}
	defer closeBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete message status = %d, want 200", resp.StatusCode)
	}
}

func TestSessionForkRevertShareHTTPAPI(t *testing.T) {
	server := httptest.NewServer(NewHandler(Options{Sessions: storage.NewMemorySessionStore()}))
	defer server.Close()

	resp, err := http.Post(server.URL+"/session", "application/json", strings.NewReader(`{"title":"parent"}`))
	if err != nil {
		t.Fatalf("POST /session error = %v", err)
	}
	defer closeBody(t, resp)
	var parent session.Info
	if err := json.NewDecoder(resp.Body).Decode(&parent); err != nil {
		t.Fatalf("decode parent: %v", err)
	}

	resp, err = http.Post(server.URL+"/session/"+string(parent.ID)+"/message", "application/json", strings.NewReader(`{"parts":[{"type":"text","text":"hello"}],"noReply":true}`))
	if err != nil {
		t.Fatalf("POST /session/id/message error = %v", err)
	}
	defer closeBody(t, resp)
	var message session.WithParts
	if err := json.NewDecoder(resp.Body).Decode(&message); err != nil {
		t.Fatalf("decode message: %v", err)
	}

	resp, err = http.Post(server.URL+"/session/"+string(parent.ID)+"/fork", "application/json", strings.NewReader(`{"messageID":`+quoteJSON(string(message.Info.ID))+`}`))
	if err != nil {
		t.Fatalf("POST /session/id/fork error = %v", err)
	}
	defer closeBody(t, resp)
	var child session.Info
	if err := json.NewDecoder(resp.Body).Decode(&child); err != nil {
		t.Fatalf("decode child: %v", err)
	}
	if child.ParentID == nil || *child.ParentID != parent.ID {
		t.Fatalf("child = %#v, want parent id", child)
	}

	resp, err = http.Get(server.URL + "/session/" + string(parent.ID) + "/children")
	if err != nil {
		t.Fatalf("GET /session/id/children error = %v", err)
	}
	defer closeBody(t, resp)
	var children []session.Info
	if err := json.NewDecoder(resp.Body).Decode(&children); err != nil {
		t.Fatalf("decode children: %v", err)
	}
	if len(children) != 1 || children[0].ID != child.ID {
		t.Fatalf("children = %#v, want forked child", children)
	}

	resp, err = http.Post(server.URL+"/session/"+string(parent.ID)+"/revert", "application/json", strings.NewReader(`{"messageID":`+quoteJSON(string(message.Info.ID))+`}`))
	if err != nil {
		t.Fatalf("POST /session/id/revert error = %v", err)
	}
	defer closeBody(t, resp)
	var reverted session.Info
	if err := json.NewDecoder(resp.Body).Decode(&reverted); err != nil {
		t.Fatalf("decode reverted: %v", err)
	}
	if reverted.Revert == nil || reverted.Revert.MessageID != message.Info.ID || reverted.Summary == nil {
		t.Fatalf("reverted = %#v, want revert metadata", reverted)
	}

	resp, err = http.Post(server.URL+"/session/"+string(parent.ID)+"/unrevert", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /session/id/unrevert error = %v", err)
	}
	defer closeBody(t, resp)
	var unreverted session.Info
	if err := json.NewDecoder(resp.Body).Decode(&unreverted); err != nil {
		t.Fatalf("decode unreverted: %v", err)
	}
	if unreverted.Revert != nil || unreverted.Summary != nil {
		t.Fatalf("unreverted = %#v, want clear revert", unreverted)
	}

	resp, err = http.Post(server.URL+"/session/"+string(parent.ID)+"/share", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /session/id/share error = %v", err)
	}
	defer closeBody(t, resp)
	var shared session.Info
	if err := json.NewDecoder(resp.Body).Decode(&shared); err != nil {
		t.Fatalf("decode shared: %v", err)
	}
	if shared.Share == nil || !strings.Contains(shared.Share.URL, string(parent.ID)) {
		t.Fatalf("shared = %#v, want local share marker", shared)
	}

	req, err := http.NewRequest(http.MethodDelete, server.URL+"/session/"+string(parent.ID)+"/share", nil)
	if err != nil {
		t.Fatalf("new unshare request: %v", err)
	}
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE /session/id/share error = %v", err)
	}
	defer closeBody(t, resp)
	var unshared session.Info
	if err := json.NewDecoder(resp.Body).Decode(&unshared); err != nil {
		t.Fatalf("decode unshared: %v", err)
	}
	if unshared.Share != nil {
		t.Fatalf("unshared = %#v, want share cleared", unshared)
	}
}

func TestSessionStateHTTPAPI(t *testing.T) {
	store := storage.NewMemorySessionStore()
	server := httptest.NewServer(NewHandler(Options{Sessions: store}))
	defer server.Close()

	resp, err := http.Post(server.URL+"/session", "application/json", strings.NewReader(`{"title":"state"}`))
	if err != nil {
		t.Fatalf("POST /session error = %v", err)
	}
	defer closeBody(t, resp)
	var created session.Info
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode session: %v", err)
	}

	if err := store.SetTodos(context.Background(), created.ID, []session.TodoInfo{{Content: "migrate", Status: "pending", Priority: "high"}}); err != nil {
		t.Fatalf("SetTodos() error = %v", err)
	}
	if err := store.SetStatus(context.Background(), created.ID, session.StatusInfo{Type: "busy"}); err != nil {
		t.Fatalf("SetStatus() error = %v", err)
	}
	if err := store.SetDiff(context.Background(), created.ID, []map[string]any{{"file": "main.go", "additions": 1.0}}); err != nil {
		t.Fatalf("SetDiff() error = %v", err)
	}

	resp, err = http.Get(server.URL + "/session/" + string(created.ID) + "/todo")
	if err != nil {
		t.Fatalf("GET /session/id/todo error = %v", err)
	}
	defer closeBody(t, resp)
	var todos []session.TodoInfo
	if err := json.NewDecoder(resp.Body).Decode(&todos); err != nil {
		t.Fatalf("decode todos: %v", err)
	}
	if len(todos) != 1 || todos[0].Content != "migrate" {
		t.Fatalf("todos = %#v, want migrate", todos)
	}

	resp, err = http.Get(server.URL + "/session/status")
	if err != nil {
		t.Fatalf("GET /session/status error = %v", err)
	}
	defer closeBody(t, resp)
	var statuses map[string]session.StatusInfo
	if err := json.NewDecoder(resp.Body).Decode(&statuses); err != nil {
		t.Fatalf("decode statuses: %v", err)
	}
	if statuses[string(created.ID)].Type != "busy" {
		t.Fatalf("statuses = %#v, want busy", statuses)
	}

	resp, err = http.Get(server.URL + "/session/" + string(created.ID) + "/diff")
	if err != nil {
		t.Fatalf("GET /session/id/diff error = %v", err)
	}
	defer closeBody(t, resp)
	var diffs []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&diffs); err != nil {
		t.Fatalf("decode diffs: %v", err)
	}
	if len(diffs) != 1 || diffs[0]["file"] != "main.go" {
		t.Fatalf("diffs = %#v, want main.go", diffs)
	}
}

func TestSessionLocalSubroutesHTTPAPI(t *testing.T) {
	root := t.TempDir()
	commandPath := filepath.Join(root, ".opencode", "command", "init.md")
	if err := os.MkdirAll(filepath.Dir(commandPath), 0o755); err != nil {
		t.Fatalf("mkdir command dir: %v", err)
	}
	if err := os.WriteFile(commandPath, []byte("Initialize {{args}}"), 0o644); err != nil {
		t.Fatalf("write init command: %v", err)
	}
	store := storage.NewMemorySessionStore()
	server := httptest.NewServer(NewHandler(Options{Sessions: store}))
	defer server.Close()

	resp, err := http.Post(server.URL+"/session?directory="+urlQueryEscape(root), "application/json", strings.NewReader(`{"title":"subroutes"}`))
	if err != nil {
		t.Fatalf("POST /session error = %v", err)
	}
	defer closeBody(t, resp)
	var created session.Info
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode session: %v", err)
	}

	resp, err = http.Post(server.URL+"/session/"+string(created.ID)+"/init", "application/json", strings.NewReader(`{"providerID":"openai-compatible","modelID":"mock-model","messageID":"msg_init"}`))
	if err != nil {
		t.Fatalf("POST /session/id/init error = %v", err)
	}
	defer closeBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("init status = %d, want 200", resp.StatusCode)
	}

	resp, err = http.Post(server.URL+"/session/"+string(created.ID)+"/shell", "application/json", strings.NewReader(`{"agent":"build","command":"echo hello"}`))
	if err != nil {
		t.Fatalf("POST /session/id/shell error = %v", err)
	}
	defer closeBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("shell status = %d, want 200", resp.StatusCode)
	}
	var shellMessage session.WithParts
	if err := json.NewDecoder(resp.Body).Decode(&shellMessage); err != nil {
		t.Fatalf("decode shell message: %v", err)
	}
	text, _ := shellMessage.Parts[0].Data["text"].(string)
	if shellMessage.Info.Role != "user" || !strings.Contains(text, "echo hello") {
		t.Fatalf("shell message = %#v, want shell prompt", shellMessage)
	}

	resp, err = http.Post(server.URL+"/session/"+string(created.ID)+"/prompt_async", "application/json", strings.NewReader(`{"noReply":true,"parts":[{"type":"text","text":"async"}]}`))
	if err != nil {
		t.Fatalf("POST /session/id/prompt_async error = %v", err)
	}
	defer closeBody(t, resp)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("prompt_async status = %d, want 204", resp.StatusCode)
	}

	if err := store.SetDiff(context.Background(), created.ID, []map[string]any{{"file": "main.go", "additions": 1.0}}); err != nil {
		t.Fatalf("SetDiff() error = %v", err)
	}
	resp, err = http.Post(server.URL+"/session/"+string(created.ID)+"/summarize", "application/json", strings.NewReader(`{"providerID":"openai-compatible","modelID":"mock-model"}`))
	if err != nil {
		t.Fatalf("POST /session/id/summarize error = %v", err)
	}
	defer closeBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("summarize status = %d, want 200", resp.StatusCode)
	}
	reloaded, err := store.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if reloaded.Summary == nil || reloaded.Summary.Files != 1 {
		t.Fatalf("summary = %#v, want persisted diff summary", reloaded.Summary)
	}
	if reloaded.Time.Compacting != nil {
		t.Fatalf("compacting = %#v, want cleared after summarize", reloaded.Time.Compacting)
	}
	messages, err := store.Messages(context.Background(), created.ID, 0)
	if err != nil {
		t.Fatalf("Messages() error = %v", err)
	}
	if len(messages) == 0 {
		t.Fatalf("messages = %#v, want compaction prompt", messages)
	}
	compaction := messages[len(messages)-1]
	if compaction.Info.Role != "user" || compaction.Info.Agent != "build" {
		t.Fatalf("compaction info = %#v, want user/build", compaction.Info)
	}
	if compaction.Info.Model == nil || compaction.Info.Model.ProviderID != "openai-compatible" || compaction.Info.Model.ModelID != "mock-model" {
		t.Fatalf("compaction model = %#v, want summarize payload model", compaction.Info.Model)
	}
	if len(compaction.Parts) != 1 || compaction.Parts[0].Type != "compaction" || compaction.Parts[0].Data["auto"] != false {
		t.Fatalf("compaction parts = %#v, want manual compaction part", compaction.Parts)
	}
}

func TestSessionSummarizeUsesLastUserAgentAndAutoFlag(t *testing.T) {
	store := storage.NewMemorySessionStore()
	server := httptest.NewServer(NewHandler(Options{Sessions: store}))
	defer server.Close()

	resp, err := http.Post(server.URL+"/session", "application/json", strings.NewReader(`{"title":"summarize","agent":"build"}`))
	if err != nil {
		t.Fatalf("POST /session error = %v", err)
	}
	defer closeBody(t, resp)
	var created session.Info
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode session: %v", err)
	}

	resp, err = http.Post(server.URL+"/session/"+string(created.ID)+"/message", "application/json", strings.NewReader(`{"agent":"plan","parts":[{"type":"text","text":"plan first"}],"noReply":true}`))
	if err != nil {
		t.Fatalf("POST /session/id/message error = %v", err)
	}
	defer closeBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("message status = %d, want 200", resp.StatusCode)
	}

	resp, err = http.Post(server.URL+"/session/"+string(created.ID)+"/summarize", "application/json", strings.NewReader(`{"providerID":"anthropic","modelID":"claude-sonnet-4-5","auto":true}`))
	if err != nil {
		t.Fatalf("POST /session/id/summarize error = %v", err)
	}
	defer closeBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("summarize status = %d, want 200", resp.StatusCode)
	}

	messages, err := store.Messages(context.Background(), created.ID, 0)
	if err != nil {
		t.Fatalf("Messages() error = %v", err)
	}
	compaction := messages[len(messages)-1]
	if compaction.Info.Agent != "plan" {
		t.Fatalf("compaction agent = %q, want last user agent plan", compaction.Info.Agent)
	}
	if compaction.Info.Model == nil || compaction.Info.Model.ProviderID != "anthropic" || compaction.Info.Model.ModelID != "claude-sonnet-4-5" {
		t.Fatalf("compaction model = %#v, want summarize payload model", compaction.Info.Model)
	}
	if len(compaction.Parts) != 1 || compaction.Parts[0].Type != "compaction" || compaction.Parts[0].Data["auto"] != true {
		t.Fatalf("compaction parts = %#v, want auto compaction part", compaction.Parts)
	}
}

func TestSessionSummarizeCleansRevertedMessages(t *testing.T) {
	store := storage.NewMemorySessionStore()
	server := httptest.NewServer(NewHandler(Options{Sessions: store}))
	defer server.Close()

	created, err := store.Create(context.Background(), session.CreateInput{Title: "revert cleanup"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	keepID := session.MessageID("msg_keep")
	if _, err := store.CreatePrompt(context.Background(), created.ID, session.PromptInput{
		MessageID: &keepID,
		Parts:     []session.Part{{Type: "text", Data: map[string]any{"text": "keep"}}},
	}); err != nil {
		t.Fatalf("CreatePrompt(keep) error = %v", err)
	}
	revertID := session.MessageID("msg_revert")
	if _, err := store.CreatePrompt(context.Background(), created.ID, session.PromptInput{
		MessageID: &revertID,
		Parts:     []session.Part{{Type: "text", Data: map[string]any{"text": "remove"}}},
	}); err != nil {
		t.Fatalf("CreatePrompt(revert) error = %v", err)
	}
	afterID := session.MessageID("msg_after")
	if _, err := store.CreatePrompt(context.Background(), created.ID, session.PromptInput{
		MessageID: &afterID,
		Parts:     []session.Part{{Type: "text", Data: map[string]any{"text": "remove after"}}},
	}); err != nil {
		t.Fatalf("CreatePrompt(after) error = %v", err)
	}
	if _, err := store.Update(context.Background(), created.ID, session.UpdateInput{Revert: &session.RevertInfo{MessageID: revertID}}); err != nil {
		t.Fatalf("Update(revert) error = %v", err)
	}

	resp, err := http.Post(server.URL+"/session/"+string(created.ID)+"/summarize", "application/json", strings.NewReader(`{"providerID":"openai-compatible","modelID":"mock-model"}`))
	if err != nil {
		t.Fatalf("POST /session/id/summarize error = %v", err)
	}
	defer closeBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("summarize status = %d, want 200", resp.StatusCode)
	}

	messages, err := store.Messages(context.Background(), created.ID, 0)
	if err != nil {
		t.Fatalf("Messages() error = %v", err)
	}
	if len(messages) != 2 || messages[0].Info.ID != keepID || messages[1].Parts[0].Type != "compaction" {
		t.Fatalf("messages = %#v, want keep message and new compaction only", messages)
	}
	reloaded, err := store.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if reloaded.Revert != nil || reloaded.Summary == nil {
		t.Fatalf("session = %#v, want revert cleared and summary refreshed", reloaded)
	}
}

func TestSessionSummarizeCleansRevertedParts(t *testing.T) {
	store := storage.NewMemorySessionStore()
	server := httptest.NewServer(NewHandler(Options{Sessions: store}))
	defer server.Close()

	created, err := store.Create(context.Background(), session.CreateInput{Title: "part cleanup"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	promptID := session.MessageID("msg_parts")
	keptPart := session.PartID("prt_keep")
	revertPart := session.PartID("prt_revert")
	afterPart := session.PartID("prt_after")
	message, err := store.CreatePrompt(context.Background(), created.ID, session.PromptInput{
		MessageID: &promptID,
		Parts: []session.Part{
			{ID: keptPart, Type: "text", Data: map[string]any{"text": "keep"}},
			{ID: revertPart, Type: "text", Data: map[string]any{"text": "remove"}},
			{ID: afterPart, Type: "text", Data: map[string]any{"text": "remove after"}},
		},
	})
	if err != nil {
		t.Fatalf("CreatePrompt(parts) error = %v", err)
	}
	if _, err := store.Update(context.Background(), created.ID, session.UpdateInput{Revert: &session.RevertInfo{MessageID: message.Info.ID, PartID: &revertPart}}); err != nil {
		t.Fatalf("Update(revert part) error = %v", err)
	}

	resp, err := http.Post(server.URL+"/session/"+string(created.ID)+"/summarize", "application/json", strings.NewReader(`{"providerID":"openai-compatible","modelID":"mock-model"}`))
	if err != nil {
		t.Fatalf("POST /session/id/summarize error = %v", err)
	}
	defer closeBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("summarize status = %d, want 200", resp.StatusCode)
	}

	cleaned, err := store.GetMessage(context.Background(), created.ID, promptID)
	if err != nil {
		t.Fatalf("GetMessage() error = %v", err)
	}
	if len(cleaned.Parts) != 1 || cleaned.Parts[0].ID != keptPart {
		t.Fatalf("parts = %#v, want only pre-revert part retained", cleaned.Parts)
	}
}

func TestSessionPromptCleansRevertedMessages(t *testing.T) {
	store := storage.NewMemorySessionStore()
	server := httptest.NewServer(NewHandler(Options{Sessions: store}))
	defer server.Close()

	created, err := store.Create(context.Background(), session.CreateInput{Title: "prompt cleanup"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	keepID := session.MessageID("msg_keep")
	if _, err := store.CreatePrompt(context.Background(), created.ID, session.PromptInput{
		MessageID: &keepID,
		Parts:     []session.Part{{Type: "text", Data: map[string]any{"text": "keep"}}},
	}); err != nil {
		t.Fatalf("CreatePrompt(keep) error = %v", err)
	}
	revertID := session.MessageID("msg_revert")
	if _, err := store.CreatePrompt(context.Background(), created.ID, session.PromptInput{
		MessageID: &revertID,
		Parts:     []session.Part{{Type: "text", Data: map[string]any{"text": "remove"}}},
	}); err != nil {
		t.Fatalf("CreatePrompt(revert) error = %v", err)
	}
	if _, err := store.Update(context.Background(), created.ID, session.UpdateInput{Revert: &session.RevertInfo{MessageID: revertID}}); err != nil {
		t.Fatalf("Update(revert) error = %v", err)
	}

	resp, err := http.Post(server.URL+"/session/"+string(created.ID)+"/message", "application/json", strings.NewReader(`{"parts":[{"type":"text","text":"new prompt"}],"noReply":true}`))
	if err != nil {
		t.Fatalf("POST /session/id/message error = %v", err)
	}
	defer closeBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("message status = %d, want 200", resp.StatusCode)
	}

	messages, err := store.Messages(context.Background(), created.ID, 0)
	if err != nil {
		t.Fatalf("Messages() error = %v", err)
	}
	if len(messages) != 2 || messages[0].Info.ID != keepID || messages[1].Parts[0].Data["text"] != "new prompt" {
		t.Fatalf("messages = %#v, want old revert cleaned before new prompt", messages)
	}
	reloaded, err := store.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if reloaded.Revert != nil {
		t.Fatalf("revert = %#v, want cleared", reloaded.Revert)
	}
}

func TestSessionPromptCreatesAssistantReply(t *testing.T) {
	store := storage.NewMemorySessionStore()
	client := &serverFakeChatClient{}
	server := httptest.NewServer(NewHandler(Options{
		Sessions: store,
		Runtime: &runtime.PromptRuntime{
			Messages: store,
			Client:   client,
			CWD:      "/tmp/project",
			Root:     "/tmp/project",
		},
	}))
	defer server.Close()

	resp, err := http.Post(server.URL+"/session", "application/json", strings.NewReader(`{"title":"chat"}`))
	if err != nil {
		t.Fatalf("POST /session error = %v", err)
	}
	defer closeBody(t, resp)
	var created session.Info
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode session: %v", err)
	}

	body := `{"agent":"build","model":{"providerID":"openai-compatible","modelID":"mock-model"},"parts":[{"type":"text","text":"hello"}]}`
	resp, err = http.Post(server.URL+"/session/"+string(created.ID)+"/message", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /session/id/message error = %v", err)
	}
	defer closeBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("prompt status = %d, want 200", resp.StatusCode)
	}
	var user session.WithParts
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		t.Fatalf("decode user message: %v", err)
	}
	if user.Info.Role != "user" {
		t.Fatalf("prompt response role = %q, want user", user.Info.Role)
	}

	resp, err = http.Get(server.URL + "/session/" + string(created.ID) + "/message")
	if err != nil {
		t.Fatalf("GET /session/id/message error = %v", err)
	}
	defer closeBody(t, resp)
	var messages []session.WithParts
	if err := json.NewDecoder(resp.Body).Decode(&messages); err != nil {
		t.Fatalf("decode messages: %v", err)
	}
	if len(messages) != 2 || messages[1].Info.Role != "assistant" || messages[1].Parts[0].Data["text"] != "assistant reply" {
		t.Fatalf("messages = %#v, want persisted assistant reply", messages)
	}
	if len(client.request.Messages) != 1 || client.request.Messages[0].Content != "hello" {
		t.Fatalf("provider request = %#v", client.request)
	}
}

func TestSessionPromptPersistsConfiguredAgentModel(t *testing.T) {
	root := t.TempDir()
	writeServerFile(t, filepath.Join(root, "opencode.jsonc"), `{
		"model": "custom-only/model",
		"agent": {
			"review": {
				"model": "openai-compatible/mock-model",
				"variant": "high"
			}
		}
	}`)
	store := storage.NewMemorySessionStore()
	server := httptest.NewServer(NewHandler(Options{Sessions: store}))
	defer server.Close()

	resp, err := http.Post(server.URL+"/session?directory="+urlQueryEscape(root), "application/json", strings.NewReader(`{"title":"chat","directory":`+quoteJSON(root)+`}`))
	if err != nil {
		t.Fatalf("POST /session error = %v", err)
	}
	defer closeBody(t, resp)
	var created session.Info
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode session: %v", err)
	}

	body := `{"agent":"review","noReply":true,"parts":[{"type":"text","text":"hello"}]}`
	resp, err = http.Post(server.URL+"/session/"+string(created.ID)+"/message?directory="+urlQueryEscape(root), "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /session/id/message error = %v", err)
	}
	defer closeBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("prompt status = %d, want 200", resp.StatusCode)
	}
	var prompt session.WithParts
	if err := json.NewDecoder(resp.Body).Decode(&prompt); err != nil {
		t.Fatalf("decode prompt: %v", err)
	}
	if prompt.Info.Model == nil ||
		prompt.Info.Model.ProviderID != "openai-compatible" ||
		prompt.Info.Model.ModelID != "mock-model" ||
		prompt.Info.Model.Variant != "high" {
		t.Fatalf("prompt model = %#v, want configured agent model", prompt.Info.Model)
	}
}

func TestCommandHTTPAPIAndSessionCommand(t *testing.T) {
	root := t.TempDir()
	commandPath := filepath.Join(root, ".opencode", "command", "ship.md")
	if err := os.MkdirAll(filepath.Dir(commandPath), 0o755); err != nil {
		t.Fatalf("mkdir command dir: %v", err)
	}
	commandMarkdown := strings.Join([]string{
		"---",
		"description: Ship command",
		"agent: build",
		"model: openai-compatible/mock-model",
		"---",
		"Ship $1",
		"Rest $2",
	}, "\n")
	if err := os.WriteFile(commandPath, []byte(commandMarkdown), 0o644); err != nil {
		t.Fatalf("write command: %v", err)
	}

	store := storage.NewMemorySessionStore()
	client := &serverFakeChatClient{}
	server := httptest.NewServer(NewHandler(Options{
		Sessions: store,
		Runtime: &runtime.PromptRuntime{
			Messages: store,
			Client:   client,
		},
	}))
	defer server.Close()

	resp, err := http.Get(server.URL + "/command?directory=" + urlQueryEscape(root))
	if err != nil {
		t.Fatalf("GET /command error = %v", err)
	}
	defer closeBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /command status = %d, want 200", resp.StatusCode)
	}
	var commands []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&commands); err != nil {
		t.Fatalf("decode commands: %v", err)
	}
	if !hasNamedItem(commands, "ship") {
		t.Fatalf("commands = %#v, want ship command", commands)
	}

	resp, err = http.Post(server.URL+"/session", "application/json", strings.NewReader(`{"title":"command"}`))
	if err != nil {
		t.Fatalf("POST /session error = %v", err)
	}
	defer closeBody(t, resp)
	var created session.Info
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode created: %v", err)
	}

	body := `{"command":"ship","arguments":"one two three","directory":` + quoteJSON(root) + `}`
	resp, err = http.Post(server.URL+"/session/"+string(created.ID)+"/command", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /session/id/command error = %v", err)
	}
	defer closeBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("session command status = %d, want 200", resp.StatusCode)
	}
	var assistant session.WithParts
	if err := json.NewDecoder(resp.Body).Decode(&assistant); err != nil {
		t.Fatalf("decode assistant: %v", err)
	}
	if assistant.Info.Role != "assistant" || assistant.Parts[0].Data["text"] != "assistant reply" {
		t.Fatalf("assistant = %#v, want assistant reply", assistant)
	}
	if len(client.request.Messages) != 1 || !strings.Contains(client.request.Messages[0].Content, "Ship one") || !strings.Contains(client.request.Messages[0].Content, "Rest two three") {
		t.Fatalf("provider request = %#v, want rendered command prompt", client.request)
	}
}

func TestConfigHTTPAPI(t *testing.T) {
	home := t.TempDir()
	xdg := filepath.Join(home, ".config")
	root := filepath.Join(home, "repo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("OPENCODE_TEST_HOME", home)

	writeServerFile(t, filepath.Join(xdg, "opencode", "opencode.jsonc"), `{"model":"global/model"}`)
	writeServerFile(t, filepath.Join(root, "opencode.json"), `{"model":"project/model","instructions":["project.md"]}`)
	writeServerFile(t, filepath.Join(root, ".opencode", "agent", "review.md"), "review agent")

	server := httptest.NewServer(NewHandler(Options{Version: "test"}))
	defer server.Close()

	resp, err := http.Get(server.URL + "/config?directory=" + urlQueryEscape(root))
	if err != nil {
		t.Fatalf("GET /config error = %v", err)
	}
	defer closeBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /config status = %d, want 200", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode config response: %v", err)
	}
	info, ok := body["info"].(map[string]any)
	if !ok {
		t.Fatalf("info = %#v, want object", body["info"])
	}
	if info["model"] != "project/model" {
		t.Fatalf("model = %#v, want project/model", info["model"])
	}
	discovery, ok := body["discovery"].(map[string]any)
	if !ok {
		t.Fatalf("discovery = %#v, want object", body["discovery"])
	}
	agents, ok := discovery["agents"].([]any)
	if !ok || len(agents) != 1 || filepath.Base(agents[0].(string)) != "review.md" {
		t.Fatalf("agents = %#v, want review.md", discovery["agents"])
	}

	req, err := http.NewRequest(http.MethodPatch, server.URL+"/config?directory="+urlQueryEscape(root), strings.NewReader(`{"username":"patched-user","model":"patched/model"}`))
	if err != nil {
		t.Fatalf("new PATCH /config request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH /config error = %v", err)
	}
	defer closeBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PATCH /config status = %d, want 200", resp.StatusCode)
	}
	var updated map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&updated); err != nil {
		t.Fatalf("decode updated config: %v", err)
	}
	if updated["username"] != "patched-user" || updated["model"] != "patched/model" {
		t.Fatalf("updated config = %#v, want patched values", updated)
	}
	localConfig := readServerJSON(t, filepath.Join(root, "config.json"))
	if localConfig["username"] != "patched-user" || localConfig["model"] != "patched/model" {
		t.Fatalf("local config file = %#v, want patched values", localConfig)
	}

	resp, err = http.Get(server.URL + "/global/config")
	if err != nil {
		t.Fatalf("GET /global/config error = %v", err)
	}
	defer closeBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /global/config status = %d, want 200", resp.StatusCode)
	}
	var global map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&global); err != nil {
		t.Fatalf("decode global config: %v", err)
	}
	if global["model"] != "global/model" {
		t.Fatalf("global config = %#v, want global model", global)
	}

	req, err = http.NewRequest(http.MethodPatch, server.URL+"/global/config", strings.NewReader(`{"username":"global-user","shell":""}`))
	if err != nil {
		t.Fatalf("new PATCH /global/config request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH /global/config error = %v", err)
	}
	defer closeBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PATCH /global/config status = %d, want 200", resp.StatusCode)
	}
	var globalUpdated map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&globalUpdated); err != nil {
		t.Fatalf("decode global updated config: %v", err)
	}
	if globalUpdated["username"] != "global-user" || globalUpdated["model"] != "global/model" {
		t.Fatalf("global updated config = %#v, want username and preserved model", globalUpdated)
	}
	if _, ok := globalUpdated["shell"]; ok {
		t.Fatalf("global updated config = %#v, want empty shell omitted", globalUpdated)
	}
	globalConfig := readServerJSON(t, filepath.Join(xdg, "opencode", "opencode.jsonc"))
	if globalConfig["username"] != "global-user" || globalConfig["model"] != "global/model" {
		t.Fatalf("global config file = %#v, want patched values", globalConfig)
	}
	if _, ok := globalConfig["shell"]; ok {
		t.Fatalf("global config file = %#v, want empty shell omitted", globalConfig)
	}
}

func TestProviderHTTPAPIUsesConfigFilters(t *testing.T) {
	home := t.TempDir()
	xdg := filepath.Join(home, ".config")
	root := filepath.Join(home, "repo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("OPENCODE_TEST_HOME", home)
	authPlugin := filepath.Join(root, ".opencode", "plugin", "provider-auth.ts")
	writeServerFile(t, authPlugin, strings.Join([]string{
		"export default async () => ({",
		"  auth: {",
		"    provider: 'local-ai',",
		"    methods: [{",
		"      type: 'api',",
		"      label: 'Local API key',",
		"      prompts: [{ type: 'text', key: 'apiKey', message: 'API key', placeholder: 'sk-local' }],",
		"      authorize: async (inputs) => ({ type: 'success', key: inputs.apiKey, metadata: { source: 'plugin' } })",
		"    }, {",
		"      type: 'oauth',",
		"      label: 'Browser OAuth',",
		"      authorize: async () => ({ url: 'https://auth.example/start', method: 'code', instructions: 'Paste code', callback: async () => ({ type: 'failed' }) })",
		"    }]",
		"  }",
		"})",
		"",
	}, "\n"))
	writeServerFile(t, filepath.Join(root, "opencode.jsonc"), `{
		"enabled_providers": ["anthropic", "local-ai"],
		"provider": {
			"local-ai": {
				"name": "Local AI",
				"models": {
					"local-model": {
						"name": "Local Model",
						"limit": {"context": 32000, "output": 2048}
					}
				}
			}
		}
	}`)

	server := httptest.NewServer(NewHandler(Options{Version: "test"}))
	defer server.Close()

	resp, err := http.Get(server.URL + "/provider?directory=" + urlQueryEscape(root))
	if err != nil {
		t.Fatalf("GET /provider error = %v", err)
	}
	defer closeBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /provider status = %d, want 200", resp.StatusCode)
	}
	var providerBody map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&providerBody); err != nil {
		t.Fatalf("decode provider response: %v", err)
	}
	all, ok := providerBody["all"].([]any)
	if !ok || len(all) != 2 {
		t.Fatalf("all = %#v, want two providers", providerBody["all"])
	}
	if all[0].(map[string]any)["id"] != "anthropic" || all[1].(map[string]any)["id"] != "local-ai" {
		t.Fatalf("all = %#v, want anthropic and local-ai", all)
	}
	defaults := providerBody["default"].(map[string]any)
	if defaults["local-ai"] != "local-model" {
		t.Fatalf("default = %#v, want local model", defaults)
	}
	if connected, ok := providerBody["connected"].([]any); !ok || len(connected) != 0 {
		t.Fatalf("connected = %#v, want empty list", providerBody["connected"])
	}

	resp, err = http.Get(server.URL + "/config/providers?directory=" + urlQueryEscape(root))
	if err != nil {
		t.Fatalf("GET /config/providers error = %v", err)
	}
	defer closeBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /config/providers status = %d, want 200", resp.StatusCode)
	}
	var configBody map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&configBody); err != nil {
		t.Fatalf("decode config providers response: %v", err)
	}
	if _, ok := configBody["providers"].([]any); !ok {
		t.Fatalf("config providers body = %#v, want providers array", configBody)
	}
	if _, ok := configBody["all"]; ok {
		t.Fatalf("config providers body = %#v, did not want all key", configBody)
	}

	resp, err = http.Get(server.URL + "/provider/auth?directory=" + urlQueryEscape(root))
	if err != nil {
		t.Fatalf("GET /provider/auth error = %v", err)
	}
	defer closeBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /provider/auth status = %d, want 200", resp.StatusCode)
	}
	var authMethods map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&authMethods); err != nil {
		t.Fatalf("decode provider auth methods: %v", err)
	}
	localAuth, ok := authMethods["local-ai"].([]any)
	if !ok || len(localAuth) != 2 {
		t.Fatalf("auth methods = %#v, want local-ai plugin methods", authMethods)
	}
	firstMethod := localAuth[0].(map[string]any)
	if firstMethod["type"] != "api" || firstMethod["label"] != "Local API key" {
		t.Fatalf("first auth method = %#v, want api method", firstMethod)
	}
	prompts := firstMethod["prompts"].([]any)
	if prompts[0].(map[string]any)["placeholder"] != "sk-local" {
		t.Fatalf("auth prompts = %#v, want placeholder", prompts)
	}

	resp, err = http.Post(server.URL+"/provider/local-ai/oauth/authorize?directory="+urlQueryEscape(root), "application/json", strings.NewReader(`{"method":"bad"}`))
	if err != nil {
		t.Fatalf("POST /provider/id/oauth/authorize invalid error = %v", err)
	}
	defer closeBody(t, resp)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("provider authorize invalid status = %d, want 400", resp.StatusCode)
	}
	var authError map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&authError); err != nil {
		t.Fatalf("decode provider authorize invalid error: %v", err)
	}
	if authError["name"] != "BadRequest" {
		t.Fatalf("provider authorize invalid error = %#v, want BadRequest", authError)
	}

	resp, err = http.Post(server.URL+"/provider/local-ai/oauth/authorize?directory="+urlQueryEscape(root), "application/json", strings.NewReader(`{"method":0,"inputs":{"apiKey":"secret-key"}}`))
	if err != nil {
		t.Fatalf("POST /provider/id/oauth/authorize error = %v", err)
	}
	defer closeBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("provider authorize status = %d, want 200", resp.StatusCode)
	}
	var apiAuth map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&apiAuth); err != nil {
		t.Fatalf("decode provider authorize: %v", err)
	}
	if apiAuth["type"] != "success" || apiAuth["key"] != "secret-key" || apiAuth["metadata"].(map[string]any)["source"] != "plugin" {
		t.Fatalf("provider authorize = %#v, want plugin api auth result", apiAuth)
	}

	resp, err = http.Post(server.URL+"/provider/local-ai/oauth/authorize?directory="+urlQueryEscape(root), "application/json", strings.NewReader(`{"method":1}`))
	if err != nil {
		t.Fatalf("POST /provider/id/oauth/authorize oauth error = %v", err)
	}
	defer closeBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("provider oauth authorize status = %d, want 200", resp.StatusCode)
	}
	var oauthAuth map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&oauthAuth); err != nil {
		t.Fatalf("decode provider oauth authorize: %v", err)
	}
	if oauthAuth["url"] != "https://auth.example/start" || oauthAuth["method"] != "code" {
		t.Fatalf("provider oauth authorize = %#v, want authorization response", oauthAuth)
	}

	resp, err = http.Post(server.URL+"/provider/local-ai/oauth/callback?directory="+urlQueryEscape(root), "application/json", strings.NewReader(`{"method":0,"code":"abc"}`))
	if err != nil {
		t.Fatalf("POST /provider/id/oauth/callback error = %v", err)
	}
	defer closeBody(t, resp)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("provider callback status = %d, want 400", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&authError); err != nil {
		t.Fatalf("decode provider callback error: %v", err)
	}
	if authError["name"] != "ProviderAuthOauthMissing" {
		t.Fatalf("provider callback error = %#v, want ProviderAuthOauthMissing", authError)
	}
}

func TestFileHTTPAPI(t *testing.T) {
	root := t.TempDir()
	writeServerFile(t, filepath.Join(root, "README.md"), "hello project\nneedle line\n")
	writeServerFile(t, filepath.Join(root, "src", "main.go"), "package main\n\nfunc Run() {}\n")
	writeServerFile(t, filepath.Join(root, "node_modules", "ignored.js"), "needle ignored\n")
	writeServerFile(t, filepath.Join(root, "image.png"), string([]byte{0x89, 'P', 'N', 'G'}))

	server := httptest.NewServer(NewHandler(Options{Version: "test"}))
	defer server.Close()

	resp, err := http.Get(server.URL + "/file?directory=" + urlQueryEscape(root) + "&path=.")
	if err != nil {
		t.Fatalf("GET /file error = %v", err)
	}
	defer closeBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /file status = %d, want 200", resp.StatusCode)
	}
	var nodes []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&nodes); err != nil {
		t.Fatalf("decode nodes: %v", err)
	}
	if len(nodes) < 3 || nodes[0]["type"] != "directory" || nodes[0]["name"] != "node_modules" || nodes[0]["ignored"] != true {
		t.Fatalf("nodes = %#v, want ignored directory sorted first", nodes)
	}

	resp, err = http.Get(server.URL + "/file/content?directory=" + urlQueryEscape(root) + "&path=README.md")
	if err != nil {
		t.Fatalf("GET /file/content text error = %v", err)
	}
	defer closeBody(t, resp)
	var content map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&content); err != nil {
		t.Fatalf("decode text content: %v", err)
	}
	if content["type"] != "text" || content["content"] != "hello project\nneedle line" {
		t.Fatalf("content = %#v, want trimmed text", content)
	}

	resp, err = http.Get(server.URL + "/file/content?directory=" + urlQueryEscape(root) + "&path=image.png")
	if err != nil {
		t.Fatalf("GET /file/content image error = %v", err)
	}
	defer closeBody(t, resp)
	var image map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&image); err != nil {
		t.Fatalf("decode image content: %v", err)
	}
	if image["type"] != "text" || image["encoding"] != "base64" || image["mimeType"] != "image/png" {
		t.Fatalf("image = %#v, want base64 png", image)
	}

	resp, err = http.Get(server.URL + "/find?directory=" + urlQueryEscape(root) + "&pattern=needle")
	if err != nil {
		t.Fatalf("GET /find error = %v", err)
	}
	defer closeBody(t, resp)
	var matches []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&matches); err != nil {
		t.Fatalf("decode matches: %v", err)
	}
	if len(matches) != 1 || matches[0]["path"].(map[string]any)["text"] != "README.md" {
		t.Fatalf("matches = %#v, want README needle only", matches)
	}

	resp, err = http.Get(server.URL + "/find/file?directory=" + urlQueryEscape(root) + "&query=main&type=file&limit=5")
	if err != nil {
		t.Fatalf("GET /find/file error = %v", err)
	}
	defer closeBody(t, resp)
	var files []string
	if err := json.NewDecoder(resp.Body).Decode(&files); err != nil {
		t.Fatalf("decode files: %v", err)
	}
	if len(files) != 1 || files[0] != "src/main.go" {
		t.Fatalf("files = %#v, want src/main.go", files)
	}

	resp, err = http.Get(server.URL + "/find/file?directory=" + urlQueryEscape(root) + "&query=&limit=5")
	if err != nil {
		t.Fatalf("GET /find/file empty error = %v", err)
	}
	defer closeBody(t, resp)
	var dirs []string
	if err := json.NewDecoder(resp.Body).Decode(&dirs); err != nil {
		t.Fatalf("decode dirs: %v", err)
	}
	if len(dirs) != 1 || dirs[0] != "src/" {
		t.Fatalf("dirs = %#v, want non-ignored directories for empty query", dirs)
	}

	resp, err = http.Get(server.URL + "/find/symbol?directory=" + urlQueryEscape(root) + "&query=Run")
	if err != nil {
		t.Fatalf("GET /find/symbol error = %v", err)
	}
	defer closeBody(t, resp)
	var symbols []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&symbols); err != nil {
		t.Fatalf("decode symbols: %v", err)
	}
	if len(symbols) != 1 || symbols[0]["name"] != "Run" || int(symbols[0]["kind"].(float64)) != 12 {
		t.Fatalf("symbols = %#v, want Run function", symbols)
	}
}

func TestFileStatusHTTPAPI(t *testing.T) {
	root := t.TempDir()
	runServerCommand(t, root, "git", "init")
	runServerCommand(t, root, "git", "config", "user.email", "test@example.com")
	runServerCommand(t, root, "git", "config", "user.name", "Test User")
	writeServerFile(t, filepath.Join(root, "tracked.txt"), "one\n")
	runServerCommand(t, root, "git", "add", "tracked.txt")
	runServerCommand(t, root, "git", "-c", "commit.gpgsign=false", "commit", "-m", "initial")
	writeServerFile(t, filepath.Join(root, "tracked.txt"), "one\ntwo\n")
	writeServerFile(t, filepath.Join(root, "new.txt"), "new\n")

	server := httptest.NewServer(NewHandler(Options{Version: "test"}))
	defer server.Close()

	resp, err := http.Get(server.URL + "/file/status?directory=" + urlQueryEscape(root))
	if err != nil {
		t.Fatalf("GET /file/status error = %v", err)
	}
	defer closeBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /file/status status = %d, want 200", resp.StatusCode)
	}
	var status []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	byPath := map[string]map[string]any{}
	for _, item := range status {
		byPath[item["path"].(string)] = item
	}
	if byPath["tracked.txt"]["status"] != "modified" || byPath["new.txt"]["status"] != "added" {
		t.Fatalf("status = %#v, want modified tracked and added new", status)
	}
}

func TestInstanceHTTPAPI(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("OPENCODE_TEST_HOME", home)
	root := t.TempDir()
	runServerCommand(t, root, "git", "init")
	runServerCommand(t, root, "git", "config", "user.email", "test@example.com")
	runServerCommand(t, root, "git", "config", "user.name", "Test User")
	writeServerFile(t, filepath.Join(root, "tracked.txt"), "one\n")
	runServerCommand(t, root, "git", "add", "tracked.txt")
	runServerCommand(t, root, "git", "-c", "commit.gpgsign=false", "commit", "-m", "initial")
	writeServerFile(t, filepath.Join(root, "tracked.txt"), "one\ntwo\n")
	writeServerFile(t, filepath.Join(root, ".opencode", "agent", "reviewer.md"), strings.Join([]string{
		"---",
		"description: Reviews code",
		"mode: subagent",
		"model: openai-compatible/local",
		"---",
		"Review carefully.",
	}, "\n"))
	writeServerFile(t, filepath.Join(root, ".opencode", "skill", "audit.md"), strings.Join([]string{
		"---",
		"name: audit",
		"description: Audit skill",
		"---",
		"Audit content.",
	}, "\n"))
	writeServerFile(t, filepath.Join(root, "opencode.jsonc"), `{
		"formatter": {
			"customfmt": {"command": ["sh"], "extensions": [".txt"]},
			"gofmt": {"disabled": true}
		},
		"lsp": {
			"custom-lsp": {"command": ["custom-lsp"], "extensions": [".txt"]}
		}
	}`)

	server := httptest.NewServer(NewHandler(Options{Version: "test"}))
	defer server.Close()

	resp, err := http.Get(server.URL + "/path?directory=" + urlQueryEscape(root))
	if err != nil {
		t.Fatalf("GET /path error = %v", err)
	}
	defer closeBody(t, resp)
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		resolvedRoot = root
	}
	var paths map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&paths); err != nil {
		t.Fatalf("decode path: %v", err)
	}
	if paths["directory"] != root || paths["worktree"] != resolvedRoot || paths["home"] == "" {
		t.Fatalf("paths = %#v, want local directory/worktree/home", paths)
	}

	resp, err = http.Get(server.URL + "/vcs?directory=" + urlQueryEscape(root))
	if err != nil {
		t.Fatalf("GET /vcs error = %v", err)
	}
	defer closeBody(t, resp)
	var vcs map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&vcs); err != nil {
		t.Fatalf("decode vcs: %v", err)
	}
	if vcs["branch"] == "" {
		t.Fatalf("vcs = %#v, want branch", vcs)
	}

	resp, err = http.Get(server.URL + "/vcs/status?directory=" + urlQueryEscape(root))
	if err != nil {
		t.Fatalf("GET /vcs/status error = %v", err)
	}
	defer closeBody(t, resp)
	var status []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatalf("decode vcs status: %v", err)
	}
	if !hasFileStatus(status, "tracked.txt", "modified") {
		t.Fatalf("vcs status = %#v, want modified tracked.txt", status)
	}

	resp, err = http.Get(server.URL + "/vcs/diff?directory=" + urlQueryEscape(root) + "&mode=git")
	if err != nil {
		t.Fatalf("GET /vcs/diff error = %v", err)
	}
	defer closeBody(t, resp)
	var diffs []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&diffs); err != nil {
		t.Fatalf("decode vcs diff: %v", err)
	}
	if !hasFileDiff(diffs, "tracked.txt", "+two") {
		t.Fatalf("vcs diff = %#v, want tracked diff", diffs)
	}

	resp, err = http.Get(server.URL + "/vcs/diff/raw?directory=" + urlQueryEscape(root))
	if err != nil {
		t.Fatalf("GET /vcs/diff/raw error = %v", err)
	}
	defer closeBody(t, resp)
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read raw diff: %v", err)
	}
	if !strings.Contains(string(raw), "+two") || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/x-diff") {
		t.Fatalf("raw diff = %q content-type=%q, want diff text", raw, resp.Header.Get("Content-Type"))
	}

	resp, err = http.Get(server.URL + "/agent?directory=" + urlQueryEscape(root))
	if err != nil {
		t.Fatalf("GET /agent error = %v", err)
	}
	defer closeBody(t, resp)
	var agents []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&agents); err != nil {
		t.Fatalf("decode agents: %v", err)
	}
	if !hasNamedItem(agents, "build") || !hasNamedItem(agents, "reviewer") {
		t.Fatalf("agents = %#v, want build and reviewer", agents)
	}

	resp, err = http.Get(server.URL + "/skill?directory=" + urlQueryEscape(root))
	if err != nil {
		t.Fatalf("GET /skill error = %v", err)
	}
	defer closeBody(t, resp)
	var skills []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&skills); err != nil {
		t.Fatalf("decode skills: %v", err)
	}
	if len(skills) != 1 || skills[0]["name"] != "audit" || !strings.Contains(skills[0]["content"].(string), "Audit content") {
		t.Fatalf("skills = %#v, want audit skill", skills)
	}

	resp, err = http.Get(server.URL + "/command?directory=" + urlQueryEscape(root))
	if err != nil {
		t.Fatalf("GET /command error = %v", err)
	}
	defer closeBody(t, resp)
	var commands []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&commands); err != nil {
		t.Fatalf("decode commands: %v", err)
	}
	if !hasNamedItem(commands, "init") || !hasNamedItem(commands, "review") {
		t.Fatalf("commands = %#v, want default init/review", commands)
	}

	resp, err = http.Get(server.URL + "/formatter?directory=" + urlQueryEscape(root))
	if err != nil {
		t.Fatalf("GET /formatter error = %v", err)
	}
	defer closeBody(t, resp)
	var formatters []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&formatters); err != nil {
		t.Fatalf("decode formatters: %v", err)
	}
	if !hasNamedItem(formatters, "customfmt") || hasNamedItem(formatters, "gofmt") {
		t.Fatalf("formatters = %#v, want customfmt and no disabled gofmt", formatters)
	}

	resp, err = http.Get(server.URL + "/lsp?directory=" + urlQueryEscape(root))
	if err != nil {
		t.Fatalf("GET /lsp error = %v", err)
	}
	defer closeBody(t, resp)
	var lsps []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&lsps); err != nil {
		t.Fatalf("decode lsp: %v", err)
	}
	if len(lsps) != 1 || lsps[0]["id"] != "custom-lsp" || lsps[0]["root"] != root {
		t.Fatalf("lsp = %#v, want custom-lsp", lsps)
	}

	resp, err = http.Post(server.URL+"/instance/dispose?directory="+urlQueryEscape(root), "application/json", nil)
	if err != nil {
		t.Fatalf("POST /instance/dispose error = %v", err)
	}
	defer closeBody(t, resp)
	var disposed bool
	if err := json.NewDecoder(resp.Body).Decode(&disposed); err != nil {
		t.Fatalf("decode dispose: %v", err)
	}
	if !disposed {
		t.Fatalf("disposed = false, want true")
	}
}

func TestVCSApplyHTTPAPI(t *testing.T) {
	root := t.TempDir()
	runServerCommand(t, root, "git", "init")
	runServerCommand(t, root, "git", "config", "user.email", "test@example.com")
	runServerCommand(t, root, "git", "config", "user.name", "Test User")
	writeServerFile(t, filepath.Join(root, "tracked.txt"), "one\n")
	runServerCommand(t, root, "git", "add", "tracked.txt")
	runServerCommand(t, root, "git", "-c", "commit.gpgsign=false", "commit", "-m", "initial")

	server := httptest.NewServer(NewHandler(Options{Version: "test"}))
	defer server.Close()

	patch := strings.Join([]string{
		"diff --git a/tracked.txt b/tracked.txt",
		"index 5626abf..814f4a4 100644",
		"--- a/tracked.txt",
		"+++ b/tracked.txt",
		"@@ -1 +1,2 @@",
		" one",
		"+two",
		"",
	}, "\n")
	resp, err := http.Post(server.URL+"/vcs/apply?directory="+urlQueryEscape(root), "application/json", strings.NewReader(`{"patch":`+quoteJSON(patch)+`}`))
	if err != nil {
		t.Fatalf("POST /vcs/apply error = %v", err)
	}
	defer closeBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /vcs/apply status = %d, want 200", resp.StatusCode)
	}
	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode apply: %v", err)
	}
	if result["applied"] != true {
		t.Fatalf("apply result = %#v, want applied", result)
	}
	data, err := os.ReadFile(filepath.Join(root, "tracked.txt"))
	if err != nil {
		t.Fatalf("read tracked: %v", err)
	}
	if string(data) != "one\ntwo\n" {
		t.Fatalf("tracked = %q, want applied patch", data)
	}
}

func TestOpenAPIAndEvent(t *testing.T) {
	server := httptest.NewServer(NewHandler(Options{Version: "test"}))
	defer server.Close()

	resp, err := http.Get(server.URL + "/openapi.json")
	if err != nil {
		t.Fatalf("GET /openapi.json error = %v", err)
	}
	defer closeBody(t, resp)

	var spec map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&spec); err != nil {
		t.Fatalf("decode spec: %v", err)
	}
	if spec["openapi"] != "3.1.0" {
		t.Fatalf("openapi = %q, want 3.1.0", spec["openapi"])
	}

	resp, err = http.Get(server.URL + "/event")
	if err != nil {
		t.Fatalf("GET /event error = %v", err)
	}
	defer closeBody(t, resp)
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("content-type = %q, want text/event-stream", resp.Header.Get("Content-Type"))
	}
}

func TestEventStreamPublishesSessionAndMessageEvents(t *testing.T) {
	store := storage.NewMemorySessionStore()
	server := httptest.NewServer(NewHandler(Options{
		Version:  "test",
		Sessions: store,
		Runtime: &runtime.PromptRuntime{
			Messages: store,
			Client:   &serverFakeChatClient{},
		},
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/event", nil)
	if err != nil {
		t.Fatalf("new event request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /event error = %v", err)
	}
	defer closeBody(t, resp)

	events := make(chan event, 16)
	errs := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		readSSEEvents(resp, events, errs)
	}()
	t.Cleanup(func() {
		cancel()
		wg.Wait()
	})

	if got := waitEventType(t, events, errs, "server.connected"); got.Properties["version"] != "test" {
		t.Fatalf("connected event = %#v, want version test", got)
	}

	createResp, err := http.Post(server.URL+"/session", "application/json", strings.NewReader(`{"title":"events"}`))
	if err != nil {
		t.Fatalf("POST /session error = %v", err)
	}
	defer closeBody(t, createResp)
	var created session.Info
	if err := json.NewDecoder(createResp.Body).Decode(&created); err != nil {
		t.Fatalf("decode created: %v", err)
	}
	if got := waitEventType(t, events, errs, "session.created"); got.Properties["sessionID"] != string(created.ID) {
		t.Fatalf("session.created = %#v, want session id %s", got, created.ID)
	}

	body := `{"agent":"build","parts":[{"type":"text","text":"hello"}]}`
	promptResp, err := http.Post(server.URL+"/session/"+string(created.ID)+"/message", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /session/id/message error = %v", err)
	}
	defer closeBody(t, promptResp)
	if promptResp.StatusCode != http.StatusOK {
		t.Fatalf("prompt status = %d, want 200", promptResp.StatusCode)
	}
	if got := waitEventType(t, events, errs, "message.updated"); got.Properties["sessionID"] != string(created.ID) {
		t.Fatalf("message.updated = %#v, want session id %s", got, created.ID)
	}
	if got := waitEventType(t, events, errs, "message.part.updated"); got.Properties["sessionID"] != string(created.ID) {
		t.Fatalf("message.part.updated = %#v, want session id %s", got, created.ID)
	}

	toolResp, err := http.Post(server.URL+"/tool/write", "application/json", strings.NewReader(`{"params":{"filePath":`+quoteJSON(filepath.Join(t.TempDir(), "event.txt"))+`,"content":"event"}}`))
	if err != nil {
		t.Fatalf("POST /tool/write error = %v", err)
	}
	defer closeBody(t, toolResp)
	if got := waitEventType(t, events, errs, "tool.executed"); got.Properties["tool"] != "write" {
		t.Fatalf("tool.executed = %#v, want write", got)
	}
}

func TestQuestionPermissionHTTPAPI(t *testing.T) {
	store := storage.NewMemorySessionStore()
	created, err := store.Create(context.Background(), session.CreateInput{Title: "permissions"})
	if err != nil {
		t.Fatalf("Create session error = %v", err)
	}
	interactions := integration.NewInteractionManager()
	question := interactions.AddQuestion(integration.QuestionRequest{
		ID:        "que_test",
		SessionID: string(created.ID),
		Questions: []integration.QuestionInfo{{
			Question: "Deploy?",
			Header:   "Deploy",
			Options: []integration.QuestionOption{{
				Label:       "Yes",
				Description: "Deploy now.",
			}},
		}},
	})
	permission := interactions.AddPermission(integration.PermissionRequest{
		ID:         "per_global",
		SessionID:  string(created.ID),
		Permission: "shell",
		Patterns:   []string{"npm test"},
		Always:     []string{"npm *"},
		Metadata:   map[string]any{"command": "npm test"},
	})
	sessionPermission := interactions.AddPermission(integration.PermissionRequest{
		ID:         "per_session",
		SessionID:  string(created.ID),
		Permission: "edit",
		Patterns:   []string{"*.go"},
		Always:     []string{"*.go"},
	})

	server := httptest.NewServer(NewHandler(Options{
		Version:  "test",
		Sessions: store,
		Interact: interactions,
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/event", nil)
	if err != nil {
		t.Fatalf("new event request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /event error = %v", err)
	}
	defer closeBody(t, resp)
	events := make(chan event, 16)
	errs := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		readSSEEvents(resp, events, errs)
	}()
	t.Cleanup(func() {
		cancel()
		wg.Wait()
	})
	_ = waitEventType(t, events, errs, "server.connected")

	resp, err = http.Get(server.URL + "/question")
	if err != nil {
		t.Fatalf("GET /question error = %v", err)
	}
	defer closeBody(t, resp)
	var questions []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&questions); err != nil {
		t.Fatalf("decode questions: %v", err)
	}
	if len(questions) != 1 || questions[0]["id"] != string(question.ID) {
		t.Fatalf("questions = %#v, want queued question", questions)
	}

	replyBody := `{"answers":[["Yes"]]}`
	resp, err = http.Post(server.URL+"/question/"+string(question.ID)+"/reply", "application/json", strings.NewReader(replyBody))
	if err != nil {
		t.Fatalf("POST /question/id/reply error = %v", err)
	}
	defer closeBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("question reply status = %d, want 200", resp.StatusCode)
	}
	if got := waitEventType(t, events, errs, "question.replied"); got.Properties["requestID"] != string(question.ID) {
		t.Fatalf("question.replied = %#v, want request id", got)
	}

	resp, err = http.Get(server.URL + "/permission")
	if err != nil {
		t.Fatalf("GET /permission error = %v", err)
	}
	defer closeBody(t, resp)
	var permissions []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&permissions); err != nil {
		t.Fatalf("decode permissions: %v", err)
	}
	globalPermission, ok := findMapByString(permissions, "id", string(permission.ID))
	if !ok || globalPermission["permission"] != "shell" {
		t.Fatalf("permissions = %#v, want queued global permission", permissions)
	}
	scopedPermission, ok := findMapByString(permissions, "id", string(sessionPermission.ID))
	if !ok || scopedPermission["permission"] != "edit" {
		t.Fatalf("permissions = %#v, want queued session-scoped permission", permissions)
	}

	resp, err = http.Post(server.URL+"/permission/"+string(permission.ID)+"/reply", "application/json", strings.NewReader(`{"reply":"always"}`))
	if err != nil {
		t.Fatalf("POST /permission/id/reply error = %v", err)
	}
	defer closeBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("permission reply status = %d, want 200", resp.StatusCode)
	}
	if got := waitEventType(t, events, errs, "permission.replied"); got.Properties["requestID"] != string(permission.ID) || got.Properties["reply"] != "always" {
		t.Fatalf("permission.replied = %#v, want request id and reply", got)
	}

	resp, err = http.Post(server.URL+"/session/"+string(created.ID)+"/permissions/"+string(sessionPermission.ID), "application/json", strings.NewReader(`{"response":"once"}`))
	if err != nil {
		t.Fatalf("POST /session/id/permissions/id error = %v", err)
	}
	defer closeBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("session permission reply status = %d, want 200", resp.StatusCode)
	}
	var sessionPermissionResult bool
	if err := json.NewDecoder(resp.Body).Decode(&sessionPermissionResult); err != nil {
		t.Fatalf("decode session permission reply: %v", err)
	}
	if !sessionPermissionResult {
		t.Fatalf("session permission reply = false, want true")
	}
	if got := waitEventType(t, events, errs, "permission.replied"); got.Properties["sessionID"] != string(created.ID) || got.Properties["requestID"] != string(sessionPermission.ID) || got.Properties["reply"] != "once" {
		t.Fatalf("session permission.replied = %#v, want session-scoped request id and response", got)
	}

	resp, err = http.Get(server.URL + "/question")
	if err != nil {
		t.Fatalf("GET /question after reply error = %v", err)
	}
	defer closeBody(t, resp)
	if err := json.NewDecoder(resp.Body).Decode(&questions); err != nil {
		t.Fatalf("decode empty questions: %v", err)
	}
	if len(questions) != 0 {
		t.Fatalf("questions = %#v, want empty after reply", questions)
	}

	resp, err = http.Get(server.URL + "/permission")
	if err != nil {
		t.Fatalf("GET /permission after replies error = %v", err)
	}
	defer closeBody(t, resp)
	if err := json.NewDecoder(resp.Body).Decode(&permissions); err != nil {
		t.Fatalf("decode empty permissions: %v", err)
	}
	if len(permissions) != 0 {
		t.Fatalf("permissions = %#v, want empty after replies", permissions)
	}
}

func TestV2HTTPAPICompatibility(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	root := t.TempDir()
	writeServerFile(t, filepath.Join(root, "opencode.jsonc"), `{
		"enabled_providers": ["anthropic", "local-ai"],
		"provider": {
			"local-ai": {
				"name": "Local AI",
				"models": {
					"local-model": {"name": "Local Model"}
				}
			}
		}
	}`)

	store := storage.NewMemorySessionStore()
	server := httptest.NewServer(NewHandler(Options{
		Version:  "test",
		Sessions: store,
		Runtime: &runtime.PromptRuntime{
			Messages: store,
			Client:   &serverFakeChatClient{},
		},
	}))
	defer server.Close()

	resp, err := http.Post(server.URL+"/session", "application/json", strings.NewReader(`{"title":"v2"}`))
	if err != nil {
		t.Fatalf("POST /session error = %v", err)
	}
	defer closeBody(t, resp)
	var created session.Info
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode session: %v", err)
	}

	resp, err = http.Get(server.URL + "/api/session?limit=10")
	if err != nil {
		t.Fatalf("GET /api/session error = %v", err)
	}
	defer closeBody(t, resp)
	var sessionsPage map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&sessionsPage); err != nil {
		t.Fatalf("decode v2 sessions: %v", err)
	}
	items := sessionsPage["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["id"] != string(created.ID) {
		t.Fatalf("sessions page = %#v, want created session", sessionsPage)
	}
	if _, ok := sessionsPage["cursor"].(map[string]any); !ok {
		t.Fatalf("sessions cursor = %#v, want cursor object", sessionsPage["cursor"])
	}

	prompt := `{"prompt":{"text":"hello v2"},"delivery":"background"}`
	resp, err = http.Post(server.URL+"/api/session/"+string(created.ID)+"/prompt", "application/json", strings.NewReader(prompt))
	if err != nil {
		t.Fatalf("POST /api/session/id/prompt error = %v", err)
	}
	defer closeBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("v2 prompt status = %d, want 200", resp.StatusCode)
	}
	var promptMessage map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&promptMessage); err != nil {
		t.Fatalf("decode prompt message: %v", err)
	}
	if promptMessage["role"] != "user" {
		t.Fatalf("prompt message = %#v, want user role", promptMessage)
	}

	resp, err = http.Get(server.URL + "/api/session/" + string(created.ID) + "/message?order=asc")
	if err != nil {
		t.Fatalf("GET /api/session/id/message error = %v", err)
	}
	defer closeBody(t, resp)
	var messagesPage map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&messagesPage); err != nil {
		t.Fatalf("decode v2 messages: %v", err)
	}
	messages := messagesPage["items"].([]any)
	if len(messages) != 1 || messages[0].(map[string]any)["role"] != "user" {
		t.Fatalf("messages page = %#v, want one user message", messagesPage)
	}

	resp, err = http.Get(server.URL + "/api/session/" + string(created.ID) + "/context")
	if err != nil {
		t.Fatalf("GET /api/session/id/context error = %v", err)
	}
	defer closeBody(t, resp)
	var contextMessages []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&contextMessages); err != nil {
		t.Fatalf("decode context: %v", err)
	}
	if len(contextMessages) != 1 || contextMessages[0]["role"] != "user" {
		t.Fatalf("context = %#v, want user message", contextMessages)
	}

	compactionID := session.MessageID("msg_compaction")
	if _, err := store.CreatePrompt(context.Background(), created.ID, session.PromptInput{
		MessageID: &compactionID,
		Parts: []session.Part{{
			Type: "compaction",
			Data: map[string]any{"auto": false},
		}},
	}); err != nil {
		t.Fatalf("CreatePrompt(compaction) error = %v", err)
	}
	tailID := session.MessageID("msg_tail")
	if _, err := store.CreatePrompt(context.Background(), created.ID, session.PromptInput{
		MessageID: &tailID,
		Parts: []session.Part{{
			Type: "text",
			Data: map[string]any{"text": "after compaction"},
		}},
	}); err != nil {
		t.Fatalf("CreatePrompt(tail) error = %v", err)
	}
	resp, err = http.Get(server.URL + "/api/session/" + string(created.ID) + "/context")
	if err != nil {
		t.Fatalf("GET /api/session/id/context after compaction error = %v", err)
	}
	defer closeBody(t, resp)
	if err := json.NewDecoder(resp.Body).Decode(&contextMessages); err != nil {
		t.Fatalf("decode context after compaction: %v", err)
	}
	if len(contextMessages) != 3 || contextMessages[0]["id"] != promptMessage["id"] || contextMessages[1]["id"] != string(compactionID) || contextMessages[2]["id"] != string(tailID) {
		t.Fatalf("context = %#v, want uncompleted compaction to keep history", contextMessages)
	}
	if _, err := store.CreateAssistant(context.Background(), created.ID, session.AssistantInput{
		ParentID: compactionID,
		Model:    session.ModelRef{ProviderID: "openai-compatible", ModelID: "mock-model"},
		Summary:  true,
		Text:     "summary",
		Finish:   "stop",
	}); err != nil {
		t.Fatalf("CreateAssistant(summary) error = %v", err)
	}
	resp, err = http.Get(server.URL + "/api/session/" + string(created.ID) + "/context")
	if err != nil {
		t.Fatalf("GET /api/session/id/context after summary error = %v", err)
	}
	defer closeBody(t, resp)
	if err := json.NewDecoder(resp.Body).Decode(&contextMessages); err != nil {
		t.Fatalf("decode context after summary: %v", err)
	}
	if len(contextMessages) != 3 || contextMessages[0]["id"] != string(compactionID) || contextMessages[1]["id"] != string(tailID) || contextMessages[2]["role"] != "assistant" {
		t.Fatalf("context = %#v, want completed compaction boundary with chronological tail and summary", contextMessages)
	}

	tailStartID := session.MessageID("msg_tail_start")
	if _, err := store.CreatePrompt(context.Background(), created.ID, session.PromptInput{
		MessageID: &tailStartID,
		Parts: []session.Part{{
			Type: "text",
			Data: map[string]any{"text": "retained tail"},
		}},
	}); err != nil {
		t.Fatalf("CreatePrompt(tail start) error = %v", err)
	}
	tailCompactionID := session.MessageID("msg_tail_compaction")
	if _, err := store.CreatePrompt(context.Background(), created.ID, session.PromptInput{
		MessageID: &tailCompactionID,
		Parts: []session.Part{{
			Type: "compaction",
			Data: map[string]any{"auto": true, "tail_start_id": string(tailStartID)},
		}},
	}); err != nil {
		t.Fatalf("CreatePrompt(tail compaction) error = %v", err)
	}
	if _, err := store.CreateAssistant(context.Background(), created.ID, session.AssistantInput{
		ParentID: tailCompactionID,
		Model:    session.ModelRef{ProviderID: "openai-compatible", ModelID: "mock-model"},
		Summary:  true,
		Text:     "tail summary",
		Finish:   "stop",
	}); err != nil {
		t.Fatalf("CreateAssistant(tail summary) error = %v", err)
	}
	resp, err = http.Get(server.URL + "/api/session/" + string(created.ID) + "/context")
	if err != nil {
		t.Fatalf("GET /api/session/id/context after tail compaction error = %v", err)
	}
	defer closeBody(t, resp)
	if err := json.NewDecoder(resp.Body).Decode(&contextMessages); err != nil {
		t.Fatalf("decode context after tail compaction: %v", err)
	}
	if len(contextMessages) < 3 || contextMessages[0]["id"] != string(tailCompactionID) || contextMessages[1]["role"] != "assistant" || contextMessages[2]["id"] != string(tailStartID) {
		t.Fatalf("context = %#v, want compaction summary followed by retained tail", contextMessages)
	}

	resp, err = http.Post(server.URL+"/api/session/"+string(created.ID)+"/compact", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /api/session/id/compact error = %v", err)
	}
	defer closeBody(t, resp)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("compact status = %d, want 204", resp.StatusCode)
	}

	resp, err = http.Post(server.URL+"/api/session/"+string(created.ID)+"/wait", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /api/session/id/wait error = %v", err)
	}
	defer closeBody(t, resp)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("wait status = %d, want 204", resp.StatusCode)
	}

	resp, err = http.Get(server.URL + "/api/provider?directory=" + urlQueryEscape(root))
	if err != nil {
		t.Fatalf("GET /api/provider error = %v", err)
	}
	defer closeBody(t, resp)
	var providers []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&providers); err != nil {
		t.Fatalf("decode v2 providers: %v", err)
	}
	if len(providers) != 2 || providers[1]["id"] != "local-ai" {
		t.Fatalf("providers = %#v, want configured providers", providers)
	}

	resp, err = http.Get(server.URL + "/api/provider/local-ai?directory=" + urlQueryEscape(root))
	if err != nil {
		t.Fatalf("GET /api/provider/id error = %v", err)
	}
	defer closeBody(t, resp)
	var provider map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&provider); err != nil {
		t.Fatalf("decode provider: %v", err)
	}
	if provider["id"] != "local-ai" {
		t.Fatalf("provider = %#v, want local-ai", provider)
	}

	resp, err = http.Get(server.URL + "/api/provider/missing?directory=" + urlQueryEscape(root))
	if err != nil {
		t.Fatalf("GET /api/provider/missing error = %v", err)
	}
	defer closeBody(t, resp)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing provider status = %d, want 404", resp.StatusCode)
	}

	resp, err = http.Get(server.URL + "/api/model?directory=" + urlQueryEscape(root))
	if err != nil {
		t.Fatalf("GET /api/model error = %v", err)
	}
	defer closeBody(t, resp)
	var models []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&models); err != nil {
		t.Fatalf("decode models: %v", err)
	}
	if !hasModel(models, "local-ai", "local-model") {
		t.Fatalf("models = %#v, want local model", models)
	}
	if len(models) == 0 || models[0]["id"] != "claude-sonnet-4-5" {
		t.Fatalf("models = %#v, want catalog-priority model first", models)
	}
}

func TestProjectWorkspaceHTTPAPI(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	root := t.TempDir()
	root = serverRealPath(t, root)
	runServerCommand(t, root, "git", "init", "--quiet")
	runServerCommand(t, root, "git", "config", "user.email", "test@example.com")
	runServerCommand(t, root, "git", "config", "user.name", "Test User")
	writeServerFile(t, filepath.Join(root, "README.md"), "hello\n")
	runServerCommand(t, root, "git", "add", "README.md")
	runServerCommand(t, root, "git", "commit", "--quiet", "-m", "init")

	server := httptest.NewServer(NewHandler(Options{Workspace: integration.NewWorkspaceStore()}))
	defer server.Close()

	resp, err := http.Get(server.URL + "/project?directory=" + urlQueryEscape(root))
	if err != nil {
		t.Fatalf("GET /project error = %v", err)
	}
	defer closeBody(t, resp)
	var projects []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&projects); err != nil {
		t.Fatalf("decode projects: %v", err)
	}
	if len(projects) != 1 || projects[0]["vcs"] != "git" || projects[0]["worktree"] != root {
		t.Fatalf("projects = %#v, want current git project", projects)
	}
	projectID := projects[0]["id"].(string)

	req, err := http.NewRequest(http.MethodPatch, server.URL+"/project/"+projectID+"?directory="+urlQueryEscape(root), strings.NewReader(`{"name":"Migrated"}`))
	if err != nil {
		t.Fatalf("new project patch request: %v", err)
	}
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH /project/id error = %v", err)
	}
	defer closeBody(t, resp)
	var updated map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&updated); err != nil {
		t.Fatalf("decode updated project: %v", err)
	}
	if updated["name"] != "Migrated" {
		t.Fatalf("updated project = %#v, want name", updated)
	}

	resp, err = http.Get(server.URL + "/experimental/workspace/adapter?directory=" + urlQueryEscape(root))
	if err != nil {
		t.Fatalf("GET /experimental/workspace/adapter error = %v", err)
	}
	defer closeBody(t, resp)
	var adapters []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&adapters); err != nil {
		t.Fatalf("decode adapters: %v", err)
	}
	if len(adapters) != 1 || adapters[0]["type"] != "worktree" {
		t.Fatalf("adapters = %#v, want worktree adapter", adapters)
	}

	body := `{"type":"worktree"}`
	resp, err = http.Post(server.URL+"/experimental/workspace?directory="+urlQueryEscape(root), "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /experimental/workspace error = %v", err)
	}
	defer closeBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create workspace status = %d, want 200", resp.StatusCode)
	}
	var workspace map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&workspace); err != nil {
		t.Fatalf("decode workspace: %v", err)
	}
	if workspace["type"] != "worktree" || workspace["projectID"] != projectID || workspace["directory"] == "" {
		t.Fatalf("workspace = %#v, want local worktree workspace", workspace)
	}
	workspaceID := workspace["id"].(string)

	resp, err = http.Get(server.URL + "/experimental/workspace?directory=" + urlQueryEscape(root))
	if err != nil {
		t.Fatalf("GET /experimental/workspace error = %v", err)
	}
	defer closeBody(t, resp)
	var workspaces []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&workspaces); err != nil {
		t.Fatalf("decode workspaces: %v", err)
	}
	if len(workspaces) != 1 || workspaces[0]["id"] != workspaceID {
		t.Fatalf("workspaces = %#v, want created workspace", workspaces)
	}

	resp, err = http.Get(server.URL + "/experimental/workspace/status?directory=" + urlQueryEscape(root))
	if err != nil {
		t.Fatalf("GET /experimental/workspace/status error = %v", err)
	}
	defer closeBody(t, resp)
	var statuses []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&statuses); err != nil {
		t.Fatalf("decode statuses: %v", err)
	}
	if len(statuses) != 1 || statuses[0]["workspaceID"] != workspaceID || statuses[0]["status"] != "connected" {
		t.Fatalf("statuses = %#v, want connected workspace", statuses)
	}

	resp, err = http.Post(server.URL+"/experimental/workspace/warp?directory="+urlQueryEscape(root), "application/json", strings.NewReader(`{"id":`+quoteJSON(workspaceID)+`,"sessionID":"ses_test","copyChanges":false}`))
	if err != nil {
		t.Fatalf("POST /experimental/workspace/warp error = %v", err)
	}
	defer closeBody(t, resp)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("warp status = %d, want 204", resp.StatusCode)
	}

	req, err = http.NewRequest(http.MethodDelete, server.URL+"/experimental/workspace/"+workspaceID+"?directory="+urlQueryEscape(root), nil)
	if err != nil {
		t.Fatalf("new workspace delete request: %v", err)
	}
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE /experimental/workspace/id error = %v", err)
	}
	defer closeBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete workspace status = %d, want 200", resp.StatusCode)
	}
}

func TestProjectInitGitHTTPAPI(t *testing.T) {
	root := t.TempDir()
	root = serverRealPath(t, root)
	server := httptest.NewServer(NewHandler(Options{Workspace: integration.NewWorkspaceStore()}))
	defer server.Close()

	resp, err := http.Post(server.URL+"/project/git/init?directory="+urlQueryEscape(root), "application/json", nil)
	if err != nil {
		t.Fatalf("POST /project/git/init error = %v", err)
	}
	defer closeBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("init git status = %d, want 200", resp.StatusCode)
	}
	var project map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&project); err != nil {
		t.Fatalf("decode project: %v", err)
	}
	if project["vcs"] != "git" || project["worktree"] != root {
		t.Fatalf("project = %#v, want initialized git project", project)
	}
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		t.Fatalf(".git missing after init: %v", err)
	}
}

func TestExperimentalToolHTTPAPI(t *testing.T) {
	server := httptest.NewServer(NewHandler(Options{}))
	defer server.Close()

	resp, err := http.Get(server.URL + "/experimental/tool/ids")
	if err != nil {
		t.Fatalf("GET /experimental/tool/ids error = %v", err)
	}
	defer closeBody(t, resp)
	var ids []string
	if err := json.NewDecoder(resp.Body).Decode(&ids); err != nil {
		t.Fatalf("decode ids: %v", err)
	}
	if !stringSliceContains(ids, "read") || !stringSliceContains(ids, "apply_patch") {
		t.Fatalf("ids = %#v, want migrated tool ids", ids)
	}

	resp, err = http.Get(server.URL + "/experimental/tool?provider=openai&model=gpt")
	if err != nil {
		t.Fatalf("GET /experimental/tool error = %v", err)
	}
	defer closeBody(t, resp)
	var tools []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&tools); err != nil {
		t.Fatalf("decode tools: %v", err)
	}
	if !hasToolWithParameters(tools, "read") {
		t.Fatalf("tools = %#v, want read tool with parameters", tools)
	}
}

func TestExperimentalWorktreeHTTPAPI(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	root := t.TempDir()
	root = serverRealPath(t, root)
	runServerCommand(t, root, "git", "init", "--quiet")
	runServerCommand(t, root, "git", "config", "user.email", "test@example.com")
	runServerCommand(t, root, "git", "config", "user.name", "Test User")
	writeServerFile(t, filepath.Join(root, "README.md"), "hello\n")
	runServerCommand(t, root, "git", "add", "README.md")
	runServerCommand(t, root, "git", "commit", "--quiet", "-m", "init")

	server := httptest.NewServer(NewHandler(Options{Workspace: integration.NewWorkspaceStore()}))
	defer server.Close()

	resp, err := http.Get(server.URL + "/experimental/worktree?directory=" + urlQueryEscape(root))
	if err != nil {
		t.Fatalf("GET /experimental/worktree error = %v", err)
	}
	defer closeBody(t, resp)
	var directories []string
	if err := json.NewDecoder(resp.Body).Decode(&directories); err != nil {
		t.Fatalf("decode worktrees: %v", err)
	}
	if len(directories) != 0 {
		t.Fatalf("directories = %#v, want empty before create", directories)
	}

	resp, err = http.Post(server.URL+"/experimental/worktree?directory="+urlQueryEscape(root), "application/json", strings.NewReader(`{"name":"review"}`))
	if err != nil {
		t.Fatalf("POST /experimental/worktree error = %v", err)
	}
	defer closeBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create worktree status = %d, want 200", resp.StatusCode)
	}
	var created map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode created worktree: %v", err)
	}
	directory, _ := created["directory"].(string)
	if created["name"] != "review" || created["branch"] != "opencode/review" || directory == "" {
		t.Fatalf("created = %#v, want named worktree", created)
	}

	resp, err = http.Get(server.URL + "/experimental/worktree?directory=" + urlQueryEscape(root))
	if err != nil {
		t.Fatalf("GET /experimental/worktree after create error = %v", err)
	}
	defer closeBody(t, resp)
	if err := json.NewDecoder(resp.Body).Decode(&directories); err != nil {
		t.Fatalf("decode worktrees after create: %v", err)
	}
	if len(directories) != 1 || directories[0] != directory {
		t.Fatalf("directories = %#v, want created worktree directory", directories)
	}

	writeServerFile(t, filepath.Join(directory, "scratch.txt"), "scratch\n")
	resp, err = http.Post(server.URL+"/experimental/worktree/reset?directory="+urlQueryEscape(root), "application/json", strings.NewReader(`{"directory":`+quoteJSON(directory)+`}`))
	if err != nil {
		t.Fatalf("POST /experimental/worktree/reset error = %v", err)
	}
	defer closeBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reset worktree status = %d, want 200", resp.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(directory, "scratch.txt")); !os.IsNotExist(err) {
		t.Fatalf("scratch.txt exists after reset, stat err = %v", err)
	}

	req, err := http.NewRequest(http.MethodDelete, server.URL+"/experimental/worktree?directory="+urlQueryEscape(root), strings.NewReader(`{"directory":`+quoteJSON(directory)+`}`))
	if err != nil {
		t.Fatalf("new delete worktree request: %v", err)
	}
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE /experimental/worktree error = %v", err)
	}
	defer closeBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete worktree status = %d, want 200", resp.StatusCode)
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatalf("worktree directory exists after delete, stat err = %v", err)
	}
}

func TestSyncHTTPAPI(t *testing.T) {
	store := integration.NewSyncStore()
	eventsBus := newEventBus()
	server := httptest.NewServer(NewHandler(Options{Sync: store, Events: eventsBus}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/event", nil)
	if err != nil {
		t.Fatalf("new event request: %v", err)
	}
	eventResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /event error = %v", err)
	}
	defer closeBody(t, eventResp)
	events := make(chan event, 16)
	errs := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		readSSEEvents(eventResp, events, errs)
	}()
	t.Cleanup(func() {
		cancel()
		wg.Wait()
	})
	_ = waitEventType(t, events, errs, "server.connected")

	resp, err := http.Post(server.URL+"/sync/start", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /sync/start error = %v", err)
	}
	defer closeBody(t, resp)
	var started bool
	if err := json.NewDecoder(resp.Body).Decode(&started); err != nil {
		t.Fatalf("decode sync start: %v", err)
	}
	if !started {
		t.Fatalf("started = false, want true")
	}

	replay := `{"directory":"/tmp","events":[` +
		`{"id":"evt_a","aggregateID":"ses_sync","seq":0,"type":"session.created.1","data":{"sessionID":"ses_sync","info":{"id":"ses_sync","title":"sync"}}},` +
		`{"id":"evt_b","aggregateID":"ses_sync","seq":1,"type":"session.updated.1","data":{"sessionID":"ses_sync","info":{"title":"updated"}}}` +
		`]}`
	resp, err = http.Post(server.URL+"/sync/replay", "application/json", strings.NewReader(replay))
	if err != nil {
		t.Fatalf("POST /sync/replay error = %v", err)
	}
	defer closeBody(t, resp)
	var replayed map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&replayed); err != nil {
		t.Fatalf("decode replay: %v", err)
	}
	if replayed["sessionID"] != "ses_sync" {
		t.Fatalf("replayed = %#v, want ses_sync", replayed)
	}
	if got := waitEventType(t, events, errs, "sync.replayed"); got.Properties["sessionID"] != "ses_sync" {
		t.Fatalf("sync.replayed = %#v, want session id", got)
	}

	resp, err = http.Post(server.URL+"/sync/history", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST /sync/history error = %v", err)
	}
	defer closeBody(t, resp)
	var history []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&history); err != nil {
		t.Fatalf("decode history: %v", err)
	}
	if len(history) != 2 || history[0]["aggregate_id"] != "ses_sync" || history[1]["seq"] != float64(1) {
		t.Fatalf("history = %#v, want replayed events", history)
	}

	resp, err = http.Post(server.URL+"/sync/history", "application/json", strings.NewReader(`{"ses_sync":0}`))
	if err != nil {
		t.Fatalf("POST /sync/history cursor error = %v", err)
	}
	defer closeBody(t, resp)
	if err := json.NewDecoder(resp.Body).Decode(&history); err != nil {
		t.Fatalf("decode cursor history: %v", err)
	}
	if len(history) != 1 || history[0]["id"] != "evt_b" {
		t.Fatalf("cursor history = %#v, want event after seq 0", history)
	}

	resp, err = http.Post(server.URL+"/sync/steal?workspace=wrk_sync", "application/json", strings.NewReader(`{"sessionID":"ses_sync"}`))
	if err != nil {
		t.Fatalf("POST /sync/steal error = %v", err)
	}
	defer closeBody(t, resp)
	var stolen map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&stolen); err != nil {
		t.Fatalf("decode stolen: %v", err)
	}
	if stolen["sessionID"] != "ses_sync" {
		t.Fatalf("stolen = %#v, want session id", stolen)
	}
	if got := waitEventType(t, events, errs, "session.updated"); got.Properties["workspaceID"] != "wrk_sync" {
		t.Fatalf("session.updated = %#v, want workspace id", got)
	}
}

type serverFakeChatClient struct {
	request llm.ChatRequest
}

func (client *serverFakeChatClient) Chat(_ context.Context, request llm.ChatRequest) (llm.ChatResponse, error) {
	client.request = request
	return llm.ChatResponse{
		Text:         "assistant reply",
		FinishReason: "stop",
		Usage: llm.Usage{
			InputTokens:  1,
			OutputTokens: 2,
			TotalTokens:  3,
		},
	}, nil
}

func TestToolHTTPAPI(t *testing.T) {
	server := httptest.NewServer(NewHandler(Options{Version: "test"}))
	defer server.Close()
	root := t.TempDir()

	resp, err := http.Get(server.URL + "/tool")
	if err != nil {
		t.Fatalf("GET /tool error = %v", err)
	}
	defer closeBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /tool status = %d, want 200", resp.StatusCode)
	}

	body := `{"directory":` + quoteJSON(root) + `,"params":{"filePath":"hello.txt","content":"hello tool"}}`
	resp, err = http.Post(server.URL+"/tool/write", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /tool/write error = %v", err)
	}
	defer closeBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /tool/write status = %d, want 200", resp.StatusCode)
	}

	body = `{"directory":` + quoteJSON(root) + `,"params":{"filePath":"hello.txt"}}`
	resp, err = http.Post(server.URL+"/tool/read", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /tool/read error = %v", err)
	}
	defer closeBody(t, resp)
	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode tool read result: %v", err)
	}
	if !strings.Contains(result["output"].(string), "hello tool") {
		t.Fatalf("tool read result = %#v", result)
	}
}

func quoteJSON(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func urlQueryEscape(value string) string {
	return url.QueryEscape(value)
}

func writeServerFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readServerJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("decode %s: %v\n%s", path, err, data)
	}
	return result
}

func runServerCommand(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, output)
	}
}

func serverRealPath(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("resolve %s: %v", path, err)
	}
	return resolved
}

func hasNamedItem(items []map[string]any, name string) bool {
	for _, item := range items {
		if item["name"] == name {
			return true
		}
	}
	return false
}

func findMapByString(items []map[string]any, key string, value string) (map[string]any, bool) {
	for _, item := range items {
		if item[key] == value {
			return item, true
		}
	}
	return nil, false
}

func hasToolWithParameters(items []map[string]any, id string) bool {
	for _, item := range items {
		if item["id"] != id {
			continue
		}
		parameters, ok := item["parameters"].(map[string]any)
		return ok && parameters["type"] == "object"
	}
	return false
}

func stringSliceContains(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}

func hasFileStatus(items []map[string]any, file string, status string) bool {
	for _, item := range items {
		if item["file"] == file && item["status"] == status {
			return true
		}
	}
	return false
}

func hasFileDiff(items []map[string]any, file string, patchContains string) bool {
	for _, item := range items {
		if item["file"] != file {
			continue
		}
		patch, _ := item["patch"].(string)
		if strings.Contains(patch, patchContains) {
			return true
		}
	}
	return false
}

func hasModel(items []map[string]any, providerID string, modelID string) bool {
	for _, item := range items {
		if item["providerID"] == providerID && item["id"] == modelID {
			return true
		}
	}
	return false
}

func readSSEEvents(resp *http.Response, events chan<- event, errs chan<- error) {
	scanner := bufio.NewScanner(resp.Body)
	var data string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data:") {
			data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			continue
		}
		if line != "" || data == "" {
			continue
		}
		var item event
		if err := json.Unmarshal([]byte(data), &item); err != nil {
			select {
			case errs <- err:
			default:
			}
			return
		}
		select {
		case events <- item:
		default:
		}
		data = ""
	}
	if err := scanner.Err(); err != nil && !strings.Contains(err.Error(), "context canceled") {
		select {
		case errs <- err:
		default:
		}
	}
}

func waitEventType(t *testing.T, events <-chan event, errs <-chan error, eventType string) event {
	t.Helper()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for {
		select {
		case err := <-errs:
			t.Fatalf("read SSE event error: %v", err)
		case item := <-events:
			if item.Type == eventType {
				return item
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for event %s", eventType)
		}
	}
}

func closeBody(t *testing.T, resp *http.Response) {
	t.Helper()
	if err := resp.Body.Close(); err != nil {
		t.Errorf("close response body: %v", err)
	}
}
