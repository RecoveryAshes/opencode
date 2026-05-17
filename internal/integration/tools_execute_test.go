package integration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadWriteGlobGrepShellTools(t *testing.T) {
	root := t.TempDir()

	writeResult, err := Execute(context.Background(), Request{
		Name:      "write",
		Directory: root,
		Params: map[string]any{
			"filePath": filepath.Join("src", "main.go"),
			"content":  "package main\n\nfunc main() {\n\tprintln(\"needle\")\n}\n",
		},
	})
	if err != nil {
		t.Fatalf("write Execute() error = %v", err)
	}
	if writeResult.Output != "Wrote file successfully." {
		t.Fatalf("write output = %q", writeResult.Output)
	}

	readResult, err := Execute(context.Background(), Request{
		Name:      "read",
		Directory: root,
		Params:    map[string]any{"filePath": filepath.Join("src", "main.go")},
	})
	if err != nil {
		t.Fatalf("read Execute() error = %v", err)
	}
	if !strings.Contains(readResult.Output, "1: package main") || !strings.Contains(readResult.Output, "<type>file</type>") {
		t.Fatalf("read output = %q", readResult.Output)
	}

	globResult, err := Execute(context.Background(), Request{
		Name:      "glob",
		Directory: root,
		Params:    map[string]any{"pattern": "*.go", "path": "src"},
	})
	if err != nil {
		t.Fatalf("glob Execute() error = %v", err)
	}
	if !strings.Contains(globResult.Output, filepath.Join(root, "src", "main.go")) {
		t.Fatalf("glob output = %q", globResult.Output)
	}

	grepResult, err := Execute(context.Background(), Request{
		Name:      "grep",
		Directory: root,
		Params:    map[string]any{"pattern": "needle", "path": "src", "include": "*.go"},
	})
	if err != nil {
		t.Fatalf("grep Execute() error = %v", err)
	}
	if grepResult.Metadata["matches"] != 1 || !strings.Contains(grepResult.Output, "Line 4") {
		t.Fatalf("grep result = %#v", grepResult)
	}

	shellResult, err := Execute(context.Background(), Request{
		Name:      "shell",
		Directory: root,
		Params:    map[string]any{"command": "printf migrated"},
	})
	if err != nil {
		t.Fatalf("shell Execute() error = %v", err)
	}
	if shellResult.Metadata["exit"] != 0 || shellResult.Output != "migrated" {
		t.Fatalf("shell result = %#v", shellResult)
	}
}

func TestEditApplyPatchWebFetchSkillTodoAndRepoOverviewTools(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	editResult, err := Execute(context.Background(), Request{
		Name:      "edit",
		Directory: root,
		Params: map[string]any{
			"filePath":  "hello.txt",
			"oldString": "hello",
			"newString": "hello go",
		},
	})
	if err != nil {
		t.Fatalf("edit Execute() error = %v", err)
	}
	if editResult.Output != "Edit applied successfully." {
		t.Fatalf("edit output = %q", editResult.Output)
	}

	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Add File: added.txt",
		"+added",
		"*** Update File: hello.txt",
		"@@",
		"-hello go",
		"+hello migrated",
		"*** End Patch",
	}, "\n")
	patchResult, err := Execute(context.Background(), Request{
		Name:      "apply_patch",
		Directory: root,
		Params:    map[string]any{"patchText": patch},
	})
	if err != nil {
		t.Fatalf("apply_patch Execute() error = %v", err)
	}
	if !strings.Contains(patchResult.Output, "A added.txt") || !strings.Contains(patchResult.Output, "M hello.txt") {
		t.Fatalf("patch output = %q", patchResult.Output)
	}

	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body><h1>Title</h1><p>Hello &amp; Go</p></body></html>"))
	}))
	defer web.Close()
	webResult, err := Execute(context.Background(), Request{
		Name:   "webfetch",
		Params: map[string]any{"url": web.URL, "format": "text"},
	})
	if err != nil {
		t.Fatalf("webfetch Execute() error = %v", err)
	}
	if !strings.Contains(webResult.Output, "Title") || !strings.Contains(webResult.Output, "Hello & Go") {
		t.Fatalf("webfetch output = %q", webResult.Output)
	}

	skillDir := filepath.Join(root, ".opencode", "skills", "review")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: review\n---\nUse for review."), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	skillResult, err := Execute(context.Background(), Request{
		Name:      "skill",
		Directory: root,
		Params:    map[string]any{"name": "review"},
	})
	if err != nil {
		t.Fatalf("skill Execute() error = %v", err)
	}
	if !strings.Contains(skillResult.Output, `<skill_content name="review">`) {
		t.Fatalf("skill output = %q", skillResult.Output)
	}

	todoResult, err := Execute(context.Background(), Request{
		Name: "todo",
		Params: map[string]any{"todos": []map[string]string{{
			"content":  "migrate",
			"status":   "pending",
			"priority": "high",
		}}},
	})
	if err != nil {
		t.Fatalf("todo Execute() error = %v", err)
	}
	if todoResult.Title != "1 todos" || !strings.Contains(todoResult.Output, "migrate") {
		t.Fatalf("todo result = %#v", todoResult)
	}

	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/test\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	overview, err := Execute(context.Background(), Request{
		Name:      "repo_overview",
		Directory: root,
		Params:    map[string]any{"path": ".", "depth": 2},
	})
	if err != nil {
		t.Fatalf("repo_overview Execute() error = %v", err)
	}
	if !strings.Contains(overview.Output, "Ecosystems: Go") || !strings.Contains(overview.Output, "go.mod") {
		t.Fatalf("repo_overview output = %q", overview.Output)
	}
}

func TestQuestionTaskAndTaskStatusTools(t *testing.T) {
	questionResult, err := Execute(context.Background(), Request{
		Name: "question",
		Params: map[string]any{
			"questions": []map[string]any{{
				"question": "Which branch?",
				"options": []map[string]string{{
					"label":       "go",
					"description": "Use the Go branch",
				}},
			}},
			"answers": []any{[]any{"go"}},
		},
	})
	if err != nil {
		t.Fatalf("question Execute() error = %v", err)
	}
	if questionResult.Title != "Asked 1 question" || !strings.Contains(questionResult.Output, `"Which branch?"="go"`) {
		t.Fatalf("question result = %#v", questionResult)
	}

	taskResult, err := Execute(context.Background(), Request{
		Name: "task",
		Params: map[string]any{
			"description":   "Inspect providers",
			"prompt":        "Find remaining provider gaps",
			"subagent_type": "explorer",
			"task_id":       "task_manual",
			"result":        "Provider gaps summarized.",
		},
	})
	if err != nil {
		t.Fatalf("task Execute() error = %v", err)
	}
	if taskResult.Metadata["task_id"] != "task_manual" || !strings.Contains(taskResult.Output, "<task_result>\nProvider gaps summarized.\n</task_result>") {
		t.Fatalf("task result = %#v", taskResult)
	}

	backgroundResult, err := Execute(context.Background(), Request{
		Name: "task",
		Params: map[string]any{
			"description":   "Background scan",
			"prompt":        "Scan while I work",
			"subagent_type": "explorer",
			"background":    true,
		},
	})
	if err != nil {
		t.Fatalf("background task Execute() error = %v", err)
	}
	if backgroundResult.Metadata["state"] != "running" || !strings.Contains(backgroundResult.Output, "state: running") {
		t.Fatalf("background task result = %#v", backgroundResult)
	}

	statusResult, err := Execute(context.Background(), Request{
		Name: "task_status",
		Params: map[string]any{
			"task_id": "task_manual",
			"state":   "completed",
			"result":  "Provider gaps summarized.",
		},
	})
	if err != nil {
		t.Fatalf("task_status Execute() error = %v", err)
	}
	if statusResult.Metadata["state"] != "completed" || !strings.Contains(statusResult.Output, "task_id: task_manual") {
		t.Fatalf("task_status result = %#v", statusResult)
	}
}

func TestParseRepositoryReferenceSupportsGitSSHShorthand(t *testing.T) {
	got, err := parseRepositoryReference("git@github.com:RecoveryAshes/opencode.git")
	if err != nil {
		t.Fatalf("parseRepositoryReference() error = %v", err)
	}
	if got.host != "github.com" || got.path != "RecoveryAshes/opencode" || got.remote != "git@github.com:RecoveryAshes/opencode.git" {
		t.Fatalf("reference = %#v", got)
	}
}

func TestReadDirectoryAndBinaryRejection(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatalf("write text fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "data.bin"), []byte{0, 1, 2, 3}, 0o644); err != nil {
		t.Fatalf("write binary fixture: %v", err)
	}

	result, err := Execute(context.Background(), Request{
		Name:      "read",
		Directory: root,
		Params:    map[string]any{"filePath": "."},
	})
	if err != nil {
		t.Fatalf("read directory Execute() error = %v", err)
	}
	if !strings.Contains(result.Output, "<type>directory</type>") || !strings.Contains(result.Output, "a.txt") {
		t.Fatalf("directory output = %q", result.Output)
	}

	_, err = Execute(context.Background(), Request{
		Name:      "read",
		Directory: root,
		Params:    map[string]any{"filePath": "data.bin"},
	})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "binary") {
		t.Fatalf("binary read error = %v, want binary rejection", err)
	}
}
