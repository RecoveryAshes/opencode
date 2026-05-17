package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// CommandInfo is the migrated local slash-command contract.
type CommandInfo struct {
	Name        string `json:"name"`
	Template    string `json:"template"`
	Description string `json:"description,omitempty"`
	Agent       string `json:"agent,omitempty"`
	Model       string `json:"model,omitempty"`
	Subtask     *bool  `json:"subtask,omitempty"`
	Path        string `json:"path"`
}

// CommandInput contains the user-supplied arguments for a command render.
type CommandInput struct {
	Name      string
	Argument  string
	Directory string
}

// CommandResult is the rendered command prompt plus parsed metadata.
type CommandResult struct {
	Command   CommandInfo `json:"command"`
	Argument  string      `json:"argument"`
	Prompt    string      `json:"prompt"`
	Agent     string      `json:"agent,omitempty"`
	Provider  string      `json:"provider,omitempty"`
	Model     string      `json:"model,omitempty"`
	ShellRuns int         `json:"shellRuns"`
}

var (
	commandArgsRegex        = regexp.MustCompile(`(?:\[Image\s+\d+\]|"[^"]*"|'[^']*'|[^\s"']+)`)
	commandPlaceholderRegex = regexp.MustCompile(`\$(\d+)`)
	commandShellRegex       = regexp.MustCompile("!`([^`]+)`")
)

// LoadCommands discovers local command markdown files and parses frontmatter.
func LoadCommands(root string) (map[string]CommandInfo, error) {
	root = filepath.Clean(root)
	files, err := commandFiles(root)
	if err != nil {
		return nil, err
	}
	result := map[string]CommandInfo{}
	for _, file := range files {
		command, err := ParseCommandFile(root, file)
		if err != nil {
			return nil, err
		}
		result[command.Name] = command
	}
	return result, nil
}

// ParseCommandFile parses one markdown command file.
func ParseCommandFile(root string, path string) (CommandInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return CommandInfo{}, fmt.Errorf("read command %s: %w", path, err)
	}
	meta, content, err := parseMarkdownFrontmatter(string(data))
	if err != nil {
		return CommandInfo{}, fmt.Errorf("parse command %s: %w", path, err)
	}
	command := CommandInfo{
		Name:        commandNameFromPath(root, path),
		Template:    strings.TrimSpace(content),
		Description: meta["description"],
		Agent:       meta["agent"],
		Model:       meta["model"],
		Path:        path,
	}
	if value, ok := parseBool(meta["subtask"]); ok {
		command.Subtask = &value
	}
	if command.Template == "" {
		return CommandInfo{}, fmt.Errorf("command %s has an empty template", path)
	}
	return command, nil
}

// ExecuteCommand renders a discovered command using the TypeScript command
// placeholder semantics and local shell interpolation.
func ExecuteCommand(ctx context.Context, input CommandInput) (CommandResult, error) {
	root := filepath.Clean(defaultCommandString(input.Directory, "."))
	commands, err := LoadCommands(root)
	if err != nil {
		return CommandResult{}, err
	}
	command, ok := commands[input.Name]
	if !ok {
		names := make([]string, 0, len(commands))
		for name := range commands {
			names = append(names, name)
		}
		slices.Sort(names)
		hint := ""
		if len(names) > 0 {
			hint = " Available commands: " + strings.Join(names, ", ")
		}
		return CommandResult{}, fmt.Errorf("command not found: %q.%s", input.Name, hint)
	}
	prompt, shellRuns, err := RenderCommandTemplate(ctx, command.Template, input.Argument, root)
	if err != nil {
		return CommandResult{}, err
	}
	provider, model := parseCommandModel(command.Model)
	return CommandResult{
		Command:   command,
		Argument:  input.Argument,
		Prompt:    prompt,
		Agent:     command.Agent,
		Provider:  provider,
		Model:     model,
		ShellRuns: shellRuns,
	}, nil
}

// RenderCommandTemplate applies $1, $2, $ARGUMENTS, and !`cmd` expansion.
func RenderCommandTemplate(ctx context.Context, template string, argument string, directory string) (string, int, error) {
	args := commandArgsRegex.FindAllString(argument, -1)
	for i, arg := range args {
		args[i] = strings.Trim(arg, `"'`)
	}
	last := 0
	for _, match := range commandPlaceholderRegex.FindAllStringSubmatch(template, -1) {
		var position int
		_, _ = fmt.Sscanf(match[1], "%d", &position)
		if position > last {
			last = position
		}
	}
	rendered := commandPlaceholderRegex.ReplaceAllStringFunc(template, func(match string) string {
		var position int
		_, _ = fmt.Sscanf(strings.TrimPrefix(match, "$"), "%d", &position)
		index := position - 1
		if index >= len(args) {
			return ""
		}
		if position == last {
			return strings.Join(args[index:], " ")
		}
		return args[index]
	})
	usesArguments := strings.Contains(template, "$ARGUMENTS")
	rendered = strings.ReplaceAll(rendered, "$ARGUMENTS", argument)
	if last == 0 && !usesArguments && strings.TrimSpace(argument) != "" {
		rendered = rendered + "\n\n" + argument
	}
	rendered, shellRuns, err := expandCommandShell(ctx, rendered, directory)
	if err != nil {
		return "", 0, err
	}
	return strings.TrimSpace(rendered), shellRuns, nil
}

func commandFiles(root string) ([]string, error) {
	result := []string{}
	for _, rel := range []string{
		filepath.Join(".opencode", "command"),
		filepath.Join(".opencode", "commands"),
		"command",
		"commands",
	} {
		dir := filepath.Join(root, rel)
		if err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			switch strings.ToLower(filepath.Ext(entry.Name())) {
			case ".md", ".markdown":
				result = append(result, path)
			}
			return nil
		}); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
	}
	slices.Sort(result)
	return result, nil
}

func parseMarkdownFrontmatter(input string) (map[string]string, string, error) {
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
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		value = strings.Trim(value, `"'`)
		meta[key] = value
	}
	return meta, content, nil
}

func commandNameFromPath(root string, path string) string {
	normalized := filepath.ToSlash(filepath.Clean(path))
	patterns := []string{
		"/.opencode/command/",
		"/.opencode/commands/",
		"/command/",
		"/commands/",
	}
	for _, pattern := range patterns {
		if index := strings.Index(normalized, pattern); index != -1 {
			rel := normalized[index+len(pattern):]
			return strings.TrimSuffix(rel, filepath.Ext(rel))
		}
	}
	rel, err := filepath.Rel(root, path)
	if err == nil {
		return strings.TrimSuffix(filepath.ToSlash(rel), filepath.Ext(rel))
	}
	base := filepath.Base(path)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

func expandCommandShell(ctx context.Context, template string, directory string) (string, int, error) {
	matches := commandShellRegex.FindAllStringSubmatch(template, -1)
	if len(matches) == 0 {
		return template, 0, nil
	}
	index := 0
	var firstErr error
	rendered := commandShellRegex.ReplaceAllStringFunc(template, func(_ string) string {
		command := matches[index][1]
		index++
		output, err := runCommandShell(ctx, command, directory)
		if err != nil && firstErr == nil {
			firstErr = err
		}
		return output
	})
	if firstErr != nil {
		return "", index, firstErr
	}
	return rendered, index, nil
}

func runCommandShell(ctx context.Context, command string, directory string) (string, error) {
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	cmd.Dir = directory
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String() + stderr.String(), nil
	}
	return stdout.String(), nil
}

func parseCommandModel(model string) (string, string) {
	if model == "" {
		return "", ""
	}
	provider, rest, ok := strings.Cut(model, "/")
	if !ok {
		return "", model
	}
	return provider, rest
}

func parseBool(value string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true":
		return true, true
	case "false":
		return false, true
	default:
		return false, false
	}
}

func defaultCommandString(value string, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
