package llm

import "testing"

func TestProviderInventoryIncludesMigrationTargets(t *testing.T) {
	got := map[string]bool{}
	for _, id := range ProviderIDs() {
		got[id] = true
	}

	for _, id := range []string{"openai", "anthropic", "google", "azure", "amazon-bedrock", "openrouter", "github-copilot", "openai-compatible"} {
		if !got[id] {
			t.Fatalf("provider %q missing from inventory", id)
		}
	}
}
