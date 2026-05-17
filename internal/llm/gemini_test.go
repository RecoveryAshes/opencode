package llm

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGeminiChatRequestAndResponse(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models/gemini-2.5-flash:streamGenerateContent" {
			t.Fatalf("path = %q, want Gemini streamGenerateContent path", r.URL.Path)
		}
		if got := r.URL.Query().Get("alt"); got != "sse" {
			t.Fatalf("alt = %q, want sse", got)
		}
		if got := r.Header.Get("x-goog-api-key"); got != "google-key" {
			t.Fatalf("x-goog-api-key = %q, want Google key", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		contents := body["contents"].([]any)
		first := contents[0].(map[string]any)
		part := first["parts"].([]any)[0].(map[string]any)
		if first["role"] != "user" || part["text"] != "hello" {
			t.Fatalf("contents = %#v, want user text part", contents)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"candidates":[{
				"content":{"role":"model","parts":[{"text":"thinking","thought":true},{"text":"hello"}]},
				"finishReason":"STOP"
			}],
			"usageMetadata":{
				"promptTokenCount":5,
				"candidatesTokenCount":2,
				"thoughtsTokenCount":1,
				"cachedContentTokenCount":1,
				"totalTokenCount":8
			}
		}`))
	}))
	defer mock.Close()

	got, err := NewGeminiClient().Chat(t.Context(), ChatRequest{
		ProviderID: "google",
		Protocol:   "gemini",
		BaseURL:    mock.URL + "/v1beta",
		APIKey:     "google-key",
		AuthHeader: "x-goog-api-key",
		Model:      "gemini-2.5-flash",
		Messages:   []Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if got.Text != "hello" || got.FinishReason != "stop" {
		t.Fatalf("Chat() = %#v, want text stop", got)
	}
	if got.Usage.InputTokens != 5 || got.Usage.OutputTokens != 3 || got.Usage.ReasoningTokens != 1 || got.Usage.CacheReadTokens != 1 || got.Usage.TotalTokens != 8 {
		t.Fatalf("usage = %#v, want Gemini token mapping", got.Usage)
	}
}

func TestGeminiChatParsesSSE(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(strings.Join([]string{
			`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"thinking","thought":true},{"text":"hel"}]}}]}`,
			`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"lo"}]},"finishReason":"MAX_TOKENS"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":4,"thoughtsTokenCount":2,"cachedContentTokenCount":1,"totalTokenCount":9}}`,
			``,
		}, "\n\n")))
	}))
	defer mock.Close()

	got, err := NewGeminiClient().Chat(t.Context(), ChatRequest{
		ProviderID: "google",
		Protocol:   "gemini",
		BaseURL:    mock.URL + "/v1beta",
		Model:      "gemini-2.5-flash",
		Messages:   []Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if got.Text != "hello" || got.FinishReason != "length" {
		t.Fatalf("Chat() = %#v, want streamed text length", got)
	}
	if got.Usage.InputTokens != 3 || got.Usage.OutputTokens != 6 || got.Usage.ReasoningTokens != 2 || got.Usage.CacheReadTokens != 1 || got.Usage.TotalTokens != 9 {
		t.Fatalf("usage = %#v, want stream usage", got.Usage)
	}
}

func TestGeminiChatSendsGenerationConfig(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		config := body["generationConfig"].(map[string]any)
		thinking := config["thinkingConfig"].(map[string]any)
		if config["maxOutputTokens"] != float64(1234) ||
			config["temperature"] != 1.0 ||
			config["topP"] != 0.95 ||
			config["topK"] != float64(64) ||
			thinking["includeThoughts"] != true ||
			thinking["thinkingLevel"] != "high" {
			t.Fatalf("generationConfig = %#v, want migrated Gemini sampling and thinking options", config)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],
			"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}
		}`))
	}))
	defer mock.Close()

	maxTokens := 1234
	temperature := 1.0
	topP := 0.95
	topK := 64
	_, err := NewGeminiClient().Chat(t.Context(), ChatRequest{
		ProviderID:  "google",
		Protocol:    "gemini",
		BaseURL:     mock.URL + "/v1beta",
		Model:       "gemini-3-pro",
		Messages:    []Message{{Role: "user", Content: "hello"}},
		MaxTokens:   &maxTokens,
		Temperature: &temperature,
		TopP:        &topP,
		TopK:        &topK,
		Options: map[string]any{
			"thinkingConfig": map[string]any{
				"includeThoughts": true,
				"thinkingLevel":   "high",
			},
		},
	})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
}

func TestGeminiChatSendsToolDefinitions(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		tools := body["tools"].([]any)
		declarations := tools[0].(map[string]any)["functionDeclarations"].([]any)
		first := declarations[0].(map[string]any)
		params := first["parameters"].(map[string]any)
		if first["name"] != "read" || params["type"] != "object" {
			t.Fatalf("tools = %#v, want Gemini function declaration", tools)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`))
	}))
	defer mock.Close()

	_, err := NewGeminiClient().Chat(t.Context(), ChatRequest{
		ProviderID: "google",
		Protocol:   "gemini",
		BaseURL:    mock.URL,
		Model:      "gemini-2.5-flash",
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

func TestProviderChatClientRoutesGemini(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"MAX_TOKENS"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":2}}`))
	}))
	defer mock.Close()

	got, err := NewProviderChatClient().Chat(t.Context(), ChatRequest{
		ProviderID: "google",
		Protocol:   "gemini",
		BaseURL:    mock.URL,
		Model:      "gemini-2.5-flash",
		Messages:   []Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if got.Text != "ok" || got.FinishReason != "length" {
		t.Fatalf("Chat() = %#v, want Gemini response", got)
	}
}

func TestGeminiChatParsesFunctionCall(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"candidates":[{
				"content":{"role":"model","parts":[{"functionCall":{"name":"read","args":{"filePath":"README.md"}}}]},
				"finishReason":"FUNCTION_CALL"
			}],
			"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":2,"totalTokenCount":3}
		}`))
	}))
	defer mock.Close()

	got, err := NewGeminiClient().Chat(t.Context(), ChatRequest{
		ProviderID: "google",
		Protocol:   "gemini",
		BaseURL:    mock.URL,
		Model:      "gemini-2.5-flash",
		Messages:   []Message{{Role: "user", Content: "read"}},
	})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if got.FinishReason != "tool-calls" || len(got.ToolCalls) != 1 {
		t.Fatalf("Chat() = %#v, want one function call", got)
	}
	if got.ToolCalls[0].Name != "read" || got.ToolCalls[0].Arguments["filePath"] != "README.md" {
		t.Fatalf("tool call = %#v, want read README.md", got.ToolCalls[0])
	}
}

func TestGeminiChatStreamParsesFunctionCall(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(strings.Join([]string{
			`data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"glob","args":{"pattern":"*.go"}}}]},"finishReason":"FUNCTION_CALL"}]}`,
			``,
		}, "\n\n")))
	}))
	defer mock.Close()

	got, err := NewGeminiClient().Chat(t.Context(), ChatRequest{
		ProviderID: "google",
		Protocol:   "gemini",
		BaseURL:    mock.URL,
		Model:      "gemini-2.5-flash",
		Messages:   []Message{{Role: "user", Content: "glob"}},
	})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if got.FinishReason != "tool-calls" || len(got.ToolCalls) != 1 {
		t.Fatalf("Chat() = %#v, want one streamed function call", got)
	}
	if got.ToolCalls[0].Name != "glob" || got.ToolCalls[0].Arguments["pattern"] != "*.go" {
		t.Fatalf("tool call = %#v, want glob *.go", got.ToolCalls[0])
	}
}
