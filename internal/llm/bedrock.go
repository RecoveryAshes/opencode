package llm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/config"
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

// Chat sends a text-only Bedrock Converse request. Bearer token auth takes
// precedence over SigV4, matching the legacy TypeScript route.
func (client *BedrockClient) Chat(ctx context.Context, request ChatRequest) (ChatResponse, error) {
	if len(request.Messages) == 0 {
		return ChatResponse{}, fmt.Errorf("at least one message is required")
	}
	body := bedrockRequest{
		ModelID:  defaultString(request.Model, "us.amazon.nova-micro-v1:0"),
		Messages: make([]bedrockMessage, 0, len(request.Messages)),
	}
	if len(request.Tools) > 0 {
		body.ToolConfig = bedrockToolConfigFor(request.Tools)
	}
	if request.MaxTokens != nil || request.Temperature != nil || request.TopP != nil {
		body.InferenceConfig = &bedrockInferenceConfig{
			MaxTokens:   request.MaxTokens,
			Temperature: request.Temperature,
			TopP:        request.TopP,
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

	endpoint, err := url.JoinPath(strings.TrimRight(request.BaseURL, "/"), "model", body.ModelID, "converse-stream")
	if err != nil {
		return ChatResponse{}, fmt.Errorf("build Bedrock endpoint: %w", err)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return ChatResponse{}, fmt.Errorf("build Bedrock request: %w", err)
	}
	httpRequest.URL.Opaque = "//" + httpRequest.URL.Host + httpRequest.URL.EscapedPath()
	if err := applyBedrockHeaders(ctx, httpRequest, request, payload); err != nil {
		return ChatResponse{}, err
	}

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
	if isBedrockEventStream(response.Header.Get("Content-Type")) {
		return decodeBedrockEventStream(bytes.NewReader(data))
	}
	return decodeBedrockResponse(data)
}

type bedrockRequest struct {
	ModelID         string                  `json:"modelId"`
	Messages        []bedrockMessage        `json:"messages"`
	InferenceConfig *bedrockInferenceConfig `json:"inferenceConfig,omitempty"`
	ToolConfig      *bedrockToolConfig      `json:"toolConfig,omitempty"`
}

type bedrockMessage struct {
	Role    string                `json:"role"`
	Content []bedrockContentBlock `json:"content"`
}

type bedrockContentBlock struct {
	Text    string          `json:"text,omitempty"`
	ToolUse *bedrockToolUse `json:"toolUse,omitempty"`
}

type bedrockToolUse struct {
	ToolUseID string         `json:"toolUseId"`
	Name      string         `json:"name"`
	Input     map[string]any `json:"input,omitempty"`
}

type bedrockInferenceConfig struct {
	MaxTokens   *int     `json:"maxTokens,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
	TopP        *float64 `json:"topP,omitempty"`
}

type bedrockToolConfig struct {
	Tools      []bedrockToolDef       `json:"tools"`
	ToolChoice map[string]interface{} `json:"toolChoice,omitempty"`
}

type bedrockToolDef struct {
	ToolSpec bedrockToolSpec `json:"toolSpec"`
}

type bedrockToolSpec struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	InputSchema map[string]interface{} `json:"inputSchema"`
}

func bedrockToolConfigFor(tools []ToolDefinition) *bedrockToolConfig {
	result := []bedrockToolDef{}
	for _, tool := range tools {
		if tool.Name == "" {
			continue
		}
		result = append(result, bedrockToolDef{
			ToolSpec: bedrockToolSpec{
				Name:        tool.Name,
				Description: tool.Description,
				InputSchema: map[string]interface{}{
					"json": defaultToolParameters(tool.Parameters),
				},
			},
		})
	}
	if len(result) == 0 {
		return nil
	}
	return &bedrockToolConfig{
		Tools:      result,
		ToolChoice: map[string]interface{}{"auto": map[string]interface{}{}},
	}
}

type bedrockResponse struct {
	Output struct {
		Message bedrockMessage `json:"message"`
	} `json:"output"`
	StopReason string       `json:"stopReason"`
	Usage      bedrockUsage `json:"usage"`
}

type bedrockStreamEvent struct {
	MessageStart *struct {
		Role string `json:"role"`
	} `json:"messageStart"`
	ContentBlockStart *struct {
		ContentBlockIndex int `json:"contentBlockIndex"`
		Start             *struct {
			ToolUse *struct {
				ToolUseID string `json:"toolUseId"`
				Name      string `json:"name"`
			} `json:"toolUse"`
		} `json:"start"`
	} `json:"contentBlockStart"`
	ContentBlockDelta *struct {
		ContentBlockIndex int `json:"contentBlockIndex"`
		Delta             *struct {
			Text    string `json:"text"`
			ToolUse *struct {
				Input string `json:"input"`
			} `json:"toolUse"`
			ReasoningContent *struct {
				Text      string `json:"text"`
				Signature string `json:"signature"`
			} `json:"reasoningContent"`
		} `json:"delta"`
	} `json:"contentBlockDelta"`
	ContentBlockStop *struct {
		ContentBlockIndex int `json:"contentBlockIndex"`
	} `json:"contentBlockStop"`
	MessageStop *struct {
		StopReason string `json:"stopReason"`
	} `json:"messageStop"`
	Metadata *struct {
		Usage bedrockUsage `json:"usage"`
	} `json:"metadata"`
	InternalServerException *bedrockStreamException `json:"internalServerException"`
	ModelStreamError        *bedrockStreamException `json:"modelStreamErrorException"`
	ValidationException     *bedrockStreamException `json:"validationException"`
	ThrottlingException     *bedrockStreamException `json:"throttlingException"`
	ServiceUnavailable      *bedrockStreamException `json:"serviceUnavailableException"`
}

type bedrockStreamException struct {
	Message string `json:"message"`
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
	toolCalls := []ToolCall{}
	for _, block := range response.Output.Message.Content {
		text.WriteString(block.Text)
		if block.ToolUse != nil && block.ToolUse.Name != "" {
			toolCalls = append(toolCalls, ToolCall{
				ID:        block.ToolUse.ToolUseID,
				Name:      block.ToolUse.Name,
				Arguments: cloneAnyMap(block.ToolUse.Input),
				Raw:       rawToolArguments(block.ToolUse.Input),
			})
		}
	}
	if text.Len() == 0 && len(toolCalls) == 0 {
		return ChatResponse{}, fmt.Errorf("bedrock response did not include text content")
	}
	return ChatResponse{
		Text:         text.String(),
		FinishReason: mapBedrockFinishReason(response.StopReason),
		Usage:        mapBedrockUsage(response.Usage),
		ToolCalls:    toolCalls,
	}, nil
}

func decodeBedrockEventStream(reader io.Reader) (ChatResponse, error) {
	decoder := eventstream.NewDecoder()
	var payload []byte
	var text strings.Builder
	usage := Usage{}
	reason := ""
	toolBuilders := map[int]*bedrockToolBuilder{}
	for {
		message, err := decoder.Decode(reader, payload)
		if err != nil {
			if err == io.EOF {
				break
			}
			return ChatResponse{}, fmt.Errorf("decode Bedrock event-stream frame: %w", err)
		}
		payload = message.Payload
		messageType, _ := message.Headers.Get(":message-type").Get().(string)
		if messageType != "event" {
			continue
		}
		eventType, _ := message.Headers.Get(":event-type").Get().(string)
		if eventType == "" || len(message.Payload) == 0 {
			continue
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(message.Payload, &raw); err != nil {
			return ChatResponse{}, fmt.Errorf("decode Bedrock event-stream payload: %w", err)
		}
		delete(raw, "p")
		wrappedPayload, err := json.Marshal(map[string]map[string]json.RawMessage{eventType: raw})
		if err != nil {
			return ChatResponse{}, fmt.Errorf("wrap Bedrock event-stream payload: %w", err)
		}
		var event bedrockStreamEvent
		if err := json.Unmarshal(wrappedPayload, &event); err != nil {
			return ChatResponse{}, fmt.Errorf("decode Bedrock event-stream event: %w", err)
		}
		if err := applyBedrockStreamEvent(&text, &usage, &reason, toolBuilders, event); err != nil {
			return ChatResponse{}, err
		}
	}
	toolCalls, err := bedrockStreamToolCalls(toolBuilders)
	if err != nil {
		return ChatResponse{}, err
	}
	if text.Len() == 0 && len(toolCalls) == 0 {
		return ChatResponse{}, fmt.Errorf("bedrock event-stream did not include text content")
	}
	return ChatResponse{
		Text:         text.String(),
		FinishReason: mapBedrockFinishReason(reason),
		Usage:        usage,
		ToolCalls:    toolCalls,
	}, nil
}

type bedrockToolBuilder struct {
	id    string
	name  string
	input strings.Builder
}

func applyBedrockStreamEvent(text *strings.Builder, usage *Usage, reason *string, toolBuilders map[int]*bedrockToolBuilder, event bedrockStreamEvent) error {
	if event.ContentBlockStart != nil &&
		event.ContentBlockStart.Start != nil &&
		event.ContentBlockStart.Start.ToolUse != nil {
		toolBuilders[event.ContentBlockStart.ContentBlockIndex] = &bedrockToolBuilder{
			id:   event.ContentBlockStart.Start.ToolUse.ToolUseID,
			name: event.ContentBlockStart.Start.ToolUse.Name,
		}
	}
	if event.ContentBlockDelta != nil && event.ContentBlockDelta.Delta != nil {
		text.WriteString(event.ContentBlockDelta.Delta.Text)
		if event.ContentBlockDelta.Delta.ToolUse != nil {
			builder := toolBuilders[event.ContentBlockDelta.ContentBlockIndex]
			if builder == nil {
				builder = &bedrockToolBuilder{}
				toolBuilders[event.ContentBlockDelta.ContentBlockIndex] = builder
			}
			builder.input.WriteString(event.ContentBlockDelta.Delta.ToolUse.Input)
		}
	}
	if event.MessageStop != nil {
		*reason = event.MessageStop.StopReason
	}
	if event.Metadata != nil {
		*usage = mapBedrockUsage(event.Metadata.Usage)
	}
	if event.InternalServerException != nil {
		return fmt.Errorf("bedrock event-stream internal server exception: %s", event.InternalServerException.Message)
	}
	if event.ModelStreamError != nil {
		return fmt.Errorf("bedrock event-stream model stream error: %s", event.ModelStreamError.Message)
	}
	if event.ServiceUnavailable != nil {
		return fmt.Errorf("bedrock event-stream service unavailable: %s", event.ServiceUnavailable.Message)
	}
	if event.ValidationException != nil {
		return fmt.Errorf("bedrock event-stream validation exception: %s", event.ValidationException.Message)
	}
	if event.ThrottlingException != nil {
		return fmt.Errorf("bedrock event-stream throttling exception: %s", event.ThrottlingException.Message)
	}
	return nil
}

func bedrockStreamToolCalls(builders map[int]*bedrockToolBuilder) ([]ToolCall, error) {
	if len(builders) == 0 {
		return nil, nil
	}
	result := make([]ToolCall, 0, len(builders))
	for index := 0; index < len(builders); index++ {
		builder, ok := builders[index]
		if !ok || builder == nil {
			continue
		}
		if builder.name == "" {
			return nil, fmt.Errorf("bedrock tool use did not include a name")
		}
		raw := builder.input.String()
		args := map[string]any{}
		if strings.TrimSpace(raw) != "" {
			if err := json.Unmarshal([]byte(raw), &args); err != nil {
				return nil, fmt.Errorf("decode bedrock tool use %s input: %w", builder.name, err)
			}
		}
		result = append(result, ToolCall{
			ID:        builder.id,
			Name:      builder.name,
			Arguments: args,
			Raw:       raw,
		})
	}
	return result, nil
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

func applyBedrockHeaders(ctx context.Context, httpRequest *http.Request, request ChatRequest, payload []byte) error {
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "application/vnd.amazon.eventstream, application/json")
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
	if request.APIKey != "" {
		return nil
	}
	credentials, region, err := bedrockSigningCredentials(ctx, request)
	if err != nil {
		return err
	}
	hash := sha256.Sum256(payload)
	payloadHash := hex.EncodeToString(hash[:])
	httpRequest.Header.Set("X-Amz-Content-Sha256", payloadHash)
	if err := v4.NewSigner().SignHTTP(ctx, credentials, httpRequest, payloadHash, "bedrock", region, time.Now().UTC()); err != nil {
		return fmt.Errorf("sign Bedrock request: %w", err)
	}
	return nil
}

func isBedrockEventStream(contentType string) bool {
	contentType = strings.ToLower(contentType)
	return strings.Contains(contentType, "application/vnd.amazon.eventstream") || strings.Contains(contentType, "application/octet-stream")
}

func bedrockRole(role string) string {
	if role == "assistant" {
		return "assistant"
	}
	return "user"
}

func bedrockSigningCredentials(ctx context.Context, request ChatRequest) (aws.Credentials, string, error) {
	if request.AWSCredentials != nil {
		region := defaultString(request.AWSCredentials.Region, defaultString(request.AWSRegion, "us-east-1"))
		if request.AWSCredentials.AccessKeyID == "" || request.AWSCredentials.SecretAccessKey == "" {
			return aws.Credentials{}, "", fmt.Errorf("bedrock converse requires both AWS access key id and secret access key")
		}
		return aws.Credentials{
			AccessKeyID:     request.AWSCredentials.AccessKeyID,
			SecretAccessKey: request.AWSCredentials.SecretAccessKey,
			SessionToken:    request.AWSCredentials.SessionToken,
			Source:          "opencode-bedrock",
		}, region, nil
	}
	region := defaultString(request.AWSRegion, "us-east-1")
	loadOptions := []func(*config.LoadOptions) error{config.WithRegion(region)}
	if request.AWSProfile != "" {
		loadOptions = append(loadOptions, config.WithSharedConfigProfile(request.AWSProfile))
	}
	cfg, err := config.LoadDefaultConfig(ctx, loadOptions...)
	if err != nil {
		return aws.Credentials{}, "", fmt.Errorf("load AWS config for Bedrock SigV4: %w", err)
	}
	resolvedRegion := defaultString(cfg.Region, region)
	credentials, err := cfg.Credentials.Retrieve(ctx)
	if err != nil {
		return aws.Credentials{}, "", fmt.Errorf("retrieve AWS credentials for Bedrock SigV4: %w", err)
	}
	if !credentials.HasKeys() {
		return aws.Credentials{}, "", fmt.Errorf("bedrock converse requires either bearer token auth or AWS credentials")
	}
	return credentials, resolvedRegion, nil
}
