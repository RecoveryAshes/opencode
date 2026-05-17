package llm

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAICompatibleChatRequestAndResponse(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path = %q, want /v1/chat/completions", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("authorization = %q, want bearer token", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if body["model"] != "mock-model" || body["stream"] != false {
			t.Fatalf("body = %#v, want model and non-streaming request", body)
		}
		messages := body["messages"].([]any)
		first := messages[0].(map[string]any)
		if first["role"] != "user" || first["content"] != "hello" {
			t.Fatalf("messages = %#v, want user hello", messages)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices":[{"message":{"content":"world"},"finish_reason":"stop"}],
			"usage":{
				"prompt_tokens":3,
				"completion_tokens":4,
				"total_tokens":7,
				"prompt_tokens_details":{"cached_tokens":1},
				"completion_tokens_details":{"reasoning_tokens":2}
			}
		}`))
	}))
	defer mock.Close()

	got, err := NewOpenAICompatibleClient().Chat(t.Context(), ChatRequest{
		BaseURL: mock.URL + "/v1",
		APIKey:  "test-key",
		Model:   "mock-model",
		Messages: []Message{{
			Role:    "user",
			Content: "hello",
		}},
	})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if got.Text != "world" || got.FinishReason != "stop" {
		t.Fatalf("Chat() = %#v, want response text", got)
	}
	if got.Usage.InputTokens != 3 || got.Usage.OutputTokens != 4 || got.Usage.CacheReadTokens != 1 || got.Usage.ReasoningTokens != 2 {
		t.Fatalf("usage = %#v", got.Usage)
	}
}

func TestOpenAICompatibleChatSendsConfiguredBodyOptions(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if body["reasoningEffort"] != "high" || body["promptCacheKey"] != "ses_123" {
			t.Fatalf("body = %#v, want configured model options", body)
		}
		metadata := body["metadata"].(map[string]any)
		if metadata["source"] != "config" {
			t.Fatalf("metadata = %#v, want nested configured option", metadata)
		}
		for _, forbidden := range []string{"apiKey", "baseURL", "headers", "timeout", "includeUsage"} {
			if _, ok := body[forbidden]; ok {
				t.Fatalf("body = %#v, did not want transport option %q", body, forbidden)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer mock.Close()

	_, err := NewOpenAICompatibleClient().Chat(t.Context(), ChatRequest{
		BaseURL: mock.URL,
		Model:   "mock-model",
		Messages: []Message{{
			Role:    "user",
			Content: "hello",
		}},
		Options: map[string]any{
			"apiKey":          "transport-key",
			"baseURL":         "https://transport.example/v1",
			"headers":         map[string]any{"X-Test": "transport"},
			"timeout":         300000,
			"includeUsage":    true,
			"reasoningEffort": "high",
			"promptCacheKey":  "ses_123",
			"metadata":        map[string]any{"source": "config"},
		},
	})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
}

func TestOpenAICompatibleChatStream(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if body["stream"] != true {
			t.Fatalf("stream = %#v, want true", body["stream"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(strings.Join([]string{
			`data: {"choices":[{"delta":{"content":"hel"},"finish_reason":null}]}`,
			`data: {"choices":[{"delta":{"content":"lo"},"finish_reason":"stop"}]}`,
			`data: {"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`,
			`data: [DONE]`,
			``,
		}, "\n\n")))
	}))
	defer mock.Close()

	got, err := NewOpenAICompatibleClient().ChatStream(t.Context(), ChatRequest{
		BaseURL: mock.URL,
		Model:   "mock-model",
		Messages: []Message{{
			Role:    "user",
			Content: "hello",
		}},
	})
	if err != nil {
		t.Fatalf("ChatStream() error = %v", err)
	}
	if got.Text != "hello" || got.FinishReason != "stop" || got.Usage.TotalTokens != 3 {
		t.Fatalf("ChatStream() = %#v", got)
	}
}

func TestOpenAICompatibleChatSupportsProviderSpecificAuthAndQuery(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("api-version"); got != "2024-10-21" {
			t.Fatalf("api-version = %q, want configured version", got)
		}
		if got := r.Header.Get("api-key"); got != "azure-key" {
			t.Fatalf("api-key = %q, want Azure key", got)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Fatalf("authorization = %q, want no bearer header", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer mock.Close()

	got, err := NewOpenAICompatibleClient().Chat(t.Context(), ChatRequest{
		BaseURL:     mock.URL + "/openai/v1",
		APIKey:      "azure-key",
		AuthHeader:  "api-key",
		QueryParams: map[string]string{"api-version": "2024-10-21"},
		Model:       "deployment",
		Messages: []Message{{
			Role:    "user",
			Content: "hello",
		}},
	})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if got.Text != "ok" {
		t.Fatalf("Chat() = %#v, want ok", got)
	}
}

func TestOpenAICompatibleChatParsesToolCalls(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices":[{
				"message":{
					"content":"",
					"tool_calls":[{
						"id":"call_1",
						"type":"function",
						"function":{"name":"read","arguments":"{\"filePath\":\"README.md\"}"}
					}]
				},
				"finish_reason":"tool_calls"
			}],
			"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}
		}`))
	}))
	defer mock.Close()

	got, err := NewOpenAICompatibleClient().Chat(t.Context(), ChatRequest{
		BaseURL: mock.URL,
		Model:   "mock-model",
		Messages: []Message{{
			Role:    "user",
			Content: "read readme",
		}},
	})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if got.FinishReason != "tool-calls" || len(got.ToolCalls) != 1 {
		t.Fatalf("Chat() = %#v, want one tool call", got)
	}
	if got.ToolCalls[0].ID != "call_1" || got.ToolCalls[0].Name != "read" || got.ToolCalls[0].Arguments["filePath"] != "README.md" {
		t.Fatalf("tool call = %#v, want read README.md", got.ToolCalls[0])
	}
}

func TestOpenAICompatibleChatSendsToolDefinitions(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if body["tool_choice"] != "auto" {
			t.Fatalf("tool_choice = %#v, want auto", body["tool_choice"])
		}
		tools := body["tools"].([]any)
		first := tools[0].(map[string]any)
		fn := first["function"].(map[string]any)
		params := fn["parameters"].(map[string]any)
		if first["type"] != "function" || fn["name"] != "read" || params["type"] != "object" {
			t.Fatalf("tools = %#v, want OpenAI function tool schema", tools)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer mock.Close()

	_, err := NewOpenAICompatibleClient().Chat(t.Context(), ChatRequest{
		BaseURL: mock.URL,
		Model:   "mock-model",
		Messages: []Message{{
			Role:    "user",
			Content: "hello",
		}},
		Tools: []ToolDefinition{{
			Name:        "read",
			Description: "Read a file",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{"filePath": map[string]any{"type": "string"}},
				"required":   []string{"filePath"},
			},
		}},
	})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
}

func TestOpenAICompatibleChatStreamParsesToolCalls(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(strings.Join([]string{
			`data: {"choices":[{"delta":{"tool_calls":[{"id":"call_1","type":"function","function":{"name":"glob","arguments":"{\"pattern\":\"*.go\"}"}}]},"finish_reason":"tool_calls"}]}`,
			`data: {"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`,
			`data: [DONE]`,
			``,
		}, "\n\n")))
	}))
	defer mock.Close()

	got, err := NewOpenAICompatibleClient().ChatStream(t.Context(), ChatRequest{
		BaseURL: mock.URL,
		Model:   "mock-model",
		Messages: []Message{{
			Role:    "user",
			Content: "find go files",
		}},
	})
	if err != nil {
		t.Fatalf("ChatStream() error = %v", err)
	}
	if got.FinishReason != "tool-calls" || len(got.ToolCalls) != 1 {
		t.Fatalf("ChatStream() = %#v, want one tool call", got)
	}
	if got.ToolCalls[0].Name != "glob" || got.ToolCalls[0].Arguments["pattern"] != "*.go" {
		t.Fatalf("tool call = %#v, want glob *.go", got.ToolCalls[0])
	}
}
