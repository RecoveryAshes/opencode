package llm

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// ProviderChatClient dispatches a resolved chat request to the provider's
// migrated wire protocol.
type ProviderChatClient struct {
	OpenAICompatible *OpenAICompatibleClient
	Anthropic        *AnthropicClient
	Gemini           *GeminiClient
	Bedrock          *BedrockClient
}

// NewProviderChatClient creates the default runtime provider client.
func NewProviderChatClient() *ProviderChatClient {
	httpClient := &http.Client{Timeout: 2 * time.Minute}
	return &ProviderChatClient{
		OpenAICompatible: &OpenAICompatibleClient{HTTPClient: httpClient},
		Anthropic:        &AnthropicClient{HTTPClient: httpClient},
		Gemini:           &GeminiClient{HTTPClient: httpClient},
		Bedrock:          &BedrockClient{HTTPClient: httpClient},
	}
}

// Chat sends a chat request using the protocol resolved by ResolveChatRequest.
func (client *ProviderChatClient) Chat(ctx context.Context, request ChatRequest) (ChatResponse, error) {
	switch request.Protocol {
	case "", "openai-compatible":
		openai := client.OpenAICompatible
		if openai == nil {
			openai = NewOpenAICompatibleClient()
		}
		return openai.Chat(ctx, request)
	case "anthropic-messages":
		anthropic := client.Anthropic
		if anthropic == nil {
			anthropic = NewAnthropicClient()
		}
		return anthropic.Chat(ctx, request)
	case "gemini":
		gemini := client.Gemini
		if gemini == nil {
			gemini = NewGeminiClient()
		}
		return gemini.Chat(ctx, request)
	case "bedrock-converse":
		bedrock := client.Bedrock
		if bedrock == nil {
			bedrock = NewBedrockClient()
		}
		return bedrock.Chat(ctx, request)
	default:
		return ChatResponse{}, fmt.Errorf("unsupported chat protocol %q", request.Protocol)
	}
}
