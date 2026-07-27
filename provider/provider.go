// Package provider defines model-provider contracts and protocol adapters.
package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ageniti/ergo-agent/message"
	"github.com/ageniti/ergo-agent/tool"
)

type ProviderHTTPError struct {
	StatusCode int
	Status     string
	Body       string
	RetryAfter time.Duration
	Headers    map[string]string
}

func (e *ProviderHTTPError) Error() string {
	return fmt.Sprintf("provider returned %s: %s", e.Status, e.Body)
}

type ImageGenerationRequest struct {
	Model   string            `json:"model,omitempty"`
	Prompt  string            `json:"prompt"`
	Images  []message.Image   `json:"images,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

type ImageGenerationResult struct {
	API          string          `json:"api"`
	Provider     string          `json:"provider"`
	Model        string          `json:"model"`
	Text         string          `json:"text,omitempty"`
	Images       []message.Image `json:"images,omitempty"`
	ResponseID   string          `json:"responseId,omitempty"`
	Usage        map[string]any  `json:"usage,omitempty"`
	StopReason   string          `json:"stopReason"`
	ErrorMessage string          `json:"errorMessage,omitempty"`
	Timestamp    int64           `json:"timestamp"`
}

type ImageGenerator interface {
	GenerateImage(context.Context, ImageGenerationRequest) ImageGenerationResult
}

type CompletionRequest struct {
	SessionID     string
	Model         string
	System        string
	Messages      []message.Message
	Tools         []tool.ToolDefinition
	ThinkingLevel string
	MaxTokens     int
	Headers       map[string]string
}

type Completion struct {
	Text              string
	TextSignature     string
	Thinking          string
	ThinkingSignature string
	ToolCalls         []message.ToolCall
	Usage             map[string]any
	StopReason        string
	ErrorMessage      string
	ResponseStatus    int
	ResponseHeaders   map[string]string
}

type CompletionDelta struct {
	Text               string `json:"text,omitempty"`
	Thinking           string `json:"thinking,omitempty"`
	ToolCallID         string `json:"toolCallId,omitempty"`
	ToolName           string `json:"toolName,omitempty"`
	ToolArgumentsDelta string `json:"toolArgumentsDelta,omitempty"`
}

type ModelPricing struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
}

type Provider interface {
	Complete(context.Context, CompletionRequest) (Completion, error)
}

type StreamingProvider interface {
	Provider
	Stream(context.Context, CompletionRequest, func(CompletionDelta) error) (Completion, error)
}

type ProviderAPI string

const (
	ProviderAPIOpenAICompletions ProviderAPI = "openai-completions"
	ProviderAPIOpenAIResponses   ProviderAPI = "openai-responses"
	ProviderAPIAnthropicMessages ProviderAPI = "anthropic-messages"
	ProviderAPIGoogleGenerative  ProviderAPI = "google-generative-ai"
	ProviderAPIGoogleVertex      ProviderAPI = "google-vertex"
	ProviderAPIMistral           ProviderAPI = "mistral-conversations"
	ProviderAPIPiMessages        ProviderAPI = "pi-messages"
)

type HTTPProviderConfig struct {
	Name               string
	API                ProviderAPI
	BaseURL            string
	APIKey             string
	Headers            map[string]string
	APIVersion         string
	ModelMap           map[string]string
	DisableDefaultAuth bool
}

type ProviderFactory interface {
	Provider(name string, timeoutMS int) (Provider, error)
}

type ModelProviderFactory interface {
	ProviderForModel(name, model string, timeoutMS int) (Provider, error)
}

// Local aliases keep protocol implementations concise and preserve the
// vocabulary of the original package.
type (
	Message        = message.Message
	Image          = message.Image
	ToolCall       = message.ToolCall
	ToolDefinition = tool.ToolDefinition

	Factory      = ProviderFactory
	ModelFactory = ModelProviderFactory
	Registry     = ProviderRegistry
	HTTPFactory  = HTTPProviderFactory
	HTTPConfig   = HTTPProviderConfig
	API          = ProviderAPI
	HTTPError    = ProviderHTTPError
	ImageRequest = ImageGenerationRequest
	ImageResult  = ImageGenerationResult
)

const (
	APIOpenAICompletions = ProviderAPIOpenAICompletions
	APIOpenAIResponses   = ProviderAPIOpenAIResponses
	APIAnthropicMessages = ProviderAPIAnthropicMessages
	APIGoogleGenerative  = ProviderAPIGoogleGenerative
	APIGoogleVertex      = ProviderAPIGoogleVertex
	APIMistral           = ProviderAPIMistral
	APIPiMessages        = ProviderAPIPiMessages
)

func NewRegistry(fallback Factory) *Registry {
	return NewProviderRegistry(fallback)
}

func NewHTTP(config HTTPConfig, timeoutMS int) (Provider, error) {
	return NewHTTPProvider(config, timeoutMS)
}

func BuiltinNames() []string {
	return BuiltinProviderNames()
}

func number(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		n, _ := v.Int64()
		return int(n)
	default:
		return 0
	}
}
