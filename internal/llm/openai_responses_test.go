package llm

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResponsesChatRequestAndJSONResponse(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("path = %q, want /v1/responses", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer openai-key" {
			t.Fatalf("authorization = %q, want bearer token", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if body["model"] != "gpt-5.2" || body["stream"] != false {
			t.Fatalf("body = %#v, want model and non-streaming request", body)
		}
		if body["temperature"] != 0.2 || body["top_p"] != 0.8 || body["top_k"] != float64(40) || body["max_output_tokens"] != float64(1234) {
			t.Fatalf("body = %#v, want sampling params", body)
		}
		input := body["input"].([]any)
		first := input[0].(map[string]any)
		content := first["content"].([]any)[0].(map[string]any)
		if first["role"] != "user" || content["type"] != "input_text" || content["text"] != "hello" {
			t.Fatalf("input = %#v, want user input_text", input)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"output":[{"type":"message","content":[{"type":"output_text","text":"world"}]}],
			"usage":{
				"input_tokens":3,
				"output_tokens":4,
				"total_tokens":7,
				"input_tokens_details":{"cached_tokens":1},
				"output_tokens_details":{"reasoning_tokens":2}
			}
		}`))
	}))
	defer mock.Close()

	got, err := NewResponsesClient().Chat(t.Context(), ChatRequest{
		ProviderID: "openai",
		Protocol:   "openai-responses",
		BaseURL:    mock.URL + "/v1",
		APIKey:     "openai-key",
		AuthHeader: "Authorization",
		AuthScheme: "Bearer",
		Model:      "gpt-5.2",
		Messages:   []Message{{Role: "user", Content: "hello"}},
		Temperature: func() *float64 {
			value := 0.2
			return &value
		}(),
		TopP: func() *float64 {
			value := 0.8
			return &value
		}(),
		TopK: func() *int {
			value := 40
			return &value
		}(),
		MaxTokens: func() *int {
			value := 1234
			return &value
		}(),
	})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if got.Text != "world" || got.FinishReason != "stop" {
		t.Fatalf("Chat() = %#v, want text stop", got)
	}
	if got.Usage.InputTokens != 3 || got.Usage.OutputTokens != 4 || got.Usage.CacheReadTokens != 1 || got.Usage.ReasoningTokens != 2 || got.Usage.TotalTokens != 7 {
		t.Fatalf("usage = %#v, want Responses usage", got.Usage)
	}
}

func TestResponsesChatSendsConfiguredBodyOptions(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if body["store"] != false || body["reasoningSummary"] != "auto" || body["promptCacheKey"] != "ses_123" {
			t.Fatalf("body = %#v, want configured Responses options", body)
		}
		include := body["include"].([]any)
		if len(include) != 1 || include[0] != "reasoning.encrypted_content" {
			t.Fatalf("include = %#v, want encrypted reasoning include", include)
		}
		for _, forbidden := range []string{"apiKey", "baseURL", "headers", "timeout", "includeUsage"} {
			if _, ok := body[forbidden]; ok {
				t.Fatalf("body = %#v, did not want transport option %q", body, forbidden)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"output_text":"ok"}`))
	}))
	defer mock.Close()

	_, err := NewResponsesClient().Chat(t.Context(), ChatRequest{
		ProviderID: "openai",
		Protocol:   "openai-responses",
		BaseURL:    mock.URL,
		Model:      "gpt-5.2",
		Messages:   []Message{{Role: "user", Content: "hello"}},
		Options: map[string]any{
			"apiKey":           "transport-key",
			"baseURL":          "https://transport.example/v1",
			"headers":          map[string]any{"X-Test": "transport"},
			"timeout":          300000,
			"includeUsage":     true,
			"store":            false,
			"reasoningSummary": "auto",
			"promptCacheKey":   "ses_123",
			"include":          []any{"reasoning.encrypted_content"},
		},
	})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
}

func TestResponsesChatParsesSSE(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(strings.Join([]string{
			`data: {"type":"response.output_text.delta","delta":"hel"}`,
			`data: {"type":"response.output_text.delta","delta":"lo"}`,
			`data: {"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}`,
			`data: [DONE]`,
			``,
		}, "\n\n")))
	}))
	defer mock.Close()

	got, err := NewResponsesClient().Chat(t.Context(), ChatRequest{
		ProviderID: "openai",
		Protocol:   "openai-responses",
		BaseURL:    mock.URL,
		Model:      "gpt-5.2",
		Messages:   []Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if got.Text != "hello" || got.Usage.TotalTokens != 3 {
		t.Fatalf("Chat() = %#v, want streamed response", got)
	}
}

func TestResponsesChatParsesFunctionCall(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"output":[{
				"type":"function_call",
				"id":"fc_1",
				"call_id":"call_1",
				"name":"read",
				"arguments":"{\"filePath\":\"README.md\"}"
			}],
			"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}
		}`))
	}))
	defer mock.Close()

	got, err := NewResponsesClient().Chat(t.Context(), ChatRequest{
		ProviderID: "openai",
		Protocol:   "openai-responses",
		BaseURL:    mock.URL,
		Model:      "gpt-5.2",
		Messages:   []Message{{Role: "user", Content: "read"}},
	})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if got.FinishReason != "stop" || len(got.ToolCalls) != 1 {
		t.Fatalf("Chat() = %#v, want one Responses function call", got)
	}
	if got.ToolCalls[0].ID != "call_1" || got.ToolCalls[0].Name != "read" || got.ToolCalls[0].Arguments["filePath"] != "README.md" {
		t.Fatalf("tool call = %#v, want read README.md", got.ToolCalls[0])
	}
}

func TestResponsesChatStreamParsesFunctionCall(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(strings.Join([]string{
			`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"glob","arguments":""}}`,
			`data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"pattern\""}`,
			`data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":":\"*.go\"}"}`,
			`data: {"type":"response.function_call_arguments.done","output_index":0,"item_id":"fc_1","call_id":"call_1","name":"glob","arguments":"{\"pattern\":\"*.go\"}"}`,
			`data: {"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}`,
			`data: [DONE]`,
			``,
		}, "\n\n")))
	}))
	defer mock.Close()

	got, err := NewResponsesClient().Chat(t.Context(), ChatRequest{
		ProviderID: "openai",
		Protocol:   "openai-responses",
		BaseURL:    mock.URL,
		Model:      "gpt-5.2",
		Messages:   []Message{{Role: "user", Content: "glob"}},
	})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if got.FinishReason != "stop" || got.Usage.TotalTokens != 3 || len(got.ToolCalls) != 1 {
		t.Fatalf("Chat() = %#v, want streamed Responses function call", got)
	}
	if got.ToolCalls[0].ID != "call_1" || got.ToolCalls[0].Name != "glob" || got.ToolCalls[0].Arguments["pattern"] != "*.go" {
		t.Fatalf("tool call = %#v, want glob *.go", got.ToolCalls[0])
	}
}

func TestResponsesChatSupportsAzureAuthAndQuery(t *testing.T) {
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
		_, _ = w.Write([]byte(`{"output_text":"ok"}`))
	}))
	defer mock.Close()

	got, err := NewResponsesClient().Chat(t.Context(), ChatRequest{
		ProviderID:  "azure",
		Protocol:    "openai-responses",
		BaseURL:     mock.URL + "/openai/v1",
		APIKey:      "azure-key",
		AuthHeader:  "api-key",
		QueryParams: map[string]string{"api-version": "2024-10-21"},
		Model:       "deployment",
		Messages:    []Message{{Role: "user", Content: "hello"}},
		Temperature: nil,
		MaxTokens:   nil,
	})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if got.Text != "ok" {
		t.Fatalf("Chat() = %#v, want ok", got)
	}
}

func TestResponsesChatSendsToolDefinitions(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		tools := body["tools"].([]any)
		first := tools[0].(map[string]any)
		params := first["parameters"].(map[string]any)
		if first["type"] != "function" || first["name"] != "read" || params["type"] != "object" {
			t.Fatalf("tools = %#v, want Responses function tool schema", tools)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"output_text":"ok"}`))
	}))
	defer mock.Close()

	_, err := NewResponsesClient().Chat(t.Context(), ChatRequest{
		ProviderID: "openai",
		Protocol:   "openai-responses",
		BaseURL:    mock.URL,
		Model:      "gpt-5.2",
		Messages:   []Message{{Role: "user", Content: "hello"}},
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

func TestProviderChatClientRoutesResponses(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"output_text":"ok","incomplete_details":{"reason":"max_output_tokens"},"usage":{"input_tokens":1,"output_tokens":2}}`))
	}))
	defer mock.Close()

	got, err := NewProviderChatClient().Chat(t.Context(), ChatRequest{
		ProviderID: "openai",
		Protocol:   "openai-responses",
		BaseURL:    mock.URL,
		Messages:   []Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if got.Text != "ok" || got.FinishReason != "length" {
		t.Fatalf("Chat() = %#v, want Responses response", got)
	}
}
