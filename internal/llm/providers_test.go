package llm

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/RecoveryAshes/opencode/internal/config"
)

func clearBedrockAuthEnv(t *testing.T) {
	t.Helper()

	for _, key := range []string{
		"OPENCODE_AWS_BEARER_TOKEN_BEDROCK",
		"AWS_BEARER_TOKEN_BEDROCK",
		"OPENCODE_AWS_ACCESS_KEY_ID",
		"AWS_ACCESS_KEY_ID",
		"OPENCODE_AWS_SECRET_ACCESS_KEY",
		"AWS_SECRET_ACCESS_KEY",
		"OPENCODE_AWS_SESSION_TOKEN",
		"AWS_SESSION_TOKEN",
		"OPENCODE_AWS_PROFILE",
		"OPENCODE_BEDROCK_PROFILE",
		"AWS_PROFILE",
		"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI",
		"AWS_CONTAINER_CREDENTIALS_FULL_URI",
		"AWS_WEB_IDENTITY_TOKEN_FILE",
	} {
		t.Setenv(key, "")
	}
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(t.TempDir(), "credentials"))
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(t.TempDir(), "config"))
}

func TestProviderInventoryIncludesMigrationTargets(t *testing.T) {
	got := map[string]bool{}
	for _, id := range ProviderIDs() {
		got[id] = true
	}

	for _, id := range []string{"openai", "anthropic", "google", "azure", "amazon-bedrock", "openrouter", "github-copilot", "cloudflare-ai-gateway", "cloudflare-workers-ai", "openai-compatible"} {
		if !got[id] {
			t.Fatalf("provider %q missing from inventory", id)
		}
	}
}

func TestListProvidersAppliesConfigFiltersAndCustomModels(t *testing.T) {
	result := ListProviders(config.Info{
		"enabled_providers":  []any{"anthropic", "custom-ai"},
		"disabled_providers": []any{"openai"},
		"provider": map[string]any{
			"custom-ai": map[string]any{
				"name": "Custom AI",
				"env":  []any{"CUSTOM_AI_KEY"},
				"api":  "https://custom.local/api",
				"npm":  "@ai-sdk/custom",
				"options": map[string]any{
					"baseURL": "https://custom.local/v1",
				},
				"models": map[string]any{
					"custom-large": map[string]any{
						"name":        "Custom Large",
						"id":          "custom-large-api",
						"attachment":  true,
						"reasoning":   true,
						"tool_call":   true,
						"temperature": false,
						"cost": map[string]any{
							"input":       1.25,
							"output":      2.5,
							"cache_read":  0.1,
							"cache_write": 0.2,
							"context_over_200k": map[string]any{
								"input":       5.0,
								"output":      8.0,
								"cache_read":  0.5,
								"cache_write": 0.6,
							},
							"tiers": []any{
								map[string]any{
									"input":       3.0,
									"output":      6.0,
									"cache_read":  0.3,
									"cache_write": 0.4,
									"tier": map[string]any{
										"type": "context",
										"size": 200000.0,
									},
								},
							},
						},
						"limit": map[string]any{
							"context": 200000,
							"output":  8192,
						},
						"modalities": map[string]any{
							"input":  []any{"text", "image"},
							"output": []any{"text"},
						},
						"provider": map[string]any{
							"api": "https://custom.local/model-api",
							"npm": "@ai-sdk/openai-compatible",
						},
						"variants": map[string]any{
							"low": map[string]any{
								"reasoningEffort": "low",
							},
							"high": map[string]any{
								"disabled":        true,
								"reasoningEffort": "high",
							},
							"custom": map[string]any{
								"disabled":     false,
								"budgetTokens": 5000,
							},
						},
					},
				},
			},
		},
	})

	if len(result.All) != 2 {
		t.Fatalf("providers = %#v, want anthropic and custom-ai", result.All)
	}
	if result.All[0].ID != "anthropic" || result.All[1].ID != "custom-ai" {
		t.Fatalf("provider order = %#v, want built-in then custom", result.All)
	}
	custom := result.All[1]
	if custom.Name != "Custom AI" || custom.Env[0] != "CUSTOM_AI_KEY" || custom.Options["baseURL"] != "https://custom.local/v1" {
		t.Fatalf("custom provider = %#v, want config provider fields", custom)
	}
	model := custom.Models["custom-large"]
	if model.ID != "custom-large" ||
		model.API["id"] != "custom-large-api" ||
		model.API["url"] != "https://custom.local/model-api" ||
		model.API["npm"] != "@ai-sdk/openai-compatible" ||
		model.Name != "Custom Large" ||
		!model.Capabilities.Attachment ||
		!model.Capabilities.Toolcall ||
		model.Capabilities.Temperature ||
		model.Cost.Input != 1.25 ||
		model.Cost.Cache.Read != 0.1 ||
		model.Cost.Cache.Write != 0.2 ||
		len(model.Cost.Tiers) != 1 ||
		model.Cost.Tiers[0].Tier.Type != "context" ||
		model.Cost.Tiers[0].Tier.Size != 200000 ||
		model.Cost.Tiers[0].Cache.Write != 0.4 ||
		model.Cost.ExperimentalOver200K == nil ||
		model.Cost.ExperimentalOver200K.Input != 5 ||
		model.Cost.ExperimentalOver200K.Cache.Read != 0.5 ||
		model.Variants["low"]["reasoningEffort"] != "low" ||
		model.Variants["medium"]["reasoningEffort"] != "medium" ||
		model.Variants["custom"]["budgetTokens"] != 5000 ||
		model.Variants["custom"]["disabled"] != nil ||
		model.Limit.Context != 200000 ||
		model.Limit.Output != 8192 ||
		!model.Capabilities.Input.Image {
		t.Fatalf("custom model = %#v, want config model fields", model)
	}
	if _, ok := model.Variants["high"]; ok {
		t.Fatalf("variants = %#v, did not want disabled high variant", model.Variants)
	}
	if result.Default["custom-ai"] != "custom-large" {
		t.Fatalf("default = %#v, want custom-large", result.Default)
	}
	if _, ok := result.Default["openai"]; ok {
		t.Fatalf("default includes disabled openai: %#v", result.Default)
	}
}

func TestConfiguredReasoningModelGetsDefaultVariants(t *testing.T) {
	result := ListProviders(config.Info{
		"enabled_providers": []any{"custom-ai"},
		"provider": map[string]any{
			"custom-ai": map[string]any{
				"models": map[string]any{
					"gpt-5-compatible": map[string]any{
						"reasoning": true,
						"provider": map[string]any{
							"npm": "@ai-sdk/openai-compatible",
						},
					},
				},
			},
		},
	})

	model := result.All[0].Models["gpt-5-compatible"]
	if model.Variants["low"]["reasoningEffort"] != "low" ||
		model.Variants["medium"]["reasoningEffort"] != "medium" ||
		model.Variants["high"]["reasoningEffort"] != "high" {
		t.Fatalf("variants = %#v, want default reasoning efforts", model.Variants)
	}
}

func TestConfiguredModelIDUsesKeyForPublicIDAndIDForAPI(t *testing.T) {
	result := ListProviders(config.Info{
		"enabled_providers": []any{"custom-ai"},
		"provider": map[string]any{
			"custom-ai": map[string]any{
				"api": "https://provider.example/v1",
				"npm": "@ai-sdk/provider",
				"models": map[string]any{
					"friendly": map[string]any{
						"id": "actual-model",
					},
				},
			},
		},
	})

	model := result.All[0].Models["friendly"]
	if model.ID != "friendly" || model.API["id"] != "actual-model" || model.Name != "friendly" {
		t.Fatalf("model = %#v, want public friendly id with actual API id", model)
	}
	if model.API["url"] != "https://provider.example/v1" || model.API["npm"] != "@ai-sdk/provider" {
		t.Fatalf("api = %#v, want provider-level api/npm", model.API)
	}
}

func TestMergePublicProviderDeepMergesOptions(t *testing.T) {
	base := PublicProvider{
		ID: "anthropic",
		Options: map[string]any{
			"headers": map[string]any{
				"anthropic-beta": "base",
			},
			"timeout": 300000,
		},
	}
	override := PublicProvider{
		ID: "anthropic",
		Options: map[string]any{
			"headers": map[string]any{
				"X-Custom": "custom",
			},
			"chunkTimeout": 15000,
		},
	}

	got := mergePublicProvider(base, override)
	headers := got.Options["headers"].(map[string]any)
	if headers["anthropic-beta"] != "base" || headers["X-Custom"] != "custom" {
		t.Fatalf("headers = %#v, want deep merge", headers)
	}
	if got.Options["timeout"] != 300000 || got.Options["chunkTimeout"] != 15000 {
		t.Fatalf("options = %#v, want preserved and override options", got.Options)
	}
}

func TestSortPublicModelsUsesCatalogPriority(t *testing.T) {
	models := []PublicModel{
		{ID: "z-local", ProviderID: "local"},
		{ID: "claude-sonnet-4-5", ProviderID: "anthropic"},
		{ID: "gpt-5", ProviderID: "openai"},
		{ID: "gpt-4o-mini", ProviderID: "openai"},
	}
	SortPublicModels(models)
	got := []string{}
	for _, model := range models {
		got = append(got, model.ProviderID+"/"+model.ID)
	}
	want := []string{"anthropic/claude-sonnet-4-5", "openai/gpt-5", "local/z-local", "openai/gpt-4o-mini"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("models = %#v, want %#v", got, want)
	}
}

func TestConfigProvidersUsesProvidersKey(t *testing.T) {
	result := ConfigProviders(config.Info{
		"disabled_providers": []any{"openai"},
	})
	if len(result.Providers) == 0 {
		t.Fatalf("providers is empty")
	}
	if result.Providers[0].ID == "openai" {
		t.Fatalf("providers starts with disabled openai: %#v", result.Providers[0])
	}
	if _, ok := result.Default["openai"]; ok {
		t.Fatalf("default includes disabled openai: %#v", result.Default)
	}
}

func TestResolveChatRequestOpenAICompatibleProfiles(t *testing.T) {
	tests := []struct {
		name      string
		provider  string
		envKey    string
		envValue  string
		baseURL   string
		apiKeyEnv string
	}{
		{
			name:      "openrouter",
			provider:  "openrouter",
			envKey:    "OPENROUTER_BASE_URL",
			envValue:  "https://local.openrouter.test/v1",
			baseURL:   "https://local.openrouter.test/v1",
			apiKeyEnv: "OPENROUTER_API_KEY",
		},
		{
			name:      "xai",
			provider:  "xai",
			envKey:    "XAI_BASE_URL",
			envValue:  "https://local.xai.test/v1",
			baseURL:   "https://local.xai.test/v1",
			apiKeyEnv: "XAI_API_KEY",
		},
		{
			name:      "groq",
			provider:  "groq",
			envKey:    "GROQ_BASE_URL",
			envValue:  "https://local.groq.test/openai/v1",
			baseURL:   "https://local.groq.test/openai/v1",
			apiKeyEnv: "GROQ_API_KEY",
		},
		{
			name:      "together legacy id",
			provider:  "togetherai",
			envKey:    "TOGETHER_AI_BASE_URL",
			envValue:  "https://local.together.test/v1",
			baseURL:   "https://local.together.test/v1",
			apiKeyEnv: "TOGETHER_AI_API_KEY",
		},
		{
			name:      "baseten",
			provider:  "baseten",
			envKey:    "BASETEN_BASE_URL",
			envValue:  "https://local.baseten.test/v1",
			baseURL:   "https://local.baseten.test/v1",
			apiKeyEnv: "BASETEN_API_KEY",
		},
		{
			name:      "deepseek",
			provider:  "deepseek",
			envKey:    "DEEPSEEK_BASE_URL",
			envValue:  "https://local.deepseek.test/v1",
			baseURL:   "https://local.deepseek.test/v1",
			apiKeyEnv: "DEEPSEEK_API_KEY",
		},
		{
			name:      "fireworks",
			provider:  "fireworks",
			envKey:    "FIREWORKS_BASE_URL",
			envValue:  "https://local.fireworks.test/v1",
			baseURL:   "https://local.fireworks.test/v1",
			apiKeyEnv: "FIREWORKS_API_KEY",
		},
		{
			name:      "perplexity",
			provider:  "perplexity",
			envKey:    "PERPLEXITY_BASE_URL",
			envValue:  "https://local.perplexity.test",
			baseURL:   "https://local.perplexity.test",
			apiKeyEnv: "PERPLEXITY_API_KEY",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(test.envKey, test.envValue)
			t.Setenv(test.apiKeyEnv, "provider-key")

			got, err := ResolveChatRequest([]Message{{Role: "user", Content: "hello"}}, test.provider, "mock-model")
			if err != nil {
				t.Fatalf("ResolveChatRequest() error = %v", err)
			}
			if got.ProviderID != test.provider || got.BaseURL != test.baseURL || got.APIKey != "provider-key" || got.Model != "mock-model" {
				t.Fatalf("request = %#v, want provider profile", got)
			}
			if got.AuthHeader != "Authorization" || got.AuthScheme != "Bearer" {
				t.Fatalf("auth = %q/%q, want bearer authorization", got.AuthHeader, got.AuthScheme)
			}
		})
	}
}

func TestResolveChatRequestOpenAIResponses(t *testing.T) {
	t.Setenv("OPENAI_BASE_URL", "https://local.openai.test/v1")
	t.Setenv("OPENAI_API_KEY", "openai-key")

	got, err := ResolveChatRequest([]Message{{Role: "user", Content: "hello"}}, "openai", "gpt-5.2")
	if err != nil {
		t.Fatalf("ResolveChatRequest() error = %v", err)
	}
	if got.ProviderID != "openai" ||
		got.Protocol != "openai-responses" ||
		got.BaseURL != "https://local.openai.test/v1" ||
		got.APIKey != "openai-key" ||
		got.Model != "gpt-5.2" {
		t.Fatalf("request = %#v, want OpenAI Responses profile", got)
	}
}

func TestResolveChatRequestAzureUsesAPIKeyHeaderAndVersion(t *testing.T) {
	t.Setenv("AZURE_OPENAI_RESOURCE_NAME", "opencode-test")
	t.Setenv("AZURE_OPENAI_API_KEY", "azure-key")
	t.Setenv("AZURE_OPENAI_API_VERSION", "2024-10-21")

	got, err := ResolveChatRequest([]Message{{Role: "user", Content: "hello"}}, "azure", "deployment")
	if err != nil {
		t.Fatalf("ResolveChatRequest() error = %v", err)
	}
	if got.BaseURL != "https://opencode-test.openai.azure.com/openai/v1" {
		t.Fatalf("baseURL = %q, want resource URL", got.BaseURL)
	}
	if got.Protocol != "openai-responses" {
		t.Fatalf("protocol = %q, want OpenAI Responses", got.Protocol)
	}
	if got.AuthHeader != "api-key" || got.AuthScheme != "" || got.APIKey != "azure-key" {
		t.Fatalf("auth = %#v, want Azure api-key header", got)
	}
	if got.QueryParams["api-version"] != "2024-10-21" {
		t.Fatalf("query params = %#v, want api-version", got.QueryParams)
	}
}

func TestResolveChatRequestAnthropicMessages(t *testing.T) {
	t.Setenv("ANTHROPIC_BASE_URL", "https://local.anthropic.test/v1")
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-key")

	got, err := ResolveChatRequest([]Message{{Role: "user", Content: "hello"}}, "anthropic", "claude-sonnet-4-5")
	if err != nil {
		t.Fatalf("ResolveChatRequest() error = %v", err)
	}
	if got.Protocol != "anthropic-messages" ||
		got.BaseURL != "https://local.anthropic.test/v1" ||
		got.APIKey != "anthropic-key" ||
		got.AuthHeader != "x-api-key" ||
		got.Model != "claude-sonnet-4-5" {
		t.Fatalf("request = %#v, want Anthropic Messages request", got)
	}
	if got.Headers["anthropic-version"] != "2023-06-01" {
		t.Fatalf("headers = %#v, want Anthropic version", got.Headers)
	}
}

func TestResolveChatRequestGemini(t *testing.T) {
	t.Setenv("GOOGLE_GENERATIVE_AI_BASE_URL", "https://local.google.test/v1beta")
	t.Setenv("GOOGLE_GENERATIVE_AI_API_KEY", "google-key")

	got, err := ResolveChatRequest([]Message{{Role: "user", Content: "hello"}}, "google", "gemini-2.5-flash")
	if err != nil {
		t.Fatalf("ResolveChatRequest() error = %v", err)
	}
	if got.Protocol != "gemini" ||
		got.BaseURL != "https://local.google.test/v1beta" ||
		got.APIKey != "google-key" ||
		got.AuthHeader != "x-goog-api-key" ||
		got.Model != "gemini-2.5-flash" {
		t.Fatalf("request = %#v, want Gemini request", got)
	}
}

func TestResolveChatRequestBedrockBearer(t *testing.T) {
	clearBedrockAuthEnv(t)
	t.Setenv("AWS_REGION", "eu-west-1")
	t.Setenv("AWS_BEARER_TOKEN_BEDROCK", "bedrock-token")

	got, err := ResolveChatRequest([]Message{{Role: "user", Content: "hello"}}, "amazon-bedrock", "us.amazon.nova-micro-v1:0")
	if err != nil {
		t.Fatalf("ResolveChatRequest() error = %v", err)
	}
	if got.Protocol != "bedrock-converse" ||
		got.BaseURL != "https://bedrock-runtime.eu-west-1.amazonaws.com" ||
		got.APIKey != "bedrock-token" ||
		got.AuthHeader != "Authorization" ||
		got.AuthScheme != "Bearer" ||
		got.Model != "us.amazon.nova-micro-v1:0" {
		t.Fatalf("request = %#v, want Bedrock request", got)
	}
}

func TestResolveChatRequestBedrockUsesAWSCredentials(t *testing.T) {
	clearBedrockAuthEnv(t)
	t.Setenv("AWS_REGION", "ap-southeast-2")
	t.Setenv("AWS_ACCESS_KEY_ID", "aws-key")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "aws-secret")
	t.Setenv("AWS_SESSION_TOKEN", "aws-session")

	got, err := ResolveChatRequest([]Message{{Role: "user", Content: "hello"}}, "amazon-bedrock", "us.amazon.nova-micro-v1:0")
	if err != nil {
		t.Fatalf("ResolveChatRequest() error = %v", err)
	}
	if got.Protocol != "bedrock-converse" ||
		got.BaseURL != "https://bedrock-runtime.ap-southeast-2.amazonaws.com" ||
		got.APIKey != "" ||
		got.AWSRegion != "ap-southeast-2" ||
		got.AWSCredentials == nil ||
		got.AWSCredentials.AccessKeyID != "aws-key" ||
		got.AWSCredentials.SecretAccessKey != "aws-secret" ||
		got.AWSCredentials.SessionToken != "aws-session" {
		t.Fatalf("request = %#v, want Bedrock SigV4 credentials", got)
	}
}

func TestResolveChatRequestBedrockUsesAWSProfile(t *testing.T) {
	clearBedrockAuthEnv(t)
	t.Setenv("AWS_REGION", "eu-central-1")
	t.Setenv("AWS_PROFILE", "bedrock-dev")

	got, err := ResolveChatRequest([]Message{{Role: "user", Content: "hello"}}, "amazon-bedrock", "us.amazon.nova-micro-v1:0")
	if err != nil {
		t.Fatalf("ResolveChatRequest() error = %v", err)
	}
	if got.APIKey != "" ||
		got.AWSCredentials != nil ||
		got.AWSProfile != "bedrock-dev" ||
		got.AWSRegion != "eu-central-1" ||
		got.BaseURL != "https://bedrock-runtime.eu-central-1.amazonaws.com" {
		t.Fatalf("request = %#v, want Bedrock profile routing", got)
	}
}

func TestResolveChatRequestBedrockAllowsDefaultCredentialChain(t *testing.T) {
	clearBedrockAuthEnv(t)
	t.Setenv("AWS_REGION", "us-west-2")
	t.Setenv("AWS_WEB_IDENTITY_TOKEN_FILE", "/var/run/secrets/eks.amazonaws.com/serviceaccount/token")

	got, err := ResolveChatRequest([]Message{{Role: "user", Content: "hello"}}, "amazon-bedrock", "us.amazon.nova-micro-v1:0")
	if err != nil {
		t.Fatalf("ResolveChatRequest() error = %v", err)
	}
	if got.APIKey != "" ||
		got.AWSCredentials != nil ||
		got.AWSProfile != "" ||
		got.AWSRegion != "us-west-2" ||
		got.BaseURL != "https://bedrock-runtime.us-west-2.amazonaws.com" {
		t.Fatalf("request = %#v, want Bedrock default credential chain routing", got)
	}
}

func TestResolveChatRequestBedrockRequiresAuth(t *testing.T) {
	clearBedrockAuthEnv(t)
	_, err := ResolveChatRequest([]Message{{Role: "user", Content: "hello"}}, "amazon-bedrock", "us.amazon.nova-micro-v1:0")
	if err == nil || !strings.Contains(err.Error(), "AWS_BEARER_TOKEN_BEDROCK or AWS credentials") {
		t.Fatalf("ResolveChatRequest() error = %v, want auth requirement", err)
	}
}

func TestResolveChatRequestCohere(t *testing.T) {
	t.Setenv("COHERE_BASE_URL", "https://local.cohere.test/v2")
	t.Setenv("COHERE_API_KEY", "cohere-key")

	got, err := ResolveChatRequest([]Message{{Role: "user", Content: "hello"}}, "cohere", "command-r")
	if err != nil {
		t.Fatalf("ResolveChatRequest() error = %v", err)
	}
	if got.Protocol != "cohere-chat" ||
		got.BaseURL != "https://local.cohere.test/v2" ||
		got.APIKey != "cohere-key" ||
		got.AuthHeader != "Authorization" ||
		got.AuthScheme != "Bearer" ||
		got.Model != "command-r" {
		t.Fatalf("request = %#v, want Cohere request", got)
	}
}

func TestResolveChatRequestCloudflareAIGateway(t *testing.T) {
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "test/account")
	t.Setenv("CLOUDFLARE_GATEWAY_ID", "test gateway")
	t.Setenv("CLOUDFLARE_API_TOKEN", "gateway-token")
	t.Setenv("CLOUDFLARE_PROVIDER_API_KEY", "provider-token")

	got, err := ResolveChatRequest([]Message{{Role: "user", Content: "hello"}}, "cloudflare-ai-gateway", "openai/gpt-4o-mini")
	if err != nil {
		t.Fatalf("ResolveChatRequest() error = %v", err)
	}
	if got.Protocol != "openai-compatible" ||
		got.BaseURL != "https://gateway.ai.cloudflare.com/v1/test%2Faccount/test%20gateway/compat" ||
		got.APIKey != "provider-token" ||
		got.Headers["cf-aig-authorization"] != "Bearer gateway-token" {
		t.Fatalf("request = %#v, want Cloudflare AI Gateway request", got)
	}
}

func TestResolveChatRequestCloudflareWorkersAI(t *testing.T) {
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "test-account")
	t.Setenv("CLOUDFLARE_WORKERS_AI_TOKEN", "workers-token")

	got, err := ResolveChatRequest([]Message{{Role: "user", Content: "hello"}}, "cloudflare-workers-ai", "@cf/meta/llama-3.1-8b-instruct")
	if err != nil {
		t.Fatalf("ResolveChatRequest() error = %v", err)
	}
	if got.Protocol != "openai-compatible" ||
		got.BaseURL != "https://api.cloudflare.com/client/v4/accounts/test-account/ai/v1" ||
		got.APIKey != "workers-token" {
		t.Fatalf("request = %#v, want Cloudflare Workers AI request", got)
	}
}

func TestResolveChatRequestVercel(t *testing.T) {
	t.Setenv("VERCEL_API_KEY", "vercel-key")

	got, err := ResolveChatRequest([]Message{{Role: "user", Content: "hello"}}, "vercel", "openai/gpt-4o-mini")
	if err != nil {
		t.Fatalf("ResolveChatRequest() error = %v", err)
	}
	if got.Protocol != "openai-compatible" ||
		got.BaseURL != "https://ai-gateway.vercel.sh/v3/ai" ||
		got.APIKey != "vercel-key" ||
		got.Headers["http-referer"] != "https://opencode.ai/" ||
		got.Headers["x-title"] != "opencode" {
		t.Fatalf("request = %#v, want Vercel AI Gateway request", got)
	}
}

func TestResolveChatRequestGitHubCopilotRequiresBaseURL(t *testing.T) {
	t.Setenv("GITHUB_COPILOT_API_KEY", "copilot-token")

	_, err := ResolveChatRequest([]Message{{Role: "user", Content: "hello"}}, "github-copilot", "gpt-4.1")
	if err == nil || !strings.Contains(err.Error(), "base URL") {
		t.Fatalf("ResolveChatRequest() error = %v, want base URL requirement", err)
	}
}

func TestResolveChatRequestGitHubCopilotWithBaseURL(t *testing.T) {
	t.Setenv("GITHUB_COPILOT_BASE_URL", "https://copilot-proxy.test/v1")
	t.Setenv("GITHUB_COPILOT_API_KEY", "copilot-token")

	got, err := ResolveChatRequest([]Message{{Role: "user", Content: "hello"}}, "github-copilot", "gpt-4.1")
	if err != nil {
		t.Fatalf("ResolveChatRequest() error = %v", err)
	}
	if got.Protocol != "openai-compatible" ||
		got.BaseURL != "https://copilot-proxy.test/v1" ||
		got.APIKey != "copilot-token" ||
		got.Model != "gpt-4.1" {
		t.Fatalf("request = %#v, want GitHub Copilot request", got)
	}
}

func TestResolveChatRequestDigitalOcean(t *testing.T) {
	t.Setenv("DIGITALOCEAN_ACCESS_TOKEN", "do-token")

	got, err := ResolveChatRequest([]Message{{Role: "user", Content: "hello"}}, "digitalocean", "router:my-router")
	if err != nil {
		t.Fatalf("ResolveChatRequest() error = %v", err)
	}
	if got.Protocol != "openai-compatible" ||
		got.BaseURL != "https://inference.do-ai.run/v1" ||
		got.APIKey != "do-token" ||
		got.Model != "router:my-router" {
		t.Fatalf("request = %#v, want DigitalOcean request", got)
	}
}

func TestResolveChatRequestGitLabDuoRequiresBaseURL(t *testing.T) {
	t.Setenv("GITLAB_DUO_API_KEY", "duo-token")

	_, err := ResolveChatRequest([]Message{{Role: "user", Content: "hello"}}, "gitlab-duo", "duo-chat-sonnet-4-5")
	if err == nil || !strings.Contains(err.Error(), "base URL") {
		t.Fatalf("ResolveChatRequest() error = %v, want base URL requirement", err)
	}
}

func TestResolveChatRequestGitLabDuoWithBaseURL(t *testing.T) {
	t.Setenv("GITLAB_DUO_BASE_URL", "https://gitlab.example/api/v4/ai/duo")
	t.Setenv("GITLAB_DUO_API_KEY", "duo-token")

	got, err := ResolveChatRequest([]Message{{Role: "user", Content: "hello"}}, "gitlab-duo", "duo-chat-sonnet-4-5")
	if err != nil {
		t.Fatalf("ResolveChatRequest() error = %v", err)
	}
	if got.Protocol != "openai-compatible" ||
		got.BaseURL != "https://gitlab.example/api/v4/ai/duo" ||
		got.APIKey != "duo-token" ||
		got.Model != "duo-chat-sonnet-4-5" {
		t.Fatalf("request = %#v, want GitLab Duo request", got)
	}
}

func TestResolveChatRequestUnknownOrUnsupportedProvider(t *testing.T) {
	if _, err := ResolveChatRequest(nil, "missing", "model"); err == nil || !strings.Contains(err.Error(), "unknown provider") {
		t.Fatalf("missing error = %v, want unknown provider", err)
	}
}
