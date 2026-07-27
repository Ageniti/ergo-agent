// Package agent is the backward-compatible complete SDK facade.
//
// Runtime implementation lives in internal/engine. Capability-specific
// contracts and implementations live in provider, resource, message, tool,
// and session packages. Type aliases preserve the original public API and
// exact type identity for existing integrations.
package agent

import (
	agentassets "github.com/ageniti/ergo-agent"
	engine "github.com/ageniti/ergo-agent/internal/engine"
	messagepkg "github.com/ageniti/ergo-agent/message"
	providerpkg "github.com/ageniti/ergo-agent/provider"
	resourcepkg "github.com/ageniti/ergo-agent/resource"
	sessionpkg "github.com/ageniti/ergo-agent/session"
	toolpkg "github.com/ageniti/ergo-agent/tool"
)

type (
	Runtime    = engine.Runtime
	Command    = engine.Command
	Event      = engine.Event
	EventSink  = engine.EventSink
	RunOptions = engine.RunOptions

	Image          = messagepkg.Image
	Message        = messagepkg.Message
	ToolCall       = messagepkg.ToolCall
	ToolDefinition = toolpkg.ToolDefinition
	ToolResult     = toolpkg.ToolResult

	Control           = sessionpkg.Control
	ControlPoller     = sessionpkg.ControlPoller
	InteractionReply  = sessionpkg.InteractionReply
	InteractionPoller = sessionpkg.InteractionPoller
	SessionController = sessionpkg.Controller

	ProviderHTTPError              = providerpkg.ProviderHTTPError
	ImageGenerationRequest         = providerpkg.ImageGenerationRequest
	ImageGenerationResult          = providerpkg.ImageGenerationResult
	ImageGenerator                 = providerpkg.ImageGenerator
	CompletionRequest              = providerpkg.CompletionRequest
	Completion                     = providerpkg.Completion
	CompletionDelta                = providerpkg.CompletionDelta
	ModelPricing                   = providerpkg.ModelPricing
	Provider                       = providerpkg.Provider
	StreamingProvider              = providerpkg.StreamingProvider
	ProviderAPI                    = providerpkg.ProviderAPI
	HTTPProviderConfig             = providerpkg.HTTPProviderConfig
	ProviderFactory                = providerpkg.ProviderFactory
	ModelProviderFactory           = providerpkg.ModelProviderFactory
	HTTPProviderFactory            = providerpkg.HTTPProviderFactory
	ProviderRegistry               = providerpkg.ProviderRegistry
	ImageModel                     = providerpkg.ImageModel
	OpenRouterImageGeneratorConfig = providerpkg.OpenRouterImageGeneratorConfig

	Agent                    = resourcepkg.Agent
	AgentDefinition          = resourcepkg.AgentDefinition
	AgentRole                = resourcepkg.AgentRole
	Skill                    = resourcepkg.Skill
	ResourceDiagnostic       = resourcepkg.ResourceDiagnostic
	PromptTemplate           = resourcepkg.PromptTemplate
	Resources                = resourcepkg.Resources
	PackageSource            = resourcepkg.PackageSource
	PackageDiagnostic        = resourcepkg.PackageDiagnostic
	PackageManager           = resourcepkg.PackageManager
	AgentPackageBuildOptions = resourcepkg.AgentPackageBuildOptions
	AgentPackageBuildResult  = resourcepkg.AgentPackageBuildResult

	Extension                = engine.Extension
	ExtensionContext         = engine.ExtensionContext
	ExtensionEvent           = engine.ExtensionEvent
	ContextEvent             = engine.ContextEvent
	ResourcesDiscoverEvent   = engine.ResourcesDiscoverEvent
	ResourcesDiscoverResult  = engine.ResourcesDiscoverResult
	SessionLifecycleEvent    = engine.SessionLifecycleEvent
	AgentEndEvent            = engine.AgentEndEvent
	InputEvent               = engine.InputEvent
	InputDecision            = engine.InputDecision
	ModelSelectEvent         = engine.ModelSelectEvent
	ThinkingLevelSelectEvent = engine.ThinkingLevelSelectEvent
	CompactionPreparation    = engine.CompactionPreparation
	CompactionEvent          = engine.CompactionEvent
	CompactionDecision       = engine.CompactionDecision
	CompactedEvent           = engine.CompactedEvent
	ProjectTrustEvent        = engine.ProjectTrustEvent
	ProjectTrustDecision     = engine.ProjectTrustDecision
	SessionInfoChangedEvent  = engine.SessionInfoChangedEvent
	SessionSwitchEvent       = engine.SessionSwitchEvent
	SessionForkEvent         = engine.SessionForkEvent
	SessionForkDecision      = engine.SessionForkDecision
	SessionTreeEvent         = engine.SessionTreeEvent
	MessageEndEvent          = engine.MessageEndEvent
	ContextUsage             = engine.ContextUsage
	ProviderRequestEvent     = engine.ProviderRequestEvent
	ProviderHeadersEvent     = engine.ProviderHeadersEvent
	ProviderResponseEvent    = engine.ProviderResponseEvent
	AgentStartEvent          = engine.AgentStartEvent
	SystemPromptOptions      = engine.SystemPromptOptions
	ToolCallEvent            = engine.ToolCallEvent
	ToolCallDecision         = engine.ToolCallDecision
	ToolResultEvent          = engine.ToolResultEvent
	ToolResultOverride       = engine.ToolResultOverride
	TurnEvent                = engine.TurnEvent
)

const (
	RuntimeVersion      = engine.RuntimeVersion
	PromptBundleVersion = engine.PromptBundleVersion

	ProviderAPIOpenAICompletions = providerpkg.ProviderAPIOpenAICompletions
	ProviderAPIOpenAIResponses   = providerpkg.ProviderAPIOpenAIResponses
	ProviderAPIAnthropicMessages = providerpkg.ProviderAPIAnthropicMessages
	ProviderAPIGoogleGenerative  = providerpkg.ProviderAPIGoogleGenerative
	ProviderAPIGoogleVertex      = providerpkg.ProviderAPIGoogleVertex
	ProviderAPIMistral           = providerpkg.ProviderAPIMistral
	ProviderAPIPiMessages        = providerpkg.ProviderAPIPiMessages
	OpenRouterImagesAPI          = providerpkg.OpenRouterImagesAPI
	DefaultOpenRouterImageModel  = providerpkg.DefaultOpenRouterImageModel

	AgentRoleMain = resourcepkg.AgentRoleMain
	AgentRoleSub  = resourcepkg.AgentRoleSub
	AgentRoleMeta = resourcepkg.AgentRoleMeta
)

func New(root string) *Runtime {
	return engine.New(root)
}

// NewDefault constructs a Runtime using the SDK's embedded default resources.
func NewDefault() (*Runtime, error) {
	root, err := agentassets.DefaultRoot()
	if err != nil {
		return nil, err
	}
	return engine.New(root), nil
}

func NewProviderRegistry(fallback ProviderFactory) *ProviderRegistry {
	return providerpkg.NewProviderRegistry(fallback)
}

func NewHTTPProvider(config HTTPProviderConfig, timeoutMS int) (Provider, error) {
	return providerpkg.NewHTTPProvider(config, timeoutMS)
}

func BuiltinProviderNames() []string {
	return providerpkg.BuiltinProviderNames()
}

func BuiltinImageModels() []ImageModel {
	return providerpkg.BuiltinImageModels()
}

func NewOpenRouterImageGenerator(config OpenRouterImageGeneratorConfig) ImageGenerator {
	return providerpkg.NewOpenRouterImageGenerator(config)
}

func NewOpenRouterImageGeneratorFromEnv() ImageGenerator {
	return providerpkg.NewOpenRouterImageGeneratorFromEnv()
}
