package runtime

import (
	"context"
	"testing"

	"github.com/RecoveryAshes/opencode/internal/domain/session"
	"github.com/RecoveryAshes/opencode/internal/llm"
	"github.com/RecoveryAshes/opencode/internal/storage"
)

type fakeChatClient struct {
	request  llm.ChatRequest
	response llm.ChatResponse
}

func (client *fakeChatClient) Chat(_ context.Context, request llm.ChatRequest) (llm.ChatResponse, error) {
	client.request = request
	if client.response.Text != "" {
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
