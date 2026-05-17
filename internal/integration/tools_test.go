package integration

import "testing"

func TestToolInventoryIncludesMigrationTargets(t *testing.T) {
	got := map[string]bool{}
	for _, name := range ToolNames() {
		got[name] = true
	}

	for _, name := range []string{"read", "write", "edit", "apply_patch", "shell", "grep", "lsp", "webfetch", "websearch", "question", "task", "task_status", "skill", "todo", "todowrite", "repo_clone", "repo_overview"} {
		if !got[name] {
			t.Fatalf("tool %q missing from inventory", name)
		}
	}
}

func TestToolSchemasCoverInventory(t *testing.T) {
	for _, tool := range AllTools() {
		schema := ToolSchema(tool.Name)
		if schema["type"] != "object" {
			t.Fatalf("schema for %s = %#v, want object schema", tool.Name, schema)
		}
		if _, ok := schema["properties"].(map[string]any); !ok {
			t.Fatalf("schema for %s = %#v, want properties", tool.Name, schema)
		}
	}
}
