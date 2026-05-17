package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverLocalOpenCodeFiles(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"agent", "agents/nested", "command", "skill", "skills/review", "theme", "themes"} {
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
	writeFile(t, filepath.Join(root, ".opencode", "skill", "review.md"))
	writeFile(t, filepath.Join(root, ".opencode", "skills", "review", "SKILL.md"))
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
	if len(got.Skills) != 2 || filepath.Base(got.Skills[0]) != "review.md" || filepath.Base(got.Skills[1]) != "SKILL.md" {
		t.Fatalf("skills = %#v, want review.md and nested SKILL.md", got.Skills)
	}
	if len(got.Themes) != 2 || filepath.Base(got.Themes[0]) != "dark.jsonc" || filepath.Base(got.Themes[1]) != "light.json" {
		t.Fatalf("themes = %#v, want dark.jsonc and light.json", got.Themes)
	}
}

func TestDiscoverMissingDirectories(t *testing.T) {
	got, err := Discover(t.TempDir())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(got.Agents) != 0 || len(got.Commands) != 0 || len(got.Skills) != 0 || len(got.Themes) != 0 {
		t.Fatalf("Discover() = %#v, want empty lists", got)
	}
}

func writeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("test"), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
