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
