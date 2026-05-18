// Package pluginruntime executes migrated server plugin hooks for the Go
// sidecar runtime.
package pluginruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/RecoveryAshes/opencode/internal/config"
	"github.com/RecoveryAshes/opencode/internal/integration"
	"github.com/RecoveryAshes/opencode/internal/llm"
)

// Runtime owns Go-side server plugin hook execution for one loaded config.
type Runtime struct {
	Info      config.Info
	Directory string
	Worktree  string
	BunPath   string
	ServerURL string

	mu      sync.Mutex
	loaded  bool
	plugins []loadedPlugin
}

type loadedPlugin struct {
	Spec    string         `json:"spec"`
	Entry   string         `json:"entry"`
	Options map[string]any `json:"options,omitempty"`
}

type bridgeRequest struct {
	Plugins   []loadedPlugin `json:"plugins"`
	Hook      string         `json:"hook"`
	Input     map[string]any `json:"input"`
	Output    map[string]any `json:"output"`
	Config    config.Info    `json:"config,omitempty"`
	Directory string         `json:"directory"`
	Worktree  string         `json:"worktree"`
	ServerURL string         `json:"serverUrl,omitempty"`
}

// New creates a server plugin runtime from already-loaded configuration.
func New(info config.Info, directory string, worktree string) *Runtime {
	return &Runtime{
		Info:      info,
		Directory: directory,
		Worktree:  worktree,
	}
}

// Enabled reports whether this runtime has any configured plugin origins.
func (runtime *Runtime) Enabled() bool {
	return runtime != nil && len(config.PluginOrigins(runtime.Info)) > 0
}

// ApplyToolDefinitions runs the tool.definition hook against provider-visible
// tool definitions.
func (runtime *Runtime) ApplyToolDefinitions(ctx context.Context, definitions []llm.ToolDefinition) ([]llm.ToolDefinition, error) {
	if !runtime.Enabled() || len(definitions) == 0 {
		return definitions, nil
	}
	result := make([]llm.ToolDefinition, 0, len(definitions))
	for _, definition := range definitions {
		output, err := runtime.Trigger(ctx, "tool.definition", map[string]any{
			"toolID": definition.Name,
		}, map[string]any{
			"description": definition.Description,
			"parameters":  cloneAnyMap(definition.Parameters),
		})
		if err != nil {
			return nil, err
		}
		if description, ok := output["description"].(string); ok {
			definition.Description = description
		}
		if parameters, ok := output["parameters"].(map[string]any); ok {
			definition.Parameters = parameters
		}
		result = append(result, definition)
	}
	return result, nil
}

// BeforeToolExecute runs tool.execute.before and returns the effective args.
func (runtime *Runtime) BeforeToolExecute(ctx context.Context, tool string, sessionID string, callID string, args map[string]any) (map[string]any, error) {
	if !runtime.Enabled() {
		return args, nil
	}
	output, err := runtime.Trigger(ctx, "tool.execute.before", map[string]any{
		"tool":      tool,
		"sessionID": sessionID,
		"callID":    callID,
	}, map[string]any{
		"args": cloneAnyMap(args),
	})
	if err != nil {
		return nil, err
	}
	if patched, ok := output["args"].(map[string]any); ok {
		return patched, nil
	}
	return args, nil
}

// AfterToolExecute runs tool.execute.after and returns the effective result.
func (runtime *Runtime) AfterToolExecute(ctx context.Context, tool string, sessionID string, callID string, args map[string]any, result integration.Result) (integration.Result, error) {
	if !runtime.Enabled() {
		return result, nil
	}
	output, err := runtime.Trigger(ctx, "tool.execute.after", map[string]any{
		"tool":      tool,
		"sessionID": sessionID,
		"callID":    callID,
		"args":      cloneAnyMap(args),
	}, map[string]any{
		"title":    result.Title,
		"output":   result.Output,
		"metadata": cloneAnyMap(result.Metadata),
	})
	if err != nil {
		return integration.Result{}, err
	}
	if title, ok := output["title"].(string); ok {
		result.Title = title
	}
	if text, ok := output["output"].(string); ok {
		result.Output = text
	}
	if metadata, ok := output["metadata"].(map[string]any); ok {
		result.Metadata = metadata
	}
	return result, nil
}

// ShellEnv runs shell.env and returns extra environment variables.
func (runtime *Runtime) ShellEnv(ctx context.Context, cwd string, sessionID string, callID string) (map[string]string, error) {
	if !runtime.Enabled() {
		return nil, nil
	}
	output, err := runtime.Trigger(ctx, "shell.env", map[string]any{
		"cwd":       cwd,
		"sessionID": sessionID,
		"callID":    callID,
	}, map[string]any{
		"env": map[string]any{},
	})
	if err != nil {
		return nil, err
	}
	raw, ok := output["env"].(map[string]any)
	if !ok {
		return nil, nil
	}
	env := map[string]string{}
	for key, value := range raw {
		if text, ok := value.(string); ok {
			env[key] = text
		}
	}
	return env, nil
}

// Trigger runs one hook across configured server plugins in config order.
func (runtime *Runtime) Trigger(ctx context.Context, hook string, input map[string]any, output map[string]any) (map[string]any, error) {
	if !runtime.Enabled() {
		return output, nil
	}
	if err := runtime.ensureLoaded(ctx); err != nil {
		return nil, err
	}
	if len(runtime.plugins) == 0 {
		return output, nil
	}
	bun, err := runtime.bun()
	if err != nil {
		return nil, err
	}
	payload := bridgeRequest{
		Plugins:   runtime.plugins,
		Hook:      hook,
		Input:     input,
		Output:    output,
		Config:    runtime.Info,
		Directory: defaultString(runtime.Directory, "."),
		Worktree:  defaultString(runtime.Worktree, defaultString(runtime.Directory, ".")),
		ServerURL: runtime.ServerURL,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode plugin hook payload: %w", err)
	}
	cmd := exec.CommandContext(ctx, bun, "--eval", bridgeScript)
	cmd.Dir = defaultString(runtime.Directory, ".")
	cmd.Stdin = bytes.NewReader(data)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("run plugin hook %s: %w\n%s", hook, err, strings.TrimSpace(stderr.String()))
	}
	var result map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return nil, fmt.Errorf("decode plugin hook %s output: %w\n%s", hook, err, strings.TrimSpace(stderr.String()))
	}
	return result, nil
}

func (runtime *Runtime) ensureLoaded(ctx context.Context) error {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.loaded {
		return nil
	}
	origins := config.PluginOrigins(runtime.Info)
	plugins := make([]loadedPlugin, 0, len(origins))
	for _, origin := range origins {
		if err := ctx.Err(); err != nil {
			return err
		}
		spec := config.PluginSpecifier(origin.Spec)
		if spec == "" || isDeprecatedPlugin(spec) {
			continue
		}
		target, err := config.ResolvePluginTarget(spec)
		if err != nil {
			continue
		}
		entry, err := ResolveServerEntry(spec, target)
		if err != nil {
			continue
		}
		if entry == "" {
			continue
		}
		plugins = append(plugins, loadedPlugin{
			Spec:    spec,
			Entry:   entry,
			Options: map[string]any(config.PluginOptions(origin.Spec)),
		})
	}
	runtime.plugins = plugins
	runtime.loaded = true
	return nil
}

func (runtime *Runtime) bun() (string, error) {
	if runtime.BunPath != "" {
		return runtime.BunPath, nil
	}
	bin, err := exec.LookPath("bun")
	if err != nil {
		return "", fmt.Errorf("bun executable not found for plugin runtime: %w", err)
	}
	return bin, nil
}

func isDeprecatedPlugin(spec string) bool {
	return strings.Contains(spec, "opencode-openai-codex-auth") || strings.Contains(spec, "opencode-copilot-auth")
}

type pluginPackage struct {
	Dir     string
	Path    string
	Main    string
	Exports map[string]any
}

// ResolveServerEntry resolves a plugin target to the server entrypoint imported
// by the Go hook bridge.
func ResolveServerEntry(spec string, target string) (string, error) {
	file, err := fileURLPath(target)
	if err != nil {
		return "", err
	}
	info, statErr := os.Stat(file)
	if statErr != nil {
		return "", fmt.Errorf("stat plugin target %s: %w", target, statErr)
	}
	pkg, pkgErr := readPluginPackage(file, info)
	if pkgErr != nil && !errors.Is(pkgErr, os.ErrNotExist) {
		return "", pkgErr
	}
	if pkgErr == nil {
		if entry, err := packageServerEntry(spec, pkg); err != nil || entry != "" {
			return entry, err
		}
		if !info.IsDir() {
			return pathToFileURL(file), nil
		}
		if config.PluginSourceKind(spec) == "file" {
			if index := directoryIndex(file); index != "" {
				return pathToFileURL(index), nil
			}
		}
		return "", nil
	}
	if info.IsDir() {
		index := directoryIndex(file)
		if index == "" {
			return "", nil
		}
		return pathToFileURL(index), nil
	}
	return pathToFileURL(file), nil
}

func readPluginPackage(file string, info os.FileInfo) (pluginPackage, error) {
	dir := file
	if !info.IsDir() {
		dir = filepath.Dir(file)
	}
	path := filepath.Join(dir, "package.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return pluginPackage{}, err
	}
	var raw struct {
		Main    string         `json:"main"`
		Exports map[string]any `json:"exports"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return pluginPackage{}, fmt.Errorf("decode plugin manifest %s: %w", path, err)
	}
	return pluginPackage{Dir: dir, Path: path, Main: raw.Main, Exports: raw.Exports}, nil
}

func packageServerEntry(spec string, pkg pluginPackage) (string, error) {
	if raw := exportValue(pkg.Exports["./server"]); raw != "" {
		entry, err := resolvePackageFile(spec, raw, "server", pkg)
		if err != nil {
			return "", err
		}
		return pathToFileURL(entry), nil
	}
	if strings.TrimSpace(pkg.Main) != "" {
		entry, err := resolvePackageFile(spec, pkg.Main, "main", pkg)
		if err != nil {
			return "", err
		}
		return pathToFileURL(entry), nil
	}
	return "", nil
}

func exportValue(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case map[string]any:
		for _, key := range []string{"import", "default"} {
			if text, ok := typed[key].(string); ok && strings.TrimSpace(text) != "" {
				return strings.TrimSpace(text)
			}
		}
	}
	return ""
}

func resolvePackageFile(spec string, raw string, kind string, pkg pluginPackage) (string, error) {
	var resolved string
	if strings.HasPrefix(raw, "file://") {
		file, err := fileURLPath(raw)
		if err != nil {
			return "", err
		}
		resolved = file
	} else if filepath.IsAbs(raw) {
		resolved = raw
	} else {
		resolved = filepath.Join(pkg.Dir, raw)
	}
	cleanDir := filepath.Clean(pkg.Dir)
	cleanEntry := filepath.Clean(resolved)
	rel, err := filepath.Rel(cleanDir, cleanEntry)
	if err != nil || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." || filepath.IsAbs(rel) {
		return "", fmt.Errorf("plugin %s resolved %s entry outside plugin directory", spec, kind)
	}
	return cleanEntry, nil
}

func directoryIndex(dir string) string {
	for _, name := range []string{"index.ts", "index.tsx", "index.js", "index.mjs", "index.cjs"} {
		candidate := filepath.Join(dir, name)
		info, err := os.Stat(candidate)
		if err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

func fileURLPath(value string) (string, error) {
	if !strings.HasPrefix(value, "file://") {
		return value, nil
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return "", fmt.Errorf("parse plugin file url: %w", err)
	}
	return parsed.Path, nil
}

func pathToFileURL(path string) string {
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
}

func cloneAnyMap(input map[string]any) map[string]any {
	if len(input) == 0 {
		return map[string]any{}
	}
	result := map[string]any{}
	for key, value := range input {
		result[key] = cloneAny(value)
	}
	return result
}

func cloneAny(input any) any {
	switch value := input.(type) {
	case map[string]any:
		return cloneAnyMap(value)
	case []any:
		result := make([]any, 0, len(value))
		for _, item := range value {
			result = append(result, cloneAny(item))
		}
		return result
	default:
		return value
	}
}

func defaultString(value string, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
