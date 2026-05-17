package integration

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func invalidTool(request Request) (Result, error) {
	toolName, err := requireString(request.Params, "tool")
	if err != nil {
		return Result{}, err
	}
	message, err := requireString(request.Params, "error")
	if err != nil {
		return Result{}, err
	}
	return Result{
		Title: "Invalid Tool",
		Metadata: map[string]any{
			"tool":  toolName,
			"error": message,
		},
		Output: "The arguments provided to the tool are invalid: " + message,
	}, nil
}

func planExitTool(request Request) (Result, error) {
	plan := optionalString(request.Params, "plan", "")
	output := "User approved switching to build agent. Wait for further instructions."
	if plan != "" {
		output = fmt.Sprintf("Plan at %s is complete. %s", plan, output)
	}
	return Result{
		Title: "Switching to build agent",
		Metadata: map[string]any{
			"plan": plan,
		},
		Output: output,
	}, nil
}

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

func questionTool(request Request) (Result, error) {
	raw, ok := request.Params["questions"]
	if !ok {
		return Result{}, fmt.Errorf("questions is required")
	}
	var questions []questionPrompt
	if err := remarshal(raw, &questions); err != nil {
		return Result{}, fmt.Errorf("questions must be an array of question objects: %w", err)
	}
	if len(questions) == 0 {
		return Result{}, fmt.Errorf("questions must include at least one question")
	}
	answers := questionAnswers(request.Params["answers"], len(questions))
	formatted := make([]string, 0, len(questions))
	for i, question := range questions {
		answer := "Unanswered"
		if i < len(answers) && len(answers[i]) > 0 {
			answer = strings.Join(answers[i], ", ")
		}
		formatted = append(formatted, fmt.Sprintf("%q=%q", question.Question, answer))
	}
	return Result{
		Title: fmt.Sprintf("Asked %d question%s", len(questions), pluralS(len(questions))),
		Metadata: map[string]any{
			"questions": questions,
			"answers":   answers,
		},
		Output: "User has answered your questions: " + strings.Join(formatted, ", ") + ". You can now continue with the user's answers in mind.",
	}, nil
}

func taskTool(request Request) (Result, error) {
	description, err := requireString(request.Params, "description")
	if err != nil {
		return Result{}, err
	}
	prompt, err := requireString(request.Params, "prompt")
	if err != nil {
		return Result{}, err
	}
	subagentType, err := requireString(request.Params, "subagent_type")
	if err != nil {
		return Result{}, err
	}
	taskID := optionalString(request.Params, "task_id", stableTaskID(description, subagentType, prompt))
	if optionalBool(request.Params, "background", false) {
		return Result{
			Title: description,
			Metadata: map[string]any{
				"task_id":       taskID,
				"subagent_type": subagentType,
				"state":         "running",
				"background":    true,
			},
			Output: strings.Join([]string{
				fmt.Sprintf("task_id: %s (for polling this task with task_status)", taskID),
				"state: running",
				"",
				"<task_result>",
				"Background task started. Continue your current work and call task_status when you need the result.",
				"</task_result>",
			}, "\n"),
		}, nil
	}
	resultText := optionalString(request.Params, "result", "Task execution is handled by the Go session runtime when subagent prompt orchestration is attached.")
	return Result{
		Title: description,
		Metadata: map[string]any{
			"task_id":       taskID,
			"subagent_type": subagentType,
			"prompt":        prompt,
		},
		Output: taskOutput(taskID, resultText),
	}, nil
}

func taskStatusTool(request Request) (Result, error) {
	taskID, err := requireString(request.Params, "task_id")
	if err != nil {
		return Result{}, err
	}
	state := optionalString(request.Params, "state", "completed")
	text := optionalString(request.Params, "result", optionalString(request.Params, "text", "Task status is resolved by the Go background job runtime when attached."))
	if optionalBool(request.Params, "wait", false) && state == "running" {
		timeout := optionalInt(request.Params, "timeout_ms", 60_000)
		text = fmt.Sprintf("Timed out after %dms while waiting for task completion.", timeout)
	}
	tag := "task_error"
	if state == "completed" || state == "running" {
		tag = "task_result"
	}
	return Result{
		Title: "Task status",
		Metadata: map[string]any{
			"task_id": taskID,
			"state":   state,
		},
		Output: strings.Join([]string{
			"task_id: " + taskID,
			"state: " + state,
			"",
			"<" + tag + ">",
			text,
			"</" + tag + ">",
		}, "\n"),
	}, nil
}

func skillTool(request Request) (Result, error) {
	name, err := requireString(request.Params, "name")
	if err != nil {
		return Result{}, err
	}
	root := resolvePath(request.Directory, ".")
	skill, err := findSkill(root, name)
	if err != nil {
		return Result{}, err
	}
	dir := filepath.Dir(skill.Location)
	files := sampledFiles(dir, 10)
	output := []string{
		fmt.Sprintf(`<skill_content name="%s">`, skill.Name),
		"# Skill: " + skill.Name,
		"",
		strings.TrimSpace(skill.Content),
		"",
		"Base directory for this skill: " + fileURLString(dir),
		"Relative paths in this skill (e.g., scripts/, reference/) are relative to this base directory.",
		"Note: file list is sampled.",
		"",
		"<skill_files>",
		strings.Join(files, "\n"),
		"</skill_files>",
		"</skill_content>",
	}
	return Result{
		Title: "Loaded skill: " + skill.Name,
		Metadata: map[string]any{
			"name": skill.Name,
			"dir":  dir,
		},
		Output: strings.Join(output, "\n"),
	}, nil
}

func findSkill(root string, name string) (SkillInfo, error) {
	skills, err := ListSkills(root)
	if err != nil {
		return SkillInfo{}, err
	}
	available := []string{}
	for _, skill := range skills {
		if skill.Name == name {
			return skill, nil
		}
		available = append(available, skill.Name)
	}
	text := strings.Join(available, ", ")
	if text == "" {
		text = "none"
	}
	return SkillInfo{}, fmt.Errorf("skill %q not found. Available skills: %s", name, text)
}

func fileURLString(path string) string {
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
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

type questionPrompt struct {
	Question    string           `json:"question"`
	Options     []questionOption `json:"options,omitempty"`
	Suggestions []string         `json:"suggestions,omitempty"`
}

type questionOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

func questionAnswers(raw any, count int) [][]string {
	result := make([][]string, count)
	if raw == nil {
		return result
	}
	switch value := raw.(type) {
	case []string:
		if count > 0 {
			result[0] = append(result[0], value...)
		}
	case []any:
		for i, item := range value {
			if i >= count {
				break
			}
			result[i] = stringsFromAny(item)
		}
	case map[string]any:
		for i := range count {
			result[i] = stringsFromAny(value[strconv.Itoa(i)])
		}
	}
	return result
}

func stringsFromAny(value any) []string {
	switch typed := value.(type) {
	case string:
		if typed == "" {
			return nil
		}
		return []string{typed}
	case []string:
		return append([]string(nil), typed...)
	case []any:
		out := []string{}
		for _, item := range typed {
			if text, ok := item.(string); ok && text != "" {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func taskOutput(taskID string, text string) string {
	return strings.Join([]string{
		fmt.Sprintf("task_id: %s (for resuming to continue this task if needed)", taskID),
		"",
		"<task_result>",
		text,
		"</task_result>",
	}, "\n")
}

func stableTaskID(description string, subagentType string, prompt string) string {
	seed := description + "\x00" + subagentType + "\x00" + prompt
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(seed))
	return fmt.Sprintf("task_%x", hash.Sum64())
}

func pluralS(count int) string {
	if count == 1 {
		return ""
	}
	return "s"
}

func remarshal(input any, output any) error {
	data, err := json.Marshal(input)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, output)
}
