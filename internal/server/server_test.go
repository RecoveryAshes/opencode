package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/RecoveryAshes/opencode/internal/domain/session"
	"github.com/RecoveryAshes/opencode/internal/storage"
)

func TestHealth(t *testing.T) {
	server := httptest.NewServer(NewHandler(Options{Version: "test"}))
	defer server.Close()

	resp, err := http.Get(server.URL + "/health")
	if err != nil {
		t.Fatalf("GET /health error = %v", err)
	}
	defer closeBody(t, resp)

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if resp.StatusCode != http.StatusOK || body["ok"] != true || body["service"] != "opencode-go" {
		t.Fatalf("status/body = %d %#v, want health ok", resp.StatusCode, body)
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

func closeBody(t *testing.T, resp *http.Response) {
	t.Helper()
	if err := resp.Body.Close(); err != nil {
		t.Errorf("close response body: %v", err)
	}
}
