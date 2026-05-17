package llm

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGeminiChatRequestAndResponse(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models/gemini-2.5-flash:generateContent" {
			t.Fatalf("path = %q, want Gemini generateContent path", r.URL.Path)
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
