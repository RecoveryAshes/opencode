package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

var ignoredOverviewDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	"__pycache__":  true,
	".venv":        true,
	"dist":         true,
	"build":        true,
	".next":        true,
	"target":       true,
	"vendor":       true,
}

func repoCloneTool(ctx context.Context, request Request) (Result, error) {
	repository, err := requireString(request.Params, "repository")
	if err != nil {
		return Result{}, err
	}
	ref, err := parseRepositoryReference(repository)
	if err != nil {
		return Result{}, err
	}
	if ref.protocol == "file" {
		return Result{}, fmt.Errorf("local file repositories are not supported")
	}
	branch := optionalString(request.Params, "branch", "")
	if branch != "" && !validRepositoryBranch(branch) {
		return Result{}, fmt.Errorf("branch must contain only alphanumeric characters, /, _, ., and -, and cannot start with - or contain double dots")
	}
	localPath := repositoryCachePath(ref)
	status := "cached"
	if _, err := os.Stat(filepath.Join(localPath, ".git")); err != nil {
		if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
			return Result{}, fmt.Errorf("create repo cache parent: %w", err)
		}
		args := []string{"clone", "--depth", "100"}
		if branch != "" {
			args = append(args, "--branch", branch)
		}
		args = append(args, "--", ref.remote, localPath)
		if output, err := runGit(ctx, filepath.Dir(localPath), args...); err != nil {
			return Result{}, fmt.Errorf("clone %s: %s: %w", ref.label, output, err)
		}
		status = "cloned"
	} else if optionalBool(request.Params, "refresh", false) {
		if output, err := runGit(ctx, localPath, "fetch", "--all", "--prune"); err != nil {
			return Result{}, fmt.Errorf("refresh %s: %s: %w", ref.label, output, err)
		}
		status = "refreshed"
	}
	if branch != "" && status != "cloned" {
		if output, err := runGit(ctx, localPath, "checkout", "-B", branch, "origin/"+branch); err != nil {
			return Result{}, fmt.Errorf("checkout %s: %s: %w", branch, output, err)
		}
		status = "refreshed"
	}
	head, _ := runGit(ctx, localPath, "rev-parse", "HEAD")
	currentBranch, _ := runGit(ctx, localPath, "branch", "--show-current")
	metadata := map[string]any{
		"repository": ref.label,
		"host":       ref.host,
		"remote":     ref.remote,
		"localPath":  localPath,
		"status":     status,
		"head":       strings.TrimSpace(head),
		"branch":     strings.TrimSpace(currentBranch),
	}
	return Result{
		Title:    ref.label,
		Metadata: metadata,
		Output: strings.Join([]string{
			"Repository ready: " + ref.label,
			"Status: " + status,
			"Local path: " + localPath,
			"Branch: " + strings.TrimSpace(currentBranch),
			"HEAD: " + strings.TrimSpace(head),
		}, "\n"),
	}, nil
}

func repoOverviewTool(ctx context.Context, request Request) (Result, error) {
	target := optionalString(request.Params, "path", "")
	repository := optionalString(request.Params, "repository", "")
	if target == "" {
		if repository == "" {
			return Result{}, fmt.Errorf("either repository or path is required")
		}
		ref, err := parseRepositoryReference(repository)
		if err != nil {
			return Result{}, err
		}
		target = repositoryCachePath(ref)
		repository = ref.label
	} else {
		target = resolvePath(request.Directory, target)
	}
	info, err := os.Stat(target)
	if err != nil {
		return Result{}, fmt.Errorf("directory not found: %s", target)
	}
	if !info.IsDir() {
		return Result{}, fmt.Errorf("path is not a directory: %s", target)
	}
	depth := optionalInt(request.Params, "depth", 3)
	if depth < 1 || depth > 6 {
		depth = 3
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		return Result{}, fmt.Errorf("read directory: %w", err)
	}
	topLevel := map[string]bool{}
	for _, entry := range entries {
		topLevel[entry.Name()] = true
	}
	structure, truncated, err := overviewStructure(target, depth)
	if err != nil {
		return Result{}, err
	}
	branch, _ := runGit(ctx, target, "branch", "--show-current")
	head, _ := runGit(ctx, target, "rev-parse", "HEAD")
	dependencyFiles := dependencyFiles(topLevel)
	entrypoints := entrypoints(target, topLevel)
	ecosystems := ecosystems(topLevel)
	metadata := map[string]any{
		"path":             target,
		"repository":       repository,
		"branch":           strings.TrimSpace(branch),
		"head":             strings.TrimSpace(head),
		"package_manager":  packageManager(topLevel),
		"ecosystems":       ecosystems,
		"dependency_files": dependencyFiles,
		"entrypoints":      entrypoints,
		"depth":            depth,
		"truncated":        truncated,
	}
	lines := []string{"Path: " + target}
	if repository != "" {
		lines = append(lines, "Repository: "+repository)
	}
	if strings.TrimSpace(branch) != "" {
		lines = append(lines, "Branch: "+strings.TrimSpace(branch))
	}
	if strings.TrimSpace(head) != "" {
		lines = append(lines, "HEAD: "+strings.TrimSpace(head))
	}
	if len(ecosystems) > 0 {
		lines = append(lines, "Ecosystems: "+strings.Join(ecosystems, ", "))
	}
	if pm := packageManager(topLevel); pm != "" {
		lines = append(lines, "Package manager: "+pm)
	}
	if len(dependencyFiles) > 0 {
		lines = append(lines, "Dependency files: "+strings.Join(dependencyFiles, ", "))
	}
	if len(entrypoints) > 0 {
		lines = append(lines, "Likely entrypoints:")
		for _, entry := range entrypoints {
			lines = append(lines, "- "+entry)
		}
	}
	lines = append(lines, "Top-level structure:")
	lines = append(lines, structure...)
	if truncated {
		lines = append(lines, "(Structure truncated)")
	}
	title := filepath.Base(target)
	if repository != "" {
		title = repository
	}
	return Result{Title: title, Metadata: metadata, Output: strings.Join(lines, "\n")}, nil
}

type repositoryReference struct {
	host     string
	path     string
	segments []string
	owner    string
	repo     string
	remote   string
	label    string
	protocol string
}

func parseRepositoryReference(input string) (repositoryReference, error) {
	cleaned := strings.TrimSpace(strings.TrimPrefix(input, "git+"))
	cleaned = strings.TrimSuffix(strings.Split(cleaned, "#")[0], "/")
	if cleaned == "" {
		return repositoryReference{}, fmt.Errorf("repository must be a git URL, host/path reference, or GitHub owner/repo shorthand")
	}
	if !strings.Contains(cleaned, "://") {
		if strings.HasPrefix(cleaned, "github:") {
			parts := strings.Split(strings.TrimPrefix(cleaned, "github:"), "/")
			return buildRepositoryReference("github.com", parts, "", "")
		}
		if scp := regexpScp(cleaned); scp != nil {
			return buildRepositoryReference(scp[0], splitRepoPath(scp[1]), cleaned, "ssh")
		}
		parts := splitRepoPath(cleaned)
		if len(parts) == 2 {
			return buildRepositoryReference("github.com", parts, "", "")
		}
		if len(parts) >= 2 && strings.Contains(parts[0], ".") {
			return buildRepositoryReference(parts[0], parts[1:], "", "")
		}
	}
	parsed, err := url.Parse(cleaned)
	if err != nil {
		return repositoryReference{}, fmt.Errorf("repository must be a git URL, host/path reference, or GitHub owner/repo shorthand")
	}
	if parsed.Scheme == "file" {
		return buildRepositoryReference("file", splitRepoPath(parsed.Path), cleaned, "file")
	}
	return buildRepositoryReference(parsed.Host, splitRepoPath(parsed.Path), cleaned, parsed.Scheme)
}

func buildRepositoryReference(host string, segments []string, remote string, protocol string) (repositoryReference, error) {
	cleanSegments := []string{}
	for _, segment := range segments {
		segment = strings.TrimSuffix(strings.TrimSpace(segment), ".git")
		if segment == "" || segment == "." || segment == ".." || strings.ContainsAny(segment, `:\/ `) {
			continue
		}
		cleanSegments = append(cleanSegments, segment)
	}
	if host == "" || strings.HasPrefix(host, "-") || len(cleanSegments) == 0 {
		return repositoryReference{}, fmt.Errorf("repository must be a git URL, host/path reference, or GitHub owner/repo shorthand")
	}
	pathName := strings.Join(cleanSegments, "/")
	if remote == "" {
		if host == "github.com" {
			remote = "https://github.com/" + pathName + ".git"
		} else {
			remote = "https://" + host + "/" + pathName + ".git"
		}
	}
	label := host + "/" + pathName
	owner := ""
	if host == "github.com" && len(cleanSegments) == 2 {
		label = pathName
		owner = cleanSegments[0]
	}
	return repositoryReference{
		host:     strings.ToLower(host),
		path:     pathName,
		segments: cleanSegments,
		owner:    owner,
		repo:     cleanSegments[len(cleanSegments)-1],
		remote:   remote,
		label:    label,
		protocol: protocol,
	}, nil
}

func repositoryCachePath(ref repositoryReference) string {
	root := firstNonEmptyEnv("OPENCODE_REPO_CACHE_DIR")
	if root == "" {
		root = filepath.Join(os.TempDir(), "opencode-go", "repos")
	}
	parts := append([]string{root}, strings.Split(ref.host, ":")...)
	parts = append(parts, ref.segments...)
	return filepath.Join(parts...)
}

func regexpScp(input string) []string {
	index := strings.Index(input, ":")
	if index <= 0 || index == len(input)-1 {
		return nil
	}
	hostPart := input[:index]
	if strings.ContainsAny(hostPart, `/\ `+"\t\r\n") {
		return nil
	}
	if at := strings.LastIndex(hostPart, "@"); at >= 0 {
		hostPart = hostPart[at+1:]
	}
	return []string{hostPart, input[index+1:]}
}

func splitRepoPath(input string) []string {
	parts := []string{}
	for _, part := range strings.Split(strings.Trim(input, "/"), "/") {
		part = strings.TrimSpace(strings.TrimSuffix(part, ".git"))
		if part != "" {
			parts = append(parts, part)
		}
	}
	return parts
}

func validRepositoryBranch(branch string) bool {
	if strings.HasPrefix(branch, "-") || strings.Contains(branch, "..") {
		return false
	}
	for _, r := range branch {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("/_.-", r) {
			continue
		}
		return false
	}
	return branch != ""
}

func overviewStructure(root string, depth int) ([]string, bool, error) {
	lines := []string{}
	truncated := false
	var visit func(string, int) error
	visit = func(dir string, level int) error {
		if level >= depth || len(lines) >= 200 {
			truncated = truncated || len(lines) >= 200
			return nil
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil
		}
		slices.SortFunc(entries, func(a os.DirEntry, b os.DirEntry) int {
			if a.IsDir() != b.IsDir() {
				if a.IsDir() {
					return -1
				}
				return 1
			}
			return strings.Compare(a.Name(), b.Name())
		})
		for _, entry := range entries {
			if ignoredOverviewDirs[entry.Name()] {
				continue
			}
			if len(lines) >= 200 {
				truncated = true
				return nil
			}
			name := entry.Name()
			if entry.IsDir() {
				name += "/"
			}
			lines = append(lines, strings.Repeat("  ", level)+name)
			if entry.IsDir() {
				if err := visit(filepath.Join(dir, entry.Name()), level+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	err := visit(root, 0)
	return lines, truncated, err
}

func dependencyFiles(files map[string]bool) []string {
	names := []string{"package.json", "package-lock.json", "bun.lock", "bun.lockb", "pnpm-lock.yaml", "yarn.lock", "requirements.txt", "pyproject.toml", "go.mod", "Cargo.toml", "Gemfile", "build.gradle", "build.gradle.kts", "pom.xml", "composer.json"}
	result := []string{}
	for _, name := range names {
		if files[name] {
			result = append(result, name)
		}
	}
	return result
}

func packageManager(files map[string]bool) string {
	switch {
	case files["bun.lock"] || files["bun.lockb"]:
		return "bun"
	case files["pnpm-lock.yaml"]:
		return "pnpm"
	case files["yarn.lock"]:
		return "yarn"
	case files["package-lock.json"]:
		return "npm"
	default:
		return ""
	}
}

func ecosystems(files map[string]bool) []string {
	result := []string{}
	if files["package.json"] {
		result = append(result, "Node.js")
	}
	if files["pyproject.toml"] || files["requirements.txt"] {
		result = append(result, "Python")
	}
	if files["go.mod"] {
		result = append(result, "Go")
	}
	if files["Cargo.toml"] {
		result = append(result, "Rust")
	}
	if files["Gemfile"] {
		result = append(result, "Ruby")
	}
	if files["build.gradle"] || files["build.gradle.kts"] || files["pom.xml"] {
		result = append(result, "Java/Kotlin")
	}
	if files["composer.json"] {
		result = append(result, "PHP")
	}
	return result
}

func entrypoints(root string, topLevel map[string]bool) []string {
	result := []string{}
	if topLevel["package.json"] {
		data, err := os.ReadFile(filepath.Join(root, "package.json"))
		if err == nil {
			var pkg map[string]any
			if json.Unmarshal(data, &pkg) == nil {
				for _, key := range []string{"main", "module", "types"} {
					if value, ok := pkg[key].(string); ok {
						result = append(result, key+": "+value)
					}
				}
			}
		}
	}
	for _, file := range []string{"index.ts", "index.tsx", "index.js", "main.ts", "main.js"} {
		if topLevel[file] {
			result = append(result, "file: "+file)
		}
	}
	return result
}

func runGit(ctx context.Context, cwd string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = cwd
	output, err := cmd.CombinedOutput()
	return string(output), err
}
