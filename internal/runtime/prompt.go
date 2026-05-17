// Package runtime owns local session execution that happens after a user
// prompt is stored.
package runtime

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/RecoveryAshes/opencode/internal/domain/session"
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
}

// NewPromptRuntime creates a prompt runtime backed by an OpenAI-compatible client.
func NewPromptRuntime(messages session.MessageRepository) *PromptRuntime {
	return &PromptRuntime{
		Messages: messages,
		Client:   llm.NewOpenAICompatibleClient(),
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
		client = llm.NewOpenAICompatibleClient()
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
	request := llm.ChatRequestFromEnv(messages, model.ModelID)
	response, err := client.Chat(ctx, request)
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
		Finish: response.FinishReason,
		Tokens: session.TokenUsage{
			Total:     optionalPositive(response.Usage.TotalTokens),
			Input:     response.Usage.InputTokens,
			Output:    response.Usage.OutputTokens,
			Reasoning: response.Usage.ReasoningTokens,
			Cache: session.CacheUsage{
				Read: response.Usage.CacheReadTokens,
			},
		},
		Cost: 0,
	})
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
