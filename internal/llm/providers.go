// Package llm defines provider contracts for the Go runtime.
package llm

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/RecoveryAshes/opencode/internal/config"
)

// Provider describes an LLM provider that must be owned by the Go runtime.
type Provider struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Protocols []string `json:"protocols"`
}

// PublicModel describes the provider model shape consumed by local clients.
type PublicModel struct {
	ID           string                    `json:"id"`
	ProviderID   string                    `json:"providerID"`
	API          map[string]any            `json:"api"`
	Name         string                    `json:"name"`
	Family       string                    `json:"family,omitempty"`
	Capabilities Capabilities              `json:"capabilities"`
	Cost         Cost                      `json:"cost"`
	Limit        Limit                     `json:"limit"`
	Status       string                    `json:"status"`
	Options      map[string]any            `json:"options"`
	Headers      map[string]string         `json:"headers"`
	ReleaseDate  string                    `json:"release_date"`
	Variants     map[string]map[string]any `json:"variants,omitempty"`
}

// Capabilities describes model feature support.
type Capabilities struct {
	Temperature bool       `json:"temperature"`
	Reasoning   bool       `json:"reasoning"`
	Attachment  bool       `json:"attachment"`
	Toolcall    bool       `json:"toolcall"`
	Input       Modalities `json:"input"`
	Output      Modalities `json:"output"`
	Interleaved any        `json:"interleaved"`
}

// Modalities is a model modality bitmap.
type Modalities struct {
	Text  bool `json:"text"`
	Audio bool `json:"audio"`
	Image bool `json:"image"`
	Video bool `json:"video"`
	PDF   bool `json:"pdf"`
}

// Cost describes per-token model cost.
type Cost struct {
	Input                float64            `json:"input"`
	Output               float64            `json:"output"`
	Cache                CacheCost          `json:"cache"`
	Tiers                []CostTier         `json:"tiers,omitempty"`
	ExperimentalOver200K *Over200KModelCost `json:"experimentalOver200K,omitempty"`
}

// CacheCost describes prompt-cache pricing.
type CacheCost struct {
	Read  float64 `json:"read"`
	Write float64 `json:"write"`
}

// CostTier describes model pricing that applies above a token threshold.
type CostTier struct {
	Input  float64      `json:"input"`
	Output float64      `json:"output"`
	Cache  CacheCost    `json:"cache"`
	Tier   CostTierSpec `json:"tier"`
}

// CostTierSpec describes the condition for a tiered model price.
type CostTierSpec struct {
	Type string  `json:"type"`
	Size float64 `json:"size"`
}

// Over200KModelCost preserves models.dev context_over_200k pricing under the
// public provider contract name used by the TypeScript implementation.
type Over200KModelCost struct {
	Input  float64   `json:"input"`
	Output float64   `json:"output"`
	Cache  CacheCost `json:"cache"`
}

// Limit describes model token limits.
type Limit struct {
	Context int `json:"context"`
	Input   int `json:"input,omitempty"`
	Output  int `json:"output"`
}

// PublicProvider matches the public provider DTO used by the TypeScript HTTP API.
type PublicProvider struct {
	ID      string                 `json:"id"`
	Name    string                 `json:"name"`
	Source  string                 `json:"source"`
	Env     []string               `json:"env"`
	Key     string                 `json:"key,omitempty"`
	Options map[string]any         `json:"options"`
	Models  map[string]PublicModel `json:"models"`
}

// ProviderListResult is returned by /provider.
type ProviderListResult struct {
	All       []PublicProvider  `json:"all"`
	Default   map[string]string `json:"default"`
	Connected []string          `json:"connected"`
}

// ConfigProvidersResult is returned by /config/providers.
type ConfigProvidersResult struct {
	Providers []PublicProvider  `json:"providers"`
	Default   map[string]string `json:"default"`
}

// AllProviders returns the provider inventory required for the migration.
func AllProviders() []Provider {
	return append([]Provider(nil), providers...)
}

// ProviderIDs returns provider IDs in stable CLI order.
func ProviderIDs() []string {
	result := make([]string, 0, len(providers))
	for _, provider := range providers {
		result = append(result, provider.ID)
	}
	return result
}

// ListProviders builds the public provider list using local config filters and
// custom provider model declarations. Network model discovery remains a later
// migration slice.
func ListProviders(info config.Info) ProviderListResult {
	filtered := filteredProviders(info)
	defaults := defaultModelIDs(filtered)
	return ProviderListResult{
		All:       filtered,
		Default:   defaults,
		Connected: []string{},
	}
}

// ConfigProviders builds the /config/providers response shape.
func ConfigProviders(info config.Info) ConfigProvidersResult {
	filtered := filteredProviders(info)
	return ConfigProvidersResult{
		Providers: filtered,
		Default:   defaultModelIDs(filtered),
	}
}

func filteredProviders(info config.Info) []PublicProvider {
	enabled := stringSetFromConfig(info["enabled_providers"])
	disabled := stringSetFromConfig(info["disabled_providers"])
	custom := configuredProviders(info)

	result := []PublicProvider{}
	for _, provider := range providers {
		if len(enabled) > 0 && !enabled[provider.ID] {
			continue
		}
		if disabled[provider.ID] {
			continue
		}
		public := publicProvider(provider)
		if override, ok := custom[provider.ID]; ok {
			public = mergePublicProvider(public, override)
		}
		result = append(result, public)
		delete(custom, provider.ID)
	}

	extraIDs := make([]string, 0, len(custom))
	for id := range custom {
		if len(enabled) > 0 && !enabled[id] {
			continue
		}
		if disabled[id] {
			continue
		}
		extraIDs = append(extraIDs, id)
	}
	sort.Strings(extraIDs)
	for _, id := range extraIDs {
		result = append(result, custom[id])
	}
	return result
}

func configuredProviders(info config.Info) map[string]PublicProvider {
	rawProviders, ok := info["provider"].(map[string]any)
	if !ok {
		return map[string]PublicProvider{}
	}
	result := map[string]PublicProvider{}
	for id, raw := range rawProviders {
		record, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		result[id] = providerFromConfig(id, record)
	}
	return result
}

func providerFromConfig(id string, input map[string]any) PublicProvider {
	name := stringFromAny(input["name"], providerName(id))
	env := stringSliceFromAny(input["env"])
	if len(env) == 0 {
		env = providerEnvVars(id)
	}
	options, _ := input["options"].(map[string]any)
	models := map[string]PublicModel{}
	if rawModels, ok := input["models"].(map[string]any); ok {
		for key, raw := range rawModels {
			modelRecord, _ := raw.(map[string]any)
			modelID := stringFromAny(modelRecord["id"], key)
			models[modelID] = modelFromConfig(id, modelID, modelRecord)
		}
	}
	return PublicProvider{
		ID:      stringFromAny(input["id"], id),
		Name:    name,
		Source:  "config",
		Env:     env,
		Key:     stringFromAny(input["key"], ""),
		Options: cloneProviderAnyMap(options),
		Models:  models,
	}
}

func publicProvider(provider Provider) PublicProvider {
	modelID := defaultProviderModel(provider.ID)
	models := map[string]PublicModel{}
	if modelID != "" {
		models[modelID] = defaultPublicModel(provider.ID, modelID)
	}
	return PublicProvider{
		ID:      provider.ID,
		Name:    provider.Name,
		Source:  "custom",
		Env:     providerEnvVars(provider.ID),
		Options: map[string]any{},
		Models:  models,
	}
}

func mergePublicProvider(base PublicProvider, override PublicProvider) PublicProvider {
	if override.ID != "" {
		base.ID = override.ID
	}
	if override.Name != "" && override.Name != providerName(base.ID) {
		base.Name = override.Name
	}
	if override.Source != "" {
		base.Source = override.Source
	}
	if len(override.Env) > 0 {
		base.Env = override.Env
	}
	if override.Key != "" {
		base.Key = override.Key
	}
	base.Options = mergeProviderAnyMap(base.Options, override.Options)
	if base.Models == nil {
		base.Models = map[string]PublicModel{}
	}
	for id, model := range override.Models {
		base.Models[id] = model
	}
	return base
}

func defaultPublicModel(providerID string, modelID string) PublicModel {
	return PublicModel{
		ID:         modelID,
		ProviderID: providerID,
		API: map[string]any{
			"id":  providerID,
			"url": "",
			"npm": "",
		},
		Name: modelID,
		Capabilities: Capabilities{
			Temperature: true,
			Toolcall:    true,
			Input:       Modalities{Text: true},
			Output:      Modalities{Text: true},
			Interleaved: false,
		},
		Cost:        Cost{Cache: CacheCost{}},
		Limit:       Limit{Context: 128000, Output: 4096},
		Status:      "active",
		Options:     map[string]any{},
		Headers:     map[string]string{},
		ReleaseDate: "",
	}
}

func modelFromConfig(providerID string, modelID string, input map[string]any) PublicModel {
	model := defaultPublicModel(providerID, modelID)
	model.Name = stringFromAny(input["name"], model.Name)
	model.Family = stringFromAny(input["family"], "")
	model.ReleaseDate = stringFromAny(input["release_date"], "")
	model.Status = stringFromAny(input["status"], model.Status)
	if options, ok := input["options"].(map[string]any); ok {
		model.Options = cloneProviderAnyMap(options)
	}
	if headers, ok := input["headers"].(map[string]any); ok {
		model.Headers = stringMapFromAny(headers)
	}
	if limit, ok := input["limit"].(map[string]any); ok {
		model.Limit = Limit{
			Context: intFromAny(limit["context"], model.Limit.Context),
			Input:   intFromAny(limit["input"], model.Limit.Input),
			Output:  intFromAny(limit["output"], model.Limit.Output),
		}
	}
	if cost, ok := input["cost"].(map[string]any); ok {
		model.Cost = Cost{
			Input:  floatFromAny(cost["input"], model.Cost.Input),
			Output: floatFromAny(cost["output"], model.Cost.Output),
			Cache: CacheCost{
				Read:  floatFromAny(cost["cache_read"], model.Cost.Cache.Read),
				Write: floatFromAny(cost["cache_write"], model.Cost.Cache.Write),
			},
			Tiers:                costTiersFromConfig(cost["tiers"]),
			ExperimentalOver200K: over200KCostFromConfig(cost["context_over_200k"]),
		}
	}
	model.Capabilities = capabilitiesFromConfig(input, model.Capabilities)
	return model
}

func costTiersFromConfig(input any) []CostTier {
	raw, ok := input.([]any)
	if !ok {
		return nil
	}
	result := []CostTier{}
	for _, item := range raw {
		record, ok := item.(map[string]any)
		if !ok {
			continue
		}
		tierRecord, _ := record["tier"].(map[string]any)
		result = append(result, CostTier{
			Input:  floatFromAny(record["input"], 0),
			Output: floatFromAny(record["output"], 0),
			Cache: CacheCost{
				Read:  floatFromAny(record["cache_read"], 0),
				Write: floatFromAny(record["cache_write"], 0),
			},
			Tier: CostTierSpec{
				Type: stringFromAny(tierRecord["type"], ""),
				Size: floatFromAny(tierRecord["size"], 0),
			},
		})
	}
	return result
}

func over200KCostFromConfig(input any) *Over200KModelCost {
	record, ok := input.(map[string]any)
	if !ok {
		return nil
	}
	return &Over200KModelCost{
		Input:  floatFromAny(record["input"], 0),
		Output: floatFromAny(record["output"], 0),
		Cache: CacheCost{
			Read:  floatFromAny(record["cache_read"], 0),
			Write: floatFromAny(record["cache_write"], 0),
		},
	}
}

func capabilitiesFromConfig(input map[string]any, fallback Capabilities) Capabilities {
	result := fallback
	result.Attachment = boolFromAny(input["attachment"], result.Attachment)
	result.Reasoning = boolFromAny(input["reasoning"], result.Reasoning)
	result.Temperature = boolFromAny(input["temperature"], result.Temperature)
	result.Toolcall = boolFromAny(input["tool_call"], result.Toolcall)
	if interleaved, ok := input["interleaved"]; ok {
		result.Interleaved = interleaved
	}
	if modalities, ok := input["modalities"].(map[string]any); ok {
		if inputModalities, ok := modalities["input"].([]any); ok {
			result.Input = modalitiesFromList(inputModalities)
		}
		if outputModalities, ok := modalities["output"].([]any); ok {
			result.Output = modalitiesFromList(outputModalities)
		}
	}
	return result
}

func modalitiesFromList(input []any) Modalities {
	result := Modalities{}
	for _, item := range input {
		switch item {
		case "text":
			result.Text = true
		case "audio":
			result.Audio = true
		case "image":
			result.Image = true
		case "video":
			result.Video = true
		case "pdf":
			result.PDF = true
		}
	}
	return result
}

func defaultModelIDs(providers []PublicProvider) map[string]string {
	result := map[string]string{}
	for _, provider := range providers {
		if len(provider.Models) == 0 {
			continue
		}
		result[provider.ID] = SortedModelIDs(provider.Models)[0]
	}
	return result
}

// SortedModelIDs applies the same user-facing model priority ordering as the
// TypeScript provider catalog.
func SortedModelIDs(models map[string]PublicModel) []string {
	ids := make([]string, 0, len(models))
	for id := range models {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		return CompareModelIDs(ids[i], ids[j]) < 0
	})
	return ids
}

// SortPublicModels applies the public model catalog order and keeps provider
// IDs as a deterministic tie-breaker when multiple providers expose the same
// model id.
func SortPublicModels(models []PublicModel) {
	sort.Slice(models, func(i, j int) bool {
		if compare := CompareModelIDs(models[i].ID, models[j].ID); compare != 0 {
			return compare < 0
		}
		return models[i].ProviderID < models[j].ProviderID
	})
}

// CompareModelIDs orders model ids using the migrated TypeScript priority
// rules.
func CompareModelIDs(left string, right string) int {
	leftRank := modelSortRank(left)
	rightRank := modelSortRank(right)
	if leftRank < rightRank {
		return -1
	}
	if leftRank > rightRank {
		return 1
	}
	return 0
}

func modelSortRank(id string) string {
	priority := []string{"gpt-5", "claude-sonnet-4", "big-pickle", "gemini-3-pro"}
	score := 0
	for index, filter := range priority {
		if strings.Contains(id, filter) {
			score = index + 1
			break
		}
	}
	latest := 1
	if strings.Contains(id, "latest") {
		latest = 0
	}
	return fmt.Sprintf("%02d:%02d:%s", 99-score, latest, invertString(id))
}

func invertString(input string) string {
	output := []byte(input)
	for i, value := range output {
		output[i] = 255 - value
	}
	return string(output)
}

func providerName(id string) string {
	for _, provider := range providers {
		if provider.ID == id {
			return provider.Name
		}
	}
	return id
}

func providerEnvVars(id string) []string {
	env := map[string][]string{
		"openai":                {"OPENAI_API_KEY"},
		"anthropic":             {"ANTHROPIC_API_KEY"},
		"google":                {"GOOGLE_GENERATIVE_AI_API_KEY", "GEMINI_API_KEY"},
		"google-vertex":         {"GOOGLE_VERTEX_PROJECT", "GOOGLE_APPLICATION_CREDENTIALS"},
		"azure":                 {"AZURE_OPENAI_API_KEY", "AZURE_OPENAI_RESOURCE_NAME"},
		"amazon-bedrock":        {"AWS_BEARER_TOKEN_BEDROCK", "AWS_ACCESS_KEY_ID", "AWS_PROFILE"},
		"openrouter":            {"OPENROUTER_API_KEY"},
		"xai":                   {"XAI_API_KEY"},
		"groq":                  {"GROQ_API_KEY"},
		"mistral":               {"MISTRAL_API_KEY"},
		"baseten":               {"BASETEN_API_KEY"},
		"deepseek":              {"DEEPSEEK_API_KEY"},
		"fireworks":             {"FIREWORKS_API_KEY"},
		"perplexity":            {"PERPLEXITY_API_KEY"},
		"cohere":                {"COHERE_API_KEY"},
		"cerebras":              {"CEREBRAS_API_KEY"},
		"deepinfra":             {"DEEPINFRA_API_KEY"},
		"together":              {"TOGETHER_API_KEY", "TOGETHER_AI_API_KEY"},
		"alibaba":               {"ALIBABA_API_KEY", "DASHSCOPE_API_KEY"},
		"vercel":                {"VERCEL_API_KEY", "AI_GATEWAY_API_KEY"},
		"cloudflare-ai-gateway": {"CLOUDFLARE_API_TOKEN", "CF_AIG_TOKEN"},
		"cloudflare-workers-ai": {"CLOUDFLARE_WORKERS_AI_TOKEN", "CLOUDFLARE_API_KEY"},
		"github-copilot":        {"GITHUB_TOKEN", "GITHUB_COPILOT_API_KEY"},
		"digitalocean":          {"DIGITALOCEAN_API_KEY", "DIGITALOCEAN_ACCESS_TOKEN"},
		"gitlab-duo":            {"GITLAB_DUO_API_KEY"},
		"venice":                {"VENICE_API_KEY"},
		"openai-compatible":     {"OPENAI_API_KEY"},
	}
	return append([]string(nil), env[id]...)
}

func defaultProviderModel(id string) string {
	switch id {
	case "openai", "azure", "openai-compatible":
		return "gpt-4o-mini"
	case "anthropic":
		return "claude-sonnet-4-5"
	case "google":
		return "gemini-2.5-flash"
	case "amazon-bedrock":
		return "us.amazon.nova-micro-v1:0"
	case "cohere":
		return "command-a-03-2025"
	default:
		if profile, ok := openAICompatibleProfiles[id]; ok {
			return profile.DefaultModel
		}
		return ""
	}
}

func stringSetFromConfig(input any) map[string]bool {
	result := map[string]bool{}
	for _, item := range stringSliceFromAny(input) {
		result[item] = true
	}
	return result
}

func stringSliceFromAny(input any) []string {
	raw, ok := input.([]any)
	if !ok {
		return nil
	}
	result := []string{}
	for _, item := range raw {
		if value, ok := item.(string); ok {
			result = append(result, value)
		}
	}
	return result
}

func stringMapFromAny(input map[string]any) map[string]string {
	result := map[string]string{}
	for key, value := range input {
		if text, ok := value.(string); ok {
			result[key] = text
		}
	}
	return result
}

func cloneProviderAnyMap(input map[string]any) map[string]any {
	if len(input) == 0 {
		return map[string]any{}
	}
	result := make(map[string]any, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

func mergeProviderAnyMap(left map[string]any, right map[string]any) map[string]any {
	result := cloneProviderAnyMap(left)
	for key, value := range right {
		result[key] = value
	}
	return result
}

func stringFromAny(input any, fallback string) string {
	if value, ok := input.(string); ok && value != "" {
		return value
	}
	return fallback
}

func boolFromAny(input any, fallback bool) bool {
	if value, ok := input.(bool); ok {
		return value
	}
	return fallback
}

func intFromAny(input any, fallback int) int {
	switch value := input.(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		return fallback
	}
}

func floatFromAny(input any, fallback float64) float64 {
	switch value := input.(type) {
	case float64:
		return value
	case int:
		return float64(value)
	case int64:
		return float64(value)
	default:
		return fallback
	}
}

// ResolveChatRequest maps a provider/model pair to the first Go-native chat
// route. Providers with distinct protocols are listed but return explicit
// errors until their native wire clients are migrated.
func ResolveChatRequest(messages []Message, providerID string, modelID string) (ChatRequest, error) {
	providerID = defaultString(providerID, "openai-compatible")
	if profile, ok := openAICompatibleProfiles[providerID]; ok {
		request := profile.chatRequest(messages, modelID)
		if request.BaseURL == "" {
			return ChatRequest{}, fmt.Errorf("%s provider requires a base URL environment variable", providerID)
		}
		return request, nil
	}
	switch providerID {
	case "openai":
		profile := responsesProfile{
			ProviderID:     "openai",
			DefaultBaseURL: defaultOpenAICompatibleBaseURL,
			BaseURLEnvVars: []string{"OPENCODE_OPENAI_BASE_URL", "OPENAI_BASE_URL"},
			APIKeyEnvVars:  []string{"OPENCODE_OPENAI_API_KEY", "OPENAI_API_KEY"},
			ModelEnvVars:   []string{"OPENCODE_OPENAI_MODEL", "OPENAI_MODEL"},
			DefaultModel:   "gpt-4o-mini",
			AuthHeader:     "Authorization",
			AuthScheme:     "Bearer",
		}
		return profile.chatRequest(messages, modelID), nil
	case "azure":
		profile := responsesProfile{
			ProviderID:     "azure",
			DefaultBaseURL: azureBaseURL(),
			BaseURLEnvVars: []string{"OPENCODE_AZURE_OPENAI_BASE_URL", "AZURE_OPENAI_BASE_URL"},
			APIKeyEnvVars:  []string{"OPENCODE_AZURE_OPENAI_API_KEY", "AZURE_OPENAI_API_KEY"},
			ModelEnvVars:   []string{"OPENCODE_AZURE_OPENAI_MODEL", "AZURE_OPENAI_MODEL"},
			DefaultModel:   "gpt-4o-mini",
			AuthHeader:     "api-key",
			QueryParams:    map[string]string{"api-version": defaultString(firstEnv("OPENCODE_AZURE_OPENAI_API_VERSION", "AZURE_OPENAI_API_VERSION"), "v1")},
		}
		if profile.DefaultBaseURL == "" {
			return ChatRequest{}, fmt.Errorf("azure provider requires AZURE_OPENAI_BASE_URL or AZURE_OPENAI_RESOURCE_NAME")
		}
		return profile.chatRequest(messages, modelID), nil
	case "anthropic":
		profile := anthropicProfile{
			ProviderID:     "anthropic",
			DefaultBaseURL: "https://api.anthropic.com/v1",
			BaseURLEnvVars: []string{"OPENCODE_ANTHROPIC_BASE_URL", "ANTHROPIC_BASE_URL"},
			APIKeyEnvVars:  []string{"OPENCODE_ANTHROPIC_API_KEY", "ANTHROPIC_API_KEY"},
			ModelEnvVars:   []string{"OPENCODE_ANTHROPIC_MODEL", "ANTHROPIC_MODEL"},
			DefaultModel:   "claude-sonnet-4-5",
			Headers:        map[string]string{"anthropic-version": "2023-06-01"},
		}
		return profile.chatRequest(messages, modelID), nil
	case "google":
		profile := geminiProfile{
			ProviderID:     "google",
			DefaultBaseURL: "https://generativelanguage.googleapis.com/v1beta",
			BaseURLEnvVars: []string{"OPENCODE_GOOGLE_BASE_URL", "GOOGLE_GENERATIVE_AI_BASE_URL", "GEMINI_BASE_URL"},
			APIKeyEnvVars: []string{
				"OPENCODE_GOOGLE_GENERATIVE_AI_API_KEY",
				"GOOGLE_GENERATIVE_AI_API_KEY",
				"GEMINI_API_KEY",
			},
			ModelEnvVars: []string{"OPENCODE_GOOGLE_MODEL", "GOOGLE_GENERATIVE_AI_MODEL", "GEMINI_MODEL"},
			DefaultModel: "gemini-2.5-flash",
		}
		return profile.chatRequest(messages, modelID), nil
	case "amazon-bedrock":
		profile := bedrockProfile{
			ProviderID:     "amazon-bedrock",
			Region:         bedrockRegion(),
			Profile:        firstEnv("OPENCODE_AWS_PROFILE", "OPENCODE_BEDROCK_PROFILE", "AWS_PROFILE"),
			DefaultBaseURL: bedrockBaseURL(),
			BaseURLEnvVars: []string{"OPENCODE_BEDROCK_BASE_URL", "BEDROCK_BASE_URL"},
			APIKeyEnvVars:  []string{"OPENCODE_AWS_BEARER_TOKEN_BEDROCK", "AWS_BEARER_TOKEN_BEDROCK"},
			ModelEnvVars:   []string{"OPENCODE_BEDROCK_MODEL", "BEDROCK_MODEL_ID"},
			DefaultModel:   "us.amazon.nova-micro-v1:0",
			Credentials:    bedrockCredentialsFromEnv(),
		}
		request := profile.chatRequest(messages, modelID)
		if request.APIKey == "" && request.AWSCredentials == nil && request.AWSProfile == "" && !bedrockDefaultCredentialChainConfigured() {
			return ChatRequest{}, fmt.Errorf("amazon-bedrock provider requires AWS_BEARER_TOKEN_BEDROCK or AWS credentials")
		}
		return request, nil
	case "cloudflare-ai-gateway":
		profile := openAIProfile{
			ProviderID:     "cloudflare-ai-gateway",
			DefaultBaseURL: cloudflareAIGatewayBaseURL(),
			BaseURLEnvVars: []string{"OPENCODE_CLOUDFLARE_AI_GATEWAY_BASE_URL", "CLOUDFLARE_AI_GATEWAY_BASE_URL"},
			APIKeyEnvVars: []string{
				"OPENCODE_CLOUDFLARE_PROVIDER_API_KEY",
				"CLOUDFLARE_PROVIDER_API_KEY",
				"OPENAI_API_KEY",
			},
			ModelEnvVars: []string{"OPENCODE_CLOUDFLARE_AI_GATEWAY_MODEL", "CLOUDFLARE_AI_GATEWAY_MODEL"},
			AuthHeader:   "Authorization",
			AuthScheme:   "Bearer",
			Headers:      cloudflareAIGatewayHeaders(),
		}
		request := profile.chatRequest(messages, modelID)
		if request.BaseURL == "" {
			return ChatRequest{}, fmt.Errorf("cloudflare-ai-gateway provider requires CLOUDFLARE_ACCOUNT_ID or CLOUDFLARE_AI_GATEWAY_BASE_URL")
		}
		return request, nil
	case "cloudflare-workers-ai":
		profile := openAIProfile{
			ProviderID:     "cloudflare-workers-ai",
			DefaultBaseURL: cloudflareWorkersAIBaseURL(),
			BaseURLEnvVars: []string{"OPENCODE_CLOUDFLARE_WORKERS_AI_BASE_URL", "CLOUDFLARE_WORKERS_AI_BASE_URL"},
			APIKeyEnvVars: []string{
				"OPENCODE_CLOUDFLARE_WORKERS_AI_TOKEN",
				"CLOUDFLARE_API_KEY",
				"CLOUDFLARE_WORKERS_AI_TOKEN",
			},
			ModelEnvVars: []string{"OPENCODE_CLOUDFLARE_WORKERS_AI_MODEL", "CLOUDFLARE_WORKERS_AI_MODEL"},
			AuthHeader:   "Authorization",
			AuthScheme:   "Bearer",
		}
		request := profile.chatRequest(messages, modelID)
		if request.BaseURL == "" {
			return ChatRequest{}, fmt.Errorf("cloudflare-workers-ai provider requires CLOUDFLARE_ACCOUNT_ID or CLOUDFLARE_WORKERS_AI_BASE_URL")
		}
		return request, nil
	case "cohere":
		profile := cohereProfile{
			ProviderID:     "cohere",
			DefaultBaseURL: "https://api.cohere.com/v2",
			BaseURLEnvVars: []string{"OPENCODE_COHERE_BASE_URL", "COHERE_BASE_URL"},
			APIKeyEnvVars:  []string{"OPENCODE_COHERE_API_KEY", "COHERE_API_KEY"},
			ModelEnvVars:   []string{"OPENCODE_COHERE_MODEL", "COHERE_MODEL"},
			DefaultModel:   "command-a-03-2025",
		}
		return profile.chatRequest(messages, modelID), nil
	case "vercel":
		profile := openAIProfile{
			ProviderID:     "vercel",
			DefaultBaseURL: "https://ai-gateway.vercel.sh/v3/ai",
			BaseURLEnvVars: []string{"OPENCODE_VERCEL_BASE_URL", "VERCEL_BASE_URL", "AI_GATEWAY_BASE_URL"},
			APIKeyEnvVars:  []string{"OPENCODE_VERCEL_API_KEY", "VERCEL_API_KEY", "AI_GATEWAY_API_KEY"},
			ModelEnvVars:   []string{"OPENCODE_VERCEL_MODEL", "VERCEL_MODEL", "AI_GATEWAY_MODEL"},
			AuthHeader:     "Authorization",
			AuthScheme:     "Bearer",
			Headers: map[string]string{
				"http-referer": "https://opencode.ai/",
				"x-title":      "opencode",
			},
		}
		return profile.chatRequest(messages, modelID), nil
	case "google-vertex":
		return ChatRequest{}, fmt.Errorf("%s provider uses a non-OpenAI chat protocol that has not been migrated yet", providerID)
	default:
		return ChatRequest{}, fmt.Errorf("unknown provider %q", providerID)
	}
}

type cohereProfile struct {
	ProviderID     string
	DefaultBaseURL string
	BaseURLEnvVars []string
	APIKeyEnvVars  []string
	ModelEnvVars   []string
	DefaultModel   string
}

func (profile cohereProfile) chatRequest(messages []Message, modelID string) ChatRequest {
	return ChatRequest{
		ProviderID: profile.ProviderID,
		Protocol:   "cohere-chat",
		BaseURL:    defaultString(firstEnv(profile.BaseURLEnvVars...), profile.DefaultBaseURL),
		APIKey:     firstEnv(profile.APIKeyEnvVars...),
		AuthHeader: "Authorization",
		AuthScheme: "Bearer",
		Model:      defaultString(modelID, defaultString(firstEnv(profile.ModelEnvVars...), profile.DefaultModel)),
		Messages:   messages,
	}
}

type responsesProfile struct {
	ProviderID     string
	DefaultBaseURL string
	BaseURLEnvVars []string
	APIKeyEnvVars  []string
	ModelEnvVars   []string
	DefaultModel   string
	AuthHeader     string
	AuthScheme     string
	QueryParams    map[string]string
}

func (profile responsesProfile) chatRequest(messages []Message, modelID string) ChatRequest {
	return ChatRequest{
		ProviderID:  profile.ProviderID,
		Protocol:    "openai-responses",
		BaseURL:     defaultString(firstEnv(profile.BaseURLEnvVars...), profile.DefaultBaseURL),
		APIKey:      firstEnv(profile.APIKeyEnvVars...),
		AuthHeader:  profile.AuthHeader,
		AuthScheme:  profile.AuthScheme,
		QueryParams: cloneStringMap(profile.QueryParams),
		Model:       defaultString(modelID, defaultString(firstEnv(profile.ModelEnvVars...), profile.DefaultModel)),
		Messages:    messages,
	}
}

type bedrockProfile struct {
	ProviderID     string
	Region         string
	Profile        string
	DefaultBaseURL string
	BaseURLEnvVars []string
	APIKeyEnvVars  []string
	ModelEnvVars   []string
	DefaultModel   string
	Credentials    *AWSCredentials
}

func (profile bedrockProfile) chatRequest(messages []Message, modelID string) ChatRequest {
	return ChatRequest{
		ProviderID:     profile.ProviderID,
		Protocol:       "bedrock-converse",
		BaseURL:        defaultString(firstEnv(profile.BaseURLEnvVars...), profile.DefaultBaseURL),
		APIKey:         firstEnv(profile.APIKeyEnvVars...),
		AuthHeader:     "Authorization",
		AuthScheme:     "Bearer",
		Model:          defaultString(modelID, defaultString(firstEnv(profile.ModelEnvVars...), profile.DefaultModel)),
		Messages:       messages,
		AWSRegion:      profile.Region,
		AWSProfile:     profile.Profile,
		AWSCredentials: profile.Credentials,
	}
}

type geminiProfile struct {
	ProviderID     string
	DefaultBaseURL string
	BaseURLEnvVars []string
	APIKeyEnvVars  []string
	ModelEnvVars   []string
	DefaultModel   string
}

func (profile geminiProfile) chatRequest(messages []Message, modelID string) ChatRequest {
	return ChatRequest{
		ProviderID: profile.ProviderID,
		Protocol:   "gemini",
		BaseURL:    defaultString(firstEnv(profile.BaseURLEnvVars...), profile.DefaultBaseURL),
		APIKey:     firstEnv(profile.APIKeyEnvVars...),
		AuthHeader: "x-goog-api-key",
		Model:      defaultString(modelID, defaultString(firstEnv(profile.ModelEnvVars...), profile.DefaultModel)),
		Messages:   messages,
	}
}

var providers = []Provider{
	{ID: "openai", Name: "OpenAI", Protocols: []string{"responses", "chat-completions"}},
	{ID: "anthropic", Name: "Anthropic", Protocols: []string{"messages"}},
	{ID: "google", Name: "Gemini", Protocols: []string{"generate-content"}},
	{ID: "google-vertex", Name: "Vertex AI", Protocols: []string{"generate-content"}},
	{ID: "azure", Name: "Azure OpenAI", Protocols: []string{"responses", "chat-completions"}},
	{ID: "amazon-bedrock", Name: "Amazon Bedrock", Protocols: []string{"converse", "invoke-model"}},
	{ID: "openrouter", Name: "OpenRouter", Protocols: []string{"openai-compatible"}},
	{ID: "xai", Name: "xAI", Protocols: []string{"openai-compatible"}},
	{ID: "groq", Name: "Groq", Protocols: []string{"openai-compatible"}},
	{ID: "mistral", Name: "Mistral", Protocols: []string{"openai-compatible"}},
	{ID: "baseten", Name: "Baseten", Protocols: []string{"openai-compatible"}},
	{ID: "deepseek", Name: "DeepSeek", Protocols: []string{"openai-compatible"}},
	{ID: "fireworks", Name: "Fireworks", Protocols: []string{"openai-compatible"}},
	{ID: "perplexity", Name: "Perplexity", Protocols: []string{"openai-compatible"}},
	{ID: "cohere", Name: "Cohere", Protocols: []string{"chat"}},
	{ID: "cerebras", Name: "Cerebras", Protocols: []string{"openai-compatible"}},
	{ID: "deepinfra", Name: "DeepInfra", Protocols: []string{"openai-compatible"}},
	{ID: "together", Name: "Together AI", Protocols: []string{"openai-compatible"}},
	{ID: "alibaba", Name: "Alibaba", Protocols: []string{"openai-compatible"}},
	{ID: "vercel", Name: "Vercel AI Gateway", Protocols: []string{"ai-gateway"}},
	{ID: "cloudflare-ai-gateway", Name: "Cloudflare AI Gateway", Protocols: []string{"openai-compatible"}},
	{ID: "cloudflare-workers-ai", Name: "Cloudflare Workers AI", Protocols: []string{"openai-compatible"}},
	{ID: "github-copilot", Name: "GitHub Copilot", Protocols: []string{"openai-compatible"}},
	{ID: "digitalocean", Name: "DigitalOcean", Protocols: []string{"openai-compatible"}},
	{ID: "gitlab-duo", Name: "GitLab Duo", Protocols: []string{"openai-compatible"}},
	{ID: "venice", Name: "Venice", Protocols: []string{"openai-compatible"}},
	{ID: "openai-compatible", Name: "OpenAI Compatible", Protocols: []string{"openai-compatible"}},
}

type openAIProfile struct {
	ProviderID     string
	DefaultBaseURL string
	BaseURLEnvVars []string
	APIKeyEnvVars  []string
	ModelEnvVars   []string
	DefaultModel   string
	AuthHeader     string
	AuthScheme     string
	Headers        map[string]string
	QueryParams    map[string]string
}

func (profile openAIProfile) chatRequest(messages []Message, modelID string) ChatRequest {
	return ChatRequest{
		ProviderID:  profile.ProviderID,
		Protocol:    "openai-compatible",
		BaseURL:     defaultString(firstEnv(profile.BaseURLEnvVars...), profile.DefaultBaseURL),
		APIKey:      firstEnv(profile.APIKeyEnvVars...),
		AuthHeader:  profile.AuthHeader,
		AuthScheme:  profile.AuthScheme,
		Headers:     cloneStringMap(profile.Headers),
		QueryParams: cloneStringMap(profile.QueryParams),
		Model:       defaultString(modelID, defaultString(firstEnv(profile.ModelEnvVars...), profile.DefaultModel)),
		Messages:    messages,
		Temperature: nil,
		MaxTokens:   nil,
	}
}

type anthropicProfile struct {
	ProviderID     string
	DefaultBaseURL string
	BaseURLEnvVars []string
	APIKeyEnvVars  []string
	ModelEnvVars   []string
	DefaultModel   string
	Headers        map[string]string
}

func (profile anthropicProfile) chatRequest(messages []Message, modelID string) ChatRequest {
	return ChatRequest{
		ProviderID: profile.ProviderID,
		Protocol:   "anthropic-messages",
		BaseURL:    defaultString(firstEnv(profile.BaseURLEnvVars...), profile.DefaultBaseURL),
		APIKey:     firstEnv(profile.APIKeyEnvVars...),
		AuthHeader: "x-api-key",
		Headers:    cloneStringMap(profile.Headers),
		Model:      defaultString(modelID, defaultString(firstEnv(profile.ModelEnvVars...), profile.DefaultModel)),
		Messages:   messages,
	}
}

var openAICompatibleProfiles = map[string]openAIProfile{
	"openai-compatible": {
		ProviderID:     "openai-compatible",
		DefaultBaseURL: defaultOpenAICompatibleBaseURL,
		BaseURLEnvVars: []string{"OPENCODE_OPENAI_COMPATIBLE_BASE_URL", "OPENAI_BASE_URL"},
		APIKeyEnvVars:  []string{"OPENCODE_OPENAI_COMPATIBLE_API_KEY", "OPENAI_API_KEY"},
		ModelEnvVars:   []string{"OPENCODE_OPENAI_COMPATIBLE_MODEL", "OPENAI_MODEL"},
		DefaultModel:   "gpt-4o-mini",
		AuthHeader:     "Authorization",
		AuthScheme:     "Bearer",
	},
	"openrouter": {
		ProviderID:     "openrouter",
		DefaultBaseURL: "https://openrouter.ai/api/v1",
		BaseURLEnvVars: []string{"OPENCODE_OPENROUTER_BASE_URL", "OPENROUTER_BASE_URL"},
		APIKeyEnvVars:  []string{"OPENCODE_OPENROUTER_API_KEY", "OPENROUTER_API_KEY"},
		ModelEnvVars:   []string{"OPENCODE_OPENROUTER_MODEL", "OPENROUTER_MODEL"},
		AuthHeader:     "Authorization",
		AuthScheme:     "Bearer",
	},
	"xai": {
		ProviderID:     "xai",
		DefaultBaseURL: "https://api.x.ai/v1",
		BaseURLEnvVars: []string{"OPENCODE_XAI_BASE_URL", "XAI_BASE_URL"},
		APIKeyEnvVars:  []string{"OPENCODE_XAI_API_KEY", "XAI_API_KEY"},
		ModelEnvVars:   []string{"OPENCODE_XAI_MODEL", "XAI_MODEL"},
		AuthHeader:     "Authorization",
		AuthScheme:     "Bearer",
	},
	"groq": {
		ProviderID:     "groq",
		DefaultBaseURL: "https://api.groq.com/openai/v1",
		BaseURLEnvVars: []string{"OPENCODE_GROQ_BASE_URL", "GROQ_BASE_URL"},
		APIKeyEnvVars:  []string{"OPENCODE_GROQ_API_KEY", "GROQ_API_KEY"},
		ModelEnvVars:   []string{"OPENCODE_GROQ_MODEL", "GROQ_MODEL"},
		AuthHeader:     "Authorization",
		AuthScheme:     "Bearer",
	},
	"mistral": {
		ProviderID:     "mistral",
		DefaultBaseURL: "https://api.mistral.ai/v1",
		BaseURLEnvVars: []string{"OPENCODE_MISTRAL_BASE_URL", "MISTRAL_BASE_URL"},
		APIKeyEnvVars:  []string{"OPENCODE_MISTRAL_API_KEY", "MISTRAL_API_KEY"},
		ModelEnvVars:   []string{"OPENCODE_MISTRAL_MODEL", "MISTRAL_MODEL"},
		AuthHeader:     "Authorization",
		AuthScheme:     "Bearer",
	},
	"baseten": {
		ProviderID:     "baseten",
		DefaultBaseURL: "https://inference.baseten.co/v1",
		BaseURLEnvVars: []string{"OPENCODE_BASETEN_BASE_URL", "BASETEN_BASE_URL"},
		APIKeyEnvVars:  []string{"OPENCODE_BASETEN_API_KEY", "BASETEN_API_KEY"},
		ModelEnvVars:   []string{"OPENCODE_BASETEN_MODEL", "BASETEN_MODEL"},
		AuthHeader:     "Authorization",
		AuthScheme:     "Bearer",
	},
	"deepseek": {
		ProviderID:     "deepseek",
		DefaultBaseURL: "https://api.deepseek.com/v1",
		BaseURLEnvVars: []string{"OPENCODE_DEEPSEEK_BASE_URL", "DEEPSEEK_BASE_URL"},
		APIKeyEnvVars:  []string{"OPENCODE_DEEPSEEK_API_KEY", "DEEPSEEK_API_KEY"},
		ModelEnvVars:   []string{"OPENCODE_DEEPSEEK_MODEL", "DEEPSEEK_MODEL"},
		AuthHeader:     "Authorization",
		AuthScheme:     "Bearer",
	},
	"fireworks": {
		ProviderID:     "fireworks",
		DefaultBaseURL: "https://api.fireworks.ai/inference/v1",
		BaseURLEnvVars: []string{"OPENCODE_FIREWORKS_BASE_URL", "FIREWORKS_BASE_URL"},
		APIKeyEnvVars:  []string{"OPENCODE_FIREWORKS_API_KEY", "FIREWORKS_API_KEY"},
		ModelEnvVars:   []string{"OPENCODE_FIREWORKS_MODEL", "FIREWORKS_MODEL"},
		AuthHeader:     "Authorization",
		AuthScheme:     "Bearer",
	},
	"perplexity": {
		ProviderID:     "perplexity",
		DefaultBaseURL: "https://api.perplexity.ai",
		BaseURLEnvVars: []string{"OPENCODE_PERPLEXITY_BASE_URL", "PERPLEXITY_BASE_URL"},
		APIKeyEnvVars:  []string{"OPENCODE_PERPLEXITY_API_KEY", "PERPLEXITY_API_KEY"},
		ModelEnvVars:   []string{"OPENCODE_PERPLEXITY_MODEL", "PERPLEXITY_MODEL"},
		AuthHeader:     "Authorization",
		AuthScheme:     "Bearer",
	},
	"cerebras": {
		ProviderID:     "cerebras",
		DefaultBaseURL: "https://api.cerebras.ai/v1",
		BaseURLEnvVars: []string{"OPENCODE_CEREBRAS_BASE_URL", "CEREBRAS_BASE_URL"},
		APIKeyEnvVars:  []string{"OPENCODE_CEREBRAS_API_KEY", "CEREBRAS_API_KEY"},
		ModelEnvVars:   []string{"OPENCODE_CEREBRAS_MODEL", "CEREBRAS_MODEL"},
		AuthHeader:     "Authorization",
		AuthScheme:     "Bearer",
	},
	"deepinfra": {
		ProviderID:     "deepinfra",
		DefaultBaseURL: "https://api.deepinfra.com/v1/openai",
		BaseURLEnvVars: []string{"OPENCODE_DEEPINFRA_BASE_URL", "DEEPINFRA_BASE_URL"},
		APIKeyEnvVars:  []string{"OPENCODE_DEEPINFRA_API_KEY", "DEEPINFRA_API_KEY"},
		ModelEnvVars:   []string{"OPENCODE_DEEPINFRA_MODEL", "DEEPINFRA_MODEL"},
		AuthHeader:     "Authorization",
		AuthScheme:     "Bearer",
	},
	"together": {
		ProviderID:     "together",
		DefaultBaseURL: "https://api.together.xyz/v1",
		BaseURLEnvVars: []string{"OPENCODE_TOGETHER_BASE_URL", "TOGETHER_BASE_URL", "TOGETHER_AI_BASE_URL"},
		APIKeyEnvVars:  []string{"OPENCODE_TOGETHER_API_KEY", "TOGETHER_API_KEY", "TOGETHER_AI_API_KEY"},
		ModelEnvVars:   []string{"OPENCODE_TOGETHER_MODEL", "TOGETHER_MODEL", "TOGETHER_AI_MODEL"},
		AuthHeader:     "Authorization",
		AuthScheme:     "Bearer",
	},
	"togetherai": {
		ProviderID:     "togetherai",
		DefaultBaseURL: "https://api.together.xyz/v1",
		BaseURLEnvVars: []string{
			"OPENCODE_TOGETHERAI_BASE_URL",
			"OPENCODE_TOGETHER_BASE_URL",
			"TOGETHER_AI_BASE_URL",
			"TOGETHER_BASE_URL",
		},
		APIKeyEnvVars: []string{
			"OPENCODE_TOGETHERAI_API_KEY",
			"OPENCODE_TOGETHER_API_KEY",
			"TOGETHER_AI_API_KEY",
			"TOGETHER_API_KEY",
		},
		ModelEnvVars: []string{
			"OPENCODE_TOGETHERAI_MODEL",
			"OPENCODE_TOGETHER_MODEL",
			"TOGETHER_AI_MODEL",
			"TOGETHER_MODEL",
		},
		AuthHeader: "Authorization",
		AuthScheme: "Bearer",
	},
	"alibaba": {
		ProviderID:     "alibaba",
		DefaultBaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1",
		BaseURLEnvVars: []string{"OPENCODE_ALIBABA_BASE_URL", "ALIBABA_BASE_URL", "DASHSCOPE_BASE_URL"},
		APIKeyEnvVars:  []string{"OPENCODE_ALIBABA_API_KEY", "ALIBABA_API_KEY", "DASHSCOPE_API_KEY"},
		ModelEnvVars:   []string{"OPENCODE_ALIBABA_MODEL", "ALIBABA_MODEL", "DASHSCOPE_MODEL"},
		AuthHeader:     "Authorization",
		AuthScheme:     "Bearer",
	},
	"github-copilot": {
		ProviderID:     "github-copilot",
		DefaultBaseURL: "",
		BaseURLEnvVars: []string{"OPENCODE_GITHUB_COPILOT_BASE_URL", "GITHUB_COPILOT_BASE_URL"},
		APIKeyEnvVars:  []string{"OPENCODE_GITHUB_COPILOT_API_KEY", "GITHUB_COPILOT_API_KEY", "GITHUB_TOKEN"},
		ModelEnvVars:   []string{"OPENCODE_GITHUB_COPILOT_MODEL", "GITHUB_COPILOT_MODEL"},
		AuthHeader:     "Authorization",
		AuthScheme:     "Bearer",
	},
	"digitalocean": {
		ProviderID:     "digitalocean",
		DefaultBaseURL: "https://inference.do-ai.run/v1",
		BaseURLEnvVars: []string{"OPENCODE_DIGITALOCEAN_BASE_URL", "DIGITALOCEAN_BASE_URL"},
		APIKeyEnvVars:  []string{"OPENCODE_DIGITALOCEAN_API_KEY", "DIGITALOCEAN_API_KEY", "DIGITALOCEAN_ACCESS_TOKEN"},
		ModelEnvVars:   []string{"OPENCODE_DIGITALOCEAN_MODEL", "DIGITALOCEAN_MODEL"},
		AuthHeader:     "Authorization",
		AuthScheme:     "Bearer",
	},
	"gitlab-duo": {
		ProviderID:     "gitlab-duo",
		DefaultBaseURL: "",
		BaseURLEnvVars: []string{"OPENCODE_GITLAB_DUO_BASE_URL", "GITLAB_DUO_BASE_URL"},
		APIKeyEnvVars:  []string{"OPENCODE_GITLAB_DUO_API_KEY", "GITLAB_DUO_API_KEY"},
		ModelEnvVars:   []string{"OPENCODE_GITLAB_DUO_MODEL", "GITLAB_DUO_MODEL"},
		AuthHeader:     "Authorization",
		AuthScheme:     "Bearer",
	},
	"venice": {
		ProviderID:     "venice",
		DefaultBaseURL: "https://api.venice.ai/api/v1",
		BaseURLEnvVars: []string{"OPENCODE_VENICE_BASE_URL", "VENICE_BASE_URL"},
		APIKeyEnvVars:  []string{"OPENCODE_VENICE_API_KEY", "VENICE_API_KEY"},
		ModelEnvVars:   []string{"OPENCODE_VENICE_MODEL", "VENICE_MODEL"},
		AuthHeader:     "Authorization",
		AuthScheme:     "Bearer",
	},
}

func azureBaseURL() string {
	if resource := firstEnv("OPENCODE_AZURE_OPENAI_RESOURCE_NAME", "AZURE_OPENAI_RESOURCE_NAME"); resource != "" {
		return "https://" + resource + ".openai.azure.com/openai/v1"
	}
	return ""
}

func bedrockBaseURL() string {
	return "https://bedrock-runtime." + bedrockRegion() + ".amazonaws.com"
}

func bedrockRegion() string {
	return defaultString(firstEnv("OPENCODE_BEDROCK_REGION", "BEDROCK_REGION", "AWS_REGION"), "us-east-1")
}

func bedrockCredentialsFromEnv() *AWSCredentials {
	accessKeyID := firstEnv("OPENCODE_AWS_ACCESS_KEY_ID", "AWS_ACCESS_KEY_ID")
	secretAccessKey := firstEnv("OPENCODE_AWS_SECRET_ACCESS_KEY", "AWS_SECRET_ACCESS_KEY")
	if accessKeyID == "" || secretAccessKey == "" {
		return nil
	}
	return &AWSCredentials{
		Region:          bedrockRegion(),
		AccessKeyID:     accessKeyID,
		SecretAccessKey: secretAccessKey,
		SessionToken:    firstEnv("OPENCODE_AWS_SESSION_TOKEN", "AWS_SESSION_TOKEN"),
	}
}

func bedrockDefaultCredentialChainConfigured() bool {
	return firstEnv(
		"OPENCODE_AWS_WEB_IDENTITY_TOKEN_FILE",
		"AWS_WEB_IDENTITY_TOKEN_FILE",
		"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI",
		"AWS_CONTAINER_CREDENTIALS_FULL_URI",
	) != ""
}

func cloudflareAIGatewayBaseURL() string {
	accountID := firstEnv("OPENCODE_CLOUDFLARE_ACCOUNT_ID", "CLOUDFLARE_ACCOUNT_ID")
	if accountID == "" {
		return ""
	}
	gatewayID := defaultString(firstEnv("OPENCODE_CLOUDFLARE_GATEWAY_ID", "CLOUDFLARE_GATEWAY_ID"), "default")
	return "https://gateway.ai.cloudflare.com/v1/" + urlPathEscape(accountID) + "/" + urlPathEscape(gatewayID) + "/compat"
}

func cloudflareWorkersAIBaseURL() string {
	accountID := firstEnv("OPENCODE_CLOUDFLARE_ACCOUNT_ID", "CLOUDFLARE_ACCOUNT_ID")
	if accountID == "" {
		return ""
	}
	return "https://api.cloudflare.com/client/v4/accounts/" + urlPathEscape(accountID) + "/ai/v1"
}

func cloudflareAIGatewayHeaders() map[string]string {
	token := firstEnv("OPENCODE_CLOUDFLARE_API_TOKEN", "CLOUDFLARE_API_TOKEN", "CF_AIG_TOKEN")
	if token == "" {
		return nil
	}
	return map[string]string{"cf-aig-authorization": "Bearer " + token}
}

func cloneStringMap(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func urlPathEscape(value string) string {
	return url.PathEscape(value)
}
