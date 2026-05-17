package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/RecoveryAshes/opencode/internal/integration"
)

func TestMCPHTTPAPI(t *testing.T) {
	manager := integration.NewMCPManager()
	server := httptest.NewServer(NewHandler(Options{MCP: manager}))
	defer server.Close()

	body := `{"name":"remote","config":{"type":"remote","url":"` + server.URL + `/health"}}`
	resp, err := http.Post(server.URL+"/mcp", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /mcp error = %v", err)
	}
	defer closeBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /mcp status = %d", resp.StatusCode)
	}

	resp, err = http.Get(server.URL + "/mcp")
	if err != nil {
		t.Fatalf("GET /mcp error = %v", err)
	}
	defer closeBody(t, resp)
	var status map[string]integration.MCPStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if status["remote"].Status != "connected" {
		t.Fatalf("status = %#v", status)
	}
}

func TestPTYHTTPAPI(t *testing.T) {
	manager := integration.NewPTYManager()
	server := httptest.NewServer(NewHandler(Options{PTY: manager}))
	defer server.Close()

	resp, err := http.Get(server.URL + "/pty/shells")
	if err != nil {
		t.Fatalf("GET /pty/shells error = %v", err)
	}
	defer closeBody(t, resp)
	var shells []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&shells); err != nil {
		t.Fatalf("decode shells: %v", err)
	}
	if len(shells) == 0 || shells[0]["path"] == "" || shells[0]["name"] == "" {
		t.Fatalf("shells = %#v, want shell list", shells)
	}

	resp, err = http.Post(server.URL+"/pty", "application/json", strings.NewReader(`{"command":"/bin/sh","args":["-c","printf pty-ready"],"title":"test"}`))
	if err != nil {
		t.Fatalf("POST /pty error = %v", err)
	}
	defer closeBody(t, resp)
	var info integration.PTYInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		t.Fatalf("decode pty info: %v", err)
	}
	if info.ID == "" || info.Title != "test" {
		t.Fatalf("info = %#v", info)
	}

	deadline := time.Now().Add(2 * time.Second)
	var output map[string]string
	for time.Now().Before(deadline) {
		resp, err = http.Get(server.URL + "/pty/" + info.ID + "/buffer")
		if err != nil {
			t.Fatalf("GET /pty/id/buffer error = %v", err)
		}
		output = map[string]string{}
		if err := json.NewDecoder(resp.Body).Decode(&output); err != nil {
			_ = resp.Body.Close()
			t.Fatalf("decode buffer: %v", err)
		}
		_ = resp.Body.Close()
		if strings.Contains(output["output"], "pty-ready") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(output["output"], "pty-ready") {
		t.Fatalf("buffer = %#v", output)
	}

	req, err := http.NewRequest(http.MethodPut, server.URL+"/pty/"+info.ID, bytes.NewBufferString(`{"title":"renamed","size":{"rows":30,"cols":100}}`))
	if err != nil {
		t.Fatalf("new pty update request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT /pty/id error = %v", err)
	}
	defer closeBody(t, resp)
	var updated integration.PTYInfo
	if err := json.NewDecoder(resp.Body).Decode(&updated); err != nil {
		t.Fatalf("decode updated: %v", err)
	}
	if updated.Title != "renamed" {
		t.Fatalf("updated = %#v", updated)
	}
	if updated.Size == nil || updated.Size.Rows != 30 || updated.Size.Cols != 100 {
		t.Fatalf("updated = %#v, want size contract preserved", updated)
	}

	resp, err = http.Post(server.URL+"/pty/"+info.ID+"/connect-token", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /pty/id/connect-token error = %v", err)
	}
	defer closeBody(t, resp)
	var token map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&token); err != nil {
		t.Fatalf("decode token: %v", err)
	}
	if token["ticket"] == "" || token["expires_in"] != float64(60) {
		t.Fatalf("token = %#v, want connect token shape", token)
	}
}
