package llm

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
)

func TestBedrockChatRequestAndResponse(t *testing.T) {
	clearBedrockAuthEnv(t)
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/model/us.amazon.nova-micro-v1:0/converse-stream" {
			t.Fatalf("path = %q, want Bedrock converse-stream path", r.URL.Path)
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

func TestBedrockChatParsesEventStream(t *testing.T) {
	clearBedrockAuthEnv(t)
	body := encodeBedrockEventStream(t,
		bedrockEventFrame("messageStart", `{"role":"assistant"}`),
		bedrockEventFrame("contentBlockDelta", `{"contentBlockIndex":0,"delta":{"text":"hel"},"p":"padding"}`),
		bedrockEventFrame("contentBlockDelta", `{"contentBlockIndex":0,"delta":{"text":"lo"}}`),
		bedrockEventFrame("messageStop", `{"stopReason":"max_tokens"}`),
		bedrockEventFrame("metadata", `{"usage":{"inputTokens":5,"outputTokens":6,"totalTokens":11,"cacheReadInputTokens":2}}`),
	)
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Path; got != "/model/us.amazon.nova-micro-v1:0/converse-stream" {
			t.Fatalf("path = %q, want Bedrock converse-stream path", got)
		}
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		_, _ = w.Write(body)
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
	if got.Text != "hello" || got.FinishReason != "length" {
		t.Fatalf("Chat() = %#v, want streamed text length", got)
	}
	if got.Usage.InputTokens != 5 || got.Usage.OutputTokens != 6 || got.Usage.TotalTokens != 11 || got.Usage.CacheReadTokens != 2 {
		t.Fatalf("usage = %#v, want event-stream usage", got.Usage)
	}
}

func TestBedrockChatEventStreamProviderError(t *testing.T) {
	clearBedrockAuthEnv(t)
	body := encodeBedrockEventStream(t,
		bedrockEventFrame("throttlingException", `{"message":"Slow down"}`),
	)
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		_, _ = w.Write(body)
	}))
	defer mock.Close()

	_, err := NewBedrockClient().Chat(t.Context(), ChatRequest{
		ProviderID: "amazon-bedrock",
		Protocol:   "bedrock-converse",
		BaseURL:    mock.URL,
		APIKey:     "bedrock-token",
		Model:      "us.amazon.nova-micro-v1:0",
		Messages:   []Message{{Role: "user", Content: "hello"}},
	})
	if err == nil || !strings.Contains(err.Error(), "Slow down") {
		t.Fatalf("Chat() error = %v, want provider error", err)
	}
}

func TestBedrockSignsWithStaticAWSCredentials(t *testing.T) {
	clearBedrockAuthEnv(t)
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); !strings.HasPrefix(got, "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/") {
			t.Fatalf("authorization = %q, want SigV4 credential scope", got)
		}
		if got := r.Header.Get("Authorization"); !strings.Contains(got, "/eu-west-1/bedrock/aws4_request") {
			t.Fatalf("authorization = %q, want Bedrock region/service scope", got)
		}
		if got := r.Header.Get("X-Amz-Security-Token"); got != "session-token" {
			t.Fatalf("x-amz-security-token = %q, want session token", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request: %v", err)
		}
		hash := sha256.Sum256(body)
		if got, want := r.Header.Get("X-Amz-Content-Sha256"), hex.EncodeToString(hash[:]); got != want {
			t.Fatalf("x-amz-content-sha256 = %q, want %q", got, want)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"output":{"message":{"content":[{"text":"signed"}]}},"stopReason":"end_turn"}`))
	}))
	defer mock.Close()

	got, err := NewBedrockClient().Chat(t.Context(), ChatRequest{
		ProviderID: "amazon-bedrock",
		Protocol:   "bedrock-converse",
		BaseURL:    mock.URL,
		Model:      "us.amazon.nova-micro-v1:0",
		Messages:   []Message{{Role: "user", Content: "hello"}},
		AWSCredentials: &AWSCredentials{
			Region:          "eu-west-1",
			AccessKeyID:     "AKIAIOSFODNN7EXAMPLE",
			SecretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
			SessionToken:    "session-token",
		},
	})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if got.Text != "signed" || got.FinishReason != "stop" {
		t.Fatalf("Chat() = %#v, want signed response", got)
	}
}

type bedrockStreamTestFrame struct {
	eventType string
	payload   []byte
}

func bedrockEventFrame(eventType string, payload string) bedrockStreamTestFrame {
	return bedrockStreamTestFrame{eventType: eventType, payload: []byte(payload)}
}

func encodeBedrockEventStream(t *testing.T, frames ...bedrockStreamTestFrame) []byte {
	t.Helper()

	var output bytes.Buffer
	encoder := eventstream.NewEncoder()
	for _, frame := range frames {
		headers := eventstream.Headers{}
		headers.Set(":message-type", eventstream.StringValue("event"))
		headers.Set(":event-type", eventstream.StringValue(frame.eventType))
		headers.Set(":content-type", eventstream.StringValue("application/json"))
		if err := encoder.Encode(&output, eventstream.Message{Headers: headers, Payload: frame.payload}); err != nil {
			t.Fatalf("encode event-stream frame: %v", err)
		}
	}
	return output.Bytes()
}

func TestBedrockRequiresBearerTokenOrAWSCredentials(t *testing.T) {
	clearBedrockAuthEnv(t)
	_, err := NewBedrockClient().Chat(t.Context(), ChatRequest{
		ProviderID: "amazon-bedrock",
		Protocol:   "bedrock-converse",
		BaseURL:    "https://bedrock-runtime.us-east-1.amazonaws.com",
		Model:      "us.amazon.nova-micro-v1:0",
		Messages:   []Message{{Role: "user", Content: "hello"}},
	})
	if err == nil || !strings.Contains(err.Error(), "AWS credentials") {
		t.Fatalf("Chat() error = %v, want auth requirement", err)
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
