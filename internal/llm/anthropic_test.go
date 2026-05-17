package llm

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAnthropicChatRequestAndResponse(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Fatalf("path = %q, want /v1/messages", r.URL.Path)
		}
		if got := r.Header.Get("x-api-key"); got != "anthropic-key" {
			t.Fatalf("x-api-key = %q, want Anthropic key", got)
		}
		if got := r.Header.Get("anthropic-version"); got != "2023-06-01" {
			t.Fatalf("anthropic-version = %q, want 2023-06-01", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if body["model"] != "claude-sonnet-4-5" || body["max_tokens"] != float64(64) || body["stream"] != true {
			t.Fatalf("body = %#v, want model, stream, and max_tokens", body)
		}
		messages := body["messages"].([]any)
		first := messages[0].(map[string]any)
		content := first["content"].([]any)[0].(map[string]any)
		if first["role"] != "user" || content["type"] != "text" || content["text"] != "hello" {
			t.Fatalf("messages = %#v, want user text block", messages)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"content":[{"type":"text","text":"hel"},{"type":"text","text":"lo"}],
			"stop_reason":"end_turn",
			"usage":{
				"input_tokens":3,
				"output_tokens":4,
				"cache_creation_input_tokens":2,
				"cache_read_input_tokens":1
			}
		}`))
	}))
	defer mock.Close()

	maxTokens := 64
	got, err := NewAnthropicClient().Chat(t.Context(), ChatRequest{
		ProviderID:  "anthropic",
		Protocol:    "anthropic-messages",
		BaseURL:     mock.URL + "/v1",
		APIKey:      "anthropic-key",
		AuthHeader:  "x-api-key",
		Model:       "claude-sonnet-4-5",
		MaxTokens:   &maxTokens,
		Headers:     map[string]string{"anthropic-version": "2023-06-01"},
		Messages:    []Message{{Role: "user", Content: "hello"}},
		Temperature: nil,
	})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if got.Text != "hello" || got.FinishReason != "stop" {
		t.Fatalf("Chat() = %#v, want text stop", got)
	}
	if got.Usage.InputTokens != 6 || got.Usage.OutputTokens != 4 || got.Usage.TotalTokens != 10 {
		t.Fatalf("usage = %#v, want inclusive input/total", got.Usage)
	}
	if got.Usage.CacheReadTokens != 1 || got.Usage.CacheWriteTokens != 2 {
		t.Fatalf("cache usage = %#v, want read/write", got.Usage)
	}
}

func TestAnthropicChatParsesSSE(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(strings.Join([]string{
			`event: message_start`,
			`data: {"type":"message_start","message":{"usage":{"input_tokens":2,"cache_read_input_tokens":1}}}`,
			`event: content_block_delta`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hel"}}`,
			`event: content_block_delta`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo"}}`,
			`event: message_delta`,
			`data: {"type":"message_delta","delta":{"stop_reason":"max_tokens"},"usage":{"output_tokens":4,"cache_creation_input_tokens":3}}`,
			`event: message_stop`,
			`data: {"type":"message_stop"}`,
			``,
		}, "\n")))
	}))
	defer mock.Close()

	got, err := NewAnthropicClient().Chat(t.Context(), ChatRequest{
		ProviderID: "anthropic",
		Protocol:   "anthropic-messages",
		BaseURL:    mock.URL,
		Model:      "claude-sonnet-4-5",
		Messages:   []Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if got.Text != "hello" || got.FinishReason != "length" {
		t.Fatalf("Chat() = %#v, want streamed text length", got)
	}
	if got.Usage.InputTokens != 6 || got.Usage.OutputTokens != 4 || got.Usage.CacheReadTokens != 1 || got.Usage.CacheWriteTokens != 3 || got.Usage.TotalTokens != 10 {
		t.Fatalf("usage = %#v, want merged stream usage", got.Usage)
	}
}

func TestProviderChatClientRoutesAnthropic(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"stop_reason":"max_tokens","usage":{"input_tokens":1,"output_tokens":2}}`))
	}))
	defer mock.Close()

	got, err := NewProviderChatClient().Chat(t.Context(), ChatRequest{
		ProviderID: "anthropic",
		Protocol:   "anthropic-messages",
		BaseURL:    mock.URL,
		Messages:   []Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if got.Text != "ok" || got.FinishReason != "length" {
		t.Fatalf("Chat() = %#v, want Anthropic response", got)
	}
}
