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

const defaultGeminiBaseURL = "https://generativelanguage.googleapis.com/v1beta"

// GeminiClient calls Google's Gemini generateContent endpoint.
type GeminiClient struct {
	HTTPClient *http.Client
}

// NewGeminiClient creates a Gemini client with production-safe defaults.
func NewGeminiClient() *GeminiClient {
	return &GeminiClient{
		HTTPClient: &http.Client{Timeout: 2 * time.Minute},
	}
}

// Chat sends a text-only Gemini generateContent request.
func (client *GeminiClient) Chat(ctx context.Context, request ChatRequest) (ChatResponse, error) {
	if len(request.Messages) == 0 {
		return ChatResponse{}, fmt.Errorf("at least one message is required")
	}
	body := geminiRequest{
		Contents: make([]geminiContent, 0, len(request.Messages)),
	}
	if request.MaxTokens != nil || request.Temperature != nil {
		body.GenerationConfig = &geminiGenerationConfig{
			MaxOutputTokens: request.MaxTokens,
			Temperature:     request.Temperature,
		}
	}
	for _, message := range request.Messages {
		if message.Role == "" || message.Content == "" {
			continue
		}
		body.Contents = append(body.Contents, geminiContent{
			Role: geminiRole(message.Role),
			Parts: []geminiPart{{
				Text: message.Content,
			}},
		})
	}
	if len(body.Contents) == 0 {
		return ChatResponse{}, fmt.Errorf("at least one non-empty message is required")
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("encode Gemini request: %w", err)
	}

	endpoint, err := geminiEndpoint(strings.TrimRight(defaultString(request.BaseURL, defaultGeminiBaseURL), "/"), defaultString(request.Model, "gemini-2.5-flash"))
	if err != nil {
		return ChatResponse{}, fmt.Errorf("build Gemini endpoint: %w", err)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return ChatResponse{}, fmt.Errorf("build Gemini request: %w", err)
	}
	applyGeminiHeaders(httpRequest, request)

	httpClient := client.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	response, err := httpClient.Do(httpRequest)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("send Gemini request: %w", err)
	}
	defer func() {
		_ = response.Body.Close()
	}()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("read Gemini response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return ChatResponse{}, fmt.Errorf("gemini response status %d: %s", response.StatusCode, strings.TrimSpace(string(data)))
	}
	if strings.Contains(response.Header.Get("Content-Type"), "text/event-stream") || bytes.Contains(data, []byte("data:")) {
		return decodeGeminiStream(bytes.NewReader(data))
	}
	return decodeGeminiResponse(data)
}

type geminiRequest struct {
	Contents         []geminiContent         `json:"contents"`
	GenerationConfig *geminiGenerationConfig `json:"generationConfig,omitempty"`
}

type geminiContent struct {
	Role  string       `json:"role"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text         string              `json:"text,omitempty"`
	Thought      bool                `json:"thought,omitempty"`
	FunctionCall *geminiFunctionCall `json:"functionCall,omitempty"`
}

type geminiFunctionCall struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
}

type geminiGenerationConfig struct {
	MaxOutputTokens *int     `json:"maxOutputTokens,omitempty"`
	Temperature     *float64 `json:"temperature,omitempty"`
}

type geminiResponse struct {
	Candidates    []geminiCandidate `json:"candidates"`
	UsageMetadata geminiUsage       `json:"usageMetadata"`
}

type geminiCandidate struct {
	Content      geminiContent `json:"content"`
	FinishReason string        `json:"finishReason"`
}

type geminiUsage struct {
	CachedContentTokenCount int `json:"cachedContentTokenCount"`
	ThoughtsTokenCount      int `json:"thoughtsTokenCount"`
	PromptTokenCount        int `json:"promptTokenCount"`
	CandidatesTokenCount    int `json:"candidatesTokenCount"`
	TotalTokenCount         int `json:"totalTokenCount"`
}

func decodeGeminiResponse(data []byte) (ChatResponse, error) {
	var response geminiResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return ChatResponse{}, fmt.Errorf("decode Gemini response: %w", err)
	}
	if len(response.Candidates) == 0 {
		return ChatResponse{}, fmt.Errorf("gemini response did not include candidates")
	}
	candidate := response.Candidates[0]
	var text strings.Builder
	toolCalls := []ToolCall{}
	for _, part := range candidate.Content.Parts {
		if part.Thought {
			continue
		}
		text.WriteString(part.Text)
		if part.FunctionCall != nil && part.FunctionCall.Name != "" {
			toolCalls = append(toolCalls, ToolCall{
				ID:        part.FunctionCall.Name,
				Name:      part.FunctionCall.Name,
				Arguments: cloneAnyMap(part.FunctionCall.Args),
				Raw:       rawToolArguments(part.FunctionCall.Args),
			})
		}
	}
	if text.Len() == 0 && len(toolCalls) == 0 {
		return ChatResponse{}, fmt.Errorf("gemini response did not include text content")
	}
	return ChatResponse{
		Text:         text.String(),
		FinishReason: mapGeminiFinishReason(candidate.FinishReason),
		Usage:        mapGeminiUsage(response.UsageMetadata),
		ToolCalls:    toolCalls,
	}, nil
}

func decodeGeminiStream(reader io.Reader) (ChatResponse, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var text strings.Builder
	usage := Usage{}
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
		var event geminiResponse
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			return ChatResponse{}, fmt.Errorf("decode Gemini stream event: %w", err)
		}
		if mapped := mapGeminiUsage(event.UsageMetadata); mapped.TotalTokens != 0 || mapped.InputTokens != 0 || mapped.OutputTokens != 0 {
			usage = mapped
		}
		if len(event.Candidates) == 0 {
			continue
		}
		candidate := event.Candidates[0]
		if candidate.FinishReason != "" {
			reason = candidate.FinishReason
		}
		for _, part := range candidate.Content.Parts {
			if part.Thought {
				continue
			}
			text.WriteString(part.Text)
			if part.FunctionCall != nil && part.FunctionCall.Name != "" {
				toolCalls = append(toolCalls, ToolCall{
					ID:        part.FunctionCall.Name,
					Name:      part.FunctionCall.Name,
					Arguments: cloneAnyMap(part.FunctionCall.Args),
					Raw:       rawToolArguments(part.FunctionCall.Args),
				})
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return ChatResponse{}, fmt.Errorf("read Gemini stream: %w", err)
	}
	if text.Len() == 0 && len(toolCalls) == 0 {
		return ChatResponse{}, fmt.Errorf("gemini stream did not include text content")
	}
	return ChatResponse{
		Text:         text.String(),
		FinishReason: mapGeminiFinishReason(reason),
		Usage:        usage,
		ToolCalls:    toolCalls,
	}, nil
}

func mapGeminiUsage(usage geminiUsage) Usage {
	output := usage.CandidatesTokenCount + usage.ThoughtsTokenCount
	total := usage.TotalTokenCount
	if total == 0 {
		total = usage.PromptTokenCount + output
	}
	return Usage{
		InputTokens:     usage.PromptTokenCount,
		OutputTokens:    output,
		ReasoningTokens: usage.ThoughtsTokenCount,
		CacheReadTokens: usage.CachedContentTokenCount,
		TotalTokens:     total,
	}
}

func mapGeminiFinishReason(reason string) string {
	switch reason {
	case "STOP":
		return "stop"
	case "FUNCTION_CALL":
		return "tool-calls"
	case "MAX_TOKENS":
		return "length"
	case "IMAGE_SAFETY", "RECITATION", "SAFETY", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII":
		return "content-filter"
	case "MALFORMED_FUNCTION_CALL":
		return "error"
	default:
		return "unknown"
	}
}

func applyGeminiHeaders(httpRequest *http.Request, request ChatRequest) {
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "text/event-stream, application/json")
	for key, value := range request.Headers {
		if key == "" || value == "" {
			continue
		}
		httpRequest.Header.Set(key, value)
	}
	if request.APIKey != "" {
		httpRequest.Header.Set(defaultString(request.AuthHeader, "x-goog-api-key"), request.APIKey)
	}
}

func geminiEndpoint(baseURL string, model string) (string, error) {
	endpoint, err := url.JoinPath(baseURL, "models", model+":streamGenerateContent")
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	query := parsed.Query()
	query.Set("alt", "sse")
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func geminiRole(role string) string {
	if role == "assistant" {
		return "model"
	}
	return "user"
}
