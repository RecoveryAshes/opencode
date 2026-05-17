// Package llm defines provider contracts for the Go runtime.
package llm

import "fmt"

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

// ResolveChatRequest maps a provider/model pair to the first Go-native chat
// route. Providers with distinct protocols are listed but return explicit
// errors until their native wire clients are migrated.
func ResolveChatRequest(messages []Message, providerID string, modelID string) (ChatRequest, error) {
	providerID = defaultString(providerID, "openai-compatible")
	if profile, ok := openAICompatibleProfiles[providerID]; ok {
		request := profile.chatRequest(messages, modelID)
		if request.BaseURL == "" {
			return ChatRequest{}, fmt.Errorf("%s provider requires a base URL environment variable", providerID)
		}
		return request, nil
	}
	switch providerID {
	case "openai":
		profile := openAIProfile{
			ProviderID:     "openai",
			DefaultBaseURL: defaultOpenAICompatibleBaseURL,
			BaseURLEnvVars: []string{"OPENCODE_OPENAI_BASE_URL", "OPENAI_BASE_URL"},
			APIKeyEnvVars:  []string{"OPENCODE_OPENAI_API_KEY", "OPENAI_API_KEY"},
			ModelEnvVars:   []string{"OPENCODE_OPENAI_MODEL", "OPENAI_MODEL"},
			DefaultModel:   "gpt-4o-mini",
			AuthHeader:     "Authorization",
			AuthScheme:     "Bearer",
		}
		return profile.chatRequest(messages, modelID), nil
	case "azure":
		profile := openAIProfile{
			ProviderID:     "azure",
			DefaultBaseURL: azureBaseURL(),
			BaseURLEnvVars: []string{"OPENCODE_AZURE_OPENAI_BASE_URL", "AZURE_OPENAI_BASE_URL"},
			APIKeyEnvVars:  []string{"OPENCODE_AZURE_OPENAI_API_KEY", "AZURE_OPENAI_API_KEY"},
			ModelEnvVars:   []string{"OPENCODE_AZURE_OPENAI_MODEL", "AZURE_OPENAI_MODEL"},
			DefaultModel:   "gpt-4o-mini",
			AuthHeader:     "api-key",
			QueryParams:    map[string]string{"api-version": defaultString(firstEnv("OPENCODE_AZURE_OPENAI_API_VERSION", "AZURE_OPENAI_API_VERSION"), "v1")},
		}
		if profile.DefaultBaseURL == "" {
			return ChatRequest{}, fmt.Errorf("azure provider requires AZURE_OPENAI_BASE_URL or AZURE_OPENAI_RESOURCE_NAME")
		}
		return profile.chatRequest(messages, modelID), nil
	case "anthropic":
		profile := anthropicProfile{
			ProviderID:     "anthropic",
			DefaultBaseURL: "https://api.anthropic.com/v1",
			BaseURLEnvVars: []string{"OPENCODE_ANTHROPIC_BASE_URL", "ANTHROPIC_BASE_URL"},
			APIKeyEnvVars:  []string{"OPENCODE_ANTHROPIC_API_KEY", "ANTHROPIC_API_KEY"},
			ModelEnvVars:   []string{"OPENCODE_ANTHROPIC_MODEL", "ANTHROPIC_MODEL"},
			DefaultModel:   "claude-sonnet-4-5",
			Headers:        map[string]string{"anthropic-version": "2023-06-01"},
		}
		return profile.chatRequest(messages, modelID), nil
	case "google", "google-vertex", "amazon-bedrock", "cohere", "vercel":
		return ChatRequest{}, fmt.Errorf("%s provider uses a non-OpenAI chat protocol that has not been migrated yet", providerID)
	default:
		return ChatRequest{}, fmt.Errorf("unknown provider %q", providerID)
	}
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

type openAIProfile struct {
	ProviderID     string
	DefaultBaseURL string
	BaseURLEnvVars []string
	APIKeyEnvVars  []string
	ModelEnvVars   []string
	DefaultModel   string
	AuthHeader     string
	AuthScheme     string
	Headers        map[string]string
	QueryParams    map[string]string
}

func (profile openAIProfile) chatRequest(messages []Message, modelID string) ChatRequest {
	return ChatRequest{
		ProviderID:  profile.ProviderID,
		Protocol:    "openai-compatible",
		BaseURL:     defaultString(firstEnv(profile.BaseURLEnvVars...), profile.DefaultBaseURL),
		APIKey:      firstEnv(profile.APIKeyEnvVars...),
		AuthHeader:  profile.AuthHeader,
		AuthScheme:  profile.AuthScheme,
		Headers:     cloneStringMap(profile.Headers),
		QueryParams: cloneStringMap(profile.QueryParams),
		Model:       defaultString(modelID, defaultString(firstEnv(profile.ModelEnvVars...), profile.DefaultModel)),
		Messages:    messages,
		Temperature: nil,
		MaxTokens:   nil,
	}
}

type anthropicProfile struct {
	ProviderID     string
	DefaultBaseURL string
	BaseURLEnvVars []string
	APIKeyEnvVars  []string
	ModelEnvVars   []string
	DefaultModel   string
	Headers        map[string]string
}

func (profile anthropicProfile) chatRequest(messages []Message, modelID string) ChatRequest {
	return ChatRequest{
		ProviderID: profile.ProviderID,
		Protocol:   "anthropic-messages",
		BaseURL:    defaultString(firstEnv(profile.BaseURLEnvVars...), profile.DefaultBaseURL),
		APIKey:     firstEnv(profile.APIKeyEnvVars...),
		AuthHeader: "x-api-key",
		Headers:    cloneStringMap(profile.Headers),
		Model:      defaultString(modelID, defaultString(firstEnv(profile.ModelEnvVars...), profile.DefaultModel)),
		Messages:   messages,
	}
}

var openAICompatibleProfiles = map[string]openAIProfile{
	"openai-compatible": {
		ProviderID:     "openai-compatible",
		DefaultBaseURL: defaultOpenAICompatibleBaseURL,
		BaseURLEnvVars: []string{"OPENCODE_OPENAI_COMPATIBLE_BASE_URL", "OPENAI_BASE_URL"},
		APIKeyEnvVars:  []string{"OPENCODE_OPENAI_COMPATIBLE_API_KEY", "OPENAI_API_KEY"},
		ModelEnvVars:   []string{"OPENCODE_OPENAI_COMPATIBLE_MODEL", "OPENAI_MODEL"},
		DefaultModel:   "gpt-4o-mini",
		AuthHeader:     "Authorization",
		AuthScheme:     "Bearer",
	},
	"openrouter": {
		ProviderID:     "openrouter",
		DefaultBaseURL: "https://openrouter.ai/api/v1",
		BaseURLEnvVars: []string{"OPENCODE_OPENROUTER_BASE_URL", "OPENROUTER_BASE_URL"},
		APIKeyEnvVars:  []string{"OPENCODE_OPENROUTER_API_KEY", "OPENROUTER_API_KEY"},
		ModelEnvVars:   []string{"OPENCODE_OPENROUTER_MODEL", "OPENROUTER_MODEL"},
		AuthHeader:     "Authorization",
		AuthScheme:     "Bearer",
	},
	"xai": {
		ProviderID:     "xai",
		DefaultBaseURL: "https://api.x.ai/v1",
		BaseURLEnvVars: []string{"OPENCODE_XAI_BASE_URL", "XAI_BASE_URL"},
		APIKeyEnvVars:  []string{"OPENCODE_XAI_API_KEY", "XAI_API_KEY"},
		ModelEnvVars:   []string{"OPENCODE_XAI_MODEL", "XAI_MODEL"},
		AuthHeader:     "Authorization",
		AuthScheme:     "Bearer",
	},
	"groq": {
		ProviderID:     "groq",
		DefaultBaseURL: "https://api.groq.com/openai/v1",
		BaseURLEnvVars: []string{"OPENCODE_GROQ_BASE_URL", "GROQ_BASE_URL"},
		APIKeyEnvVars:  []string{"OPENCODE_GROQ_API_KEY", "GROQ_API_KEY"},
		ModelEnvVars:   []string{"OPENCODE_GROQ_MODEL", "GROQ_MODEL"},
		AuthHeader:     "Authorization",
		AuthScheme:     "Bearer",
	},
	"mistral": {
		ProviderID:     "mistral",
		DefaultBaseURL: "https://api.mistral.ai/v1",
		BaseURLEnvVars: []string{"OPENCODE_MISTRAL_BASE_URL", "MISTRAL_BASE_URL"},
		APIKeyEnvVars:  []string{"OPENCODE_MISTRAL_API_KEY", "MISTRAL_API_KEY"},
		ModelEnvVars:   []string{"OPENCODE_MISTRAL_MODEL", "MISTRAL_MODEL"},
		AuthHeader:     "Authorization",
		AuthScheme:     "Bearer",
	},
	"cerebras": {
		ProviderID:     "cerebras",
		DefaultBaseURL: "https://api.cerebras.ai/v1",
		BaseURLEnvVars: []string{"OPENCODE_CEREBRAS_BASE_URL", "CEREBRAS_BASE_URL"},
		APIKeyEnvVars:  []string{"OPENCODE_CEREBRAS_API_KEY", "CEREBRAS_API_KEY"},
		ModelEnvVars:   []string{"OPENCODE_CEREBRAS_MODEL", "CEREBRAS_MODEL"},
		AuthHeader:     "Authorization",
		AuthScheme:     "Bearer",
	},
	"deepinfra": {
		ProviderID:     "deepinfra",
		DefaultBaseURL: "https://api.deepinfra.com/v1/openai",
		BaseURLEnvVars: []string{"OPENCODE_DEEPINFRA_BASE_URL", "DEEPINFRA_BASE_URL"},
		APIKeyEnvVars:  []string{"OPENCODE_DEEPINFRA_API_KEY", "DEEPINFRA_API_KEY"},
		ModelEnvVars:   []string{"OPENCODE_DEEPINFRA_MODEL", "DEEPINFRA_MODEL"},
		AuthHeader:     "Authorization",
		AuthScheme:     "Bearer",
	},
	"together": {
		ProviderID:     "together",
		DefaultBaseURL: "https://api.together.xyz/v1",
		BaseURLEnvVars: []string{"OPENCODE_TOGETHER_BASE_URL", "TOGETHER_BASE_URL", "TOGETHER_AI_BASE_URL"},
		APIKeyEnvVars:  []string{"OPENCODE_TOGETHER_API_KEY", "TOGETHER_API_KEY", "TOGETHER_AI_API_KEY"},
		ModelEnvVars:   []string{"OPENCODE_TOGETHER_MODEL", "TOGETHER_MODEL", "TOGETHER_AI_MODEL"},
		AuthHeader:     "Authorization",
		AuthScheme:     "Bearer",
	},
	"togetherai": {
		ProviderID:     "togetherai",
		DefaultBaseURL: "https://api.together.xyz/v1",
		BaseURLEnvVars: []string{
			"OPENCODE_TOGETHERAI_BASE_URL",
			"OPENCODE_TOGETHER_BASE_URL",
			"TOGETHER_AI_BASE_URL",
			"TOGETHER_BASE_URL",
		},
		APIKeyEnvVars: []string{
			"OPENCODE_TOGETHERAI_API_KEY",
			"OPENCODE_TOGETHER_API_KEY",
			"TOGETHER_AI_API_KEY",
			"TOGETHER_API_KEY",
		},
		ModelEnvVars: []string{
			"OPENCODE_TOGETHERAI_MODEL",
			"OPENCODE_TOGETHER_MODEL",
			"TOGETHER_AI_MODEL",
			"TOGETHER_MODEL",
		},
		AuthHeader: "Authorization",
		AuthScheme: "Bearer",
	},
	"alibaba": {
		ProviderID:     "alibaba",
		DefaultBaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1",
		BaseURLEnvVars: []string{"OPENCODE_ALIBABA_BASE_URL", "ALIBABA_BASE_URL", "DASHSCOPE_BASE_URL"},
		APIKeyEnvVars:  []string{"OPENCODE_ALIBABA_API_KEY", "ALIBABA_API_KEY", "DASHSCOPE_API_KEY"},
		ModelEnvVars:   []string{"OPENCODE_ALIBABA_MODEL", "ALIBABA_MODEL", "DASHSCOPE_MODEL"},
		AuthHeader:     "Authorization",
		AuthScheme:     "Bearer",
	},
	"github-copilot": {
		ProviderID:     "github-copilot",
		DefaultBaseURL: "",
		BaseURLEnvVars: []string{"OPENCODE_GITHUB_COPILOT_BASE_URL", "GITHUB_COPILOT_BASE_URL"},
		APIKeyEnvVars:  []string{"OPENCODE_GITHUB_COPILOT_API_KEY", "GITHUB_COPILOT_API_KEY", "GITHUB_TOKEN"},
		ModelEnvVars:   []string{"OPENCODE_GITHUB_COPILOT_MODEL", "GITHUB_COPILOT_MODEL"},
		AuthHeader:     "Authorization",
		AuthScheme:     "Bearer",
	},
	"digitalocean": {
		ProviderID:     "digitalocean",
		DefaultBaseURL: "https://inference.do-ai.run/v1",
		BaseURLEnvVars: []string{"OPENCODE_DIGITALOCEAN_BASE_URL", "DIGITALOCEAN_BASE_URL"},
		APIKeyEnvVars:  []string{"OPENCODE_DIGITALOCEAN_API_KEY", "DIGITALOCEAN_API_KEY", "DIGITALOCEAN_ACCESS_TOKEN"},
		ModelEnvVars:   []string{"OPENCODE_DIGITALOCEAN_MODEL", "DIGITALOCEAN_MODEL"},
		AuthHeader:     "Authorization",
		AuthScheme:     "Bearer",
	},
	"gitlab-duo": {
		ProviderID:     "gitlab-duo",
		DefaultBaseURL: "",
		BaseURLEnvVars: []string{"OPENCODE_GITLAB_DUO_BASE_URL", "GITLAB_DUO_BASE_URL"},
		APIKeyEnvVars:  []string{"OPENCODE_GITLAB_DUO_API_KEY", "GITLAB_DUO_API_KEY"},
		ModelEnvVars:   []string{"OPENCODE_GITLAB_DUO_MODEL", "GITLAB_DUO_MODEL"},
		AuthHeader:     "Authorization",
		AuthScheme:     "Bearer",
	},
	"venice": {
		ProviderID:     "venice",
		DefaultBaseURL: "https://api.venice.ai/api/v1",
		BaseURLEnvVars: []string{"OPENCODE_VENICE_BASE_URL", "VENICE_BASE_URL"},
		APIKeyEnvVars:  []string{"OPENCODE_VENICE_API_KEY", "VENICE_API_KEY"},
		ModelEnvVars:   []string{"OPENCODE_VENICE_MODEL", "VENICE_MODEL"},
		AuthHeader:     "Authorization",
		AuthScheme:     "Bearer",
	},
}

func azureBaseURL() string {
	if resource := firstEnv("OPENCODE_AZURE_OPENAI_RESOURCE_NAME", "AZURE_OPENAI_RESOURCE_NAME"); resource != "" {
		return "https://" + resource + ".openai.azure.com/openai/v1"
	}
	return ""
}

func cloneStringMap(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}
