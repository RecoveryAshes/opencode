package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// BedrockClient calls Amazon Bedrock Converse.
type BedrockClient struct {
	HTTPClient *http.Client
}

// NewBedrockClient creates a Bedrock client with production-safe defaults.
func NewBedrockClient() *BedrockClient {
	return &BedrockClient{
		HTTPClient: &http.Client{Timeout: 2 * time.Minute},
	}
}

// Chat sends a text-only Bedrock Converse request. SigV4 and binary event
// stream support are separate migration slices; this path supports Bedrock
// bearer token auth.
func (client *BedrockClient) Chat(ctx context.Context, request ChatRequest) (ChatResponse, error) {
	if request.APIKey == "" {
		return ChatResponse{}, fmt.Errorf("bedrock converse requires bearer token auth until SigV4 signing is migrated")
	}
	if len(request.Messages) == 0 {
		return ChatResponse{}, fmt.Errorf("at least one message is required")
	}
	body := bedrockRequest{
		ModelID:  defaultString(request.Model, "us.amazon.nova-micro-v1:0"),
		Messages: make([]bedrockMessage, 0, len(request.Messages)),
	}
	if request.MaxTokens != nil || request.Temperature != nil {
		body.InferenceConfig = &bedrockInferenceConfig{
			MaxTokens:   request.MaxTokens,
			Temperature: request.Temperature,
		}
	}
	for _, message := range request.Messages {
		if message.Role == "" || message.Content == "" {
			continue
		}
		body.Messages = append(body.Messages, bedrockMessage{
			Role: bedrockRole(message.Role),
			Content: []bedrockContentBlock{{
				Text: message.Content,
			}},
		})
	}
	if len(body.Messages) == 0 {
		return ChatResponse{}, fmt.Errorf("at least one non-empty message is required")
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("encode Bedrock request: %w", err)
	}

	endpoint, err := url.JoinPath(strings.TrimRight(request.BaseURL, "/"), "model", body.ModelID, "converse")
	if err != nil {
		return ChatResponse{}, fmt.Errorf("build Bedrock endpoint: %w", err)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return ChatResponse{}, fmt.Errorf("build Bedrock request: %w", err)
	}
	applyBedrockHeaders(httpRequest, request)

	httpClient := client.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	response, err := httpClient.Do(httpRequest)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("send Bedrock request: %w", err)
	}
	defer func() {
		_ = response.Body.Close()
	}()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("read Bedrock response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return ChatResponse{}, fmt.Errorf("bedrock response status %d: %s", response.StatusCode, strings.TrimSpace(string(data)))
	}
	return decodeBedrockResponse(data)
}

type bedrockRequest struct {
	ModelID         string                  `json:"modelId"`
	Messages        []bedrockMessage        `json:"messages"`
	InferenceConfig *bedrockInferenceConfig `json:"inferenceConfig,omitempty"`
}

type bedrockMessage struct {
	Role    string                `json:"role"`
	Content []bedrockContentBlock `json:"content"`
}

type bedrockContentBlock struct {
	Text string `json:"text,omitempty"`
}

type bedrockInferenceConfig struct {
	MaxTokens   *int     `json:"maxTokens,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
}

type bedrockResponse struct {
	Output struct {
		Message bedrockMessage `json:"message"`
	} `json:"output"`
	StopReason string       `json:"stopReason"`
	Usage      bedrockUsage `json:"usage"`
}

type bedrockUsage struct {
	InputTokens           int `json:"inputTokens"`
	OutputTokens          int `json:"outputTokens"`
	TotalTokens           int `json:"totalTokens"`
	CacheReadInputTokens  int `json:"cacheReadInputTokens"`
	CacheWriteInputTokens int `json:"cacheWriteInputTokens"`
}

func decodeBedrockResponse(data []byte) (ChatResponse, error) {
	var response bedrockResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return ChatResponse{}, fmt.Errorf("decode Bedrock response: %w", err)
	}
	var text strings.Builder
	for _, block := range response.Output.Message.Content {
		text.WriteString(block.Text)
	}
	if text.Len() == 0 {
		return ChatResponse{}, fmt.Errorf("bedrock response did not include text content")
	}
	return ChatResponse{
		Text:         text.String(),
		FinishReason: mapBedrockFinishReason(response.StopReason),
		Usage:        mapBedrockUsage(response.Usage),
	}, nil
}

func mapBedrockUsage(usage bedrockUsage) Usage {
	total := usage.TotalTokens
	if total == 0 {
		total = usage.InputTokens + usage.OutputTokens
	}
	return Usage{
		InputTokens:      usage.InputTokens,
		OutputTokens:     usage.OutputTokens,
		CacheReadTokens:  usage.CacheReadInputTokens,
		CacheWriteTokens: usage.CacheWriteInputTokens,
		TotalTokens:      total,
	}
}

func mapBedrockFinishReason(reason string) string {
	switch reason {
	case "", "end_turn", "stop_sequence":
		return "stop"
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool-calls"
	case "content_filtered", "guardrail_intervened":
		return "content-filter"
	default:
		return "unknown"
	}
}

func applyBedrockHeaders(httpRequest *http.Request, request ChatRequest) {
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "application/json")
	for key, value := range request.Headers {
		if key == "" || value == "" {
			continue
		}
		httpRequest.Header.Set(key, value)
	}
	if request.APIKey != "" {
		value := request.APIKey
		if !strings.HasPrefix(strings.ToLower(value), "bearer ") {
			value = "Bearer " + value
		}
		httpRequest.Header.Set(defaultString(request.AuthHeader, "Authorization"), value)
	}
}

func bedrockRole(role string) string {
	if role == "assistant" {
		return "assistant"
	}
	return "user"
}
