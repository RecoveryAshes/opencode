package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/RecoveryAshes/opencode/internal/auth"
)

const (
	defaultGitLabInstanceURL  = "https://gitlab.com"
	defaultGitLabAIGatewayURL = "https://cloud.gitlab.com"
	gitlabProviderVersion     = "6.6.0"
)

type gitLabDirectAccessTokenSource struct {
	InstanceURL  string
	APIKey       string
	FeatureFlags map[string]bool
	HTTPClient   *http.Client
	Headers      map[string]string
}

func (source gitLabDirectAccessTokenSource) Token(ctx context.Context) (string, error) {
	if strings.TrimSpace(source.APIKey) == "" {
		return "", fmt.Errorf("GITLAB_TOKEN is required")
	}
	instanceURL := strings.TrimRight(defaultString(source.InstanceURL, defaultGitLabInstanceURL), "/")
	payload := map[string]any{}
	if len(source.FeatureFlags) > 0 {
		payload["feature_flags"] = source.FeatureFlags
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode GitLab direct access request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, instanceURL+"/api/v4/ai/third_party_agents/direct_access", bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("build GitLab direct access request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	for key, value := range source.authHeaders() {
		if key != "" && value != "" {
			request.Header.Set(key, value)
		}
	}
	client := source.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("fetch GitLab direct access token: %w", err)
	}
	defer func() {
		_ = response.Body.Close()
	}()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return "", fmt.Errorf("read GitLab direct access response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("GitLab direct access status %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	var decoded struct {
		Token   string            `json:"token"`
		Headers map[string]string `json:"headers"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return "", fmt.Errorf("decode GitLab direct access response: %w", err)
	}
	if decoded.Token == "" {
		return "", fmt.Errorf("GitLab direct access response did not include token")
	}
	for key, value := range decoded.Headers {
		if strings.EqualFold(key, "x-api-key") || key == "" || value == "" {
			continue
		}
		if source.Headers != nil {
			source.Headers[key] = value
		}
	}
	return decoded.Token, nil
}

func (source gitLabDirectAccessTokenSource) authHeaders() map[string]string {
	if strings.HasPrefix(source.APIKey, "glpat-") || strings.HasPrefix(source.APIKey, "gloas-") {
		return map[string]string{"PRIVATE-TOKEN": source.APIKey}
	}
	return map[string]string{"Authorization": "Bearer " + source.APIKey}
}

func gitLabChatRequest(messages []Message, modelID string) (ChatRequest, error) {
	if strings.HasPrefix(modelID, "duo-workflow") {
		return ChatRequest{}, fmt.Errorf("gitlab workflow models require the GitLab workflow protocol, which is not migrated yet")
	}
	apiKey := firstEnv("OPENCODE_GITLAB_TOKEN", "GITLAB_TOKEN", "OPENCODE_GITLAB_API_KEY", "GITLAB_API_KEY")
	account, _ := auth.DefaultStore().Active("gitlab")
	if apiKey == "" && account != nil {
		switch account.Credential.Type {
		case "api":
			apiKey = account.Credential.Key
		case "oauth":
			apiKey = account.Credential.Access
		}
	}
	if apiKey == "" {
		return ChatRequest{}, fmt.Errorf("gitlab provider requires GITLAB_TOKEN")
	}
	instanceURL := defaultString(firstEnv("OPENCODE_GITLAB_INSTANCE_URL", "GITLAB_INSTANCE_URL"), defaultGitLabInstanceURL)
	aiGatewayURL := defaultString(firstEnv("OPENCODE_GITLAB_AI_GATEWAY_URL", "GITLAB_AI_GATEWAY_URL"), defaultGitLabAIGatewayURL)
	flags := gitLabFeatureFlags()
	headers := gitLabAIGatewayHeaders()
	if account != nil {
		mergeGitLabStoredMetadata(&instanceURL, &aiGatewayURL, headers, account.Credential.Metadata)
	}
	source := gitLabDirectAccessTokenSource{
		InstanceURL:  instanceURL,
		APIKey:       apiKey,
		FeatureFlags: flags,
		Headers:      headers,
	}
	mapping := gitLabModelMapping(modelID)
	switch mapping.Provider {
	case "anthropic":
		return ChatRequest{
			ProviderID:  "gitlab",
			Protocol:    "anthropic-messages",
			BaseURL:     strings.TrimRight(aiGatewayURL, "/") + "/ai/v1/proxy/anthropic",
			AuthHeader:  "Authorization",
			AuthScheme:  "Bearer",
			Headers:     headers,
			Model:       mapping.Model,
			Messages:    messages,
			TokenSource: source,
		}, nil
	case "openai":
		return ChatRequest{
			ProviderID:  "gitlab",
			Protocol:    "openai-compatible",
			BaseURL:     strings.TrimRight(aiGatewayURL, "/") + "/ai/v1/proxy/openai/v1",
			AuthHeader:  "Authorization",
			AuthScheme:  "Bearer",
			Headers:     headers,
			Model:       mapping.Model,
			Messages:    messages,
			TokenSource: source,
		}, nil
	default:
		return ChatRequest{}, fmt.Errorf("gitlab model %q is not supported", modelID)
	}
}

func mergeGitLabStoredMetadata(instanceURL *string, aiGatewayURL *string, headers map[string]string, metadata map[string]string) {
	for key, value := range metadata {
		if value == "" {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "instanceurl", "instance_url", "gitlab_instance_url":
			*instanceURL = value
		case "aigatewayurl", "ai_gateway_url", "gitlab_ai_gateway_url":
			*aiGatewayURL = value
		default:
			header, ok := strings.CutPrefix(key, "header:")
			if !ok {
				header, ok = strings.CutPrefix(key, "headers.")
			}
			if ok && header != "" {
				headers[header] = value
			}
		}
	}
}

type gitLabModelRoute struct {
	Provider string
	Model    string
}

func gitLabModelMapping(modelID string) gitLabModelRoute {
	switch modelID {
	case "", "duo-chat-sonnet-4-5":
		return gitLabModelRoute{Provider: "anthropic", Model: "claude-sonnet-4-5-20250929"}
	case "duo-chat-opus-4-7":
		return gitLabModelRoute{Provider: "anthropic", Model: "claude-opus-4-7"}
	case "duo-chat-opus-4-6":
		return gitLabModelRoute{Provider: "anthropic", Model: "claude-opus-4-6"}
	case "duo-chat-sonnet-4-6":
		return gitLabModelRoute{Provider: "anthropic", Model: "claude-sonnet-4-6"}
	case "duo-chat-opus-4-5":
		return gitLabModelRoute{Provider: "anthropic", Model: "claude-opus-4-5-20251101"}
	case "duo-chat-haiku-4-5":
		return gitLabModelRoute{Provider: "anthropic", Model: "claude-haiku-4-5-20251001"}
	case "duo-chat-gpt-5-1":
		return gitLabModelRoute{Provider: "openai", Model: "gpt-5.1-2025-11-13"}
	case "duo-chat-gpt-5-2":
		return gitLabModelRoute{Provider: "openai", Model: "gpt-5.2-2025-12-11"}
	case "duo-chat-gpt-5-4":
		return gitLabModelRoute{Provider: "openai", Model: "gpt-5.4-2026-03-05"}
	case "duo-chat-gpt-5-mini":
		return gitLabModelRoute{Provider: "openai", Model: "gpt-5-mini-2025-08-07"}
	case "duo-chat-gpt-5-4-mini":
		return gitLabModelRoute{Provider: "openai", Model: "gpt-5.4-mini"}
	case "duo-chat-gpt-5-4-nano":
		return gitLabModelRoute{Provider: "openai", Model: "gpt-5.4-nano"}
	case "duo-chat-gpt-5-codex":
		return gitLabModelRoute{Provider: "openai", Model: "gpt-5-codex"}
	case "duo-chat-gpt-5-2-codex":
		return gitLabModelRoute{Provider: "openai", Model: "gpt-5.2-codex"}
	case "duo-chat-gpt-5-3-codex":
		return gitLabModelRoute{Provider: "openai", Model: "gpt-5.3-codex"}
	default:
		return gitLabModelRoute{Provider: "anthropic", Model: modelID}
	}
}

func gitLabFeatureFlags() map[string]bool {
	return map[string]bool{
		"duo_agent_platform_agentic_chat": true,
		"duo_agent_platform":              true,
	}
}

func gitLabAIGatewayHeaders() map[string]string {
	return map[string]string{
		"User-Agent":     fmt.Sprintf("opencode/dev gitlab-ai-provider/%s (%s %s; %s)", gitlabProviderVersion, runtime.GOOS, osVersion(), runtime.GOARCH),
		"anthropic-beta": "context-1m-2025-08-07",
	}
}

func osVersion() string {
	if value := strings.TrimSpace(os.Getenv("OSTYPE")); value != "" {
		return value
	}
	return runtime.GOOS
}
