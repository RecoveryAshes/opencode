package integration

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func todoTool(request Request) (Result, error) {
	raw, ok := request.Params["todos"]
	if !ok {
		return Result{}, fmt.Errorf("todos is required")
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return Result{}, fmt.Errorf("encode todos: %w", err)
	}
	var todos []map[string]string
	if err := json.Unmarshal(data, &todos); err != nil {
		return Result{}, fmt.Errorf("todos must be an array of todo objects: %w", err)
	}
	open := 0
	for _, todo := range todos {
		if todo["status"] != "completed" {
			open++
		}
	}
	return Result{
		Title: fmt.Sprintf("%d todos", open),
		Metadata: map[string]any{
			"todos": todos,
		},
		Output: string(mustJSONIndent(todos)),
	}, nil
}

func skillTool(request Request) (Result, error) {
	name, err := requireString(request.Params, "name")
	if err != nil {
		return Result{}, err
	}
	root := resolvePath(request.Directory, ".")
	location, err := findSkill(root, name)
	if err != nil {
		return Result{}, err
	}
	data, err := os.ReadFile(location)
	if err != nil {
		return Result{}, fmt.Errorf("read skill %s: %w", location, err)
	}
	dir := filepath.Dir(location)
	files := sampledFiles(dir, 10)
	output := []string{
		fmt.Sprintf(`<skill_content name="%s">`, name),
		"# Skill: " + name,
		"",
		strings.TrimSpace(string(data)),
		"",
		"Base directory for this skill: " + dir,
		"Relative paths in this skill (e.g., scripts/, reference/) are relative to this base directory.",
		"Note: file list is sampled.",
		"",
		"<skill_files>",
		strings.Join(files, "\n"),
		"</skill_files>",
		"</skill_content>",
	}
	return Result{
		Title: "Loaded skill: " + name,
		Metadata: map[string]any{
			"name": name,
			"dir":  dir,
		},
		Output: strings.Join(output, "\n"),
	}, nil
}

func findSkill(root string, name string) (string, error) {
	candidates := []string{
		filepath.Join(root, ".opencode", "skill", name, "SKILL.md"),
		filepath.Join(root, ".opencode", "skills", name, "SKILL.md"),
		filepath.Join(root, ".opencode", "skill", name+".md"),
		filepath.Join(root, ".opencode", "skills", name+".md"),
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	available := []string{}
	for _, dir := range []string{filepath.Join(root, ".opencode", "skill"), filepath.Join(root, ".opencode", "skills")} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			item := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
			if entry.IsDir() {
				item = entry.Name()
			}
			available = append(available, item)
		}
	}
	return "", fmt.Errorf("skill %q not found. Available skills: %s", name, strings.Join(available, ", "))
}

func sampledFiles(root string, limit int) []string {
	files := []string{}
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || len(files) >= limit {
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if filepath.Base(path) == "SKILL.md" {
			return nil
		}
		files = append(files, "<file>"+path+"</file>")
		return nil
	})
	return files
}

func mustJSONIndent(value any) []byte {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return []byte("[]")
	}
	return data
}
