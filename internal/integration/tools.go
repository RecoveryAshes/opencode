// Package integration defines local tool, MCP, LSP, PTY, and filesystem
// integration contracts for the Go runtime.
package integration

// Tool describes a migrated opencode tool surface.
type Tool struct {
	Name     string `json:"name"`
	Category string `json:"category"`
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

var tools = []Tool{
	{Name: "read", Category: "filesystem"},
	{Name: "write", Category: "filesystem"},
	{Name: "edit", Category: "filesystem"},
	{Name: "apply_patch", Category: "filesystem"},
	{Name: "shell", Category: "process"},
	{Name: "glob", Category: "search"},
	{Name: "grep", Category: "search"},
	{Name: "lsp", Category: "language-server"},
	{Name: "task", Category: "agent"},
	{Name: "task_status", Category: "agent"},
	{Name: "webfetch", Category: "network"},
	{Name: "websearch", Category: "network"},
	{Name: "question", Category: "user-input"},
	{Name: "todo", Category: "session"},
	{Name: "skill", Category: "plugin"},
	{Name: "repo_clone", Category: "git"},
	{Name: "repo_overview", Category: "git"},
}
