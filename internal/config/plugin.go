package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// PluginSpec is the user-facing plugin config value. It is either a string or a
// two-item tuple of plugin spec and options.
type PluginSpec any

// PluginOrigin preserves config provenance for a winning plugin spec.
type PluginOrigin struct {
	Spec   PluginSpec `json:"spec"`
	Source string     `json:"source"`
	Scope  string     `json:"scope"`
}

// PluginInfo is the Go inspection/install view of a plugin config entry.
type PluginInfo struct {
	Spec    PluginSpec `json:"spec"`
	Name    string     `json:"name"`
	Source  string     `json:"source"`
	Scope   string     `json:"scope"`
	Kind    string     `json:"kind"`
	Options Info       `json:"options,omitempty"`
}

// PluginInstallResult describes an installed plugin and config writes.
type PluginInstallResult struct {
	Spec    string            `json:"spec"`
	Target  string            `json:"target"`
	Targets []PluginTarget    `json:"targets"`
	Updates []PluginConfigHit `json:"updates"`
}

// PluginTarget is a server or TUI target found in a plugin package manifest.
type PluginTarget struct {
	Kind    string `json:"kind"`
	Options Info   `json:"options,omitempty"`
}

// PluginConfigHit reports one config file patch made by InstallPlugin.
type PluginConfigHit struct {
	Kind string `json:"kind"`
	Mode string `json:"mode"`
	File string `json:"file"`
}

// PluginInstallOptions controls plugin config patching.
type PluginInstallOptions struct {
	Directory string
	Worktree  string
	Global    bool
	Force     bool
}

type pluginPackage struct {
	Name     string         `json:"name"`
	Main     string         `json:"main"`
	Exports  map[string]any `json:"exports"`
	OCThemes []string       `json:"oc-themes"`
}

// ListPlugins returns resolved plugin entries using the same provenance rules
// as Load.
func ListPlugins(directory string, worktree string) ([]PluginInfo, error) {
	loaded, err := Load(LoadOptions{Directory: directory, Worktree: worktree})
	if err != nil {
		return nil, err
	}
	origins := pluginOriginsFromInfo(loaded.Info)
	result := make([]PluginInfo, 0, len(origins))
	for _, origin := range origins {
		spec := PluginSpecifier(origin.Spec)
		info := PluginInfo{
			Spec:   origin.Spec,
			Name:   pluginIdentity(spec),
			Source: origin.Source,
			Scope:  origin.Scope,
			Kind:   PluginSourceKind(spec),
		}
		if options := PluginOptions(origin.Spec); len(options) > 0 {
			info.Options = options
		}
		result = append(result, info)
	}
	return result, nil
}

// InstallPlugin installs an npm plugin into the opencode cache or validates a
// local plugin target, then patches the appropriate config files.
func InstallPlugin(spec string, opts PluginInstallOptions) (PluginInstallResult, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return PluginInstallResult{}, fmt.Errorf("plugin module is required")
	}
	target, err := ResolvePluginTarget(spec)
	if err != nil {
		return PluginInstallResult{}, err
	}
	targets, err := ReadPluginManifest(spec, target)
	if err != nil {
		return PluginInstallResult{}, err
	}
	if len(targets) == 0 {
		return PluginInstallResult{}, fmt.Errorf("plugin %s does not expose server or tui targets", spec)
	}
	updates, err := PatchPluginConfig(spec, targets, opts)
	if err != nil {
		return PluginInstallResult{}, err
	}
	return PluginInstallResult{Spec: spec, Target: target, Targets: targets, Updates: updates}, nil
}

// ResolvePluginTarget resolves a plugin spec to a local file/package directory.
func ResolvePluginTarget(spec string) (string, error) {
	if IsPathPluginSpec(spec) {
		return ResolvePathPluginTarget(spec)
	}
	return installNPMPlugin(spec)
}

// ReadPluginManifest inspects a package target and returns exposed plugin
// targets. Local files without package.json default to a server target.
func ReadPluginManifest(spec string, target string) ([]PluginTarget, error) {
	file := target
	if strings.HasPrefix(file, "file://") {
		parsed, err := url.Parse(file)
		if err != nil {
			return nil, fmt.Errorf("parse plugin file url: %w", err)
		}
		file = parsed.Path
	}
	info, err := os.Stat(file)
	if err != nil {
		return nil, fmt.Errorf("stat plugin target %s: %w", target, err)
	}
	dir := file
	if !info.IsDir() {
		dir = filepath.Dir(file)
	}
	pkgPath := filepath.Join(dir, "package.json")
	data, err := os.ReadFile(pkgPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && !info.IsDir() {
			return []PluginTarget{{Kind: "server"}}, nil
		}
		return nil, fmt.Errorf("read plugin manifest %s: %w", pkgPath, err)
	}
	var pkg pluginPackage
	if err := json.Unmarshal(data, &pkg); err != nil {
		return nil, fmt.Errorf("decode plugin manifest %s: %w", pkgPath, err)
	}
	targets := []PluginTarget{}
	if target := exportTarget(pkg.Exports["./server"]); target != nil {
		target.Kind = "server"
		targets = append(targets, *target)
	} else if strings.TrimSpace(pkg.Main) != "" {
		targets = append(targets, PluginTarget{Kind: "server"})
	}
	if target := exportTarget(pkg.Exports["./tui"]); target != nil {
		target.Kind = "tui"
		targets = append(targets, *target)
	}
	if len(pkg.OCThemes) > 0 && !hasPluginTarget(targets, "tui") {
		targets = append(targets, PluginTarget{Kind: "tui"})
	}
	return targets, nil
}

// PatchPluginConfig updates opencode/tui plugin arrays for plugin targets.
func PatchPluginConfig(spec string, targets []PluginTarget, opts PluginInstallOptions) ([]PluginConfigHit, error) {
	dir, err := pluginPatchDir(opts)
	if err != nil {
		return nil, err
	}
	result := []PluginConfigHit{}
	for _, target := range targets {
		name := "opencode"
		if target.Kind == "tui" {
			name = "tui"
		}
		file, existing, err := firstConfigFile(dir, name)
		if err != nil {
			return nil, err
		}
		item := pluginConfigItem(spec, target.Options)
		nextList, mode := patchPluginList(existing["plugin"], spec, item, opts.Force)
		if mode != "noop" {
			existing["plugin"] = nextList
			if _, err := writeConfigJSON(file, existing); err != nil {
				return nil, err
			}
		}
		result = append(result, PluginConfigHit{Kind: target.Kind, Mode: mode, File: file})
	}
	return result, nil
}

// PluginSpecifier returns the string part of a plugin spec.
func PluginSpecifier(spec PluginSpec) string {
	if text, ok := spec.(string); ok {
		return text
	}
	if list, ok := spec.([]any); ok && len(list) > 0 {
		if text, ok := list[0].(string); ok {
			return text
		}
	}
	return ""
}

// PluginOptions returns plugin tuple options, if present.
func PluginOptions(spec PluginSpec) Info {
	list, ok := spec.([]any)
	if !ok || len(list) < 2 {
		return nil
	}
	if opts, ok := list[1].(map[string]any); ok {
		return Info(opts)
	}
	return nil
}

// PluginSourceKind classifies a plugin spec as file or npm.
func PluginSourceKind(spec string) string {
	if IsPathPluginSpec(spec) {
		return "file"
	}
	return "npm"
}

// IsPathPluginSpec reports whether a plugin spec points to a local file or dir.
func IsPathPluginSpec(spec string) bool {
	if strings.HasPrefix(spec, "file://") || strings.HasPrefix(spec, ".") || filepath.IsAbs(spec) {
		return true
	}
	return windowsAbsPattern.MatchString(spec)
}

// ResolvePathPluginTarget normalizes local plugin specs to file:// URLs.
func ResolvePathPluginTarget(spec string) (string, error) {
	raw := spec
	if strings.HasPrefix(spec, "file://") {
		parsed, err := url.Parse(spec)
		if err != nil {
			return "", fmt.Errorf("parse plugin file url: %w", err)
		}
		raw = parsed.Path
	}
	if !filepath.IsAbs(raw) && !windowsAbsPattern.MatchString(raw) {
		var err error
		raw, err = filepath.Abs(raw)
		if err != nil {
			return "", fmt.Errorf("resolve plugin path: %w", err)
		}
	}
	info, err := os.Stat(raw)
	if err != nil {
		if strings.HasPrefix(spec, "file://") {
			return spec, nil
		}
		return pathToFileURL(raw), nil
	}
	if !info.IsDir() {
		return pathToFileURL(raw), nil
	}
	if regularFile(filepath.Join(raw, "package.json")) {
		return pathToFileURL(raw), nil
	}
	for _, name := range []string{"index.ts", "index.tsx", "index.js", "index.mjs", "index.cjs"} {
		candidate := filepath.Join(raw, name)
		if regularFile(candidate) {
			return pathToFileURL(candidate), nil
		}
	}
	return "", fmt.Errorf("plugin directory %s is missing package.json or index file", raw)
}

// ResolvePluginSpec resolves path-like plugin specs relative to a config file.
func ResolvePluginSpec(spec PluginSpec, configFile string) (PluginSpec, error) {
	text := PluginSpecifier(spec)
	if text == "" || !IsPathPluginSpec(text) {
		return spec, nil
	}
	raw := text
	if !strings.HasPrefix(text, "file://") && !filepath.IsAbs(text) && !windowsAbsPattern.MatchString(text) {
		raw = filepath.Join(filepath.Dir(configFile), text)
	}
	resolved, err := ResolvePathPluginTarget(raw)
	if err != nil {
		if !strings.HasPrefix(raw, "file://") {
			resolved = pathToFileURL(raw)
		} else {
			resolved = raw
		}
	}
	if list, ok := spec.([]any); ok {
		next := append([]any{}, list...)
		next[0] = resolved
		return next, nil
	}
	return resolved, nil
}

// DiscoverPlugins finds local JS/TS plugins under singular and plural plugin
// directories.
func DiscoverPlugins(dir string) ([]PluginSpec, error) {
	files, err := filesInDirs(
		[]string{".ts", ".js"},
		filepath.Join(dir, "plugin"),
		filepath.Join(dir, "plugins"),
	)
	if err != nil {
		return nil, err
	}
	result := make([]PluginSpec, 0, len(files))
	for _, file := range files {
		result = append(result, pathToFileURL(file))
	}
	return result, nil
}

func pluginOriginsFromInfo(info Info) []PluginOrigin {
	values, ok := info["plugin_origins"].([]any)
	if !ok {
		return nil
	}
	result := []PluginOrigin{}
	for _, value := range values {
		record, ok := value.(map[string]any)
		if !ok {
			continue
		}
		spec, ok := record["spec"]
		if !ok {
			continue
		}
		origin := PluginOrigin{Spec: spec}
		if source, ok := record["source"].(string); ok {
			origin.Source = source
		}
		if scope, ok := record["scope"].(string); ok {
			origin.Scope = scope
		}
		result = append(result, origin)
	}
	return result
}

// PluginOrigins returns the derived plugin provenance stored by Load.
func PluginOrigins(info Info) []PluginOrigin {
	return pluginOriginsFromInfo(info)
}

func mergePluginOrigins(existing []PluginOrigin, source string, scope string, specs []PluginSpec) []PluginOrigin {
	if len(specs) == 0 {
		return existing
	}
	next := append([]PluginOrigin{}, existing...)
	for _, spec := range specs {
		next = append(next, PluginOrigin{Spec: spec, Source: source, Scope: scope})
	}
	return deduplicatePluginOrigins(next)
}

func deduplicatePluginOrigins(input []PluginOrigin) []PluginOrigin {
	seen := map[string]bool{}
	result := []PluginOrigin{}
	for i := len(input) - 1; i >= 0; i-- {
		item := input[i]
		spec := PluginSpecifier(item.Spec)
		identity := pluginIdentity(spec)
		if seen[identity] {
			continue
		}
		seen[identity] = true
		result = append(result, item)
	}
	slices.Reverse(result)
	return result
}

func pluginIdentity(spec string) string {
	if strings.HasPrefix(spec, "file://") {
		return spec
	}
	return parsePluginPackage(spec)
}

func parsePluginPackage(spec string) string {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return spec
	}
	if strings.HasPrefix(spec, "npm:") {
		rest := strings.TrimPrefix(spec, "npm:")
		return parsePluginPackage(rest)
	}
	if strings.HasPrefix(spec, "@") {
		parts := strings.Split(spec, "/")
		if len(parts) >= 2 {
			return parts[0] + "/" + strings.Split(parts[1], "@")[0]
		}
		return spec
	}
	return strings.Split(spec, "@")[0]
}

func pluginScopeForSource(source string, directory string) string {
	source = filepath.Clean(source)
	global := filepath.Clean(GlobalConfigDir())
	if source == global || strings.HasPrefix(source, global+string(filepath.Separator)) {
		return "global"
	}
	return "local"
}

func pluginSpecsFromInfo(info Info, source string) ([]PluginSpec, error) {
	raw, ok := info["plugin"]
	if !ok {
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("plugin must be an array in %s", source)
	}
	result := make([]PluginSpec, 0, len(list))
	for _, item := range list {
		switch typed := item.(type) {
		case string:
			resolved, err := ResolvePluginSpec(typed, source)
			if err != nil {
				return nil, err
			}
			result = append(result, resolved)
		case []any:
			if len(typed) == 0 {
				return nil, fmt.Errorf("plugin tuple is empty in %s", source)
			}
			if _, ok := typed[0].(string); !ok {
				return nil, fmt.Errorf("plugin tuple spec must be a string in %s", source)
			}
			resolved, err := ResolvePluginSpec(typed, source)
			if err != nil {
				return nil, err
			}
			result = append(result, resolved)
		default:
			return nil, fmt.Errorf("plugin entry must be a string or tuple in %s", source)
		}
	}
	return result, nil
}

func exportTarget(value any) *PluginTarget {
	switch typed := value.(type) {
	case string:
		if strings.TrimSpace(typed) == "" {
			return nil
		}
		return &PluginTarget{}
	case map[string]any:
		if !hasExportValue(typed) {
			return nil
		}
		target := PluginTarget{}
		if config, ok := typed["config"].(map[string]any); ok {
			target.Options = Info(config)
		}
		return &target
	default:
		return nil
	}
}

func hasExportValue(record map[string]any) bool {
	for _, key := range []string{"import", "default"} {
		if text, ok := record[key].(string); ok && strings.TrimSpace(text) != "" {
			return true
		}
	}
	return false
}

func hasPluginTarget(targets []PluginTarget, kind string) bool {
	for _, target := range targets {
		if target.Kind == kind {
			return true
		}
	}
	return false
}

func pluginConfigItem(spec string, options Info) any {
	if len(options) == 0 {
		return spec
	}
	return []any{spec, map[string]any(options)}
}

func patchPluginList(raw any, spec string, item any, force bool) ([]any, string) {
	list, _ := raw.([]any)
	identity := pluginIdentity(spec)
	dups := []int{}
	for i, existing := range list {
		existingSpec := PluginSpecifier(existing)
		if existingSpec == "" {
			continue
		}
		if existingSpec == spec || (!strings.HasPrefix(existingSpec, "file://") && pluginIdentity(existingSpec) == identity) {
			dups = append(dups, i)
		}
	}
	if len(dups) == 0 {
		return append(append([]any{}, list...), item), "add"
	}
	if !force {
		return append([]any{}, list...), "noop"
	}
	next := append([]any{}, list...)
	keep := dups[0]
	next[keep] = item
	for i := len(dups) - 1; i >= 1; i-- {
		index := dups[i]
		next = append(next[:index], next[index+1:]...)
	}
	return next, "replace"
}

func pluginPatchDir(opts PluginInstallOptions) (string, error) {
	if opts.Global {
		return GlobalConfigDir(), nil
	}
	directory, err := normalizeDirectory(opts.Directory)
	if err != nil {
		return "", err
	}
	worktree := opts.Worktree
	if worktree == "" {
		worktree = directory
	}
	return filepath.Join(filepath.Clean(worktree), ".opencode"), nil
}

func firstConfigFile(dir string, name string) (string, Info, error) {
	candidates := []string{
		filepath.Join(dir, name+".json"),
		filepath.Join(dir, name+".jsonc"),
	}
	file := candidates[0]
	for _, candidate := range candidates {
		if regularFile(candidate) {
			file = candidate
			break
		}
	}
	existing, err := readConfigFile(file)
	if err != nil {
		return "", nil, err
	}
	return file, existing, nil
}

func installNPMPlugin(spec string) (string, error) {
	npm, err := exec.LookPath("npm")
	if err != nil {
		return "", fmt.Errorf("npm executable not found: %w", err)
	}
	pkg := spec
	if !strings.Contains(spec, "@") || strings.HasPrefix(spec, "@") && strings.Count(spec, "@") == 1 {
		pkg = spec + "@latest"
	}
	dir := filepath.Join(GlobalCacheDir(), "packages", sanitizePluginCacheKey(pkg))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create plugin cache %s: %w", dir, err)
	}
	cmd := exec.Command(npm, "install", "--ignore-scripts", "--save-exact", "--no-audit", "--no-fund", pkg)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("install plugin %s: %w\n%s", spec, err, strings.TrimSpace(string(output)))
	}
	name := parsePluginPackage(spec)
	target := filepath.Join(dir, "node_modules", name)
	if _, err := os.Stat(target); err != nil {
		return "", fmt.Errorf("installed plugin target not found %s: %w", target, err)
	}
	return target, nil
}

func GlobalCacheDir() string {
	base := os.Getenv("XDG_CACHE_HOME")
	if base == "" {
		if home, err := os.UserHomeDir(); err == nil {
			base = filepath.Join(home, ".cache")
		}
	}
	if base == "" {
		base = "."
	}
	return filepath.Join(base, "opencode")
}

func sanitizePluginCacheKey(spec string) string {
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_", "@", "_")
	return replacer.Replace(spec)
}

func pathToFileURL(path string) string {
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
}

var windowsAbsPattern = regexp.MustCompile(`^[A-Za-z]:[\\/]`)
