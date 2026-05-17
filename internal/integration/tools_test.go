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
