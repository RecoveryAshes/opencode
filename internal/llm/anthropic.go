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

const defaultAnthropicBaseURL = "https://api.anthropic.com/v1"

// AnthropicClient calls Anthropic's Messages API.
type AnthropicClient struct {
	HTTPClient *http.Client
}

// NewAnthropicClient creates an Anthropic Messages client with production-safe defaults.
func NewAnthropicClient() *AnthropicClient {
	return &AnthropicClient{
		HTTPClient: &http.Client{Timeout: 2 * time.Minute},
	}
}

// Chat sends a text-only Anthropic Messages request and parses the non-stream
// response. Streaming and tool blocks are separate migration slices.
func (client *AnthropicClient) Chat(ctx context.Context, request ChatRequest) (ChatResponse, error) {
	if len(request.Messages) == 0 {
		return ChatResponse{}, fmt.Errorf("at least one message is required")
	}
	body := anthropicRequest{
		Model:     defaultString(request.Model, "claude-sonnet-4-5"),
		Messages:  make([]anthropicMessage, 0, len(request.Messages)),
		MaxTokens: defaultInt(request.MaxTokens, 4096),
	}
	if request.Temperature != nil {
		body.Temperature = request.Temperature
	}
	for _, message := range request.Messages {
		if message.Role == "" || message.Content == "" {
			continue
		}
		body.Messages = append(body.Messages, anthropicMessage{
			Role: message.Role,
			Content: []anthropicContentBlock{{
				Type: "text",
				Text: message.Content,
			}},
		})
	}
	if len(body.Messages) == 0 {
		return ChatResponse{}, fmt.Errorf("at least one non-empty message is required")
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("encode Anthropic request: %w", err)
	}

	endpoint, err := url.JoinPath(strings.TrimRight(defaultString(request.BaseURL, defaultAnthropicBaseURL), "/"), "messages")
	if err != nil {
		return ChatResponse{}, fmt.Errorf("build Anthropic endpoint: %w", err)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return ChatResponse{}, fmt.Errorf("build Anthropic request: %w", err)
	}
	applyAnthropicHeaders(httpRequest, request)

	httpClient := client.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	response, err := httpClient.Do(httpRequest)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("send Anthropic request: %w", err)
	}
	defer func() {
		_ = response.Body.Close()
	}()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("read Anthropic response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return ChatResponse{}, fmt.Errorf("anthropic response status %d: %s", response.StatusCode, strings.TrimSpace(string(data)))
	}
	return decodeAnthropicResponse(data)
}

type anthropicRequest struct {
	Model       string             `json:"model"`
	Messages    []anthropicMessage `json:"messages"`
	MaxTokens   int                `json:"max_tokens"`
	Temperature *float64           `json:"temperature,omitempty"`
}

type anthropicMessage struct {
	Role    string                  `json:"role"`
	Content []anthropicContentBlock `json:"content"`
}

type anthropicContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type anthropicResponse struct {
	Content    []anthropicContentBlock `json:"content"`
	StopReason string                  `json:"stop_reason"`
	Usage      anthropicUsage          `json:"usage"`
}

type anthropicUsage struct {
	InputTokens              int  `json:"input_tokens"`
	OutputTokens             int  `json:"output_tokens"`
	CacheCreationInputTokens *int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     *int `json:"cache_read_input_tokens"`
}

func decodeAnthropicResponse(data []byte) (ChatResponse, error) {
	var response anthropicResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return ChatResponse{}, fmt.Errorf("decode Anthropic response: %w", err)
	}
	var text strings.Builder
	for _, block := range response.Content {
		if block.Type != "text" {
			continue
		}
		text.WriteString(block.Text)
	}
	if text.Len() == 0 {
		return ChatResponse{}, fmt.Errorf("anthropic response did not include text content")
	}
	return ChatResponse{
		Text:         text.String(),
		FinishReason: mapAnthropicFinishReason(response.StopReason),
		Usage:        mapAnthropicUsage(response.Usage),
	}, nil
}

func mapAnthropicUsage(usage anthropicUsage) Usage {
	result := Usage{
		InputTokens:  usage.InputTokens,
		OutputTokens: usage.OutputTokens,
		TotalTokens:  usage.InputTokens + usage.OutputTokens,
	}
	if usage.CacheReadInputTokens != nil {
		result.CacheReadTokens = *usage.CacheReadInputTokens
		result.InputTokens += *usage.CacheReadInputTokens
	}
	if usage.CacheCreationInputTokens != nil {
		result.CacheWriteTokens = *usage.CacheCreationInputTokens
		result.InputTokens += *usage.CacheCreationInputTokens
	}
	result.TotalTokens = result.InputTokens + result.OutputTokens
	return result
}

func mapAnthropicFinishReason(reason string) string {
	switch reason {
	case "", "end_turn", "stop_sequence", "pause_turn":
		return "stop"
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool-calls"
	case "refusal":
		return "content-filter"
	default:
		return "unknown"
	}
}

func applyAnthropicHeaders(httpRequest *http.Request, request ChatRequest) {
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "application/json")
	httpRequest.Header.Set("anthropic-version", "2023-06-01")
	for key, value := range request.Headers {
		if key == "" || value == "" {
			continue
		}
		httpRequest.Header.Set(key, value)
	}
	if request.APIKey != "" {
		httpRequest.Header.Set(defaultString(request.AuthHeader, "x-api-key"), request.APIKey)
	}
}

func defaultInt(value *int, fallback int) int {
	if value == nil || *value <= 0 {
		return fallback
	}
	return *value
}
