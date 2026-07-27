package engine

import (
	"github.com/ageniti/ergo-agent/internal/buildinfo"
	messagepkg "github.com/ageniti/ergo-agent/message"
	sessionpkg "github.com/ageniti/ergo-agent/session"
	toolpkg "github.com/ageniti/ergo-agent/tool"
)

const (
	RuntimeVersion      = buildinfo.RuntimeVersion
	PromptBundleVersion = buildinfo.PromptBundleVersion
)

type (
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
)

type Command struct {
	RequestID               string           `json:"requestId"`
	RunID                   string           `json:"runId"`
	SessionID               string           `json:"sessionId"`
	AgentID                 string           `json:"agentId"`
	AgentScope              string           `json:"agentScope"`
	Prompt                  string           `json:"prompt"`
	CWD                     string           `json:"cwd"`
	Provider                string           `json:"provider"`
	Model                   string           `json:"model"`
	ThinkingLevel           string           `json:"thinkingLevel"`
	PlanMode                bool             `json:"planMode"`
	PlanModeSpecified       bool             `json:"planModeSpecified"`
	PlanID                  string           `json:"planId"`
	PlanExecuting           bool             `json:"planExecuting"`
	ApprovedToolCallIDs     []string         `json:"approvedToolCallIds"`
	ApprovedArgumentHashes  []string         `json:"approvedArgumentHashes"`
	DeniedArgumentHashes    []string         `json:"deniedArgumentHashes"`
	Resume                  bool             `json:"resume"`
	ResumePrompt            string           `json:"resumePrompt"`
	SessionEntries          []map[string]any `json:"sessionEntries"`
	SessionLeafID           string           `json:"sessionLeafId"`
	Operation               string           `json:"operation"`
	CustomInstructions      string           `json:"customInstructions"`
	TargetEntryID           string           `json:"targetEntryId"`
	Summarize               bool             `json:"summarize"`
	ReplaceInstructions     bool             `json:"replaceInstructions"`
	SkillName               string           `json:"skillName"`
	TemplateName            string           `json:"templateName"`
	TemplateArgs            []string         `json:"templateArgs"`
	Images                  []Image          `json:"images"`
	TargetProvider          string           `json:"targetProvider"`
	TargetModel             string           `json:"targetModel"`
	TargetThinkingLevel     string           `json:"targetThinkingLevel"`
	ActiveToolNames         []string         `json:"activeToolNames"`
	CustomType              string           `json:"customType"`
	CustomData              any              `json:"customData"`
	CustomContent           string           `json:"customContent"`
	Display                 *bool            `json:"display"`
	Label                   string           `json:"label"`
	SessionName             string           `json:"sessionName"`
	CommandName             string           `json:"commandName"`
	CommandArgs             string           `json:"commandArgs"`
	ProviderTimeoutMS       int              `json:"providerTimeoutMs"`
	ProviderMaxRetries      int              `json:"providerMaxRetries"`
	ProviderMaxRetryDelayMS int              `json:"providerMaxRetryDelayMs"`
	RetryEnabled            *bool            `json:"retryEnabled"`
	RetryMaxRetries         int              `json:"retryMaxRetries"`
	RetryBaseDelayMS        int              `json:"retryBaseDelayMs"`
	CompactionEnabled       *bool            `json:"compactionEnabled"`
	CompactionReserve       int              `json:"compactionReserveTokens"`
	CompactionKeepRecent    int              `json:"compactionKeepRecentTokens"`
	ContextWindow           int              `json:"contextWindow"`
	MaxOutputTokens         int              `json:"maxOutputTokens"`
	SteeringMode            string           `json:"steeringMode"`
	FollowUpMode            string           `json:"followUpMode"`
	ProjectTrusted          *bool            `json:"projectTrusted"`
	PackageSource           string           `json:"packageSource"`
	PackageScope            string           `json:"packageScope"`
	PackagePersist          *bool            `json:"packagePersist"`
}

type Event struct {
	Type      string
	RunID     string
	SessionID string
	AgentID   string
	Sequence  uint64
	Payload   map[string]any
}

type EventSink func(Event) error

type RunOptions struct {
	Controls     ControlPoller
	Interactions InteractionPoller
	Sessions     SessionController
}
