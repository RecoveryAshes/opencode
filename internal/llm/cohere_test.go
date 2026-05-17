package llm

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCohereChatRequestAndResponse(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/chat" {
			t.Fatalf("path = %q, want /v2/chat", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer cohere-key" {
			t.Fatalf("authorization = %q, want bearer token", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if body["model"] != "command-r" {
			t.Fatalf("model = %#v, want command-r", body["model"])
		}
		messages := body["messages"].([]any)
		first := messages[0].(map[string]any)
		if first["role"] != "user" || first["content"] != "hello" {
			t.Fatalf("messages = %#v, want user hello", messages)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"message":{"content":[{"type":"text","text":"hel"},{"type":"text","text":"lo"}]},
			"finish_reason":"COMPLETE",
			"usage":{"tokens":{"input_tokens":3,"output_tokens":4,"total_tokens":7}}
		}`))
	}))
	defer mock.Close()

	got, err := NewCohereClient().Chat(t.Context(), ChatRequest{
		ProviderID: "cohere",
		Protocol:   "cohere-chat",
		BaseURL:    mock.URL + "/v2",
		APIKey:     "cohere-key",
		AuthHeader: "Authorization",
		AuthScheme: "Bearer",
		Model:      "command-r",
		Messages:   []Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if got.Text != "hello" || got.FinishReason != "stop" {
		t.Fatalf("Chat() = %#v, want text stop", got)
	}
	if got.Usage.InputTokens != 3 || got.Usage.OutputTokens != 4 || got.Usage.TotalTokens != 7 {
		t.Fatalf("usage = %#v, want Cohere usage", got.Usage)
	}
}

func TestProviderChatClientRoutesCohere(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":{"content":[{"type":"text","text":"ok"}]},"finish_reason":"MAX_TOKENS","usage":{"tokens":{"input_tokens":1,"output_tokens":2}}}`))
	}))
	defer mock.Close()

	got, err := NewProviderChatClient().Chat(t.Context(), ChatRequest{
		ProviderID: "cohere",
		Protocol:   "cohere-chat",
		BaseURL:    mock.URL,
		APIKey:     "cohere-key",
		Model:      "command-r",
		Messages:   []Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if got.Text != "ok" || got.FinishReason != "length" {
		t.Fatalf("Chat() = %#v, want Cohere response", got)
	}
}
