package integration

import (
	"context"
	"encoding/json"
	"fmt"
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
		Name:      "bash",
		Directory: root,
		Params:    map[string]any{"command": "printf migrated"},
	})
	if err != nil {
		t.Fatalf("bash Execute() error = %v", err)
	}
	if shellResult.Metadata["exit"] != 0 || shellResult.Output != "migrated" {
		t.Fatalf("bash result = %#v", shellResult)
	}

	legacyShellResult, err := Execute(context.Background(), Request{
		Name:      "shell",
		Directory: root,
		Params:    map[string]any{"command": "printf legacy"},
	})
	if err != nil {
		t.Fatalf("legacy shell Execute() error = %v", err)
	}
	if legacyShellResult.Metadata["exit"] != 0 || legacyShellResult.Output != "legacy" {
		t.Fatalf("legacy shell result = %#v", legacyShellResult)
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
	if strings.Contains(skillResult.Output, "---\nname: review") {
		t.Fatalf("skill output includes frontmatter = %q", skillResult.Output)
	}
	if !strings.Contains(skillResult.Output, "Base directory for this skill: file://") {
		t.Fatalf("skill output missing file URL base directory = %q", skillResult.Output)
	}

	todoResult, err := Execute(context.Background(), Request{
		Name: "todowrite",
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

	legacyTodoResult, err := Execute(context.Background(), Request{
		Name: "todo",
		Params: map[string]any{"todos": []map[string]string{{
			"content":  "legacy",
			"status":   "pending",
			"priority": "medium",
		}}},
	})
	if err != nil {
		t.Fatalf("legacy todo Execute() error = %v", err)
	}
	if legacyTodoResult.Title != "1 todos" || !strings.Contains(legacyTodoResult.Output, "legacy") {
		t.Fatalf("legacy todo result = %#v", legacyTodoResult)
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

func TestWebSearchForwardsTypeScriptParametersToBridge(t *testing.T) {
	var captured map[string]string
	bridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		captured = map[string]string{
			"q":                    query.Get("q"),
			"numResults":           query.Get("numResults"),
			"livecrawl":            query.Get("livecrawl"),
			"type":                 query.Get("type"),
			"contextMaxCharacters": query.Get("contextMaxCharacters"),
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []string{"ok"}})
	}))
	defer bridge.Close()
	t.Setenv("OPENCODE_WEBSEARCH_ENDPOINT", bridge.URL+"/search?existing=1")

	result, err := Execute(context.Background(), Request{
		Name: "websearch",
		Params: map[string]any{
			"query":                "go migration",
			"numResults":           float64(5),
			"livecrawl":            "preferred",
			"type":                 "deep",
			"contextMaxCharacters": float64(12000),
		},
	})
	if err != nil {
		t.Fatalf("websearch Execute() error = %v", err)
	}
	want := map[string]string{
		"q":                    "go migration",
		"numResults":           "5",
		"livecrawl":            "preferred",
		"type":                 "deep",
		"contextMaxCharacters": "12000",
	}
	if !mapsEqual(captured, want) {
		t.Fatalf("captured query = %#v, want %#v", captured, want)
	}
	if result.Metadata["provider"] != "endpoint" || result.Metadata["livecrawl"] != "preferred" || result.Metadata["type"] != "deep" {
		t.Fatalf("websearch metadata = %#v", result.Metadata)
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

func TestInvalidAndPlanExitTools(t *testing.T) {
	invalidResult, err := Execute(context.Background(), Request{
		Name:   "invalid",
		Params: map[string]any{"tool": "read", "error": "filePath is required"},
	})
	if err != nil {
		t.Fatalf("invalid Execute() error = %v", err)
	}
	if invalidResult.Title != "Invalid Tool" || !strings.Contains(invalidResult.Output, "filePath is required") {
		t.Fatalf("invalid result = %#v", invalidResult)
	}

	planResult, err := Execute(context.Background(), Request{
		Name:   "plan_exit",
		Params: map[string]any{"plan": ".opencode/plan.md"},
	})
	if err != nil {
		t.Fatalf("plan_exit Execute() error = %v", err)
	}
	if planResult.Title != "Switching to build agent" || !strings.Contains(planResult.Output, ".opencode/plan.md") {
		t.Fatalf("plan_exit result = %#v", planResult)
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

func TestParseRepositoryReferenceMatchesTypeScriptUtility(t *testing.T) {
	t.Setenv("OPENCODE_REPO_CLONE_GITHUB_BASE_URL", "https://mirror.example/repos")

	github, err := parseRepositoryReference("https://github.com/anomalyco/opencode.git#readme")
	if err != nil {
		t.Fatalf("parse github reference: %v", err)
	}
	if github.protocol != "https:" || github.label != "anomalyco/opencode" || github.remote != "https://mirror.example/repos/anomalyco/opencode.git" {
		t.Fatalf("github reference = %#v", github)
	}

	localhost, err := parseRepositoryReference("localhost/team/repo")
	if err != nil {
		t.Fatalf("parse localhost reference: %v", err)
	}
	if localhost.host != "localhost" || localhost.path != "team/repo" || localhost.remote != "https://localhost/team/repo.git" {
		t.Fatalf("localhost reference = %#v", localhost)
	}

	fileRef, err := parseRepositoryReference("file:///tmp/opencode.git")
	if err != nil {
		t.Fatalf("parse file reference: %v", err)
	}
	if fileRef.protocol != "file:" || fileRef.host != "file" || fileRef.label != "/tmp/opencode.git" {
		t.Fatalf("file reference = %#v", fileRef)
	}
}

func TestRepositoryBranchValidationMatchesTypeScriptUtility(t *testing.T) {
	for _, branch := range []string{"main", "feature/go_migration-1", "release.v1"} {
		if !validRepositoryBranch(branch) {
			t.Fatalf("validRepositoryBranch(%q) = false, want true", branch)
		}
	}
	for _, branch := range []string{"-main", "feature..bad", "feature bad"} {
		if validRepositoryBranch(branch) {
			t.Fatalf("validRepositoryBranch(%q) = true, want false", branch)
		}
	}
}

func TestAgentFromMarkdownUsesPathDerivedName(t *testing.T) {
	root := t.TempDir()
	agentPath := filepath.Join(root, ".opencode", "agents", "nested", "review.md")
	if err := os.MkdirAll(filepath.Dir(agentPath), 0o755); err != nil {
		t.Fatalf("mkdir agent dir: %v", err)
	}
	if err := os.WriteFile(agentPath, []byte(strings.Join([]string{
		"---",
		"name: frontmatter-name",
		"description: Nested reviewer",
		"mode: subagent",
		"---",
		"Review deeply.",
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write agent: %v", err)
	}

	agent, err := agentFromMarkdown(agentPath)
	if err != nil {
		t.Fatalf("agentFromMarkdown() error = %v", err)
	}
	if agent.Name != "nested/review" || agent.Description != "Nested reviewer" || agent.Prompt != "Review deeply." {
		t.Fatalf("agent = %#v, want path-derived nested name and metadata", agent)
	}
}

func TestListSkillsIncludesConfiguredLocalPaths(t *testing.T) {
	root := t.TempDir()
	writeIntegrationFile(t, filepath.Join(root, "opencode.jsonc"), `{
		"skills": {
			"paths": ["extra-skills"]
		}
	}`)
	writeIntegrationFile(t, filepath.Join(root, "extra-skills", "audit", "SKILL.md"), strings.Join([]string{
		"---",
		"name: audit",
		"description: Audit from configured path",
		"---",
		"Audit configured path.",
	}, "\n"))

	skills, err := ListSkills(root)
	if err != nil {
		t.Fatalf("ListSkills() error = %v", err)
	}
	if len(skills) != 1 || skills[0].Name != "audit" || skills[0].Description != "Audit from configured path" {
		t.Fatalf("skills = %#v, want configured audit skill", skills)
	}

	result, err := Execute(context.Background(), Request{
		Name:      "skill",
		Directory: root,
		Params:    map[string]any{"name": "audit"},
	})
	if err != nil {
		t.Fatalf("configured skill Execute() error = %v", err)
	}
	if !strings.Contains(result.Output, `<skill_content name="audit">`) || !strings.Contains(result.Output, "Audit configured path.") {
		t.Fatalf("configured skill output = %q", result.Output)
	}
}

func TestListSkillsSkipsMissingFrontmatterName(t *testing.T) {
	root := t.TempDir()
	writeIntegrationFile(t, filepath.Join(root, ".opencode", "skills", "missing", "SKILL.md"), "# Missing\n\nNo frontmatter.")

	skills, err := ListSkills(root)
	if err != nil {
		t.Fatalf("ListSkills() error = %v", err)
	}
	if len(skills) != 0 {
		t.Fatalf("skills = %#v, want no skills without frontmatter name", skills)
	}
}

func TestListSkillsIncludesConfiguredRemoteURLs(t *testing.T) {
	root := t.TempDir()
	cacheRoot := filepath.Join(root, "cache")
	t.Setenv("XDG_CACHE_HOME", cacheRoot)
	downloads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/skills/index.json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"skills": [
					{"name":"remote-audit","files":["SKILL.md","references/guide.md"]},
					{"name":"missing-main","files":["README.md"]}
				]
			}`))
		case "/.well-known/skills/remote-audit/SKILL.md":
			downloads++
			_, _ = w.Write([]byte(strings.Join([]string{
				"---",
				"name: remote-audit",
				"description: Audit from remote URL",
				"---",
				"Audit remote path.",
			}, "\n")))
		case "/.well-known/skills/remote-audit/references/guide.md":
			downloads++
			_, _ = w.Write([]byte("# Guide"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	writeIntegrationFile(t, filepath.Join(root, "opencode.jsonc"), fmt.Sprintf(`{
		"skills": {
			"urls": [%q]
		}
	}`, server.URL+"/.well-known/skills"))

	first, err := ListSkills(root)
	if err != nil {
		t.Fatalf("ListSkills() error = %v", err)
	}
	if len(first) != 1 || first[0].Name != "remote-audit" || first[0].Description != "Audit from remote URL" {
		t.Fatalf("first skills = %#v, want remote-audit only", first)
	}
	if !strings.HasPrefix(first[0].Location, filepath.Join(cacheRoot, "opencode", "skills", "remote-audit")) {
		t.Fatalf("remote skill location = %q, want cached under XDG cache", first[0].Location)
	}
	if downloads != 2 {
		t.Fatalf("downloads after first pull = %d, want 2", downloads)
	}

	second, err := ListSkills(root)
	if err != nil {
		t.Fatalf("second ListSkills() error = %v", err)
	}
	if len(second) != len(first) || second[0].Name != first[0].Name {
		t.Fatalf("second skills = %#v, want cached first result %#v", second, first)
	}
	if downloads != 2 {
		t.Fatalf("downloads after cached pull = %d, want unchanged", downloads)
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

func mapsEqual(left map[string]string, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range right {
		if left[key] != value {
			return false
		}
	}
	return true
}

func writeIntegrationFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
