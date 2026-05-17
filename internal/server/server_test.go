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
	interactions := integration.NewInteractionManager()
	question := interactions.AddQuestion(integration.QuestionRequest{
		SessionID: "ses_test",
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
		SessionID:  "ses_test",
		Permission: "shell",
		Patterns:   []string{"npm test"},
		Always:     []string{"npm *"},
		Metadata:   map[string]any{"command": "npm test"},
	})

	server := httptest.NewServer(NewHandler(Options{
		Version:  "test",
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
	if len(permissions) != 1 || permissions[0]["id"] != string(permission.ID) || permissions[0]["permission"] != "shell" {
		t.Fatalf("permissions = %#v, want queued permission", permissions)
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

	resp, err = http.Post(server.URL+"/api/session/"+string(created.ID)+"/wait", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /api/session/id/wait error = %v", err)
	}
	defer closeBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("wait status = %d, want 200", resp.StatusCode)
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
