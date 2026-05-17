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
