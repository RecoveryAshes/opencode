package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/tailscale/hujson"
)

// Info is the Go migration representation of opencode configuration.
//
// The TypeScript schema is intentionally broad and extensible. During the Go
// migration we preserve unknown fields as JSON-compatible values so the sidecar
// can round-trip and serve current configuration without prematurely narrowing
// provider, plugin, agent, and experimental shapes.
type Info map[string]any

// LoadOptions controls local configuration discovery.
type LoadOptions struct {
	Directory string
	Worktree  string
}

// LoadResult is returned by config inspection APIs.
type LoadResult struct {
	Info      Info      `json:"info"`
	Files     []string  `json:"files"`
	Dirs      []string  `json:"dirs"`
	Discovery Discovery `json:"discovery"`
}

// UpdateResult describes a persisted config update.
type UpdateResult struct {
	Info    Info   `json:"info"`
	Changed bool   `json:"changed"`
	File    string `json:"file"`
}

// Load reads global, explicit, project, .opencode, and environment-provided
// config sources using the same broad precedence order as the TypeScript
// runtime.
func Load(opts LoadOptions) (LoadResult, error) {
	directory, err := normalizeDirectory(opts.Directory)
	if err != nil {
		return LoadResult{}, err
	}
	worktree := opts.Worktree
	if worktree == "" {
		worktree = directory
	}
	worktree = filepath.Clean(worktree)

	result := LoadResult{
		Info: Info{},
	}

	globalDir := GlobalConfigDir()
	for _, file := range []string{
		filepath.Join(globalDir, "config.json"),
		filepath.Join(globalDir, "opencode.json"),
		filepath.Join(globalDir, "opencode.jsonc"),
	} {
		if err := result.mergeFile(file); err != nil {
			return LoadResult{}, err
		}
	}

	if explicit := os.Getenv("OPENCODE_CONFIG"); explicit != "" {
		if err := result.mergeFile(explicit); err != nil {
			return LoadResult{}, err
		}
	}

	if !truthyEnv("OPENCODE_DISABLE_PROJECT_CONFIG") {
		files, err := ProjectFiles("opencode", directory, worktree)
		if err != nil {
			return LoadResult{}, err
		}
		for _, file := range files {
			if err := result.mergeFile(file); err != nil {
				return LoadResult{}, err
			}
		}
	}

	dirs, err := ConfigDirectories(directory, worktree)
	if err != nil {
		return LoadResult{}, err
	}
	result.Dirs = dirs
	for _, dir := range dirs {
		if !strings.HasSuffix(filepath.Clean(dir), string(filepath.Separator)+".opencode") && dir != os.Getenv("OPENCODE_CONFIG_DIR") {
			continue
		}
		for _, file := range []string{
			filepath.Join(dir, "opencode.json"),
			filepath.Join(dir, "opencode.jsonc"),
		} {
			if err := result.mergeFile(file); err != nil {
				return LoadResult{}, err
			}
		}
	}

	if content := os.Getenv("OPENCODE_CONFIG_CONTENT"); strings.TrimSpace(content) != "" {
		next, err := ParseJSONC([]byte(content), "OPENCODE_CONFIG_CONTENT")
		if err != nil {
			return LoadResult{}, err
		}
		result.Info = mergeInfo(result.Info, next)
		result.Files = append(result.Files, "OPENCODE_CONFIG_CONTENT")
	}

	discovery, err := Discover(directory)
	if err != nil {
		return LoadResult{}, err
	}
	result.Discovery = discovery
	return result, nil
}

// ParseJSONC decodes JSON with comments and trailing commas into Info.
func ParseJSONC(data []byte, source string) (Info, error) {
	standard, err := hujson.Standardize(data)
	if err != nil {
		return nil, fmt.Errorf("parse jsonc %s: %w", source, err)
	}
	var value map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(standard)))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("decode config %s: %w", source, err)
	}
	if value == nil {
		return Info{}, nil
	}
	delete(value, "theme")
	delete(value, "keybinds")
	delete(value, "tui")
	return normalizeJSON(value).(map[string]any), nil
}

// UpdateLocal merges patch into DIRECTORY/config.json and returns the persisted
// local config. This mirrors the TypeScript local config update target.
func UpdateLocal(directory string, patch Info) (UpdateResult, error) {
	directory, err := normalizeDirectory(directory)
	if err != nil {
		return UpdateResult{}, err
	}
	file := filepath.Join(directory, "config.json")
	existing, err := readConfigFile(file)
	if err != nil {
		return UpdateResult{}, err
	}
	merged := mergeInfo(existing, writableInfo(patch, false))
	changed, err := writeConfigJSON(file, merged)
	if err != nil {
		return UpdateResult{}, err
	}
	return UpdateResult{Info: merged, Changed: changed, File: file}, nil
}

// UpdateGlobal merges patch into the selected global config file. It preserves
// jsonc files by writing JSON-compatible content back to the same path.
func UpdateGlobal(patch Info) (UpdateResult, error) {
	file := globalConfigFile()
	existing, err := readConfigFile(file)
	if err != nil {
		return UpdateResult{}, err
	}
	merged := mergeInfo(existing, writableInfo(patch, true))
	if shell, ok := patch["shell"].(string); ok && shell == "" {
		delete(merged, "shell")
	}
	changed, err := writeConfigJSON(file, merged)
	if err != nil {
		return UpdateResult{}, err
	}
	return UpdateResult{Info: merged, Changed: changed, File: file}, nil
}

// ProjectFiles returns opencode.jsonc/opencode.json files from directory upward
// to the worktree boundary, ordered from root to leaf.
func ProjectFiles(name string, directory string, worktree string) ([]string, error) {
	directory, err := normalizeDirectory(directory)
	if err != nil {
		return nil, err
	}
	if worktree == "" {
		worktree = directory
	}
	worktree, err = filepath.Abs(worktree)
	if err != nil {
		return nil, fmt.Errorf("resolve worktree: %w", err)
	}
	worktree = filepath.Clean(worktree)

	files := []string{}
	for _, dir := range dirsUp(directory, worktree) {
		for _, candidate := range []string{
			filepath.Join(dir, name+".jsonc"),
			filepath.Join(dir, name+".json"),
		} {
			if regularFile(candidate) {
				files = append(files, candidate)
			}
		}
	}
	slices.Reverse(files)
	return files, nil
}

// ConfigDirectories returns global, discovered project .opencode, home
// .opencode, and explicit OPENCODE_CONFIG_DIR directories in TypeScript order.
func ConfigDirectories(directory string, worktree string) ([]string, error) {
	directory, err := normalizeDirectory(directory)
	if err != nil {
		return nil, err
	}
	if worktree == "" {
		worktree = directory
	}
	worktreeAbs, err := filepath.Abs(worktree)
	if err != nil {
		return nil, fmt.Errorf("resolve worktree: %w", err)
	}
	worktreeAbs = filepath.Clean(worktreeAbs)

	result := []string{GlobalConfigDir()}
	if !truthyEnv("OPENCODE_DISABLE_PROJECT_CONFIG") {
		for _, dir := range dirsUp(directory, worktreeAbs) {
			candidate := filepath.Join(dir, ".opencode")
			if fileInfo, err := os.Stat(candidate); err == nil && fileInfo.IsDir() {
				result = append(result, candidate)
			}
		}
	}
	if home := os.Getenv("OPENCODE_TEST_HOME"); home != "" {
		if candidate := filepath.Join(home, ".opencode"); dirExists(candidate) {
			result = append(result, candidate)
		}
	} else if home, err := os.UserHomeDir(); err == nil {
		if candidate := filepath.Join(home, ".opencode"); dirExists(candidate) {
			result = append(result, candidate)
		}
	}
	if explicit := os.Getenv("OPENCODE_CONFIG_DIR"); explicit != "" {
		result = append(result, explicit)
	}
	return uniqueStrings(result), nil
}

// GlobalConfigDir returns the opencode XDG config directory.
func GlobalConfigDir() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		if home, err := os.UserHomeDir(); err == nil {
			base = filepath.Join(home, ".config")
		}
	}
	if base == "" {
		base = "."
	}
	return filepath.Join(base, "opencode")
}

func globalConfigFile() string {
	dir := GlobalConfigDir()
	for _, name := range []string{"opencode.jsonc", "opencode.json", "config.json"} {
		file := filepath.Join(dir, name)
		if regularFile(file) {
			return file
		}
	}
	return filepath.Join(dir, "opencode.jsonc")
}

func readConfigFile(path string) (Info, error) {
	if !regularFile(path) {
		return Info{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return Info{}, nil
	}
	return ParseJSONC(data, path)
}

func writeConfigJSON(path string, info Info) (bool, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, fmt.Errorf("create config directory %s: %w", filepath.Dir(path), err)
	}
	next, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return false, fmt.Errorf("encode config %s: %w", path, err)
	}
	next = append(next, '\n')
	before, err := os.ReadFile(path)
	if err == nil && string(before) == string(next) {
		return false, nil
	}
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("read config %s: %w", path, err)
	}
	if err := os.WriteFile(path, next, 0o644); err != nil {
		return false, fmt.Errorf("write config %s: %w", path, err)
	}
	return true, nil
}

func writableInfo(info Info, global bool) Info {
	result := Info{}
	for key, value := range info {
		if key == "plugin_origins" {
			continue
		}
		if global && key == "shell" {
			if text, ok := value.(string); ok && text == "" {
				continue
			}
		}
		result[key] = value
	}
	return result
}

func (result *LoadResult) mergeFile(path string) error {
	if !regularFile(path) {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config %s: %w", path, err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return nil
	}
	next, err := ParseJSONC(data, path)
	if err != nil {
		return err
	}
	result.Info = mergeInfo(result.Info, next)
	result.Files = append(result.Files, path)
	return nil
}

func mergeInfo(left Info, right Info) Info {
	merged := mergeMap(map[string]any(left), map[string]any(right))
	if leftInstructions, ok := left["instructions"].([]any); ok {
		if rightInstructions, ok := right["instructions"].([]any); ok {
			merged["instructions"] = uniqueAny(append(append([]any{}, leftInstructions...), rightInstructions...))
		}
	}
	return Info(merged)
}

func mergeMap(left map[string]any, right map[string]any) map[string]any {
	result := map[string]any{}
	for key, value := range left {
		result[key] = value
	}
	for key, value := range right {
		leftMap, leftOK := result[key].(map[string]any)
		rightMap, rightOK := value.(map[string]any)
		if leftOK && rightOK {
			result[key] = mergeMap(leftMap, rightMap)
			continue
		}
		result[key] = value
	}
	return result
}

func normalizeJSON(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			typed[key] = normalizeJSON(item)
		}
		return typed
	case []any:
		for index, item := range typed {
			typed[index] = normalizeJSON(item)
		}
		return typed
	case json.Number:
		if integer, err := typed.Int64(); err == nil {
			return integer
		}
		if decimal, err := typed.Float64(); err == nil {
			return decimal
		}
		return typed.String()
	default:
		return value
	}
}

func normalizeDirectory(directory string) (string, error) {
	if directory == "" {
		directory = "."
	}
	abs, err := filepath.Abs(directory)
	if err != nil {
		return "", fmt.Errorf("resolve directory: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("stat directory: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("not a directory: %s", abs)
	}
	return filepath.Clean(abs), nil
}

func dirsUp(start string, stop string) []string {
	start = filepath.Clean(start)
	stop = filepath.Clean(stop)
	result := []string{}
	for {
		result = append(result, start)
		if start == stop {
			break
		}
		parent := filepath.Dir(start)
		if parent == start {
			break
		}
		start = parent
	}
	return result
}

func regularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func truthyEnv(name string) bool {
	switch strings.ToLower(os.Getenv(name)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func uniqueStrings(input []string) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, item := range input {
		if seen[item] {
			continue
		}
		seen[item] = true
		result = append(result, item)
	}
	return result
}

func uniqueAny(input []any) []any {
	result := []any{}
	for _, item := range input {
		exists := false
		for _, current := range result {
			if reflect.DeepEqual(current, item) {
				exists = true
				break
			}
		}
		if !exists {
			result = append(result, item)
		}
	}
	return result
}
