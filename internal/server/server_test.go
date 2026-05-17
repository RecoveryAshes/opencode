package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RecoveryAshes/opencode/internal/domain/session"
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
	var commands map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&commands); err != nil {
		t.Fatalf("decode commands: %v", err)
	}
	ship, ok := commands["ship"].(map[string]any)
	if !ok || ship["description"] != "Ship command" {
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
