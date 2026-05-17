package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	"github.com/RecoveryAshes/opencode/internal/storage"
)

func TestRunVersion(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := Run(context.Background(), []string{"version"}, &stdout, &stderr, "test-version")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if strings.TrimSpace(stdout.String()) != "test-version" {
		t.Fatalf("stdout = %q, want test-version", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunRetryDelay(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := Run(context.Background(), []string{"retry-delay", "--attempt", "3"}, &stdout, &stderr, "test")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if strings.TrimSpace(stdout.String()) != "8000" {
		t.Fatalf("stdout = %q, want 8000", stdout.String())
	}
}

func TestRunServeLoadsConfiguredMCP(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "xdg"))
	t.Setenv("OPENCODE_TEST_HOME", filepath.Join(root, "home"))
	t.Chdir(root)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer remote.Close()
	configContent := fmt.Sprintf(`{"mcp":{"remote":{"type":"remote","url":%q,"timeout":1000}}}`, remote.URL)
	if err := os.WriteFile(filepath.Join(root, "opencode.jsonc"), []byte(configContent), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stdout := &lockedBuffer{}
	var stderr bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- Run(ctx, []string{"serve", "--hostname", "127.0.0.1", "--port", "0"}, stdout, &stderr, "test")
	}()

	serverURL := waitForServeURL(t, stdout)
	resp, err := http.Get(serverURL + "/mcp")
	if err != nil {
		t.Fatalf("GET /mcp error = %v; stderr=%q", err, stderr.String())
	}
	defer closeResponseBody(t, resp)
	var status map[string]map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatalf("decode mcp status: %v", err)
	}
	if status["remote"]["status"] != "connected" {
		t.Fatalf("mcp status = %#v, want remote connected", status)
	}

	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("serve exit code = %d, want 0; stderr=%q", code, stderr.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("serve did not exit after cancellation")
	}
}

func TestUnknownCommand(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := Run(context.Background(), []string{"missing"}, &stdout, &stderr, "test")
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "unknown command: missing") {
		t.Fatalf("stderr = %q, want unknown command", stderr.String())
	}
}

func TestRunToolExecutesIntegrationTool(t *testing.T) {
	root := t.TempDir()
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := Run(context.Background(), []string{
		"tool",
		"--directory", root,
		"--params", `{"filePath":"note.txt","content":"migrated\n"}`,
		"write",
	}, &stdout, &stderr, "test")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr = %q", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	data, err := os.ReadFile(filepath.Join(root, "note.txt"))
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if string(data) != "migrated\n" {
		t.Fatalf("file content = %q, want migrated newline", string(data))
	}

	var result integration.Result
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode stdout JSON: %v\nstdout=%s", err, stdout.String())
	}
	if result.Output != "Wrote file successfully." || result.Metadata["exists"] != false {
		t.Fatalf("result = %#v", result)
	}
}

func TestRunToolParamsFile(t *testing.T) {
	root := t.TempDir()
	params := filepath.Join(root, "params.json")
	if err := os.WriteFile(params, []byte(`{"command":"printf from-file"}`), 0o644); err != nil {
		t.Fatalf("write params file: %v", err)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := Run(context.Background(), []string{
		"tool",
		"--directory", root,
		"--params-file", params,
		"shell",
	}, &stdout, &stderr, "test")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr = %q", code, stderr.String())
	}

	var result integration.Result
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode stdout JSON: %v\nstdout=%s", err, stdout.String())
	}
	if result.Output != "from-file" || result.Metadata["exit"] != float64(0) {
		t.Fatalf("result = %#v", result)
	}
}

func TestRunToolRejectsInvalidParams(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := Run(context.Background(), []string{"tool", "--params", `[]`, "read"}, &stdout, &stderr, "test")
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "cannot unmarshal array into Go value") {
		t.Fatalf("stderr = %q, want JSON object error", stderr.String())
	}
}

func TestRunToolRejectsParamsAndParamsFileTogether(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := Run(context.Background(), []string{
		"tool",
		"--params", `{"filePath":"a.txt"}`,
		"--params-file", "params.json",
		"read",
	}, &stdout, &stderr, "test")
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "mutually exclusive") {
		t.Fatalf("stderr = %q, want mutually exclusive error", stderr.String())
	}
}

func TestRunAgentListJSONDiscoversConfiguredAndMarkdownAgents(t *testing.T) {
	root := t.TempDir()
	t.Setenv("OPENCODE_TEST_HOME", filepath.Join(root, "home"))
	configContent := strings.Join([]string{
		"{",
		`  "agent": {`,
		`    "reviewer": {`,
		`      "description": "Review configured changes",`,
		`      "mode": "subagent",`,
		`      "model": "openai-compatible/local-model",`,
		`      "permission": {"edit": "deny"}`,
		"    }",
		"  }",
		"}",
	}, "\n")
	if err := os.WriteFile(filepath.Join(root, "opencode.jsonc"), []byte(configContent), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	agentPath := filepath.Join(root, ".opencode", "agents", "nested", "writer.md")
	if err := os.MkdirAll(filepath.Dir(agentPath), 0o755); err != nil {
		t.Fatalf("mkdir agent dir: %v", err)
	}
	agentContent := strings.Join([]string{
		"---",
		"description: Write release notes",
		"mode: all",
		"---",
		"Write concise release notes.",
	}, "\n")
	if err := os.WriteFile(agentPath, []byte(agentContent), 0o644); err != nil {
		t.Fatalf("write agent: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(context.Background(), []string{"agent", "--directory", root, "--json", "list"}, &stdout, &stderr, "test")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	var agents []map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &agents); err != nil {
		t.Fatalf("decode agents JSON: %v\nstdout=%s", err, stdout.String())
	}
	names := map[string]map[string]any{}
	for _, agent := range agents {
		names[agent["name"].(string)] = agent
	}
	if names["build"]["native"] != true {
		t.Fatalf("build agent = %#v, want native built-in", names["build"])
	}
	reviewer := names["reviewer"]
	if reviewer["description"] != "Review configured changes" || reviewer["mode"] != "subagent" {
		t.Fatalf("reviewer = %#v, want configured metadata", reviewer)
	}
	model := reviewer["model"].(map[string]any)
	if model["providerID"] != "openai-compatible" || model["modelID"] != "local-model" {
		t.Fatalf("reviewer model = %#v, want parsed provider/model", model)
	}
	writer := names["nested/writer"]
	if writer["description"] != "Write release notes" || !strings.Contains(writer["prompt"].(string), "release notes") {
		t.Fatalf("writer = %#v, want markdown metadata and prompt", writer)
	}
}

func TestRunAgentListTextOrdersNativeAgentsFirst(t *testing.T) {
	root := t.TempDir()
	t.Setenv("OPENCODE_TEST_HOME", filepath.Join(root, "home"))
	agentPath := filepath.Join(root, ".opencode", "agent", "aaa.md")
	if err := os.MkdirAll(filepath.Dir(agentPath), 0o755); err != nil {
		t.Fatalf("mkdir agent dir: %v", err)
	}
	if err := os.WriteFile(agentPath, []byte("---\nmode: all\n---\nCustom prompt."), 0o644); err != nil {
		t.Fatalf("write agent: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(context.Background(), []string{"agent", "--directory", root, "list"}, &stdout, &stderr, "test")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	output := stdout.String()
	buildIndex := strings.Index(output, "build (primary)")
	customIndex := strings.Index(output, "aaa (all)")
	if buildIndex < 0 || customIndex < 0 || buildIndex > customIndex {
		t.Fatalf("agent list output = %q, want native build before custom aaa", output)
	}
	if !strings.Contains(output, `  {`) || !strings.Contains(output, `"*"`) {
		t.Fatalf("agent list output = %q, want indented permission JSON", output)
	}
}

func TestRunAgentGetMissing(t *testing.T) {
	root := t.TempDir()
	t.Setenv("OPENCODE_TEST_HOME", filepath.Join(root, "home"))
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := Run(context.Background(), []string{"agent", "--directory", root, "get", "missing"}, &stdout, &stderr, "test")
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "agent not found: missing") {
		t.Fatalf("stderr = %q, want missing agent error", stderr.String())
	}
}

func TestRunMCPListJSONConnectsConfiguredServers(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "xdg"))
	t.Setenv("OPENCODE_TEST_HOME", filepath.Join(root, "home"))
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer remote.Close()
	configContent := fmt.Sprintf(`{
  "mcp": {
    "disabled": {"enabled": false},
    "remote": {"type": "remote", "url": %q, "timeout": 1000}
  }
}`, remote.URL)
	if err := os.WriteFile(filepath.Join(root, "opencode.jsonc"), []byte(configContent), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(context.Background(), []string{"mcp", "--directory", root, "--json", "list"}, &stdout, &stderr, "test")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	var items []map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &items); err != nil {
		t.Fatalf("decode mcp JSON: %v\nstdout=%s", err, stdout.String())
	}
	statuses := map[string]string{}
	for _, item := range items {
		status := item["status"].(map[string]any)
		statuses[item["name"].(string)] = status["status"].(string)
	}
	if statuses["disabled"] != "disabled" || statuses["remote"] != "connected" {
		t.Fatalf("statuses = %#v, want disabled and connected", statuses)
	}
}

func TestRunMCPListTextShowsEmptyState(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "xdg"))
	t.Setenv("OPENCODE_TEST_HOME", filepath.Join(root, "home"))
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := Run(context.Background(), []string{"mcp", "--directory", root, "list"}, &stdout, &stderr, "test")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "No MCP servers configured" {
		t.Fatalf("stdout = %q, want empty state", stdout.String())
	}
}

func TestRunMCPToolsAndCallConfiguredLocalServer(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "xdg"))
	t.Setenv("OPENCODE_TEST_HOME", filepath.Join(root, "home"))
	serverPath := writeAppMCPFixture(t)
	configContent := fmt.Sprintf(`{
  "mcp": {
    "mock": {"type": "local", "command": ["go", "run", %q], "timeout": 5000}
  }
}`, serverPath)
	if err := os.WriteFile(filepath.Join(root, "opencode.jsonc"), []byte(configContent), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(context.Background(), []string{"mcp", "--directory", root, "--json", "tools", "mock"}, &stdout, &stderr, "test")
	if code != 0 {
		t.Fatalf("tools exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	var tools []map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &tools); err != nil {
		t.Fatalf("decode tools JSON: %v\nstdout=%s", err, stdout.String())
	}
	if len(tools) != 1 || tools[0]["name"] != "echo" {
		t.Fatalf("tools = %#v, want echo tool", tools)
	}

	stdout.Reset()
	stderr.Reset()
	code = Run(context.Background(), []string{"mcp", "--directory", root, "--params", `{"text":"hello mcp"}`, "call", "mock", "echo"}, &stdout, &stderr, "test")
	if code != 0 {
		t.Fatalf("call exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	var result map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode call JSON: %v\nstdout=%s", err, stdout.String())
	}
	content := result["content"].([]any)[0].(map[string]any)
	if content["text"] != "hello mcp" {
		t.Fatalf("call result = %#v, want echoed text", result)
	}
}

func TestRunPTYShellsJSON(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := Run(context.Background(), []string{"pty", "--json", "shells"}, &stdout, &stderr, "test")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	var shells []map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &shells); err != nil {
		t.Fatalf("decode shells JSON: %v\nstdout=%s", err, stdout.String())
	}
	if len(shells) == 0 || shells[0]["path"] == "" || shells[0]["name"] == "" {
		t.Fatalf("shells = %#v, want shell list", shells)
	}
}

func TestRunPTYRunCommandJSON(t *testing.T) {
	root := t.TempDir()
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := Run(context.Background(), []string{"pty", "--json", "run", "--cwd", root, "--command", `printf "%s:%s" "$PWD" "$OPENCODE_TERMINAL"`}, &stdout, &stderr, "test")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	var result map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode pty run JSON: %v\nstdout=%s", err, stdout.String())
	}
	info := result["info"].(map[string]any)
	if info["status"] != "exited" || info["cwd"] != root {
		t.Fatalf("info = %#v, want exited in cwd", info)
	}
	output := result["output"].(string)
	if !strings.Contains(output, root+":1") {
		t.Fatalf("output = %q, want cwd and terminal env", output)
	}
}

func TestRunSkillsListAndGet(t *testing.T) {
	root := t.TempDir()
	t.Setenv("OPENCODE_TEST_HOME", filepath.Join(root, "home"))
	skillDir := filepath.Join(root, ".opencode", "skills", "audit")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	content := strings.Join([]string{
		"---",
		"name: audit",
		"description: Audit skill",
		"---",
		"Audit the current change.",
	}, "\n")
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(context.Background(), []string{"skills", "--directory", root, "--json", "list"}, &stdout, &stderr, "test")
	if code != 0 {
		t.Fatalf("list exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	var skills []map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &skills); err != nil {
		t.Fatalf("decode skills JSON: %v\nstdout=%s", err, stdout.String())
	}
	if len(skills) != 1 || skills[0]["name"] != "audit" || skills[0]["description"] != "Audit skill" {
		t.Fatalf("skills = %#v, want audit skill", skills)
	}

	stdout.Reset()
	stderr.Reset()
	code = Run(context.Background(), []string{"skills", "--directory", root, "get", "audit"}, &stdout, &stderr, "test")
	if code != 0 {
		t.Fatalf("get exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "Audit the current change." {
		t.Fatalf("skill content = %q, want content without frontmatter", stdout.String())
	}
}

func TestRunFormattersList(t *testing.T) {
	root := t.TempDir()
	t.Setenv("OPENCODE_TEST_HOME", filepath.Join(root, "home"))
	configContent := strings.Join([]string{
		"{",
		`  "formatter": {`,
		`    "gofmt": {"disabled": true},`,
		`    "customfmt": {"command": ["sh", "-c", "true"], "extensions": [".custom"]}`,
		"  }",
		"}",
	}, "\n")
	if err := os.WriteFile(filepath.Join(root, "opencode.jsonc"), []byte(configContent), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(context.Background(), []string{"formatters", "--directory", root, "--json", "list"}, &stdout, &stderr, "test")
	if code != 0 {
		t.Fatalf("json list exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	var formatters []map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &formatters); err != nil {
		t.Fatalf("decode formatter JSON: %v\nstdout=%s", err, stdout.String())
	}
	byName := map[string]map[string]any{}
	for _, formatter := range formatters {
		byName[formatter["name"].(string)] = formatter
	}
	if _, exists := byName["gofmt"]; exists {
		t.Fatalf("formatters = %#v, want disabled gofmt omitted", formatters)
	}
	custom := byName["customfmt"]
	if custom == nil || custom["enabled"] != true {
		t.Fatalf("customfmt = %#v, want enabled custom formatter", custom)
	}
	extensions := custom["extensions"].([]any)
	if len(extensions) != 1 || extensions[0] != ".custom" {
		t.Fatalf("customfmt extensions = %#v, want .custom", extensions)
	}

	stdout.Reset()
	stderr.Reset()
	code = Run(context.Background(), []string{"formatters", "--directory", root, "list"}, &stdout, &stderr, "test")
	if code != 0 {
		t.Fatalf("text list exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	text := stdout.String()
	if !strings.Contains(text, "customfmt\ttrue\t.custom") || strings.Contains(text, "gofmt") {
		t.Fatalf("formatter text = %q, want customfmt only", text)
	}
}

func TestRunLSPList(t *testing.T) {
	root := t.TempDir()
	t.Setenv("OPENCODE_TEST_HOME", filepath.Join(root, "home"))
	configContent := strings.Join([]string{
		"{",
		`  "lsp": {`,
		`    "custom-lsp": {"command": ["custom-lsp"], "extensions": [".custom"]}`,
		"  }",
		"}",
	}, "\n")
	if err := os.WriteFile(filepath.Join(root, "opencode.jsonc"), []byte(configContent), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(context.Background(), []string{"lsp", "--directory", root, "--json", "list"}, &stdout, &stderr, "test")
	if code != 0 {
		t.Fatalf("json list exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	var statuses []map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &statuses); err != nil {
		t.Fatalf("decode lsp JSON: %v\nstdout=%s", err, stdout.String())
	}
	byID := map[string]map[string]any{}
	for _, status := range statuses {
		byID[status["id"].(string)] = status
	}
	custom := byID["custom-lsp"]
	if custom == nil || custom["status"] != "connected" || custom["root"] != root {
		t.Fatalf("custom-lsp status = %#v, want connected in root; all statuses = %#v", custom, statuses)
	}

	stdout.Reset()
	stderr.Reset()
	code = Run(context.Background(), []string{"lsp", "--directory", root, "status"}, &stdout, &stderr, "test")
	if code != 0 {
		t.Fatalf("text status exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "custom-lsp\tconnected\t"+root) {
		t.Fatalf("lsp text = %q, want custom-lsp status", stdout.String())
	}
}

func TestRunSessionLifecycleWithSQLite(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "opencode.db")
	ctx := context.Background()

	created := runAppJSON[map[string]any](t, ctx, []string{
		"session", "--db", dbPath, "create", "--title", "Go session",
	})
	sessionID, ok := created["id"].(string)
	if !ok || !strings.HasPrefix(sessionID, "ses") {
		t.Fatalf("created session id = %#v, want ses-prefixed string", created["id"])
	}
	if created["title"] != "Go session" {
		t.Fatalf("created title = %#v, want Go session", created["title"])
	}

	listed := runAppJSON[[]map[string]any](t, ctx, []string{
		"session", "--db", dbPath, "list", "--search", "go", "--limit", "1",
	})
	if len(listed) != 1 || listed[0]["id"] != sessionID {
		t.Fatalf("listed sessions = %#v, want created session", listed)
	}

	updated := runAppJSON[map[string]any](t, ctx, []string{
		"session", "--db", dbPath, "update", "--title", "Renamed", sessionID,
	})
	if updated["title"] != "Renamed" {
		t.Fatalf("updated title = %#v, want Renamed", updated["title"])
	}

	prompt := runAppJSON[map[string]any](t, ctx, []string{
		"session", "--db", dbPath, "prompt", "--no-reply", "--text", "hello from cli", sessionID,
	})
	info, ok := prompt["info"].(map[string]any)
	if !ok || info["role"] != "user" {
		t.Fatalf("prompt info = %#v, want user role", prompt["info"])
	}
	parts, ok := prompt["parts"].([]any)
	if !ok || len(parts) != 1 {
		t.Fatalf("prompt parts = %#v, want one part", prompt["parts"])
	}
	firstPart, ok := parts[0].(map[string]any)
	if !ok || firstPart["type"] != "text" || firstPart["text"] != "hello from cli" {
		t.Fatalf("prompt first part = %#v, want text part", parts[0])
	}

	messages := runAppJSON[[]map[string]any](t, ctx, []string{
		"session", "--db", dbPath, "messages", sessionID,
	})
	if len(messages) != 1 {
		t.Fatalf("messages = %#v, want one message", messages)
	}
	messageInfo, ok := messages[0]["info"].(map[string]any)
	if !ok || messageInfo["id"] != info["id"] {
		t.Fatalf("message info = %#v, want prompt message id %#v", messages[0]["info"], info["id"])
	}

	get := runAppJSON[map[string]any](t, ctx, []string{
		"session", "--db", dbPath, "get", sessionID,
	})
	if get["title"] != "Renamed" {
		t.Fatalf("get title = %#v, want Renamed", get["title"])
	}

	forked := runAppJSON[map[string]any](t, ctx, []string{
		"session", "--db", dbPath, "fork", sessionID,
	})
	if forked["parentID"] != sessionID {
		t.Fatalf("forked parentID = %#v, want %s", forked["parentID"], sessionID)
	}
	children := runAppJSON[[]map[string]any](t, ctx, []string{
		"session", "--db", dbPath, "children", sessionID,
	})
	if len(children) != 1 || children[0]["id"] != forked["id"] {
		t.Fatalf("children = %#v, want forked session", children)
	}

	deleted := runAppJSON[bool](t, ctx, []string{
		"session", "--db", dbPath, "delete", sessionID,
	})
	if !deleted {
		t.Fatalf("deleted = false, want true")
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(ctx, []string{"session", "--db", dbPath, "get", sessionID}, &stdout, &stderr, "test")
	if code != 1 {
		t.Fatalf("get deleted exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "session not found") {
		t.Fatalf("stderr = %q, want session not found", stderr.String())
	}
}

func TestRunSessionUsesOPENCODEDB(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "env.db")
	t.Setenv("OPENCODE_DB", dbPath)

	created := runAppJSON[map[string]any](t, context.Background(), []string{
		"session", "create", "--title", "Env DB",
	})
	if created["title"] != "Env DB" {
		t.Fatalf("created title = %#v, want Env DB", created["title"])
	}
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("OPENCODE_DB database was not created: %v", err)
	}
}

func TestRunDBPathMatchesPersistentStorageRules(t *testing.T) {
	home := t.TempDir()
	xdgData := filepath.Join(home, "share")
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", xdgData)
	t.Setenv("OPENCODE_TEST_HOME", home)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(context.Background(), []string{"db", "path"}, &stdout, &stderr, "test")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	want := filepath.Join(xdgData, "opencode", "opencode.db")
	if strings.TrimSpace(stdout.String()) != want {
		t.Fatalf("db path = %q, want %q", strings.TrimSpace(stdout.String()), want)
	}

	stdout.Reset()
	stderr.Reset()
	code = Run(context.Background(), []string{"db", "--db", "custom.db", "path"}, &stdout, &stderr, "test")
	if code != 0 {
		t.Fatalf("relative db path exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	want = filepath.Join(xdgData, "opencode", "custom.db")
	if strings.TrimSpace(stdout.String()) != want {
		t.Fatalf("relative db path = %q, want %q", strings.TrimSpace(stdout.String()), want)
	}
}

func TestRunDBMigrateAndQuery(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "opencode.db")
	ctx := context.Background()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(ctx, []string{"db", "--db", dbPath, "migrate"}, &stdout, &stderr, "test")
	if code != 0 {
		t.Fatalf("migrate exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Migration complete") {
		t.Fatalf("migrate stdout = %q, want migration complete", stdout.String())
	}

	created := runAppJSON[map[string]any](t, ctx, []string{
		"session", "--db", dbPath, "create", "--title", "DB CLI",
	})
	sessionID := created["id"].(string)

	stdout.Reset()
	stderr.Reset()
	code = Run(ctx, []string{"db", "--db", dbPath, "query", "select title, id from session order by title"}, &stdout, &stderr, "test")
	if code != 0 {
		t.Fatalf("query exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 2 || lines[0] != "title\tid" || lines[1] != "DB CLI\t"+sessionID {
		t.Fatalf("query stdout = %q, want TSV header and row in selected column order", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = Run(ctx, []string{"db", "--db", dbPath, "--format", "json", "query", "select title from session"}, &stdout, &stderr, "test")
	if code != 0 {
		t.Fatalf("json query exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	var rows []map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &rows); err != nil {
		t.Fatalf("decode json query: %v\nstdout=%s", err, stdout.String())
	}
	if len(rows) != 1 || rows[0]["title"] != "DB CLI" {
		t.Fatalf("json rows = %#v, want title row", rows)
	}
}

func TestRunDBQueryRejectsWritesInReadOnlyMode(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "opencode.db")
	created := runAppJSON[map[string]any](t, context.Background(), []string{
		"session", "--db", dbPath, "create", "--title", "readonly",
	})
	if created["title"] != "readonly" {
		t.Fatalf("created = %#v, want readonly title", created)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(context.Background(), []string{"db", "--db", dbPath, "query", "delete from session"}, &stdout, &stderr, "test")
	if code != 1 {
		t.Fatalf("delete query exit code = %d, want 1", code)
	}
	if !strings.Contains(strings.ToLower(stderr.String()), "readonly") {
		t.Fatalf("stderr = %q, want readonly error", stderr.String())
	}
}

func TestRunDBRejectsInvalidCommandShape(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(context.Background(), []string{"db", "path", "extra"}, &stdout, &stderr, "test")
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "usage: opencode db") {
		t.Fatalf("stderr = %q, want db usage", stderr.String())
	}
}

func TestRunExportSessionJSON(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "opencode.db")
	ctx := context.Background()
	created := runAppJSON[map[string]any](t, ctx, []string{
		"session", "--db", dbPath, "create", "--title", "Export Me",
	})
	sessionID := created["id"].(string)
	prompt := runAppJSON[map[string]any](t, ctx, []string{
		"session", "--db", dbPath, "prompt", "--no-reply", "--text", "hello export", sessionID,
	})
	promptInfo := prompt["info"].(map[string]any)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(ctx, []string{"export", "--db", dbPath, sessionID}, &stdout, &stderr, "test")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	var exported map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &exported); err != nil {
		t.Fatalf("decode export JSON: %v\nstdout=%s", err, stdout.String())
	}
	info, ok := exported["info"].(map[string]any)
	if !ok || info["id"] != sessionID || info["title"] != "Export Me" {
		t.Fatalf("export info = %#v, want session info", exported["info"])
	}
	messages, ok := exported["messages"].([]any)
	if !ok || len(messages) != 1 {
		t.Fatalf("export messages = %#v, want one message", exported["messages"])
	}
	message := messages[0].(map[string]any)
	messageInfo := message["info"].(map[string]any)
	if messageInfo["id"] != promptInfo["id"] || messageInfo["role"] != "user" {
		t.Fatalf("message info = %#v, want exported prompt", message["info"])
	}
	parts := message["parts"].([]any)
	part := parts[0].(map[string]any)
	if part["type"] != "text" || part["text"] != "hello export" {
		t.Fatalf("exported part = %#v, want text part", part)
	}
}

func TestRunExportLatestAndSanitize(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "opencode.db")
	ctx := context.Background()
	first := runAppJSON[map[string]any](t, ctx, []string{
		"session", "--db", dbPath, "create", "--title", "Older",
	})
	_ = runAppJSON[map[string]any](t, ctx, []string{
		"session", "--db", dbPath, "prompt", "--no-reply", "--text", "older secret", first["id"].(string),
	})
	second := runAppJSON[map[string]any](t, ctx, []string{
		"session", "--db", dbPath, "create", "--title", "Latest",
	})
	secondID := second["id"].(string)
	_ = runAppJSON[map[string]any](t, ctx, []string{
		"session", "--db", dbPath, "prompt", "--no-reply", "--text", "latest secret", secondID,
	})

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(ctx, []string{"export", "--db", dbPath, "--sanitize"}, &stdout, &stderr, "test")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	var exported map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &exported); err != nil {
		t.Fatalf("decode sanitized export JSON: %v\nstdout=%s", err, stdout.String())
	}
	info := exported["info"].(map[string]any)
	if info["id"] != secondID || info["title"] != "[redacted:session-title:"+secondID+"]" {
		t.Fatalf("sanitized info = %#v, want latest redacted session", info)
	}
	messages := exported["messages"].([]any)
	message := messages[0].(map[string]any)
	parts := message["parts"].([]any)
	part := parts[0].(map[string]any)
	partID := part["id"].(string)
	if part["text"] != "[redacted:text:"+partID+"]" {
		t.Fatalf("sanitized part = %#v, want redacted text", part)
	}
}

func TestRunExportMissingSession(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "opencode.db")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(context.Background(), []string{"export", "--db", dbPath, "ses_missing"}, &stdout, &stderr, "test")
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "Session not found: ses_missing") {
		t.Fatalf("stderr = %q, want missing session", stderr.String())
	}
}

func TestRunImportSessionJSON(t *testing.T) {
	ctx := context.Background()
	sourceDB := filepath.Join(t.TempDir(), "source.db")
	created := runAppJSON[map[string]any](t, ctx, []string{
		"session", "--db", sourceDB, "create", "--title", "Import Me",
	})
	sessionID := created["id"].(string)
	prompt := runAppJSON[map[string]any](t, ctx, []string{
		"session", "--db", sourceDB, "prompt", "--no-reply", "--text", "hello import", sessionID,
	})
	promptInfo := prompt["info"].(map[string]any)

	var exportStdout bytes.Buffer
	var exportStderr bytes.Buffer
	code := Run(ctx, []string{"export", "--db", sourceDB, sessionID}, &exportStdout, &exportStderr, "test")
	if code != 0 {
		t.Fatalf("export exit code = %d, want 0; stderr=%q", code, exportStderr.String())
	}
	exportPath := filepath.Join(t.TempDir(), "session.json")
	if err := os.WriteFile(exportPath, exportStdout.Bytes(), 0o644); err != nil {
		t.Fatalf("write export file: %v", err)
	}

	targetDB := filepath.Join(t.TempDir(), "target.db")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code = Run(ctx, []string{"import", "--db", targetDB, exportPath}, &stdout, &stderr, "test")
	if code != 0 {
		t.Fatalf("import exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "Imported session: "+sessionID {
		t.Fatalf("import stdout = %q, want imported session", stdout.String())
	}
	got := runAppJSON[map[string]any](t, ctx, []string{"session", "--db", targetDB, "get", sessionID})
	if got["title"] != "Import Me" {
		t.Fatalf("imported get = %#v, want imported title", got)
	}
	messages := runAppJSON[[]map[string]any](t, ctx, []string{"session", "--db", targetDB, "messages", sessionID})
	if len(messages) != 1 {
		t.Fatalf("imported messages = %#v, want one message", messages)
	}
	info := messages[0]["info"].(map[string]any)
	if info["id"] != promptInfo["id"] {
		t.Fatalf("imported message info = %#v, want original prompt id", info)
	}
	parts := messages[0]["parts"].([]any)
	part := parts[0].(map[string]any)
	if part["text"] != "hello import" {
		t.Fatalf("imported part = %#v, want hello import", part)
	}
}

func TestRunStatsJSONAggregatesSessionsMessagesModelsAndTools(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "opencode.db")
	ctx := context.Background()
	store, err := storage.OpenSQLiteSessionStore(dbPath)
	if err != nil {
		t.Fatalf("OpenSQLiteSessionStore() error = %v", err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	}()
	info, err := store.Create(ctx, session.CreateInput{
		Title:  "Stats",
		Cost:   1.25,
		Tokens: &session.TokenUsage{Input: 10, Output: 20, Reasoning: 3, Cache: session.CacheUsage{Read: 4, Write: 5}},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	prompt, err := store.CreatePrompt(ctx, info.ID, session.PromptInput{
		Parts: []session.Part{{Type: "text", Data: map[string]any{"text": "use read"}}},
	})
	if err != nil {
		t.Fatalf("CreatePrompt() error = %v", err)
	}
	_, err = store.CreateAssistant(ctx, info.ID, session.AssistantInput{
		ParentID: prompt.Info.ID,
		Model:    session.ModelRef{ProviderID: "openai-compatible", ModelID: "mock-model"},
		Cost:     0.75,
		Tokens:   session.TokenUsage{Input: 7, Output: 11, Reasoning: 2, Cache: session.CacheUsage{Read: 3, Write: 5}},
		Tools: []session.ToolExecution{{
			CallID: "call_1",
			Tool:   "read",
			Input:  map[string]any{"filePath": "README.md"},
			Output: "content",
			Title:  "README.md",
		}},
	})
	if err != nil {
		t.Fatalf("CreateAssistant() error = %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(ctx, []string{"stats", "--db", dbPath, "--json"}, &stdout, &stderr, "test")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	var stats map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &stats); err != nil {
		t.Fatalf("decode stats JSON: %v\nstdout=%s", err, stdout.String())
	}
	if stats["totalSessions"] != float64(1) || stats["totalMessages"] != float64(2) || stats["totalCost"] != 1.25 {
		t.Fatalf("stats overview = %#v, want one session two messages cost 1.25", stats)
	}
	totalTokens := stats["totalTokens"].(map[string]any)
	cache := totalTokens["cache"].(map[string]any)
	if totalTokens["input"] != float64(10) || totalTokens["output"] != float64(20) || totalTokens["reasoning"] != float64(3) ||
		cache["read"] != float64(4) || cache["write"] != float64(5) {
		t.Fatalf("totalTokens = %#v, want session token totals", totalTokens)
	}
	toolUsage := stats["toolUsage"].(map[string]any)
	if toolUsage["read"] != float64(1) {
		t.Fatalf("toolUsage = %#v, want read count", toolUsage)
	}
	modelUsage := stats["modelUsage"].(map[string]any)
	mock := modelUsage["openai-compatible/mock-model"].(map[string]any)
	mockTokens := mock["tokens"].(map[string]any)
	mockCache := mockTokens["cache"].(map[string]any)
	if mock["messages"] != float64(1) || mock["cost"] != 0.75 ||
		mockTokens["input"] != float64(7) || mockTokens["output"] != float64(13) ||
		mockCache["read"] != float64(3) || mockCache["write"] != float64(5) {
		t.Fatalf("modelUsage = %#v, want assistant model usage", mock)
	}
}

func TestRunStatsTextSupportsModelAndToolLimits(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "opencode.db")
	ctx := context.Background()
	store, err := storage.OpenSQLiteSessionStore(dbPath)
	if err != nil {
		t.Fatalf("OpenSQLiteSessionStore() error = %v", err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	}()
	info, err := store.Create(ctx, session.CreateInput{Title: "Stats Text", Tokens: &session.TokenUsage{Input: 1000}})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	prompt, err := store.CreatePrompt(ctx, info.ID, session.PromptInput{Parts: []session.Part{{Type: "text", Data: map[string]any{"text": "tools"}}}})
	if err != nil {
		t.Fatalf("CreatePrompt() error = %v", err)
	}
	if _, err := store.CreateAssistant(ctx, info.ID, session.AssistantInput{
		ParentID: prompt.Info.ID,
		Model:    session.ModelRef{ProviderID: "openai-compatible", ModelID: "model-a"},
		Tools: []session.ToolExecution{
			{Tool: "read", Input: map[string]any{}, Output: "ok"},
			{Tool: "write", Input: map[string]any{}, Output: "ok"},
		},
	}); err != nil {
		t.Fatalf("CreateAssistant(model-a) error = %v", err)
	}
	if _, err := store.CreateAssistant(ctx, info.ID, session.AssistantInput{
		ParentID: prompt.Info.ID,
		Model:    session.ModelRef{ProviderID: "openai-compatible", ModelID: "model-a"},
		Tools:    []session.ToolExecution{{Tool: "read", Input: map[string]any{}, Output: "ok"}},
	}); err != nil {
		t.Fatalf("CreateAssistant(model-a second) error = %v", err)
	}
	if _, err := store.CreateAssistant(ctx, info.ID, session.AssistantInput{
		ParentID: prompt.Info.ID,
		Model:    session.ModelRef{ProviderID: "openai-compatible", ModelID: "model-b"},
	}); err != nil {
		t.Fatalf("CreateAssistant(model-b) error = %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(ctx, []string{"stats", "--db", dbPath, "--models", "1", "--tools", "1"}, &stdout, &stderr, "test")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	text := stdout.String()
	for _, want := range []string{"OVERVIEW", "Sessions: 1", "Messages: 4", "MODEL USAGE", "openai-compatible/model-a: 2 messages", "TOOL USAGE", "read: 2 (66.7%)"} {
		if !strings.Contains(text, want) {
			t.Fatalf("stats text = %q, missing %q", text, want)
		}
	}
	if strings.Contains(text, "model-b") || strings.Contains(text, "write:") {
		t.Fatalf("stats text = %q, want model/tool limits applied", text)
	}

	stdout.Reset()
	stderr.Reset()
	code = Run(ctx, []string{"stats", "--db", dbPath, "--models"}, &stdout, &stderr, "test")
	if code != 0 {
		t.Fatalf("bare --models exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "openai-compatible/model-b: 1 messages") {
		t.Fatalf("bare --models stdout = %q, want all models", stdout.String())
	}
}

func TestRunDebugFileCommands(t *testing.T) {
	root := t.TempDir()
	writeAppFile(t, filepath.Join(root, "README.md"), "hello project\nneedle line\n")
	writeAppFile(t, filepath.Join(root, "src", "main.go"), "package main\n\nfunc Run() {}\n")
	writeAppFile(t, filepath.Join(root, "node_modules", "ignored.js"), "needle ignored\n")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(context.Background(), []string{"debug", "file", "read", "--directory", root, "README.md"}, &stdout, &stderr, "test")
	if code != 0 {
		t.Fatalf("read exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	var content map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &content); err != nil {
		t.Fatalf("decode read JSON: %v\nstdout=%s", err, stdout.String())
	}
	if content["type"] != "text" || content["content"] != "hello project\nneedle line" {
		t.Fatalf("read content = %#v, want trimmed text envelope", content)
	}

	stdout.Reset()
	stderr.Reset()
	code = Run(context.Background(), []string{"debug", "file", "list", "--directory", root, "."}, &stdout, &stderr, "test")
	if code != 0 {
		t.Fatalf("list exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	var nodes []map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &nodes); err != nil {
		t.Fatalf("decode list JSON: %v\nstdout=%s", err, stdout.String())
	}
	if len(nodes) < 3 || nodes[0]["type"] != "directory" || nodes[0]["name"] != "node_modules" || nodes[0]["ignored"] != true {
		t.Fatalf("nodes = %#v, want ignored directory sorted first", nodes)
	}

	stdout.Reset()
	stderr.Reset()
	code = Run(context.Background(), []string{"debug", "file", "search", "--directory", root, "--type", "file", "main"}, &stdout, &stderr, "test")
	if code != 0 {
		t.Fatalf("search exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "src/main.go" {
		t.Fatalf("search stdout = %q, want src/main.go", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = Run(context.Background(), []string{"debug", "file", "tree", "--limit", "10", root}, &stdout, &stderr, "test")
	if code != 0 {
		t.Fatalf("tree exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "src" {
		t.Fatalf("tree stdout = %q, want src directory", stdout.String())
	}
}

func TestRunDebugFileStatus(t *testing.T) {
	root := t.TempDir()
	runAppCommand(t, root, "git", "init")
	runAppCommand(t, root, "git", "config", "user.email", "test@example.com")
	runAppCommand(t, root, "git", "config", "user.name", "Test User")
	writeAppFile(t, filepath.Join(root, "tracked.txt"), "one\n")
	runAppCommand(t, root, "git", "add", "tracked.txt")
	runAppCommand(t, root, "git", "-c", "commit.gpgsign=false", "commit", "-m", "initial")
	writeAppFile(t, filepath.Join(root, "tracked.txt"), "one\ntwo\n")
	writeAppFile(t, filepath.Join(root, "new.txt"), "new\n")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(context.Background(), []string{"debug", "file", "status", "--directory", root}, &stdout, &stderr, "test")
	if code != 0 {
		t.Fatalf("status exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	var status []map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &status); err != nil {
		t.Fatalf("decode status JSON: %v\nstdout=%s", err, stdout.String())
	}
	byPath := map[string]map[string]any{}
	for _, item := range status {
		byPath[item["path"].(string)] = item
	}
	if byPath["tracked.txt"]["status"] != "modified" || byPath["new.txt"]["status"] != "added" {
		t.Fatalf("status = %#v, want modified tracked and added new", status)
	}
}

func TestRunSessionPromptCreatesAssistantReply(t *testing.T) {
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("llm path = %s, want /chat/completions", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer local-key" {
			t.Fatalf("authorization = %q, want bearer local-key", r.Header.Get("Authorization"))
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode llm request: %v", err)
		}
		if request["model"] != "local-model" {
			t.Fatalf("model = %#v, want local-model", request["model"])
		}
		messages, ok := request["messages"].([]any)
		if !ok || len(messages) != 1 {
			t.Fatalf("messages = %#v, want one message", request["messages"])
		}
		first, ok := messages[0].(map[string]any)
		if !ok || first["role"] != "user" || first["content"] != "hello llm" {
			t.Fatalf("first message = %#v, want user hello llm", messages[0])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"assistant from go"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
	}))
	defer llmServer.Close()

	t.Setenv("OPENCODE_OPENAI_COMPATIBLE_BASE_URL", llmServer.URL)
	t.Setenv("OPENCODE_OPENAI_COMPATIBLE_API_KEY", "local-key")
	dbPath := filepath.Join(t.TempDir(), "opencode.db")
	ctx := context.Background()

	created := runAppJSON[map[string]any](t, ctx, []string{
		"session", "--db", dbPath, "create", "--title", "Chat",
	})
	sessionID := created["id"].(string)

	assistant := runAppJSON[map[string]any](t, ctx, []string{
		"session", "--db", dbPath, "prompt", "--text", "hello llm", "--model", "local-model", sessionID,
	})
	info, ok := assistant["info"].(map[string]any)
	if !ok || info["role"] != "assistant" || info["finish"] != "stop" {
		t.Fatalf("assistant info = %#v, want assistant stop", assistant["info"])
	}
	if info["providerID"] != "openai-compatible" || info["modelID"] != "local-model" {
		t.Fatalf("assistant model info = %#v, want openai-compatible/local-model", info)
	}
	parts, ok := assistant["parts"].([]any)
	if !ok || len(parts) != 2 {
		t.Fatalf("assistant parts = %#v, want text and step-finish", assistant["parts"])
	}
	firstPart, ok := parts[0].(map[string]any)
	if !ok || firstPart["type"] != "text" || firstPart["text"] != "assistant from go" {
		t.Fatalf("assistant first part = %#v, want text reply", parts[0])
	}

	messages := runAppJSON[[]map[string]any](t, ctx, []string{
		"session", "--db", dbPath, "messages", sessionID,
	})
	if len(messages) != 2 {
		t.Fatalf("messages = %#v, want user and assistant", messages)
	}
	secondInfo, ok := messages[1]["info"].(map[string]any)
	if !ok || secondInfo["id"] != info["id"] {
		t.Fatalf("second message info = %#v, want assistant id %#v", messages[1]["info"], info["id"])
	}
}

func TestRunSessionPromptUsesConfiguredAgentModel(t *testing.T) {
	home := t.TempDir()
	xdg := filepath.Join(home, ".config")
	root := filepath.Join(home, "repo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("OPENCODE_TEST_HOME", home)
	t.Chdir(root)
	writeAppFile(t, filepath.Join(root, "opencode.jsonc"), `{
		"model": "custom-only/model",
		"agent": {
			"review": {
				"model": "openai-compatible/local-model",
				"variant": "high"
			}
		}
	}`)
	dbPath := filepath.Join(t.TempDir(), "opencode.db")
	ctx := context.Background()

	created := runAppJSON[map[string]any](t, ctx, []string{
		"session", "--db", dbPath, "create", "--title", "Config Model",
	})
	sessionID := created["id"].(string)
	prompt := runAppJSON[map[string]any](t, ctx, []string{
		"session", "--db", dbPath, "prompt", "--no-reply", "--agent", "review", "--text", "hello config", sessionID,
	})

	info, ok := prompt["info"].(map[string]any)
	if !ok {
		t.Fatalf("prompt info = %#v, want object", prompt["info"])
	}
	model, ok := info["model"].(map[string]any)
	if !ok ||
		model["providerID"] != "openai-compatible" ||
		model["modelID"] != "local-model" ||
		model["variant"] != "high" {
		t.Fatalf("prompt model = %#v, want configured agent model", info["model"])
	}
}

func TestRunSessionPromptInheritsPreviousUserModel(t *testing.T) {
	home := t.TempDir()
	xdg := filepath.Join(home, ".config")
	root := filepath.Join(home, "repo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("OPENCODE_TEST_HOME", home)
	t.Chdir(root)
	dbPath := filepath.Join(t.TempDir(), "opencode.db")
	ctx := context.Background()

	created := runAppJSON[map[string]any](t, ctx, []string{
		"session", "--db", dbPath, "create", "--title", "Previous Model",
	})
	sessionID := created["id"].(string)
	_ = runAppJSON[map[string]any](t, ctx, []string{
		"session", "--db", dbPath, "prompt", "--no-reply", "--provider", "openrouter", "--model", "openai/gpt-4o-mini", "--text", "first", sessionID,
	})
	prompt := runAppJSON[map[string]any](t, ctx, []string{
		"session", "--db", dbPath, "prompt", "--no-reply", "--text", "second", sessionID,
	})

	info, ok := prompt["info"].(map[string]any)
	if !ok {
		t.Fatalf("prompt info = %#v, want object", prompt["info"])
	}
	model, ok := info["model"].(map[string]any)
	if !ok || model["providerID"] != "openrouter" || model["modelID"] != "openai/gpt-4o-mini" {
		t.Fatalf("prompt model = %#v, want previous user model", info["model"])
	}
}

func TestRunPromptCreatesSessionAndPrintsAssistantText(t *testing.T) {
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode llm request: %v", err)
		}
		messages := request["messages"].([]any)
		first := messages[0].(map[string]any)
		if first["content"] != "hello run" {
			t.Fatalf("first message = %#v, want hello run", first)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"run reply"},"finish_reason":"stop"}]}`))
	}))
	defer llmServer.Close()
	t.Setenv("OPENCODE_OPENAI_COMPATIBLE_BASE_URL", llmServer.URL)
	t.Setenv("OPENCODE_OPENAI_COMPATIBLE_API_KEY", "local-key")
	dbPath := filepath.Join(t.TempDir(), "run.db")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(context.Background(), []string{"run", "--db", dbPath, "--model", "local-model", "hello run"}, &stdout, &stderr, "test")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "run reply" {
		t.Fatalf("stdout = %q, want run reply", stdout.String())
	}

	sessions := runAppJSON[[]map[string]any](t, context.Background(), []string{"session", "--db", dbPath, "list"})
	if len(sessions) != 1 || sessions[0]["title"] != "hello run" {
		t.Fatalf("sessions = %#v, want persisted run session", sessions)
	}
}

func TestRunPromptJSONOutput(t *testing.T) {
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"json reply"},"finish_reason":"stop"}]}`))
	}))
	defer llmServer.Close()
	isolateAppConfig(t, t.TempDir())
	t.Setenv("OPENCODE_OPENAI_COMPATIBLE_BASE_URL", llmServer.URL)
	t.Setenv("OPENCODE_OPENAI_COMPATIBLE_API_KEY", "local-key")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(context.Background(), []string{"run", "--json", "--text", "hello json"}, &stdout, &stderr, "test")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	var assistant map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &assistant); err != nil {
		t.Fatalf("decode stdout JSON: %v\nstdout=%s", err, stdout.String())
	}
	info := assistant["info"].(map[string]any)
	parts := assistant["parts"].([]any)
	first := parts[0].(map[string]any)
	if info["role"] != "assistant" || first["text"] != "json reply" {
		t.Fatalf("assistant = %#v, want JSON assistant reply", assistant)
	}
}

func TestRunCommandsAndSessionCommand(t *testing.T) {
	root := t.TempDir()
	commandPath := filepath.Join(root, ".opencode", "command", "ship.md")
	if err := os.MkdirAll(filepath.Dir(commandPath), 0o755); err != nil {
		t.Fatalf("mkdir command dir: %v", err)
	}
	commandMarkdown := strings.Join([]string{
		"---",
		"description: Ship command",
		"agent: build",
		"model: openai-compatible/local-model",
		"---",
		"Ship $1",
		"Rest $2",
		"Shell !`printf ok`",
	}, "\n")
	if err := os.WriteFile(commandPath, []byte(commandMarkdown), 0o644); err != nil {
		t.Fatalf("write command: %v", err)
	}
	dbPath := filepath.Join(t.TempDir(), "opencode.db")
	ctx := context.Background()

	commands := runAppJSON[map[string]any](t, ctx, []string{"commands", "--directory", root})
	ship, ok := commands["ship"].(map[string]any)
	if !ok || ship["description"] != "Ship command" || ship["model"] != "openai-compatible/local-model" {
		t.Fatalf("commands = %#v, want ship metadata", commands)
	}

	created := runAppJSON[map[string]any](t, ctx, []string{"session", "--db", dbPath, "create", "--title", "Command"})
	sessionID := created["id"].(string)
	message := runAppJSON[map[string]any](t, ctx, []string{
		"session", "--db", dbPath, "command",
		"--directory", root,
		"--argument", `"first arg" second third`,
		"--no-reply",
		sessionID,
		"ship",
	})
	info, ok := message["info"].(map[string]any)
	if !ok || info["role"] != "user" || info["agent"] != "build" {
		t.Fatalf("message info = %#v, want build user message", message["info"])
	}
	model, ok := info["model"].(map[string]any)
	if !ok || model["providerID"] != "openai-compatible" || model["modelID"] != "local-model" {
		t.Fatalf("message model = %#v, want command model", info["model"])
	}
	parts, ok := message["parts"].([]any)
	if !ok || len(parts) != 1 {
		t.Fatalf("message parts = %#v, want one text part", message["parts"])
	}
	part, ok := parts[0].(map[string]any)
	if !ok {
		t.Fatalf("part = %#v, want object", parts[0])
	}
	text, ok := part["text"].(string)
	if !ok {
		t.Fatalf("part text = %#v, want string", part["text"])
	}
	for _, want := range []string{"Ship first arg", "Rest second third", "Shell ok"} {
		if !strings.Contains(text, want) {
			t.Fatalf("rendered command text = %q, missing %q", text, want)
		}
	}
	metadata, ok := part["metadata"].(map[string]any)
	if !ok || metadata["command"] != "ship" || metadata["arguments"] != `"first arg" second third` {
		t.Fatalf("metadata = %#v, want command metadata", part["metadata"])
	}
}

func TestRunSessionCommandUsesConfiguredAgentModelWhenCommandOmitsModel(t *testing.T) {
	home := t.TempDir()
	xdg := filepath.Join(home, ".config")
	root := filepath.Join(home, "repo")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("OPENCODE_TEST_HOME", home)
	writeAppFile(t, filepath.Join(root, "opencode.jsonc"), `{
		"agent": {
			"build": {
				"model": "openai-compatible/command-model",
				"variant": "low"
			}
		}
	}`)
	commandPath := filepath.Join(root, ".opencode", "command", "plain.md")
	writeAppFile(t, commandPath, "Plain command")
	dbPath := filepath.Join(t.TempDir(), "opencode.db")
	ctx := context.Background()

	created := runAppJSON[map[string]any](t, ctx, []string{"session", "--db", dbPath, "create", "--title", "Command Config"})
	sessionID := created["id"].(string)
	message := runAppJSON[map[string]any](t, ctx, []string{
		"session", "--db", dbPath, "command",
		"--directory", root,
		"--no-reply",
		sessionID,
		"plain",
	})

	info, ok := message["info"].(map[string]any)
	if !ok {
		t.Fatalf("message info = %#v, want object", message["info"])
	}
	model, ok := info["model"].(map[string]any)
	if !ok ||
		model["providerID"] != "openai-compatible" ||
		model["modelID"] != "command-model" ||
		model["variant"] != "low" {
		t.Fatalf("message model = %#v, want configured command fallback model", info["model"])
	}
}

func TestRunSessionCommandCreatesAssistantReply(t *testing.T) {
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode llm request: %v", err)
		}
		messages, ok := request["messages"].([]any)
		if !ok || len(messages) != 1 {
			t.Fatalf("messages = %#v, want one command prompt", request["messages"])
		}
		first, ok := messages[0].(map[string]any)
		if !ok || first["content"] != "Run command" {
			t.Fatalf("first message = %#v, want rendered command prompt", messages[0])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"command reply"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer llmServer.Close()
	t.Setenv("OPENCODE_OPENAI_COMPATIBLE_BASE_URL", llmServer.URL)
	t.Setenv("OPENCODE_OPENAI_COMPATIBLE_API_KEY", "local-key")

	root := t.TempDir()
	isolateAppConfig(t, root)
	commandPath := filepath.Join(root, ".opencode", "command", "run.md")
	if err := os.MkdirAll(filepath.Dir(commandPath), 0o755); err != nil {
		t.Fatalf("mkdir command dir: %v", err)
	}
	if err := os.WriteFile(commandPath, []byte("Run command"), 0o644); err != nil {
		t.Fatalf("write command: %v", err)
	}
	dbPath := filepath.Join(t.TempDir(), "opencode.db")
	ctx := context.Background()
	created := runAppJSON[map[string]any](t, ctx, []string{"session", "--db", dbPath, "create", "--title", "Command Reply"})
	sessionID := created["id"].(string)

	assistant := runAppJSON[map[string]any](t, ctx, []string{
		"session", "--db", dbPath, "command",
		"--directory", root,
		sessionID,
		"run",
	})
	info, ok := assistant["info"].(map[string]any)
	if !ok || info["role"] != "assistant" {
		t.Fatalf("assistant info = %#v, want assistant", assistant["info"])
	}
	parts, ok := assistant["parts"].([]any)
	if !ok || len(parts) != 2 {
		t.Fatalf("assistant parts = %#v, want text and finish", assistant["parts"])
	}
	part, ok := parts[0].(map[string]any)
	if !ok || part["text"] != "command reply" {
		t.Fatalf("assistant first part = %#v, want command reply", parts[0])
	}
}

func TestRunConfigLoadsLocalConfig(t *testing.T) {
	home := t.TempDir()
	xdg := filepath.Join(home, ".config")
	root := filepath.Join(home, "repo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("OPENCODE_TEST_HOME", home)
	writeAppFile(t, filepath.Join(xdg, "opencode", "opencode.jsonc"), `{"model":"global/model"}`)
	writeAppFile(t, filepath.Join(root, "opencode.jsonc"), `{"model":"project/model","instructions":["local.md"]}`)

	got := runAppJSON[map[string]any](t, context.Background(), []string{"config", "--directory", root})
	info, ok := got["info"].(map[string]any)
	if !ok {
		t.Fatalf("info = %#v, want object", got["info"])
	}
	if info["model"] != "project/model" {
		t.Fatalf("model = %#v, want project/model", info["model"])
	}
	files, ok := got["files"].([]any)
	if !ok || len(files) != 2 {
		t.Fatalf("files = %#v, want global and project files", got["files"])
	}
}

func TestRunProvidersJSONUsesConfig(t *testing.T) {
	home := t.TempDir()
	xdg := filepath.Join(home, ".config")
	root := filepath.Join(home, "repo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("OPENCODE_TEST_HOME", home)
	writeAppFile(t, filepath.Join(root, "opencode.jsonc"), `{
		"enabled_providers": ["openai-compatible"],
		"provider": {
			"openai-compatible": {
				"models": {
					"local-model": {"name": "Local Model"}
				}
			}
		}
	}`)

	got := runAppJSON[map[string]any](t, context.Background(), []string{"providers", "--json", "--directory", root})
	all, ok := got["all"].([]any)
	if !ok || len(all) != 1 {
		t.Fatalf("all = %#v, want one provider", got["all"])
	}
	provider := all[0].(map[string]any)
	if provider["id"] != "openai-compatible" {
		t.Fatalf("provider = %#v, want openai-compatible", provider)
	}
	models := provider["models"].(map[string]any)
	if _, ok := models["local-model"]; !ok {
		t.Fatalf("models = %#v, want local-model", models)
	}
}

func TestRunModelsCommand(t *testing.T) {
	root := t.TempDir()
	isolateAppConfig(t, root)
	writeAppFile(t, filepath.Join(root, "opencode.jsonc"), `{
		"enabled_providers": ["openai-compatible"]
	}`)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(context.Background(), []string{"models", "--directory", root, "openai-compatible"}, &stdout, &stderr, "test")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "openai-compatible/gpt-4o-mini") {
		t.Fatalf("stdout = %q, want provider/model ids", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = Run(context.Background(), []string{"models", "--directory", root, "--verbose", "openai-compatible"}, &stdout, &stderr, "test")
	if code != 0 {
		t.Fatalf("verbose exit code = %d, want 0; stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "openai-compatible/gpt-4o-mini") || !strings.Contains(stdout.String(), `"providerID":"openai-compatible"`) {
		t.Fatalf("verbose stdout = %q, want model id and metadata", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = Run(context.Background(), []string{"models", "--directory", root, "missing"}, &stdout, &stderr, "test")
	if code != 2 || !strings.Contains(stderr.String(), "provider not found: missing") {
		t.Fatalf("missing provider code = %d stderr = %q, want provider not found", code, stderr.String())
	}
}

func writeAppFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func writeAppMCPFixture(t *testing.T) string {
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
		if !hasID {
			continue
		}
		method, _ := req["method"].(string)
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
	return path
}

func runAppCommand(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v failed: %v\n%s", name, args, err, string(output))
	}
}

func isolateAppConfig(t *testing.T, root string) {
	t.Helper()
	home := t.TempDir()
	xdg := filepath.Join(home, ".config")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("OPENCODE_TEST_HOME", home)
	t.Setenv("OPENCODE_DISABLE_PROJECT_CONFIG", "1")
	t.Chdir(root)
}

func TestRunSessionPromptRejectsTextAndTextFileTogether(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := Run(context.Background(), []string{
		"session", "prompt", "--text", "a", "--text-file", "prompt.txt", "ses_test",
	}, &stdout, &stderr, "test")
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "mutually exclusive") {
		t.Fatalf("stderr = %q, want mutually exclusive error", stderr.String())
	}
}

func runAppJSON[T any](t *testing.T, ctx context.Context, args []string) T {
	t.Helper()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(ctx, args, &stdout, &stderr, "test")
	if code != 0 {
		t.Fatalf("Run(%v) exit code = %d, want 0; stderr=%q stdout=%q", args, code, stderr.String(), stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("Run(%v) stderr = %q, want empty", args, stderr.String())
	}
	var result T
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("Run(%v) decode JSON: %v\nstdout=%s", args, err, stdout.String())
	}
	return result
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (buffer *lockedBuffer) Write(data []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buf.Write(data)
}

func (buffer *lockedBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buf.String()
}

var _ io.Writer = (*lockedBuffer)(nil)

func waitForServeURL(t *testing.T, stdout interface{ String() string }) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		text := strings.TrimSpace(stdout.String())
		if text != "" {
			firstLine := strings.Split(text, "\n")[0]
			parsed, err := url.Parse(firstLine)
			if err == nil && parsed.Scheme == "http" && parsed.Host != "" {
				return firstLine
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("serve URL was not written to stdout; stdout=%q", stdout.String())
	return ""
}

func closeResponseBody(t *testing.T, resp *http.Response) {
	t.Helper()
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("close response body: %v", err)
	}
}
