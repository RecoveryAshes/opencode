package integration

// JSONSchema is the small JSON Schema subset needed to advertise migrated tools
// to LLM providers without coupling provider packages to tool implementations.
type JSONSchema map[string]any

// ToolSchema returns the migrated tool's provider-facing input schema.
func ToolSchema(name string) JSONSchema {
	switch CanonicalToolName(name) {
	case "invalid":
		return objectSchema([]string{"tool", "error"}, map[string]any{
			"tool":  stringSchema("Tool name that was called with invalid arguments."),
			"error": stringSchema("Validation error to report back to the model."),
		})
	case "read":
		return objectSchema([]string{"filePath"}, map[string]any{
			"filePath": stringSchema("File or directory path to read, relative to the workspace unless absolute."),
			"offset":   integerSchema("One-based line or entry offset."),
			"limit":    integerSchema("Maximum lines or directory entries to return."),
		})
	case "write":
		return objectSchema([]string{"filePath", "content"}, map[string]any{
			"filePath": stringSchema("File path to write, relative to the workspace unless absolute."),
			"content":  stringSchema("Complete file content to write."),
		})
	case "edit":
		return objectSchema([]string{"filePath", "oldString", "newString"}, map[string]any{
			"filePath":   stringSchema("File path to edit, relative to the workspace unless absolute."),
			"oldString":  stringSchema("Exact text to replace. Use an empty string to create a new file."),
			"newString":  stringSchema("Replacement text."),
			"replaceAll": booleanSchema("Replace every occurrence of oldString."),
		})
	case "apply_patch":
		return objectSchema([]string{"patchText"}, map[string]any{
			"patchText": stringSchema("Patch text using the opencode apply_patch format."),
		})
	case "glob":
		return objectSchema([]string{"pattern"}, map[string]any{
			"pattern": stringSchema("Glob pattern to match file names or relative paths."),
			"path":    stringSchema("Directory to search. Defaults to the workspace root."),
		})
	case "grep":
		return objectSchema([]string{"pattern"}, map[string]any{
			"pattern": stringSchema("Regular expression to search for."),
			"path":    stringSchema("File or directory to search. Defaults to the workspace root."),
			"include": stringSchema("Optional glob filter for matching file paths."),
		})
	case "bash":
		return objectSchema([]string{"command"}, map[string]any{
			"command": stringSchema("Shell command to run in the workspace."),
		})
	case "lsp":
		return objectSchema([]string{"operation", "filePath", "line", "character"}, map[string]any{
			"operation": enumSchema("LSP operation to run.", []string{
				"goToDefinition",
				"findReferences",
				"hover",
				"documentSymbol",
				"workspaceSymbol",
				"goToImplementation",
				"prepareCallHierarchy",
				"incomingCalls",
				"outgoingCalls",
			}),
			"filePath":  stringSchema("File path for document-level operations."),
			"query":     stringSchema("Workspace symbol search query."),
			"line":      integerSchema("One-based line number."),
			"character": integerSchema("One-based character offset."),
		})
	case "webfetch":
		return objectSchema([]string{"url"}, map[string]any{
			"url":     stringSchema("HTTP or HTTPS URL to fetch."),
			"format":  enumSchema("Preferred output format.", []string{"markdown", "text", "html"}),
			"timeout": integerSchema("Request timeout in seconds."),
		})
	case "websearch":
		return objectSchema([]string{"query"}, map[string]any{
			"query":      stringSchema("Search query."),
			"numResults": integerSchema("Requested number of search results."),
		})
	case "question":
		return objectSchema([]string{"questions"}, map[string]any{
			"questions": arraySchema("Questions to ask the user.", objectSchema([]string{"question"}, map[string]any{
				"question": stringSchema("Question text."),
				"header":   stringSchema("Short UI header."),
				"id":       stringSchema("Stable question identifier."),
			})),
			"answers": arraySchema("Optional prefilled answers for non-interactive runs.", map[string]any{"type": "array", "items": stringSchema("Answer value.")}),
		})
	case "task":
		return objectSchema([]string{"description", "prompt", "subagent_type"}, map[string]any{
			"description":   stringSchema("Short task description."),
			"prompt":        stringSchema("Full task prompt for the subagent."),
			"subagent_type": stringSchema("Subagent type to run."),
			"task_id":       stringSchema("Optional stable task identifier."),
			"background":    booleanSchema("Run the task in the background."),
		})
	case "task_status":
		return objectSchema([]string{"task_id"}, map[string]any{
			"task_id":    stringSchema("Task identifier returned by task."),
			"state":      enumSchema("Task state override for fixtures or adapters.", []string{"running", "completed", "error"}),
			"wait":       booleanSchema("Wait for completion if supported."),
			"timeout_ms": integerSchema("Maximum wait time in milliseconds."),
		})
	case "skill":
		return objectSchema([]string{"name"}, map[string]any{
			"name": stringSchema("Skill name to load."),
		})
	case "todowrite":
		return objectSchema([]string{"todos"}, map[string]any{
			"todos": arraySchema("Updated todo list.", objectSchema([]string{"content", "status"}, map[string]any{
				"content":  stringSchema("Todo item content."),
				"status":   enumSchema("Todo status.", []string{"pending", "in_progress", "completed"}),
				"priority": enumSchema("Todo priority.", []string{"high", "medium", "low"}),
				"id":       stringSchema("Optional stable todo identifier."),
			})),
		})
	case "repo_clone":
		return objectSchema([]string{"repository"}, map[string]any{
			"repository": stringSchema("Git repository URL or host/owner/name reference."),
			"branch":     stringSchema("Optional branch to checkout."),
			"refresh":    booleanSchema("Fetch and refresh an existing cached clone."),
		})
	case "repo_overview":
		return objectSchema([]string{}, map[string]any{
			"repository": stringSchema("Git repository URL or host/owner/name reference."),
			"path":       stringSchema("Local repository path."),
			"depth":      integerSchema("Directory tree depth from 1 to 6."),
		})
	case "plan_exit":
		return objectSchema([]string{}, map[string]any{
			"plan": stringSchema("Optional relative plan file path."),
		})
	default:
		return objectSchema([]string{}, map[string]any{})
	}
}

func objectSchema(required []string, properties map[string]any) JSONSchema {
	schema := JSONSchema{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func stringSchema(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func integerSchema(description string) map[string]any {
	return map[string]any{"type": "integer", "description": description}
}

func booleanSchema(description string) map[string]any {
	return map[string]any{"type": "boolean", "description": description}
}

func arraySchema(description string, items map[string]any) map[string]any {
	return map[string]any{"type": "array", "description": description, "items": items}
}

func enumSchema(description string, values []string) map[string]any {
	return map[string]any{"type": "string", "description": description, "enum": values}
}
