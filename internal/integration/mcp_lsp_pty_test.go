package integration

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMCPManagerLocalTools(t *testing.T) {
	serverPath := writeMCPFixture(t)
	manager := NewMCPManager()
	status := manager.Add(context.Background(), "mock", MCPConfig{
		Type:    "local",
		Command: []string{"go", "run", serverPath},
		Timeout: 5000,
	})
	if status["mock"].Status != "connected" {
		t.Fatalf("status = %#v, want connected", status)
	}
	tools, err := manager.Tools(context.Background(), "mock")
	if err != nil {
		t.Fatalf("Tools() error = %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("tools = %#v", tools)
	}
	result, err := manager.CallTool(context.Background(), "mock", "echo", map[string]any{"text": "hello"})
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	content := result["content"].([]any)[0].(map[string]any)
	if content["text"] != "hello" {
		t.Fatalf("result = %#v", result)
	}
	manager.Disconnect("mock")
}

func TestLSPFallbackTool(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "main.go")
	if err := os.WriteFile(file, []byte("package main\n\ntype Server struct{}\n\nfunc Run() {}\n\nfunc main() { Run() }\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	result, err := Execute(context.Background(), Request{
		Name:      "lsp",
		Directory: root,
		Params:    map[string]any{"operation": "documentSymbol", "filePath": "main.go", "line": 1, "character": 1},
	})
	if err != nil {
		t.Fatalf("lsp Execute() error = %v", err)
	}
	if !strings.Contains(result.Output, "Server") || !strings.Contains(result.Output, "Run") {
		t.Fatalf("lsp output = %q", result.Output)
	}

	definition, err := Execute(context.Background(), Request{
		Name:      "lsp",
		Directory: root,
		Params:    map[string]any{"operation": "goToDefinition", "filePath": "main.go", "line": 7, "character": 15},
	})
	if err != nil {
		t.Fatalf("lsp goToDefinition Execute() error = %v", err)
	}
	if !strings.Contains(definition.Output, `"line": 5`) || !strings.Contains(definition.Output, "func Run()") {
		t.Fatalf("goToDefinition output = %q", definition.Output)
	}

	references, err := Execute(context.Background(), Request{
		Name:      "lsp",
		Directory: root,
		Params:    map[string]any{"operation": "findReferences", "filePath": "main.go", "line": 7, "character": 15},
	})
	if err != nil {
		t.Fatalf("lsp findReferences Execute() error = %v", err)
	}
	if !strings.Contains(references.Output, `"line": 5`) || !strings.Contains(references.Output, `"line": 7`) {
		t.Fatalf("findReferences output = %q", references.Output)
	}
}

func TestPTYManagerLifecycle(t *testing.T) {
	manager := NewPTYManager()
	info, err := manager.Create(context.Background(), PTYCreateInput{
		Command: "/bin/sh",
		Args:    []string{"-c", "printf ready"},
		Title:   "test",
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	var output string
	for time.Now().Before(deadline) {
		output, _ = manager.Buffer(info.ID)
		if strings.Contains(output, "ready") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(output, "ready") {
		t.Fatalf("buffer = %q, want ready", output)
	}
	if len(manager.List()) != 1 {
		t.Fatalf("List() = %#v", manager.List())
	}
	if !manager.Remove(info.ID) {
		t.Fatalf("Remove() = false")
	}
}

func writeMCPFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mcp.go")
	source := `package main

import (
	"bufio"
	"encoding/json"
	"os"
)

func main() {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var req map[string]any
		_ = json.Unmarshal(scanner.Bytes(), &req)
		id, hasID := req["id"]
		method, _ := req["method"].(string)
		if !hasID {
			continue
		}
		result := map[string]any{}
		switch method {
		case "initialize":
			result = map[string]any{"protocolVersion":"2024-11-05","capabilities":map[string]any{"tools":map[string]any{}}}
		case "tools/list":
			result = map[string]any{"tools":[]map[string]any{{"name":"echo","description":"echo text","inputSchema":map[string]any{"type":"object"}}}}
		case "tools/call":
			params := req["params"].(map[string]any)
			args := params["arguments"].(map[string]any)
			result = map[string]any{"content":[]map[string]any{{"type":"text","text":args["text"]}}}
		}
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"jsonrpc":"2.0","id":id,"result":result})
	}
}
`
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		t.Fatalf("write MCP fixture: %v", err)
	}
	var check map[string]any
	if err := json.Unmarshal([]byte(`{"ok":true}`), &check); err != nil {
		t.Fatalf("json sanity: %v", err)
	}
	return path
}
