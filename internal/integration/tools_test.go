package integration

import "testing"

func TestToolInventoryMatchesTypeScriptRegistryOrder(t *testing.T) {
	want := []string{
		"invalid",
		"question",
		"bash",
		"read",
		"glob",
		"grep",
		"edit",
		"write",
		"task",
		"task_status",
		"webfetch",
		"todowrite",
		"websearch",
		"repo_clone",
		"repo_overview",
		"skill",
		"apply_patch",
		"lsp",
		"plan_exit",
	}
	got := ToolNames()
	if len(got) != len(want) {
		t.Fatalf("ToolNames() = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ToolNames()[%d] = %q, want %q; all names = %#v", i, got[i], want[i], got)
		}
	}
}

func TestToolAliasesCanonicalizeLegacyGoNames(t *testing.T) {
	tests := map[string]string{
		"shell": "bash",
		"bash":  "bash",
		"todo":  "todowrite",
		"patch": "apply_patch",
	}
	for input, want := range tests {
		if got := CanonicalToolName(input); got != want {
			t.Fatalf("CanonicalToolName(%q) = %q, want %q", input, got, want)
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

func TestLSPSchemaMatchesTypeScriptOperationSet(t *testing.T) {
	schema := ToolSchema("lsp")
	props := schema["properties"].(map[string]any)
	operation := props["operation"].(map[string]any)
	got := operation["enum"].([]string)
	want := []string{
		"goToDefinition",
		"findReferences",
		"hover",
		"documentSymbol",
		"workspaceSymbol",
		"goToImplementation",
		"prepareCallHierarchy",
		"incomingCalls",
		"outgoingCalls",
	}
	if len(got) != len(want) {
		t.Fatalf("lsp operation enum = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("lsp operation enum[%d] = %q, want %q; all = %#v", i, got[i], want[i], got)
		}
	}
}
