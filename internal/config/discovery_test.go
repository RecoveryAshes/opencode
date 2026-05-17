package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverLocalOpenCodeFiles(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	t.Setenv("OPENCODE_TEST_HOME", home)
	for _, dir := range []string{"agent", "agents/nested", "command", "plugin", "plugins/nested", "skill", "skills/review", "theme", "themes"} {
		if err := os.MkdirAll(filepath.Join(root, ".opencode", dir), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, ".opencode", "commands", "nested"), 0o755); err != nil {
		t.Fatalf("mkdir nested commands: %v", err)
	}
	writeFile(t, filepath.Join(root, ".opencode", "agent", "build.md"))
	writeFile(t, filepath.Join(root, ".opencode", "agents", "nested", "explore.md"))
	writeFile(t, filepath.Join(root, ".opencode", "command", "commit.markdown"))
	writeFile(t, filepath.Join(root, ".opencode", "commands", "nested", "review.md"))
	writeFile(t, filepath.Join(root, ".opencode", "plugin", "audit.ts"))
	writeFile(t, filepath.Join(root, ".opencode", "plugins", "nested", "notify.js"))
	writeFile(t, filepath.Join(root, ".opencode", "plugins", "ignored.md"))
	writeFile(t, filepath.Join(root, ".opencode", "skill", "review.md"))
	writeFile(t, filepath.Join(root, ".opencode", "skills", "review", "SKILL.md"))
	writeFile(t, filepath.Join(root, ".agents", "skills", "project", "SKILL.md"))
	writeFile(t, filepath.Join(home, ".claude", "skills", "global-claude", "SKILL.md"))
	writeFile(t, filepath.Join(home, ".agents", "skills", "global-agents", "SKILL.md"))
	writeFile(t, filepath.Join(home, ".agents", "skills", "ignored.md"))
	writeFile(t, filepath.Join(root, ".opencode", "theme", "dark.jsonc"))
	writeFile(t, filepath.Join(root, ".opencode", "themes", "light.json"))
	writeFile(t, filepath.Join(root, ".opencode", "theme", "ignored.txt"))

	got, err := Discover(root)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(got.Agents) != 2 || filepath.Base(got.Agents[0]) != "build.md" || filepath.Base(got.Agents[1]) != "explore.md" {
		t.Fatalf("agents = %#v, want build.md and nested explore.md", got.Agents)
	}
	if len(got.Commands) != 2 || filepath.Base(got.Commands[0]) != "commit.markdown" || filepath.Base(got.Commands[1]) != "review.md" {
		t.Fatalf("commands = %#v, want commit.markdown and nested review.md", got.Commands)
	}
	if len(got.Plugins) != 2 || filepath.Base(got.Plugins[0]) != "audit.ts" || filepath.Base(got.Plugins[1]) != "notify.js" {
		t.Fatalf("plugins = %#v, want audit.ts and nested notify.js", got.Plugins)
	}
	if len(got.Skills) != 5 || !containsBase(got.Skills, "review.md") || countBase(got.Skills, "SKILL.md") != 4 {
		t.Fatalf("skills = %#v, want opencode and external SKILL.md files", got.Skills)
	}
	if len(got.Themes) != 2 || filepath.Base(got.Themes[0]) != "dark.jsonc" || filepath.Base(got.Themes[1]) != "light.json" {
		t.Fatalf("themes = %#v, want dark.jsonc and light.json", got.Themes)
	}
}

func containsBase(paths []string, base string) bool {
	for _, path := range paths {
		if filepath.Base(path) == base {
			return true
		}
	}
	return false
}

func countBase(paths []string, base string) int {
	count := 0
	for _, path := range paths {
		if filepath.Base(path) == base {
			count++
		}
	}
	return count
}

func TestDiscoverMissingDirectories(t *testing.T) {
	got, err := Discover(t.TempDir())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(got.Agents) != 0 || len(got.Commands) != 0 || len(got.Plugins) != 0 || len(got.Skills) != 0 || len(got.Themes) != 0 {
		t.Fatalf("Discover() = %#v, want empty lists", got)
	}
}

func writeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte("test"), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
