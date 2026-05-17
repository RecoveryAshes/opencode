package llm

import (
	"strings"
	"testing"
)

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

func TestResolveChatRequestBedrockRequiresBearerForNow(t *testing.T) {
	_, err := ResolveChatRequest([]Message{{Role: "user", Content: "hello"}}, "amazon-bedrock", "us.amazon.nova-micro-v1:0")
	if err == nil || !strings.Contains(err.Error(), "AWS_BEARER_TOKEN_BEDROCK") {
		t.Fatalf("ResolveChatRequest() error = %v, want bearer token requirement", err)
	}
}

func TestResolveChatRequestUnknownOrUnsupportedProvider(t *testing.T) {
	if _, err := ResolveChatRequest(nil, "missing", "model"); err == nil || !strings.Contains(err.Error(), "unknown provider") {
		t.Fatalf("missing error = %v, want unknown provider", err)
	}
}
