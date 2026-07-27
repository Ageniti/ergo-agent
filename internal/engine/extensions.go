package engine

import (
	"context"
	"encoding/json"
	"errors"
)

// Extension is the headless Go equivalent of Pi's Extension API. Applications
// register extensions during process startup; no TUI lifecycle is exposed.
type Extension struct {
	Name                  string
	Tools                 []ToolDefinition
	Commands              map[string]func(context.Context, string) (string, error)
	ContextCommands       map[string]func(context.Context, *ExtensionContext, string) (string, error)
	BeforeAgentStart      func(context.Context, *AgentStartEvent) error
	BeforeToolCall        func(context.Context, ToolCallEvent) (ToolCallDecision, error)
	AfterToolCall         func(context.Context, ToolResultEvent) (ToolResultOverride, error)
	AfterTurn             func(context.Context, TurnEvent) error
	OnEvent               func(context.Context, *ExtensionEvent) error
	TransformContext      func(context.Context, *ContextEvent) error
	BeforeProviderRequest func(context.Context, *ProviderRequestEvent) error
	BeforeProviderHeaders func(context.Context, *ProviderHeadersEvent) error
	AfterProviderResponse func(context.Context, ProviderResponseEvent) error
	DiscoverResources     func(context.Context, ResourcesDiscoverEvent) (ResourcesDiscoverResult, error)
	SessionStart          func(context.Context, SessionLifecycleEvent) error
	SessionShutdown       func(context.Context, SessionLifecycleEvent) error
	AgentEnd              func(context.Context, AgentEndEvent) error
	AgentSettled          func(context.Context, AgentEndEvent) error
	Input                 func(context.Context, *InputEvent) (InputDecision, error)
	ModelSelect           func(context.Context, ModelSelectEvent) error
	ThinkingLevelSelect   func(context.Context, ThinkingLevelSelectEvent) error
	BeforeCompact         func(context.Context, *CompactionEvent) (CompactionDecision, error)
	AfterCompact          func(context.Context, CompactedEvent) error
	ProjectTrust          func(context.Context, ProjectTrustEvent) (ProjectTrustDecision, error)
	SessionInfoChanged    func(context.Context, SessionInfoChangedEvent) error
	SessionBeforeSwitch   func(context.Context, SessionSwitchEvent) (bool, error)
	SessionBeforeFork     func(context.Context, SessionForkEvent) (SessionForkDecision, error)
	SessionBeforeTree     func(context.Context, SessionTreeEvent) (bool, error)
	SessionTree           func(context.Context, SessionTreeEvent) error
	MessageEnd            func(context.Context, *MessageEndEvent) error
	Action                func(context.Context, *ExtensionContext, *ExtensionEvent) error
}

type ResourcesDiscoverEvent struct{ CWD, AgentID, Reason string }
type ResourcesDiscoverResult struct {
	SystemPrompt string
	Tools        []ToolDefinition
	SkillPaths   []string
	PromptPaths  []string
}
type SessionLifecycleEvent struct{ RunID, SessionID, AgentID, Reason string }
type AgentEndEvent struct {
	AgentID  string
	Depth    int
	Messages []Message
	Message  Message
	Err      error
}
type InputEvent struct {
	Text              string
	Images            []Image
	Source            string
	StreamingBehavior string
}
type InputDecision struct {
	Action string
	Text   string
	Images []Image
}
type ModelSelectEvent struct {
	Provider, Model, PreviousProvider, PreviousModel, Source string
}
type ThinkingLevelSelectEvent struct {
	Level, PreviousLevel string
}
type CompactionPreparation = compactionPreparation
type CompactionEvent struct {
	Preparation        *CompactionPreparation
	BranchEntries      []map[string]any
	CustomInstructions string
	Reason             string
	WillRetry          bool
}
type CompactionDecision struct {
	Cancel             bool
	CustomInstructions string
}
type CompactedEvent struct {
	Entry         map[string]any
	FromExtension bool
	Reason        string
	WillRetry     bool
}
type ProjectTrustEvent struct{ CWD string }
type ProjectTrustDecision struct {
	Trusted  string
	Remember bool
}
type SessionInfoChangedEvent struct{ Name string }
type SessionSwitchEvent struct {
	Reason, Target string
}
type SessionForkEvent struct {
	EntryID, Position string
}
type SessionForkDecision struct {
	Cancel                  bool
	SkipConversationRestore bool
}
type SessionTreeEvent struct {
	TargetID, OldLeafID, NewLeafID string
	Summarize, FromExtension       bool
	CustomInstructions, Label      string
	ReplaceInstructions            bool
}
type MessageEndEvent struct {
	AgentID string
	Depth   int
	Message Message
}
type ContextUsage struct {
	Tokens        *int     `json:"tokens"`
	ContextWindow int      `json:"contextWindow"`
	Percent       *float64 `json:"percent"`
}

// ExtensionContext exposes headless session actions. It intentionally omits
// Pi's UI/rendering/shortcut methods while retaining state, queue, model,
// thinking, tool, label, message and provider actions.
type ExtensionContext struct{ execution *execution }

func (c *ExtensionContext) CWD() string {
	c.execution.mu.Lock()
	defer c.execution.mu.Unlock()
	return c.execution.currentCWD
}
func (c *ExtensionContext) Signal() context.Context { return c.execution.ctx }
func (c *ExtensionContext) IsIdle() bool {
	c.execution.mu.Lock()
	defer c.execution.mu.Unlock()
	return c.execution.activeAgents == 0
}
func (c *ExtensionContext) IsProjectTrusted() bool {
	return c.execution.projectTrusted
}
func (c *ExtensionContext) ContextUsage() *ContextUsage {
	c.execution.mu.Lock()
	entries := append([]map[string]any(nil), c.execution.entries...)
	leafID := c.execution.leafID
	currentMessages := append([]Message(nil), c.execution.currentMessages...)
	active := c.execution.activeAgents > 0
	c.execution.mu.Unlock()
	window := firstPositive(c.execution.command.ContextWindow, defaultContextWindow)
	branch := branchEntries(entries, leafID)
	lastCompaction := -1
	for i := len(branch) - 1; i >= 0; i-- {
		if branch[i]["type"] == "compaction" {
			lastCompaction = i
			break
		}
	}
	if lastCompaction >= 0 {
		validUsage := false
		for i := len(branch) - 1; i > lastCompaction; i-- {
			message, ok := entryMessage(branch[i])
			if ok && message.Role == "assistant" && message.StopReason != "aborted" && message.StopReason != "error" && usageTokens(message.Usage) > 0 {
				validUsage = true
				break
			}
		}
		if !validUsage {
			return &ContextUsage{ContextWindow: window}
		}
	}
	messages := messagesFromEntries(branch)
	if active && len(currentMessages) > 0 {
		messages = currentMessages
	}
	tokens := estimateContextTokens(messages)
	percent := float64(tokens) * 100 / float64(window)
	return &ContextUsage{Tokens: &tokens, ContextWindow: window, Percent: &percent}
}
func (c *ExtensionContext) Compact() {
	c.execution.mu.Lock()
	c.execution.compactionRequested = true
	c.execution.mu.Unlock()
}
func (c *ExtensionContext) SystemPrompt() string {
	c.execution.mu.Lock()
	defer c.execution.mu.Unlock()
	return c.execution.currentSystem
}
func (c *ExtensionContext) AllTools() []ToolDefinition {
	c.execution.mu.Lock()
	defer c.execution.mu.Unlock()
	return append([]ToolDefinition(nil), c.execution.currentTools...)
}
func (c *ExtensionContext) SystemPromptOptions() SystemPromptOptions {
	c.execution.mu.Lock()
	defer c.execution.mu.Unlock()
	options := c.execution.currentPromptOptions
	options.SelectedTools = append([]string(nil), options.SelectedTools...)
	options.PromptGuidelines = append([]string(nil), options.PromptGuidelines...)
	options.ToolSnippets = cloneStringMap(options.ToolSnippets)
	return options
}
func (c *ExtensionContext) WaitForIdle(ctx context.Context) error {
	for {
		c.execution.mu.Lock()
		idle := c.execution.activeAgents == 0
		c.execution.mu.Unlock()
		if idle {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.execution.stateChanged:
		}
	}
}
func (c *ExtensionContext) NewSession(ctx context.Context) error {
	if c.execution.sessionController == nil {
		return errors.New("session controller is not configured")
	}
	for _, extension := range c.execution.runtime.extensions() {
		if extension.SessionBeforeSwitch != nil {
			cancel, err := extension.SessionBeforeSwitch(ctx, SessionSwitchEvent{Reason: "new"})
			if err != nil {
				return err
			}
			if cancel {
				return nil
			}
		}
	}
	return c.execution.sessionController.NewSession(ctx)
}
func (c *ExtensionContext) Fork(ctx context.Context, entryID, position string) error {
	if position == "" {
		position = "at"
	}
	if position != "before" && position != "at" {
		return errors.New("fork position must be before or at")
	}
	if c.execution.sessionController == nil {
		return errors.New("session controller is not configured")
	}
	for _, extension := range c.execution.runtime.extensions() {
		if extension.SessionBeforeFork != nil {
			decision, err := extension.SessionBeforeFork(ctx, SessionForkEvent{EntryID: entryID, Position: position})
			if err != nil {
				return err
			}
			if decision.Cancel {
				return nil
			}
		}
	}
	return c.execution.sessionController.Fork(ctx, entryID, position)
}
func (c *ExtensionContext) NavigateTree(targetID string) error {
	if targetID == "" {
		return errors.New("target entry id is required")
	}
	c.execution.mu.Lock()
	oldLeaf := c.execution.leafID
	c.execution.mu.Unlock()
	event := SessionTreeEvent{TargetID: targetID, OldLeafID: oldLeaf, NewLeafID: targetID, FromExtension: true}
	for _, extension := range c.execution.runtime.extensions() {
		if extension.SessionBeforeTree != nil {
			cancel, err := extension.SessionBeforeTree(c.execution.ctx, event)
			if err != nil {
				return err
			}
			if cancel {
				return nil
			}
		}
	}
	c.execution.mu.Lock()
	c.execution.leafID = targetID
	c.execution.mu.Unlock()
	for _, extension := range c.execution.runtime.extensions() {
		if extension.SessionTree != nil {
			if err := extension.SessionTree(c.execution.ctx, event); err != nil {
				return err
			}
		}
	}
	return nil
}
func (c *ExtensionContext) SwitchSession(ctx context.Context, session string) error {
	if c.execution.sessionController == nil {
		return errors.New("session controller is not configured")
	}
	for _, extension := range c.execution.runtime.extensions() {
		if extension.SessionBeforeSwitch != nil {
			cancel, err := extension.SessionBeforeSwitch(ctx, SessionSwitchEvent{Reason: "resume", Target: session})
			if err != nil {
				return err
			}
			if cancel {
				return nil
			}
		}
	}
	return c.execution.sessionController.SwitchSession(ctx, session)
}
func (c *ExtensionContext) Reload() error {
	discovered, err := c.execution.runtime.discoverExtensionResources(c.execution.ctx, c.CWD(), c.execution.command.AgentID, "reload")
	if err != nil {
		return err
	}
	c.execution.mu.Lock()
	c.execution.discoveredResources = discovered
	c.execution.mu.Unlock()
	return c.execution.emit("agent.resources_discover", map[string]any{"event": map[string]any{"type": "resources_discover", "cwd": c.CWD(), "reason": "reload"}})
}
func (c *ExtensionContext) Shutdown() { c.Abort() }

func (c *ExtensionContext) SendUserMessage(content, delivery string) {
	message := Message{Role: "user", Content: content, Timestamp: nowMillis()}
	c.execution.mu.Lock()
	defer c.execution.mu.Unlock()
	if delivery == "followUp" {
		c.execution.followUps = append(c.execution.followUps, message)
	} else {
		c.execution.steering = append(c.execution.steering, message)
	}
}
func (c *ExtensionContext) AppendEntry(customType string, data any) {
	c.execution.appendEntry("custom", map[string]any{"customType": customType, "data": data})
}
func (c *ExtensionContext) AppendMessage(customType, content string, display bool, details any) {
	c.execution.appendEntry("custom_message", map[string]any{"customType": customType, "content": content, "display": display, "details": details})
}
func (c *ExtensionContext) SetSessionName(name string) {
	c.execution.appendEntry("session_info", map[string]any{"name": name})
	for _, extension := range c.execution.runtime.extensions() {
		if extension.SessionInfoChanged != nil {
			_ = extension.SessionInfoChanged(c.execution.ctx, SessionInfoChangedEvent{Name: name})
		}
	}
}
func (c *ExtensionContext) SetLabel(entryID, label string) {
	c.execution.appendEntry("label", map[string]any{"targetId": entryID, "label": label})
}
func (c *ExtensionContext) SetModel(provider, model string) error {
	previousProvider, previousModel := c.Model()
	c.execution.mu.Lock()
	c.execution.providerOverride = provider
	c.execution.modelOverride = model
	c.execution.mu.Unlock()
	c.execution.appendEntry("model_change", map[string]any{"provider": provider, "modelId": model})
	event := ModelSelectEvent{Provider: provider, Model: model, PreviousProvider: previousProvider, PreviousModel: previousModel, Source: "set"}
	for _, extension := range c.execution.runtime.extensions() {
		if extension.ModelSelect != nil {
			if err := extension.ModelSelect(c.execution.ctx, event); err != nil {
				return err
			}
		}
	}
	return nil
}
func (c *ExtensionContext) SetThinkingLevel(level string) error {
	previous := c.ThinkingLevel()
	c.execution.mu.Lock()
	c.execution.thinkingOverride = level
	c.execution.mu.Unlock()
	c.execution.appendEntry("thinking_level_change", map[string]any{"thinkingLevel": level})
	event := ThinkingLevelSelectEvent{Level: level, PreviousLevel: previous}
	for _, extension := range c.execution.runtime.extensions() {
		if extension.ThinkingLevelSelect != nil {
			if err := extension.ThinkingLevelSelect(c.execution.ctx, event); err != nil {
				return err
			}
		}
	}
	return nil
}
func (c *ExtensionContext) SetActiveTools(names []string) {
	c.execution.mu.Lock()
	c.execution.toolsOverride = append([]string(nil), names...)
	c.execution.toolsOverrideSet = true
	c.execution.mu.Unlock()
	c.execution.appendEntry("active_tools_change", map[string]any{"activeToolNames": append([]string(nil), names...)})
}
func (c *ExtensionContext) Model() (provider, model string) {
	provider, model, _, _, _ = c.execution.runtimeOverrides()
	if provider == "" {
		provider = c.execution.command.Provider
	}
	if model == "" {
		model = c.execution.command.Model
	}
	return provider, model
}
func (c *ExtensionContext) ThinkingLevel() string {
	_, _, thinking, _, _ := c.execution.runtimeOverrides()
	if thinking == "" {
		thinking = c.execution.command.ThinkingLevel
	}
	return thinking
}
func (c *ExtensionContext) ActiveTools() []string {
	_, _, _, names, set := c.execution.runtimeOverrides()
	if !set {
		names = append([]string(nil), c.execution.command.ActiveToolNames...)
	}
	return names
}
func (c *ExtensionContext) HasPendingMessages() bool {
	c.execution.mu.Lock()
	defer c.execution.mu.Unlock()
	return len(c.execution.steering) > 0 || len(c.execution.followUps) > 0
}
func (c *ExtensionContext) Abort() {
	if c.execution.cancel != nil {
		c.execution.cancel()
	}
}
func (c *ExtensionContext) RegisterProvider(name string, provider Provider) error {
	return c.execution.runtime.RegisterProvider(name, provider)
}
func (c *ExtensionContext) UnregisterProvider(name string) {
	c.execution.runtime.UnregisterProvider(name)
}

func cloneStringMap(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	result := make(map[string]string, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

type ExtensionEvent struct {
	Type, RunID, SessionID, AgentID string
	Payload                         map[string]any
}

type ContextEvent struct {
	AgentID  string
	Depth    int
	System   string
	Messages []Message
	Tools    []ToolDefinition
}

type ProviderRequestEvent struct {
	Provider Provider
	Request  CompletionRequest
}
type ProviderHeadersEvent struct {
	Provider Provider
	Request  CompletionRequest
	Headers  map[string]string
}

type ProviderResponseEvent struct {
	Provider Provider
	Request  CompletionRequest
	Response Completion
	Err      error
	Status   int
	Headers  map[string]string
}

type AgentStartEvent struct {
	AgentID             string
	Depth               int
	Prompt              string
	Images              []Image
	System              string
	SystemPromptOptions SystemPromptOptions
	Messages            []Message
}
type SystemPromptOptions struct {
	CWD                string
	SelectedTools      []string
	ToolSnippets       map[string]string
	PromptGuidelines   []string
	AppendSystemPrompt string
	ProjectTrusted     bool
}
type ToolCallEvent struct {
	AgentID string
	Depth   int
	Call    ToolCall
	Input   map[string]any
}
type ToolCallDecision struct {
	Block  bool
	Reason string
}
type ToolResultEvent struct {
	AgentID string
	Depth   int
	Call    ToolCall
	Result  ToolResult
	Err     error
}
type ToolResultOverride struct {
	Result    *ToolResult
	IsError   *bool
	Terminate *bool
}
type TurnEvent struct {
	AgentID string
	Depth   int
	Message Message
	Usage   map[string]any
}

func (r *Runtime) RegisterExtension(extension Extension) {
	r.extensionMu.Lock()
	defer r.extensionMu.Unlock()
	r.Extensions = append(r.Extensions, extension)
}
func (r *Runtime) RegisterModelPricing(provider, model string, pricing ModelPricing) {
	r.pricingMu.Lock()
	defer r.pricingMu.Unlock()
	if r.pricing == nil {
		r.pricing = map[string]ModelPricing{}
	}
	r.pricing[provider+"\x00"+model] = pricing
}
func (r *Runtime) RegisterModelPricingJSON(data []byte) error {
	var providers map[string]map[string]ModelPricing
	if err := json.Unmarshal(data, &providers); err != nil {
		return err
	}
	for provider, models := range providers {
		for model, pricing := range models {
			r.RegisterModelPricing(provider, model, pricing)
		}
	}
	return nil
}
func (r *Runtime) ModelPricing(provider, model string) (ModelPricing, bool) {
	r.pricingMu.RLock()
	defer r.pricingMu.RUnlock()
	pricing, ok := r.pricing[provider+"\x00"+model]
	return pricing, ok
}
func (r *Runtime) extensions() []Extension {
	r.extensionMu.RLock()
	defer r.extensionMu.RUnlock()
	return append([]Extension(nil), r.Extensions...)
}
