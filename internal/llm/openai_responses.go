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
	body := map[string]any{}
	applyResponsesOptions(body, request.Options)
	body["model"] = defaultString(request.Model, "gpt-4o-mini")
	body["input"] = make([]responsesInputItem, 0, len(request.Messages))
	body["stream"] = false
	if len(request.Tools) > 0 {
		body["tools"] = responsesToolDefinitions(request.Tools)
	}
	if request.MaxTokens != nil {
		body["max_output_tokens"] = *request.MaxTokens
	}
	if request.Temperature != nil {
		body["temperature"] = *request.Temperature
	}
	if request.TopP != nil {
		body["top_p"] = *request.TopP
	}
	if request.TopK != nil {
		body["top_k"] = *request.TopK
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
		body["input"] = append(body["input"].([]responsesInputItem), item)
	}
	if len(body["input"].([]responsesInputItem)) == 0 {
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

func applyResponsesOptions(body map[string]any, options map[string]any) {
	for key, value := range options {
		if !responsesBodyOption(key, value) {
			continue
		}
		body[key] = value
	}
}

func responsesBodyOption(key string, value any) bool {
	if key == "" || value == nil {
		return false
	}
	switch key {
	case "apiKey", "baseURL", "headers", "fetch", "timeout", "chunkTimeout", "includeUsage", "setCacheKey":
		return false
	case "model", "input", "stream", "tools", "temperature", "top_p", "top_k", "max_output_tokens":
		return false
	default:
		return true
	}
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

type responsesToolDef struct {
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters"`
}

func responsesToolDefinitions(tools []ToolDefinition) []responsesToolDef {
	result := make([]responsesToolDef, 0, len(tools))
	for _, tool := range tools {
		if tool.Name == "" {
			continue
		}
		result = append(result, responsesToolDef{
			Type:        "function",
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  defaultToolParameters(tool.Parameters),
		})
	}
	return result
}

type responsesJSON struct {
	Output            []responsesOutputItem `json:"output"`
	OutputText        string                `json:"output_text"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Usage responsesUsage `json:"usage"`
}

type responsesStreamEvent struct {
	Type        string              `json:"type"`
	Delta       string              `json:"delta"`
	OutputIndex int                 `json:"output_index"`
	ItemID      string              `json:"item_id"`
	CallID      string              `json:"call_id"`
	Name        string              `json:"name"`
	Arguments   string              `json:"arguments"`
	Item        responsesOutputItem `json:"item"`
	Response    *struct {
		IncompleteDetails *struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
		Usage responsesUsage `json:"usage"`
	} `json:"response"`
}

type responsesOutputItem struct {
	Type      string             `json:"type"`
	ID        string             `json:"id"`
	CallID    string             `json:"call_id"`
	Name      string             `json:"name"`
	Arguments string             `json:"arguments"`
	Content   []responsesContent `json:"content"`
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
	toolCalls := []ToolCall{}
	for _, item := range response.Output {
		switch item.Type {
		case "message":
			for _, content := range item.Content {
				if content.Type != "output_text" {
					continue
				}
				text.WriteString(content.Text)
			}
		case "function_call":
			call, err := responsesToolCall(item.ID, item.CallID, item.Name, item.Arguments)
			if err != nil {
				return ChatResponse{}, err
			}
			toolCalls = append(toolCalls, call)
		}
	}
	if text.Len() == 0 && len(toolCalls) == 0 {
		return ChatResponse{}, fmt.Errorf("responses response did not include text content")
	}
	return ChatResponse{
		Text:         text.String(),
		FinishReason: mapResponsesFinishReason(reasonFromIncomplete(response.IncompleteDetails)),
		Usage:        mapResponsesUsage(response.Usage),
		ToolCalls:    toolCalls,
	}, nil
}

func decodeResponsesStream(reader io.Reader) (ChatResponse, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var text strings.Builder
	usage := Usage{}
	reason := ""
	toolCalls := map[int]responsesOutputItem{}
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
		switch event.Type {
		case "response.output_text.delta":
			text.WriteString(event.Delta)
		case "response.output_item.added":
			if event.Item.Type == "function_call" {
				toolCalls[event.OutputIndex] = event.Item
			}
		case "response.function_call_arguments.delta":
			item := toolCalls[event.OutputIndex]
			item.Arguments += event.Delta
			if event.ItemID != "" {
				item.ID = event.ItemID
			}
			toolCalls[event.OutputIndex] = item
		case "response.function_call_arguments.done":
			item := toolCalls[event.OutputIndex]
			item.Type = "function_call"
			item.ID = defaultString(defaultString(event.ItemID, item.ID), event.CallID)
			item.CallID = defaultString(event.CallID, item.CallID)
			item.Name = defaultString(event.Name, item.Name)
			item.Arguments = defaultString(event.Arguments, item.Arguments)
			toolCalls[event.OutputIndex] = item
		case "response.output_item.done":
			if event.Item.Type == "function_call" {
				toolCalls[event.OutputIndex] = event.Item
			}
		}
		if event.Response != nil {
			usage = mapResponsesUsage(event.Response.Usage)
			reason = reasonFromIncomplete(event.Response.IncompleteDetails)
		}
	}
	if err := scanner.Err(); err != nil {
		return ChatResponse{}, fmt.Errorf("read Responses stream: %w", err)
	}
	calls, err := responsesToolCalls(toolCalls)
	if err != nil {
		return ChatResponse{}, err
	}
	if text.Len() == 0 && len(calls) == 0 {
		return ChatResponse{}, fmt.Errorf("responses stream did not include text content")
	}
	return ChatResponse{
		Text:         text.String(),
		FinishReason: mapResponsesFinishReason(reason),
		Usage:        usage,
		ToolCalls:    calls,
	}, nil
}

func responsesToolCalls(items map[int]responsesOutputItem) ([]ToolCall, error) {
	if len(items) == 0 {
		return nil, nil
	}
	result := make([]ToolCall, 0, len(items))
	for index := 0; index < len(items); index++ {
		item, ok := items[index]
		if !ok {
			continue
		}
		call, err := responsesToolCall(item.ID, item.CallID, item.Name, item.Arguments)
		if err != nil {
			return nil, err
		}
		result = append(result, call)
	}
	return result, nil
}

func responsesToolCall(id string, callID string, name string, arguments string) (ToolCall, error) {
	if name == "" {
		return ToolCall{}, fmt.Errorf("responses function call did not include a name")
	}
	args := map[string]any{}
	if strings.TrimSpace(arguments) != "" {
		if err := json.Unmarshal([]byte(arguments), &args); err != nil {
			return ToolCall{}, fmt.Errorf("decode responses function call %s arguments: %w", name, err)
		}
	}
	return ToolCall{
		ID:        defaultString(callID, id),
		Name:      name,
		Arguments: args,
		Raw:       arguments,
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
