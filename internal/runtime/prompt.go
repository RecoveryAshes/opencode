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

	model := modelRef(userMessage)
	providerConfig, err := runtime.providerConfig()
	if err != nil {
		return session.WithParts{}, err
	}
	response, tools, usage, err := runtime.runProviderLoop(ctx, sessionID, client, messages, model, localToolDefinitions(userMessage.Info.Tools), providerConfig)
	if err != nil {
		return session.WithParts{}, err
	}

	return runtime.Messages.CreateAssistant(ctx, sessionID, session.AssistantInput{
		ParentID: userMessage.Info.ID,
		Agent:    defaultString(userMessage.Info.Agent, "build"),
		Model:    model,
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

func (runtime *PromptRuntime) runProviderLoop(ctx context.Context, sessionID session.ID, client ChatClient, messages []llm.Message, model session.ModelRef, definitions []llm.ToolDefinition, providerConfig config.Info) (llm.ChatResponse, []session.ToolExecution, llm.Usage, error) {
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
			return llm.ChatResponse{}, nil, llm.Usage{}, err
		}
		applyConfiguredProviderOptions(&request, providerConfig, model)
		request.Tools = definitions
		next, err := client.Chat(ctx, request)
		if err != nil {
			return llm.ChatResponse{}, nil, llm.Usage{}, err
		}
		response = next
		usage = mergeUsage(usage, next.Usage)
		if len(next.ToolCalls) == 0 {
			return response, tools, usage, nil
		}
		executed := runtime.executeToolCalls(ctx, sessionID, next.ToolCalls)
		tools = append(tools, executed...)
		if iteration+1 >= maxIterations {
			return response, tools, usage, nil
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

func applyConfiguredProviderOptions(request *llm.ChatRequest, info config.Info, model session.ModelRef) {
	providers, ok := info["provider"].(map[string]any)
	if !ok {
		return
	}
	rawProvider, ok := providers[model.ProviderID].(map[string]any)
	if !ok {
		return
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
	if rawModelOptions, ok := rawModel["options"].(map[string]any); ok {
		applyChatOptions(request, rawModelOptions)
	}
	if rawHeaders, ok := rawModel["headers"].(map[string]any); ok {
		request.Headers = mergeHeaders(request.Headers, stringMapFromConfig(rawHeaders))
	}
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

func modelRef(message session.WithParts) session.ModelRef {
	if message.Info.Model != nil {
		return *message.Info.Model
	}
	return session.ModelRef{
		ProviderID: "openai-compatible",
		ModelID:    "gpt-4o-mini",
	}
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
