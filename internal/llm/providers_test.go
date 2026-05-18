package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
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

func writeModelsDevFixture(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "models.json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write models fixture: %v", err)
	}
	return path
}

func isolateModelsDevCatalog(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("OPENCODE_MODELS_URL", "")
	t.Setenv("OPENCODE_MODELS_PATH", "")
	t.Setenv("OPENCODE_DISABLE_MODELS_FETCH", "1")
	t.Setenv("OPENCODE_CLIENT", "")
	defaultModelsDevCatalog.mu.Lock()
	defaultModelsDevCatalog.cacheKey = ""
	defaultModelsDevCatalog.raw = nil
	defaultModelsDevCatalog.mu.Unlock()
	t.Cleanup(func() {
		defaultModelsDevCatalog.mu.Lock()
		defaultModelsDevCatalog.cacheKey = ""
		defaultModelsDevCatalog.raw = nil
		defaultModelsDevCatalog.mu.Unlock()
	})
	return root
}

func modelsDevFixture(provider string) string {
	return `{
		"` + provider + `": {
			"id": "` + provider + `",
			"name": "Fixture AI",
			"env": ["FIXTURE_API_KEY"],
			"npm": "@fixture/sdk",
			"api": "https://fixture.example/v1",
			"models": {
				"fixture-pro": {
					"id": "fixture-pro",
					"name": "Fixture Pro",
					"attachment": false,
					"reasoning": false,
					"temperature": true,
					"tool_call": true,
					"release_date": "2026-01-02",
					"modalities": {"input":["text"],"output":["text"]},
					"limit": {"context": 128000, "output": 4096},
					"cost": {"input": 1, "output": 2}
				}
			}
		}
	}`
}

func TestProviderInventoryIncludesMigrationTargets(t *testing.T) {
	got := map[string]bool{}
	for _, id := range ProviderIDs() {
		got[id] = true
	}

	for _, id := range []string{"openai", "anthropic", "google", "google-vertex", "google-vertex-anthropic", "azure", "amazon-bedrock", "opencode", "openrouter", "llmgateway", "nvidia", "kilo", "zenmux", "github-copilot", "cloudflare-ai-gateway", "cloudflare-workers-ai", "gitlab", "sap-ai-core", "openai-compatible"} {
		if !got[id] {
			t.Fatalf("provider %q missing from inventory", id)
		}
	}
}

func TestListProvidersLoadsModelsDevPath(t *testing.T) {
	isolateModelsDevCatalog(t)
	path := writeModelsDevFixture(t, `{
		"fixture-ai": {
			"id": "fixture-ai",
			"name": "Fixture AI",
			"env": ["FIXTURE_API_KEY"],
			"npm": "@fixture/sdk",
			"api": "https://fixture.example/v1",
			"models": {
				"fixture-pro": {
					"id": "fixture-pro-api",
					"name": "Fixture Pro",
					"family": "fixture",
					"attachment": true,
					"reasoning": true,
					"temperature": true,
					"tool_call": false,
					"interleaved": {"field":"reasoning_content"},
					"release_date": "2026-01-02",
					"modalities": {"input":["text","image","pdf"],"output":["text"]},
					"limit": {"context": 200000, "input": 128000, "output": 64000},
					"cost": {
						"input": 1.25,
						"output": 2.5,
						"cache_read": 0.1,
						"cache_write": 0.2,
						"context_over_200k": {"input": 3, "output": 4, "cache_read": 0.3, "cache_write": 0.4}
					}
				}
			}
		}
	}`)
	t.Setenv("OPENCODE_MODELS_PATH", path)

	result := ListProviders(config.Info{"enabled_providers": []any{"fixture-ai"}})
	if len(result.All) != 1 {
		t.Fatalf("providers = %#v, want fixture-ai only", result.All)
	}
	provider := result.All[0]
	if provider.ID != "fixture-ai" || provider.Name != "Fixture AI" || provider.Env[0] != "FIXTURE_API_KEY" {
		t.Fatalf("provider = %#v, want models.dev provider fields", provider)
	}
	model := provider.Models["fixture-pro"]
	if model.ID != "fixture-pro" ||
		model.API["id"] != "fixture-pro-api" ||
		model.API["url"] != "https://fixture.example/v1" ||
		model.API["npm"] != "@fixture/sdk" ||
		model.Name != "Fixture Pro" ||
		model.Family != "fixture" ||
		!model.Capabilities.Attachment ||
		!model.Capabilities.Reasoning ||
		model.Capabilities.Toolcall ||
		model.Capabilities.Interleaved == nil ||
		!model.Capabilities.Input.Image ||
		!model.Capabilities.Input.PDF ||
		model.Limit.Context != 200000 ||
		model.Limit.Input != 128000 ||
		model.Limit.Output != 64000 ||
		model.Cost.Input != 1.25 ||
		model.Cost.Cache.Write != 0.2 ||
		model.Cost.ExperimentalOver200K == nil ||
		model.Cost.ExperimentalOver200K.Cache.Write != 0.4 ||
		model.ReleaseDate != "2026-01-02" {
		t.Fatalf("model = %#v, want models.dev model mapping", model)
	}
	if result.Default["fixture-ai"] != "fixture-pro" {
		t.Fatalf("default = %#v, want fixture-pro", result.Default)
	}
}

func TestListProvidersLoadsModelsDevExperimentalModes(t *testing.T) {
	isolateModelsDevCatalog(t)
	path := writeModelsDevFixture(t, `{
		"fixture-ai": {
			"id": "fixture-ai",
			"name": "Fixture AI",
			"env": ["FIXTURE_API_KEY"],
			"npm": "@fixture/sdk",
			"api": "https://fixture.example/v1",
			"models": {
				"fixture-pro": {
					"id": "fixture-pro",
					"name": "Fixture Pro",
					"attachment": false,
					"reasoning": true,
					"temperature": true,
					"tool_call": true,
					"release_date": "2026-01-02",
					"modalities": {"input":["text"],"output":["text"]},
					"limit": {"context": 128000, "output": 4096},
					"cost": {"input": 1, "output": 2},
					"experimental": {
						"modes": {
							"turbo": {
								"cost": {"input": 0.5, "output": 1.5, "cache_read": 0.05},
								"provider": {
									"body": {"reasoning_effort":"low","max_output_tokens":1234},
									"headers": {"X-Fixture":"turbo"}
								}
							}
						}
					}
				}
			}
		}
	}`)
	t.Setenv("OPENCODE_MODELS_PATH", path)

	result := ListProviders(config.Info{"enabled_providers": []any{"fixture-ai"}})
	model := result.All[0].Models["fixture-pro-turbo"]
	if model.ID != "fixture-pro-turbo" ||
		model.Name != "Fixture Pro Turbo" ||
		model.Cost.Input != 0.5 ||
		model.Cost.Output != 1.5 ||
		model.Cost.Cache.Read != 0.05 ||
		model.Options["reasoningEffort"] != "low" ||
		model.Options["maxOutputTokens"] != float64(1234) ||
		model.Headers["X-Fixture"] != "turbo" {
		t.Fatalf("mode model = %#v, want experimental mode mapping", model)
	}
}

func TestListProvidersModelsDevPathMergesConfigOverrides(t *testing.T) {
	isolateModelsDevCatalog(t)
	path := writeModelsDevFixture(t, `{
		"fixture-ai": {
			"id": "fixture-ai",
			"name": "Fixture AI",
			"env": ["FIXTURE_API_KEY"],
			"models": {
				"fixture-pro": {
					"id": "fixture-pro",
					"name": "Fixture Pro",
					"attachment": false,
					"reasoning": false,
					"temperature": true,
					"tool_call": true,
					"release_date": "2026-01-02",
					"modalities": {"input":["text"],"output":["text"]},
					"limit": {"context": 128000, "output": 4096},
					"cost": {"input": 1, "output": 2}
				}
			}
		}
	}`)
	t.Setenv("OPENCODE_MODELS_PATH", path)

	result := ListProviders(config.Info{
		"enabled_providers": []any{"fixture-ai"},
		"provider": map[string]any{
			"fixture-ai": map[string]any{
				"name": "Configured Fixture",
				"options": map[string]any{
					"baseURL": "https://configured.example/v1",
				},
				"models": map[string]any{
					"local-model": map[string]any{"id": "local-api"},
				},
			},
		},
	})
	provider := result.All[0]
	if provider.Name != "Configured Fixture" || provider.Options["baseURL"] != "https://configured.example/v1" {
		t.Fatalf("provider = %#v, want config override over models.dev", provider)
	}
	if _, ok := provider.Models["fixture-pro"]; !ok {
		t.Fatalf("models = %#v, want models.dev model preserved", provider.Models)
	}
	if provider.Models["local-model"].API["id"] != "local-api" {
		t.Fatalf("models = %#v, want config model merged", provider.Models)
	}
}

func TestListProvidersMissingModelsDevPathFallsBackToStaticCatalog(t *testing.T) {
	isolateModelsDevCatalog(t)
	t.Setenv("OPENCODE_MODELS_PATH", filepath.Join(t.TempDir(), "missing.json"))

	result := ListProviders(config.Info{"enabled_providers": []any{"openai"}})
	if len(result.All) != 1 || result.All[0].ID != "openai" || len(result.All[0].Models) == 0 {
		t.Fatalf("providers = %#v, want static openai fallback", result.All)
	}
}

func TestListProvidersLoadsModelsDevCacheFile(t *testing.T) {
	isolateModelsDevCatalog(t)
	cachePath := modelsDevCacheFile()
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		t.Fatalf("mkdir cache: %v", err)
	}
	if err := os.WriteFile(cachePath, []byte(modelsDevFixture("cache-ai")), 0o644); err != nil {
		t.Fatalf("write cache: %v", err)
	}

	result := ListProviders(config.Info{"enabled_providers": []any{"cache-ai"}})
	if len(result.All) != 1 || result.All[0].ID != "cache-ai" || result.All[0].Models["fixture-pro"].Name != "Fixture Pro" {
		t.Fatalf("providers = %#v, want models.dev cache provider", result.All)
	}
}

func TestListProvidersLoadsModelsDevSnapshotFallback(t *testing.T) {
	isolateModelsDevCatalog(t)

	result := ListProviders(config.Info{"enabled_providers": []any{"openai"}})
	if len(result.All) != 1 || result.All[0].ID != "openai" {
		t.Fatalf("providers = %#v, want openai from snapshot", result.All)
	}
	if _, ok := result.All[0].Models["gpt-4o"]; !ok {
		t.Fatalf("openai models = %#v, want snapshot model", result.All[0].Models)
	}
}

func TestRefreshModelsCatalogFetchesAndCachesModelsDev(t *testing.T) {
	isolateModelsDevCatalog(t)
	t.Setenv("OPENCODE_DISABLE_MODELS_FETCH", "")
	t.Setenv("OPENCODE_CLIENT", "test-client")

	var gotPath string
	var gotUserAgent string
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotUserAgent = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(modelsDevFixture("remote-ai")))
	}))
	defer remote.Close()
	t.Setenv("OPENCODE_MODELS_URL", remote.URL)

	if err := RefreshModelsCatalog(t.Context(), true); err != nil {
		t.Fatalf("RefreshModelsCatalog() error = %v", err)
	}
	if gotPath != "/api.json" {
		t.Fatalf("path = %q, want /api.json", gotPath)
	}
	if gotUserAgent != "opencode/go/dev/test-client" {
		t.Fatalf("user agent = %q, want Go models.dev agent", gotUserAgent)
	}
	result := ListProviders(config.Info{"enabled_providers": []any{"remote-ai"}})
	if len(result.All) != 1 || result.All[0].ID != "remote-ai" {
		t.Fatalf("providers = %#v, want remote-ai", result.All)
	}
	cacheData, err := os.ReadFile(modelsDevCacheFile())
	if err != nil {
		t.Fatalf("read cache: %v", err)
	}
	if !strings.Contains(string(cacheData), "remote-ai") {
		t.Fatalf("cache = %q, want remote-ai catalog", string(cacheData))
	}
}

func TestModelsDevCustomURLUsesSHA1CacheName(t *testing.T) {
	isolateModelsDevCatalog(t)
	t.Setenv("OPENCODE_MODELS_URL", "https://fixture.models.example")

	got := filepath.Base(modelsDevCacheFile())
	want := "models-" + sha1Hex("https://fixture.models.example") + ".json"
	if got != want {
		t.Fatalf("cache name = %q, want %q", got, want)
	}
}

func TestRefreshModelsCatalogSkipsFreshCache(t *testing.T) {
	isolateModelsDevCatalog(t)
	t.Setenv("OPENCODE_DISABLE_MODELS_FETCH", "")
	t.Setenv("OPENCODE_MODELS_URL", "https://fresh-cache.example")
	cachePath := modelsDevCacheFile()
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		t.Fatalf("mkdir cache: %v", err)
	}
	if err := os.WriteFile(cachePath, []byte(modelsDevFixture("fresh-ai")), 0o644); err != nil {
		t.Fatalf("write cache: %v", err)
	}

	remoteCalled := false
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		remoteCalled = true
		_, _ = w.Write([]byte(modelsDevFixture("remote-ai")))
	}))
	defer remote.Close()

	if err := RefreshModelsCatalog(t.Context(), false); err != nil {
		t.Fatalf("RefreshModelsCatalog() error = %v", err)
	}
	if remoteCalled {
		t.Fatalf("remote was called despite fresh cache")
	}
	result := ListProviders(config.Info{"enabled_providers": []any{"fresh-ai"}})
	if len(result.All) != 1 || result.All[0].ID != "fresh-ai" {
		t.Fatalf("providers = %#v, want fresh cache provider", result.All)
	}
}

func TestListProvidersAppliesConfigFiltersAndCustomModels(t *testing.T) {
	isolateModelsDevCatalog(t)
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

func TestListProvidersIncludesMigratedProviderHeaders(t *testing.T) {
	result := ListProviders(config.Info{
		"enabled_providers": []any{"openrouter", "llmgateway", "nvidia", "kilo", "zenmux", "cerebras"},
	})
	providers := map[string]PublicProvider{}
	for _, provider := range result.All {
		providers[provider.ID] = provider
	}

	tests := map[string]map[string]string{
		"openrouter": {
			"HTTP-Referer": "https://opencode.ai/",
			"X-Title":      "opencode",
		},
		"llmgateway": {
			"HTTP-Referer": "https://opencode.ai/",
			"X-Title":      "opencode",
			"X-Source":     "opencode",
		},
		"nvidia": {
			"HTTP-Referer":            "https://opencode.ai/",
			"X-Title":                 "opencode",
			"X-BILLING-INVOKE-ORIGIN": "OpenCode",
		},
		"kilo": {
			"HTTP-Referer": "https://opencode.ai/",
			"X-Title":      "opencode",
		},
		"zenmux": {
			"HTTP-Referer": "https://opencode.ai/",
			"X-Title":      "opencode",
		},
		"cerebras": {
			"X-Cerebras-3rd-Party-Integration": "opencode",
		},
	}
	for id, want := range tests {
		t.Run(id, func(t *testing.T) {
			rawHeaders, ok := providers[id].Options["headers"].(map[string]any)
			if !ok {
				t.Fatalf("%s headers = %#v, want provider headers", id, providers[id].Options["headers"])
			}
			if len(rawHeaders) != len(want) {
				t.Fatalf("%s headers = %#v, want %#v", id, rawHeaders, want)
			}
			for key, value := range want {
				if rawHeaders[key] != value {
					t.Fatalf("%s headers[%q] = %#v, want %q", id, key, rawHeaders[key], value)
				}
			}
		})
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

func TestConfiguredReasoningVariantsMatchProviderTransform(t *testing.T) {
	tests := []struct {
		name       string
		providerID string
		modelID    string
		model      map[string]any
		check      func(t *testing.T, variants map[string]map[string]any)
	}{
		{
			name:       "openai gpt 5.2 includes none and xhigh encrypted reasoning",
			providerID: "openai",
			modelID:    "gpt-5.2",
			model: map[string]any{
				"reasoning":    true,
				"release_date": "2025-12-10",
				"provider":     map[string]any{"npm": "@ai-sdk/openai"},
			},
			check: func(t *testing.T, variants map[string]map[string]any) {
				t.Helper()
				if variants["none"]["reasoningEffort"] != "none" ||
					variants["xhigh"]["reasoningEffort"] != "xhigh" ||
					variants["xhigh"]["reasoningSummary"] != "auto" {
					t.Fatalf("variants = %#v, want OpenAI gpt-5.2 none/xhigh reasoning variants", variants)
				}
				include, ok := variants["xhigh"]["include"].([]any)
				if !ok || len(include) != 1 || include[0] != "reasoning.encrypted_content" {
					t.Fatalf("include = %#v, want encrypted reasoning include", variants["xhigh"]["include"])
				}
			},
		},
		{
			name:       "openrouter uses nested reasoning effort",
			providerID: "openrouter",
			modelID:    "openai/gpt-5.2",
			model: map[string]any{
				"reasoning": true,
				"provider":  map[string]any{"npm": "@openrouter/ai-sdk-provider"},
			},
			check: func(t *testing.T, variants map[string]map[string]any) {
				t.Helper()
				reasoning, ok := variants["xhigh"]["reasoning"].(map[string]any)
				if !ok || reasoning["effort"] != "xhigh" {
					t.Fatalf("variants = %#v, want OpenRouter nested reasoning effort", variants)
				}
			},
		},
		{
			name:       "anthropic adaptive opus includes display",
			providerID: "anthropic",
			modelID:    "claude-opus-4-7",
			model: map[string]any{
				"id":        "claude-opus-4-7",
				"reasoning": true,
				"provider":  map[string]any{"npm": "@ai-sdk/anthropic"},
			},
			check: func(t *testing.T, variants map[string]map[string]any) {
				t.Helper()
				thinking, ok := variants["xhigh"]["thinking"].(map[string]any)
				if !ok || thinking["type"] != "adaptive" || thinking["display"] != "summarized" || variants["max"]["effort"] != "max" {
					t.Fatalf("variants = %#v, want Anthropic adaptive variants", variants)
				}
			},
		},
		{
			name:       "gemini 2.5 pro uses max thinking budget",
			providerID: "google",
			modelID:    "gemini-2.5-pro",
			model: map[string]any{
				"reasoning": true,
				"provider":  map[string]any{"npm": "@ai-sdk/google"},
			},
			check: func(t *testing.T, variants map[string]map[string]any) {
				t.Helper()
				thinking, ok := variants["max"]["thinkingConfig"].(map[string]any)
				if !ok || thinking["includeThoughts"] != true || thinking["thinkingBudget"] != 32768 {
					t.Fatalf("variants = %#v, want Gemini 2.5 Pro max budget", variants)
				}
			},
		},
		{
			name:       "bedrock nova uses reasoning config",
			providerID: "amazon-bedrock",
			modelID:    "us.amazon.nova-micro-v1:0",
			model: map[string]any{
				"reasoning": true,
				"provider":  map[string]any{"npm": "@ai-sdk/amazon-bedrock"},
			},
			check: func(t *testing.T, variants map[string]map[string]any) {
				t.Helper()
				config, ok := variants["high"]["reasoningConfig"].(map[string]any)
				if !ok || config["type"] != "enabled" || config["maxReasoningEffort"] != "high" {
					t.Fatalf("variants = %#v, want Bedrock Nova reasoningConfig variants", variants)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := ListProviders(config.Info{
				"enabled_providers": []any{test.providerID},
				"provider": map[string]any{
					test.providerID: map[string]any{
						"models": map[string]any{
							test.modelID: test.model,
						},
					},
				},
			})
			model := result.All[0].Models[test.modelID]
			test.check(t, model.Variants)
		})
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
			name:      "llmgateway",
			provider:  "llmgateway",
			envKey:    "LLMGATEWAY_BASE_URL",
			envValue:  "https://local.llmgateway.test/v1",
			baseURL:   "https://local.llmgateway.test/v1",
			apiKeyEnv: "LLMGATEWAY_API_KEY",
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
		{
			name:      "nvidia",
			provider:  "nvidia",
			envKey:    "NVIDIA_BASE_URL",
			envValue:  "https://local.nvidia.test/v1",
			baseURL:   "https://local.nvidia.test/v1",
			apiKeyEnv: "NVIDIA_API_KEY",
		},
		{
			name:      "kilo",
			provider:  "kilo",
			envKey:    "KILO_BASE_URL",
			envValue:  "https://local.kilo.test/api/gateway",
			baseURL:   "https://local.kilo.test/api/gateway",
			apiKeyEnv: "KILO_API_KEY",
		},
		{
			name:      "zenmux",
			provider:  "zenmux",
			envKey:    "ZENMUX_BASE_URL",
			envValue:  "https://local.zenmux.test/api/v1",
			baseURL:   "https://local.zenmux.test/api/v1",
			apiKeyEnv: "ZENMUX_API_KEY",
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

func TestResolveChatRequestMigratedProviderHeaders(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		headers  map[string]string
	}{
		{
			name:     "openrouter",
			provider: "openrouter",
			headers: map[string]string{
				"HTTP-Referer": "https://opencode.ai/",
				"X-Title":      "opencode",
			},
		},
		{
			name:     "llmgateway",
			provider: "llmgateway",
			headers: map[string]string{
				"HTTP-Referer": "https://opencode.ai/",
				"X-Title":      "opencode",
				"X-Source":     "opencode",
			},
		},
		{
			name:     "nvidia",
			provider: "nvidia",
			headers: map[string]string{
				"HTTP-Referer":            "https://opencode.ai/",
				"X-Title":                 "opencode",
				"X-BILLING-INVOKE-ORIGIN": "OpenCode",
			},
		},
		{
			name:     "kilo",
			provider: "kilo",
			headers: map[string]string{
				"HTTP-Referer": "https://opencode.ai/",
				"X-Title":      "opencode",
			},
		},
		{
			name:     "zenmux",
			provider: "zenmux",
			headers: map[string]string{
				"HTTP-Referer": "https://opencode.ai/",
				"X-Title":      "opencode",
			},
		},
		{
			name:     "cerebras",
			provider: "cerebras",
			headers: map[string]string{
				"X-Cerebras-3rd-Party-Integration": "opencode",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ResolveChatRequest([]Message{{Role: "user", Content: "hello"}}, test.provider, "mock-model")
			if err != nil {
				t.Fatalf("ResolveChatRequest() error = %v", err)
			}
			if len(got.Headers) != len(test.headers) {
				t.Fatalf("headers = %#v, want %#v", got.Headers, test.headers)
			}
			for key, value := range test.headers {
				if got.Headers[key] != value {
					t.Fatalf("headers[%q] = %q, want %q in %#v", key, got.Headers[key], value, got.Headers)
				}
			}
		})
	}
}

func TestResolveChatRequestOpencodeUsesPublicFallback(t *testing.T) {
	t.Setenv("OPENCODE_API_KEY", "")

	got, err := ResolveChatRequest([]Message{{Role: "user", Content: "hello"}}, "opencode", "")
	if err != nil {
		t.Fatalf("ResolveChatRequest() error = %v", err)
	}
	if got.ProviderID != "opencode" ||
		got.Protocol != "openai-compatible" ||
		got.BaseURL != "https://opencode.ai/zen/v1" ||
		got.APIKey != "public" ||
		got.Model != "big-pickle" {
		t.Fatalf("request = %#v, want OpenCode Zen public profile", got)
	}
}

func TestResolveChatRequestOpencodeUsesConfiguredAPIKey(t *testing.T) {
	t.Setenv("OPENCODE_API_KEY", "zen-key")
	t.Setenv("OPENCODE_ZEN_BASE_URL", "https://local.zen.test/v1")
	t.Setenv("OPENCODE_MODEL", "gpt-5.3-codex")

	got, err := ResolveChatRequest([]Message{{Role: "user", Content: "hello"}}, "opencode", "")
	if err != nil {
		t.Fatalf("ResolveChatRequest() error = %v", err)
	}
	if got.BaseURL != "https://local.zen.test/v1" || got.APIKey != "zen-key" || got.Model != "gpt-5.3-codex" {
		t.Fatalf("request = %#v, want configured OpenCode Zen profile", got)
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

func TestResolveChatRequestGoogleVertex(t *testing.T) {
	t.Setenv("GOOGLE_VERTEX_PROJECT", "test-project")
	t.Setenv("GOOGLE_VERTEX_LOCATION", "europe-west4")

	got, err := ResolveChatRequest([]Message{{Role: "user", Content: "hello"}}, "google-vertex", "gemini-2.5-pro")
	if err != nil {
		t.Fatalf("ResolveChatRequest() error = %v", err)
	}
	if got.Protocol != "gemini" ||
		got.BaseURL != "https://europe-west4-aiplatform.googleapis.com/v1/projects/test-project/locations/europe-west4/publishers/google" ||
		got.APIKey != "" ||
		got.AuthHeader != "Authorization" ||
		got.AuthScheme != "Bearer" ||
		got.Model != "gemini-2.5-pro" ||
		got.TokenSource == nil {
		t.Fatalf("request = %#v, want Vertex Gemini request", got)
	}
}

func TestResolveChatRequestGoogleVertexGlobalLocation(t *testing.T) {
	t.Setenv("GOOGLE_CLOUD_PROJECT", "global-project")
	t.Setenv("VERTEX_LOCATION", "global")

	got, err := ResolveChatRequest([]Message{{Role: "user", Content: "hello"}}, "google-vertex", "")
	if err != nil {
		t.Fatalf("ResolveChatRequest() error = %v", err)
	}
	if got.BaseURL != "https://aiplatform.googleapis.com/v1/projects/global-project/locations/global/publishers/google" ||
		got.Model != "gemini-2.5-flash" ||
		got.TokenSource == nil {
		t.Fatalf("request = %#v, want global Vertex endpoint", got)
	}
}

func TestResolveChatRequestGoogleVertexRequiresProject(t *testing.T) {
	t.Setenv("GOOGLE_VERTEX_PROJECT", "")
	t.Setenv("GOOGLE_CLOUD_PROJECT", "")
	t.Setenv("GCP_PROJECT", "")
	t.Setenv("GCLOUD_PROJECT", "")

	_, err := ResolveChatRequest([]Message{{Role: "user", Content: "hello"}}, "google-vertex", "gemini-2.5-flash")
	if err == nil || !strings.Contains(err.Error(), "GOOGLE_VERTEX_PROJECT or GOOGLE_CLOUD_PROJECT") {
		t.Fatalf("ResolveChatRequest() error = %v, want project requirement", err)
	}
}

func TestResolveChatRequestGoogleVertexAnthropic(t *testing.T) {
	t.Setenv("GOOGLE_VERTEX_PROJECT", "anthropic-project")
	t.Setenv("GOOGLE_VERTEX_ANTHROPIC_LOCATION", "europe-west1")

	got, err := ResolveChatRequest([]Message{{Role: "user", Content: "hello"}}, "google-vertex-anthropic", "")
	if err != nil {
		t.Fatalf("ResolveChatRequest() error = %v", err)
	}
	if got.Protocol != "anthropic-messages" ||
		got.BaseURL != "https://europe-west1-aiplatform.googleapis.com/v1/projects/anthropic-project/locations/europe-west1/publishers/anthropic" ||
		got.APIKey != "" ||
		got.AuthHeader != "Authorization" ||
		got.AuthScheme != "Bearer" ||
		got.Model != "claude-sonnet-4-6@default" ||
		got.Headers["anthropic-version"] != "2023-06-01" ||
		got.TokenSource == nil {
		t.Fatalf("request = %#v, want Vertex Anthropic request", got)
	}
}

func TestResolveChatRequestGoogleVertexAnthropicUsesGlobalDefault(t *testing.T) {
	t.Setenv("GOOGLE_CLOUD_PROJECT", "anthropic-global")
	t.Setenv("GOOGLE_VERTEX_LOCATION", "")
	t.Setenv("GOOGLE_CLOUD_LOCATION", "")
	t.Setenv("VERTEX_LOCATION", "")

	got, err := ResolveChatRequest([]Message{{Role: "user", Content: "hello"}}, "google-vertex-anthropic", "claude-haiku-4-5@20251001")
	if err != nil {
		t.Fatalf("ResolveChatRequest() error = %v", err)
	}
	if got.BaseURL != "https://aiplatform.googleapis.com/v1/projects/anthropic-global/locations/global/publishers/anthropic" ||
		got.Model != "claude-haiku-4-5@20251001" {
		t.Fatalf("request = %#v, want Vertex Anthropic default location", got)
	}
}

func TestResolveChatRequestGoogleVertexAnthropicRequiresProject(t *testing.T) {
	t.Setenv("GOOGLE_VERTEX_PROJECT", "")
	t.Setenv("GOOGLE_CLOUD_PROJECT", "")
	t.Setenv("GCP_PROJECT", "")
	t.Setenv("GCLOUD_PROJECT", "")

	_, err := ResolveChatRequest([]Message{{Role: "user", Content: "hello"}}, "google-vertex-anthropic", "claude-sonnet-4-6@default")
	if err == nil || !strings.Contains(err.Error(), "GOOGLE_VERTEX_PROJECT or GOOGLE_CLOUD_PROJECT") {
		t.Fatalf("ResolveChatRequest() error = %v, want project requirement", err)
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

func TestResolveChatRequestGitLabAgenticAnthropic(t *testing.T) {
	t.Setenv("GITLAB_TOKEN", "gitlab-token")
	t.Setenv("GITLAB_INSTANCE_URL", "https://gitlab.example")
	t.Setenv("GITLAB_AI_GATEWAY_URL", "https://cloud.gitlab.example")

	got, err := ResolveChatRequest([]Message{{Role: "user", Content: "hello"}}, "gitlab", "duo-chat-sonnet-4-6")
	if err != nil {
		t.Fatalf("ResolveChatRequest() error = %v", err)
	}
	if got.Protocol != "anthropic-messages" ||
		got.BaseURL != "https://cloud.gitlab.example/ai/v1/proxy/anthropic" ||
		got.Model != "claude-sonnet-4-6" ||
		got.APIKey != "" ||
		got.TokenSource == nil ||
		got.Headers["anthropic-beta"] != "context-1m-2025-08-07" ||
		!strings.Contains(got.Headers["User-Agent"], "gitlab-ai-provider/6.6.0") {
		t.Fatalf("request = %#v, want GitLab Anthropic proxy request", got)
	}
}

func TestResolveChatRequestGitLabAgenticOpenAI(t *testing.T) {
	t.Setenv("GITLAB_TOKEN", "gitlab-token")

	got, err := ResolveChatRequest([]Message{{Role: "user", Content: "hello"}}, "gitlab", "duo-chat-gpt-5-4-nano")
	if err != nil {
		t.Fatalf("ResolveChatRequest() error = %v", err)
	}
	if got.Protocol != "openai-compatible" ||
		got.BaseURL != "https://cloud.gitlab.com/ai/v1/proxy/openai/v1" ||
		got.Model != "gpt-5.4-nano" ||
		got.TokenSource == nil {
		t.Fatalf("request = %#v, want GitLab OpenAI proxy request", got)
	}
}

func TestResolveChatRequestGitLabWorkflowExplicitlyUnsupported(t *testing.T) {
	t.Setenv("GITLAB_TOKEN", "gitlab-token")

	_, err := ResolveChatRequest([]Message{{Role: "user", Content: "hello"}}, "gitlab", "duo-workflow-sonnet-4-6")
	if err == nil || !strings.Contains(err.Error(), "workflow protocol") {
		t.Fatalf("ResolveChatRequest() error = %v, want workflow protocol gap", err)
	}
}

func TestGitLabDirectAccessTokenSource(t *testing.T) {
	var gotAuth string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token": "direct-token",
			"headers": map[string]string{
				"x-gitlab-realm": "realm",
				"x-api-key":      "strip-me",
			},
		})
	}))
	defer server.Close()
	headers := map[string]string{}
	source := gitLabDirectAccessTokenSource{
		InstanceURL:  server.URL,
		APIKey:       "oauth-token",
		FeatureFlags: gitLabFeatureFlags(),
		Headers:      headers,
	}

	token, err := source.Token(context.Background())
	if err != nil {
		t.Fatalf("Token() error = %v", err)
	}
	if token != "direct-token" || gotAuth != "Bearer oauth-token" {
		t.Fatalf("token/auth = %q/%q, want direct token and bearer auth", token, gotAuth)
	}
	flags, ok := gotBody["feature_flags"].(map[string]any)
	if !ok || flags["duo_agent_platform"] != true || flags["duo_agent_platform_agentic_chat"] != true {
		t.Fatalf("body = %#v, want GitLab feature flags", gotBody)
	}
	if headers["x-gitlab-realm"] != "realm" {
		t.Fatalf("headers = %#v, want direct-access headers merged", headers)
	}
	if _, ok := headers["x-api-key"]; ok {
		t.Fatalf("headers = %#v, did not want x-api-key forwarded", headers)
	}
}

func TestResolveChatRequestSapAICore(t *testing.T) {
	t.Setenv("AICORE_SERVICE_KEY", "service-key")
	t.Setenv("AICORE_BASE_URL", "https://sap.example/v1")
	t.Setenv("AICORE_DEPLOYMENT_ID", "deployment")
	t.Setenv("AICORE_RESOURCE_GROUP", "resource-group")

	got, err := ResolveChatRequest([]Message{{Role: "user", Content: "hello"}}, "sap-ai-core", "anthropic--claude-4.6-opus")
	if err != nil {
		t.Fatalf("ResolveChatRequest() error = %v", err)
	}
	if got.Protocol != "openai-compatible" ||
		got.BaseURL != "https://sap.example/v1" ||
		got.APIKey != "service-key" ||
		got.Model != "anthropic--claude-4.6-opus" ||
		got.Options["deploymentId"] != "deployment" ||
		got.Options["resourceGroup"] != "resource-group" {
		t.Fatalf("request = %#v, want SAP AI Core request", got)
	}
}

func TestResolveChatRequestSapAICoreRequiresServiceKey(t *testing.T) {
	_, err := ResolveChatRequest([]Message{{Role: "user", Content: "hello"}}, "sap-ai-core", "model")
	if err == nil || !strings.Contains(err.Error(), "AICORE_SERVICE_KEY") {
		t.Fatalf("ResolveChatRequest() error = %v, want service key requirement", err)
	}
}

func TestResolveChatRequestUnknownOrUnsupportedProvider(t *testing.T) {
	if _, err := ResolveChatRequest(nil, "missing", "model"); err == nil || !strings.Contains(err.Error(), "unknown provider") {
		t.Fatalf("missing error = %v, want unknown provider", err)
	}
}
