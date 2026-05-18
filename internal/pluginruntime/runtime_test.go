package pluginruntime

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RecoveryAshes/opencode/internal/config"
	"github.com/RecoveryAshes/opencode/internal/integration"
	"github.com/RecoveryAshes/opencode/internal/llm"
)

func TestRuntimeRunsToolHooks(t *testing.T) {
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("bun is required for JS plugin hook execution")
	}
	root := t.TempDir()
	plugin := filepath.Join(root, "plugin.ts")
	if err := os.WriteFile(plugin, []byte(strings.Join([]string{
		"export default async () => ({",
		"  'tool.definition': (input, output) => {",
		"    if (input.toolID === 'read') output.description = 'plugin read description'",
		"  },",
		"  'tool.execute.before': (input, output) => {",
		"    if (input.tool === 'read') output.args.filePath = 'patched.txt'",
		"  },",
		"  'tool.execute.after': (input, output) => {",
		"    if (input.tool === 'read') { output.output += '\\nplugin after'; output.metadata.plugin = true }",
		"  },",
		"  'shell.env': (_input, output) => { output.env.PLUGIN_ENV = 'from-plugin' },",
		"})",
		"",
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write plugin: %v", err)
	}
	loaded, err := config.Load(config.LoadOptions{
		Directory: root,
		Worktree:  root,
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	loaded.Info["plugin_origins"] = []any{map[string]any{
		"spec":   fileURL(plugin),
		"source": filepath.Join(root, "opencode.json"),
		"scope":  "local",
	}}
	runtime := New(loaded.Info, root, root)
	ctx := context.Background()

	defs, err := runtime.ApplyToolDefinitions(ctx, []llm.ToolDefinition{{
		Name:        "read",
		Description: "Read a file",
		Parameters:  map[string]any{"type": "object"},
	}})
	if err != nil {
		t.Fatalf("ApplyToolDefinitions() error = %v", err)
	}
	if len(defs) != 1 || defs[0].Description != "plugin read description" {
		t.Fatalf("definitions = %#v, want plugin mutation", defs)
	}

	args, err := runtime.BeforeToolExecute(ctx, "read", "session", "call", map[string]any{"filePath": "input.txt"})
	if err != nil {
		t.Fatalf("BeforeToolExecute() error = %v", err)
	}
	if args["filePath"] != "patched.txt" {
		t.Fatalf("args = %#v, want patched filePath", args)
	}

	result, err := runtime.AfterToolExecute(ctx, "read", "session", "call", args, integration.Result{
		Title:    "patched.txt",
		Output:   "read output",
		Metadata: map[string]any{},
	})
	if err != nil {
		t.Fatalf("AfterToolExecute() error = %v", err)
	}
	if !strings.Contains(result.Output, "plugin after") || result.Metadata["plugin"] != true {
		t.Fatalf("result = %#v, want after hook mutation", result)
	}

	env, err := runtime.ShellEnv(ctx, root, "session", "call")
	if err != nil {
		t.Fatalf("ShellEnv() error = %v", err)
	}
	if env["PLUGIN_ENV"] != "from-plugin" {
		t.Fatalf("env = %#v, want plugin env", env)
	}
}

func fileURL(path string) string {
	return "file://" + filepath.ToSlash(path)
}
