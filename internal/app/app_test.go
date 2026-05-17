package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RecoveryAshes/opencode/internal/integration"
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
