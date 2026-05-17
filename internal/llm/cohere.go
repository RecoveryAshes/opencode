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

const defaultCohereBaseURL = "https://api.cohere.com/v2"

// CohereClient calls Cohere's v2 Chat API.
type CohereClient struct {
	HTTPClient *http.Client
}

// NewCohereClient creates a Cohere client with production-safe defaults.
func NewCohereClient() *CohereClient {
	return &CohereClient{
		HTTPClient: &http.Client{Timeout: 2 * time.Minute},
	}
}

// Chat sends a text-only Cohere v2 chat request.
func (client *CohereClient) Chat(ctx context.Context, request ChatRequest) (ChatResponse, error) {
	if len(request.Messages) == 0 {
		return ChatResponse{}, fmt.Errorf("at least one message is required")
	}
	body := cohereRequest{
		Model:    defaultString(request.Model, "command-a-03-2025"),
		Messages: make([]cohereMessage, 0, len(request.Messages)),
	}
	if request.MaxTokens != nil {
		body.MaxTokens = request.MaxTokens
	}
	if request.Temperature != nil {
		body.Temperature = request.Temperature
	}
	for _, message := range request.Messages {
		if message.Role == "" || message.Content == "" {
			continue
		}
		body.Messages = append(body.Messages, cohereMessage{
			Role:    cohereRole(message.Role),
			Content: message.Content,
		})
	}
	if len(body.Messages) == 0 {
		return ChatResponse{}, fmt.Errorf("at least one non-empty message is required")
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("encode Cohere request: %w", err)
	}

	endpoint, err := url.JoinPath(strings.TrimRight(defaultString(request.BaseURL, defaultCohereBaseURL), "/"), "chat")
	if err != nil {
		return ChatResponse{}, fmt.Errorf("build Cohere endpoint: %w", err)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return ChatResponse{}, fmt.Errorf("build Cohere request: %w", err)
	}
	applyOpenAICompatibleHeaders(httpRequest, request, "application/json")

	httpClient := client.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	response, err := httpClient.Do(httpRequest)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("send Cohere request: %w", err)
	}
	defer func() {
		_ = response.Body.Close()
	}()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("read Cohere response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return ChatResponse{}, fmt.Errorf("cohere response status %d: %s", response.StatusCode, strings.TrimSpace(string(data)))
	}
	return decodeCohereResponse(data)
}

type cohereRequest struct {
	Model       string          `json:"model"`
	Messages    []cohereMessage `json:"messages"`
	MaxTokens   *int            `json:"max_tokens,omitempty"`
	Temperature *float64        `json:"temperature,omitempty"`
}

type cohereMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type cohereResponse struct {
	Message struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"message"`
	FinishReason string      `json:"finish_reason"`
	Usage        cohereUsage `json:"usage"`
}

type cohereUsage struct {
	Tokens struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"tokens"`
}

func decodeCohereResponse(data []byte) (ChatResponse, error) {
	var response cohereResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return ChatResponse{}, fmt.Errorf("decode Cohere response: %w", err)
	}
	var text strings.Builder
	for _, content := range response.Message.Content {
		if content.Type != "text" {
			continue
		}
		text.WriteString(content.Text)
	}
	if text.Len() == 0 {
		return ChatResponse{}, fmt.Errorf("cohere response did not include text content")
	}
	return ChatResponse{
		Text:         text.String(),
		FinishReason: mapCohereFinishReason(response.FinishReason),
		Usage:        mapCohereUsage(response.Usage),
	}, nil
}

func mapCohereUsage(usage cohereUsage) Usage {
	total := usage.Tokens.TotalTokens
	if total == 0 {
		total = usage.Tokens.InputTokens + usage.Tokens.OutputTokens
	}
	return Usage{
		InputTokens:  usage.Tokens.InputTokens,
		OutputTokens: usage.Tokens.OutputTokens,
		TotalTokens:  total,
	}
}

func mapCohereFinishReason(reason string) string {
	switch strings.ToUpper(reason) {
	case "", "COMPLETE", "STOP_SEQUENCE":
		return "stop"
	case "MAX_TOKENS":
		return "length"
	case "ERROR":
		return "error"
	default:
		return "unknown"
	}
}

func cohereRole(role string) string {
	if role == "assistant" {
		return "assistant"
	}
	if role == "system" {
		return "system"
	}
	return "user"
}
