package llm

import (
	"bufio"
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
		Stream:    true,
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
	if strings.Contains(response.Header.Get("Content-Type"), "text/event-stream") || bytes.Contains(data, []byte("data:")) {
		return decodeAnthropicStream(bytes.NewReader(data))
	}
	return decodeAnthropicResponse(data)
}

type anthropicRequest struct {
	Model       string             `json:"model"`
	Messages    []anthropicMessage `json:"messages"`
	Stream      bool               `json:"stream"`
	MaxTokens   int                `json:"max_tokens"`
	Temperature *float64           `json:"temperature,omitempty"`
}

type anthropicMessage struct {
	Role    string                  `json:"role"`
	Content []anthropicContentBlock `json:"content"`
}

type anthropicContentBlock struct {
	Type  string         `json:"type"`
	Text  string         `json:"text,omitempty"`
	ID    string         `json:"id,omitempty"`
	Name  string         `json:"name,omitempty"`
	Input map[string]any `json:"input,omitempty"`
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

type anthropicStreamEvent struct {
	Type    string `json:"type"`
	Message *struct {
		Usage *anthropicStreamUsage `json:"usage"`
	} `json:"message"`
	ContentBlock *struct {
		Type  string         `json:"type"`
		Text  string         `json:"text"`
		ID    string         `json:"id"`
		Name  string         `json:"name"`
		Input map[string]any `json:"input"`
	} `json:"content_block"`
	Delta *struct {
		Type       string  `json:"type"`
		Text       string  `json:"text"`
		StopReason *string `json:"stop_reason"`
	} `json:"delta"`
	Usage *anthropicStreamUsage `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

type anthropicStreamUsage struct {
	InputTokens              *int `json:"input_tokens"`
	OutputTokens             *int `json:"output_tokens"`
	CacheCreationInputTokens *int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     *int `json:"cache_read_input_tokens"`
}

func decodeAnthropicResponse(data []byte) (ChatResponse, error) {
	var response anthropicResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return ChatResponse{}, fmt.Errorf("decode Anthropic response: %w", err)
	}
	var text strings.Builder
	toolCalls := []ToolCall{}
	for _, block := range response.Content {
		switch block.Type {
		case "text":
			text.WriteString(block.Text)
		case "tool_use":
			toolCalls = append(toolCalls, ToolCall{
				ID:        block.ID,
				Name:      block.Name,
				Arguments: cloneAnyMap(block.Input),
				Raw:       rawToolArguments(block.Input),
			})
		default:
			continue
		}
	}
	if text.Len() == 0 && len(toolCalls) == 0 {
		return ChatResponse{}, fmt.Errorf("anthropic response did not include text content")
	}
	return ChatResponse{
		Text:         text.String(),
		FinishReason: mapAnthropicFinishReason(response.StopReason),
		Usage:        mapAnthropicUsage(response.Usage),
		ToolCalls:    toolCalls,
	}, nil
}

func decodeAnthropicStream(reader io.Reader) (ChatResponse, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var text strings.Builder
	var usage *anthropicStreamUsage
	reason := ""
	toolCalls := []ToolCall{}
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var event anthropicStreamEvent
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			return ChatResponse{}, fmt.Errorf("decode Anthropic stream event: %w", err)
		}
		switch event.Type {
		case "message_start":
			usage = mergeAnthropicStreamUsage(usage, event.MessageUsage())
		case "content_block_start":
			if event.ContentBlock == nil {
				continue
			}
			switch event.ContentBlock.Type {
			case "text":
				text.WriteString(event.ContentBlock.Text)
			case "tool_use":
				toolCalls = append(toolCalls, ToolCall{
					ID:        event.ContentBlock.ID,
					Name:      event.ContentBlock.Name,
					Arguments: cloneAnyMap(event.ContentBlock.Input),
					Raw:       rawToolArguments(event.ContentBlock.Input),
				})
			}
		case "content_block_delta":
			if event.Delta != nil && event.Delta.Type == "text_delta" {
				text.WriteString(event.Delta.Text)
			}
		case "message_delta":
			usage = mergeAnthropicStreamUsage(usage, event.Usage)
			if event.Delta != nil && event.Delta.StopReason != nil {
				reason = *event.Delta.StopReason
			}
		case "error":
			if event.Error != nil {
				return ChatResponse{}, fmt.Errorf("anthropic stream error %s: %s", event.Error.Type, event.Error.Message)
			}
			return ChatResponse{}, fmt.Errorf("anthropic stream error")
		}
	}
	if err := scanner.Err(); err != nil {
		return ChatResponse{}, fmt.Errorf("read Anthropic stream: %w", err)
	}
	if text.Len() == 0 && len(toolCalls) == 0 {
		return ChatResponse{}, fmt.Errorf("anthropic stream did not include text content")
	}
	return ChatResponse{
		Text:         text.String(),
		FinishReason: mapAnthropicFinishReason(reason),
		Usage:        mapAnthropicStreamUsage(usage),
		ToolCalls:    toolCalls,
	}, nil
}

func (event anthropicStreamEvent) MessageUsage() *anthropicStreamUsage {
	if event.Message == nil {
		return nil
	}
	return event.Message.Usage
}

func mergeAnthropicStreamUsage(left *anthropicStreamUsage, right *anthropicStreamUsage) *anthropicStreamUsage {
	if left == nil {
		return right
	}
	if right == nil {
		return left
	}
	output := *left
	if right.InputTokens != nil {
		output.InputTokens = right.InputTokens
	}
	if right.OutputTokens != nil {
		output.OutputTokens = right.OutputTokens
	}
	if right.CacheCreationInputTokens != nil {
		output.CacheCreationInputTokens = right.CacheCreationInputTokens
	}
	if right.CacheReadInputTokens != nil {
		output.CacheReadInputTokens = right.CacheReadInputTokens
	}
	return &output
}

func mapAnthropicStreamUsage(usage *anthropicStreamUsage) Usage {
	if usage == nil {
		return Usage{}
	}
	nonCached := optionalIntValue(usage.InputTokens)
	cacheRead := optionalIntValue(usage.CacheReadInputTokens)
	cacheWrite := optionalIntValue(usage.CacheCreationInputTokens)
	output := optionalIntValue(usage.OutputTokens)
	input := nonCached + cacheRead + cacheWrite
	return Usage{
		InputTokens:      input,
		OutputTokens:     output,
		CacheReadTokens:  cacheRead,
		CacheWriteTokens: cacheWrite,
		TotalTokens:      input + output,
	}
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
	httpRequest.Header.Set("Accept", "text/event-stream, application/json")
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

func optionalIntValue(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}
