package runtime

import (
	"context"
	"testing"

	"github.com/RecoveryAshes/opencode/internal/domain/session"
	"github.com/RecoveryAshes/opencode/internal/llm"
	"github.com/RecoveryAshes/opencode/internal/storage"
)

type fakeChatClient struct {
	request llm.ChatRequest
}

func (client *fakeChatClient) Chat(_ context.Context, request llm.ChatRequest) (llm.ChatResponse, error) {
	client.request = request
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
