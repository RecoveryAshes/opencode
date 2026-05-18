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

func TestRuntimeRunsProviderAuthHooks(t *testing.T) {
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("bun is required for JS plugin hook execution")
	}
	root := t.TempDir()
	plugin := filepath.Join(root, "auth-plugin.ts")
	if err := os.WriteFile(plugin, []byte(strings.Join([]string{
		"export default async () => ({",
		"  auth: {",
		"    provider: 'local-ai',",
		"    methods: [{",
		"      type: 'api',",
		"      label: 'Local API key',",
		"      prompts: [{ type: 'text', key: 'apiKey', message: 'API key', placeholder: 'sk-local' }],",
		"      authorize: async (inputs) => ({ type: 'success', key: inputs.apiKey, metadata: { source: 'plugin' } })",
		"    }, {",
		"      type: 'oauth',",
		"      label: 'Browser OAuth',",
		"      authorize: async () => ({ url: 'https://auth.example/start', method: 'code', instructions: 'Paste code', callback: async () => ({ type: 'failed' }) })",
		"    }]",
		"  }",
		"})",
		"",
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write plugin: %v", err)
	}
	info := config.Info{"plugin_origins": []any{map[string]any{
		"spec":   fileURL(plugin),
		"source": filepath.Join(root, "opencode.json"),
		"scope":  "local",
	}}}
	runtime := New(info, root, root)
	ctx := context.Background()

	methods, err := runtime.AuthMethods(ctx)
	if err != nil {
		t.Fatalf("AuthMethods() error = %v", err)
	}
	if len(methods["local-ai"]) != 2 || methods["local-ai"][0].Label != "Local API key" || methods["local-ai"][0].Prompts[0].Placeholder != "sk-local" {
		t.Fatalf("methods = %#v, want plugin auth methods", methods)
	}

	oauth, api, handled, err := runtime.AuthorizeProvider(ctx, "local-ai", 0, map[string]string{"apiKey": "secret-key"})
	if err != nil {
		t.Fatalf("AuthorizeProvider(api) error = %v", err)
	}
	if !handled || oauth != nil || api == nil || api.Type != "success" || api.Key != "secret-key" || api.Metadata["source"] != "plugin" {
		t.Fatalf("api auth = oauth:%#v api:%#v handled:%v, want api success", oauth, api, handled)
	}

	oauth, api, handled, err = runtime.AuthorizeProvider(ctx, "local-ai", 1, nil)
	if err != nil {
		t.Fatalf("AuthorizeProvider(oauth) error = %v", err)
	}
	if !handled || api != nil || oauth == nil || oauth.URL != "https://auth.example/start" || oauth.Method != "code" {
		t.Fatalf("oauth auth = oauth:%#v api:%#v handled:%v, want oauth authorization", oauth, api, handled)
	}
}

func TestRuntimeRunsChatHooks(t *testing.T) {
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("bun is required for JS plugin hook execution")
	}
	root := t.TempDir()
	plugin := filepath.Join(root, "chat-plugin.ts")
	if err := os.WriteFile(plugin, []byte(strings.Join([]string{
		"export default async () => ({",
		"  'chat.params': (input, output) => {",
		"    output.temperature = 0.42",
		"    output.topP = 0.7",
		"    output.topK = 12",
		"    output.maxOutputTokens = 345",
		"    output.options = { ...output.options, pluginOption: input.agent + ':' + input.model.modelID }",
		"  },",
		"  'chat.headers': (_input, output) => {",
		"    output.headers['X-Plugin-Chat'] = 'enabled'",
		"  },",
		"})",
		"",
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write plugin: %v", err)
	}
	info := config.Info{"plugin_origins": []any{map[string]any{
		"spec":   fileURL(plugin),
		"source": filepath.Join(root, "opencode.json"),
		"scope":  "local",
	}}}
	runtime := New(info, root, root)
	request := llm.ChatRequest{
		Options: map[string]any{"existing": true},
		Headers: map[string]string{"X-Existing": "yes"},
	}
	chat := ChatContext{
		SessionID: "session",
		Agent:     "build",
		Model:     map[string]any{"providerID": "openai", "modelID": "gpt-test"},
		Provider:  map[string]any{"source": "config"},
		Message:   map[string]any{},
	}
	if err := runtime.ApplyChatParams(context.Background(), &request, chat); err != nil {
		t.Fatalf("ApplyChatParams() error = %v", err)
	}
	if err := runtime.ApplyChatHeaders(context.Background(), &request, chat); err != nil {
		t.Fatalf("ApplyChatHeaders() error = %v", err)
	}
	if request.Temperature == nil || *request.Temperature != 0.42 ||
		request.TopP == nil || *request.TopP != 0.7 ||
		request.TopK == nil || *request.TopK != 12 ||
		request.MaxTokens == nil || *request.MaxTokens != 345 {
		t.Fatalf("request params = %#v, want plugin mutations", request)
	}
	if request.Options["pluginOption"] != "build:gpt-test" || request.Options["existing"] != true {
		t.Fatalf("options = %#v, want merged plugin option", request.Options)
	}
	if request.Headers["X-Plugin-Chat"] != "enabled" || request.Headers["X-Existing"] != "yes" {
		t.Fatalf("headers = %#v, want plugin and existing headers", request.Headers)
	}
}

func TestRuntimeRunsConfigHook(t *testing.T) {
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("bun is required for JS plugin hook execution")
	}
	root := t.TempDir()
	plugin := filepath.Join(root, "config-plugin.ts")
	if err := os.WriteFile(plugin, []byte(strings.Join([]string{
		"export default {",
		"  id: 'demo.config',",
		"  server: async () => ({",
		"    config(cfg) {",
		"      cfg.provider ??= {}",
		"      cfg.provider.demo = { name: 'Demo Provider', models: { chat: { name: 'Demo Chat' } } }",
		"      cfg.enabled_providers = ['demo']",
		"    }",
		"  })",
		"}",
		"",
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write plugin: %v", err)
	}
	info := config.Info{"plugin_origins": []any{map[string]any{
		"spec":   fileURL(plugin),
		"source": filepath.Join(root, "opencode.json"),
		"scope":  "local",
	}}}
	runtime := New(info, root, root)
	mutated, err := runtime.ApplyConfigHook(context.Background())
	if err != nil {
		t.Fatalf("ApplyConfigHook() error = %v", err)
	}
	providers := mutated["provider"].(map[string]any)
	demo := providers["demo"].(map[string]any)
	if demo["name"] != "Demo Provider" {
		t.Fatalf("provider = %#v, want plugin provider", demo)
	}
	if !anyStringSliceEqual(mutated["enabled_providers"], []string{"demo"}) {
		t.Fatalf("enabled_providers = %#v, want demo", mutated["enabled_providers"])
	}
}

func fileURL(path string) string {
	return "file://" + filepath.ToSlash(path)
}

func anyStringSliceEqual(value any, want []string) bool {
	items, ok := value.([]any)
	if !ok || len(items) != len(want) {
		return false
	}
	for index, item := range items {
		if item != want[index] {
			return false
		}
	}
	return true
}
