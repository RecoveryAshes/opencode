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

// ResponsesClient calls OpenAI's Responses API.
type ResponsesClient struct {
	HTTPClient *http.Client
}

// NewResponsesClient creates an OpenAI Responses client with production-safe defaults.
func NewResponsesClient() *ResponsesClient {
	return &ResponsesClient{
		HTTPClient: &http.Client{Timeout: 2 * time.Minute},
	}
}

// Chat sends a text-only Responses request and accepts either JSON or SSE
// response bodies. Tool calls, hosted tools, and WebSocket transport are later
// protocol slices.
func (client *ResponsesClient) Chat(ctx context.Context, request ChatRequest) (ChatResponse, error) {
	if len(request.Messages) == 0 {
		return ChatResponse{}, fmt.Errorf("at least one message is required")
	}
	body := responsesRequest{
		Model:  defaultString(request.Model, "gpt-4o-mini"),
		Input:  make([]responsesInputItem, 0, len(request.Messages)),
		Stream: false,
	}
	if request.MaxTokens != nil {
		body.MaxOutputTokens = request.MaxTokens
	}
	if request.Temperature != nil {
		body.Temperature = request.Temperature
	}
	for _, message := range request.Messages {
		if message.Role == "" || message.Content == "" {
			continue
		}
		item := responsesInputItem{Role: message.Role}
		switch message.Role {
		case "assistant":
			item.Content = []responsesContent{{Type: "output_text", Text: message.Content}}
		case "system":
			item.ContentString = message.Content
		default:
			item.Role = "user"
			item.Content = []responsesContent{{Type: "input_text", Text: message.Content}}
		}
		body.Input = append(body.Input, item)
	}
	if len(body.Input) == 0 {
		return ChatResponse{}, fmt.Errorf("at least one non-empty message is required")
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("encode Responses request: %w", err)
	}

	endpoint, err := responsesEndpoint(strings.TrimRight(defaultString(request.BaseURL, defaultOpenAICompatibleBaseURL), "/"), request.QueryParams)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("build Responses endpoint: %w", err)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return ChatResponse{}, fmt.Errorf("build Responses request: %w", err)
	}
	applyOpenAICompatibleHeaders(httpRequest, request, "application/json")

	httpClient := client.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	response, err := httpClient.Do(httpRequest)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("send Responses request: %w", err)
	}
	defer func() {
		_ = response.Body.Close()
	}()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("read Responses response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return ChatResponse{}, fmt.Errorf("responses status %d: %s", response.StatusCode, strings.TrimSpace(string(data)))
	}
	if strings.Contains(response.Header.Get("Content-Type"), "text/event-stream") || bytes.Contains(data, []byte("data:")) {
		return decodeResponsesStream(bytes.NewReader(data))
	}
	return decodeResponsesJSON(data)
}

type responsesRequest struct {
	Model           string               `json:"model"`
	Input           []responsesInputItem `json:"input"`
	Stream          bool                 `json:"stream"`
	MaxOutputTokens *int                 `json:"max_output_tokens,omitempty"`
	Temperature     *float64             `json:"temperature,omitempty"`
}

type responsesInputItem struct {
	Role          string             `json:"role"`
	Content       []responsesContent `json:"content,omitempty"`
	ContentString string             `json:"-"`
}

func (item responsesInputItem) MarshalJSON() ([]byte, error) {
	type alias responsesInputItem
	if item.ContentString == "" {
		return json.Marshal(alias(item))
	}
	return json.Marshal(struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}{
		Role:    item.Role,
		Content: item.ContentString,
	})
}

type responsesContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type responsesJSON struct {
	Output []struct {
		Type    string             `json:"type"`
		Content []responsesContent `json:"content"`
	} `json:"output"`
	OutputText        string `json:"output_text"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Usage responsesUsage `json:"usage"`
}

type responsesStreamEvent struct {
	Type     string `json:"type"`
	Delta    string `json:"delta"`
	Response *struct {
		IncompleteDetails *struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
		Usage responsesUsage `json:"usage"`
	} `json:"response"`
}

type responsesUsage struct {
	InputTokens        int `json:"input_tokens"`
	OutputTokens       int `json:"output_tokens"`
	TotalTokens        int `json:"total_tokens"`
	InputTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	OutputTokensDetails *struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

func decodeResponsesJSON(data []byte) (ChatResponse, error) {
	var response responsesJSON
	if err := json.Unmarshal(data, &response); err != nil {
		return ChatResponse{}, fmt.Errorf("decode Responses response: %w", err)
	}
	var text strings.Builder
	text.WriteString(response.OutputText)
	for _, item := range response.Output {
		for _, content := range item.Content {
			if content.Type != "output_text" {
				continue
			}
			text.WriteString(content.Text)
		}
	}
	if text.Len() == 0 {
		return ChatResponse{}, fmt.Errorf("responses response did not include text content")
	}
	return ChatResponse{
		Text:         text.String(),
		FinishReason: mapResponsesFinishReason(reasonFromIncomplete(response.IncompleteDetails)),
		Usage:        mapResponsesUsage(response.Usage),
	}, nil
}

func decodeResponsesStream(reader io.Reader) (ChatResponse, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var text strings.Builder
	usage := Usage{}
	reason := ""
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var event responsesStreamEvent
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			return ChatResponse{}, fmt.Errorf("decode Responses stream event: %w", err)
		}
		if event.Type == "response.output_text.delta" {
			text.WriteString(event.Delta)
		}
		if event.Response != nil {
			usage = mapResponsesUsage(event.Response.Usage)
			reason = reasonFromIncomplete(event.Response.IncompleteDetails)
		}
	}
	if err := scanner.Err(); err != nil {
		return ChatResponse{}, fmt.Errorf("read Responses stream: %w", err)
	}
	if text.Len() == 0 {
		return ChatResponse{}, fmt.Errorf("responses stream did not include text content")
	}
	return ChatResponse{
		Text:         text.String(),
		FinishReason: mapResponsesFinishReason(reason),
		Usage:        usage,
	}, nil
}

func mapResponsesUsage(usage responsesUsage) Usage {
	result := Usage{
		InputTokens:  usage.InputTokens,
		OutputTokens: usage.OutputTokens,
		TotalTokens:  usage.TotalTokens,
	}
	if result.TotalTokens == 0 {
		result.TotalTokens = result.InputTokens + result.OutputTokens
	}
	if usage.InputTokensDetails != nil {
		result.CacheReadTokens = usage.InputTokensDetails.CachedTokens
	}
	if usage.OutputTokensDetails != nil {
		result.ReasoningTokens = usage.OutputTokensDetails.ReasoningTokens
	}
	return result
}

func mapResponsesFinishReason(reason string) string {
	switch reason {
	case "", "stop":
		return "stop"
	case "max_output_tokens":
		return "length"
	case "content_filter":
		return "content-filter"
	default:
		return "unknown"
	}
}

func responsesEndpoint(baseURL string, queryParams map[string]string) (string, error) {
	endpoint, err := url.JoinPath(baseURL, "responses")
	if err != nil {
		return "", err
	}
	if len(queryParams) == 0 {
		return endpoint, nil
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	query := parsed.Query()
	for key, value := range queryParams {
		if key == "" || value == "" {
			continue
		}
		query.Set(key, value)
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func reasonFromIncomplete(input *struct {
	Reason string `json:"reason"`
}) string {
	if input == nil {
		return ""
	}
	return input.Reason
}
