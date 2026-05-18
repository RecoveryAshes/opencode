package pluginruntime

import (
	"context"

	"github.com/RecoveryAshes/opencode/internal/llm"
)

// ChatContext carries provider request metadata exposed to chat hooks.
type ChatContext struct {
	SessionID string
	Agent     string
	Model     map[string]any
	Provider  map[string]any
	Message   map[string]any
}

// ApplyChatParams runs chat.params and mutates the provider request fields that
// match the TypeScript plugin hook output contract.
func (runtime *Runtime) ApplyChatParams(ctx context.Context, request *llm.ChatRequest, chat ChatContext) error {
	if !runtime.Enabled() || request == nil {
		return nil
	}
	output, err := runtime.Trigger(ctx, "chat.params", chat.input(), map[string]any{
		"temperature":     floatPointerValue(request.Temperature),
		"topP":            floatPointerValue(request.TopP),
		"topK":            intPointerValue(request.TopK),
		"maxOutputTokens": intPointerValue(request.MaxTokens),
		"options":         cloneAnyMap(request.Options),
	})
	if err != nil {
		return err
	}
	if value, ok := optionalFloat(output["temperature"]); ok {
		request.Temperature = value
	}
	if value, ok := optionalFloat(output["topP"]); ok {
		request.TopP = value
	}
	if value, ok := optionalInt(output["topK"]); ok {
		request.TopK = value
	}
	if value, ok := optionalInt(output["maxOutputTokens"]); ok {
		request.MaxTokens = value
	}
	if options, ok := output["options"].(map[string]any); ok {
		request.Options = options
	}
	return nil
}

// ApplyChatHeaders runs chat.headers and merges returned transport headers.
func (runtime *Runtime) ApplyChatHeaders(ctx context.Context, request *llm.ChatRequest, chat ChatContext) error {
	if !runtime.Enabled() || request == nil {
		return nil
	}
	output, err := runtime.Trigger(ctx, "chat.headers", chat.input(), map[string]any{
		"headers": map[string]any{},
	})
	if err != nil {
		return err
	}
	headers, ok := output["headers"].(map[string]any)
	if !ok || len(headers) == 0 {
		return nil
	}
	if request.Headers == nil {
		request.Headers = map[string]string{}
	}
	for key, value := range headers {
		if text, ok := value.(string); ok {
			request.Headers[key] = text
		}
	}
	return nil
}

func (chat ChatContext) input() map[string]any {
	return map[string]any{
		"sessionID": chat.SessionID,
		"agent":     chat.Agent,
		"model":     chat.Model,
		"provider":  chat.Provider,
		"message":   chat.Message,
	}
}

func floatPointerValue(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}

func intPointerValue(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func optionalFloat(value any) (*float64, bool) {
	if value == nil {
		return nil, true
	}
	switch typed := value.(type) {
	case float64:
		return &typed, true
	case int:
		next := float64(typed)
		return &next, true
	default:
		return nil, false
	}
}

func optionalInt(value any) (*int, bool) {
	if value == nil {
		return nil, true
	}
	switch typed := value.(type) {
	case int:
		return &typed, true
	case float64:
		next := int(typed)
		return &next, true
	default:
		return nil, false
	}
}
