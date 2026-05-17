package integration

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// ProjectUpdateInput is the mutable subset accepted by PATCH /project/{id}.
type ProjectUpdateInput struct {
	Name     *string           `json:"name,omitempty"`
	Icon     *ProjectIcon      `json:"icon,omitempty"`
	Commands map[string]string `json:"commands,omitempty"`
}

// WorkspaceAdapterEntry is the public workspace adapter descriptor.
type WorkspaceAdapterEntry struct {
	Type        string `json:"type"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// WorkspaceInfo mirrors the experimental workspace DTO from the TypeScript API.
type WorkspaceInfo struct {
	ID        string  `json:"id"`
	Type      string  `json:"type"`
	Name      string  `json:"name"`
	Branch    *string `json:"branch,omitempty"`
	Directory *string `json:"directory,omitempty"`
	Extra     any     `json:"extra,omitempty"`
	ProjectID string  `json:"projectID"`
	TimeUsed  int64   `json:"timeUsed"`
}

// WorkspaceStatus reports connection state for one workspace.
type WorkspaceStatus struct {
	WorkspaceID string `json:"workspaceID"`
	Status      string `json:"status"`
}

// WorkspaceCreateInput is the POST /experimental/workspace payload.
type WorkspaceCreateInput struct {
	ID     string  `json:"id,omitempty"`
	Type   string  `json:"type"`
	Branch *string `json:"branch,omitempty"`
	Extra  any     `json:"extra,omitempty"`
}

// WorkspaceWarpInput is the POST /experimental/workspace/warp payload.
type WorkspaceWarpInput struct {
	ID          *string `json:"id"`
	SessionID   string  `json:"sessionID"`
	CopyChanges bool    `json:"copyChanges,omitempty"`
}

// WorktreeInfo is the experimental worktree DTO.
type WorktreeInfo struct {
	Name      string `json:"name"`
	Branch    string `json:"branch,omitempty"`
	Directory string `json:"directory"`
}

// WorktreeCreateInput is the POST /experimental/worktree payload.
type WorktreeCreateInput struct {
	Name         string `json:"name,omitempty"`
	StartCommand string `json:"startCommand,omitempty"`
}

// WorktreeDirectoryInput is used by worktree remove and reset endpoints.
type WorktreeDirectoryInput struct {
	Directory string `json:"directory"`
}

// WorkspaceStore stores local experimental workspaces for the Go sidecar.
type WorkspaceStore struct {
	mu         sync.RWMutex
	projects   map[string]ProjectInfo
	workspaces map[string]WorkspaceInfo
	order      []string
}

// NewWorkspaceStore creates an empty local workspace store.
func NewWorkspaceStore() *WorkspaceStore {
	return &WorkspaceStore{
		projects:   map[string]ProjectInfo{},
		workspaces: map[string]WorkspaceInfo{},
		order:      []string{},
	}
}

// ListProjects returns known projects plus the current local project.
func (store *WorkspaceStore) ListProjects(ctx context.Context, directory string) ([]ProjectInfo, error) {
	current, err := CurrentProject(ctx, directory)
	if err != nil {
		return nil, err
	}
	store.upsertProject(current)

	store.mu.RLock()
	defer store.mu.RUnlock()
	result := make([]ProjectInfo, 0, len(store.projects))
	for _, project := range store.projects {
		result = append(result, project)
	}
	slices.SortFunc(result, func(a ProjectInfo, b ProjectInfo) int {
		if a.Time.Updated != b.Time.Updated {
			if a.Time.Updated > b.Time.Updated {
				return -1
			}
			return 1
		}
		return strings.Compare(a.ID, b.ID)
	})
	return result, nil
}

// UpdateProject applies local metadata changes to a known project.
func (store *WorkspaceStore) UpdateProject(ctx context.Context, directory string, projectID string, input ProjectUpdateInput) (ProjectInfo, error) {
	current, err := CurrentProject(ctx, directory)
	if err != nil {
		return ProjectInfo{}, err
	}
	store.upsertProject(current)

	store.mu.Lock()
	defer store.mu.Unlock()
	project, ok := store.projects[projectID]
	if !ok {
		return ProjectInfo{}, fmt.Errorf("project not found: %s", projectID)
	}
	if input.Name != nil {
		project.Name = *input.Name
	}
	if input.Icon != nil {
		icon := *input.Icon
		project.Icon = &icon
	}
	if input.Commands != nil {
		project.Commands = cloneStringMap(input.Commands)
	}
	project.Time.Updated = time.Now().UnixMilli()
	store.projects[projectID] = project
	return project, nil
}

// InitGit initializes git for the request directory and returns refreshed project metadata.
func (store *WorkspaceStore) InitGit(ctx context.Context, directory string) (ProjectInfo, error) {
	root, err := cleanProjectRoot(directory)
	if err != nil {
		return ProjectInfo{}, err
	}
	if !isGitRepository(ctx, root) {
		cmd := exec.CommandContext(ctx, "git", "init", "--quiet")
		cmd.Dir = root
		output, err := cmd.CombinedOutput()
		if err != nil {
			message := strings.TrimSpace(string(output))
			if message == "" {
				message = "failed to initialize git repository"
			}
			return ProjectInfo{}, fmt.Errorf("%s: %w", message, err)
		}
	}
	project, err := CurrentProject(ctx, root)
	if err != nil {
		return ProjectInfo{}, err
	}
	project.Time.Initialized = time.Now().UnixMilli()
	store.upsertProject(project)
	return project, nil
}

// WorkspaceAdapters returns the built-in local workspace adapters.
func (store *WorkspaceStore) WorkspaceAdapters(ctx context.Context, directory string) ([]WorkspaceAdapterEntry, error) {
	project, err := CurrentProject(ctx, directory)
	if err != nil {
		return nil, err
	}
	store.upsertProject(project)
	return []WorkspaceAdapterEntry{{
		Type:        "worktree",
		Name:        "Worktree",
		Description: "Create a git worktree",
	}}, nil
}

// ListWorkspaces returns workspaces registered for the current project.
func (store *WorkspaceStore) ListWorkspaces(ctx context.Context, directory string) ([]WorkspaceInfo, error) {
	project, err := CurrentProject(ctx, directory)
	if err != nil {
		return nil, err
	}
	store.upsertProject(project)
	if err := store.syncGitWorktrees(ctx, project); err != nil {
		return nil, err
	}
	return store.listWorkspaces(project.ID), nil
}

// SyncWorkspaces refreshes locally discoverable git worktrees.
func (store *WorkspaceStore) SyncWorkspaces(ctx context.Context, directory string) error {
	project, err := CurrentProject(ctx, directory)
	if err != nil {
		return err
	}
	store.upsertProject(project)
	return store.syncGitWorktrees(ctx, project)
}

// WorkspaceStatuses returns connection status for known local workspaces.
func (store *WorkspaceStore) WorkspaceStatuses(ctx context.Context, directory string) ([]WorkspaceStatus, error) {
	workspaces, err := store.ListWorkspaces(ctx, directory)
	if err != nil {
		return nil, err
	}
	result := make([]WorkspaceStatus, 0, len(workspaces))
	for _, workspace := range workspaces {
		result = append(result, WorkspaceStatus{WorkspaceID: workspace.ID, Status: "connected"})
	}
	return result, nil
}

// CreateWorkspace creates and registers a local worktree workspace.
func (store *WorkspaceStore) CreateWorkspace(ctx context.Context, directory string, input WorkspaceCreateInput) (WorkspaceInfo, error) {
	if input.Type == "" {
		input.Type = "worktree"
	}
	if input.Type != "worktree" {
		return WorkspaceInfo{}, fmt.Errorf("unknown workspace adapter: %s", input.Type)
	}
	project, err := CurrentProject(ctx, directory)
	if err != nil {
		return WorkspaceInfo{}, err
	}
	if project.VCS != "git" {
		return WorkspaceInfo{}, fmt.Errorf("worktrees are only supported for git projects")
	}
	store.upsertProject(project)

	name := slugifyWorkspaceName("workspace")
	id := input.ID
	if id == "" {
		id = newWorkspaceID()
	}
	root := filepath.Join(globalStateDir(userHomeDir()), "worktree", project.ID)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return WorkspaceInfo{}, fmt.Errorf("create worktree root: %w", err)
	}
	target := filepath.Join(root, name)
	for suffix := 2; pathExists(target); suffix++ {
		target = filepath.Join(root, fmt.Sprintf("%s-%d", name, suffix))
	}

	args := []string{"worktree", "add", "--no-checkout", "--detach", target, "HEAD"}
	if input.Branch != nil && strings.TrimSpace(*input.Branch) != "" {
		branch := strings.TrimSpace(*input.Branch)
		args = []string{"worktree", "add", "--no-checkout", "-b", branch, target, "HEAD"}
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = project.Worktree
	output, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = "failed to create git worktree"
		}
		return WorkspaceInfo{}, fmt.Errorf("%s: %w", message, err)
	}
	reset := exec.CommandContext(ctx, "git", "reset", "--hard")
	reset.Dir = target
	if output, err := reset.CombinedOutput(); err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = "failed to populate git worktree"
		}
		return WorkspaceInfo{}, fmt.Errorf("%s: %w", message, err)
	}
	if realTarget, err := filepath.EvalSymlinks(target); err == nil {
		target = realTarget
	}

	workspace := WorkspaceInfo{
		ID:        id,
		Type:      "worktree",
		Name:      filepath.Base(target),
		Branch:    input.Branch,
		Directory: &target,
		Extra:     input.Extra,
		ProjectID: project.ID,
		TimeUsed:  time.Now().UnixMilli(),
	}
	store.upsertWorkspace(workspace)
	return workspace, nil
}

// RemoveWorkspace unregisters and removes a local workspace if it is known.
func (store *WorkspaceStore) RemoveWorkspace(ctx context.Context, directory string, id string) (*WorkspaceInfo, error) {
	project, err := CurrentProject(ctx, directory)
	if err != nil {
		return nil, err
	}
	store.upsertProject(project)

	store.mu.Lock()
	workspace, ok := store.workspaces[id]
	if ok {
		delete(store.workspaces, id)
		store.order = slices.DeleteFunc(store.order, func(candidate string) bool { return candidate == id })
	}
	store.mu.Unlock()
	if !ok {
		return nil, nil
	}
	if workspace.Directory != nil && project.VCS == "git" {
		_ = removeGitWorktree(ctx, project.Worktree, *workspace.Directory)
	}
	return &workspace, nil
}

// WarpWorkspaceSession records recent usage for the target workspace.
func (store *WorkspaceStore) WarpWorkspaceSession(_ context.Context, input WorkspaceWarpInput) error {
	if input.ID == nil {
		return nil
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	workspace, ok := store.workspaces[*input.ID]
	if !ok {
		return fmt.Errorf("workspace not found: %s", *input.ID)
	}
	workspace.TimeUsed = time.Now().UnixMilli()
	store.workspaces[*input.ID] = workspace
	return nil
}

// ListWorktreeDirectories returns sandbox worktree directories for the current project.
func (store *WorkspaceStore) ListWorktreeDirectories(ctx context.Context, directory string) ([]string, error) {
	project, err := CurrentProject(ctx, directory)
	if err != nil {
		return nil, err
	}
	if project.VCS != "git" {
		return []string{}, nil
	}
	items, err := gitWorktreeList(ctx, project.Worktree)
	if err != nil {
		return nil, err
	}
	result := []string{}
	for _, item := range items {
		if item.path == "" || samePath(item.path, project.Worktree) {
			continue
		}
		result = append(result, item.path)
	}
	slices.Sort(result)
	return result, nil
}

// CreateWorktree creates a git sandbox worktree with a branch.
func (store *WorkspaceStore) CreateWorktree(ctx context.Context, directory string, input WorktreeCreateInput) (WorktreeInfo, error) {
	project, err := CurrentProject(ctx, directory)
	if err != nil {
		return WorktreeInfo{}, err
	}
	if project.VCS != "git" {
		return WorktreeInfo{}, fmt.Errorf("worktrees are only supported for git projects")
	}
	name := slugifyWorkspaceName(input.Name)
	root := filepath.Join(globalStateDir(userHomeDir()), "worktree", project.ID)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return WorktreeInfo{}, fmt.Errorf("create worktree root: %w", err)
	}
	target := filepath.Join(root, name)
	branch := "opencode/" + name
	for suffix := 2; pathExists(target) || gitRefExists(ctx, project.Worktree, branch); suffix++ {
		name = fmt.Sprintf("%s-%d", slugifyWorkspaceName(input.Name), suffix)
		target = filepath.Join(root, name)
		branch = "opencode/" + name
	}

	cmd := exec.CommandContext(ctx, "git", "worktree", "add", "--no-checkout", "-b", branch, target, "HEAD")
	cmd.Dir = project.Worktree
	output, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = "failed to create git worktree"
		}
		return WorktreeInfo{}, fmt.Errorf("%s: %w", message, err)
	}
	if output, err := gitCombined(ctx, target, "reset", "--hard"); err != nil {
		return WorktreeInfo{}, fmt.Errorf("%s: %w", defaultMessage(output, "failed to populate git worktree"), err)
	}
	if strings.TrimSpace(input.StartCommand) != "" {
		if err := runShellCommand(ctx, target, input.StartCommand); err != nil {
			return WorktreeInfo{}, err
		}
	}
	if realTarget, err := filepath.EvalSymlinks(target); err == nil {
		target = realTarget
	}
	return WorktreeInfo{Name: filepath.Base(target), Branch: branch, Directory: target}, nil
}

// RemoveWorktree removes a git sandbox worktree.
func (store *WorkspaceStore) RemoveWorktree(ctx context.Context, directory string, input WorktreeDirectoryInput) (bool, error) {
	project, err := CurrentProject(ctx, directory)
	if err != nil {
		return false, err
	}
	if project.VCS != "git" {
		return false, fmt.Errorf("worktrees are only supported for git projects")
	}
	if input.Directory == "" {
		return false, fmt.Errorf("directory is required")
	}
	return true, removeGitWorktree(ctx, project.Worktree, input.Directory)
}

// ResetWorktree resets and cleans a sandbox worktree to the current project HEAD.
func (store *WorkspaceStore) ResetWorktree(ctx context.Context, directory string, input WorktreeDirectoryInput) (bool, error) {
	project, err := CurrentProject(ctx, directory)
	if err != nil {
		return false, err
	}
	if project.VCS != "git" {
		return false, fmt.Errorf("worktrees are only supported for git projects")
	}
	if input.Directory == "" {
		return false, fmt.Errorf("directory is required")
	}
	if samePath(project.Worktree, input.Directory) {
		return false, fmt.Errorf("cannot reset the primary workspace")
	}
	items, err := gitWorktreeList(ctx, project.Worktree)
	if err != nil {
		return false, err
	}
	found := false
	for _, item := range items {
		if samePath(item.path, input.Directory) {
			found = true
			input.Directory = item.path
			break
		}
	}
	if !found {
		return false, fmt.Errorf("worktree not found")
	}
	if output, err := gitCombined(ctx, input.Directory, "reset", "--hard", "HEAD"); err != nil {
		return false, fmt.Errorf("%s: %w", defaultMessage(output, "failed to reset worktree"), err)
	}
	if output, err := gitCombined(ctx, input.Directory, "clean", "-ffdx"); err != nil {
		return false, fmt.Errorf("%s: %w", defaultMessage(output, "failed to clean worktree"), err)
	}
	if status := strings.TrimSpace(gitText(ctx, input.Directory, "-c", "core.fsmonitor=false", "status", "--porcelain=v1")); status != "" {
		return false, fmt.Errorf("worktree reset left local changes:\n%s", status)
	}
	return true, nil
}

func (store *WorkspaceStore) upsertProject(project ProjectInfo) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if existing, ok := store.projects[project.ID]; ok {
		if existing.Name != "" {
			project.Name = existing.Name
		}
		if existing.Icon != nil {
			project.Icon = existing.Icon
		}
		if existing.Commands != nil {
			project.Commands = cloneStringMap(existing.Commands)
		}
		if existing.Time.Created != 0 {
			project.Time.Created = existing.Time.Created
		}
		if existing.Time.Initialized != 0 && project.Time.Initialized == 0 {
			project.Time.Initialized = existing.Time.Initialized
		}
	}
	store.projects[project.ID] = project
}

func (store *WorkspaceStore) upsertWorkspace(workspace WorkspaceInfo) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, ok := store.workspaces[workspace.ID]; !ok {
		store.order = append(store.order, workspace.ID)
	}
	store.workspaces[workspace.ID] = workspace
}

func (store *WorkspaceStore) listWorkspaces(projectID string) []WorkspaceInfo {
	store.mu.RLock()
	defer store.mu.RUnlock()
	result := []WorkspaceInfo{}
	for _, id := range store.order {
		workspace := store.workspaces[id]
		if workspace.ProjectID == projectID {
			result = append(result, workspace)
		}
	}
	slices.SortFunc(result, func(a WorkspaceInfo, b WorkspaceInfo) int {
		if a.TimeUsed != b.TimeUsed {
			if a.TimeUsed > b.TimeUsed {
				return -1
			}
			return 1
		}
		return strings.Compare(a.ID, b.ID)
	})
	return result
}

func (store *WorkspaceStore) syncGitWorktrees(ctx context.Context, project ProjectInfo) error {
	if project.VCS != "git" {
		return nil
	}
	items, err := gitWorktreeList(ctx, project.Worktree)
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.path == "" || samePath(item.path, project.Worktree) {
			continue
		}
		id := workspaceIDFromDirectory(item.path)
		branch := strings.TrimPrefix(item.branch, "refs/heads/")
		var branchPtr *string
		if branch != "" {
			branchPtr = &branch
		}
		directory := item.path
		if existing, ok := store.findWorkspaceByDirectory(project.ID, directory); ok {
			existing.Type = "worktree"
			existing.Branch = branchPtr
			existing.Directory = &directory
			existing.ProjectID = project.ID
			store.upsertWorkspace(existing)
			continue
		}
		store.upsertWorkspace(WorkspaceInfo{
			ID:        id,
			Type:      "worktree",
			Name:      filepath.Base(directory),
			Branch:    branchPtr,
			Directory: &directory,
			ProjectID: project.ID,
			TimeUsed:  time.Now().UnixMilli(),
		})
	}
	return nil
}

func (store *WorkspaceStore) findWorkspaceByDirectory(projectID string, directory string) (WorkspaceInfo, bool) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	for _, workspace := range store.workspaces {
		if workspace.ProjectID != projectID || workspace.Directory == nil {
			continue
		}
		if samePath(*workspace.Directory, directory) {
			return workspace, true
		}
	}
	return WorkspaceInfo{}, false
}

type gitWorktreeEntry struct {
	path   string
	branch string
}

func gitWorktreeList(ctx context.Context, directory string) ([]gitWorktreeEntry, error) {
	cmd := exec.CommandContext(ctx, "git", "worktree", "list", "--porcelain")
	cmd.Dir = directory
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("read git worktrees: %w", err)
	}
	entries := []gitWorktreeEntry{}
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "worktree ") {
			entries = append(entries, gitWorktreeEntry{path: strings.TrimSpace(strings.TrimPrefix(line, "worktree "))})
			continue
		}
		if strings.HasPrefix(line, "branch ") && len(entries) > 0 {
			entries[len(entries)-1].branch = strings.TrimSpace(strings.TrimPrefix(line, "branch "))
		}
	}
	return entries, nil
}

func removeGitWorktree(ctx context.Context, root string, directory string) error {
	var branch string
	if entries, err := gitWorktreeList(ctx, root); err == nil {
		for _, entry := range entries {
			if samePath(entry.path, directory) {
				branch = strings.TrimPrefix(entry.branch, "refs/heads/")
				break
			}
		}
	}
	cmd := exec.CommandContext(ctx, "git", "worktree", "remove", "--force", directory)
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		_ = os.RemoveAll(directory)
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = "failed to remove git worktree"
		}
		return fmt.Errorf("%s: %w", message, err)
	}
	_ = os.RemoveAll(directory)
	if branch != "" {
		_, _ = gitCombined(ctx, root, "branch", "-D", branch)
	}
	return nil
}

func gitCombined(ctx context.Context, directory string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = directory
	output, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(output)), err
}

func runShellCommand(ctx context.Context, directory string, command string) error {
	cmd := exec.CommandContext(ctx, "bash", "-lc", command)
	cmd.Dir = directory
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w", defaultMessage(strings.TrimSpace(string(output)), "worktree start command failed"), err)
	}
	return nil
}

func defaultMessage(value string, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func newWorkspaceID() string {
	random := make([]byte, 10)
	if _, err := rand.Read(random); err != nil {
		return fmt.Sprintf("wrk_%x", time.Now().UnixNano())
	}
	return "wrk_" + hex.EncodeToString(random)
}

func workspaceIDFromDirectory(directory string) string {
	return "wrk_" + hex.EncodeToString([]byte(filepath.Clean(directory)))
}

func slugifyWorkspaceName(input string) string {
	name := strings.Trim(strings.ToLower(input), "-")
	name = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			return r
		}
		return '-'
	}, name)
	name = strings.Trim(name, "-")
	if name == "" {
		return "workspace"
	}
	return name
}

func samePath(left string, right string) bool {
	leftAbs, leftErr := filepath.Abs(left)
	rightAbs, rightErr := filepath.Abs(right)
	if leftErr == nil {
		left = leftAbs
	}
	if rightErr == nil {
		right = rightAbs
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func userHomeDir() string {
	home, _ := os.UserHomeDir()
	return home
}

func cloneStringMap(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	result := make(map[string]string, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}
