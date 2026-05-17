package integration

import "testing"

func TestToolInventoryIncludesMigrationTargets(t *testing.T) {
	got := map[string]bool{}
	for _, name := range ToolNames() {
		got[name] = true
	}

	for _, name := range []string{"read", "write", "edit", "apply_patch", "shell", "grep", "lsp", "webfetch", "websearch", "skill"} {
		if !got[name] {
			t.Fatalf("tool %q missing from inventory", name)
		}
	}
}
