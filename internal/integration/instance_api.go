package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/RecoveryAshes/opencode/internal/config"
)

const globalProjectID = "global"

// PathInfo mirrors the /path response from the TypeScript instance API.
type PathInfo struct {
	Home      string `json:"home"`
	State     string `json:"state"`
	Config    string `json:"config"`
	Worktree  string `json:"worktree"`
	Directory string `json:"directory"`
}

// VCSInfo describes the current git branch and default branch.
type VCSInfo struct {
	Branch        string `json:"branch,omitempty"`
	DefaultBranch string `json:"default_branch,omitempty"`
}

// VCSFileStatus is the /vcs/status file row contract.
type VCSFileStatus struct {
	File      string `json:"file"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Status    string `json:"status"`
}

// VCSFileDiff is the /vcs/diff file row contract.
type VCSFileDiff struct {
	File      string `json:"file"`
	Patch     string `json:"patch,omitempty"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Status    string `json:"status,omitempty"`
}

// VCSApplyResult is returned after applying a patch.
type VCSApplyResult struct {
	Applied bool `json:"applied"`
}

// ProjectInfo is the local project metadata contract used by project endpoints.
type ProjectInfo struct {
	ID        string            `json:"id"`
	Worktree  string            `json:"worktree"`
	VCS       string            `json:"vcs,omitempty"`
	Name      string            `json:"name,omitempty"`
	Icon      *ProjectIcon      `json:"icon,omitempty"`
	Commands  map[string]string `json:"commands,omitempty"`
	Time      ProjectTime       `json:"time"`
	Sandboxes []string          `json:"sandboxes"`
}

// ProjectIcon mirrors optional project icon metadata.
type ProjectIcon struct {
	URL      string `json:"url,omitempty"`
	Override string `json:"override,omitempty"`
	Color    string `json:"color,omitempty"`
}

// ProjectTime mirrors project timestamps in milliseconds.
type ProjectTime struct {
	Created     int64 `json:"created"`
	Updated     int64 `json:"updated"`
	Initialized int64 `json:"initialized,omitempty"`
}

// AgentInfo is the public /agent item contract for local agents.
type AgentInfo struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Mode        string         `json:"mode"`
	Native      bool           `json:"native,omitempty"`
	Hidden      bool           `json:"hidden,omitempty"`
	TopP        float64        `json:"topP,omitempty"`
	Temperature float64        `json:"temperature,omitempty"`
	Color       string         `json:"color,omitempty"`
	Permission  map[string]any `json:"permission"`
	Model       *ModelRef      `json:"model,omitempty"`
	Variant     string         `json:"variant,omitempty"`
	Prompt      string         `json:"prompt,omitempty"`
	Options     map[string]any `json:"options"`
	Steps       int            `json:"steps,omitempty"`
}

// ModelRef identifies a configured model.
type ModelRef struct {
	ProviderID string `json:"providerID"`
	ModelID    string `json:"modelID"`
}

// SkillInfo is the public /skill item contract.
type SkillInfo struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Location    string `json:"location"`
	Content     string `json:"content"`
}

// CommandListInfo is the /command list item contract used by instance routes.
type CommandListInfo struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Agent       string   `json:"agent,omitempty"`
	Model       string   `json:"model,omitempty"`
	Source      string   `json:"source,omitempty"`
	Template    string   `json:"template"`
	Subtask     *bool    `json:"subtask,omitempty"`
	Hints       []string `json:"hints"`
}

// FormatterStatus reports whether a formatter is enabled for this workspace.
type FormatterStatus struct {
	Name       string   `json:"name"`
	Extensions []string `json:"extensions"`
	Enabled    bool     `json:"enabled"`
}

// LSPStatus reports a language server status.
type LSPStatus struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Root   string `json:"root"`
	Status string `json:"status"`
}

// InstancePathInfo returns local path metadata for a directory.
func InstancePathInfo(directory string) (PathInfo, error) {
	resolved, err := cleanProjectRoot(directory)
	if err != nil {
		return PathInfo{}, err
	}
	worktree := gitWorktree(context.Background(), resolved)
	if worktree == "" {
		worktree = resolved
	}
	home, _ := os.UserHomeDir()
	return PathInfo{
		Home:      home,
		State:     globalStateDir(home),
		Config:    config.GlobalConfigDir(),
		Worktree:  worktree,
		Directory: resolved,
	}, nil
}

// CurrentProject returns project metadata for a local directory.
func CurrentProject(ctx context.Context, directory string) (ProjectInfo, error) {
	resolved, err := cleanProjectRoot(directory)
	if err != nil {
		return ProjectInfo{}, err
	}
	worktree := gitWorktree(ctx, resolved)
	if worktree == "" {
		worktree = resolved
	}
	id := globalProjectID
	vcs := ""
	if isGitRepository(ctx, resolved) {
		id = projectID(ctx, resolved)
		vcs = "git"
	}
	now := time.Now().UnixMilli()
	return ProjectInfo{
		ID:        id,
		Worktree:  worktree,
		VCS:       vcs,
		Time:      ProjectTime{Created: now, Updated: now},
		Sandboxes: []string{},
	}, nil
}

// VCS returns branch metadata for a local git directory.
func VCS(ctx context.Context, directory string) (VCSInfo, error) {
	root, err := cleanProjectRoot(directory)
	if err != nil {
		return VCSInfo{}, err
	}
	if !isGitRepository(ctx, root) {
		return VCSInfo{}, nil
	}
	return VCSInfo{
		Branch:        strings.TrimSpace(gitText(ctx, root, "branch", "--show-current")),
		DefaultBranch: defaultBranch(ctx, root),
	}, nil
}

// VCSStatus returns status rows for a local git directory.
func VCSStatus(ctx context.Context, directory string) ([]VCSFileStatus, error) {
	root, err := cleanProjectRoot(directory)
	if err != nil {
		return nil, err
	}
	if !isGitRepository(ctx, root) {
		return []VCSFileStatus{}, nil
	}
	statusItems := gitStatusItems(ctx, root)
	stats := gitStats(ctx, root, "HEAD")
	result := make([]VCSFileStatus, 0, len(statusItems))
	for _, item := range statusItems {
		stat := stats[item.file]
		if item.status == "added" && stat.additions == 0 && stat.deletions == 0 {
			stat = statUntracked(root, item.file)
		}
		result = append(result, VCSFileStatus{
			File:      item.file,
			Additions: stat.additions,
			Deletions: stat.deletions,
			Status:    item.status,
		})
	}
	return result, nil
}

// VCSDiff returns diff rows for the requested mode.
func VCSDiff(ctx context.Context, directory string, mode string) ([]VCSFileDiff, error) {
	root, err := cleanProjectRoot(directory)
	if err != nil {
		return nil, err
	}
	if !isGitRepository(ctx, root) {
		return []VCSFileDiff{}, nil
	}
	ref := "HEAD"
	if mode == "branch" {
		base := mergeBaseDefault(ctx, root)
		if base == "" {
			return []VCSFileDiff{}, nil
		}
		ref = base
	}
	items := gitStatusItems(ctx, root)
	stats := gitStats(ctx, root, ref)
	result := make([]VCSFileDiff, 0, len(items))
	for _, item := range items {
		stat := stats[item.file]
		patch := ""
		if item.code == "??" {
			stat = statUntracked(root, item.file)
			patch = gitText(ctx, root, "--no-pager", "diff", "--no-index", "--", "/dev/null", item.file)
		} else {
			patch = gitText(ctx, root, "--no-pager", "diff", ref, "--", item.file)
			if strings.TrimSpace(patch) == "" {
				patch = gitText(ctx, root, "--no-pager", "diff", "--staged", ref, "--", item.file)
			}
		}
		result = append(result, VCSFileDiff{
			File:      item.file,
			Patch:     patch,
			Additions: stat.additions,
			Deletions: stat.deletions,
			Status:    item.status,
		})
	}
	return result, nil
}

// VCSDiffRaw returns the raw working tree patch.
func VCSDiffRaw(ctx context.Context, directory string) (string, error) {
	root, err := cleanProjectRoot(directory)
	if err != nil {
		return "", err
	}
	if !isGitRepository(ctx, root) {
		return "", nil
	}
	tracked := gitText(ctx, root, "--no-pager", "diff", "HEAD")
	parts := []string{}
	if strings.TrimSpace(tracked) != "" {
		parts = append(parts, tracked)
	}
	for _, item := range gitStatusItems(ctx, root) {
		if item.code != "??" {
			continue
		}
		patch := gitText(ctx, root, "--no-pager", "diff", "--no-index", "--", "/dev/null", item.file)
		if strings.TrimSpace(patch) != "" {
			parts = append(parts, patch)
		}
	}
	return strings.Join(parts, "\n"), nil
}

// VCSApply applies a patch to a local git directory.
func VCSApply(ctx context.Context, directory string, patch string) (VCSApplyResult, error) {
	root, err := cleanProjectRoot(directory)
	if err != nil {
		return VCSApplyResult{}, err
	}
	if !isGitRepository(ctx, root) {
		return VCSApplyResult{}, fmt.Errorf("patch can't be applied because the project is not git-based")
	}
	cmd := exec.CommandContext(ctx, "git", "apply", "--whitespace=nowarn", "-")
	cmd.Dir = root
	cmd.Stdin = strings.NewReader(patch)
	output, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = "patch can't be applied"
		}
		return VCSApplyResult{}, fmt.Errorf("%s", message)
	}
	return VCSApplyResult{Applied: true}, nil
}

// ListAgents returns built-in and local configured agents.
func ListAgents(directory string) ([]AgentInfo, error) {
	cfg, err := config.Load(config.LoadOptions{Directory: directory})
	if err != nil {
		return nil, err
	}
	agents := map[string]AgentInfo{}
	for _, agent := range builtInAgents() {
		agents[agent.Name] = agent
	}
	if configured, ok := cfg.Info["agent"].(map[string]any); ok {
		for name, raw := range configured {
			info := agentFromConfig(name, raw)
			if info.Name != "" {
				agents[info.Name] = mergeAgent(agents[info.Name], info)
			}
		}
	}
	for _, path := range cfg.Discovery.Agents {
		info, err := agentFromMarkdown(path)
		if err != nil {
			return nil, err
		}
		if info.Name != "" {
			agents[info.Name] = mergeAgent(agents[info.Name], info)
		}
	}
	result := make([]AgentInfo, 0, len(agents))
	for _, agent := range agents {
		if agent.Permission == nil {
			agent.Permission = map[string]any{}
		}
		if agent.Options == nil {
			agent.Options = map[string]any{}
		}
		result = append(result, agent)
	}
	slices.SortFunc(result, func(a AgentInfo, b AgentInfo) int { return strings.Compare(a.Name, b.Name) })
	return result, nil
}

// ListSkills returns project-local skills discovered under .opencode.
func ListSkills(directory string) ([]SkillInfo, error) {
	cfg, err := config.Load(config.LoadOptions{Directory: directory})
	if err != nil {
		return nil, err
	}
	result := []SkillInfo{}
	for _, path := range cfg.Discovery.Skills {
		skill, err := parseSkillFile(path)
		if err != nil {
			return nil, err
		}
		if skill.Name != "" {
			result = append(result, skill)
		}
	}
	slices.SortFunc(result, func(a SkillInfo, b SkillInfo) int { return strings.Compare(a.Name, b.Name) })
	return result, nil
}

// ListCommands returns command list items for instance routes.
func ListCommands(directory string) ([]CommandListInfo, error) {
	commands, err := config.LoadCommands(directory)
	if err != nil {
		return nil, err
	}
	result := make([]CommandListInfo, 0, len(commands)+2)
	result = append(result, defaultCommand("init"), defaultCommand("review"))
	for _, command := range commands {
		result = append(result, CommandListInfo{
			Name:        command.Name,
			Description: command.Description,
			Agent:       command.Agent,
			Model:       command.Model,
			Source:      "command",
			Template:    command.Template,
			Subtask:     command.Subtask,
			Hints:       commandHints(command.Template),
		})
	}
	slices.SortFunc(result, func(a CommandListInfo, b CommandListInfo) int { return strings.Compare(a.Name, b.Name) })
	return result, nil
}

// FormatterStatuses returns the configured formatter list.
func FormatterStatuses(directory string) ([]FormatterStatus, error) {
	cfg, err := config.Load(config.LoadOptions{Directory: directory})
	if err != nil {
		return nil, err
	}
	formatter, exists := cfg.Info["formatter"]
	if !exists || formatter == false {
		return []FormatterStatus{}, nil
	}
	statuses := builtinFormatterStatuses()
	if configured, ok := formatter.(map[string]any); ok {
		for name, raw := range configured {
			entry, _ := raw.(map[string]any)
			disabled, _ := entry["disabled"].(bool)
			if disabled {
				delete(statuses, name)
				if name == "ruff" || name == "uv" {
					delete(statuses, "ruff")
					delete(statuses, "uv")
				}
				continue
			}
			status := statuses[name]
			status.Name = name
			if extensions, ok := stringList(entry["extensions"]); ok {
				status.Extensions = extensions
			}
			if len(status.Extensions) == 0 {
				status.Extensions = []string{}
			}
			status.Enabled = commandAvailable(entry["command"])
			statuses[name] = status
		}
	}
	result := make([]FormatterStatus, 0, len(statuses))
	for _, status := range statuses {
		status.Enabled = status.Enabled && formatterCommandExists(status.Name, directory)
		result = append(result, status)
	}
	slices.SortFunc(result, func(a FormatterStatus, b FormatterStatus) int { return strings.Compare(a.Name, b.Name) })
	return result, nil
}

// LSPStatuses returns configured local language server state.
func LSPStatuses(directory string) ([]LSPStatus, error) {
	cfg, err := config.Load(config.LoadOptions{Directory: directory})
	if err != nil {
		return nil, err
	}
	lsp, ok := cfg.Info["lsp"]
	if !ok || lsp == false {
		return []LSPStatus{}, nil
	}
	root, err := cleanProjectRoot(directory)
	if err != nil {
		return nil, err
	}
	statuses := map[string]LSPStatus{}
	if lsp == true {
		for _, status := range builtinLSPStatuses(root) {
			statuses[status.ID] = status
		}
	}
	if configured, ok := lsp.(map[string]any); ok {
		for id, raw := range configured {
			entry, _ := raw.(map[string]any)
			if disabled, _ := entry["disabled"].(bool); disabled {
				delete(statuses, id)
				continue
			}
			statuses[id] = LSPStatus{ID: id, Name: id, Root: root, Status: "connected"}
		}
	}
	result := make([]LSPStatus, 0, len(statuses))
	for _, status := range statuses {
		result = append(result, status)
	}
	slices.SortFunc(result, func(a LSPStatus, b LSPStatus) int { return strings.Compare(a.ID, b.ID) })
	return result, nil
}

func globalStateDir(home string) string {
	if state := os.Getenv("XDG_STATE_HOME"); state != "" {
		return filepath.Join(state, "opencode")
	}
	if home != "" {
		return filepath.Join(home, ".local", "state", "opencode")
	}
	return filepath.Join(".local", "state", "opencode")
}

func gitWorktree(ctx context.Context, directory string) string {
	output := gitText(ctx, directory, "rev-parse", "--show-toplevel")
	return strings.TrimSpace(output)
}

func projectID(ctx context.Context, directory string) string {
	root := gitWorktree(ctx, directory)
	if root == "" {
		return globalProjectID
	}
	output := gitText(ctx, root, "rev-list", "--max-parents=0", "HEAD")
	roots := nonEmptyLines(output)
	if len(roots) == 0 {
		return globalProjectID
	}
	slices.Sort(roots)
	return roots[0]
}

func defaultBranch(ctx context.Context, directory string) string {
	for _, ref := range []string{"origin/HEAD", "refs/remotes/origin/HEAD"} {
		output := gitText(ctx, directory, "symbolic-ref", "--quiet", "--short", ref)
		output = strings.TrimSpace(output)
		if output != "" {
			return strings.TrimPrefix(output, "origin/")
		}
	}
	for _, branch := range []string{"main", "master"} {
		if gitRefExists(ctx, directory, branch) || gitRefExists(ctx, directory, "origin/"+branch) {
			return branch
		}
	}
	return ""
}

func gitRefExists(ctx context.Context, directory string, ref string) bool {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--verify", "--quiet", ref)
	cmd.Dir = directory
	return cmd.Run() == nil
}

func mergeBaseDefault(ctx context.Context, directory string) string {
	info, err := VCS(ctx, directory)
	if err != nil || info.DefaultBranch == "" || info.Branch == "" || info.Branch == info.DefaultBranch {
		return ""
	}
	ref := info.DefaultBranch
	if gitRefExists(ctx, directory, "origin/"+ref) {
		ref = "origin/" + ref
	}
	return strings.TrimSpace(gitText(ctx, directory, "merge-base", ref, "HEAD"))
}

type gitStatusItem struct {
	file   string
	code   string
	status string
}

type gitStat struct {
	additions int
	deletions int
}

func gitStatusItems(ctx context.Context, directory string) []gitStatusItem {
	output := gitText(ctx, directory, "-c", "core.quotepath=false", "status", "--porcelain")
	result := []gitStatusItem{}
	for _, line := range strings.Split(strings.TrimRight(output, "\r\n"), "\n") {
		if len(line) < 4 {
			continue
		}
		code := strings.TrimSpace(line[:2])
		file := strings.TrimSpace(line[3:])
		if before, after, ok := strings.Cut(file, " -> "); ok {
			_ = before
			file = after
		}
		result = append(result, gitStatusItem{file: filepath.ToSlash(file), code: code, status: statusFromGitCode(code)})
	}
	slices.SortFunc(result, func(a gitStatusItem, b gitStatusItem) int { return strings.Compare(a.file, b.file) })
	return result
}

func gitStats(ctx context.Context, directory string, ref string) map[string]gitStat {
	result := map[string]gitStat{}
	if ref == "" {
		return result
	}
	output := gitText(ctx, directory, "-c", "core.quotepath=false", "diff", "--numstat", ref)
	for _, line := range nonEmptyLines(output) {
		parts := strings.Split(line, "\t")
		if len(parts) < 3 {
			continue
		}
		result[filepath.ToSlash(parts[2])] = gitStat{additions: parseNumstat(parts[0]), deletions: parseNumstat(parts[1])}
	}
	return result
}

func statUntracked(root string, file string) gitStat {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
	if err != nil {
		return gitStat{}
	}
	return gitStat{additions: len(splitLines(string(data)))}
}

func statusFromGitCode(code string) string {
	if strings.Contains(code, "D") {
		return "deleted"
	}
	if code == "??" || strings.Contains(code, "A") {
		return "added"
	}
	return "modified"
}

func builtInAgents() []AgentInfo {
	defaultPermission := map[string]any{"*": "allow"}
	return []AgentInfo{
		{Name: "build", Description: "The default agent. Executes tools based on configured permissions.", Mode: "primary", Native: true, Permission: cloneMap(defaultPermission), Options: map[string]any{}},
		{Name: "plan", Description: "Plan mode. Disallows all edit tools.", Mode: "primary", Native: true, Permission: cloneMap(defaultPermission), Options: map[string]any{}},
		{Name: "general", Description: "General-purpose agent for researching complex questions and executing multi-step tasks. Use this agent to execute multiple units of work in parallel.", Mode: "subagent", Native: true, Permission: cloneMap(defaultPermission), Options: map[string]any{}},
		{Name: "explore", Description: "Fast agent specialized for exploring codebases.", Mode: "subagent", Native: true, Permission: cloneMap(defaultPermission), Options: map[string]any{}},
	}
}

func agentFromConfig(name string, raw any) AgentInfo {
	entry, _ := raw.(map[string]any)
	agent := AgentInfo{Name: name, Mode: "subagent", Permission: map[string]any{}, Options: map[string]any{}}
	if value, ok := entry["name"].(string); ok && value != "" {
		agent.Name = value
	}
	if value, ok := entry["description"].(string); ok {
		agent.Description = value
	}
	if value, ok := entry["mode"].(string); ok && value != "" {
		agent.Mode = value
	}
	if value, ok := entry["hidden"].(bool); ok {
		agent.Hidden = value
	}
	if value, ok := numberValue(entry["top_p"]); ok {
		agent.TopP = value
	}
	if value, ok := numberValue(entry["temperature"]); ok {
		agent.Temperature = value
	}
	if value, ok := entry["color"].(string); ok {
		agent.Color = value
	}
	if value, ok := entry["variant"].(string); ok {
		agent.Variant = value
	}
	if value, ok := entry["prompt"].(string); ok {
		agent.Prompt = value
	}
	if value, ok := intValue(entry["steps"]); ok {
		agent.Steps = value
	}
	if value, ok := intValue(entry["maxSteps"]); ok && agent.Steps == 0 {
		agent.Steps = value
	}
	if permission, ok := entry["permission"].(map[string]any); ok {
		agent.Permission = permission
	}
	if options, ok := entry["options"].(map[string]any); ok {
		agent.Options = options
	}
	if model, ok := entry["model"].(string); ok {
		provider, modelID, found := strings.Cut(model, "/")
		if found {
			agent.Model = &ModelRef{ProviderID: provider, ModelID: modelID}
		}
	}
	return agent
}

func mergeAgent(base AgentInfo, override AgentInfo) AgentInfo {
	if base.Name == "" {
		return override
	}
	if override.Description != "" {
		base.Description = override.Description
	}
	if override.Mode != "" {
		base.Mode = override.Mode
	}
	if override.Hidden {
		base.Hidden = true
	}
	if override.TopP != 0 {
		base.TopP = override.TopP
	}
	if override.Temperature != 0 {
		base.Temperature = override.Temperature
	}
	if override.Color != "" {
		base.Color = override.Color
	}
	if override.Model != nil {
		base.Model = override.Model
	}
	if override.Variant != "" {
		base.Variant = override.Variant
	}
	if override.Prompt != "" {
		base.Prompt = override.Prompt
	}
	if len(override.Permission) > 0 {
		base.Permission = override.Permission
	}
	if len(override.Options) > 0 {
		base.Options = override.Options
	}
	if override.Steps != 0 {
		base.Steps = override.Steps
	}
	return base
}

func agentFromMarkdown(path string) (AgentInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return AgentInfo{}, fmt.Errorf("read agent %s: %w", path, err)
	}
	meta, content, err := parseFrontmatterLocal(string(data))
	if err != nil {
		return AgentInfo{}, fmt.Errorf("parse agent %s: %w", path, err)
	}
	raw := map[string]any{"prompt": strings.TrimSpace(content)}
	for key, value := range meta {
		raw[key] = value
	}
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if explicit, ok := raw["name"].(string); ok && explicit != "" {
		name = explicit
	}
	return agentFromConfig(name, raw), nil
}

func parseSkillFile(path string) (SkillInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return SkillInfo{}, fmt.Errorf("read skill %s: %w", path, err)
	}
	meta, content, err := parseFrontmatterLocal(string(data))
	if err != nil {
		return SkillInfo{}, fmt.Errorf("parse skill %s: %w", path, err)
	}
	name := meta["name"]
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	return SkillInfo{Name: name, Description: meta["description"], Location: path, Content: strings.TrimSpace(content)}, nil
}

func parseFrontmatterLocal(input string) (map[string]string, string, error) {
	if !strings.HasPrefix(input, "---\n") && !strings.HasPrefix(input, "---\r\n") {
		return map[string]string{}, input, nil
	}
	newline := "\n"
	prefixLen := len("---\n")
	if strings.HasPrefix(input, "---\r\n") {
		newline = "\r\n"
		prefixLen = len("---\r\n")
	}
	rest := input[prefixLen:]
	endMarker := newline + "---"
	end := strings.Index(rest, endMarker)
	if end == -1 {
		return nil, "", fmt.Errorf("unterminated YAML frontmatter")
	}
	raw := rest[:end]
	content := strings.TrimPrefix(rest[end+len(endMarker):], newline)
	meta := map[string]string{}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return nil, "", fmt.Errorf("invalid frontmatter line %q", line)
		}
		meta[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), `"'`)
	}
	return meta, content, nil
}

func defaultCommand(name string) CommandListInfo {
	template := "Initialize opencode configuration for ${path}."
	description := "guided AGENTS.md setup"
	subtask := false
	if name == "review" {
		template = "Review changes in ${path}."
		description = "review changes [commit|branch|pr], defaults to uncommitted"
		subtask = true
	}
	return CommandListInfo{
		Name:        name,
		Description: description,
		Source:      "command",
		Template:    template,
		Subtask:     &subtask,
		Hints:       commandHints(template),
	}
}

func commandHints(template string) []string {
	result := []string{}
	for i := 1; i <= 9; i++ {
		marker := fmt.Sprintf("$%d", i)
		if strings.Contains(template, marker) {
			result = append(result, marker)
		}
	}
	if strings.Contains(template, "$ARGUMENTS") {
		result = append(result, "$ARGUMENTS")
	}
	return result
}

func builtinFormatterStatuses() map[string]FormatterStatus {
	return map[string]FormatterStatus{
		"gofmt":          {Name: "gofmt", Extensions: []string{".go"}, Enabled: true},
		"mix":            {Name: "mix", Extensions: []string{".ex", ".exs", ".eex", ".heex", ".leex", ".neex", ".sface"}, Enabled: true},
		"prettier":       {Name: "prettier", Extensions: []string{".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx", ".mts", ".cts", ".html", ".htm", ".css", ".scss", ".sass", ".less", ".vue", ".svelte", ".json", ".jsonc", ".yaml", ".yml", ".toml", ".xml", ".md", ".mdx", ".graphql", ".gql"}, Enabled: true},
		"biome":          {Name: "biome", Extensions: []string{".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx", ".mts", ".cts", ".html", ".htm", ".css", ".scss", ".sass", ".less", ".vue", ".svelte", ".json", ".jsonc", ".yaml", ".yml", ".toml", ".xml", ".md", ".mdx", ".graphql", ".gql"}, Enabled: true},
		"zig":            {Name: "zig", Extensions: []string{".zig", ".zon"}, Enabled: true},
		"clang-format":   {Name: "clang-format", Extensions: []string{".c", ".cc", ".cpp", ".cxx", ".c++", ".h", ".hh", ".hpp", ".hxx", ".h++", ".ino", ".C", ".H"}, Enabled: true},
		"ktlint":         {Name: "ktlint", Extensions: []string{".kt", ".kts"}, Enabled: true},
		"ruff":           {Name: "ruff", Extensions: []string{".py", ".pyi"}, Enabled: true},
		"uv":             {Name: "uv", Extensions: []string{".py", ".pyi"}, Enabled: true},
		"rubocop":        {Name: "rubocop", Extensions: []string{".rb", ".rake", ".gemspec", ".ru"}, Enabled: true},
		"standardrb":     {Name: "standardrb", Extensions: []string{".rb", ".rake", ".gemspec", ".ru"}, Enabled: true},
		"htmlbeautifier": {Name: "htmlbeautifier", Extensions: []string{".erb", ".html.erb"}, Enabled: true},
		"dart":           {Name: "dart", Extensions: []string{".dart"}, Enabled: true},
		"ocamlformat":    {Name: "ocamlformat", Extensions: []string{".ml", ".mli"}, Enabled: true},
		"terraform":      {Name: "terraform", Extensions: []string{".tf", ".tfvars"}, Enabled: true},
		"latexindent":    {Name: "latexindent", Extensions: []string{".tex"}, Enabled: true},
		"gleam":          {Name: "gleam", Extensions: []string{".gleam"}, Enabled: true},
		"shfmt":          {Name: "shfmt", Extensions: []string{".sh", ".bash"}, Enabled: true},
		"nixfmt":         {Name: "nixfmt", Extensions: []string{".nix"}, Enabled: true},
		"rustfmt":        {Name: "rustfmt", Extensions: []string{".rs"}, Enabled: true},
		"pint":           {Name: "pint", Extensions: []string{".php"}, Enabled: true},
		"ormolu":         {Name: "ormolu", Extensions: []string{".hs"}, Enabled: true},
		"cljfmt":         {Name: "cljfmt", Extensions: []string{".clj", ".cljs", ".cljc", ".edn"}, Enabled: true},
		"dfmt":           {Name: "dfmt", Extensions: []string{".d"}, Enabled: true},
	}
}

func formatterCommandExists(name string, directory string) bool {
	switch name {
	case "prettier":
		return hasPackageDependency(directory, "prettier")
	case "biome":
		return regularFileLocal(filepath.Join(directory, "biome.json")) || regularFileLocal(filepath.Join(directory, "biome.jsonc"))
	case "ruff":
		return regularFileLocal(filepath.Join(directory, "ruff.toml")) || fileContains(filepath.Join(directory, "pyproject.toml"), "[tool.ruff]") || fileContains(filepath.Join(directory, "requirements.txt"), "ruff")
	case "uv":
		return commandExists("uv")
	case "clang-format":
		return regularFileLocal(filepath.Join(directory, ".clang-format"))
	case "ocamlformat":
		return regularFileLocal(filepath.Join(directory, ".ocamlformat"))
	case "pint":
		return fileContains(filepath.Join(directory, "composer.json"), "laravel/pint")
	default:
		return commandExists(name)
	}
}

func commandAvailable(raw any) bool {
	values, ok := stringList(raw)
	if !ok || len(values) == 0 {
		return false
	}
	if filepath.IsAbs(values[0]) {
		return regularFileLocal(values[0])
	}
	return commandExists(values[0])
}

func builtinLSPStatuses(root string) []LSPStatus {
	result := []LSPStatus{}
	for _, item := range []struct {
		id      string
		command string
	}{
		{id: "gopls", command: "gopls"},
		{id: "typescript-language-server", command: "typescript-language-server"},
		{id: "pyright", command: "pyright-langserver"},
		{id: "rust-analyzer", command: "rust-analyzer"},
	} {
		if commandExists(item.command) {
			result = append(result, LSPStatus{ID: item.id, Name: item.id, Root: root, Status: "connected"})
		}
	}
	return result
}

func stringList(raw any) ([]string, bool) {
	items, ok := raw.([]any)
	if !ok {
		return nil, false
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		text, ok := item.(string)
		if !ok {
			return nil, false
		}
		result = append(result, text)
	}
	return result, true
}

func numberValue(raw any) (float64, bool) {
	switch value := raw.(type) {
	case float64:
		return value, true
	case int:
		return float64(value), true
	case json.Number:
		parsed, err := value.Float64()
		return parsed, err == nil
	default:
		return 0, false
	}
}

func intValue(raw any) (int, bool) {
	switch value := raw.(type) {
	case int:
		return value, true
	case int64:
		return int(value), true
	case float64:
		return int(value), true
	case json.Number:
		parsed, err := value.Int64()
		return int(parsed), err == nil
	default:
		return 0, false
	}
}

func cloneMap(input map[string]any) map[string]any {
	result := map[string]any{}
	for key, value := range input {
		result[key] = value
	}
	return result
}

func commandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func regularFileLocal(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func hasPackageDependency(directory string, name string) bool {
	data, err := os.ReadFile(filepath.Join(directory, "package.json"))
	if err != nil {
		return false
	}
	var parsed struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return false
	}
	return parsed.Dependencies[name] != "" || parsed.DevDependencies[name] != ""
}

func fileContains(path string, needle string) bool {
	data, err := os.ReadFile(path)
	return err == nil && bytes.Contains(data, []byte(needle))
}
