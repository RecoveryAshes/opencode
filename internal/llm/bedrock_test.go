package llm

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBedrockChatRequestAndResponse(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/model/us.amazon.nova-micro-v1:0/converse" {
			t.Fatalf("path = %q, want Bedrock converse path", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer bedrock-token" {
			t.Fatalf("authorization = %q, want bearer token", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if body["modelId"] != "us.amazon.nova-micro-v1:0" {
			t.Fatalf("modelId = %#v, want model id", body["modelId"])
		}
		messages := body["messages"].([]any)
		first := messages[0].(map[string]any)
		content := first["content"].([]any)[0].(map[string]any)
		if first["role"] != "user" || content["text"] != "hello" {
			t.Fatalf("messages = %#v, want user text block", messages)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"output":{"message":{"role":"assistant","content":[{"text":"hel"},{"text":"lo"}]}},
			"stopReason":"end_turn",
			"usage":{
				"inputTokens":3,
				"outputTokens":4,
				"totalTokens":7,
				"cacheReadInputTokens":1,
				"cacheWriteInputTokens":2
			}
		}`))
	}))
	defer mock.Close()

	got, err := NewBedrockClient().Chat(t.Context(), ChatRequest{
		ProviderID: "amazon-bedrock",
		Protocol:   "bedrock-converse",
		BaseURL:    mock.URL,
		APIKey:     "bedrock-token",
		AuthHeader: "Authorization",
		AuthScheme: "Bearer",
		Model:      "us.amazon.nova-micro-v1:0",
		Messages:   []Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if got.Text != "hello" || got.FinishReason != "stop" {
		t.Fatalf("Chat() = %#v, want text stop", got)
	}
	if got.Usage.InputTokens != 3 || got.Usage.OutputTokens != 4 || got.Usage.TotalTokens != 7 {
		t.Fatalf("usage = %#v, want Bedrock token mapping", got.Usage)
	}
	if got.Usage.CacheReadTokens != 1 || got.Usage.CacheWriteTokens != 2 {
		t.Fatalf("cache usage = %#v, want read/write", got.Usage)
	}
}

func TestBedrockRequiresBearerTokenUntilSigV4Migrates(t *testing.T) {
	_, err := NewBedrockClient().Chat(t.Context(), ChatRequest{
		ProviderID: "amazon-bedrock",
		Protocol:   "bedrock-converse",
		BaseURL:    "https://bedrock-runtime.us-east-1.amazonaws.com",
		Model:      "us.amazon.nova-micro-v1:0",
		Messages:   []Message{{Role: "user", Content: "hello"}},
	})
	if err == nil || !strings.Contains(err.Error(), "SigV4 signing is migrated") {
		t.Fatalf("Chat() error = %v, want SigV4 migration error", err)
	}
}

func TestProviderChatClientRoutesBedrock(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"output":{"message":{"content":[{"text":"ok"}]}},"stopReason":"max_tokens","usage":{"inputTokens":1,"outputTokens":2}}`))
	}))
	defer mock.Close()

	got, err := NewProviderChatClient().Chat(t.Context(), ChatRequest{
		ProviderID: "amazon-bedrock",
		Protocol:   "bedrock-converse",
		BaseURL:    mock.URL,
		APIKey:     "bedrock-token",
		Model:      "us.amazon.nova-micro-v1:0",
		Messages:   []Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if got.Text != "ok" || got.FinishReason != "length" {
		t.Fatalf("Chat() = %#v, want Bedrock response", got)
	}
}
