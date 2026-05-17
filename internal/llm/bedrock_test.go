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

func TestBedrockChatSendsToolDefinitions(t *testing.T) {
	clearBedrockAuthEnv(t)
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		toolConfig := body["toolConfig"].(map[string]any)
		tools := toolConfig["tools"].([]any)
		spec := tools[0].(map[string]any)["toolSpec"].(map[string]any)
		inputSchema := spec["inputSchema"].(map[string]any)
		params := inputSchema["json"].(map[string]any)
		if spec["name"] != "read" || params["type"] != "object" {
			t.Fatalf("toolConfig = %#v, want Bedrock toolSpec schema", toolConfig)
		}
		if _, ok := toolConfig["toolChoice"].(map[string]any)["auto"]; !ok {
			t.Fatalf("toolChoice = %#v, want auto", toolConfig["toolChoice"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"output":{"message":{"content":[{"text":"ok"}]}},"stopReason":"end_turn"}`))
	}))
	defer mock.Close()

	_, err := NewBedrockClient().Chat(t.Context(), ChatRequest{
		ProviderID: "amazon-bedrock",
		Protocol:   "bedrock-converse",
		BaseURL:    mock.URL,
		APIKey:     "bedrock-token",
		Model:      "us.amazon.nova-micro-v1:0",
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

func TestBedrockChatSendsInferenceConfig(t *testing.T) {
	clearBedrockAuthEnv(t)
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		config := body["inferenceConfig"].(map[string]any)
		if config["maxTokens"] != float64(1234) ||
			config["temperature"] != 0.6 ||
			config["topP"] != 0.95 {
			t.Fatalf("inferenceConfig = %#v, want maxTokens, temperature, and topP", config)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"output":{"message":{"content":[{"text":"ok"}]}},"stopReason":"end_turn"}`))
	}))
	defer mock.Close()

	maxTokens := 1234
	temperature := 0.6
	topP := 0.95
	_, err := NewBedrockClient().Chat(t.Context(), ChatRequest{
		ProviderID:  "amazon-bedrock",
		Protocol:    "bedrock-converse",
		BaseURL:     mock.URL,
		APIKey:      "bedrock-token",
		Model:       "us.amazon.nova-micro-v1:0",
		Messages:    []Message{{Role: "user", Content: "hello"}},
		MaxTokens:   &maxTokens,
		Temperature: &temperature,
		TopP:        &topP,
	})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
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

func TestBedrockChatParsesToolUse(t *testing.T) {
	clearBedrockAuthEnv(t)
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"output":{"message":{"role":"assistant","content":[{"toolUse":{"toolUseId":"toolu_1","name":"read","input":{"filePath":"README.md"}}}]}},
			"stopReason":"tool_use",
			"usage":{"inputTokens":1,"outputTokens":2,"totalTokens":3}
		}`))
	}))
	defer mock.Close()

	got, err := NewBedrockClient().Chat(t.Context(), ChatRequest{
		ProviderID: "amazon-bedrock",
		Protocol:   "bedrock-converse",
		BaseURL:    mock.URL,
		APIKey:     "bedrock-token",
		Model:      "us.amazon.nova-micro-v1:0",
		Messages:   []Message{{Role: "user", Content: "read"}},
	})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if got.FinishReason != "tool-calls" || len(got.ToolCalls) != 1 {
		t.Fatalf("Chat() = %#v, want one Bedrock tool use", got)
	}
	if got.ToolCalls[0].ID != "toolu_1" || got.ToolCalls[0].Name != "read" || got.ToolCalls[0].Arguments["filePath"] != "README.md" {
		t.Fatalf("tool call = %#v, want read README.md", got.ToolCalls[0])
	}
}

func TestBedrockChatEventStreamParsesToolUse(t *testing.T) {
	clearBedrockAuthEnv(t)
	body := encodeBedrockEventStream(t,
		bedrockEventFrame("contentBlockStart", `{"contentBlockIndex":0,"start":{"toolUse":{"toolUseId":"toolu_1","name":"glob"}}}`),
		bedrockEventFrame("contentBlockDelta", `{"contentBlockIndex":0,"delta":{"toolUse":{"input":"{\"pattern\""}}}`),
		bedrockEventFrame("contentBlockDelta", `{"contentBlockIndex":0,"delta":{"toolUse":{"input":":\"*.go\"}"}}}`),
		bedrockEventFrame("messageStop", `{"stopReason":"tool_use"}`),
		bedrockEventFrame("metadata", `{"usage":{"inputTokens":1,"outputTokens":2,"totalTokens":3}}`),
	)
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		_, _ = w.Write(body)
	}))
	defer mock.Close()

	got, err := NewBedrockClient().Chat(t.Context(), ChatRequest{
		ProviderID: "amazon-bedrock",
		Protocol:   "bedrock-converse",
		BaseURL:    mock.URL,
		APIKey:     "bedrock-token",
		Model:      "us.amazon.nova-micro-v1:0",
		Messages:   []Message{{Role: "user", Content: "glob"}},
	})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if got.FinishReason != "tool-calls" || got.Usage.TotalTokens != 3 || len(got.ToolCalls) != 1 {
		t.Fatalf("Chat() = %#v, want streamed Bedrock tool use", got)
	}
	if got.ToolCalls[0].ID != "toolu_1" || got.ToolCalls[0].Name != "glob" || got.ToolCalls[0].Arguments["pattern"] != "*.go" {
		t.Fatalf("tool call = %#v, want glob *.go", got.ToolCalls[0])
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
