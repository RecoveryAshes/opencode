// Package llm defines provider contracts for the Go runtime.
package llm

import (
	"fmt"
	"net/url"
)

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
		profile := responsesProfile{
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
		profile := responsesProfile{
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
	case "google":
		profile := geminiProfile{
			ProviderID:     "google",
			DefaultBaseURL: "https://generativelanguage.googleapis.com/v1beta",
			BaseURLEnvVars: []string{"OPENCODE_GOOGLE_BASE_URL", "GOOGLE_GENERATIVE_AI_BASE_URL", "GEMINI_BASE_URL"},
			APIKeyEnvVars: []string{
				"OPENCODE_GOOGLE_GENERATIVE_AI_API_KEY",
				"GOOGLE_GENERATIVE_AI_API_KEY",
				"GEMINI_API_KEY",
			},
			ModelEnvVars: []string{"OPENCODE_GOOGLE_MODEL", "GOOGLE_GENERATIVE_AI_MODEL", "GEMINI_MODEL"},
			DefaultModel: "gemini-2.5-flash",
		}
		return profile.chatRequest(messages, modelID), nil
	case "amazon-bedrock":
		profile := bedrockProfile{
			ProviderID:     "amazon-bedrock",
			DefaultBaseURL: bedrockBaseURL(),
			BaseURLEnvVars: []string{"OPENCODE_BEDROCK_BASE_URL", "BEDROCK_BASE_URL"},
			APIKeyEnvVars:  []string{"OPENCODE_AWS_BEARER_TOKEN_BEDROCK", "AWS_BEARER_TOKEN_BEDROCK"},
			ModelEnvVars:   []string{"OPENCODE_BEDROCK_MODEL", "BEDROCK_MODEL_ID"},
			DefaultModel:   "us.amazon.nova-micro-v1:0",
		}
		request := profile.chatRequest(messages, modelID)
		if request.APIKey == "" {
			return ChatRequest{}, fmt.Errorf("amazon-bedrock provider requires AWS_BEARER_TOKEN_BEDROCK until SigV4 signing is migrated")
		}
		return request, nil
	case "cloudflare-ai-gateway":
		profile := openAIProfile{
			ProviderID:     "cloudflare-ai-gateway",
			DefaultBaseURL: cloudflareAIGatewayBaseURL(),
			BaseURLEnvVars: []string{"OPENCODE_CLOUDFLARE_AI_GATEWAY_BASE_URL", "CLOUDFLARE_AI_GATEWAY_BASE_URL"},
			APIKeyEnvVars: []string{
				"OPENCODE_CLOUDFLARE_PROVIDER_API_KEY",
				"CLOUDFLARE_PROVIDER_API_KEY",
				"OPENAI_API_KEY",
			},
			ModelEnvVars: []string{"OPENCODE_CLOUDFLARE_AI_GATEWAY_MODEL", "CLOUDFLARE_AI_GATEWAY_MODEL"},
			AuthHeader:   "Authorization",
			AuthScheme:   "Bearer",
			Headers:      cloudflareAIGatewayHeaders(),
		}
		request := profile.chatRequest(messages, modelID)
		if request.BaseURL == "" {
			return ChatRequest{}, fmt.Errorf("cloudflare-ai-gateway provider requires CLOUDFLARE_ACCOUNT_ID or CLOUDFLARE_AI_GATEWAY_BASE_URL")
		}
		return request, nil
	case "cloudflare-workers-ai":
		profile := openAIProfile{
			ProviderID:     "cloudflare-workers-ai",
			DefaultBaseURL: cloudflareWorkersAIBaseURL(),
			BaseURLEnvVars: []string{"OPENCODE_CLOUDFLARE_WORKERS_AI_BASE_URL", "CLOUDFLARE_WORKERS_AI_BASE_URL"},
			APIKeyEnvVars: []string{
				"OPENCODE_CLOUDFLARE_WORKERS_AI_TOKEN",
				"CLOUDFLARE_API_KEY",
				"CLOUDFLARE_WORKERS_AI_TOKEN",
			},
			ModelEnvVars: []string{"OPENCODE_CLOUDFLARE_WORKERS_AI_MODEL", "CLOUDFLARE_WORKERS_AI_MODEL"},
			AuthHeader:   "Authorization",
			AuthScheme:   "Bearer",
		}
		request := profile.chatRequest(messages, modelID)
		if request.BaseURL == "" {
			return ChatRequest{}, fmt.Errorf("cloudflare-workers-ai provider requires CLOUDFLARE_ACCOUNT_ID or CLOUDFLARE_WORKERS_AI_BASE_URL")
		}
		return request, nil
	case "cohere":
		profile := cohereProfile{
			ProviderID:     "cohere",
			DefaultBaseURL: "https://api.cohere.com/v2",
			BaseURLEnvVars: []string{"OPENCODE_COHERE_BASE_URL", "COHERE_BASE_URL"},
			APIKeyEnvVars:  []string{"OPENCODE_COHERE_API_KEY", "COHERE_API_KEY"},
			ModelEnvVars:   []string{"OPENCODE_COHERE_MODEL", "COHERE_MODEL"},
			DefaultModel:   "command-a-03-2025",
		}
		return profile.chatRequest(messages, modelID), nil
	case "vercel":
		profile := openAIProfile{
			ProviderID:     "vercel",
			DefaultBaseURL: "https://ai-gateway.vercel.sh/v3/ai",
			BaseURLEnvVars: []string{"OPENCODE_VERCEL_BASE_URL", "VERCEL_BASE_URL", "AI_GATEWAY_BASE_URL"},
			APIKeyEnvVars:  []string{"OPENCODE_VERCEL_API_KEY", "VERCEL_API_KEY", "AI_GATEWAY_API_KEY"},
			ModelEnvVars:   []string{"OPENCODE_VERCEL_MODEL", "VERCEL_MODEL", "AI_GATEWAY_MODEL"},
			AuthHeader:     "Authorization",
			AuthScheme:     "Bearer",
			Headers: map[string]string{
				"http-referer": "https://opencode.ai/",
				"x-title":      "opencode",
			},
		}
		return profile.chatRequest(messages, modelID), nil
	case "google-vertex":
		return ChatRequest{}, fmt.Errorf("%s provider uses a non-OpenAI chat protocol that has not been migrated yet", providerID)
	default:
		return ChatRequest{}, fmt.Errorf("unknown provider %q", providerID)
	}
}

type cohereProfile struct {
	ProviderID     string
	DefaultBaseURL string
	BaseURLEnvVars []string
	APIKeyEnvVars  []string
	ModelEnvVars   []string
	DefaultModel   string
}

func (profile cohereProfile) chatRequest(messages []Message, modelID string) ChatRequest {
	return ChatRequest{
		ProviderID: profile.ProviderID,
		Protocol:   "cohere-chat",
		BaseURL:    defaultString(firstEnv(profile.BaseURLEnvVars...), profile.DefaultBaseURL),
		APIKey:     firstEnv(profile.APIKeyEnvVars...),
		AuthHeader: "Authorization",
		AuthScheme: "Bearer",
		Model:      defaultString(modelID, defaultString(firstEnv(profile.ModelEnvVars...), profile.DefaultModel)),
		Messages:   messages,
	}
}

type responsesProfile struct {
	ProviderID     string
	DefaultBaseURL string
	BaseURLEnvVars []string
	APIKeyEnvVars  []string
	ModelEnvVars   []string
	DefaultModel   string
	AuthHeader     string
	AuthScheme     string
	QueryParams    map[string]string
}

func (profile responsesProfile) chatRequest(messages []Message, modelID string) ChatRequest {
	return ChatRequest{
		ProviderID:  profile.ProviderID,
		Protocol:    "openai-responses",
		BaseURL:     defaultString(firstEnv(profile.BaseURLEnvVars...), profile.DefaultBaseURL),
		APIKey:      firstEnv(profile.APIKeyEnvVars...),
		AuthHeader:  profile.AuthHeader,
		AuthScheme:  profile.AuthScheme,
		QueryParams: cloneStringMap(profile.QueryParams),
		Model:       defaultString(modelID, defaultString(firstEnv(profile.ModelEnvVars...), profile.DefaultModel)),
		Messages:    messages,
	}
}

type bedrockProfile struct {
	ProviderID     string
	DefaultBaseURL string
	BaseURLEnvVars []string
	APIKeyEnvVars  []string
	ModelEnvVars   []string
	DefaultModel   string
}

func (profile bedrockProfile) chatRequest(messages []Message, modelID string) ChatRequest {
	return ChatRequest{
		ProviderID: profile.ProviderID,
		Protocol:   "bedrock-converse",
		BaseURL:    defaultString(firstEnv(profile.BaseURLEnvVars...), profile.DefaultBaseURL),
		APIKey:     firstEnv(profile.APIKeyEnvVars...),
		AuthHeader: "Authorization",
		AuthScheme: "Bearer",
		Model:      defaultString(modelID, defaultString(firstEnv(profile.ModelEnvVars...), profile.DefaultModel)),
		Messages:   messages,
	}
}

type geminiProfile struct {
	ProviderID     string
	DefaultBaseURL string
	BaseURLEnvVars []string
	APIKeyEnvVars  []string
	ModelEnvVars   []string
	DefaultModel   string
}

func (profile geminiProfile) chatRequest(messages []Message, modelID string) ChatRequest {
	return ChatRequest{
		ProviderID: profile.ProviderID,
		Protocol:   "gemini",
		BaseURL:    defaultString(firstEnv(profile.BaseURLEnvVars...), profile.DefaultBaseURL),
		APIKey:     firstEnv(profile.APIKeyEnvVars...),
		AuthHeader: "x-goog-api-key",
		Model:      defaultString(modelID, defaultString(firstEnv(profile.ModelEnvVars...), profile.DefaultModel)),
		Messages:   messages,
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
	{ID: "baseten", Name: "Baseten", Protocols: []string{"openai-compatible"}},
	{ID: "deepseek", Name: "DeepSeek", Protocols: []string{"openai-compatible"}},
	{ID: "fireworks", Name: "Fireworks", Protocols: []string{"openai-compatible"}},
	{ID: "perplexity", Name: "Perplexity", Protocols: []string{"openai-compatible"}},
	{ID: "cohere", Name: "Cohere", Protocols: []string{"chat"}},
	{ID: "cerebras", Name: "Cerebras", Protocols: []string{"openai-compatible"}},
	{ID: "deepinfra", Name: "DeepInfra", Protocols: []string{"openai-compatible"}},
	{ID: "together", Name: "Together AI", Protocols: []string{"openai-compatible"}},
	{ID: "alibaba", Name: "Alibaba", Protocols: []string{"openai-compatible"}},
	{ID: "vercel", Name: "Vercel AI Gateway", Protocols: []string{"ai-gateway"}},
	{ID: "cloudflare-ai-gateway", Name: "Cloudflare AI Gateway", Protocols: []string{"openai-compatible"}},
	{ID: "cloudflare-workers-ai", Name: "Cloudflare Workers AI", Protocols: []string{"openai-compatible"}},
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
	"baseten": {
		ProviderID:     "baseten",
		DefaultBaseURL: "https://inference.baseten.co/v1",
		BaseURLEnvVars: []string{"OPENCODE_BASETEN_BASE_URL", "BASETEN_BASE_URL"},
		APIKeyEnvVars:  []string{"OPENCODE_BASETEN_API_KEY", "BASETEN_API_KEY"},
		ModelEnvVars:   []string{"OPENCODE_BASETEN_MODEL", "BASETEN_MODEL"},
		AuthHeader:     "Authorization",
		AuthScheme:     "Bearer",
	},
	"deepseek": {
		ProviderID:     "deepseek",
		DefaultBaseURL: "https://api.deepseek.com/v1",
		BaseURLEnvVars: []string{"OPENCODE_DEEPSEEK_BASE_URL", "DEEPSEEK_BASE_URL"},
		APIKeyEnvVars:  []string{"OPENCODE_DEEPSEEK_API_KEY", "DEEPSEEK_API_KEY"},
		ModelEnvVars:   []string{"OPENCODE_DEEPSEEK_MODEL", "DEEPSEEK_MODEL"},
		AuthHeader:     "Authorization",
		AuthScheme:     "Bearer",
	},
	"fireworks": {
		ProviderID:     "fireworks",
		DefaultBaseURL: "https://api.fireworks.ai/inference/v1",
		BaseURLEnvVars: []string{"OPENCODE_FIREWORKS_BASE_URL", "FIREWORKS_BASE_URL"},
		APIKeyEnvVars:  []string{"OPENCODE_FIREWORKS_API_KEY", "FIREWORKS_API_KEY"},
		ModelEnvVars:   []string{"OPENCODE_FIREWORKS_MODEL", "FIREWORKS_MODEL"},
		AuthHeader:     "Authorization",
		AuthScheme:     "Bearer",
	},
	"perplexity": {
		ProviderID:     "perplexity",
		DefaultBaseURL: "https://api.perplexity.ai",
		BaseURLEnvVars: []string{"OPENCODE_PERPLEXITY_BASE_URL", "PERPLEXITY_BASE_URL"},
		APIKeyEnvVars:  []string{"OPENCODE_PERPLEXITY_API_KEY", "PERPLEXITY_API_KEY"},
		ModelEnvVars:   []string{"OPENCODE_PERPLEXITY_MODEL", "PERPLEXITY_MODEL"},
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

func bedrockBaseURL() string {
	region := defaultString(firstEnv("OPENCODE_BEDROCK_REGION", "BEDROCK_REGION", "AWS_REGION"), "us-east-1")
	return "https://bedrock-runtime." + region + ".amazonaws.com"
}

func cloudflareAIGatewayBaseURL() string {
	accountID := firstEnv("OPENCODE_CLOUDFLARE_ACCOUNT_ID", "CLOUDFLARE_ACCOUNT_ID")
	if accountID == "" {
		return ""
	}
	gatewayID := defaultString(firstEnv("OPENCODE_CLOUDFLARE_GATEWAY_ID", "CLOUDFLARE_GATEWAY_ID"), "default")
	return "https://gateway.ai.cloudflare.com/v1/" + urlPathEscape(accountID) + "/" + urlPathEscape(gatewayID) + "/compat"
}

func cloudflareWorkersAIBaseURL() string {
	accountID := firstEnv("OPENCODE_CLOUDFLARE_ACCOUNT_ID", "CLOUDFLARE_ACCOUNT_ID")
	if accountID == "" {
		return ""
	}
	return "https://api.cloudflare.com/client/v4/accounts/" + urlPathEscape(accountID) + "/ai/v1"
}

func cloudflareAIGatewayHeaders() map[string]string {
	token := firstEnv("OPENCODE_CLOUDFLARE_API_TOKEN", "CLOUDFLARE_API_TOKEN", "CF_AIG_TOKEN")
	if token == "" {
		return nil
	}
	return map[string]string{"cf-aig-authorization": "Bearer " + token}
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

func urlPathEscape(value string) string {
	return url.PathEscape(value)
}
