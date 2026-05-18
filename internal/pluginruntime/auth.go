package pluginruntime

import (
	"context"
	"fmt"
)

// AuthMethod mirrors the public provider auth method contract.
type AuthMethod struct {
	Type    string       `json:"type"`
	Label   string       `json:"label"`
	Prompts []AuthPrompt `json:"prompts,omitempty"`
}

// AuthPrompt describes a provider auth prompt.
type AuthPrompt struct {
	Type        string         `json:"type"`
	Key         string         `json:"key"`
	Message     string         `json:"message"`
	Placeholder string         `json:"placeholder,omitempty"`
	Options     []AuthOption   `json:"options,omitempty"`
	When        *AuthCondition `json:"when,omitempty"`
}

// AuthOption describes a select prompt option.
type AuthOption struct {
	Label string `json:"label"`
	Value string `json:"value"`
	Hint  string `json:"hint,omitempty"`
}

// AuthCondition is the supported prompt visibility rule.
type AuthCondition struct {
	Key   string `json:"key"`
	Op    string `json:"op"`
	Value string `json:"value"`
}

// Authorization is the public OAuth authorization response.
type Authorization struct {
	URL          string `json:"url"`
	Method       string `json:"method"`
	Instructions string `json:"instructions"`
}

// AuthAPIResult is returned when an API auth method completes immediately.
type AuthAPIResult struct {
	Type     string            `json:"type,omitempty"`
	Key      string            `json:"key,omitempty"`
	Provider string            `json:"provider,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// AuthMethods returns auth methods exposed by server plugin hooks.
func (runtime *Runtime) AuthMethods(ctx context.Context) (map[string][]AuthMethod, error) {
	if !runtime.Enabled() {
		return map[string][]AuthMethod{}, nil
	}
	output, err := runtime.Trigger(ctx, "auth.methods", nil, map[string]any{
		"methods": map[string]any{},
	})
	if err != nil {
		return nil, err
	}
	methods := map[string][]AuthMethod{}
	raw, ok := output["methods"].(map[string]any)
	if !ok {
		return methods, nil
	}
	for provider, value := range raw {
		list, err := decodeAuthMethods(value)
		if err != nil {
			return nil, fmt.Errorf("decode auth methods for %s: %w", provider, err)
		}
		methods[provider] = list
	}
	return methods, nil
}

// AuthorizeProvider runs the selected provider auth method.
func (runtime *Runtime) AuthorizeProvider(ctx context.Context, providerID string, method int, inputs map[string]string) (*Authorization, *AuthAPIResult, bool, error) {
	if !runtime.Enabled() {
		return nil, nil, false, nil
	}
	output, err := runtime.Trigger(ctx, "auth.authorize", map[string]any{
		"providerID": providerID,
		"method":     method,
		"inputs":     inputs,
	}, map[string]any{})
	if err != nil {
		return nil, nil, false, err
	}
	raw, ok := output["authorization"]
	if !ok || raw == nil {
		return nil, nil, true, nil
	}
	record, ok := raw.(map[string]any)
	if !ok {
		return nil, nil, true, nil
	}
	if url, ok := record["url"].(string); ok {
		return &Authorization{
			URL:          url,
			Method:       stringValue(record["method"]),
			Instructions: stringValue(record["instructions"]),
		}, nil, true, nil
	}
	return nil, &AuthAPIResult{
		Type:     stringValue(record["type"]),
		Key:      stringValue(record["key"]),
		Provider: stringValue(record["provider"]),
		Metadata: stringStringMap(record["metadata"]),
	}, true, nil
}

func decodeAuthMethods(value any) ([]AuthMethod, error) {
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("methods must be an array")
	}
	result := make([]AuthMethod, 0, len(items))
	for _, item := range items {
		record, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("method must be an object")
		}
		method := AuthMethod{
			Type:  stringValue(record["type"]),
			Label: stringValue(record["label"]),
		}
		if rawPrompts, ok := record["prompts"].([]any); ok {
			prompts := make([]AuthPrompt, 0, len(rawPrompts))
			for _, rawPrompt := range rawPrompts {
				prompt, err := decodeAuthPrompt(rawPrompt)
				if err != nil {
					return nil, err
				}
				prompts = append(prompts, prompt)
			}
			method.Prompts = prompts
		}
		result = append(result, method)
	}
	return result, nil
}

func decodeAuthPrompt(value any) (AuthPrompt, error) {
	record, ok := value.(map[string]any)
	if !ok {
		return AuthPrompt{}, fmt.Errorf("prompt must be an object")
	}
	prompt := AuthPrompt{
		Type:        stringValue(record["type"]),
		Key:         stringValue(record["key"]),
		Message:     stringValue(record["message"]),
		Placeholder: stringValue(record["placeholder"]),
	}
	if rawOptions, ok := record["options"].([]any); ok {
		options := make([]AuthOption, 0, len(rawOptions))
		for _, rawOption := range rawOptions {
			option, ok := rawOption.(map[string]any)
			if !ok {
				return AuthPrompt{}, fmt.Errorf("prompt option must be an object")
			}
			options = append(options, AuthOption{
				Label: stringValue(option["label"]),
				Value: stringValue(option["value"]),
				Hint:  stringValue(option["hint"]),
			})
		}
		prompt.Options = options
	}
	if rawWhen, ok := record["when"].(map[string]any); ok {
		prompt.When = &AuthCondition{
			Key:   stringValue(rawWhen["key"]),
			Op:    stringValue(rawWhen["op"]),
			Value: stringValue(rawWhen["value"]),
		}
	}
	return prompt, nil
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func stringStringMap(value any) map[string]string {
	record, ok := value.(map[string]any)
	if !ok || len(record) == 0 {
		return nil
	}
	result := map[string]string{}
	for key, raw := range record {
		if text, ok := raw.(string); ok {
			result[key] = text
		}
	}
	return result
}
