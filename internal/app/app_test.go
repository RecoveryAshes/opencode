package app

import (
	"bytes"
	"context"
	"encoding/json"
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
