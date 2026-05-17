// Package runtime owns local session execution that happens after a user
// prompt is stored.
package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/RecoveryAshes/opencode/internal/config"
	"github.com/RecoveryAshes/opencode/internal/domain/session"
	"github.com/RecoveryAshes/opencode/internal/integration"
	"github.com/RecoveryAshes/opencode/internal/llm"
)

// ChatClient is the LLM boundary required by prompt execution.
type ChatClient interface {
	Chat(context.Context, llm.ChatRequest) (llm.ChatResponse, error)
}

// PromptRuntime executes the first Go-native text prompt loop.
type PromptRuntime struct {
	Messages session.MessageRepository
	Client   ChatClient
	CWD      string
	Root     string
	Config   config.Info
	// MaxToolIterations caps provider/tool feedback loops for one assistant turn.
	MaxToolIterations int
}

// NewPromptRuntime creates a prompt runtime backed by migrated provider clients.
func NewPromptRuntime(messages session.MessageRepository) *PromptRuntime {
	return &PromptRuntime{
		Messages: messages,
		Client:   llm.NewProviderChatClient(),
	}
}

// Reply sends the current session transcript to the configured provider and
// persists the assistant response.
func (runtime *PromptRuntime) Reply(ctx context.Context, sessionID session.ID, userMessage session.WithParts) (session.WithParts, error) {
	if runtime.Messages == nil {
		return session.WithParts{}, fmt.Errorf("runtime message repository is required")
	}
	client := runtime.Client
	if client == nil {
		client = llm.NewProviderChatClient()
	}

	transcript, err := runtime.Messages.Messages(ctx, sessionID, 0)
	if err != nil {
		return session.WithParts{}, err
	}
	messages := lowerTranscript(transcript)
	if len(messages) == 0 {
		messages = lowerTranscript([]session.WithParts{userMessage})
	}

	providerConfig, err := runtime.providerConfig()
	if err != nil {
		return session.WithParts{}, err
	}
	model, configuredDefault := modelRef(userMessage, providerConfig, userMessage.Info.Agent, transcript)
	response, tools, usage, effectiveModel, err := runtime.runProviderLoop(ctx, sessionID, client, messages, model, userMessage.Info.Agent, localToolDefinitions(userMessage.Info.Tools), providerConfig, configuredDefault)
	if err != nil {
		return session.WithParts{}, err
	}

	return runtime.Messages.CreateAssistant(ctx, sessionID, session.AssistantInput{
		ParentID: userMessage.Info.ID,
		Agent:    defaultString(userMessage.Info.Agent, "build"),
		Model:    effectiveModel,
		Path: session.PathInfo{
			CWD:  defaultString(runtime.CWD, mustGetwd()),
			Root: defaultString(runtime.Root, defaultString(runtime.CWD, mustGetwd())),
		},
		Text:   response.Text,
		Tools:  tools,
		Finish: finishReason(response.FinishReason, tools),
		Tokens: session.TokenUsage{
			Total:     optionalPositive(usage.TotalTokens),
			Input:     usage.InputTokens,
			Output:    usage.OutputTokens,
			Reasoning: usage.ReasoningTokens,
			Cache: session.CacheUsage{
				Read:  usage.CacheReadTokens,
				Write: usage.CacheWriteTokens,
			},
		},
		Cost: 0,
	})
}

func (runtime *PromptRuntime) runProviderLoop(ctx context.Context, sessionID session.ID, client ChatClient, messages []llm.Message, model session.ModelRef, agentName string, definitions []llm.ToolDefinition, providerConfig config.Info, configuredDefault bool) (llm.ChatResponse, []session.ToolExecution, llm.Usage, session.ModelRef, error) {
	maxIterations := runtime.MaxToolIterations
	if maxIterations <= 0 {
		maxIterations = 4
	}
	tools := []session.ToolExecution{}
	usage := llm.Usage{}
	var response llm.ChatResponse
	for iteration := 0; ; iteration++ {
		request, err := llm.ResolveChatRequest(messages, model.ProviderID, model.ModelID)
		if err != nil {
			if !configuredDefault {
				return llm.ChatResponse{}, nil, llm.Usage{}, session.ModelRef{}, err
			}
			model = session.ModelRef{ProviderID: "openai-compatible", ModelID: "gpt-4o-mini"}
			request, err = llm.ResolveChatRequest(messages, model.ProviderID, model.ModelID)
			if err != nil {
				return llm.ChatResponse{}, nil, llm.Usage{}, session.ModelRef{}, err
			}
		}
		applyConfiguredProviderOptions(&request, providerConfig, model, agentName, sessionID)
		request.Tools = definitions
		next, err := client.Chat(ctx, request)
		if err != nil {
			return llm.ChatResponse{}, nil, llm.Usage{}, session.ModelRef{}, err
		}
		response = next
		usage = mergeUsage(usage, next.Usage)
		if len(next.ToolCalls) == 0 {
			return response, tools, usage, model, nil
		}
		executed := runtime.executeToolCalls(ctx, sessionID, next.ToolCalls)
		tools = append(tools, executed...)
		if iteration+1 >= maxIterations {
			return response, tools, usage, model, nil
		}
		messages = append(messages, toolResultMessage(next, executed))
	}
}

func (runtime *PromptRuntime) providerConfig() (config.Info, error) {
	if runtime.Config != nil {
		return runtime.Config, nil
	}
	directory := defaultString(runtime.CWD, ".")
	if info, err := os.Stat(directory); err != nil || !info.IsDir() {
		return config.Info{}, nil
	}
	loaded, err := config.Load(config.LoadOptions{Directory: directory, Worktree: defaultString(runtime.Root, directory)})
	if err != nil {
		return nil, err
	}
	return loaded.Info, nil
}

func applyConfiguredProviderOptions(request *llm.ChatRequest, info config.Info, model session.ModelRef, agentName string, sessionID session.ID) {
	providers, ok := info["provider"].(map[string]any)
	var rawProvider map[string]any
	if ok {
		rawProvider, _ = providers[model.ProviderID].(map[string]any)
	}
	if rawOptions, ok := rawProvider["options"].(map[string]any); ok {
		applyChatOptions(request, rawOptions)
	}
	rawModels, _ := rawProvider["models"].(map[string]any)
	rawModel, _ := rawModels[model.ModelID].(map[string]any)
	if apiID, ok := rawModel["id"].(string); ok && apiID != "" {
		request.Model = apiID
	}
	apiURL := stringFromConfig(rawProvider["api"])
	if modelProvider, ok := rawModel["provider"].(map[string]any); ok {
		apiURL = defaultString(stringFromConfig(modelProvider["api"]), apiURL)
	}
	if apiURL != "" && request.BaseURL == "" {
		request.BaseURL = apiURL
	}
	request.Options = mergeAnyOptions(request.Options, defaultProviderBodyOptions(*request, rawProvider, rawModel, sessionID))
	applyDefaultSampling(request)
	if rawModelOptions, ok := rawModel["options"].(map[string]any); ok {
		applyChatOptions(request, rawModelOptions)
		request.Options = mergeAnyOptions(request.Options, bodyOptionsFromConfig(rawModelOptions))
	}
	if rawAgentOptions := configuredAgentOptions(info, agentName); len(rawAgentOptions) > 0 {
		applyChatOptions(request, rawAgentOptions)
		request.Options = mergeAnyOptions(request.Options, bodyOptionsFromConfig(rawAgentOptions))
	}
	applyConfiguredAgentSampling(request, info, agentName)
	if model.Variant != "" {
		if rawVariants, ok := rawModel["variants"].(map[string]any); ok {
			if rawVariant, ok := rawVariants[model.Variant].(map[string]any); ok {
				applyChatOptions(request, rawVariant)
				request.Options = mergeAnyOptions(request.Options, bodyOptionsFromConfig(rawVariant))
			}
		}
	}
	if rawHeaders, ok := rawModel["headers"].(map[string]any); ok {
		request.Headers = mergeHeaders(request.Headers, stringMapFromConfig(rawHeaders))
	}
}

func applyDefaultSampling(request *llm.ChatRequest) {
	modelID := strings.ToLower(request.Model)
	if request.Temperature == nil {
		if value, ok := defaultTemperature(modelID); ok {
			request.Temperature = &value
		}
	}
	if request.TopP == nil {
		if value, ok := defaultTopP(modelID); ok {
			request.TopP = &value
		}
	}
	if request.TopK == nil {
		if value, ok := defaultTopK(modelID); ok {
			request.TopK = &value
		}
	}
}

func defaultTemperature(modelID string) (float64, bool) {
	switch {
	case strings.Contains(modelID, "qwen"):
		return 0.55, true
	case strings.Contains(modelID, "claude"):
		return 0, false
	case strings.Contains(modelID, "gemini"),
		strings.Contains(modelID, "glm-4.6"),
		strings.Contains(modelID, "glm-4.7"),
		strings.Contains(modelID, "minimax-m2"):
		return 1.0, true
	case strings.Contains(modelID, "kimi-k2"):
		if strings.Contains(modelID, "thinking") ||
			strings.Contains(modelID, "k2.") ||
			strings.Contains(modelID, "k2p") ||
			strings.Contains(modelID, "k2-5") {
			return 1.0, true
		}
		return 0.6, true
	default:
		return 0, false
	}
}

func defaultTopP(modelID string) (float64, bool) {
	switch {
	case strings.Contains(modelID, "qwen"):
		return 1, true
	case strings.Contains(modelID, "minimax-m2"),
		strings.Contains(modelID, "gemini"),
		strings.Contains(modelID, "kimi-k2.5"),
		strings.Contains(modelID, "kimi-k2p5"),
		strings.Contains(modelID, "kimi-k2-5"):
		return 0.95, true
	default:
		return 0, false
	}
}

func defaultTopK(modelID string) (int, bool) {
	switch {
	case strings.Contains(modelID, "minimax-m2"):
		if strings.Contains(modelID, "m2.") || strings.Contains(modelID, "m25") || strings.Contains(modelID, "m21") {
			return 40, true
		}
		return 20, true
	case strings.Contains(modelID, "gemini"):
		return 64, true
	default:
		return 0, false
	}
}

func applyConfiguredAgentSampling(request *llm.ChatRequest, info config.Info, agentName string) {
	rawAgent := configuredAgent(info, agentName)
	if rawAgent == nil {
		return
	}
	if value, ok := floatFromConfig(rawAgent["temperature"]); ok {
		request.Temperature = &value
	}
	if value, ok := floatFromConfig(rawAgent["top_p"]); ok {
		request.TopP = &value
	}
	if value, ok := intFromConfig(rawAgent["top_k"]); ok {
		request.TopK = &value
	}
	if value, ok := intFromConfig(rawAgent["max_output_tokens"]); ok {
		request.MaxTokens = &value
	} else if value, ok := intFromConfig(rawAgent["maxOutputTokens"]); ok {
		request.MaxTokens = &value
	}
}

func configuredAgentOptions(info config.Info, agentName string) map[string]any {
	rawAgent := configuredAgent(info, agentName)
	if rawAgent == nil {
		return nil
	}
	options, _ := rawAgent["options"].(map[string]any)
	return options
}

func configuredAgent(info config.Info, agentName string) map[string]any {
	name := defaultString(agentName, "build")
	agents, ok := info["agent"].(map[string]any)
	if !ok {
		return nil
	}
	rawAgent, ok := agents[name].(map[string]any)
	if !ok {
		return nil
	}
	return rawAgent
}

func defaultProviderBodyOptions(request llm.ChatRequest, rawProvider map[string]any, rawModel map[string]any, sessionID session.ID) map[string]any {
	result := map[string]any{}
	apiNPM := stringFromConfig(rawProvider["npm"])
	if modelProvider, ok := rawModel["provider"].(map[string]any); ok {
		apiNPM = defaultString(stringFromConfig(modelProvider["npm"]), apiNPM)
	}
	modelID := strings.ToLower(request.Model)
	switch {
	case request.ProviderID == "openai" ||
		request.ProviderID == "github-copilot" ||
		apiNPM == "@ai-sdk/openai" ||
		apiNPM == "@ai-sdk/github-copilot":
		result["store"] = false
	case request.ProviderID == "azure" || apiNPM == "@ai-sdk/azure":
		result["store"] = false
		result["promptCacheKey"] = string(sessionID)
	}
	if request.ProviderID == "openai" || boolFromConfig(rawProviderOption(rawProvider, "setCacheKey"), false) {
		result["promptCacheKey"] = string(sessionID)
	}
	if strings.Contains(modelID, "gpt-5") && !strings.Contains(modelID, "gpt-5-chat") {
		if !strings.Contains(modelID, "gpt-5-pro") {
			result["reasoningEffort"] = "medium"
			result["reasoningSummary"] = "auto"
		}
		if strings.Contains(modelID, "gpt-5.") &&
			!strings.Contains(modelID, "codex") &&
			!strings.Contains(modelID, "-chat") &&
			request.ProviderID != "azure" {
			result["textVerbosity"] = "low"
		}
	}
	if request.ProviderID == "venice" {
		result["promptCacheKey"] = string(sessionID)
	}
	if request.ProviderID == "openrouter" {
		result["prompt_cache_key"] = string(sessionID)
	}
	if request.ProviderID == "baseten" {
		result["chat_template_args"] = map[string]any{"enable_thinking": true}
	}
	if (strings.Contains(request.ProviderID, "zai") || strings.Contains(request.ProviderID, "zhipuai")) &&
		request.Protocol == "openai-compatible" {
		result["thinking"] = map[string]any{
			"type":           "enabled",
			"clear_thinking": false,
		}
	}
	if (request.ProviderID == "alibaba" || request.ProviderID == "alibaba-cn") &&
		boolFromConfig(rawModel["reasoning"], false) &&
		request.Protocol == "openai-compatible" &&
		!strings.Contains(modelID, "kimi-k2-thinking") {
		result["enable_thinking"] = true
	}
	return result
}

func rawProviderOption(rawProvider map[string]any, key string) any {
	if rawOptions, ok := rawProvider["options"].(map[string]any); ok {
		return rawOptions[key]
	}
	return nil
}

func applyChatOptions(request *llm.ChatRequest, options map[string]any) {
	if value := stringFromConfig(options["apiKey"]); value != "" {
		request.APIKey = value
	}
	if value := stringFromConfig(options["baseURL"]); value != "" {
		request.BaseURL = value
	}
	if rawHeaders, ok := options["headers"].(map[string]any); ok {
		request.Headers = mergeHeaders(request.Headers, stringMapFromConfig(rawHeaders))
	}
}

func bodyOptionsFromConfig(options map[string]any) map[string]any {
	result := map[string]any{}
	for key, value := range options {
		switch key {
		case "apiKey", "baseURL", "headers", "fetch", "timeout", "chunkTimeout":
			continue
		default:
			result[key] = value
		}
	}
	return result
}

func mergeAnyOptions(left map[string]any, right map[string]any) map[string]any {
	if len(left) == 0 && len(right) == 0 {
		return nil
	}
	result := map[string]any{}
	for key, value := range left {
		result[key] = value
	}
	for key, value := range right {
		if leftMap, ok := result[key].(map[string]any); ok {
			if rightMap, ok := value.(map[string]any); ok {
				result[key] = mergeAnyOptions(leftMap, rightMap)
				continue
			}
		}
		result[key] = value
	}
	return result
}

func mergeHeaders(left map[string]string, right map[string]string) map[string]string {
	if len(left) == 0 && len(right) == 0 {
		return nil
	}
	result := map[string]string{}
	for key, value := range left {
		result[key] = value
	}
	for key, value := range right {
		result[key] = value
	}
	return result
}

func stringMapFromConfig(input map[string]any) map[string]string {
	result := map[string]string{}
	for key, value := range input {
		if text, ok := value.(string); ok {
			result[key] = text
		}
	}
	return result
}

func stringFromConfig(input any) string {
	if text, ok := input.(string); ok {
		return text
	}
	return ""
}

func boolFromConfig(input any, fallback bool) bool {
	if value, ok := input.(bool); ok {
		return value
	}
	return fallback
}

func floatFromConfig(input any) (float64, bool) {
	switch value := input.(type) {
	case float64:
		return value, true
	case int:
		return float64(value), true
	case int64:
		return float64(value), true
	default:
		return 0, false
	}
}

func intFromConfig(input any) (int, bool) {
	switch value := input.(type) {
	case int:
		return value, true
	case int64:
		return int(value), true
	case float64:
		return int(value), true
	default:
		return 0, false
	}
}

func localToolDefinitions(enabled map[string]bool) []llm.ToolDefinition {
	result := []llm.ToolDefinition{}
	for _, tool := range integration.AllTools() {
		disabled := false
		for _, alias := range integration.ToolAliases(tool.Name) {
			if allowed, ok := enabled[alias]; ok && !allowed {
				disabled = true
				break
			}
		}
		if disabled {
			continue
		}
		result = append(result, llm.ToolDefinition{
			Name:        tool.Name,
			Description: toolDescription(tool),
			Parameters:  map[string]any(integration.ToolSchema(tool.Name)),
		})
	}
	return result
}

func toolDescription(tool integration.Tool) string {
	switch integration.CanonicalToolName(tool.Name) {
	case "invalid":
		return "Report invalid tool arguments back to the model."
	case "read":
		return "Read a file or list a directory from the local workspace."
	case "write":
		return "Write complete content to a file in the local workspace."
	case "edit":
		return "Replace exact text in a local workspace file."
	case "apply_patch":
		return "Apply an opencode patch to add, update, move, or delete files."
	case "bash":
		return "Run a shell command in the local workspace."
	case "glob":
		return "Find files by glob pattern in the local workspace."
	case "grep":
		return "Search local files using a regular expression."
	case "lsp":
		return "Query language-server-style symbols or hover information."
	case "webfetch":
		return "Fetch content from an HTTP or HTTPS URL."
	case "websearch":
		return "Search the web through the configured Go search bridge."
	case "question":
		return "Ask the user structured questions when more input is required."
	case "task":
		return "Start or simulate a subtask handled by the session runtime."
	case "task_status":
		return "Check the status or result of a background task."
	case "skill":
		return "Load a local opencode skill by name."
	case "todowrite":
		return "Create or update the session todo list."
	case "repo_clone":
		return "Clone or refresh a Git repository into the local cache."
	case "repo_overview":
		return "Summarize a local or cached Git repository."
	case "plan_exit":
		return "Signal that plan mode is complete and implementation can begin."
	default:
		return "Run the migrated opencode " + tool.Category + " tool."
	}
}

func (runtime *PromptRuntime) executeToolCalls(ctx context.Context, sessionID session.ID, calls []llm.ToolCall) []session.ToolExecution {
	if len(calls) == 0 {
		return nil
	}
	directory := defaultString(runtime.CWD, mustGetwd())
	result := make([]session.ToolExecution, 0, len(calls))
	for _, call := range calls {
		start := session.NowMillis()
		execution := session.ToolExecution{
			CallID:    defaultString(call.ID, call.Name),
			Tool:      call.Name,
			Input:     call.Arguments,
			Raw:       call.Raw,
			StartTime: start,
		}
		output, err := integration.Execute(ctx, integration.Request{
			Name:      call.Name,
			Directory: directory,
			Params:    call.Arguments,
		})
		execution.EndTime = session.NowMillis()
		if err != nil {
			execution.Error = err.Error()
		} else {
			execution.Title = output.Title
			execution.Output = output.Output
			execution.Metadata = output.Metadata
			if err := runtime.persistTodoTool(ctx, sessionID, call.Name, output.Metadata); err != nil {
				execution.Error = err.Error()
			}
		}
		result = append(result, execution)
	}
	return result
}

func (runtime *PromptRuntime) persistTodoTool(ctx context.Context, sessionID session.ID, tool string, metadata map[string]any) error {
	if tool != "todo" && tool != "todowrite" {
		return nil
	}
	store, ok := runtime.Messages.(session.TodoRepository)
	if !ok {
		return nil
	}
	todos, err := todosFromMetadata(metadata)
	if err != nil {
		return err
	}
	return store.SetTodos(ctx, sessionID, todos)
}

func finishReason(reason string, tools []session.ToolExecution) string {
	if reason != "" && reason != "tool-calls" {
		return reason
	}
	if len(tools) > 0 {
		if reason == "tool-calls" {
			return reason
		}
		return "stop"
	}
	return defaultString(reason, "stop")
}

func toolResultMessage(response llm.ChatResponse, tools []session.ToolExecution) llm.Message {
	var output strings.Builder
	if strings.TrimSpace(response.Text) != "" {
		output.WriteString("The assistant said before requesting tools:\n")
		output.WriteString(response.Text)
		output.WriteString("\n\n")
	}
	output.WriteString("Local tool results are available below. Continue the answer using these results.\n")
	for _, tool := range tools {
		output.WriteString("\n<tool_call")
		if tool.CallID != "" {
			output.WriteString(` id="`)
			output.WriteString(escapeAttribute(tool.CallID))
			output.WriteString(`"`)
		}
		output.WriteString(` name="`)
		output.WriteString(escapeAttribute(tool.Tool))
		output.WriteString(`">`)
		output.WriteString("\n<input>")
		output.WriteString(mustJSON(tool.Input))
		output.WriteString("</input>\n")
		if tool.Error != "" {
			output.WriteString("<error>")
			output.WriteString(tool.Error)
			output.WriteString("</error>\n")
		} else {
			output.WriteString("<output>")
			output.WriteString(tool.Output)
			output.WriteString("</output>\n")
		}
		output.WriteString("</tool_call>\n")
	}
	return llm.Message{Role: "user", Content: output.String()}
}

func todosFromMetadata(metadata map[string]any) ([]session.TodoInfo, error) {
	raw, ok := metadata["todos"]
	if !ok {
		return []session.TodoInfo{}, nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("encode todo metadata: %w", err)
	}
	var todos []session.TodoInfo
	if err := json.Unmarshal(data, &todos); err != nil {
		return nil, fmt.Errorf("decode todo metadata: %w", err)
	}
	for index := range todos {
		if todos[index].Priority == "" {
			todos[index].Priority = "medium"
		}
	}
	return todos, nil
}

func mergeUsage(left llm.Usage, right llm.Usage) llm.Usage {
	result := llm.Usage{
		InputTokens:      left.InputTokens + right.InputTokens,
		OutputTokens:     left.OutputTokens + right.OutputTokens,
		ReasoningTokens:  left.ReasoningTokens + right.ReasoningTokens,
		CacheReadTokens:  left.CacheReadTokens + right.CacheReadTokens,
		CacheWriteTokens: left.CacheWriteTokens + right.CacheWriteTokens,
		TotalTokens:      left.TotalTokens + right.TotalTokens,
	}
	if result.TotalTokens == 0 {
		result.TotalTokens = result.InputTokens + result.OutputTokens + result.ReasoningTokens
	}
	return result
}

func lowerTranscript(messages []session.WithParts) []llm.Message {
	result := []llm.Message{}
	for _, message := range messages {
		if message.Info.Role != "user" && message.Info.Role != "assistant" {
			continue
		}
		text := textContent(message.Parts)
		if strings.TrimSpace(text) == "" {
			continue
		}
		result = append(result, llm.Message{
			Role:    message.Info.Role,
			Content: text,
		})
	}
	return result
}

func textContent(parts []session.Part) string {
	var output strings.Builder
	for _, part := range parts {
		if part.Type != "text" {
			continue
		}
		text, ok := part.Data["text"].(string)
		if !ok || text == "" {
			continue
		}
		if output.Len() > 0 {
			output.WriteString("\n")
		}
		output.WriteString(text)
	}
	return output.String()
}

func modelRef(message session.WithParts, info config.Info, agentName string, transcript []session.WithParts) (session.ModelRef, bool) {
	if message.Info.Model != nil {
		return *message.Info.Model, false
	}
	if rawAgent := configuredAgent(info, agentName); rawAgent != nil {
		if configuredModel := stringFromConfig(rawAgent["model"]); configuredModel != "" {
			providerID, modelID := parseModelRef(configuredModel)
			return session.ModelRef{
				ProviderID: providerID,
				ModelID:    modelID,
				Variant:    stringFromConfig(rawAgent["variant"]),
			}, false
		}
	}
	for index := len(transcript) - 1; index >= 0; index-- {
		candidate := transcript[index]
		if candidate.Info.ID == message.Info.ID {
			continue
		}
		if candidate.Info.Role == "user" && candidate.Info.Model != nil {
			return *candidate.Info.Model, false
		}
	}
	if configured := stringFromConfig(info["model"]); configured != "" {
		providerID, modelID := parseModelRef(configured)
		return session.ModelRef{
			ProviderID: providerID,
			ModelID:    modelID,
		}, true
	}
	return session.ModelRef{
		ProviderID: "openai-compatible",
		ModelID:    "gpt-4o-mini",
	}, false
}

func parseModelRef(value string) (string, string) {
	providerID, modelID, ok := strings.Cut(value, "/")
	if !ok {
		return "openai-compatible", value
	}
	return providerID, modelID
}

func optionalPositive(value int) *int {
	if value <= 0 {
		return nil
	}
	return &value
}

func mustJSON(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(data)
}

func escapeAttribute(value string) string {
	replacer := strings.NewReplacer("&", "&amp;", `"`, "&quot;", "<", "&lt;", ">", "&gt;")
	return replacer.Replace(value)
}

func mustGetwd() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}

func defaultString(value string, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
