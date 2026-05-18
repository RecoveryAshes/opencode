// Package llm defines provider contracts for the Go runtime.
package llm

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
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
	catalog := modelsDevProvidersFromEnv()
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
		if override, ok := catalog[provider.ID]; ok {
			public = mergePublicProvider(public, override)
		}
		if override, ok := custom[provider.ID]; ok {
			public = mergePublicProvider(public, override)
		}
		result = append(result, public)
		delete(catalog, provider.ID)
		delete(custom, provider.ID)
	}

	extraIDs := make([]string, 0, len(catalog)+len(custom))
	for id := range catalog {
		if len(enabled) > 0 && !enabled[id] {
			continue
		}
		if disabled[id] {
			continue
		}
		extraIDs = append(extraIDs, id)
	}
	for id := range custom {
		if len(enabled) > 0 && !enabled[id] {
			continue
		}
		if disabled[id] {
			continue
		}
		if _, ok := catalog[id]; ok {
			continue
		}
		extraIDs = append(extraIDs, id)
	}
	sort.Strings(extraIDs)
	for _, id := range extraIDs {
		public, ok := catalog[id]
		if !ok {
			public = custom[id]
		} else if override, ok := custom[id]; ok {
			public = mergePublicProvider(public, override)
		}
		result = append(result, public)
	}
	return result
}

func modelsDevProvidersFromEnv() map[string]PublicProvider {
	path := strings.TrimSpace(os.Getenv("OPENCODE_MODELS_PATH"))
	if path == "" {
		return map[string]PublicProvider{}
	}
	return modelsDevProvidersFromPath(path)
}

func modelsDevProvidersFromPath(path string) map[string]PublicProvider {
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]PublicProvider{}
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return map[string]PublicProvider{}
	}
	result := map[string]PublicProvider{}
	for id, value := range raw {
		record, ok := value.(map[string]any)
		if !ok {
			continue
		}
		provider := providerFromModelsDev(id, record)
		if provider.ID == "" {
			continue
		}
		result[provider.ID] = provider
	}
	return result
}

func providerFromModelsDev(id string, input map[string]any) PublicProvider {
	providerID := stringFromAny(input["id"], id)
	name := stringFromAny(input["name"], providerName(providerID))
	env := stringSliceFromAny(input["env"])
	if len(env) == 0 {
		env = providerEnvVars(providerID)
	}
	models := map[string]PublicModel{}
	if rawModels, ok := input["models"].(map[string]any); ok {
		for key, raw := range rawModels {
			modelRecord, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			model := modelFromConfig(providerID, key, input, modelRecord)
			models[key] = model
			for mode, rawMode := range modelsDevExperimentalModes(modelRecord) {
				modeRecord, ok := rawMode.(map[string]any)
				if !ok {
					continue
				}
				modeModel := modelFromModelsDevMode(model, mode, modeRecord)
				models[modeModel.ID] = modeModel
			}
		}
	}
	return PublicProvider{
		ID:      providerID,
		Name:    name,
		Source:  "custom",
		Env:     env,
		Options: defaultProviderOptions(providerID),
		Models:  models,
	}
}

func modelsDevExperimentalModes(input map[string]any) map[string]any {
	experimental, ok := input["experimental"].(map[string]any)
	if !ok {
		return nil
	}
	modes, ok := experimental["modes"].(map[string]any)
	if !ok {
		return nil
	}
	return modes
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
			models[key] = modelFromConfig(id, key, input, modelRecord)
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
		Options: defaultProviderOptions(provider.ID),
		Models:  models,
	}
}

func defaultProviderOptions(id string) map[string]any {
	headers := map[string]string{}
	if profile, ok := openAICompatibleProfiles[id]; ok {
		for key, value := range profile.Headers {
			headers[key] = value
		}
	}
	if len(headers) == 0 {
		return map[string]any{}
	}
	resultHeaders := map[string]any{}
	for key, value := range headers {
		resultHeaders[key] = value
	}
	return map[string]any{"headers": resultHeaders}
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
			"id":  modelID,
			"url": "",
			"npm": "@ai-sdk/openai-compatible",
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

func modelFromConfig(providerID string, modelID string, providerInput map[string]any, input map[string]any) PublicModel {
	model := defaultPublicModel(providerID, modelID)
	apiID := stringFromAny(input["id"], modelID)
	apiNPM := stringFromAny(providerInput["npm"], stringFromAny(model.API["npm"], "@ai-sdk/openai-compatible"))
	apiURL := stringFromAny(providerInput["api"], stringFromAny(model.API["url"], ""))
	if modelProvider, ok := input["provider"].(map[string]any); ok {
		apiNPM = stringFromAny(modelProvider["npm"], apiNPM)
		apiURL = stringFromAny(modelProvider["api"], apiURL)
	}
	model.API = map[string]any{
		"id":  apiID,
		"npm": apiNPM,
		"url": apiURL,
	}
	if name, ok := input["name"].(string); ok && name != "" {
		model.Name = name
	} else if apiID != modelID {
		model.Name = modelID
	}
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
	configVariants, disabledVariants := variantsFromConfig(input["variants"])
	model.Variants = mergeModelVariants(defaultReasoningVariants(model), configVariants, disabledVariants)
	return model
}

func modelFromModelsDevMode(base PublicModel, mode string, input map[string]any) PublicModel {
	model := base
	model.ID = base.ID + "-" + mode
	model.Name = strings.TrimSpace(base.Name + " " + strings.ToUpper(mode[:1]) + mode[1:])
	model.API = cloneProviderAnyMap(base.API)
	model.Options = cloneProviderAnyMap(base.Options)
	model.Headers = cloneStringMap(base.Headers)
	model.Variants = cloneVariantMap(base.Variants)
	if cost, ok := input["cost"].(map[string]any); ok {
		model.Cost = mergeModelCost(model.Cost, costFromConfig(cost))
	}
	if provider, ok := input["provider"].(map[string]any); ok {
		if body, ok := provider["body"].(map[string]any); ok {
			model.Options = camelCaseProviderBody(body)
		}
		if headers, ok := provider["headers"].(map[string]any); ok {
			model.Headers = stringMapFromAny(headers)
		}
		if api := stringFromAny(provider["api"], ""); api != "" {
			model.API["url"] = api
		}
		if npm := stringFromAny(provider["npm"], ""); npm != "" {
			model.API["npm"] = npm
		}
	}
	model.Variants = mergeModelVariants(defaultReasoningVariants(model), nil, nil)
	return model
}

func costFromConfig(cost map[string]any) Cost {
	return Cost{
		Input:  floatFromAny(cost["input"], 0),
		Output: floatFromAny(cost["output"], 0),
		Cache: CacheCost{
			Read:  floatFromAny(cost["cache_read"], 0),
			Write: floatFromAny(cost["cache_write"], 0),
		},
		Tiers:                costTiersFromConfig(cost["tiers"]),
		ExperimentalOver200K: over200KCostFromConfig(cost["context_over_200k"]),
	}
}

func mergeModelCost(base Cost, override Cost) Cost {
	base.Input = override.Input
	base.Output = override.Output
	base.Cache = override.Cache
	if override.Tiers != nil {
		base.Tiers = override.Tiers
	}
	if override.ExperimentalOver200K != nil {
		base.ExperimentalOver200K = override.ExperimentalOver200K
	}
	return base
}

func camelCaseProviderBody(input map[string]any) map[string]any {
	result := map[string]any{}
	for key, value := range input {
		result[snakeToLowerCamel(key)] = value
	}
	return result
}

func snakeToLowerCamel(input string) string {
	var output strings.Builder
	upperNext := false
	for _, r := range input {
		if r == '_' {
			upperNext = true
			continue
		}
		if upperNext {
			output.WriteString(strings.ToUpper(string(r)))
			upperNext = false
			continue
		}
		output.WriteRune(r)
	}
	return output.String()
}

func cloneVariantMap(input map[string]map[string]any) map[string]map[string]any {
	if len(input) == 0 {
		return nil
	}
	result := make(map[string]map[string]any, len(input))
	for key, value := range input {
		result[key] = cloneProviderAnyMap(value)
	}
	return result
}

func defaultReasoningVariants(model PublicModel) map[string]map[string]any {
	if !model.Capabilities.Reasoning {
		return nil
	}
	id := strings.ToLower(model.ID)
	apiID := strings.ToLower(stringFromAny(model.API["id"], model.ID))
	for _, blocked := range []string{"deepseek-chat", "deepseek-reasoner", "deepseek-r1", "deepseek-v3", "minimax", "glm", "kimi", "k2p", "qwen", "big-pickle", "grok"} {
		if strings.Contains(id, blocked) {
			return nil
		}
	}
	apiNPM, _ := model.API["npm"].(string)
	switch apiNPM {
	case "@openrouter/ai-sdk-provider":
		if !strings.Contains(id, "gpt") && !strings.Contains(id, "gemini-3") && !strings.Contains(id, "claude") {
			return nil
		}
		efforts := openAIReasoningEfforts(id, model.ReleaseDate)
		if !strings.Contains(id, "gpt") {
			efforts = openAIEfforts
		}
		return reasoningVariants(efforts, func(effort string) map[string]any {
			return map[string]any{"reasoning": map[string]any{"effort": effort}}
		})
	case "ai-gateway-provider":
		efforts := widelySupportedEfforts
		if strings.HasPrefix(apiID, "openai/") {
			efforts = openAIReasoningEfforts(apiID, model.ReleaseDate)
		}
		return reasoningEffortVariants(efforts, false)
	case "@ai-sdk/gateway":
		switch {
		case strings.Contains(id, "anthropic"):
			if efforts := anthropicAdaptiveEfforts(apiID); len(efforts) > 0 {
				return reasoningVariants(efforts, func(effort string) map[string]any {
					return map[string]any{"thinking": map[string]any{"type": "adaptive"}, "effort": effort}
				})
			}
			return map[string]map[string]any{
				"high": {"thinking": map[string]any{"type": "enabled", "budgetTokens": 16000}},
				"max":  {"thinking": map[string]any{"type": "enabled", "budgetTokens": 31999}},
			}
		case strings.Contains(id, "google"):
			if strings.Contains(id, "2.5") {
				return googleBudgetVariants(24576)
			}
			return googleThinkingLevelVariants([]string{"low", "high"})
		default:
			return reasoningEffortVariants(openAICompatibleReasoningEfforts(apiID), false)
		}
	case "@ai-sdk/github-copilot":
		if strings.Contains(id, "gemini") {
			return nil
		}
		if strings.Contains(id, "claude") {
			return reasoningEffortVariants(widelySupportedEfforts, false)
		}
		efforts := append([]string{}, widelySupportedEfforts...)
		if strings.Contains(id, "5.1-codex-max") || strings.Contains(id, "5.2") || strings.Contains(id, "5.3") ||
			(strings.Contains(id, "gpt-5") && model.ReleaseDate >= openAIXHighEffortReleaseDate) {
			efforts = append(efforts, "xhigh")
		}
		return reasoningEffortVariants(efforts, true)
	case "@ai-sdk/openai":
		return reasoningEffortVariants(openAIReasoningEfforts(apiID, model.ReleaseDate), true)
	case "@ai-sdk/azure":
		if id == "o1-mini" {
			return nil
		}
		efforts := widelySupportedEfforts
		if gpt5FamilyRE.MatchString(id) && gpt5Version(id) == 0 {
			efforts = append([]string{"minimal"}, widelySupportedEfforts...)
		}
		return reasoningEffortVariants(efforts, true)
	case "@ai-sdk/openai-compatible", "@ai-sdk/xai", "@ai-sdk/deepinfra", "@ai-sdk/togetherai", "@ai-sdk/cerebras", "venice-ai-sdk-provider":
		efforts := append([]string{}, widelySupportedEfforts...)
		if strings.Contains(apiID, "deepseek-v4") {
			efforts = append(efforts, "max")
		}
		return reasoningEffortVariants(efforts, false)
	case "@ai-sdk/anthropic", "@ai-sdk/google-vertex/anthropic":
		if efforts := anthropicAdaptiveEfforts(apiID); len(efforts) > 0 {
			return reasoningVariants(efforts, func(effort string) map[string]any {
				thinking := map[string]any{"type": "adaptive"}
				if strings.Contains(apiID, "opus-4-7") || strings.Contains(apiID, "opus-4.7") {
					thinking["display"] = "summarized"
				}
				return map[string]any{"thinking": thinking, "effort": effort}
			})
		}
		if strings.Contains(apiID, "opus-4-5") || strings.Contains(apiID, "opus-4.5") {
			return reasoningVariants(widelySupportedEfforts, func(effort string) map[string]any {
				return map[string]any{"effort": effort}
			})
		}
		return map[string]map[string]any{
			"high": {"thinking": map[string]any{"type": "enabled", "budgetTokens": minInt(16000, model.Limit.Output/2-1)}},
			"max":  {"thinking": map[string]any{"type": "enabled", "budgetTokens": minInt(31999, model.Limit.Output-1)}},
		}
	case "@ai-sdk/amazon-bedrock":
		if efforts := anthropicAdaptiveEfforts(apiID); len(efforts) > 0 {
			return reasoningVariants(efforts, func(effort string) map[string]any {
				config := map[string]any{"type": "adaptive", "maxReasoningEffort": effort}
				if strings.Contains(apiID, "opus-4-7") || strings.Contains(apiID, "opus-4.7") {
					config["display"] = "summarized"
				}
				return map[string]any{"reasoningConfig": config}
			})
		}
		if strings.Contains(apiID, "anthropic") {
			return map[string]map[string]any{
				"high": {"reasoningConfig": map[string]any{"type": "enabled", "budgetTokens": 16000}},
				"max":  {"reasoningConfig": map[string]any{"type": "enabled", "budgetTokens": 31999}},
			}
		}
		return reasoningVariants(widelySupportedEfforts, func(effort string) map[string]any {
			return map[string]any{"reasoningConfig": map[string]any{"type": "enabled", "maxReasoningEffort": effort}}
		})
	case "@ai-sdk/google", "@ai-sdk/google-vertex":
		if strings.Contains(id, "2.5") {
			return googleBudgetVariants(googleThinkingBudgetMax(id))
		}
		return googleThinkingLevelVariants(googleThinkingLevelEfforts(id))
	case "@ai-sdk/mistral":
		for _, candidate := range []string{"mistral-small-2603", "mistral-small-latest", "mistral-medium-3.5", "mistral-medium-2604"} {
			if strings.Contains(apiID, candidate) {
				return map[string]map[string]any{"high": {"reasoningEffort": "high"}}
			}
		}
		return nil
	case "@ai-sdk/groq":
		return reasoningEffortVariants(append([]string{"none"}, widelySupportedEfforts...), false)
	default:
		return nil
	}
}

var (
	widelySupportedEfforts       = []string{"low", "medium", "high"}
	openAIEfforts                = []string{"none", "minimal", "low", "medium", "high", "xhigh"}
	openAINoneEffortReleaseDate  = "2025-11-13"
	openAIXHighEffortReleaseDate = "2025-12-04"
	gpt5FamilyRE                 = regexp.MustCompile(`(?:^|/)gpt-5(?:[.-]|$)`)
	gpt5VersionRE                = regexp.MustCompile(`(?:^|/)gpt-5[.-](\d+)(?:[.-]|$)`)
	gpt5ProRE                    = regexp.MustCompile(`(?:^|/)gpt-5[.-]?pro(?:[.-]|$)`)
	gpt5VersionedProRE           = regexp.MustCompile(`(?:^|/)gpt-5[.-]\d+[.-]pro(?:[.-]|$)`)
)

func reasoningEffortVariants(efforts []string, includeReasoning bool) map[string]map[string]any {
	return reasoningVariants(efforts, func(effort string) map[string]any {
		variant := map[string]any{"reasoningEffort": effort}
		if includeReasoning {
			variant["reasoningSummary"] = "auto"
			variant["include"] = []any{"reasoning.encrypted_content"}
		}
		return variant
	})
}

func reasoningVariants(efforts []string, build func(string) map[string]any) map[string]map[string]any {
	if len(efforts) == 0 {
		return nil
	}
	result := map[string]map[string]any{}
	for _, effort := range efforts {
		result[effort] = build(effort)
	}
	return result
}

func openAIReasoningEfforts(apiID string, releaseDate string) []string {
	id := strings.ToLower(apiID)
	if strings.Contains(id, "deep-research") {
		return []string{"medium"}
	}
	if efforts := gpt5ChatReasoningEfforts(id); efforts != nil {
		return efforts
	}
	if gpt5ProRE.MatchString(id) {
		return []string{"high"}
	}
	if efforts := gpt5CodexReasoningEfforts(id); efforts != nil {
		return efforts
	}
	if efforts := versionedGpt5ReasoningEfforts(id); efforts != nil {
		return efforts
	}
	efforts := append([]string{}, widelySupportedEfforts...)
	if gpt5FamilyRE.MatchString(id) {
		efforts = append([]string{"minimal"}, efforts...)
	}
	if releaseDate >= openAINoneEffortReleaseDate {
		efforts = append([]string{"none"}, efforts...)
	}
	if releaseDate >= openAIXHighEffortReleaseDate {
		efforts = append(efforts, "xhigh")
	}
	return efforts
}

func openAICompatibleReasoningEfforts(apiID string) []string {
	id := strings.ToLower(apiID)
	if efforts := gpt5ChatReasoningEfforts(id); efforts != nil {
		return efforts
	}
	if gpt5ProRE.MatchString(id) {
		return []string{"high"}
	}
	if efforts := gpt5CodexReasoningEfforts(id); efforts != nil {
		return efforts
	}
	if efforts := versionedGpt5ReasoningEfforts(id); efforts != nil {
		return efforts
	}
	return openAIEfforts
}

func versionedGpt5ReasoningEfforts(apiID string) []string {
	if gpt5VersionedProRE.MatchString(apiID) {
		return []string{"medium", "high", "xhigh"}
	}
	version := gpt5Version(apiID)
	switch {
	case version == 1:
		return []string{"none", "low", "medium", "high"}
	case version >= 2:
		return []string{"none", "low", "medium", "high", "xhigh"}
	default:
		return nil
	}
}

func gpt5CodexReasoningEfforts(apiID string) []string {
	if !gpt5FamilyRE.MatchString(apiID) || !strings.Contains(apiID, "codex") {
		return nil
	}
	version := gpt5Version(apiID)
	switch {
	case version >= 3:
		return []string{"none", "low", "medium", "high", "xhigh"}
	case strings.Contains(apiID, "codex-max") || version >= 2:
		return []string{"low", "medium", "high", "xhigh"}
	default:
		return widelySupportedEfforts
	}
}

func gpt5ChatReasoningEfforts(apiID string) []string {
	if !gpt5FamilyRE.MatchString(apiID) || !strings.Contains(apiID, "-chat") {
		return nil
	}
	if gpt5Version(apiID) == 0 {
		return []string{}
	}
	return []string{"medium"}
}

func gpt5Version(apiID string) int {
	match := gpt5VersionRE.FindStringSubmatch(apiID)
	if len(match) < 2 {
		return 0
	}
	version, err := strconv.Atoi(match[1])
	if err != nil {
		return 0
	}
	return version
}

func anthropicAdaptiveEfforts(apiID string) []string {
	switch {
	case strings.Contains(apiID, "opus-4-7") || strings.Contains(apiID, "opus-4.7"):
		return []string{"low", "medium", "high", "xhigh", "max"}
	case strings.Contains(apiID, "opus-4-6") || strings.Contains(apiID, "opus-4.6") ||
		strings.Contains(apiID, "sonnet-4-6") || strings.Contains(apiID, "sonnet-4.6"):
		return []string{"low", "medium", "high", "max"}
	default:
		return nil
	}
}

func googleThinkingLevelEfforts(apiID string) []string {
	id := strings.ToLower(apiID)
	if !strings.Contains(id, "gemini-3") {
		return []string{"low", "high"}
	}
	if strings.Contains(id, "flash-image") {
		return []string{"minimal", "high"}
	}
	if strings.Contains(id, "pro-image") {
		return []string{"high"}
	}
	if strings.Contains(id, "flash") {
		return []string{"minimal", "low", "medium", "high"}
	}
	return []string{"low", "medium", "high"}
}

func googleThinkingBudgetMax(apiID string) int {
	id := strings.ToLower(apiID)
	if strings.Contains(id, "2.5") && strings.Contains(id, "pro") && !strings.Contains(id, "flash") {
		return 32768
	}
	return 24576
}

func googleThinkingLevelVariants(efforts []string) map[string]map[string]any {
	return reasoningVariants(efforts, func(effort string) map[string]any {
		return map[string]any{"thinkingConfig": map[string]any{"includeThoughts": true, "thinkingLevel": effort}}
	})
}

func googleBudgetVariants(maxBudget int) map[string]map[string]any {
	return map[string]map[string]any{
		"high": {"thinkingConfig": map[string]any{"includeThoughts": true, "thinkingBudget": 16000}},
		"max":  {"thinkingConfig": map[string]any{"includeThoughts": true, "thinkingBudget": maxBudget}},
	}
}

func minInt(left int, right int) int {
	if left < right {
		return left
	}
	return right
}

func mergeModelVariants(base map[string]map[string]any, override map[string]map[string]any, disabled map[string]bool) map[string]map[string]any {
	if len(base) == 0 && len(override) == 0 {
		return nil
	}
	result := map[string]map[string]any{}
	for key, value := range base {
		if disabled[key] {
			continue
		}
		result[key] = cloneProviderAnyMap(value)
	}
	for key, value := range override {
		if disabled[key] {
			continue
		}
		result[key] = cloneProviderAnyMap(value)
	}
	return result
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

func variantsFromConfig(input any) (map[string]map[string]any, map[string]bool) {
	raw, ok := input.(map[string]any)
	if !ok {
		return nil, nil
	}
	result := map[string]map[string]any{}
	disabled := map[string]bool{}
	for name, item := range raw {
		record, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if boolFromAny(record["disabled"], false) {
			disabled[name] = true
			continue
		}
		cleaned := cloneProviderAnyMap(record)
		delete(cleaned, "disabled")
		result[name] = cleaned
	}
	return result, disabled
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
		"openai":        {"OPENAI_API_KEY"},
		"anthropic":     {"ANTHROPIC_API_KEY"},
		"google":        {"GOOGLE_GENERATIVE_AI_API_KEY", "GEMINI_API_KEY"},
		"google-vertex": {"GOOGLE_VERTEX_PROJECT", "GOOGLE_APPLICATION_CREDENTIALS"},
		"google-vertex-anthropic": {
			"GOOGLE_VERTEX_PROJECT",
			"GOOGLE_VERTEX_LOCATION",
			"GOOGLE_APPLICATION_CREDENTIALS",
		},
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
		"llmgateway":            {"LLMGATEWAY_API_KEY"},
		"nvidia":                {"NVIDIA_API_KEY"},
		"kilo":                  {"KILO_API_KEY"},
		"zenmux":                {"ZENMUX_API_KEY"},
		"opencode":              {"OPENCODE_API_KEY"},
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
		if nested, ok := value.(map[string]any); ok {
			result[key] = cloneProviderAnyMap(nested)
			continue
		}
		result[key] = value
	}
	return result
}

func mergeProviderAnyMap(left map[string]any, right map[string]any) map[string]any {
	result := cloneProviderAnyMap(left)
	for key, value := range right {
		if rightMap, ok := value.(map[string]any); ok {
			if leftMap, ok := result[key].(map[string]any); ok {
				result[key] = mergeProviderAnyMap(leftMap, rightMap)
				continue
			}
			result[key] = cloneProviderAnyMap(rightMap)
			continue
		}
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
		project := googleVertexProject()
		if project == "" {
			return ChatRequest{}, fmt.Errorf("google-vertex provider requires GOOGLE_VERTEX_PROJECT or GOOGLE_CLOUD_PROJECT")
		}
		location := googleVertexLocation()
		profile := geminiProfile{
			ProviderID:     "google-vertex",
			DefaultBaseURL: googleVertexGeminiBaseURL(project, location),
			BaseURLEnvVars: []string{"OPENCODE_GOOGLE_VERTEX_BASE_URL", "GOOGLE_VERTEX_BASE_URL"},
			ModelEnvVars:   []string{"OPENCODE_GOOGLE_VERTEX_MODEL", "GOOGLE_VERTEX_MODEL"},
			DefaultModel:   "gemini-2.5-flash",
			AuthHeader:     "Authorization",
			AuthScheme:     "Bearer",
			TokenSource:    googleTokenSource{},
		}
		return profile.chatRequest(messages, modelID), nil
	case "google-vertex-anthropic":
		project := googleVertexProject()
		if project == "" {
			return ChatRequest{}, fmt.Errorf("google-vertex-anthropic provider requires GOOGLE_VERTEX_PROJECT or GOOGLE_CLOUD_PROJECT")
		}
		location := googleVertexAnthropicLocation()
		profile := anthropicProfile{
			ProviderID:     "google-vertex-anthropic",
			DefaultBaseURL: googleVertexAnthropicBaseURL(project, location),
			BaseURLEnvVars: []string{"OPENCODE_GOOGLE_VERTEX_ANTHROPIC_BASE_URL", "GOOGLE_VERTEX_ANTHROPIC_BASE_URL"},
			ModelEnvVars:   []string{"OPENCODE_GOOGLE_VERTEX_ANTHROPIC_MODEL", "GOOGLE_VERTEX_ANTHROPIC_MODEL"},
			DefaultModel:   "claude-sonnet-4-6@default",
			Headers:        map[string]string{"anthropic-version": "2023-06-01"},
			AuthHeader:     "Authorization",
			AuthScheme:     "Bearer",
			TokenSource:    googleTokenSource{},
		}
		return profile.chatRequest(messages, modelID), nil
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
	AuthHeader     string
	AuthScheme     string
	TokenSource    TokenSource
}

func (profile geminiProfile) chatRequest(messages []Message, modelID string) ChatRequest {
	authHeader := defaultString(profile.AuthHeader, "x-goog-api-key")
	return ChatRequest{
		ProviderID:  profile.ProviderID,
		Protocol:    "gemini",
		BaseURL:     defaultString(firstEnv(profile.BaseURLEnvVars...), profile.DefaultBaseURL),
		APIKey:      firstEnv(profile.APIKeyEnvVars...),
		AuthHeader:  authHeader,
		AuthScheme:  profile.AuthScheme,
		Model:       defaultString(modelID, defaultString(firstEnv(profile.ModelEnvVars...), profile.DefaultModel)),
		Messages:    messages,
		TokenSource: profile.TokenSource,
	}
}

var providers = []Provider{
	{ID: "openai", Name: "OpenAI", Protocols: []string{"responses", "chat-completions"}},
	{ID: "anthropic", Name: "Anthropic", Protocols: []string{"messages"}},
	{ID: "google", Name: "Gemini", Protocols: []string{"generate-content"}},
	{ID: "google-vertex", Name: "Vertex AI", Protocols: []string{"generate-content"}},
	{ID: "google-vertex-anthropic", Name: "Vertex (Anthropic)", Protocols: []string{"messages"}},
	{ID: "azure", Name: "Azure OpenAI", Protocols: []string{"responses", "chat-completions"}},
	{ID: "amazon-bedrock", Name: "Amazon Bedrock", Protocols: []string{"converse", "invoke-model"}},
	{ID: "opencode", Name: "OpenCode Zen", Protocols: []string{"openai-compatible"}},
	{ID: "openrouter", Name: "OpenRouter", Protocols: []string{"openai-compatible"}},
	{ID: "llmgateway", Name: "LLM Gateway", Protocols: []string{"openai-compatible"}},
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
	{ID: "nvidia", Name: "Nvidia", Protocols: []string{"openai-compatible"}},
	{ID: "kilo", Name: "Kilo Gateway", Protocols: []string{"openai-compatible"}},
	{ID: "zenmux", Name: "ZenMux", Protocols: []string{"openai-compatible"}},
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
	DefaultAPIKey  string
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
		APIKey:      defaultString(firstEnv(profile.APIKeyEnvVars...), profile.DefaultAPIKey),
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
	AuthHeader     string
	AuthScheme     string
	TokenSource    TokenSource
}

func (profile anthropicProfile) chatRequest(messages []Message, modelID string) ChatRequest {
	authHeader := defaultString(profile.AuthHeader, "x-api-key")
	return ChatRequest{
		ProviderID:  profile.ProviderID,
		Protocol:    "anthropic-messages",
		BaseURL:     defaultString(firstEnv(profile.BaseURLEnvVars...), profile.DefaultBaseURL),
		APIKey:      firstEnv(profile.APIKeyEnvVars...),
		AuthHeader:  authHeader,
		AuthScheme:  profile.AuthScheme,
		Headers:     cloneStringMap(profile.Headers),
		Model:       defaultString(modelID, defaultString(firstEnv(profile.ModelEnvVars...), profile.DefaultModel)),
		Messages:    messages,
		TokenSource: profile.TokenSource,
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
	"opencode": {
		ProviderID:     "opencode",
		DefaultBaseURL: "https://opencode.ai/zen/v1",
		BaseURLEnvVars: []string{"OPENCODE_ZEN_BASE_URL"},
		APIKeyEnvVars:  []string{"OPENCODE_API_KEY"},
		DefaultAPIKey:  "public",
		ModelEnvVars:   []string{"OPENCODE_MODEL"},
		DefaultModel:   "big-pickle",
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
		Headers: map[string]string{
			"HTTP-Referer": "https://opencode.ai/",
			"X-Title":      "opencode",
		},
	},
	"llmgateway": {
		ProviderID:     "llmgateway",
		DefaultBaseURL: "https://api.llmgateway.io/v1",
		BaseURLEnvVars: []string{"OPENCODE_LLMGATEWAY_BASE_URL", "LLMGATEWAY_BASE_URL"},
		APIKeyEnvVars:  []string{"OPENCODE_LLMGATEWAY_API_KEY", "LLMGATEWAY_API_KEY"},
		ModelEnvVars:   []string{"OPENCODE_LLMGATEWAY_MODEL", "LLMGATEWAY_MODEL"},
		DefaultModel:   "gpt-4o-mini-search-preview",
		AuthHeader:     "Authorization",
		AuthScheme:     "Bearer",
		Headers: map[string]string{
			"HTTP-Referer": "https://opencode.ai/",
			"X-Title":      "opencode",
			"X-Source":     "opencode",
		},
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
		Headers: map[string]string{
			"X-Cerebras-3rd-Party-Integration": "opencode",
		},
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
	"nvidia": {
		ProviderID:     "nvidia",
		DefaultBaseURL: "https://integrate.api.nvidia.com/v1",
		BaseURLEnvVars: []string{"OPENCODE_NVIDIA_BASE_URL", "NVIDIA_BASE_URL"},
		APIKeyEnvVars:  []string{"OPENCODE_NVIDIA_API_KEY", "NVIDIA_API_KEY"},
		ModelEnvVars:   []string{"OPENCODE_NVIDIA_MODEL", "NVIDIA_MODEL"},
		DefaultModel:   "upstage/solar-10_7b-instruct",
		AuthHeader:     "Authorization",
		AuthScheme:     "Bearer",
		Headers: map[string]string{
			"HTTP-Referer":            "https://opencode.ai/",
			"X-Title":                 "opencode",
			"X-BILLING-INVOKE-ORIGIN": "OpenCode",
		},
	},
	"kilo": {
		ProviderID:     "kilo",
		DefaultBaseURL: "https://api.kilo.ai/api/gateway",
		BaseURLEnvVars: []string{"OPENCODE_KILO_BASE_URL", "KILO_BASE_URL"},
		APIKeyEnvVars:  []string{"OPENCODE_KILO_API_KEY", "KILO_API_KEY"},
		ModelEnvVars:   []string{"OPENCODE_KILO_MODEL", "KILO_MODEL"},
		DefaultModel:   "rekaai/reka-edge",
		AuthHeader:     "Authorization",
		AuthScheme:     "Bearer",
		Headers: map[string]string{
			"HTTP-Referer": "https://opencode.ai/",
			"X-Title":      "opencode",
		},
	},
	"zenmux": {
		ProviderID:     "zenmux",
		DefaultBaseURL: "https://zenmux.ai/api/v1",
		BaseURLEnvVars: []string{"OPENCODE_ZENMUX_BASE_URL", "ZENMUX_BASE_URL"},
		APIKeyEnvVars:  []string{"OPENCODE_ZENMUX_API_KEY", "ZENMUX_API_KEY"},
		ModelEnvVars:   []string{"OPENCODE_ZENMUX_MODEL", "ZENMUX_MODEL"},
		DefaultModel:   "deepseek/deepseek-chat",
		AuthHeader:     "Authorization",
		AuthScheme:     "Bearer",
		Headers: map[string]string{
			"HTTP-Referer": "https://opencode.ai/",
			"X-Title":      "opencode",
		},
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
