package config

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestParseJSONCAllowsCommentsTrailingCommasAndDropsLegacyTUIKeys(t *testing.T) {
	got, err := ParseJSONC([]byte(`{
		// comment
		"model": "anthropic/claude",
		"instructions": ["AGENTS.md",],
		"theme": "legacy",
		"keybinds": {},
		"tui": {"old": true},
	}`), "test.jsonc")
	if err != nil {
		t.Fatalf("ParseJSONC() error = %v", err)
	}
	if got["model"] != "anthropic/claude" {
		t.Fatalf("model = %#v, want anthropic/claude", got["model"])
	}
	if _, ok := got["theme"]; ok {
		t.Fatalf("theme legacy key was not dropped: %#v", got)
	}
	if _, ok := got["keybinds"]; ok {
		t.Fatalf("keybinds legacy key was not dropped: %#v", got)
	}
	if _, ok := got["tui"]; ok {
		t.Fatalf("tui legacy key was not dropped: %#v", got)
	}
}

func TestLoadMergesGlobalProjectOpenCodeDirsAndEnvContent(t *testing.T) {
	home := t.TempDir()
	xdg := filepath.Join(home, ".config")
	project := filepath.Join(home, "repo")
	nested := filepath.Join(project, "packages", "app")
	for _, dir := range []string{
		filepath.Join(xdg, "opencode"),
		filepath.Join(project, ".opencode"),
		filepath.Join(nested, ".opencode"),
		filepath.Join(home, ".opencode"),
		nested,
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("OPENCODE_TEST_HOME", home)
	t.Setenv("XDG_CONFIG_HOME", xdg)
	explicitDir := filepath.Join(home, "explicit-config-dir")
	t.Setenv("OPENCODE_CONFIG_DIR", explicitDir)
	t.Setenv("OPENCODE_CONFIG_CONTENT", `{"instructions":["env.md"],"provider":{"env":{"models":{"z":{}}}}}`)

	writeConfig(t, filepath.Join(xdg, "opencode", "opencode.jsonc"), `{
		"instructions": ["global.md"],
		"provider": {"anthropic": {"models": {"claude": {}}}},
		"model": "anthropic/claude"
	}`)
	writeConfig(t, filepath.Join(project, "opencode.json"), `{
		"instructions": ["project.md"],
		"provider": {"anthropic": {"apiKey": "project-key"}}
	}`)
	writeConfig(t, filepath.Join(nested, "opencode.jsonc"), `{
		"instructions": ["nested.md"],
		"model": "openai-compatible/local"
	}`)
	writeConfig(t, filepath.Join(project, ".opencode", "opencode.json"), `{
		"agent": {"build": {"model": "anthropic/claude"}}
	}`)
	writeConfig(t, filepath.Join(nested, ".opencode", "opencode.jsonc"), `{
		"agent": {"reviewer": {"model": "openai-compatible/local"}}
	}`)
	writeConfig(t, filepath.Join(home, ".opencode", "opencode.json"), `{
		"username": "home-user"
	}`)
	writeConfig(t, filepath.Join(explicitDir, "opencode.jsonc"), `{
		"small_model": "explicit/small"
	}`)

	got, err := Load(LoadOptions{Directory: nested, Worktree: project})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if got.Info["model"] != "openai-compatible/local" {
		t.Fatalf("model = %#v, want nested override", got.Info["model"])
	}
	if got.Info["username"] != "home-user" {
		t.Fatalf("username = %#v, want home .opencode config", got.Info["username"])
	}
	if got.Info["small_model"] != "explicit/small" {
		t.Fatalf("small_model = %#v, want OPENCODE_CONFIG_DIR config", got.Info["small_model"])
	}
	instructions, ok := got.Info["instructions"].([]any)
	if !ok {
		t.Fatalf("instructions = %#v, want array", got.Info["instructions"])
	}
	if !sameStrings(instructions, []string{"global.md", "project.md", "nested.md", "env.md"}) {
		t.Fatalf("instructions = %#v, want concatenated unique order", instructions)
	}

	provider := got.Info["provider"].(map[string]any)
	anthropic := provider["anthropic"].(map[string]any)
	if anthropic["apiKey"] != "project-key" {
		t.Fatalf("anthropic provider = %#v, want merged apiKey", anthropic)
	}
	if _, ok := anthropic["models"].(map[string]any)["claude"]; !ok {
		t.Fatalf("anthropic models = %#v, want global model preserved", anthropic["models"])
	}
	agent := got.Info["agent"].(map[string]any)
	if _, ok := agent["build"]; !ok {
		t.Fatalf("agent = %#v, want build from project .opencode", agent)
	}
	if _, ok := agent["reviewer"]; !ok {
		t.Fatalf("agent = %#v, want reviewer from nested .opencode", agent)
	}
	if !slices.Contains(got.Files, "OPENCODE_CONFIG_CONTENT") {
		t.Fatalf("files = %#v, want env content marker", got.Files)
	}
	if !slices.Contains(got.Dirs, filepath.Join(nested, ".opencode")) {
		t.Fatalf("dirs = %#v, want nested .opencode", got.Dirs)
	}
}

func TestProjectFilesRootToLeafAndDisableProjectConfig(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	rootConfig := filepath.Join(root, "opencode.json")
	nestedConfig := filepath.Join(nested, "opencode.jsonc")
	writeConfig(t, rootConfig, `{}`)
	writeConfig(t, nestedConfig, `{}`)

	got, err := ProjectFiles("opencode", nested, root)
	if err != nil {
		t.Fatalf("ProjectFiles() error = %v", err)
	}
	if len(got) != 2 || got[0] != rootConfig || got[1] != nestedConfig {
		t.Fatalf("ProjectFiles() = %#v, want root then nested", got)
	}

	t.Setenv("OPENCODE_DISABLE_PROJECT_CONFIG", "1")
	loaded, err := Load(LoadOptions{Directory: nested, Worktree: root})
	if err != nil {
		t.Fatalf("Load() with disabled project config error = %v", err)
	}
	if slices.Contains(loaded.Files, rootConfig) || slices.Contains(loaded.Files, nestedConfig) {
		t.Fatalf("files = %#v, want project config skipped", loaded.Files)
	}
}

func writeConfig(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func sameStrings(got []any, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i, item := range got {
		if item != want[i] {
			return false
		}
	}
	return true
}
