package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RecoveryAshes/opencode/internal/domain/session"
	"github.com/RecoveryAshes/opencode/internal/llm"
	"github.com/RecoveryAshes/opencode/internal/storage"
)

type fakeChatClient struct {
	request   llm.ChatRequest
	requests  []llm.ChatRequest
	response  llm.ChatResponse
	responses []llm.ChatResponse
}

func (client *fakeChatClient) Chat(_ context.Context, request llm.ChatRequest) (llm.ChatResponse, error) {
	client.request = request
	client.requests = append(client.requests, request)
	if len(client.responses) > 0 {
		index := len(client.requests) - 1
		if index >= len(client.responses) {
			index = len(client.responses) - 1
		}
		return client.responses[index], nil
	}
	if client.response.Text != "" || client.response.FinishReason != "" || len(client.response.ToolCalls) > 0 || client.response.Usage.TotalTokens > 0 {
		return client.response, nil
	}
	return llm.ChatResponse{
		Text:         "assistant reply",
		FinishReason: "stop",
		Usage: llm.Usage{
			InputTokens:  2,
			OutputTokens: 3,
			TotalTokens:  5,
		},
	}, nil
}

func TestPromptRuntimePersistsAssistantReply(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemorySessionStore()
	info, err := store.Create(ctx, session.CreateInput{Title: "chat"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	user, err := store.CreatePrompt(ctx, info.ID, session.PromptInput{
		Model: &session.ModelRef{ProviderID: "openai-compatible", ModelID: "mock-model"},
		Parts: []session.Part{{
			Type: "text",
			Data: map[string]any{"text": "hello"},
		}},
	})
	if err != nil {
		t.Fatalf("CreatePrompt() error = %v", err)
	}
	client := &fakeChatClient{}
	runtime := &PromptRuntime{
		Messages: store,
		Client:   client,
		CWD:      "/tmp/project",
		Root:     "/tmp/project",
	}

	assistant, err := runtime.Reply(ctx, info.ID, user)
	if err != nil {
		t.Fatalf("Reply() error = %v", err)
	}
	if assistant.Info.Role != "assistant" || assistant.Info.ParentID == nil || *assistant.Info.ParentID != user.Info.ID {
		t.Fatalf("assistant info = %#v", assistant.Info)
	}
	if assistant.Info.ModelID != "mock-model" || assistant.Info.ProviderID != "openai-compatible" {
		t.Fatalf("assistant model = %#v", assistant.Info)
	}
	if len(assistant.Parts) != 2 || assistant.Parts[0].Type != "text" || assistant.Parts[0].Data["text"] != "assistant reply" {
		t.Fatalf("assistant parts = %#v", assistant.Parts)
	}
	if len(client.request.Messages) != 1 || client.request.Messages[0].Content != "hello" {
		t.Fatalf("provider request = %#v", client.request)
	}
	if client.request.ProviderID != "openai-compatible" || client.request.Model != "mock-model" {
		t.Fatalf("provider request = %#v, want selected model", client.request)
	}

	messages, err := store.Messages(ctx, info.ID, 0)
	if err != nil {
		t.Fatalf("Messages() error = %v", err)
	}
	if len(messages) != 2 || messages[1].Info.ID != assistant.Info.ID {
		t.Fatalf("messages = %#v, want user and assistant", messages)
	}
}

func TestPromptRuntimeUsesSelectedProvider(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemorySessionStore()
	info, err := store.Create(ctx, session.CreateInput{Title: "chat"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	user, err := store.CreatePrompt(ctx, info.ID, session.PromptInput{
		Model: &session.ModelRef{ProviderID: "openrouter", ModelID: "openai/gpt-4o-mini"},
		Parts: []session.Part{{
			Type: "text",
			Data: map[string]any{"text": "hello"},
		}},
	})
	if err != nil {
		t.Fatalf("CreatePrompt() error = %v", err)
	}
	t.Setenv("OPENROUTER_BASE_URL", "https://local.openrouter.test/api/v1")
	t.Setenv("OPENROUTER_API_KEY", "router-key")
	client := &fakeChatClient{}
	runtime := &PromptRuntime{Messages: store, Client: client}

	if _, err := runtime.Reply(ctx, info.ID, user); err != nil {
		t.Fatalf("Reply() error = %v", err)
	}
	if client.request.ProviderID != "openrouter" ||
		client.request.BaseURL != "https://local.openrouter.test/api/v1" ||
		client.request.APIKey != "router-key" ||
		client.request.Model != "openai/gpt-4o-mini" {
		t.Fatalf("provider request = %#v, want openrouter profile", client.request)
	}
	if len(client.request.Tools) == 0 {
		t.Fatalf("provider request = %#v, want migrated tool definitions", client.request)
	}
}

func TestPromptRuntimeUsesConfiguredDefaultModel(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemorySessionStore()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "opencode.jsonc"), []byte(`{
		"model": "openrouter/openai/gpt-4o-mini"
	}`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	info, err := store.Create(ctx, session.CreateInput{Title: "chat"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	user, err := store.CreatePrompt(ctx, info.ID, session.PromptInput{
		Parts: []session.Part{{
			Type: "text",
			Data: map[string]any{"text": "hello"},
		}},
	})
	if err != nil {
		t.Fatalf("CreatePrompt() error = %v", err)
	}
	t.Setenv("OPENROUTER_BASE_URL", "https://local.openrouter.test/api/v1")
	client := &fakeChatClient{}
	runtime := &PromptRuntime{Messages: store, Client: client, CWD: root, Root: root}

	assistant, err := runtime.Reply(ctx, info.ID, user)
	if err != nil {
		t.Fatalf("Reply() error = %v", err)
	}
	if client.request.ProviderID != "openrouter" || client.request.Model != "openai/gpt-4o-mini" {
		t.Fatalf("provider request = %#v, want configured default model", client.request)
	}
	if assistant.Info.ProviderID != "openrouter" || assistant.Info.ModelID != "openai/gpt-4o-mini" {
		t.Fatalf("assistant info = %#v, want configured default model", assistant.Info)
	}
}

func TestPromptRuntimeInheritsPreviousUserModel(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemorySessionStore()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "opencode.jsonc"), []byte(`{
		"model": "custom-only/model"
	}`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	info, err := store.Create(ctx, session.CreateInput{Title: "chat"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if _, err := store.CreatePrompt(ctx, info.ID, session.PromptInput{
		Model: &session.ModelRef{ProviderID: "openrouter", ModelID: "openai/gpt-4o-mini"},
		Parts: []session.Part{{Type: "text", Data: map[string]any{"text": "first"}}},
	}); err != nil {
		t.Fatalf("CreatePrompt(first) error = %v", err)
	}
	user, err := store.CreatePrompt(ctx, info.ID, session.PromptInput{
		Parts: []session.Part{{Type: "text", Data: map[string]any{"text": "second"}}},
	})
	if err != nil {
		t.Fatalf("CreatePrompt(second) error = %v", err)
	}
	t.Setenv("OPENROUTER_BASE_URL", "https://local.openrouter.test/api/v1")
	client := &fakeChatClient{}
	runtime := &PromptRuntime{Messages: store, Client: client, CWD: root, Root: root}

	assistant, err := runtime.Reply(ctx, info.ID, user)
	if err != nil {
		t.Fatalf("Reply() error = %v", err)
	}
	if client.request.ProviderID != "openrouter" || client.request.Model != "openai/gpt-4o-mini" {
		t.Fatalf("provider request = %#v, want previous user model", client.request)
	}
	if assistant.Info.ProviderID != "openrouter" || assistant.Info.ModelID != "openai/gpt-4o-mini" {
		t.Fatalf("assistant info = %#v, want previous user model", assistant.Info)
	}
}

func TestPromptRuntimeUsesConfiguredAgentModel(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemorySessionStore()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "opencode.jsonc"), []byte(`{
		"model": "custom-only/model",
		"agent": {
			"review": {
				"model": "openrouter/openai/gpt-4o-mini",
				"variant": "high"
			}
		}
	}`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	info, err := store.Create(ctx, session.CreateInput{Title: "chat"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	user, err := store.CreatePrompt(ctx, info.ID, session.PromptInput{
		Agent: "review",
		Parts: []session.Part{{Type: "text", Data: map[string]any{"text": "hello"}}},
	})
	if err != nil {
		t.Fatalf("CreatePrompt() error = %v", err)
	}
	t.Setenv("OPENROUTER_BASE_URL", "https://local.openrouter.test/api/v1")
	client := &fakeChatClient{}
	runtime := &PromptRuntime{Messages: store, Client: client, CWD: root, Root: root}

	assistant, err := runtime.Reply(ctx, info.ID, user)
	if err != nil {
		t.Fatalf("Reply() error = %v", err)
	}
	if client.request.ProviderID != "openrouter" || client.request.Model != "openai/gpt-4o-mini" {
		t.Fatalf("provider request = %#v, want configured agent model", client.request)
	}
	if assistant.Info.ProviderID != "openrouter" || assistant.Info.ModelID != "openai/gpt-4o-mini" || assistant.Info.Variant != "high" {
		t.Fatalf("assistant info = %#v, want configured agent model and variant", assistant.Info)
	}
}

func TestPromptRuntimeFallsBackFromUnresolvedConfiguredDefaultModel(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemorySessionStore()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "opencode.jsonc"), []byte(`{
		"model": "custom-only/model"
	}`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	info, err := store.Create(ctx, session.CreateInput{Title: "chat"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	user, err := store.CreatePrompt(ctx, info.ID, session.PromptInput{
		Parts: []session.Part{{Type: "text", Data: map[string]any{"text": "hello"}}},
	})
	if err != nil {
		t.Fatalf("CreatePrompt() error = %v", err)
	}
	client := &fakeChatClient{}
	runtime := &PromptRuntime{Messages: store, Client: client, CWD: root, Root: root}

	assistant, err := runtime.Reply(ctx, info.ID, user)
	if err != nil {
		t.Fatalf("Reply() error = %v", err)
	}
	if client.request.ProviderID != "openai-compatible" || client.request.Model != "gpt-4o-mini" {
		t.Fatalf("provider request = %#v, want fallback model", client.request)
	}
	if assistant.Info.ProviderID != "openai-compatible" || assistant.Info.ModelID != "gpt-4o-mini" {
		t.Fatalf("assistant info = %#v, want fallback model", assistant.Info)
	}
}

func TestPromptRuntimeUsesConfiguredCustomProvider(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemorySessionStore()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "opencode.jsonc"), []byte(`{
		"model": "custom-ai/friendly",
		"provider": {
			"custom-ai": {
				"api": "https://custom.example/v1",
				"options": {
					"apiKey": "custom-key"
				},
				"models": {
					"friendly": {
						"id": "actual-model"
					}
				}
			}
		}
	}`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	info, err := store.Create(ctx, session.CreateInput{Title: "chat"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	user, err := store.CreatePrompt(ctx, info.ID, session.PromptInput{
		Model: &session.ModelRef{ProviderID: "custom-ai", ModelID: "friendly"},
		Parts: []session.Part{{Type: "text", Data: map[string]any{"text": "hello"}}},
	})
	if err != nil {
		t.Fatalf("CreatePrompt() error = %v", err)
	}
	client := &fakeChatClient{}
	runtime := &PromptRuntime{Messages: store, Client: client, CWD: root, Root: root}

	assistant, err := runtime.Reply(ctx, info.ID, user)
	if err != nil {
		t.Fatalf("Reply() error = %v", err)
	}
	if client.request.ProviderID != "custom-ai" ||
		client.request.Protocol != "openai-compatible" ||
		client.request.BaseURL != "https://custom.example/v1" ||
		client.request.APIKey != "custom-key" ||
		client.request.Model != "actual-model" {
		t.Fatalf("provider request = %#v, want configured custom provider", client.request)
	}
	if assistant.Info.ProviderID != "custom-ai" || assistant.Info.ModelID != "friendly" {
		t.Fatalf("assistant info = %#v, want public custom provider model", assistant.Info)
	}
}

func TestPromptRuntimeAppliesConfiguredProviderOptions(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemorySessionStore()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "opencode.jsonc"), []byte(`{
		"provider": {
			"openai-compatible": {
				"api": "https://provider-api.test/v1",
				"options": {
					"apiKey": "config-key",
					"baseURL": "https://configured.test/v1",
					"headers": {"X-Provider": "provider"}
				},
				"models": {
					"friendly-model": {
						"id": "actual-model",
						"options": {
							"headers": {"X-Model": "model"},
							"reasoningEffort": "low",
							"metadata": {"model": "base", "shared": "model"}
						},
						"variants": {
							"fast": {
								"reasoningEffort": "high",
								"metadata": {"variant": "fast", "shared": "variant"}
							}
						}
					}
				}
			}
		}
	}`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	info, err := store.Create(ctx, session.CreateInput{Title: "chat"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	user, err := store.CreatePrompt(ctx, info.ID, session.PromptInput{
		Model: &session.ModelRef{ProviderID: "openai-compatible", ModelID: "friendly-model", Variant: "fast"},
		Parts: []session.Part{{Type: "text", Data: map[string]any{"text": "hello"}}},
	})
	if err != nil {
		t.Fatalf("CreatePrompt() error = %v", err)
	}
	client := &fakeChatClient{}
	runtime := &PromptRuntime{Messages: store, Client: client, CWD: root, Root: root}

	if _, err := runtime.Reply(ctx, info.ID, user); err != nil {
		t.Fatalf("Reply() error = %v", err)
	}
	if client.request.BaseURL != "https://configured.test/v1" ||
		client.request.APIKey != "config-key" ||
		client.request.Model != "actual-model" {
		t.Fatalf("provider request = %#v, want configured base URL, key, and api model", client.request)
	}
	if client.request.Headers["X-Provider"] != "provider" || client.request.Headers["X-Model"] != "model" {
		t.Fatalf("headers = %#v, want provider and model headers", client.request.Headers)
	}
	if client.request.Options["reasoningEffort"] != "high" {
		t.Fatalf("options = %#v, want variant to override model body options", client.request.Options)
	}
	metadata := client.request.Options["metadata"].(map[string]any)
	if metadata["model"] != "base" || metadata["variant"] != "fast" || metadata["shared"] != "variant" {
		t.Fatalf("metadata = %#v, want deep-merged model and variant options", metadata)
	}
	if _, ok := client.request.Options["headers"]; ok {
		t.Fatalf("options = %#v, did not want transport headers in body options", client.request.Options)
	}
}

func TestPromptRuntimeAppliesProviderTransformDefaults(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemorySessionStore()
	info, err := store.Create(ctx, session.CreateInput{Title: "chat"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	user, err := store.CreatePrompt(ctx, info.ID, session.PromptInput{
		Model: &session.ModelRef{ProviderID: "openai", ModelID: "gpt-5.2"},
		Parts: []session.Part{{Type: "text", Data: map[string]any{"text": "hello"}}},
	})
	if err != nil {
		t.Fatalf("CreatePrompt() error = %v", err)
	}
	t.Setenv("OPENAI_BASE_URL", "https://local.openai.test/v1")
	client := &fakeChatClient{}
	runtime := &PromptRuntime{Messages: store, Client: client}

	if _, err := runtime.Reply(ctx, info.ID, user); err != nil {
		t.Fatalf("Reply() error = %v", err)
	}
	if client.request.Options["store"] != false ||
		client.request.Options["promptCacheKey"] != string(info.ID) ||
		client.request.Options["reasoningEffort"] != "medium" ||
		client.request.Options["reasoningSummary"] != "auto" ||
		client.request.Options["textVerbosity"] != "low" {
		t.Fatalf("options = %#v, want migrated OpenAI gpt-5 defaults", client.request.Options)
	}
}

func TestPromptRuntimeConfiguredOptionsOverrideProviderDefaults(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemorySessionStore()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "opencode.jsonc"), []byte(`{
		"provider": {
			"openai": {
				"models": {
					"gpt-5.2": {
						"options": {
							"reasoningEffort": "high",
							"textVerbosity": "medium"
						}
					}
				}
			}
		}
	}`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	info, err := store.Create(ctx, session.CreateInput{Title: "chat"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	user, err := store.CreatePrompt(ctx, info.ID, session.PromptInput{
		Model: &session.ModelRef{ProviderID: "openai", ModelID: "gpt-5.2"},
		Parts: []session.Part{{Type: "text", Data: map[string]any{"text": "hello"}}},
	})
	if err != nil {
		t.Fatalf("CreatePrompt() error = %v", err)
	}
	t.Setenv("OPENAI_BASE_URL", "https://local.openai.test/v1")
	client := &fakeChatClient{}
	runtime := &PromptRuntime{Messages: store, Client: client, CWD: root, Root: root}

	if _, err := runtime.Reply(ctx, info.ID, user); err != nil {
		t.Fatalf("Reply() error = %v", err)
	}
	if client.request.Options["reasoningEffort"] != "high" || client.request.Options["textVerbosity"] != "medium" {
		t.Fatalf("options = %#v, want model options to override defaults", client.request.Options)
	}
}

func TestPromptRuntimeMergesConfiguredAgentOptions(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemorySessionStore()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "opencode.jsonc"), []byte(`{
		"provider": {
			"openai": {
				"models": {
					"gpt-5.2": {
						"options": {
							"reasoningEffort": "low",
							"metadata": {"model": "gpt-5", "shared": "model"}
						},
						"variants": {
							"max": {
								"reasoningEffort": "max",
								"metadata": {"variant": "max", "shared": "variant"}
							}
						}
					}
				}
			}
		},
		"agent": {
			"review": {
				"temperature": 0.2,
				"top_p": 0.8,
				"top_k": 40,
				"max_output_tokens": 1234,
				"options": {
					"reasoningEffort": "high",
					"metadata": {"agent": "review", "shared": "agent"}
				}
			}
		}
	}`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	info, err := store.Create(ctx, session.CreateInput{Title: "chat"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	user, err := store.CreatePrompt(ctx, info.ID, session.PromptInput{
		Agent: "review",
		Model: &session.ModelRef{ProviderID: "openai", ModelID: "gpt-5.2", Variant: "max"},
		Parts: []session.Part{{Type: "text", Data: map[string]any{"text": "hello"}}},
	})
	if err != nil {
		t.Fatalf("CreatePrompt() error = %v", err)
	}
	t.Setenv("OPENAI_BASE_URL", "https://local.openai.test/v1")
	client := &fakeChatClient{}
	runtime := &PromptRuntime{Messages: store, Client: client, CWD: root, Root: root}

	if _, err := runtime.Reply(ctx, info.ID, user); err != nil {
		t.Fatalf("Reply() error = %v", err)
	}
	if client.request.Options["reasoningEffort"] != "max" {
		t.Fatalf("options = %#v, want variant to override agent and model options", client.request.Options)
	}
	metadata := client.request.Options["metadata"].(map[string]any)
	if metadata["model"] != "gpt-5" || metadata["agent"] != "review" || metadata["variant"] != "max" || metadata["shared"] != "variant" {
		t.Fatalf("metadata = %#v, want model < agent < variant merge order", metadata)
	}
	if client.request.Temperature == nil || *client.request.Temperature != 0.2 ||
		client.request.TopP == nil || *client.request.TopP != 0.8 ||
		client.request.TopK == nil || *client.request.TopK != 40 ||
		client.request.MaxTokens == nil || *client.request.MaxTokens != 1234 {
		t.Fatalf("request = %#v, want configured agent sampling params", client.request)
	}
}

func TestPromptRuntimeAppliesDefaultSamplingParams(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemorySessionStore()
	info, err := store.Create(ctx, session.CreateInput{Title: "chat"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	user, err := store.CreatePrompt(ctx, info.ID, session.PromptInput{
		Model: &session.ModelRef{ProviderID: "openai-compatible", ModelID: "gemini-3-pro"},
		Parts: []session.Part{{Type: "text", Data: map[string]any{"text": "hello"}}},
	})
	if err != nil {
		t.Fatalf("CreatePrompt() error = %v", err)
	}
	client := &fakeChatClient{}
	runtime := &PromptRuntime{Messages: store, Client: client}

	if _, err := runtime.Reply(ctx, info.ID, user); err != nil {
		t.Fatalf("Reply() error = %v", err)
	}
	if client.request.Temperature == nil || *client.request.Temperature != 1 ||
		client.request.TopP == nil || *client.request.TopP != 0.95 ||
		client.request.TopK == nil || *client.request.TopK != 64 {
		t.Fatalf("request = %#v, want migrated default sampling params", client.request)
	}
}

func TestPromptRuntimeAppliesProviderCacheKeyDefaults(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemorySessionStore()
	info, err := store.Create(ctx, session.CreateInput{Title: "chat"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	user, err := store.CreatePrompt(ctx, info.ID, session.PromptInput{
		Model: &session.ModelRef{ProviderID: "openrouter", ModelID: "openai/gpt-4o-mini"},
		Parts: []session.Part{{Type: "text", Data: map[string]any{"text": "hello"}}},
	})
	if err != nil {
		t.Fatalf("CreatePrompt() error = %v", err)
	}
	t.Setenv("OPENROUTER_BASE_URL", "https://local.openrouter.test/api/v1")
	client := &fakeChatClient{}
	runtime := &PromptRuntime{Messages: store, Client: client}

	if _, err := runtime.Reply(ctx, info.ID, user); err != nil {
		t.Fatalf("Reply() error = %v", err)
	}
	if client.request.Options["prompt_cache_key"] != string(info.ID) {
		t.Fatalf("options = %#v, want OpenRouter prompt_cache_key", client.request.Options)
	}
}

func TestPromptRuntimeAppliesAzureProviderDefaults(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemorySessionStore()
	info, err := store.Create(ctx, session.CreateInput{Title: "chat"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	user, err := store.CreatePrompt(ctx, info.ID, session.PromptInput{
		Model: &session.ModelRef{ProviderID: "azure", ModelID: "deployment"},
		Parts: []session.Part{{Type: "text", Data: map[string]any{"text": "hello"}}},
	})
	if err != nil {
		t.Fatalf("CreatePrompt() error = %v", err)
	}
	t.Setenv("AZURE_OPENAI_RESOURCE_NAME", "opencode-test")
	client := &fakeChatClient{}
	runtime := &PromptRuntime{Messages: store, Client: client}

	if _, err := runtime.Reply(ctx, info.ID, user); err != nil {
		t.Fatalf("Reply() error = %v", err)
	}
	if client.request.Options["store"] != false || client.request.Options["promptCacheKey"] != string(info.ID) {
		t.Fatalf("options = %#v, want Azure store false and prompt cache key", client.request.Options)
	}
}

func TestPromptRuntimeAppliesAdditionalProviderTransformOptions(t *testing.T) {
	tests := []struct {
		name       string
		providerID string
		modelID    string
		apiNPM     string
		rawModel   string
		env        map[string]string
		check      func(t *testing.T, request llm.ChatRequest, sessionID session.ID)
	}{
		{
			name:       "openrouter includes usage and gemini 3 reasoning",
			providerID: "openrouter",
			modelID:    "google/gemini-3-pro",
			apiNPM:     "@openrouter/ai-sdk-provider",
			env:        map[string]string{"OPENROUTER_BASE_URL": "https://local.openrouter.test/api/v1"},
			check: func(t *testing.T, request llm.ChatRequest, sessionID session.ID) {
				t.Helper()
				usage, ok := request.Options["usage"].(map[string]any)
				reasoning, rok := request.Options["reasoning"].(map[string]any)
				if !ok || usage["include"] != true || !rok || reasoning["effort"] != "high" || request.Options["prompt_cache_key"] != string(sessionID) {
					t.Fatalf("options = %#v, want OpenRouter usage, reasoning, and cache key", request.Options)
				}
			},
		},
		{
			name:       "google reasoning enables thinking config",
			providerID: "google",
			modelID:    "gemini-3-pro",
			apiNPM:     "@ai-sdk/google",
			rawModel:   `"reasoning": true,`,
			env:        map[string]string{"GOOGLE_GENERATIVE_AI_BASE_URL": "https://local.google.test/v1beta"},
			check: func(t *testing.T, request llm.ChatRequest, _ session.ID) {
				t.Helper()
				thinking, ok := request.Options["thinkingConfig"].(map[string]any)
				if !ok || thinking["includeThoughts"] != true || thinking["thinkingLevel"] != "high" {
					t.Fatalf("options = %#v, want Gemini thinking config", request.Options)
				}
			},
		},
		{
			name:       "anthropic kimi enables thinking and disables tool streaming",
			providerID: "anthropic",
			modelID:    "kimi-k2.5",
			apiNPM:     "@ai-sdk/anthropic",
			rawModel:   `"limit": {"output": 12000},`,
			env:        map[string]string{"ANTHROPIC_BASE_URL": "https://local.anthropic.test/v1"},
			check: func(t *testing.T, request llm.ChatRequest, _ session.ID) {
				t.Helper()
				thinking, ok := request.Options["thinking"].(map[string]any)
				if request.Options["toolStreaming"] != false || !ok || thinking["type"] != "enabled" || thinking["budgetTokens"] != 5999 {
					t.Fatalf("options = %#v, want Anthropic Kimi thinking defaults", request.Options)
				}
			},
		},
		{
			name:       "gateway enables caching",
			providerID: "openai-compatible",
			modelID:    "openai/gpt-5.2",
			apiNPM:     "@ai-sdk/gateway",
			env:        map[string]string{"OPENCODE_OPENAI_COMPATIBLE_BASE_URL": "https://local.gateway.test/v1"},
			check: func(t *testing.T, request llm.ChatRequest, _ session.ID) {
				t.Helper()
				gateway, ok := request.Options["gateway"].(map[string]any)
				if !ok || gateway["caching"] != "auto" {
					t.Fatalf("options = %#v, want gateway caching auto", request.Options)
				}
			},
		},
		{
			name:       "azure gpt 5.5 only sets reasoning summary",
			providerID: "azure",
			modelID:    "gpt-5.5",
			apiNPM:     "@ai-sdk/azure",
			env:        map[string]string{"AZURE_OPENAI_RESOURCE_NAME": "opencode-test"},
			check: func(t *testing.T, request llm.ChatRequest, _ session.ID) {
				t.Helper()
				if request.Options["store"] != false ||
					request.Options["reasoningSummary"] != "auto" ||
					request.Options["reasoningEffort"] != nil ||
					request.Options["textVerbosity"] != nil {
					t.Fatalf("options = %#v, want Azure gpt-5.5 summary-only reasoning default", request.Options)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store := storage.NewMemorySessionStore()
			root := t.TempDir()
			for key, value := range test.env {
				t.Setenv(key, value)
			}
			config := `{
				"provider": {
					"` + test.providerID + `": {
						"models": {
							"` + test.modelID + `": {
								` + test.rawModel + `
								"provider": {"npm": "` + test.apiNPM + `"}
							}
						}
					}
				}
			}`
			if err := os.WriteFile(filepath.Join(root, "opencode.jsonc"), []byte(config), 0o644); err != nil {
				t.Fatalf("write config: %v", err)
			}
			info, err := store.Create(ctx, session.CreateInput{Title: "chat"})
			if err != nil {
				t.Fatalf("Create() error = %v", err)
			}
			user, err := store.CreatePrompt(ctx, info.ID, session.PromptInput{
				Model: &session.ModelRef{ProviderID: test.providerID, ModelID: test.modelID},
				Parts: []session.Part{{Type: "text", Data: map[string]any{"text": "hello"}}},
			})
			if err != nil {
				t.Fatalf("CreatePrompt() error = %v", err)
			}
			client := &fakeChatClient{}
			runtime := &PromptRuntime{Messages: store, Client: client, CWD: root, Root: root}

			if _, err := runtime.Reply(ctx, info.ID, user); err != nil {
				t.Fatalf("Reply() error = %v", err)
			}
			test.check(t, client.request, info.ID)
		})
	}
}

func TestPromptRuntimeHonorsDisabledTools(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemorySessionStore()
	info, err := store.Create(ctx, session.CreateInput{Title: "chat"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	user, err := store.CreatePrompt(ctx, info.ID, session.PromptInput{
		Tools: map[string]bool{"shell": false},
		Parts: []session.Part{{Type: "text", Data: map[string]any{"text": "hello"}}},
	})
	if err != nil {
		t.Fatalf("CreatePrompt() error = %v", err)
	}
	client := &fakeChatClient{}
	runtime := &PromptRuntime{Messages: store, Client: client}

	if _, err := runtime.Reply(ctx, info.ID, user); err != nil {
		t.Fatalf("Reply() error = %v", err)
	}
	seen := map[string]bool{}
	for _, tool := range client.request.Tools {
		seen[tool.Name] = true
		if tool.Name == "bash" || tool.Name == "shell" {
			t.Fatalf("provider tools include disabled shell alias: %#v", client.request.Tools)
		}
	}
	if !seen["read"] || !seen["write"] {
		t.Fatalf("provider tools = %#v, want enabled local tools", client.request.Tools)
	}
}

func TestPromptRuntimePersistsTodoToolMetadata(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemorySessionStore()
	info, err := store.Create(ctx, session.CreateInput{Title: "todos"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	user, err := store.CreatePrompt(ctx, info.ID, session.PromptInput{
		Model: &session.ModelRef{ProviderID: "openai-compatible", ModelID: "mock-model"},
		Parts: []session.Part{{Type: "text", Data: map[string]any{"text": "plan"}}},
	})
	if err != nil {
		t.Fatalf("CreatePrompt() error = %v", err)
	}
	client := &fakeChatClient{
		responses: []llm.ChatResponse{
			{
				FinishReason: "tool-calls",
				ToolCalls: []llm.ToolCall{{
					ID:   "call_todo",
					Name: "todo",
					Arguments: map[string]any{"todos": []map[string]string{{
						"content":  "migrate session state",
						"status":   "in_progress",
						"priority": "high",
					}}},
				}},
			},
			{Text: "done", FinishReason: "stop"},
		},
	}
	runtime := &PromptRuntime{Messages: store, Client: client}

	if _, err := runtime.Reply(ctx, info.ID, user); err != nil {
		t.Fatalf("Reply() error = %v", err)
	}
	todos, err := store.Todos(ctx, info.ID)
	if err != nil {
		t.Fatalf("Todos() error = %v", err)
	}
	if len(todos) != 1 || todos[0].Content != "migrate session state" || todos[0].Priority != "high" {
		t.Fatalf("todos = %#v, want persisted todo tool metadata", todos)
	}
}

func TestPromptRuntimeUsesOpenAIResponsesProtocol(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemorySessionStore()
	info, err := store.Create(ctx, session.CreateInput{Title: "chat"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	user, err := store.CreatePrompt(ctx, info.ID, session.PromptInput{
		Model: &session.ModelRef{ProviderID: "openai", ModelID: "gpt-5.2"},
		Parts: []session.Part{{
			Type: "text",
			Data: map[string]any{"text": "hello"},
		}},
	})
	if err != nil {
		t.Fatalf("CreatePrompt() error = %v", err)
	}
	t.Setenv("OPENAI_BASE_URL", "https://local.openai.test/v1")
	t.Setenv("OPENAI_API_KEY", "openai-key")
	client := &fakeChatClient{}
	runtime := &PromptRuntime{Messages: store, Client: client}

	if _, err := runtime.Reply(ctx, info.ID, user); err != nil {
		t.Fatalf("Reply() error = %v", err)
	}
	if client.request.ProviderID != "openai" ||
		client.request.Protocol != "openai-responses" ||
		client.request.BaseURL != "https://local.openai.test/v1" ||
		client.request.APIKey != "openai-key" {
		t.Fatalf("provider request = %#v, want OpenAI Responses protocol", client.request)
	}
}

func TestPromptRuntimePersistsAnthropicCacheUsage(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemorySessionStore()
	info, err := store.Create(ctx, session.CreateInput{Title: "chat"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	user, err := store.CreatePrompt(ctx, info.ID, session.PromptInput{
		Model: &session.ModelRef{ProviderID: "anthropic", ModelID: "claude-sonnet-4-5"},
		Parts: []session.Part{{
			Type: "text",
			Data: map[string]any{"text": "hello"},
		}},
	})
	if err != nil {
		t.Fatalf("CreatePrompt() error = %v", err)
	}
	client := &fakeChatClient{
		response: llm.ChatResponse{
			Text:         "assistant reply",
			FinishReason: "stop",
			Usage: llm.Usage{
				InputTokens:      3,
				OutputTokens:     4,
				CacheReadTokens:  1,
				CacheWriteTokens: 2,
				TotalTokens:      7,
			},
		},
	}
	runtime := &PromptRuntime{Messages: store, Client: client}

	assistant, err := runtime.Reply(ctx, info.ID, user)
	if err != nil {
		t.Fatalf("Reply() error = %v", err)
	}
	if client.request.ProviderID != "anthropic" || client.request.Protocol != "anthropic-messages" {
		t.Fatalf("provider request = %#v, want Anthropic Messages protocol", client.request)
	}
	if assistant.Info.Tokens == nil || assistant.Info.Tokens.Cache.Read != 1 || assistant.Info.Tokens.Cache.Write != 2 {
		t.Fatalf("assistant tokens = %#v, want cache read/write", assistant.Info.Tokens)
	}
}

func TestPromptRuntimeUsesGeminiProtocol(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemorySessionStore()
	info, err := store.Create(ctx, session.CreateInput{Title: "chat"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	user, err := store.CreatePrompt(ctx, info.ID, session.PromptInput{
		Model: &session.ModelRef{ProviderID: "google", ModelID: "gemini-2.5-flash"},
		Parts: []session.Part{{
			Type: "text",
			Data: map[string]any{"text": "hello"},
		}},
	})
	if err != nil {
		t.Fatalf("CreatePrompt() error = %v", err)
	}
	t.Setenv("GOOGLE_GENERATIVE_AI_BASE_URL", "https://local.google.test/v1beta")
	t.Setenv("GOOGLE_GENERATIVE_AI_API_KEY", "google-key")
	client := &fakeChatClient{}
	runtime := &PromptRuntime{Messages: store, Client: client}

	if _, err := runtime.Reply(ctx, info.ID, user); err != nil {
		t.Fatalf("Reply() error = %v", err)
	}
	if client.request.ProviderID != "google" ||
		client.request.Protocol != "gemini" ||
		client.request.BaseURL != "https://local.google.test/v1beta" ||
		client.request.APIKey != "google-key" {
		t.Fatalf("provider request = %#v, want Gemini protocol", client.request)
	}
}

func TestPromptRuntimeUsesBedrockProtocol(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemorySessionStore()
	info, err := store.Create(ctx, session.CreateInput{Title: "chat"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	user, err := store.CreatePrompt(ctx, info.ID, session.PromptInput{
		Model: &session.ModelRef{ProviderID: "amazon-bedrock", ModelID: "us.amazon.nova-micro-v1:0"},
		Parts: []session.Part{{
			Type: "text",
			Data: map[string]any{"text": "hello"},
		}},
	})
	if err != nil {
		t.Fatalf("CreatePrompt() error = %v", err)
	}
	t.Setenv("AWS_REGION", "eu-west-1")
	t.Setenv("AWS_BEARER_TOKEN_BEDROCK", "bedrock-token")
	client := &fakeChatClient{}
	runtime := &PromptRuntime{Messages: store, Client: client}

	if _, err := runtime.Reply(ctx, info.ID, user); err != nil {
		t.Fatalf("Reply() error = %v", err)
	}
	if client.request.ProviderID != "amazon-bedrock" ||
		client.request.Protocol != "bedrock-converse" ||
		client.request.BaseURL != "https://bedrock-runtime.eu-west-1.amazonaws.com" ||
		client.request.APIKey != "bedrock-token" {
		t.Fatalf("provider request = %#v, want Bedrock protocol", client.request)
	}
}

func TestPromptRuntimeUsesBedrockAWSCredentials(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemorySessionStore()
	info, err := store.Create(ctx, session.CreateInput{Title: "chat"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	user, err := store.CreatePrompt(ctx, info.ID, session.PromptInput{
		Model: &session.ModelRef{ProviderID: "amazon-bedrock", ModelID: "us.amazon.nova-micro-v1:0"},
		Parts: []session.Part{{
			Type: "text",
			Data: map[string]any{"text": "hello"},
		}},
	})
	if err != nil {
		t.Fatalf("CreatePrompt() error = %v", err)
	}
	t.Setenv("AWS_REGION", "ap-southeast-2")
	t.Setenv("AWS_ACCESS_KEY_ID", "aws-key")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "aws-secret")
	client := &fakeChatClient{}
	runtime := &PromptRuntime{Messages: store, Client: client}

	if _, err := runtime.Reply(ctx, info.ID, user); err != nil {
		t.Fatalf("Reply() error = %v", err)
	}
	if client.request.ProviderID != "amazon-bedrock" ||
		client.request.Protocol != "bedrock-converse" ||
		client.request.APIKey != "" ||
		client.request.AWSCredentials == nil ||
		client.request.AWSCredentials.AccessKeyID != "aws-key" {
		t.Fatalf("provider request = %#v, want Bedrock AWS credentials", client.request)
	}
}

func TestPromptRuntimeUsesCohereProtocol(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemorySessionStore()
	info, err := store.Create(ctx, session.CreateInput{Title: "chat"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	user, err := store.CreatePrompt(ctx, info.ID, session.PromptInput{
		Model: &session.ModelRef{ProviderID: "cohere", ModelID: "command-r"},
		Parts: []session.Part{{
			Type: "text",
			Data: map[string]any{"text": "hello"},
		}},
	})
	if err != nil {
		t.Fatalf("CreatePrompt() error = %v", err)
	}
	t.Setenv("COHERE_BASE_URL", "https://local.cohere.test/v2")
	t.Setenv("COHERE_API_KEY", "cohere-key")
	client := &fakeChatClient{}
	runtime := &PromptRuntime{Messages: store, Client: client}

	if _, err := runtime.Reply(ctx, info.ID, user); err != nil {
		t.Fatalf("Reply() error = %v", err)
	}
	if client.request.ProviderID != "cohere" ||
		client.request.Protocol != "cohere-chat" ||
		client.request.BaseURL != "https://local.cohere.test/v2" ||
		client.request.APIKey != "cohere-key" {
		t.Fatalf("provider request = %#v, want Cohere protocol", client.request)
	}
}

func TestPromptRuntimeExecutesToolCalls(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hello tool\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	store := storage.NewMemorySessionStore()
	info, err := store.Create(ctx, session.CreateInput{Title: "tools"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	user, err := store.CreatePrompt(ctx, info.ID, session.PromptInput{
		Parts: []session.Part{{Type: "text", Data: map[string]any{"text": "read hello"}}},
	})
	if err != nil {
		t.Fatalf("CreatePrompt() error = %v", err)
	}
	client := &fakeChatClient{
		responses: []llm.ChatResponse{
			{
				FinishReason: "tool-calls",
				ToolCalls: []llm.ToolCall{{
					ID:        "call_1",
					Name:      "read",
					Arguments: map[string]any{"filePath": "hello.txt"},
					Raw:       `{"filePath":"hello.txt"}`,
				}},
				Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
			},
			{
				Text:         "hello tool was read",
				FinishReason: "stop",
				Usage:        llm.Usage{InputTokens: 2, OutputTokens: 3, TotalTokens: 5},
			},
		},
	}
	runtime := &PromptRuntime{Messages: store, Client: client, CWD: root, Root: root}

	assistant, err := runtime.Reply(ctx, info.ID, user)
	if err != nil {
		t.Fatalf("Reply() error = %v", err)
	}
	if assistant.Info.Finish != "stop" {
		t.Fatalf("assistant finish = %q, want stop", assistant.Info.Finish)
	}
	if len(assistant.Parts) != 3 || assistant.Parts[0].Type != "text" || assistant.Parts[1].Type != "tool" {
		t.Fatalf("assistant parts = %#v, want text, tool, and step-finish", assistant.Parts)
	}
	if assistant.Parts[0].Data["text"] != "hello tool was read" {
		t.Fatalf("assistant text = %#v, want final provider answer", assistant.Parts[0].Data)
	}
	tool := assistant.Parts[1]
	if tool.Data["callID"] != "call_1" || tool.Data["tool"] != "read" {
		t.Fatalf("tool part = %#v, want call_1/read", tool.Data)
	}
	state, ok := tool.Data["state"].(map[string]any)
	if !ok {
		t.Fatalf("state = %#v, want object", tool.Data["state"])
	}
	if state["status"] != "completed" || !strings.Contains(stringValue(state["output"]), "hello tool") {
		t.Fatalf("state = %#v, want completed read output", state)
	}
	if len(client.requests) != 2 {
		t.Fatalf("provider calls = %d, want initial tool call and final answer", len(client.requests))
	}
	if len(client.requests[1].Messages) != 2 || !strings.Contains(client.requests[1].Messages[1].Content, "hello tool") {
		t.Fatalf("second provider request = %#v, want injected tool result", client.requests[1])
	}
	if assistant.Info.Tokens == nil || assistant.Info.Tokens.Input != 3 || assistant.Info.Tokens.Output != 4 || assistant.Info.Tokens.Total == nil || *assistant.Info.Tokens.Total != 7 {
		t.Fatalf("tokens = %#v, want merged tool-loop usage", assistant.Info.Tokens)
	}
}

func TestPromptRuntimePersistsToolCallErrors(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemorySessionStore()
	info, err := store.Create(ctx, session.CreateInput{Title: "tools"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	user, err := store.CreatePrompt(ctx, info.ID, session.PromptInput{
		Parts: []session.Part{{Type: "text", Data: map[string]any{"text": "missing tool"}}},
	})
	if err != nil {
		t.Fatalf("CreatePrompt() error = %v", err)
	}
	client := &fakeChatClient{
		responses: []llm.ChatResponse{
			{
				FinishReason: "tool-calls",
				ToolCalls: []llm.ToolCall{{
					ID:        "call_missing",
					Name:      "missing",
					Arguments: map[string]any{},
				}},
			},
			{
				Text:         "missing tool could not run",
				FinishReason: "stop",
			},
		},
	}
	runtime := &PromptRuntime{Messages: store, Client: client}

	assistant, err := runtime.Reply(ctx, info.ID, user)
	if err != nil {
		t.Fatalf("Reply() error = %v", err)
	}
	if len(assistant.Parts) != 3 || assistant.Parts[1].Type != "tool" {
		t.Fatalf("assistant parts = %#v, want text, tool, and step-finish", assistant.Parts)
	}
	state, ok := assistant.Parts[1].Data["state"].(map[string]any)
	if !ok {
		t.Fatalf("state = %#v, want object", assistant.Parts[1].Data["state"])
	}
	if state["status"] != "error" || !strings.Contains(stringValue(state["error"]), "not implemented") {
		t.Fatalf("state = %#v, want missing tool error", state)
	}
	if len(client.requests) != 2 || !strings.Contains(client.requests[1].Messages[1].Content, "<error>") {
		t.Fatalf("second provider request = %#v, want injected tool error", client.requests)
	}
}

func TestProviderToolSchemaSanitizesGeminiSchemas(t *testing.T) {
	input := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"mode": map[string]any{
				"enum": []any{1, 2},
				"type": "integer",
			},
			"ratio": map[string]any{
				"type": "number",
				"enum": []any{0.5, 1},
			},
			"tags": map[string]any{
				"type": "array",
			},
			"bad": map[string]any{
				"type": "string",
				"properties": map[string]any{
					"x": map[string]any{"type": "string"},
				},
				"required": []any{"x"},
			},
			"combined": map[string]any{
				"type":       "string",
				"oneOf":      []any{map[string]any{"type": "string"}},
				"properties": map[string]any{"kept": map[string]any{"type": "string"}},
				"required":   []any{"kept"},
			},
		},
		"required": []any{"mode", "missing"},
	}

	got := providerToolSchema(input, "google", "gemini-3-pro")
	properties := got["properties"].(map[string]any)
	mode := properties["mode"].(map[string]any)
	if mode["type"] != "string" || !anyStringSliceEqual(mode["enum"], []string{"1", "2"}) {
		t.Fatalf("mode schema = %#v, want integer enum converted to string enum", mode)
	}
	ratio := properties["ratio"].(map[string]any)
	if ratio["type"] != "string" || !anyStringSliceEqual(ratio["enum"], []string{"0.5", "1"}) {
		t.Fatalf("ratio schema = %#v, want number enum converted to string enum", ratio)
	}
	if !anyStringSliceEqual(got["required"], []string{"mode"}) {
		t.Fatalf("required = %#v, want only existing properties", got["required"])
	}
	tags := properties["tags"].(map[string]any)
	items := tags["items"].(map[string]any)
	if items["type"] != "string" {
		t.Fatalf("tags items = %#v, want empty array items defaulted to string", items)
	}
	bad := properties["bad"].(map[string]any)
	if _, ok := bad["properties"]; ok {
		t.Fatalf("bad schema = %#v, did not want properties on non-object type", bad)
	}
	if _, ok := bad["required"]; ok {
		t.Fatalf("bad schema = %#v, did not want required on non-object type", bad)
	}
	combined := properties["combined"].(map[string]any)
	if _, ok := combined["properties"]; !ok {
		t.Fatalf("combined schema = %#v, want combiner schema to keep properties", combined)
	}
	if _, ok := combined["required"]; !ok {
		t.Fatalf("combined schema = %#v, want combiner schema to keep required", combined)
	}

	originalMode := input["properties"].(map[string]any)["mode"].(map[string]any)
	if originalMode["type"] != "integer" || !anyValueSliceEqual(originalMode["enum"], []any{1, 2}) {
		t.Fatalf("input schema mutated unexpectedly: %#v", originalMode)
	}
}

func TestProviderToolSchemaSanitizesMoonshotSchemas(t *testing.T) {
	input := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"ref": map[string]any{
				"$ref":        "#/$defs/Thing",
				"description": "Moonshot rejects siblings beside $ref",
			},
			"tuple": map[string]any{
				"type": "array",
				"items": []any{
					map[string]any{"type": "string"},
					map[string]any{"type": "number"},
				},
			},
			"emptyTuple": map[string]any{
				"type":  "array",
				"items": []any{},
			},
		},
	}

	got := providerToolSchema(input, "moonshotai", "kimi-k2")
	properties := got["properties"].(map[string]any)
	ref := properties["ref"].(map[string]any)
	if len(ref) != 1 || ref["$ref"] != "#/$defs/Thing" {
		t.Fatalf("ref schema = %#v, want only $ref", ref)
	}
	tuple := properties["tuple"].(map[string]any)
	items := tuple["items"].(map[string]any)
	if items["type"] != "string" {
		t.Fatalf("tuple items = %#v, want first tuple item schema", tuple["items"])
	}
	emptyTuple := properties["emptyTuple"].(map[string]any)
	if _, ok := emptyTuple["items"].(map[string]any); !ok {
		t.Fatalf("empty tuple items = %#v, want empty schema object", emptyTuple["items"])
	}

	originalRef := input["properties"].(map[string]any)["ref"].(map[string]any)
	if originalRef["description"] == nil {
		t.Fatalf("input schema mutated unexpectedly: %#v", originalRef)
	}
}

func TestLowerTranscriptSanitizesProviderText(t *testing.T) {
	got := lowerTranscript([]session.WithParts{
		{
			Info: session.MessageInfo{Role: "user"},
			Parts: []session.Part{
				{Type: "text", Data: map[string]any{"text": "hello " + string([]byte{0xed, 0xa0, 0x80})}},
				{Type: "text", Data: map[string]any{"text": "world"}},
			},
		},
		{
			Info:  session.MessageInfo{Role: "assistant"},
			Parts: []session.Part{{Type: "text", Data: map[string]any{"text": string([]byte{0xed, 0xb0, 0x80})}}},
		},
	})

	if len(got) != 2 {
		t.Fatalf("messages = %#v, want two provider messages", got)
	}
	if got[0].Content != "hello \uFFFD\nworld" {
		t.Fatalf("user content = %q, want invalid UTF-8 replaced", got[0].Content)
	}
	if got[1].Content != "\uFFFD" {
		t.Fatalf("assistant content = %q, want invalid UTF-8 replaced", got[1].Content)
	}
}

func TestToolResultMessageSanitizesProviderText(t *testing.T) {
	message := toolResultMessage(llm.ChatResponse{
		Text: "partial " + string([]byte{0xed, 0xa0, 0x80}),
	}, []session.ToolExecution{
		{
			CallID: "call_1",
			Tool:   "read",
			Input:  map[string]any{"filePath": "a.txt"},
			Output: "tool " + string([]byte{0xed, 0xb0, 0x80}),
		},
	})

	if strings.ContainsRune(message.Content, '\uFFFD') == false {
		t.Fatalf("content = %q, want replacement character for invalid UTF-8", message.Content)
	}
	if strings.Contains(message.Content, "partial \uFFFD") == false || strings.Contains(message.Content, "tool \uFFFD") == false {
		t.Fatalf("content = %q, want sanitized assistant preface and tool output", message.Content)
	}
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func anyStringSliceEqual(value any, want []string) bool {
	items, ok := value.([]any)
	if !ok || len(items) != len(want) {
		return false
	}
	for index, item := range items {
		if item != want[index] {
			return false
		}
	}
	return true
}

func anyValueSliceEqual(value any, want []any) bool {
	items, ok := value.([]any)
	if !ok || len(items) != len(want) {
		return false
	}
	for index, item := range items {
		if item != want[index] {
			return false
		}
	}
	return true
}
