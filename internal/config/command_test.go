package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadCommandsParsesFrontmatterAndNestedNames(t *testing.T) {
	root := t.TempDir()
	commandPath := filepath.Join(root, ".opencode", "commands", "review", "pr.md")
	if err := os.MkdirAll(filepath.Dir(commandPath), 0o755); err != nil {
		t.Fatalf("mkdir command dir: %v", err)
	}
	content := strings.Join([]string{
		"---",
		"description: Review a PR",
		"agent: reviewer",
		"model: openrouter/openai/gpt-4o-mini",
		"subtask: true",
		"---",
		"Review $1 with $ARGUMENTS",
	}, "\n")
	if err := os.WriteFile(commandPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write command: %v", err)
	}

	commands, err := LoadCommands(root)
	if err != nil {
		t.Fatalf("LoadCommands() error = %v", err)
	}
	command, ok := commands["review/pr"]
	if !ok {
		t.Fatalf("commands = %#v, want review/pr", commands)
	}
	if command.Description != "Review a PR" || command.Agent != "reviewer" || command.Model != "openrouter/openai/gpt-4o-mini" {
		t.Fatalf("command = %#v, want parsed metadata", command)
	}
	if command.Subtask == nil || *command.Subtask != true {
		t.Fatalf("subtask = %#v, want true", command.Subtask)
	}
	if command.Template != "Review $1 with $ARGUMENTS" {
		t.Fatalf("template = %q", command.Template)
	}
}

func TestExecuteCommandRendersArgumentsAndShell(t *testing.T) {
	root := t.TempDir()
	commandPath := filepath.Join(root, "commands", "ship.md")
	if err := os.MkdirAll(filepath.Dir(commandPath), 0o755); err != nil {
		t.Fatalf("mkdir command dir: %v", err)
	}
	content := strings.Join([]string{
		"---",
		"description: Ship command",
		"agent: build",
		"model: openai-compatible/local-model",
		"---",
		"First=$1",
		"Rest=$2",
		"All=$ARGUMENTS",
		"Shell=!`printf shell-output`",
	}, "\n")
	if err := os.WriteFile(commandPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write command: %v", err)
	}

	result, err := ExecuteCommand(context.Background(), CommandInput{
		Name:      "ship",
		Argument:  `"one arg" two three`,
		Directory: root,
	})
	if err != nil {
		t.Fatalf("ExecuteCommand() error = %v", err)
	}
	if result.Command.Name != "ship" || result.Agent != "build" || result.Provider != "openai-compatible" || result.Model != "local-model" {
		t.Fatalf("result metadata = %#v", result)
	}
	if result.ShellRuns != 1 {
		t.Fatalf("ShellRuns = %d, want 1", result.ShellRuns)
	}
	for _, want := range []string{
		"First=one arg",
		"Rest=two three",
		`All="one arg" two three`,
		"Shell=shell-output",
	} {
		if !strings.Contains(result.Prompt, want) {
			t.Fatalf("prompt = %q, missing %q", result.Prompt, want)
		}
	}
}

func TestRenderCommandTemplateAppendsUnusedArguments(t *testing.T) {
	prompt, _, err := RenderCommandTemplate(context.Background(), "Do the thing", "extra context", t.TempDir())
	if err != nil {
		t.Fatalf("RenderCommandTemplate() error = %v", err)
	}
	if prompt != "Do the thing\n\nextra context" {
		t.Fatalf("prompt = %q, want appended arguments", prompt)
	}
}

func TestExecuteCommandReportsAvailableCommands(t *testing.T) {
	root := t.TempDir()
	commandPath := filepath.Join(root, ".opencode", "command", "known.md")
	if err := os.MkdirAll(filepath.Dir(commandPath), 0o755); err != nil {
		t.Fatalf("mkdir command dir: %v", err)
	}
	if err := os.WriteFile(commandPath, []byte("Known command"), 0o644); err != nil {
		t.Fatalf("write command: %v", err)
	}

	_, err := ExecuteCommand(context.Background(), CommandInput{Name: "missing", Directory: root})
	if err == nil {
		t.Fatalf("ExecuteCommand() error = nil, want missing command error")
	}
	if !strings.Contains(err.Error(), `command not found: "missing"`) || !strings.Contains(err.Error(), "Available commands: known") {
		t.Fatalf("error = %q, want missing command with hint", err.Error())
	}
}
