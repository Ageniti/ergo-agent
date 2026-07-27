// Package extensions exposes the compiled Go extension contracts.
//
// The package is an additive SDK entry point. Every declaration aliases the
// original agent package, preserving callback signatures and type identity.
package extensions

import engine "github.com/ageniti/ergo-agent/internal/engine"

type (
	Extension                = engine.Extension
	Context                  = engine.ExtensionContext
	Event                    = engine.ExtensionEvent
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
