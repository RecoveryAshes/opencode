// Package llm defines provider contracts for the Go runtime.
package llm

// Provider describes an LLM provider that must be owned by the Go runtime.
type Provider struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Protocols []string `json:"protocols"`
}

// AllProviders returns the provider inventory required for the migration.
func AllProviders() []Provider {
	return append([]Provider(nil), providers...)
}

// ProviderIDs returns provider IDs in stable CLI order.
func ProviderIDs() []string {
	result := make([]string, 0, len(providers))
	for _, provider := range providers {
		result = append(result, provider.ID)
	}
	return result
}

var providers = []Provider{
	{ID: "openai", Name: "OpenAI", Protocols: []string{"responses", "chat-completions"}},
	{ID: "anthropic", Name: "Anthropic", Protocols: []string{"messages"}},
	{ID: "google", Name: "Gemini", Protocols: []string{"generate-content"}},
	{ID: "google-vertex", Name: "Vertex AI", Protocols: []string{"generate-content"}},
	{ID: "azure", Name: "Azure OpenAI", Protocols: []string{"responses", "chat-completions"}},
	{ID: "amazon-bedrock", Name: "Amazon Bedrock", Protocols: []string{"converse", "invoke-model"}},
	{ID: "openrouter", Name: "OpenRouter", Protocols: []string{"openai-compatible"}},
	{ID: "xai", Name: "xAI", Protocols: []string{"openai-compatible"}},
	{ID: "groq", Name: "Groq", Protocols: []string{"openai-compatible"}},
	{ID: "mistral", Name: "Mistral", Protocols: []string{"openai-compatible"}},
	{ID: "cohere", Name: "Cohere", Protocols: []string{"chat"}},
	{ID: "cerebras", Name: "Cerebras", Protocols: []string{"openai-compatible"}},
	{ID: "deepinfra", Name: "DeepInfra", Protocols: []string{"openai-compatible"}},
	{ID: "together", Name: "Together AI", Protocols: []string{"openai-compatible"}},
	{ID: "alibaba", Name: "Alibaba", Protocols: []string{"openai-compatible"}},
	{ID: "vercel", Name: "Vercel AI Gateway", Protocols: []string{"ai-gateway"}},
	{ID: "github-copilot", Name: "GitHub Copilot", Protocols: []string{"openai-compatible"}},
	{ID: "digitalocean", Name: "DigitalOcean", Protocols: []string{"openai-compatible"}},
	{ID: "gitlab-duo", Name: "GitLab Duo", Protocols: []string{"openai-compatible"}},
	{ID: "venice", Name: "Venice", Protocols: []string{"openai-compatible"}},
	{ID: "openai-compatible", Name: "OpenAI Compatible", Protocols: []string{"openai-compatible"}},
}
