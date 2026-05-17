// Package integration defines local tool, MCP, LSP, PTY, and filesystem
// integration contracts for the Go runtime.
package integration

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	defaultReadLimit = 2_000
	maxLineLength    = 2_000
	maxReadBytes     = 50 * 1024
	maxSearchResults = 100
	maxShellBytes    = 30_000
)

// Tool describes a migrated opencode tool surface.
type Tool struct {
	Name     string `json:"name"`
	Category string `json:"category"`
}

// ToolListItem mirrors the experimental tool registry HTTP contract.
type ToolListItem struct {
	ID          string     `json:"id"`
	Description string     `json:"description"`
	Parameters  JSONSchema `json:"parameters"`
}

// Request describes one Go tool execution.
type Request struct {
	Name      string         `json:"name"`
	Params    map[string]any `json:"params"`
	Directory string         `json:"directory,omitempty"`
}

// Result is the stable execution response returned by migrated Go tools.
type Result struct {
	Title    string         `json:"title"`
	Metadata map[string]any `json:"metadata"`
	Output   string         `json:"output"`
}

// AllTools returns the local tool inventory required for the migration.
func AllTools() []Tool {
	return append([]Tool(nil), tools...)
}

// ToolNames returns tool names in stable CLI order.
func ToolNames() []string {
	result := make([]string, 0, len(tools))
	for _, tool := range tools {
		result = append(result, tool.Name)
	}
	return result
}

// ToolIDs returns tool identifiers in stable registry order.
func ToolIDs() []string {
	return ToolNames()
}

// CanonicalToolName maps legacy Go-local aliases to TypeScript-compatible
// public tool IDs.
func CanonicalToolName(name string) string {
	switch name {
	case "shell":
		return "bash"
	case "todo":
		return "todowrite"
	case "fetch":
		return "webfetch"
	case "search":
		return "websearch"
	case "patch":
		return "apply_patch"
	default:
		return name
	}
}

// ToolAliases returns public and compatibility names that may refer to a tool.
func ToolAliases(name string) []string {
	switch CanonicalToolName(name) {
	case "bash":
		return []string{"bash", "shell"}
	case "todowrite":
		return []string{"todowrite", "todo"}
	case "webfetch":
		return []string{"webfetch", "fetch"}
	case "websearch":
		return []string{"websearch", "search"}
	case "apply_patch":
		return []string{"apply_patch", "patch"}
	default:
		return []string{CanonicalToolName(name)}
	}
}

// ToolList returns provider-facing tool descriptors for the local Go registry.
func ToolList() []ToolListItem {
	result := make([]ToolListItem, 0, len(tools))
	for _, tool := range tools {
		result = append(result, ToolListItem{
			ID:          tool.Name,
			Description: toolDescription(tool.Name),
			Parameters:  ToolSchema(tool.Name),
		})
	}
	return result
}

// Execute runs a migrated local tool.
func Execute(ctx context.Context, request Request) (Result, error) {
	switch CanonicalToolName(request.Name) {
	case "invalid":
		return invalidTool(request)
	case "read":
		return readTool(request)
	case "write":
		return writeTool(request)
	case "edit":
		return editTool(request)
	case "apply_patch":
		return applyPatchTool(request)
	case "glob":
		return globTool(request)
	case "grep":
		return grepTool(request)
	case "bash":
		return shellTool(ctx, request)
	case "lsp":
		return lspTool(request)
	case "webfetch":
		return webFetchTool(ctx, request)
	case "websearch":
		return webSearchTool(ctx, request)
	case "skill":
		return skillTool(request)
	case "repo_clone":
		return repoCloneTool(ctx, request)
	case "repo_overview":
		return repoOverviewTool(ctx, request)
	case "todowrite":
		return todoTool(request)
	case "question":
		return questionTool(request)
	case "task":
		return taskTool(request)
	case "task_status":
		return taskStatusTool(request)
	case "plan_exit":
		return planExitTool(request)
	default:
		return Result{}, fmt.Errorf("tool %q is not implemented in Go yet", request.Name)
	}
}

var tools = []Tool{
	{Name: "invalid", Category: "control"},
	{Name: "question", Category: "user-input"},
	{Name: "bash", Category: "process"},
	{Name: "read", Category: "filesystem"},
	{Name: "glob", Category: "search"},
	{Name: "grep", Category: "search"},
	{Name: "edit", Category: "filesystem"},
	{Name: "write", Category: "filesystem"},
	{Name: "task", Category: "agent"},
	{Name: "task_status", Category: "agent"},
	{Name: "webfetch", Category: "network"},
	{Name: "todowrite", Category: "session"},
	{Name: "websearch", Category: "network"},
	{Name: "repo_clone", Category: "git"},
	{Name: "repo_overview", Category: "git"},
	{Name: "skill", Category: "plugin"},
	{Name: "apply_patch", Category: "filesystem"},
	{Name: "lsp", Category: "language-server"},
	{Name: "plan_exit", Category: "control"},
}

func toolDescription(name string) string {
	switch CanonicalToolName(name) {
	case "invalid":
		return "Report invalid tool arguments back to the model."
	case "read":
		return "Read a file or directory from the local filesystem."
	case "write":
		return "Write a file to the local filesystem."
	case "edit":
		return "Perform exact string replacements in files."
	case "apply_patch":
		return "Apply a structured patch to local files."
	case "bash":
		return "Run a shell command in the workspace."
	case "glob":
		return "Fast file pattern matching across the workspace."
	case "grep":
		return "Fast content search across the workspace."
	case "lsp":
		return "Query language server features such as symbols and hover."
	case "task":
		return "Launch a subagent task."
	case "task_status":
		return "Poll a background subagent task."
	case "webfetch":
		return "Fetch content from a URL."
	case "websearch":
		return "Search the web for current information."
	case "question":
		return "Ask the user for structured input."
	case "todowrite":
		return "Create and maintain a structured task list."
	case "skill":
		return "Load a specialized skill by name."
	case "repo_clone":
		return "Clone or refresh a repository in the managed cache."
	case "repo_overview":
		return "Summarize a cached or local repository structure."
	case "plan_exit":
		return "Signal that plan mode is complete and implementation can begin."
	default:
		return name
	}
}

func readTool(request Request) (Result, error) {
	filePath, err := requireString(request.Params, "filePath")
	if err != nil {
		return Result{}, err
	}
	target := resolvePath(request.Directory, filePath)
	info, err := os.Stat(target)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Result{}, notFoundWithSuggestions(target)
		}
		return Result{}, fmt.Errorf("stat %s: %w", target, err)
	}

	if info.IsDir() {
		return readDirectory(target, request)
	}
	return readFile(target, request)
}

func readDirectory(target string, request Request) (Result, error) {
	entries, err := os.ReadDir(target)
	if err != nil {
		return Result{}, fmt.Errorf("read directory %s: %w", target, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			name += string(filepath.Separator)
		}
		names = append(names, name)
	}
	slices.Sort(names)

	offset := optionalInt(request.Params, "offset", 1)
	limit := optionalInt(request.Params, "limit", defaultReadLimit)
	start := max(0, offset-1)
	if start > len(names) && (len(names) != 0 || offset != 1) {
		return Result{}, fmt.Errorf("offset %d is out of range for this directory (%d entries)", offset, len(names))
	}
	end := min(len(names), start+limit)
	selected := names[start:end]
	truncated := end < len(names)
	output := strings.Join([]string{
		"<path>" + target + "</path>",
		"<type>directory</type>",
		"<entries>",
		strings.Join(selected, "\n"),
		directoryFooter(len(selected), len(names), offset, truncated),
		"</entries>",
	}, "\n")
	return Result{
		Title: target,
		Metadata: map[string]any{
			"preview":   strings.Join(selected[:min(len(selected), 20)], "\n"),
			"truncated": truncated,
			"loaded":    []string{},
		},
		Output: output,
	}, nil
}

func readFile(target string, request Request) (Result, error) {
	data, err := os.ReadFile(target)
	if err != nil {
		return Result{}, fmt.Errorf("read file %s: %w", target, err)
	}
	if isBinary(data) {
		return Result{}, fmt.Errorf("cannot read binary file: %s", target)
	}

	offset := optionalInt(request.Params, "offset", 1)
	limit := optionalInt(request.Params, "limit", defaultReadLimit)
	lines := splitLines(string(data))
	if offset > len(lines) && (len(lines) != 0 || offset != 1) {
		return Result{}, fmt.Errorf("offset %d is out of range for this file (%d lines)", offset, len(lines))
	}

	start := max(0, offset-1)
	raw := []string{}
	totalBytes := 0
	cut := false
	for i := start; i < len(lines) && len(raw) < limit; i++ {
		line := truncateLine(lines[i])
		size := len([]byte(line))
		if len(raw) > 0 {
			size++
		}
		if totalBytes+size > maxReadBytes {
			cut = true
			break
		}
		raw = append(raw, line)
		totalBytes += size
	}
	last := offset + len(raw) - 1
	more := start+len(raw) < len(lines)

	var output strings.Builder
	output.WriteString("<path>" + target + "</path>\n<type>file</type>\n<content>\n")
	for i, line := range raw {
		if i > 0 {
			output.WriteByte('\n')
		}
		output.WriteString(strconv.Itoa(offset+i) + ": " + line)
	}
	if cut {
		_, _ = fmt.Fprintf(&output, "\n\n(Output capped at 50 KB. Showing lines %d-%d. Use offset=%d to continue.)", offset, last, last+1)
	} else if more {
		_, _ = fmt.Fprintf(&output, "\n\n(Showing lines %d-%d of %d. Use offset=%d to continue.)", offset, last, len(lines), last+1)
	} else {
		_, _ = fmt.Fprintf(&output, "\n\n(End of file - total %d lines)", len(lines))
	}
	output.WriteString("\n</content>")

	return Result{
		Title: target,
		Metadata: map[string]any{
			"preview":   strings.Join(raw[:min(len(raw), 20)], "\n"),
			"truncated": cut || more,
			"loaded":    []string{},
		},
		Output: output.String(),
	}, nil
}

func writeTool(request Request) (Result, error) {
	filePath, err := requireString(request.Params, "filePath")
	if err != nil {
		return Result{}, err
	}
	content, err := requireString(request.Params, "content")
	if err != nil {
		return Result{}, err
	}
	target := resolvePath(request.Directory, filePath)
	existing, readErr := os.ReadFile(target)
	exists := readErr == nil
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return Result{}, fmt.Errorf("read existing file %s: %w", target, readErr)
	}

	data := []byte(content)
	if bytes.HasPrefix(existing, []byte{0xef, 0xbb, 0xbf}) && !bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) {
		data = append([]byte{0xef, 0xbb, 0xbf}, data...)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return Result{}, fmt.Errorf("create parent directory: %w", err)
	}
	if err := os.WriteFile(target, data, 0o644); err != nil {
		return Result{}, fmt.Errorf("write file %s: %w", target, err)
	}

	return Result{
		Title: target,
		Metadata: map[string]any{
			"filepath": target,
			"exists":   exists,
		},
		Output: "Wrote file successfully.",
	}, nil
}

func globTool(request Request) (Result, error) {
	pattern, err := requireString(request.Params, "pattern")
	if err != nil {
		return Result{}, err
	}
	search := resolvePath(request.Directory, optionalString(request.Params, "path", "."))
	info, err := os.Stat(search)
	if err != nil {
		return Result{}, fmt.Errorf("stat glob path: %w", err)
	}
	if !info.IsDir() {
		return Result{}, fmt.Errorf("glob path must be a directory: %s", search)
	}

	matches := []fileMatch{}
	if err := filepath.WalkDir(search, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(search, path)
		if relErr != nil {
			return relErr
		}
		if !matchGlob(pattern, rel) && !matchGlob(pattern, filepath.Base(path)) {
			return nil
		}
		info, statErr := entry.Info()
		if statErr != nil {
			return nil
		}
		matches = append(matches, fileMatch{Path: path, ModTime: info.ModTime()})
		return nil
	}); err != nil {
		return Result{}, fmt.Errorf("walk glob path: %w", err)
	}
	sortMatches(matches)
	truncated := len(matches) > maxSearchResults
	if truncated {
		matches = matches[:maxSearchResults]
	}
	output := "No files found"
	if len(matches) > 0 {
		lines := make([]string, 0, len(matches)+2)
		for _, match := range matches {
			lines = append(lines, match.Path)
		}
		if truncated {
			lines = append(lines, "", "(Results are truncated: showing first 100 results. Consider using a more specific path or pattern.)")
		}
		output = strings.Join(lines, "\n")
	}
	return Result{
		Title: search,
		Metadata: map[string]any{
			"count":     len(matches),
			"truncated": truncated,
		},
		Output: output,
	}, nil
}

func grepTool(request Request) (Result, error) {
	pattern, err := requireString(request.Params, "pattern")
	if err != nil {
		return Result{}, err
	}
	if pattern == "" {
		return Result{}, errors.New("pattern is required")
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return Result{}, fmt.Errorf("compile pattern: %w", err)
	}
	search := resolvePath(request.Directory, optionalString(request.Params, "path", "."))
	include := optionalString(request.Params, "include", "")
	info, err := os.Stat(search)
	if err != nil {
		return Result{}, fmt.Errorf("stat grep path: %w", err)
	}

	files := []string{}
	if info.IsDir() {
		if err := filepath.WalkDir(search, func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return nil
			}
			if include != "" && !matchGlob(include, filepath.Base(path)) && !matchGlob(include, mustRel(search, path)) {
				return nil
			}
			files = append(files, path)
			return nil
		}); err != nil {
			return Result{}, fmt.Errorf("walk grep path: %w", err)
		}
	} else {
		files = append(files, search)
	}

	matches := []grepMatch{}
	for _, file := range files {
		fileMatches, err := grepFile(file, re)
		if err != nil {
			continue
		}
		matches = append(matches, fileMatches...)
	}
	slices.SortFunc(matches, func(a grepMatch, b grepMatch) int {
		if !a.ModTime.Equal(b.ModTime) {
			if a.ModTime.After(b.ModTime) {
				return -1
			}
			return 1
		}
		if a.Path < b.Path {
			return -1
		}
		if a.Path > b.Path {
			return 1
		}
		return a.Line - b.Line
	})
	if len(matches) == 0 {
		return Result{Title: pattern, Metadata: map[string]any{"matches": 0, "truncated": false}, Output: "No files found"}, nil
	}

	total := len(matches)
	truncated := total > maxSearchResults
	final := matches
	if truncated {
		final = final[:maxSearchResults]
	}
	output := []string{fmt.Sprintf("Found %d matches", total)}
	if truncated {
		output[0] = fmt.Sprintf("Found %d matches (showing first %d)", total, maxSearchResults)
	}
	current := ""
	for _, match := range final {
		if match.Path != current {
			if current != "" {
				output = append(output, "")
			}
			current = match.Path
			output = append(output, match.Path+":")
		}
		output = append(output, fmt.Sprintf("  Line %d: %s", match.Line, truncateLine(match.Text)))
	}
	if truncated {
		output = append(output, "", fmt.Sprintf("(Results truncated: showing %d of %d matches (%d hidden). Consider using a more specific path or pattern.)", maxSearchResults, total, total-maxSearchResults))
	}
	return Result{
		Title: pattern,
		Metadata: map[string]any{
			"matches":   total,
			"truncated": truncated,
		},
		Output: strings.Join(output, "\n"),
	}, nil
}

func shellTool(ctx context.Context, request Request) (Result, error) {
	command, err := requireString(request.Params, "command")
	if err != nil {
		return Result{}, err
	}
	cwd := resolvePath(request.Directory, ".")
	shell, args := shellCommand(command)
	cmd := exec.CommandContext(ctx, shell, args...)
	cmd.Dir = cwd
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	exit := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exit = exitErr.ExitCode()
		} else {
			return Result{}, fmt.Errorf("run shell command: %w", err)
		}
	}
	output := strings.TrimRight(stdout.String()+stderr.String(), "\n")
	truncated := false
	if len([]byte(output)) > maxShellBytes {
		truncated = true
		buf := []byte(output)
		output = string(buf[len(buf)-maxShellBytes:])
	}
	return Result{
		Title: command,
		Metadata: map[string]any{
			"exit":      exit,
			"output":    output,
			"truncated": truncated,
		},
		Output: output,
	}, nil
}

func requireString(params map[string]any, key string) (string, error) {
	value, ok := params[key]
	if !ok {
		return "", fmt.Errorf("%s is required", key)
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string", key)
	}
	return text, nil
}

func optionalString(params map[string]any, key string, fallback string) string {
	if value, ok := params[key].(string); ok && value != "" {
		return value
	}
	return fallback
}

func optionalInt(params map[string]any, key string, fallback int) int {
	switch value := params[key].(type) {
	case int:
		return value
	case float64:
		return int(value)
	case string:
		parsed, err := strconv.Atoi(value)
		if err == nil {
			return parsed
		}
	}
	return fallback
}

func resolvePath(directory string, value string) string {
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	if directory == "" {
		directory = "."
	}
	return filepath.Clean(filepath.Join(directory, value))
}

func notFoundWithSuggestions(target string) error {
	entries, err := os.ReadDir(filepath.Dir(target))
	if err != nil {
		return fmt.Errorf("file not found: %s", target)
	}
	base := strings.ToLower(filepath.Base(target))
	suggestions := []string{}
	for _, entry := range entries {
		name := strings.ToLower(entry.Name())
		if strings.Contains(name, base) || strings.Contains(base, name) {
			suggestions = append(suggestions, filepath.Join(filepath.Dir(target), entry.Name()))
		}
	}
	if len(suggestions) == 0 {
		return fmt.Errorf("file not found: %s", target)
	}
	if len(suggestions) > 3 {
		suggestions = suggestions[:3]
	}
	return fmt.Errorf("file not found: %s\n\nDid you mean one of these?\n%s", target, strings.Join(suggestions, "\n"))
}

func directoryFooter(selected int, total int, offset int, truncated bool) string {
	if truncated {
		return fmt.Sprintf("\n(Showing %d of %d entries. Use 'offset' parameter to read beyond entry %d)", selected, total, offset+selected)
	}
	return fmt.Sprintf("\n(%d entries)", total)
}

func splitLines(text string) []string {
	if text == "" {
		return []string{}
	}
	return strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
}

func truncateLine(line string) string {
	if len(line) <= maxLineLength {
		return line
	}
	return line[:maxLineLength] + "... (line truncated to 2000 chars)"
}

func isBinary(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	if !utf8.Valid(data) {
		return true
	}
	limit := min(len(data), 4096)
	nonPrintable := 0
	for _, b := range data[:limit] {
		if b == 0 {
			return true
		}
		if b < 9 || b > 13 && b < 32 {
			nonPrintable++
		}
	}
	return float64(nonPrintable)/float64(limit) > 0.3
}

func matchGlob(pattern string, path string) bool {
	path = filepath.ToSlash(path)
	pattern = filepath.ToSlash(pattern)
	if ok, _ := filepath.Match(pattern, path); ok {
		return true
	}
	if strings.Contains(pattern, "**") {
		re := regexp.QuoteMeta(pattern)
		re = strings.ReplaceAll(re, `\*\*`, ".*")
		re = strings.ReplaceAll(re, `\*`, `[^/]*`)
		ok, _ := regexp.MatchString("^"+re+"$", path)
		return ok
	}
	return false
}

type fileMatch struct {
	Path    string
	ModTime time.Time
}

func sortMatches(matches []fileMatch) {
	slices.SortFunc(matches, func(a fileMatch, b fileMatch) int {
		if a.ModTime.After(b.ModTime) {
			return -1
		}
		if a.ModTime.Before(b.ModTime) {
			return 1
		}
		return strings.Compare(a.Path, b.Path)
	})
}

type grepMatch struct {
	Path    string
	Line    int
	Text    string
	ModTime time.Time
}

func grepFile(path string, re *regexp.Regexp) ([]grepMatch, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if isBinary(data) {
		return nil, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNumber := 0
	result := []grepMatch{}
	for scanner.Scan() {
		lineNumber++
		text := scanner.Text()
		if re.MatchString(text) {
			result = append(result, grepMatch{Path: path, Line: lineNumber, Text: text, ModTime: info.ModTime()})
		}
	}
	return result, scanner.Err()
}

func mustRel(base string, target string) string {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return target
	}
	return rel
}

func shellCommand(command string) (string, []string) {
	if runtime.GOOS == "windows" {
		if comspec := os.Getenv("COMSPEC"); comspec != "" {
			return comspec, []string{"/C", command}
		}
		return "cmd.exe", []string{"/C", command}
	}
	if shell := os.Getenv("SHELL"); shell != "" {
		return shell, []string{"-lc", command}
	}
	return "/bin/sh", []string{"-lc", command}
}

// HTTPStatus maps tool execution errors to the local HTTP API status.
func HTTPStatus(err error) int {
	if err == nil {
		return http.StatusOK
	}
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return http.StatusBadRequest
	}
	return http.StatusBadRequest
}
