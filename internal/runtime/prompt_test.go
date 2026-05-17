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

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}
