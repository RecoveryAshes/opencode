package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadResolvesPluginOriginsAndAutodiscovery(t *testing.T) {
	home := t.TempDir()
	xdg := filepath.Join(home, ".config")
	root := filepath.Join(home, "repo")
	nested := filepath.Join(root, "nested")
	for _, dir := range []string{
		filepath.Join(xdg, "opencode"),
		filepath.Join(root, ".opencode", "plugins"),
		filepath.Join(nested),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("OPENCODE_TEST_HOME", home)

	writeConfig(t, filepath.Join(xdg, "opencode", "opencode.jsonc"), `{
		"plugin": ["opencode-wakatime", ["@org/plugin", {"global": true}]]
	}`)
	writeConfig(t, filepath.Join(root, "opencode.jsonc"), `{
		"plugin": ["./local-plugin.js", "opencode-wakatime@2.0.0"]
	}`)
	writeConfig(t, filepath.Join(root, "local-plugin.js"), `export default { id: "local", server() {} }`)
	writeConfig(t, filepath.Join(root, ".opencode", "plugins", "auto.ts"), `export default { id: "auto", server() {} }`)

	loaded, err := Load(LoadOptions{Directory: nested, Worktree: root})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	plugins, ok := loaded.Info["plugin"].([]any)
	if !ok {
		t.Fatalf("plugin = %#v, want array", loaded.Info["plugin"])
	}
	if len(plugins) != 4 {
		t.Fatalf("plugins = %#v, want global package, local file, package override, and autodiscovered", plugins)
	}
	if PluginSpecifier(plugins[0]) != "@org/plugin" {
		t.Fatalf("plugins[0] = %#v, want @org/plugin tuple", plugins[0])
	}
	if !strings.HasPrefix(PluginSpecifier(plugins[1]), "file://") {
		t.Fatalf("plugins[1] = %#v, want local file URL", plugins[1])
	}
	if PluginSpecifier(plugins[2]) != "opencode-wakatime@2.0.0" {
		t.Fatalf("plugins[2] = %#v, want project package override", plugins[2])
	}
	if !strings.HasPrefix(PluginSpecifier(plugins[3]), "file://") {
		t.Fatalf("plugins[3] = %#v, want autodiscovered file URL", plugins[3])
	}
	origins := pluginOriginsFromInfo(loaded.Info)
	if len(origins) != 4 {
		t.Fatalf("origins = %#v, want four", origins)
	}
	if origins[0].Scope != "global" || origins[1].Scope != "local" || origins[3].Source != filepath.Join(root, ".opencode") {
		t.Fatalf("origins = %#v, want global/local/autodiscovered provenance", origins)
	}
}

func TestReadPluginManifestTargets(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, filepath.Join(root, "package.json"), `{
		"name": "fixture-plugin",
		"exports": {
			"./server": {"import": "./server.js", "config": {"api": "x"}},
			"./tui": "./tui.js"
		}
	}`)
	targets, err := ReadPluginManifest("fixture-plugin", root)
	if err != nil {
		t.Fatalf("ReadPluginManifest() error = %v", err)
	}
	if len(targets) != 2 || targets[0].Kind != "server" || targets[1].Kind != "tui" {
		t.Fatalf("targets = %#v, want server and tui", targets)
	}
	if targets[0].Options["api"] != "x" {
		t.Fatalf("server options = %#v, want api option", targets[0].Options)
	}
}

func TestPatchPluginConfigAddNoopAndForceReplace(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	result, err := PatchPluginConfig("opencode-test-plugin", []PluginTarget{{Kind: "server"}}, PluginInstallOptions{
		Directory: root,
		Worktree:  root,
	})
	if err != nil {
		t.Fatalf("PatchPluginConfig(add) error = %v", err)
	}
	if len(result) != 1 || result[0].Mode != "add" || result[0].File != filepath.Join(root, ".opencode", "opencode.json") {
		t.Fatalf("add result = %#v", result)
	}
	again, err := PatchPluginConfig("opencode-test-plugin", []PluginTarget{{Kind: "server"}}, PluginInstallOptions{
		Directory: root,
		Worktree:  root,
	})
	if err != nil {
		t.Fatalf("PatchPluginConfig(noop) error = %v", err)
	}
	if again[0].Mode != "noop" {
		t.Fatalf("noop result = %#v", again)
	}
	replaced, err := PatchPluginConfig("opencode-test-plugin@2.0.0", []PluginTarget{{Kind: "server"}}, PluginInstallOptions{
		Directory: root,
		Worktree:  root,
		Force:     true,
	})
	if err != nil {
		t.Fatalf("PatchPluginConfig(replace) error = %v", err)
	}
	if replaced[0].Mode != "replace" {
		t.Fatalf("replace result = %#v", replaced)
	}
	written, err := readConfigFile(filepath.Join(root, ".opencode", "opencode.json"))
	if err != nil {
		t.Fatalf("read patched config: %v", err)
	}
	list := written["plugin"].([]any)
	if len(list) != 1 || list[0] != "opencode-test-plugin@2.0.0" {
		t.Fatalf("written plugin list = %#v, want replaced version", list)
	}
}
