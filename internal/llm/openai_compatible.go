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
	"os"
	"strings"
	"time"
)

const defaultOpenAICompatibleBaseURL = "https://api.openai.com/v1"

// Message is the provider-neutral text-only chat message used by the first Go
// prompt runtime slice.
type Message struct {
	Role    string
	Content string
}

// ChatRequest describes one text-only OpenAI-compatible chat completion.
type ChatRequest struct {
	BaseURL     string
	APIKey      string
	Model       string
	Messages    []Message
	Temperature *float64
	MaxTokens   *int
}

// Usage is token accounting returned by OpenAI-compatible providers.
type Usage struct {
	InputTokens     int
	OutputTokens    int
	ReasoningTokens int
	CacheReadTokens int
	TotalTokens     int
}

// ChatResponse is the assistant text and finish metadata returned by a provider.
type ChatResponse struct {
	Text         string
	FinishReason string
	Usage        Usage
}

// OpenAICompatibleClient calls a provider exposing /chat/completions.
type OpenAICompatibleClient struct {
	HTTPClient *http.Client
}

// NewOpenAICompatibleClient creates a chat client with production-safe defaults.
func NewOpenAICompatibleClient() *OpenAICompatibleClient {
	return &OpenAICompatibleClient{
		HTTPClient: &http.Client{Timeout: 2 * time.Minute},
	}
}

// Chat sends a non-streaming chat completion request. The legacy TS route uses
// streaming, but this preserves the wire protocol and keeps the first Go
// runtime slice deterministic; SSE parsing can build on this client.
func (client *OpenAICompatibleClient) Chat(ctx context.Context, request ChatRequest) (ChatResponse, error) {
	if len(request.Messages) == 0 {
		return ChatResponse{}, fmt.Errorf("at least one message is required")
	}
	baseURL := strings.TrimRight(defaultString(request.BaseURL, defaultOpenAICompatibleBaseURL), "/")
	model := defaultString(request.Model, "gpt-4o-mini")
	body := openAIChatRequest{
		Model:       model,
		Messages:    make([]openAIChatMessage, 0, len(request.Messages)),
		Stream:      false,
		Temperature: request.Temperature,
		MaxTokens:   request.MaxTokens,
	}
	for _, message := range request.Messages {
		if message.Role == "" || message.Content == "" {
			continue
		}
		body.Messages = append(body.Messages, openAIChatMessage(message))
	}
	if len(body.Messages) == 0 {
		return ChatResponse{}, fmt.Errorf("at least one non-empty message is required")
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("encode chat request: %w", err)
	}

	endpoint, err := url.JoinPath(baseURL, "chat", "completions")
	if err != nil {
		return ChatResponse{}, fmt.Errorf("build chat endpoint: %w", err)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return ChatResponse{}, fmt.Errorf("build chat request: %w", err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "application/json")
	if request.APIKey != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+request.APIKey)
	}

	httpClient := client.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	response, err := httpClient.Do(httpRequest)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("send chat request: %w", err)
	}
	defer func() {
		_ = response.Body.Close()
	}()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("read chat response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return ChatResponse{}, fmt.Errorf("chat response status %d: %s", response.StatusCode, strings.TrimSpace(string(data)))
	}
	return decodeOpenAIChatResponse(data)
}

// ChatStream consumes OpenAI-compatible SSE responses and concatenates text
// deltas. It is used by contract tests and is ready for later event-bus wiring.
func (client *OpenAICompatibleClient) ChatStream(ctx context.Context, request ChatRequest) (ChatResponse, error) {
	streamRequest := request
	if len(streamRequest.Messages) == 0 {
		return ChatResponse{}, fmt.Errorf("at least one message is required")
	}
	baseURL := strings.TrimRight(defaultString(streamRequest.BaseURL, defaultOpenAICompatibleBaseURL), "/")
	body := openAIChatRequest{
		Model:    defaultString(streamRequest.Model, "gpt-4o-mini"),
		Messages: make([]openAIChatMessage, 0, len(streamRequest.Messages)),
		Stream:   true,
		StreamOptions: map[string]bool{
			"include_usage": true,
		},
		Temperature: streamRequest.Temperature,
		MaxTokens:   streamRequest.MaxTokens,
	}
	for _, message := range streamRequest.Messages {
		if message.Role == "" || message.Content == "" {
			continue
		}
		body.Messages = append(body.Messages, openAIChatMessage(message))
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("encode chat stream request: %w", err)
	}
	endpoint, err := url.JoinPath(baseURL, "chat", "completions")
	if err != nil {
		return ChatResponse{}, fmt.Errorf("build chat stream endpoint: %w", err)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return ChatResponse{}, fmt.Errorf("build chat stream request: %w", err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "text/event-stream")
	if streamRequest.APIKey != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+streamRequest.APIKey)
	}
	httpClient := client.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	response, err := httpClient.Do(httpRequest)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("send chat stream request: %w", err)
	}
	defer func() {
		_ = response.Body.Close()
	}()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		data, readErr := io.ReadAll(response.Body)
		if readErr != nil {
			return ChatResponse{}, fmt.Errorf("read chat stream error response: %w", readErr)
		}
		return ChatResponse{}, fmt.Errorf("chat stream response status %d: %s", response.StatusCode, strings.TrimSpace(string(data)))
	}
	return decodeOpenAIChatStream(response.Body)
}

// ChatRequestFromEnv builds runtime provider config from environment variables.
func ChatRequestFromEnv(messages []Message, model string) ChatRequest {
	return ChatRequest{
		BaseURL:  firstEnv("OPENCODE_OPENAI_COMPATIBLE_BASE_URL", "OPENAI_BASE_URL"),
		APIKey:   firstEnv("OPENCODE_OPENAI_COMPATIBLE_API_KEY", "OPENAI_API_KEY"),
		Model:    defaultString(model, firstEnv("OPENCODE_OPENAI_COMPATIBLE_MODEL", "OPENAI_MODEL")),
		Messages: messages,
	}
}

type openAIChatRequest struct {
	Model         string              `json:"model"`
	Messages      []openAIChatMessage `json:"messages"`
	Stream        bool                `json:"stream"`
	StreamOptions map[string]bool     `json:"stream_options,omitempty"`
	Temperature   *float64            `json:"temperature,omitempty"`
	MaxTokens     *int                `json:"max_tokens,omitempty"`
}

type openAIChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIChatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage openAIUsage `json:"usage"`
}

type openAIStreamEvent struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *openAIUsage `json:"usage"`
}

type openAIUsage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	TotalTokens         int `json:"total_tokens"`
	PromptTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails *struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

func decodeOpenAIChatResponse(data []byte) (ChatResponse, error) {
	var response openAIChatResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return ChatResponse{}, fmt.Errorf("decode chat response: %w", err)
	}
	if len(response.Choices) == 0 {
		return ChatResponse{}, fmt.Errorf("chat response did not include choices")
	}
	return ChatResponse{
		Text:         response.Choices[0].Message.Content,
		FinishReason: mapFinishReason(response.Choices[0].FinishReason),
		Usage:        mapOpenAIUsage(response.Usage),
	}, nil
}

func decodeOpenAIChatStream(reader io.Reader) (ChatResponse, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var text strings.Builder
	finish := ""
	usage := Usage{}
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var event openAIStreamEvent
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			return ChatResponse{}, fmt.Errorf("decode chat stream event: %w", err)
		}
		if event.Usage != nil {
			usage = mapOpenAIUsage(*event.Usage)
		}
		if len(event.Choices) == 0 {
			continue
		}
		text.WriteString(event.Choices[0].Delta.Content)
		if event.Choices[0].FinishReason != "" {
			finish = event.Choices[0].FinishReason
		}
	}
	if err := scanner.Err(); err != nil {
		return ChatResponse{}, fmt.Errorf("read chat stream: %w", err)
	}
	return ChatResponse{
		Text:         text.String(),
		FinishReason: mapFinishReason(finish),
		Usage:        usage,
	}, nil
}

func mapOpenAIUsage(usage openAIUsage) Usage {
	result := Usage{
		InputTokens:  usage.PromptTokens,
		OutputTokens: usage.CompletionTokens,
		TotalTokens:  usage.TotalTokens,
	}
	if result.TotalTokens == 0 {
		result.TotalTokens = result.InputTokens + result.OutputTokens
	}
	if usage.PromptTokensDetails != nil {
		result.CacheReadTokens = usage.PromptTokensDetails.CachedTokens
	}
	if usage.CompletionTokensDetails != nil {
		result.ReasoningTokens = usage.CompletionTokensDetails.ReasoningTokens
	}
	return result
}

func mapFinishReason(reason string) string {
	switch reason {
	case "", "stop":
		return "stop"
	case "length":
		return "length"
	case "content_filter":
		return "content-filter"
	case "function_call", "tool_calls":
		return "tool-calls"
	default:
		return "unknown"
	}
}

func firstEnv(names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}

func defaultString(value string, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
