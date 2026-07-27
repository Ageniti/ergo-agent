package engine

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Runtime struct {
	Resources          Resources
	Providers          ProviderFactory
	Images             ImageGenerator
	MaxTurns, MaxDepth int
	Extensions         []Extension
	extensionMu        sync.RWMutex
	pricing            map[string]ModelPricing
	pricingMu          sync.RWMutex
	DefaultAgentID     string
}

func New(root string) *Runtime {
	return newRuntime(root, "chief-agent")
}

// NewMinimal constructs a Runtime with no implicit entry Agent. It deliberately
// has no dependency on the SDK's embedded default resources.
func NewMinimal(root string) *Runtime {
	return newRuntime(root, "")
}

func newRuntime(root, defaultAgentID string) *Runtime {
	return &Runtime{Resources: Resources{Root: root}, Providers: NewProviderRegistry(HTTPProviderFactory{}), Images: NewOpenRouterImageGeneratorFromEnv(), MaxDepth: 4, pricing: map[string]ModelPricing{}, DefaultAgentID: defaultAgentID}
}

type execution struct {
	runtime                                     *Runtime
	ctx                                         context.Context
	cancel                                      context.CancelFunc
	command                                     Command
	sink                                        EventSink
	mu                                          sync.Mutex
	emitMu                                      sync.Mutex
	eventErr                                    error
	sequence                                    uint64
	pending, pendingInput                       int
	entries                                     []map[string]any
	leafID                                      string
	steering, followUps                         []Message
	approvedCalls, approvedHashes, deniedHashes map[string]bool
	mcpApproval                                 map[string]bool
	interactionPoller                           InteractionPoller
	elicitationSequence                         uint64
	providerOverride, modelOverride             string
	thinkingOverride                            string
	toolsOverride                               []string
	toolsOverrideSet                            bool
	currentCWD, currentSystem                   string
	currentTools                                []ToolDefinition
	currentPromptOptions                        SystemPromptOptions
	currentMessages                             []Message
	activeAgents                                int
	stateChanged                                chan struct{}
	compactionRequested                         bool
	projectTrusted                              bool
	sessionController                           SessionController
	discoveredResources                         []ResourcesDiscoverResult
}

func (r *Runtime) Run(ctx context.Context, payload map[string]any, poll ControlPoller, sink EventSink) error {
	return r.RunWithOptions(ctx, payload, RunOptions{Controls: poll}, sink)
}

func toolNameAvailable(required string, tools []ToolDefinition) bool {
	if strings.HasSuffix(required, "*") {
		prefix := strings.TrimSuffix(required, "*")
		for _, tool := range tools {
			if strings.HasPrefix(tool.Name, prefix) {
				return true
			}
		}
		return false
	}
	return hasTool(tools, required)
}

func (r *Runtime) RunWithOptions(ctx context.Context, payload map[string]any, options RunOptions, sink EventSink) (err error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if sink == nil {
		sink = func(Event) error { return nil }
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	var command Command
	if err = json.Unmarshal(data, &command); err != nil {
		return err
	}
	if command.AgentID == "" {
		if r.DefaultAgentID == "" {
			return errors.New("agentId is required for a Runtime without a default Agent")
		}
		command.AgentID = r.DefaultAgentID
	}
	if command.AgentScope == "" {
		command.AgentScope = "user"
	}
	if command.AgentScope != "user" && command.AgentScope != "project" && command.AgentScope != "both" {
		return fmt.Errorf("agentScope must be user, project, or both")
	}
	if command.Operation == "" {
		command.Operation = "prompt"
	}
	projectTrusted := command.ProjectTrusted == nil || *command.ProjectTrusted
	e := &execution{runtime: r, ctx: ctx, cancel: cancel, command: command, sink: sink, entries: append([]map[string]any(nil), command.SessionEntries...), leafID: command.SessionLeafID, approvedCalls: set(command.ApprovedToolCallIDs), approvedHashes: set(command.ApprovedArgumentHashes), deniedHashes: set(command.DeniedArgumentHashes), mcpApproval: map[string]bool{}, interactionPoller: options.Interactions, projectTrusted: projectTrusted, sessionController: options.Sessions, stateChanged: make(chan struct{}, 1)}
	e.currentCWD = command.CWD
	if err = e.emit("run.started", map[string]any{"runtimeVersion": RuntimeVersion, "promptBundleVersion": PromptBundleVersion, "engine": "go-native-agent-runtime"}); err != nil {
		return err
	}
	startReason := "new"
	if command.Resume || len(command.SessionEntries) > 0 {
		startReason = "resume"
	}
	if err = e.emit("agent.session_start", map[string]any{"event": map[string]any{"type": "session_start", "reason": startReason}}); err != nil {
		return err
	}
	for _, extension := range r.extensions() {
		if extension.SessionStart != nil {
			if err = extension.SessionStart(ctx, SessionLifecycleEvent{RunID: command.RunID, SessionID: command.SessionID, AgentID: command.AgentID, Reason: startReason}); err != nil {
				return err
			}
		}
	}
	defer func() {
		reason := "complete"
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			reason = "abort"
		} else if err != nil {
			reason = "error"
		}
		for _, extension := range r.extensions() {
			if extension.SessionShutdown != nil {
				_ = extension.SessionShutdown(ctx, SessionLifecycleEvent{RunID: command.RunID, SessionID: command.SessionID, AgentID: command.AgentID, Reason: reason})
			}
		}
		_ = e.emit("agent.session_shutdown", map[string]any{"event": map[string]any{"type": "session_shutdown", "reason": reason}})
	}()
	for _, extension := range r.extensions() {
		if extension.ProjectTrust == nil {
			continue
		}
		decision, trustErr := extension.ProjectTrust(ctx, ProjectTrustEvent{CWD: command.CWD})
		if trustErr != nil {
			return trustErr
		}
		switch decision.Trusted {
		case "", "undecided":
		case "yes":
			projectTrusted = true
		case "no":
			projectTrusted = false
		default:
			return fmt.Errorf("extension returned invalid project trust decision %q", decision.Trusted)
		}
	}
	e.projectTrusted = projectTrusted
	e.discoveredResources, err = r.discoverExtensionResources(ctx, command.CWD, command.AgentID, "startup")
	if err != nil {
		return err
	}
	if err = r.expandPromptOperation(&command, projectTrusted, e.discoveredResources); err != nil {
		return err
	}
	e.command = command
	if command.Operation != "prompt" {
		return e.operation(ctx)
	}
	controlsDone := make(chan struct{})
	if options.Controls != nil {
		go e.pollControls(ctx, options.Controls, controlsDone)
	}
	defer close(controlsDone)
	text, usage, err := e.runAgent(ctx, command.AgentID, command.Prompt, command.CWD, 0, command.AgentScope, "")
	if err != nil {
		_ = e.emit("run.failed", map[string]any{"message": err.Error()})
		return err
	}
	planMode := e.effectivePlanMode()
	planSteps := []map[string]any(nil)
	completedSteps := []int(nil)
	if planMode {
		planSteps = extractPlan(text)
		if len(planSteps) > 0 {
			e.appendEntry("custom", map[string]any{"customType": "plan-mode", "data": map[string]any{"enabled": true, "executing": false, "todos": planSteps}})
		}
	} else {
		completedSteps = extractDone(text)
		if command.PlanID != "" {
			e.appendEntry("custom", map[string]any{"customType": "plan-mode", "data": map[string]any{"enabled": false, "executing": len(completedSteps) == 0, "planId": command.PlanID, "completedSteps": completedSteps}})
		}
	}
	if err = e.snapshot(); err != nil {
		return err
	}
	if e.pending > 0 {
		return e.emit("run.paused", map[string]any{"reason": "awaiting_approval", "pendingApprovals": e.pending})
	}
	if e.pendingInput > 0 {
		return e.emit("run.paused", map[string]any{"reason": "awaiting_input", "pendingInteractions": e.pendingInput})
	}
	if len(planSteps) > 0 {
		_ = e.emit("plan.created", map[string]any{"steps": planSteps})
	} else if len(completedSteps) > 0 {
		_ = e.emit("plan.progress", map[string]any{"completedSteps": completedSteps})
	}
	return e.emit("run.completed", map[string]any{"text": text, "message": map[string]any{"role": "assistant", "content": []map[string]any{{"type": "text", "text": text}}, "usage": usage}, "usage": usage, "operation": command.Operation})
}

func (r *Runtime) discoverExtensionResources(ctx context.Context, cwd, agentID, reason string) ([]ResourcesDiscoverResult, error) {
	var results []ResourcesDiscoverResult
	for _, extension := range r.extensions() {
		if extension.DiscoverResources == nil {
			continue
		}
		result, err := extension.DiscoverResources(ctx, ResourcesDiscoverEvent{CWD: cwd, AgentID: agentID, Reason: reason})
		if err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	return results, nil
}

func (r *Runtime) expandPromptOperation(command *Command, projectTrusted bool, discovered []ResourcesDiscoverResult) error {
	var extensionSkillPaths, extensionPromptPaths []string
	for _, result := range discovered {
		extensionSkillPaths = append(extensionSkillPaths, result.SkillPaths...)
		extensionPromptPaths = append(extensionPromptPaths, result.PromptPaths...)
	}
	switch command.Operation {
	case "skill":
		scope := "user"
		if projectTrusted {
			scope = "both"
		}
		skills, err := r.Resources.Skills(command.CWD, scope)
		if err != nil {
			return err
		}
		skills = append(skills, loadSkillsFromPaths(extensionSkillPaths)...)
		for _, skill := range skills {
			if skill.Name != command.SkillName {
				continue
			}
			data, readErr := os.ReadFile(skill.Path)
			if readErr != nil {
				return readErr
			}
			_, body := frontmatter(string(data))
			baseDir := skillBaseDir(skill)
			body = expandSkillContent(skill, body)
			command.Prompt = fmt.Sprintf("<skill name=\"%s\" location=\"%s\" base_dir=\"%s\">\nReferences are relative to %s. Literal {baseDir} placeholders have already been expanded.\n\n%s\n</skill>", skill.Name, skill.Path, baseDir, baseDir, strings.TrimSpace(body))
			if command.CustomInstructions != "" {
				command.Prompt += "\n\n" + command.CustomInstructions
			}
			command.Operation = "prompt"
			return nil
		}
		return fmt.Errorf("skill %q not found", command.SkillName)
	case "prompt_template":
		templates := append(r.Resources.TemplatesAtScope(command.CWD, projectTrusted), loadTemplatesFromPaths(extensionPromptPaths)...)
		for _, template := range templates {
			if template.Name == command.TemplateName {
				command.Prompt = substituteTemplateArgs(template.Body, command.TemplateArgs)
				command.Operation = "prompt"
				return nil
			}
		}
		return fmt.Errorf("prompt template %q not found", command.TemplateName)
	}
	return nil
}

func (e *execution) runAgent(ctx context.Context, agentID, prompt, cwd string, depth int, scope, preferredPackageRoot string) (string, map[string]any, error) {
	if depth > e.runtime.MaxDepth {
		return "", nil, fmt.Errorf("subagent depth limit exceeded")
	}
	if !e.projectTrusted && (scope == "project" || scope == "both") {
		scope = "user"
	}
	var def Agent
	var err error
	if preferredPackageRoot != "" {
		def, err = agentInPackage(preferredPackageRoot, agentID)
	} else {
		def, err = e.runtime.Resources.AgentAt(agentID, cwd, scope)
	}
	if err != nil {
		return "", nil, err
	}
	currentPackageRoot, packaged, err := agentPackageRootForProfile(def.Path)
	if err != nil {
		return "", nil, err
	}
	settings := sessionSettings{}
	if depth == 0 {
		settings = restoreSettings(e.branch())
		if e.command.SteeringMode == "" {
			e.command.SteeringMode = first(settings.SteeringMode, "one-at-a-time")
		}
		if e.command.FollowUpMode == "" {
			e.command.FollowUpMode = first(settings.FollowUpMode, "one-at-a-time")
		}
	}
	planMode := depth == 0 && e.command.PlanMode
	if depth == 0 && !e.command.PlanModeSpecified && settings.PlanModeSet {
		planMode = settings.PlanMode
	}
	providerOverride, modelOverride, thinkingOverride, toolsOverride, toolsOverrideSet := e.runtimeOverrides()
	providerName := first(providerOverride, settings.Provider, e.command.Provider)
	model := first(modelOverride, settings.Model, e.command.Model)
	if depth > 0 {
		model = first(def.Model, model)
		providerName = first(def.Provider, providerForModel(model), providerName)
	} else {
		model = first(model, def.Model)
		providerName = first(providerName, def.Provider, providerForModel(model))
	}
	provider, err := providerFromFactory(e.runtime.Providers, providerName, model, e.command.ProviderTimeoutMS)
	if err != nil {
		return "", nil, err
	}
	thinking := first(thinkingOverride, settings.Thinking, e.command.ThinkingLevel, def.ThinkingLevel, "medium")
	if depth == 0 {
		for _, extension := range e.runtime.extensions() {
			if extension.ModelSelect != nil {
				if err := extension.ModelSelect(ctx, ModelSelectEvent{Provider: providerName, Model: model, Source: "restore"}); err != nil {
					return "", nil, err
				}
			}
			if extension.ThinkingLevelSelect != nil {
				if err := extension.ThinkingLevelSelect(ctx, ThinkingLevelSelectEvent{Level: thinking}); err != nil {
					return "", nil, err
				}
			}
		}
	}
	toolNames := def.Tools
	if depth == 0 && settings.ActiveToolsSet {
		toolNames = settings.ActiveTools
	}
	if len(e.command.ActiveToolNames) > 0 && depth == 0 {
		toolNames = e.command.ActiveToolNames
	}
	if toolsOverrideSet && depth == 0 {
		toolNames = toolsOverride
	}
	if planMode {
		toolNames = planTools(toolNames)
	}
	baseToolFilterSet := settings.ActiveToolsSet || len(e.command.ActiveToolNames) > 0 || toolsOverrideSet
	baseToolFilter := append([]string(nil), toolNames...)
	mcp, err := loadMCP(ctx, cwd, mcpHost{
		sample: func(sampleCtx context.Context, params *mcp.CreateMessageParams) (*mcp.CreateMessageResult, error) {
			return e.sampleForMCP(sampleCtx, provider, model, thinking, params)
		},
		elicit: func(_ context.Context, params *mcp.ElicitParams) (*mcp.ElicitResult, error) {
			return e.waitForMCPElicitation(ctx, params)
		},
	})
	if err != nil {
		return "", nil, err
	}
	defer mcp.close()
	e.mu.Lock()
	for name := range mcp.approval {
		e.mcpApproval[name] = true
	}
	e.mu.Unlock()
	discoveryScope := "both"
	if !e.projectTrusted {
		discoveryScope = "user"
	}
	availableAgents := e.runtime.Resources.AgentDefinitionsAt(cwd, discoveryScope)
	subagentNames := make([]string, 0, len(availableAgents))
	for _, candidate := range availableAgents {
		if canDelegateTo(def, candidate) {
			subagentNames = append(subagentNames, candidate.Name)
		}
	}
	ts := &toolset{cwd: cwd, planMode: planMode, subagentNames: subagentNames, subagentScope: scope, images: e.runtime.Images}
	if depth == 0 {
		ts.todos, ts.nextTodo = restoreTodos(e.branch())
	}
	ts.subagent = func(childCtx context.Context, name, task, childCwd, scope string) (string, error) {
		target := cwd
		if childCwd != "" {
			if filepath.IsAbs(childCwd) {
				target = filepath.Clean(childCwd)
			} else {
				target = filepath.Join(cwd, childCwd)
			}
		}
		if !inside(e.command.CWD, target) {
			return "", fmt.Errorf("subagent cwd is outside the run workspace")
		}
		childScope := scope
		if childScope == "" {
			childScope = ts.subagentScope
		}
		if !e.projectTrusted && (childScope == "project" || childScope == "both") {
			childScope = "user"
		}
		var targetDefinition Agent
		childPackageRoot := ""
		if packaged {
			if candidate, packageErr := agentInPackage(currentPackageRoot, name); packageErr == nil {
				targetDefinition = candidate
				childPackageRoot = currentPackageRoot
			}
		}
		if targetDefinition.Name == "" {
			for _, candidate := range e.runtime.Resources.AgentDefinitionsAt(target, childScope) {
				if candidate.Name == name {
					targetDefinition = candidate
					break
				}
			}
		}
		if targetDefinition.Name == "" || !canDelegateTo(def, targetDefinition) {
			return "", fmt.Errorf("agent %q is not available", name)
		}
		_ = e.emit("subagent.started", map[string]any{"agentId": name, "depth": depth + 1})
		text, _, childErr := e.runAgent(childCtx, name, "Task: "+task, target, depth+1, childScope, childPackageRoot)
		if childErr == nil {
			_ = e.emit("subagent.completed", map[string]any{"agentId": name, "depth": depth + 1})
		}
		return text, childErr
	}
	activeNames := expandTools(toolNames, definitionNames(mcp.tools))
	tools := ts.definitions(activeNames)
	for _, tool := range mcp.tools {
		if contains(activeNames, tool.Name) {
			tools = upsertTool(tools, tool)
		}
	}
	resourcePrompt := ""
	var discoveredSkills []Skill
	claimedExtensionTools := map[string]bool{}
	for _, extension := range e.runtime.extensions() {
		// Pi keeps the first extension registration for a name, while an
		// extension tool is allowed to replace a built-in tool.
		for _, tool := range extensionTools(extension.Tools) {
			if claimedExtensionTools[tool.Name] {
				continue
			}
			claimedExtensionTools[tool.Name] = true
			tools = upsertTool(tools, tool)
		}
	}
	for _, result := range e.discoveredResources {
		for _, tool := range result.Tools {
			if claimedExtensionTools[tool.Name] {
				continue
			}
			claimedExtensionTools[tool.Name] = true
			tools = upsertTool(tools, tool)
		}
		if result.SystemPrompt != "" {
			resourcePrompt += "\n\n" + result.SystemPrompt
		}
		discoveredSkills = append(discoveredSkills, loadSkillsFromPaths(result.SkillPaths)...)
	}
	if _, packaged, manifestErr := agentPackageManifestForProfile(def.Path); manifestErr != nil {
		return "", nil, manifestErr
	} else if packaged {
		requiredTools := agentRequiredTools(def)
		capabilityTools := tools
		var missing []string
		for _, required := range requiredTools {
			if !toolNameAvailable(required, capabilityTools) {
				missing = append(missing, required)
			}
		}
		if len(missing) > 0 {
			return "", nil, fmt.Errorf("Agent Package %q requires unavailable host tools: %s", def.Name, strings.Join(missing, ", "))
		}
	}
	system, err := e.runtime.Resources.BuildSystemPromptWithSkills(def, cwd, tools, planMode, e.projectTrusted, discoveredSkills)
	if err != nil {
		return "", nil, err
	}
	system += resourcePrompt
	e.mu.Lock()
	if depth == 0 {
		e.currentCWD, e.currentSystem = cwd, system
		e.currentTools = append([]ToolDefinition(nil), tools...)
		e.currentPromptOptions = SystemPromptOptions{CWD: cwd, SelectedTools: definitionNames(tools), ProjectTrusted: e.projectTrusted}
	}
	e.activeAgents++
	e.mu.Unlock()
	e.signalStateChanged()
	defer func() {
		e.mu.Lock()
		e.activeAgents--
		e.mu.Unlock()
		e.signalStateChanged()
	}()
	messages := []Message{}
	checkpointResume := false
	checkpointAssistant := Message{}
	if depth == 0 {
		messages = messagesFromEntries(e.branch())
		checkpointResume = hasPendingToolCalls(messages)
		if checkpointResume {
			checkpointAssistant = messages[len(messages)-1]
		}
		if e.command.Resume {
			prompt = first(e.command.ResumePrompt, "Continue from the previous turn. Apply the user's approval decision and do not repeat completed work.")
		}
		images := append([]Image(nil), e.command.Images...)
		for _, extension := range e.runtime.extensions() {
			if extension.Input == nil {
				continue
			}
			event := &InputEvent{Text: prompt, Images: images, Source: "rpc"}
			decision, inputErr := extension.Input(ctx, event)
			if inputErr != nil {
				return "", nil, inputErr
			}
			prompt, images = event.Text, event.Images
			switch decision.Action {
			case "", "continue":
			case "transform":
				prompt, images = decision.Text, append([]Image(nil), decision.Images...)
			case "handled":
				return "", nil, nil
			default:
				return "", nil, fmt.Errorf("extension returned invalid input action %q", decision.Action)
			}
		}
		e.command.Images = images
	}
	userMessage := Message{Role: "user", Content: prompt, Timestamp: nowMillis()}
	if !checkpointResume {
		modePrompt, modeErr := e.runtime.Resources.ModePrompt(planMode, depth == 0 && e.command.PlanExecuting, prompt)
		if modeErr != nil {
			return "", nil, modeErr
		}
		if modePrompt != "" {
			messages = append(messages, Message{Role: "user", Content: modePrompt, Timestamp: nowMillis()})
		}
		if depth == 0 {
			userMessage.Images = append([]Image(nil), e.command.Images...)
		}
		messages = append(messages, userMessage)
	}
	snippets := map[string]string{}
	guidelines := []string{}
	for _, tool := range tools {
		if tool.PromptSnippet != "" {
			snippets[tool.Name] = tool.PromptSnippet
		}
		guidelines = append(guidelines, tool.PromptGuidelines...)
	}
	promptOptions := SystemPromptOptions{CWD: cwd, SelectedTools: definitionNames(tools), ToolSnippets: snippets, PromptGuidelines: guidelines, AppendSystemPrompt: def.Body, ProjectTrusted: e.projectTrusted}
	if depth == 0 {
		e.mu.Lock()
		e.currentPromptOptions = promptOptions
		e.mu.Unlock()
	}
	for _, extension := range e.runtime.extensions() {
		if extension.BeforeAgentStart != nil {
			event := &AgentStartEvent{
				AgentID: agentID, Depth: depth, Prompt: prompt, Images: append([]Image(nil), e.command.Images...),
				System: system, Messages: messages,
				SystemPromptOptions: promptOptions,
			}
			if err := extension.BeforeAgentStart(ctx, event); err != nil {
				return "", nil, err
			}
			system = event.System
			messages = event.Messages
		}
	}
	if depth == 0 {
		e.mu.Lock()
		e.currentSystem = system
		e.currentPromptOptions = promptOptions
		e.currentMessages = append([]Message(nil), messages...)
		e.mu.Unlock()
	}
	_ = e.emit("agent.agent_start", map[string]any{"agentId": agentID, "depth": depth, "event": map[string]any{"type": "agent_start"}})
	_ = e.emit("agent.turn_start", map[string]any{"agentId": agentID, "depth": depth, "event": map[string]any{"type": "turn_start", "turn": 1}})
	if !checkpointResume {
		_ = e.emit("agent.message_start", map[string]any{"agentId": agentID, "depth": depth, "event": map[string]any{"type": "message_start", "message": userMessage}})
		transformed, transformErr := e.transformMessageEnd(ctx, agentID, depth, userMessage)
		if transformErr != nil {
			return "", nil, transformErr
		}
		userMessage = transformed
		messages[len(messages)-1] = userMessage
		if depth == 0 {
			e.mu.Lock()
			e.currentMessages = append([]Message(nil), messages...)
			e.mu.Unlock()
			e.appendMessage(messages[len(messages)-1], nil)
		}
		_ = e.emit("agent.message_end", map[string]any{"agentId": agentID, "depth": depth, "event": map[string]any{"type": "message_end", "message": userMessage}})
	}
	var usage map[string]any
	overflowRecoveryAttempted := false
	if checkpointResume {
		pendingAssistant := checkpointAssistant
		batch := e.executeToolBatch(ctx, agentID, depth, pendingAssistant.ToolCalls, tools)
		messages = append(messages, batch.messages...)
		if e.pending > 0 || e.pendingInput > 0 || batch.terminate {
			if err := e.endAgent(ctx, agentID, depth, messages, pendingAssistant, nil); err != nil {
				return "", pendingAssistant.Usage, err
			}
			return pendingAssistant.Content, pendingAssistant.Usage, nil
		}
	}
	for turn := 0; e.runtime.MaxTurns <= 0 || turn < e.runtime.MaxTurns; turn++ {
		if turn > 0 {
			_ = e.emit("agent.turn_start", map[string]any{"agentId": agentID, "depth": depth, "event": map[string]any{"type": "turn_start", "turn": turn + 1}})
		}
		if depth == 0 {
			messages = append(messages, e.injectQueuedMessages("steer", agentID, depth)...)
			enabled := e.command.CompactionEnabled == nil || *e.command.CompactionEnabled
			window := e.command.ContextWindow
			if window <= 0 {
				window = defaultContextWindow
			}
			reserve := e.command.CompactionReserve
			if reserve <= 0 {
				reserve = defaultCompactionReserve
			}
			e.mu.Lock()
			manualCompaction := e.compactionRequested
			e.compactionRequested = false
			e.mu.Unlock()
			if manualCompaction || (enabled && estimateContextTokens(messages) > window-reserve) {
				_ = e.emit("session.compaction_start", map[string]any{"reason": "threshold", "tokens": estimateContextTokens(messages), "contextWindow": window})
				compacted, compactErr := e.compactWithReason(ctx, provider, model, thinking, "", "threshold", false, false)
				if compactErr != nil {
					return "", usage, compactErr
				}
				if compacted {
					messages = messagesFromEntries(e.branch())
				}
			}
			e.mu.Lock()
			e.currentMessages = append([]Message(nil), messages...)
			e.mu.Unlock()
		}
		partial := Message{Role: "assistant", Provider: providerName, Model: model, Timestamp: nowMillis()}
		_ = e.emit("agent.message_start", map[string]any{"agentId": agentID, "depth": depth, "event": map[string]any{"type": "message_start", "message": partial}})
		activeToolsNow := append([]string(nil), baseToolFilter...)
		activeToolsSet := baseToolFilterSet
		if depth == 0 {
			providerNameNow, modelNow, thinkingNow, currentTools, currentToolsSet := e.runtimeOverrides()
			activeToolsNow = currentTools
			activeToolsSet = currentToolsSet
			nextProviderName, nextModel := providerName, model
			if providerNameNow != "" {
				nextProviderName = providerNameNow
			}
			if modelNow != "" {
				nextModel = modelNow
			}
			if nextProviderName != providerName || nextModel != model {
				provider, err = providerFromFactory(e.runtime.Providers, nextProviderName, nextModel, e.command.ProviderTimeoutMS)
				if err != nil {
					return "", usage, err
				}
				providerName, model = nextProviderName, nextModel
			}
			if thinkingNow != "" {
				thinking = thinkingNow
			}
		}
		requestTools := tools
		if activeToolsSet {
			requestTools = filterTools(tools, expandTools(activeToolsNow, definitionNames(mcp.tools)))
		}
		request := CompletionRequest{SessionID: e.command.SessionID, Model: model, System: system, Messages: messages, Tools: requestTools, ThinkingLevel: thinking, MaxTokens: e.command.MaxOutputTokens}
		for _, extension := range e.runtime.extensions() {
			if extension.TransformContext != nil {
				event := &ContextEvent{AgentID: agentID, Depth: depth, System: request.System, Messages: request.Messages, Tools: request.Tools}
				if err := extension.TransformContext(ctx, event); err != nil {
					return "", usage, err
				}
				request.System, request.Messages, request.Tools = event.System, event.Messages, event.Tools
			}
		}
		textIndex, thinkingIndex, nextContentIndex := -1, -1, 0
		toolIndices := map[string]int{}
		resetPartial := func() {
			partial = Message{Role: "assistant", Provider: providerName, Model: model, Timestamp: nowMillis()}
			textIndex, thinkingIndex, nextContentIndex = -1, -1, 0
			toolIndices = map[string]int{}
		}
		emitStreamEvent := func(streamEvent map[string]any) error {
			streamEvent["partial"] = partial
			return e.emit("agent.message_update", map[string]any{"agentId": agentID, "depth": depth, "assistantMessageEvent": streamEvent, "message": partial, "event": map[string]any{"type": "message_update", "assistantMessageEvent": streamEvent, "message": partial}})
		}
		completion, completeErr := e.complete(ctx, provider, request, func(delta CompletionDelta) error {
			if delta.Thinking != "" {
				if thinkingIndex < 0 {
					thinkingIndex, nextContentIndex = nextContentIndex, nextContentIndex+1
					if err := emitStreamEvent(map[string]any{"type": "thinking_start", "contentIndex": thinkingIndex}); err != nil {
						return err
					}
				}
				partial.Thinking += delta.Thinking
				if err := emitStreamEvent(map[string]any{"type": "thinking_delta", "contentIndex": thinkingIndex, "delta": delta.Thinking}); err != nil {
					return err
				}
			}
			if delta.Text != "" {
				if textIndex < 0 {
					textIndex, nextContentIndex = nextContentIndex, nextContentIndex+1
					if err := emitStreamEvent(map[string]any{"type": "text_start", "contentIndex": textIndex}); err != nil {
						return err
					}
				}
				partial.Content += delta.Text
				if err := emitStreamEvent(map[string]any{"type": "text_delta", "contentIndex": textIndex, "delta": delta.Text}); err != nil {
					return err
				}
			}
			if delta.ToolCallID != "" || delta.ToolName != "" || delta.ToolArgumentsDelta != "" {
				key := first(delta.ToolCallID, delta.ToolName)
				index, exists := toolIndices[key]
				if !exists {
					index, nextContentIndex = nextContentIndex, nextContentIndex+1
					toolIndices[key] = index
					partial.ToolCalls = append(partial.ToolCalls, ToolCall{ID: delta.ToolCallID, Name: delta.ToolName})
					if err := emitStreamEvent(map[string]any{"type": "toolcall_start", "contentIndex": index}); err != nil {
						return err
					}
				}
				for i := range partial.ToolCalls {
					if partial.ToolCalls[i].ID == delta.ToolCallID || (partial.ToolCalls[i].ID == "" && partial.ToolCalls[i].Name == delta.ToolName) {
						partial.ToolCalls[i].Arguments = append(partial.ToolCalls[i].Arguments, delta.ToolArgumentsDelta...)
					}
				}
				if err := emitStreamEvent(map[string]any{"type": "toolcall_delta", "contentIndex": index, "delta": delta.ToolArgumentsDelta}); err != nil {
					return err
				}
			}
			return nil
		}, resetPartial)
		if completeErr != nil {
			stopReason := "error"
			if errors.Is(completeErr, context.Canceled) || errors.Is(completeErr, context.DeadlineExceeded) {
				stopReason = "aborted"
			}
			assistant := Message{Role: "assistant", Provider: providerName, Model: model, Timestamp: nowMillis(), StopReason: stopReason, ErrorMessage: completeErr.Error()}
			assistant, err = e.transformMessageEnd(ctx, agentID, depth, assistant)
			if err != nil {
				return "", usage, err
			}
			messages = append(messages, assistant)
			if depth == 0 {
				e.mu.Lock()
				e.currentMessages = append([]Message(nil), messages...)
				e.mu.Unlock()
			}
			if depth == 0 {
				e.appendMessage(assistant, nil)
			}
			_ = e.emit("agent.message_end", map[string]any{"agentId": agentID, "depth": depth, "event": map[string]any{"type": "message_end", "message": assistant}})
			_ = e.emit("agent.turn_end", map[string]any{"agentId": agentID, "depth": depth, "event": map[string]any{"type": "turn_end", "message": assistant, "toolResults": []any{}}})
			enabled := e.command.CompactionEnabled == nil || *e.command.CompactionEnabled
			if depth == 0 && enabled && !overflowRecoveryAttempted && isContextOverflowError(completeErr) {
				overflowRecoveryAttempted = true
				_ = e.emit("session.compaction_start", map[string]any{"reason": "overflow", "willRetry": true})
				compacted, compactErr := e.compactWithReason(ctx, provider, model, thinking, "", "overflow", true, false)
				if compactErr != nil {
					return "", usage, compactErr
				}
				if compacted {
					messages = messagesFromEntries(e.branch())
					continue
				}
			}
			if err := e.endAgent(ctx, agentID, depth, messages, assistant, completeErr); err != nil {
				return "", usage, err
			}
			return "", usage, nil
		}
		pricing, hasPricing := e.runtime.ModelPricing(providerName, model)
		completion.Usage = applyModelPricing(completion.Usage, pricing, hasPricing)
		usage = completion.Usage
		assistant := Message{Role: "assistant", Provider: providerName, Model: model, Timestamp: nowMillis(), Content: completion.Text, TextSignature: completion.TextSignature, Thinking: completion.Thinking, ThinkingSignature: completion.ThinkingSignature, ToolCalls: completion.ToolCalls, Usage: completion.Usage, StopReason: completion.StopReason, ErrorMessage: completion.ErrorMessage}
		assistant, err = e.transformMessageEnd(ctx, agentID, depth, assistant)
		if err != nil {
			return "", usage, err
		}
		partial = assistant
		if thinkingIndex >= 0 {
			_ = emitStreamEvent(map[string]any{"type": "thinking_end", "contentIndex": thinkingIndex, "content": assistant.Thinking})
		}
		if textIndex >= 0 {
			_ = emitStreamEvent(map[string]any{"type": "text_end", "contentIndex": textIndex, "content": assistant.Content})
		}
		for _, call := range assistant.ToolCalls {
			if index, ok := toolIndices[first(call.ID, call.Name)]; ok {
				_ = emitStreamEvent(map[string]any{"type": "toolcall_end", "contentIndex": index, "toolCall": call})
			}
		}
		messages = append(messages, assistant)
		if depth == 0 {
			e.mu.Lock()
			e.currentMessages = append([]Message(nil), messages...)
			e.mu.Unlock()
		}
		if depth == 0 {
			e.appendMessage(assistant, nil)
		}
		_ = e.emit("agent.message_end", map[string]any{"agentId": agentID, "depth": depth, "event": map[string]any{"type": "message_end", "message": assistant}})
		for _, extension := range e.runtime.extensions() {
			if extension.AfterTurn != nil {
				if err := extension.AfterTurn(ctx, TurnEvent{AgentID: agentID, Depth: depth, Message: assistant, Usage: usage}); err != nil {
					return "", usage, err
				}
			}
		}
		if len(completion.ToolCalls) == 0 {
			_ = e.emit("agent.turn_end", map[string]any{"agentId": agentID, "depth": depth, "event": map[string]any{"type": "turn_end", "message": assistant, "toolResults": []any{}}})
			enabled := e.command.CompactionEnabled == nil || *e.command.CompactionEnabled
			if depth == 0 && enabled && isContextOverflowMessage(assistant, firstPositive(e.command.ContextWindow, defaultContextWindow)) {
				_ = e.emit("session.compaction_start", map[string]any{"reason": "overflow", "willRetry": false})
				if _, compactErr := e.compactWithReason(ctx, provider, model, thinking, "", "overflow", false, false); compactErr != nil {
					return "", usage, compactErr
				}
			}
			if depth == 0 {
				if queued := e.injectQueuedMessages("steer", agentID, depth); len(queued) > 0 {
					messages = append(messages, queued...)
					continue
				}
				if queued := e.injectQueuedMessages("follow_up", agentID, depth); len(queued) > 0 {
					messages = append(messages, queued...)
					continue
				}
			}
			if err := e.endAgent(ctx, agentID, depth, messages, assistant, nil); err != nil {
				return "", usage, err
			}
			return completion.Text, usage, nil
		}
		if depth == 0 {
			if err := e.snapshot(); err != nil {
				return "", usage, err
			}
		}
		var turnToolResults []Message
		terminate := false
		if completion.StopReason == "length" {
			turnToolResults = e.failTruncatedToolCalls(agentID, depth, completion.ToolCalls)
		} else {
			batch := e.executeToolBatch(ctx, agentID, depth, completion.ToolCalls, tools)
			turnToolResults, terminate = batch.messages, batch.terminate
		}
		messages = append(messages, turnToolResults...)
		if depth == 0 {
			e.mu.Lock()
			e.currentMessages = append([]Message(nil), messages...)
			e.mu.Unlock()
		}
		_ = e.emit("agent.turn_end", map[string]any{"agentId": agentID, "depth": depth, "event": map[string]any{"type": "turn_end", "message": assistant, "toolResults": turnToolResults}})
		if e.pending > 0 || e.pendingInput > 0 {
			if err := e.endAgent(ctx, agentID, depth, messages, assistant, nil); err != nil {
				return "", usage, err
			}
			return completion.Text, usage, nil
		}
		if terminate {
			if err := e.endAgent(ctx, agentID, depth, messages, assistant, nil); err != nil {
				return "", usage, err
			}
			return completion.Text, usage, nil
		}
	}
	limitErr := fmt.Errorf("agent exceeded %d tool turns", e.runtime.MaxTurns)
	if err := e.endAgent(ctx, agentID, depth, messages, Message{Role: "assistant", StopReason: "error", ErrorMessage: limitErr.Error(), Timestamp: nowMillis()}, limitErr); err != nil {
		return "", usage, err
	}
	return "", usage, limitErr
}

func (e *execution) endAgent(ctx context.Context, agentID string, depth int, messages []Message, message Message, cause error) error {
	event := AgentEndEvent{AgentID: agentID, Depth: depth, Messages: append([]Message(nil), messages...), Message: message, Err: cause}
	for _, extension := range e.runtime.extensions() {
		if extension.AgentEnd != nil {
			if err := extension.AgentEnd(ctx, event); err != nil {
				return err
			}
		}
	}
	if err := e.emit("agent.agent_end", map[string]any{"agentId": agentID, "depth": depth, "event": map[string]any{"type": "agent_end", "messages": messages}}); err != nil {
		return err
	}
	for _, extension := range e.runtime.extensions() {
		if extension.AgentSettled != nil {
			if err := extension.AgentSettled(ctx, event); err != nil {
				return err
			}
		}
	}
	return e.emit("agent.agent_settled", map[string]any{"agentId": agentID, "depth": depth, "event": map[string]any{"type": "agent_settled"}})
}

func (e *execution) failTruncatedToolCalls(agentID string, depth int, calls []ToolCall) []Message {
	messages := make([]Message, 0, len(calls))
	for _, call := range calls {
		_ = e.emit("agent.tool_execution_start", map[string]any{"agentId": agentID, "depth": depth, "event": map[string]any{"type": "tool_execution_start", "toolCallId": call.ID, "toolName": call.Name, "args": json.RawMessage(call.Arguments)}})
		result := ToolResult{Text: fmt.Sprintf("Tool call %q was not executed: the response hit the output token limit, so its arguments may be truncated. Re-issue the tool call with complete arguments.", call.Name)}
		message := Message{Role: "tool", ToolCallID: call.ID, ToolName: call.Name, Content: result.Text, IsError: true, Timestamp: nowMillis()}
		_ = e.emit("agent.tool_execution_end", map[string]any{"agentId": agentID, "depth": depth, "event": map[string]any{"type": "tool_execution_end", "toolCallId": call.ID, "toolName": call.Name, "result": result, "isError": true}})
		_ = e.emit("agent.message_start", map[string]any{"agentId": agentID, "depth": depth, "event": map[string]any{"type": "message_start", "message": message}})
		_ = e.emit("agent.message_end", map[string]any{"agentId": agentID, "depth": depth, "event": map[string]any{"type": "message_end", "message": message}})
		if depth == 0 {
			e.appendMessage(message, map[string]any{"toolName": call.Name})
		}
		messages = append(messages, message)
	}
	return messages
}

func providerForModel(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.HasPrefix(model, "claude-"):
		return "anthropic"
	case strings.HasPrefix(model, "gemini-"):
		return "google"
	case strings.HasPrefix(model, "mistral-"), strings.HasPrefix(model, "magistral-"):
		return "mistral"
	case strings.HasPrefix(model, "gpt-"), strings.HasPrefix(model, "o1"), strings.HasPrefix(model, "o3"), strings.HasPrefix(model, "o4"):
		return "openai"
	default:
		return ""
	}
}

func (e *execution) runtimeOverrides() (provider, model, thinking string, tools []string, toolsSet bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.providerOverride, e.modelOverride, e.thinkingOverride, append([]string(nil), e.toolsOverride...), e.toolsOverrideSet
}

func filterTools(tools []ToolDefinition, names []string) []ToolDefinition {
	if len(names) == 0 {
		return nil
	}
	allowed := set(names)
	result := make([]ToolDefinition, 0, len(tools))
	for _, tool := range tools {
		if allowed[tool.Name] {
			result = append(result, tool)
		}
	}
	return result
}

func upsertTool(tools []ToolDefinition, replacement ToolDefinition) []ToolDefinition {
	for index := range tools {
		if tools[index].Name == replacement.Name {
			tools[index] = replacement
			return tools
		}
	}
	return append(tools, replacement)
}

// extensionTools implements Pi's registration map semantics: within one
// extension the last registration supplies the definition while retaining the
// first registration position.
func extensionTools(tools []ToolDefinition) []ToolDefinition {
	byName := map[string]ToolDefinition{}
	order := make([]string, 0, len(tools))
	for _, tool := range tools {
		if _, exists := byName[tool.Name]; !exists {
			order = append(order, tool.Name)
		}
		byName[tool.Name] = tool
	}
	result := make([]ToolDefinition, 0, len(order))
	for _, name := range order {
		result = append(result, byName[name])
	}
	return result
}

func hasPendingToolCalls(messages []Message) bool {
	if len(messages) == 0 {
		return false
	}
	last := messages[len(messages)-1]
	return last.Role == "assistant" && len(last.ToolCalls) > 0
}

type toolOutcome struct {
	call    ToolCall
	result  ToolResult
	err     error
	message Message
}

type toolBatch struct {
	messages  []Message
	terminate bool
}

type preparedToolExecution struct {
	call ToolCall
	tool *ToolDefinition
}

// executeOneTool preserves the single-call entry point used by focused runtime
// tests while sharing the same serial preparation path as batched execution.
func (e *execution) executeOneTool(ctx context.Context, agentID string, depth int, call ToolCall, tools []ToolDefinition) toolOutcome {
	_ = e.emit("agent.tool_execution_start", map[string]any{"agentId": agentID, "depth": depth, "event": map[string]any{"type": "tool_execution_start", "toolCallId": call.ID, "toolName": call.Name, "args": json.RawMessage(call.Arguments)}})
	prepared, immediate := e.prepareToolExecution(ctx, agentID, depth, call, tools)
	if immediate != nil {
		return *immediate
	}
	return e.executePreparedTool(ctx, agentID, depth, prepared)
}

func (e *execution) executeToolBatch(ctx context.Context, agentID string, depth int, calls []ToolCall, tools []ToolDefinition) toolBatch {
	sequential := false
	for _, call := range calls {
		if tool := findTool(tools, call.Name); tool != nil && tool.ExecutionMode == "sequential" {
			sequential = true
			break
		}
	}
	outcomes := make([]toolOutcome, len(calls))
	if sequential {
		messages := make([]Message, 0, len(calls))
		finalized := make([]toolOutcome, 0, len(calls))
		for i, call := range calls {
			_ = e.emit("agent.tool_execution_start", map[string]any{"agentId": agentID, "depth": depth, "event": map[string]any{"type": "tool_execution_start", "toolCallId": call.ID, "toolName": call.Name, "args": json.RawMessage(call.Arguments)}})
			prepared, immediate := e.prepareToolExecution(ctx, agentID, depth, call, tools)
			if immediate != nil {
				outcomes[i] = *immediate
			} else {
				outcomes[i] = e.executePreparedTool(ctx, agentID, depth, prepared)
			}
			finalized = append(finalized, outcomes[i])
			messages = append(messages, e.recordToolOutcome(agentID, depth, outcomes[i]))
			if ctx.Err() != nil {
				break
			}
		}
		return toolBatch{messages: messages, terminate: shouldTerminateToolOutcomes(finalized)}
	} else {
		prepared := make([]*preparedToolExecution, len(calls))
		processed := 0
		for i, call := range calls {
			_ = e.emit("agent.tool_execution_start", map[string]any{"agentId": agentID, "depth": depth, "event": map[string]any{"type": "tool_execution_start", "toolCallId": call.ID, "toolName": call.Name, "args": json.RawMessage(call.Arguments)}})
			item, immediate := e.prepareToolExecution(ctx, agentID, depth, call, tools)
			if immediate != nil {
				outcomes[i] = *immediate
			} else {
				prepared[i] = &item
			}
			processed = i + 1
			if ctx.Err() != nil {
				break
			}
		}
		prepared = prepared[:processed]
		outcomes = outcomes[:processed]
		var wait sync.WaitGroup
		for i, item := range prepared {
			if item == nil {
				continue
			}
			wait.Add(1)
			i, item := i, *item
			go func() {
				defer wait.Done()
				outcomes[i] = e.executePreparedTool(ctx, agentID, depth, item)
			}()
		}
		wait.Wait()
	}
	messages := make([]Message, 0, len(outcomes))
	for i, outcome := range outcomes {
		if outcome.call.ID == "" {
			call := calls[i]
			outcome = toolOutcome{call: call, err: ctx.Err(), result: ToolResult{Text: "Operation aborted"}}
			outcome.message = toolOutcomeMessage(outcome)
		}
		messages = append(messages, e.recordToolOutcome(agentID, depth, outcome))
	}
	return toolBatch{messages: messages, terminate: shouldTerminateToolOutcomes(outcomes)}
}

func (e *execution) recordToolOutcome(agentID string, depth int, outcome toolOutcome) Message {
	_ = e.emit("agent.message_start", map[string]any{"agentId": agentID, "depth": depth, "event": map[string]any{"type": "message_start", "message": outcome.message}})
	if transformed, err := e.transformMessageEnd(e.ctx, agentID, depth, outcome.message); err == nil {
		outcome.message = transformed
	} else {
		outcome.message.IsError = true
		outcome.message.Content = err.Error()
	}
	_ = e.emit("agent.message_end", map[string]any{"agentId": agentID, "depth": depth, "event": map[string]any{"type": "message_end", "message": outcome.message}})
	if depth == 0 {
		e.appendMessage(outcome.message, map[string]any{"toolName": outcome.call.Name, "details": outcome.result.Details})
		if outcome.call.Name == "todo" {
			_ = e.emit("todo.updated", outcome.result.Details)
		}
	}
	return outcome.message
}

func (e *execution) transformMessageEnd(ctx context.Context, agentID string, depth int, message Message) (Message, error) {
	originalRole := message.Role
	event := &MessageEndEvent{AgentID: agentID, Depth: depth, Message: message}
	for _, extension := range e.runtime.extensions() {
		if extension.MessageEnd == nil {
			continue
		}
		if err := extension.MessageEnd(ctx, event); err != nil {
			return message, err
		}
		if event.Message.Role != originalRole {
			return message, errors.New("message_end replacement must preserve the message role")
		}
	}
	return event.Message, nil
}

func shouldTerminateToolOutcomes(outcomes []toolOutcome) bool {
	terminate := len(outcomes) > 0
	for _, outcome := range outcomes {
		if !outcome.result.Terminate {
			terminate = false
			break
		}
	}
	return terminate
}

func (e *execution) prepareToolExecution(ctx context.Context, agentID string, depth int, call ToolCall, tools []ToolDefinition) (preparedToolExecution, *toolOutcome) {
	outcome := toolOutcome{call: call}
	tool := findTool(tools, call.Name)
	if tool == nil {
		outcome.err = fmt.Errorf("tool %s not found", call.Name)
		outcome.result = ToolResult{Text: outcome.err.Error()}
		finished := e.finishToolOutcome(agentID, depth, outcome)
		return preparedToolExecution{}, &finished
	}
	preparedCall, validationErr := validateToolCall(tool, call)
	if validationErr != nil {
		outcome.err = validationErr
		outcome.result = ToolResult{Text: validationErr.Error()}
		finished := e.finishToolOutcome(agentID, depth, outcome)
		return preparedToolExecution{}, &finished
	}
	call = preparedCall
	outcome.call = preparedCall
	input := map[string]any{}
	if err := json.Unmarshal(call.Arguments, &input); err != nil {
		outcome.err = err
		outcome.result = ToolResult{Text: err.Error()}
		finished := e.finishToolOutcome(agentID, depth, outcome)
		return preparedToolExecution{}, &finished
	}
	for _, extension := range e.runtime.extensions() {
		if extension.BeforeToolCall == nil {
			continue
		}
		decision, err := extension.BeforeToolCall(ctx, ToolCallEvent{AgentID: agentID, Depth: depth, Call: call, Input: input})
		if err != nil {
			outcome.err = err
			outcome.result = ToolResult{Text: err.Error()}
			finished := e.finishToolOutcome(agentID, depth, outcome)
			return preparedToolExecution{}, &finished
		}
		if decision.Block {
			outcome.err = fmt.Errorf("%s", first(decision.Reason, "Tool execution was blocked"))
			outcome.result = ToolResult{Text: outcome.err.Error()}
			finished := e.finishToolOutcome(agentID, depth, outcome)
			return preparedToolExecution{}, &finished
		}
	}
	mutated, err := json.Marshal(input)
	if err != nil {
		outcome.err = err
		outcome.result = ToolResult{Text: err.Error()}
		finished := e.finishToolOutcome(agentID, depth, outcome)
		return preparedToolExecution{}, &finished
	}
	call.Arguments = mutated
	outcome.call = call
	if ctx.Err() != nil {
		outcome.err = errors.New("Operation aborted")
		outcome.result = ToolResult{Text: outcome.err.Error()}
		finished := e.finishToolOutcome(agentID, depth, outcome)
		return preparedToolExecution{}, &finished
	}
	hash := argumentHash(call.Name, call.Arguments)
	if e.deniedHashes[hash] {
		outcome.err = fmt.Errorf("this tool call was denied by the user")
		outcome.result = ToolResult{Text: outcome.err.Error()}
		finished := e.finishToolOutcome(agentID, depth, outcome)
		return preparedToolExecution{}, &finished
	}
	if e.requiresApproval(call) && !e.approvedCalls[call.ID] && !e.approvedHashes[hash] {
		var input map[string]any
		_ = json.Unmarshal(call.Arguments, &input)
		e.mu.Lock()
		e.pending++
		e.mu.Unlock()
		_ = e.emit("approval.requested", map[string]any{"approvalId": stableApprovalID(e.command.RunID, call.ID, hash), "toolCallId": call.ID, "toolName": call.Name, "input": input, "argumentsHash": hash, "policyVersion": "pi-permission-v1"})
		outcome.err = fmt.Errorf("dangerous tool call requires App approval")
		outcome.result = ToolResult{Text: outcome.err.Error()}
		finished := e.finishToolOutcome(agentID, depth, outcome)
		return preparedToolExecution{}, &finished
	}
	return preparedToolExecution{call: call, tool: tool}, nil
}

func (e *execution) executePreparedTool(ctx context.Context, agentID string, depth int, prepared preparedToolExecution) toolOutcome {
	call, tool := prepared.call, prepared.tool
	outcome := toolOutcome{call: call}
	if tool.ExecuteWithUpdates != nil {
		outcome.result, outcome.err = tool.ExecuteWithUpdates(ctx, call.Arguments, func(update ToolResult) error {
			return e.emit("agent.tool_execution_update", map[string]any{"agentId": agentID, "depth": depth, "event": map[string]any{"type": "tool_execution_update", "toolCallId": call.ID, "toolName": call.Name, "args": json.RawMessage(call.Arguments), "partialResult": update}})
		})
	} else {
		outcome.result, outcome.err = tool.Execute(ctx, call.Arguments)
	}
	if questionnaire, ok := outcome.result.Details["questionnaire"].(map[string]any); ok && outcome.err == nil {
		interactionID := stableQuestionnaireID(e.command.RunID, call.ID, questionnaire)
		ready := false
		if e.interactionPoller != nil {
			if reply, pollErr := e.interactionPoller(ctx, interactionID); pollErr != nil {
				outcome.err = pollErr
				outcome.result = ToolResult{Text: pollErr.Error()}
			} else if reply.Ready {
				ready = true
				if reply.Cancelled {
					outcome.result = ToolResult{Text: "The user cancelled the questionnaire.", Details: map[string]any{"interactionId": interactionID, "cancelled": true}}
				} else {
					answer, marshalErr := json.Marshal(reply.Response)
					if marshalErr != nil {
						outcome.err = marshalErr
						outcome.result = ToolResult{Text: marshalErr.Error()}
					} else {
						outcome.result = ToolResult{Text: "Questionnaire response:\n" + string(answer), Details: map[string]any{"interactionId": interactionID, "response": reply.Response}}
					}
				}
			}
		}
		if !ready && outcome.err == nil {
			e.mu.Lock()
			e.pendingInput++
			e.mu.Unlock()
			_ = e.emit("input.requested", map[string]any{"interactionId": interactionID, "toolCallId": call.ID, "kind": "questionnaire", "request": questionnaire})
		}
	}
	for _, extension := range e.runtime.extensions() {
		if extension.AfterToolCall != nil {
			override, err := extension.AfterToolCall(ctx, ToolResultEvent{AgentID: agentID, Depth: depth, Call: call, Result: outcome.result, Err: outcome.err})
			if err != nil {
				outcome.err = err
				outcome.result = ToolResult{Text: err.Error()}
			} else {
				if override.Result != nil {
					outcome.result = *override.Result
				}
				if override.Terminate != nil {
					outcome.result.Terminate = *override.Terminate
				}
				if override.IsError != nil {
					if *override.IsError && outcome.err == nil {
						outcome.err = errors.New(outcome.result.Text)
					} else if !*override.IsError {
						outcome.err = nil
					}
				}
			}
		}
	}
	return e.finishToolOutcome(agentID, depth, outcome)
}

func stableQuestionnaireID(runID, toolCallID string, request map[string]any) string {
	encoded, _ := json.Marshal(stable(request))
	sum := sha256.Sum256(append([]byte(runID+":"+toolCallID+":"), encoded...))
	value := sum[:16]
	value[6] = (value[6] & 0x0f) | 0x50
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16])
}

func stableApprovalID(runID, toolCallID, argumentsHash string) string {
	return stableQuestionnaireID(runID, toolCallID, map[string]any{"kind": "approval", "argumentsHash": argumentsHash})
}

func (e *execution) finishToolOutcome(agentID string, depth int, outcome toolOutcome) toolOutcome {
	outcome.message = toolOutcomeMessage(outcome)
	_ = e.emit("agent.tool_execution_end", map[string]any{"agentId": agentID, "depth": depth, "event": map[string]any{"type": "tool_execution_end", "toolCallId": outcome.call.ID, "toolName": outcome.call.Name, "result": outcome.result, "isError": outcome.err != nil}})
	return outcome
}

func toolOutcomeMessage(outcome toolOutcome) Message {
	text := outcome.result.Text
	if outcome.err != nil {
		text = outcome.err.Error()
	}
	return Message{Role: "tool", ToolCallID: outcome.call.ID, ToolName: outcome.call.Name, Content: text, Images: outcome.result.Images, Usage: outcome.result.Usage, AddedToolNames: outcome.result.AddedToolNames, IsError: outcome.err != nil, Timestamp: nowMillis()}
}

func (e *execution) operation(ctx context.Context) error {
	switch e.command.Operation {
	case "inspect":
		scope := "user"
		if e.projectTrusted {
			scope = "both"
		}
		def, err := e.runtime.Resources.AgentAt(e.command.AgentID, e.command.CWD, scope)
		if err != nil {
			return err
		}
		settings := restoreSettings(e.branch())
		toolNames := append([]string(nil), def.Tools...)
		if settings.ActiveToolsSet {
			toolNames = append([]string(nil), settings.ActiveTools...)
		}
		if len(e.command.ActiveToolNames) > 0 {
			toolNames = append([]string(nil), e.command.ActiveToolNames...)
		}
		if e.command.PlanMode {
			toolNames = planTools(toolNames)
		}
		mcpLoaded, loadErr := loadMCP(ctx, e.command.CWD, mcpHost{})
		if loadErr != nil {
			return loadErr
		}
		defer mcpLoaded.close()
		activeNames := expandTools(toolNames, definitionNames(mcpLoaded.tools))
		ts := &toolset{cwd: e.command.CWD, planMode: e.command.PlanMode, images: e.runtime.Images}
		tools := ts.definitions(activeNames)
		for _, tool := range mcpLoaded.tools {
			if contains(activeNames, tool.Name) {
				tools = upsertTool(tools, tool)
			}
		}
		resourcePrompt := ""
		claimed := map[string]bool{}
		for _, extension := range e.runtime.extensions() {
			for _, tool := range extensionTools(extension.Tools) {
				if !claimed[tool.Name] {
					claimed[tool.Name] = true
					tools = upsertTool(tools, tool)
				}
			}
		}
		var extraSkillPaths, extraPromptPaths []string
		for _, discovered := range e.discoveredResources {
			for _, tool := range discovered.Tools {
				if !claimed[tool.Name] {
					claimed[tool.Name] = true
					tools = upsertTool(tools, tool)
				}
			}
			if discovered.SystemPrompt != "" {
				resourcePrompt += "\n\n" + discovered.SystemPrompt
			}
			extraSkillPaths = append(extraSkillPaths, discovered.SkillPaths...)
			extraPromptPaths = append(extraPromptPaths, discovered.PromptPaths...)
		}
		extraSkills := loadSkillsFromPaths(extraSkillPaths)
		prompt, err := e.runtime.Resources.BuildSystemPromptWithSkills(def, e.command.CWD, tools, e.command.PlanMode, e.projectTrusted, extraSkills)
		if err != nil {
			return err
		}
		prompt += resourcePrompt
		skills, _ := e.runtime.Resources.Skills(e.command.CWD, scope)
		skills = append(skills, extraSkills...)
		templates := e.runtime.Resources.TemplatesAtScope(e.command.CWD, e.projectTrusted)
		templates = append(templates, loadTemplatesFromPaths(extraPromptPaths)...)
		packageDiagnostics := e.runtime.Resources.PackageDiagnosticsScope(e.command.CWD, scope)
		resourceDiagnostics := e.runtime.Resources.SkillDiagnostics(e.command.CWD, scope)
		commands := []map[string]any{}
		for _, s := range skills {
			commands = append(commands, map[string]any{"name": "skill:" + s.Name, "description": s.Description, "source": "skill", "sourceInfo": s.Path})
		}
		for _, p := range templates {
			commands = append(commands, map[string]any{"name": p.Name, "description": p.Description, "source": "prompt", "sourceInfo": p.Path})
		}
		_ = e.emit("session.inspected", map[string]any{"systemPrompt": prompt, "model": map[string]any{"provider": e.command.Provider, "id": e.command.Model}, "thinkingLevel": e.command.ThinkingLevel, "activeToolNames": definitionNames(tools), "commands": commands, "resourceDiagnostics": resourceDiagnostics, "packageDiagnostics": packageDiagnostics, "engine": "go-native-agent-runtime"})
	case "append_custom_entry":
		e.appendEntry("custom", map[string]any{"customType": e.command.CustomType, "data": e.command.CustomData, "content": e.command.CustomContent})
	case "append_custom_message":
		display := true
		if e.command.Display != nil {
			display = *e.command.Display
		}
		e.appendEntry("custom_message", map[string]any{"customType": e.command.CustomType, "content": e.command.CustomContent, "display": display, "details": e.command.CustomData})
	case "set_label":
		e.appendEntry("label", map[string]any{"targetId": e.command.TargetEntryID, "label": e.command.Label})
	case "set_session_name":
		(&ExtensionContext{execution: e}).SetSessionName(e.command.SessionName)
	case "set_model":
		if err := (&ExtensionContext{execution: e}).SetModel(e.command.TargetProvider, e.command.TargetModel); err != nil {
			return err
		}
	case "set_thinking_level":
		if err := (&ExtensionContext{execution: e}).SetThinkingLevel(e.command.TargetThinkingLevel); err != nil {
			return err
		}
	case "set_active_tools":
		(&ExtensionContext{execution: e}).SetActiveTools(e.command.ActiveToolNames)
	case "set_queue_modes":
		e.appendEntry("custom", map[string]any{"customType": "queue-modes", "data": map[string]any{"steeringMode": first(e.command.SteeringMode, "one-at-a-time"), "followUpMode": first(e.command.FollowUpMode, "one-at-a-time")}})
	case "set_plan_mode":
		e.appendEntry("custom", map[string]any{"customType": "plan-mode", "data": map[string]any{"enabled": e.command.PlanMode, "executing": e.command.PlanExecuting}})
	case "compact":
		provider, providerErr := providerFromFactory(e.runtime.Providers, e.command.Provider, e.command.Model, e.command.ProviderTimeoutMS)
		if providerErr != nil {
			return providerErr
		}
		compacted, compactErr := e.compactWithReason(ctx, provider, e.command.Model, e.command.ThinkingLevel, e.command.CustomInstructions, "manual", false, true)
		if compactErr != nil {
			return compactErr
		}
		if !compacted {
			return fmt.Errorf("session does not contain enough context to compact")
		}
	case "navigate_tree":
		oldLeaf := e.leafID
		treeEvent := SessionTreeEvent{TargetID: e.command.TargetEntryID, OldLeafID: oldLeaf, NewLeafID: e.command.TargetEntryID, Summarize: e.command.Summarize, CustomInstructions: e.command.CustomInstructions, ReplaceInstructions: e.command.ReplaceInstructions, Label: e.command.Label}
		for _, extension := range e.runtime.extensions() {
			if extension.SessionBeforeTree != nil {
				cancel, eventErr := extension.SessionBeforeTree(ctx, treeEvent)
				if eventErr != nil {
					return eventErr
				}
				if cancel {
					return e.emit("run.completed", map[string]any{"operation": e.command.Operation, "cancelled": true})
				}
			}
		}
		if e.command.Summarize {
			if err := e.summarizeAbandonedBranch(ctx, e.command.TargetEntryID); err != nil {
				return err
			}
		} else {
			e.leafID = e.command.TargetEntryID
		}
		treeEvent.NewLeafID = e.leafID
		for _, extension := range e.runtime.extensions() {
			if extension.SessionTree != nil {
				if eventErr := extension.SessionTree(ctx, treeEvent); eventErr != nil {
					return eventErr
				}
			}
		}
	case "extension_command":
		for _, extension := range e.runtime.extensions() {
			if command := extension.ContextCommands[e.command.CommandName]; command != nil {
				text, err := command(ctx, &ExtensionContext{execution: e}, e.command.CommandArgs)
				if err != nil {
					return err
				}
				e.appendEntry("custom", map[string]any{"customType": "extension-command", "data": map[string]any{"name": e.command.CommandName, "result": text}})
				if err = e.snapshot(); err != nil {
					return err
				}
				return e.emit("run.completed", map[string]any{"operation": e.command.Operation, "text": text})
			}
			if command := extension.Commands[e.command.CommandName]; command != nil {
				text, err := command(ctx, e.command.CommandArgs)
				if err != nil {
					return err
				}
				e.appendEntry("custom", map[string]any{"customType": "extension-command", "data": map[string]any{"name": e.command.CommandName, "result": text}})
				if err = e.snapshot(); err != nil {
					return err
				}
				return e.emit("run.completed", map[string]any{"operation": e.command.Operation, "text": text})
			}
		}
		return fmt.Errorf("extension command %q is not registered", e.command.CommandName)
	case "package_install", "package_update":
		if e.command.PackageScope == "project" && !e.projectTrusted {
			return errors.New("project package operations require a trusted project")
		}
		manager := PackageManager{CWD: e.command.CWD}
		persist := e.command.PackagePersist == nil || *e.command.PackagePersist
		var path string
		var err error
		if e.command.Operation == "package_install" {
			path, err = manager.Install(ctx, e.command.PackageSource, e.command.PackageScope, persist)
		} else {
			path, err = manager.Update(ctx, e.command.PackageSource, e.command.PackageScope)
		}
		if err != nil {
			return err
		}
		return e.emit("run.completed", map[string]any{"operation": e.command.Operation, "source": e.command.PackageSource, "path": path})
	case "package_remove":
		if e.command.PackageScope == "project" && !e.projectTrusted {
			return errors.New("project package operations require a trusted project")
		}
		persist := e.command.PackagePersist == nil || *e.command.PackagePersist
		if err := (PackageManager{CWD: e.command.CWD}).Remove(e.command.PackageSource, e.command.PackageScope, persist); err != nil {
			return err
		}
		return e.emit("run.completed", map[string]any{"operation": e.command.Operation, "source": e.command.PackageSource})
	case "package_list":
		configured, diagnostics := configuredPackages(e.command.CWD)
		packages := make([]map[string]any, 0, len(configured))
		for _, item := range configured {
			if item.Scope == "project" && !e.projectTrusted {
				continue
			}
			packages = append(packages, map[string]any{"source": item.Source, "scope": item.Scope, "installedPath": packageRoot(item.PackageSource, item.Base)})
		}
		return e.emit("run.completed", map[string]any{"operation": e.command.Operation, "packages": packages, "diagnostics": diagnostics})
	default:
		return fmt.Errorf("unsupported operation %q", e.command.Operation)
	}
	if err := e.snapshot(); err != nil {
		return err
	}
	return e.emit("run.completed", map[string]any{"operation": e.command.Operation})
}

func (e *execution) signalStateChanged() {
	if e.stateChanged == nil {
		return
	}
	select {
	case e.stateChanged <- struct{}{}:
	default:
	}
}

var templateArgumentPattern = regexp.MustCompile(`\$\{([0-9]+|ARGUMENTS|@):-([^}]*)\}|\$\{@:([0-9]+)(:([0-9]+))?\}|\$(ARGUMENTS|@|[0-9]+)`)

func substituteTemplateArgs(content string, args []string) string {
	all := strings.Join(args, " ")
	return templateArgumentPattern.ReplaceAllStringFunc(content, func(match string) string {
		parts := templateArgumentPattern.FindStringSubmatch(match)
		if parts[1] != "" {
			value := all
			if parts[1] != "@" && parts[1] != "ARGUMENTS" {
				index, _ := strconv.Atoi(parts[1])
				if index > 0 && index <= len(args) {
					value = args[index-1]
				} else {
					value = ""
				}
			}
			if value == "" {
				return parts[2]
			}
			return value
		}
		if parts[3] != "" {
			start, _ := strconv.Atoi(parts[3])
			if start < 1 {
				start = 1
			}
			start--
			if start >= len(args) {
				return ""
			}
			end := len(args)
			if parts[5] != "" {
				length, _ := strconv.Atoi(parts[5])
				end = min(end, start+length)
			}
			return strings.Join(args[start:end], " ")
		}
		value := parts[6]
		if value == "@" || value == "ARGUMENTS" {
			return all
		}
		index, _ := strconv.Atoi(value)
		if index > 0 && index <= len(args) {
			return args[index-1]
		}
		return ""
	})
}

func (e *execution) emit(kind string, payload map[string]any) error {
	// Events are the durable execution journal. Tool bodies may run in
	// parallel, so serialize the complete extension + sink pipeline to keep
	// sequence numbers and persisted order identical on every ECS instance.
	e.emitMu.Lock()
	defer e.emitMu.Unlock()
	if e.eventErr != nil {
		return e.eventErr
	}
	e.sequence++
	sequence := e.sequence
	for _, extension := range e.runtime.extensions() {
		event := &ExtensionEvent{Type: kind, RunID: e.command.RunID, SessionID: e.command.SessionID, AgentID: e.command.AgentID, Payload: payload}
		if extension.OnEvent != nil {
			if err := extension.OnEvent(e.ctx, event); err != nil {
				return e.failEventPipeline(err)
			}
			payload = event.Payload
		}
		if extension.Action != nil {
			if err := extension.Action(e.ctx, &ExtensionContext{execution: e}, event); err != nil {
				return e.failEventPipeline(err)
			}
			payload = event.Payload
		}
	}
	if err := e.sink(Event{Type: kind, RunID: e.command.RunID, SessionID: e.command.SessionID, AgentID: e.command.AgentID, Sequence: sequence, Payload: payload}); err != nil {
		return e.failEventPipeline(err)
	}
	return nil
}

func (e *execution) failEventPipeline(err error) error {
	e.eventErr = err
	if e.cancel != nil {
		e.cancel()
	}
	return err
}

func (e *execution) complete(ctx context.Context, provider Provider, request CompletionRequest, onDelta func(CompletionDelta) error, onReset func()) (Completion, error) {
	enabled := e.command.RetryEnabled == nil || *e.command.RetryEnabled
	attempts := e.command.RetryMaxRetries
	if attempts == 0 {
		attempts = e.command.ProviderMaxRetries
	}
	if attempts == 0 {
		attempts = 3
	}
	if attempts > 10 {
		attempts = 10
	}
	var last error
	for attempt := 0; attempt <= attempts; attempt++ {
		currentRequest := request
		for _, extension := range e.runtime.extensions() {
			if extension.BeforeProviderRequest == nil {
				continue
			}
			event := &ProviderRequestEvent{Provider: provider, Request: currentRequest}
			if err := extension.BeforeProviderRequest(ctx, event); err != nil {
				return Completion{}, err
			}
			currentRequest = event.Request
		}
		requestHeaders := cloneHeaders(currentRequest.Headers)
		if providerHeaders, ok := provider.(interface {
			ProviderHeaders(CompletionRequest) map[string]string
		}); ok {
			requestHeaders = mergeHeaders(providerHeaders.ProviderHeaders(currentRequest), requestHeaders)
		}
		currentRequest.Headers = requestHeaders
		for _, extension := range e.runtime.extensions() {
			if extension.BeforeProviderHeaders == nil {
				continue
			}
			event := &ProviderHeadersEvent{Provider: provider, Request: currentRequest, Headers: currentRequest.Headers}
			if err := extension.BeforeProviderHeaders(ctx, event); err != nil {
				return Completion{}, err
			}
			currentRequest.Headers = event.Headers
		}
		var result Completion
		var err error
		if streaming, ok := provider.(StreamingProvider); ok {
			result, err = streaming.Stream(ctx, currentRequest, onDelta)
		} else {
			result, err = provider.Complete(ctx, currentRequest)
		}
		if result.Usage != nil {
			result.Usage = normalizeUsage(result.Usage)
		}
		for _, extension := range e.runtime.extensions() {
			if extension.AfterProviderResponse != nil {
				status, headers := result.ResponseStatus, result.ResponseHeaders
				var httpErr *ProviderHTTPError
				if errors.As(err, &httpErr) {
					status, headers = httpErr.StatusCode, httpErr.Headers
				}
				if hookErr := extension.AfterProviderResponse(ctx, ProviderResponseEvent{Provider: provider, Request: currentRequest, Response: result, Err: err, Status: status, Headers: headers}); hookErr != nil {
					return Completion{}, hookErr
				}
			}
		}
		if err == nil {
			return result, nil
		}
		last = err
		if !enabled || attempt == attempts || !retryableProviderError(err) {
			break
		}
		_ = e.emit("agent.provider_retry", map[string]any{"attempt": attempt + 1, "maxRetries": attempts, "message": err.Error()})
		if onDelta != nil {
			if onReset != nil {
				onReset()
			}
			_ = e.emit("agent.message_reset", map[string]any{"reason": "provider_retry"})
		}
		baseDelay := time.Second
		if e.command.RetryBaseDelayMS > 0 {
			baseDelay = time.Duration(e.command.RetryBaseDelayMS) * time.Millisecond
		}
		maxDelay := 60 * time.Second
		if e.command.ProviderMaxRetryDelayMS > 0 {
			maxDelay = time.Duration(e.command.ProviderMaxRetryDelayMS) * time.Millisecond
		}
		delay := min(time.Duration(1<<min(attempt, 20))*baseDelay, maxDelay)
		var httpErr *ProviderHTTPError
		if errors.As(err, &httpErr) && httpErr.RetryAfter > delay {
			delay = min(httpErr.RetryAfter, maxDelay)
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return Completion{}, ctx.Err()
		case <-timer.C:
		}
	}
	return Completion{}, last
}

func normalizeUsage(raw map[string]any) map[string]any {
	result := make(map[string]any, len(raw)+7)
	for key, value := range raw {
		result[key] = value
	}
	input := firstUsageNumber(raw, "input", "input_tokens", "prompt_tokens", "promptTokenCount")
	output := firstUsageNumber(raw, "output", "output_tokens", "completion_tokens", "candidatesTokenCount")
	cacheRead := firstUsageNumber(raw, "cacheRead", "cache_read_input_tokens", "cachedContentTokenCount")
	cacheWrite := firstUsageNumber(raw, "cacheWrite", "cache_creation_input_tokens", "cache_write_input_tokens")
	reasoning := firstUsageNumber(raw, "reasoning", "thoughtsTokenCount")
	if details, ok := raw["prompt_tokens_details"].(map[string]any); ok {
		cacheRead = max(cacheRead, number(details["cached_tokens"]))
		cacheWrite = max(cacheWrite, number(details["cache_write_tokens"]))
	}
	if details, ok := raw["input_tokens_details"].(map[string]any); ok {
		cacheRead = max(cacheRead, number(details["cached_tokens"]))
		cacheWrite = max(cacheWrite, number(details["cache_write_tokens"]))
	}
	if details, ok := raw["output_tokens_details"].(map[string]any); ok {
		reasoning = max(reasoning, number(details["reasoning_tokens"]))
	}
	_, hasPromptTokens := raw["prompt_tokens"]
	_, hasPromptDetails := raw["input_tokens_details"]
	_, hasGeminiPrompt := raw["promptTokenCount"]
	if hasPromptTokens || hasPromptDetails || hasGeminiPrompt {
		input = max(0, input-cacheRead-cacheWrite)
	}
	total := firstUsageNumber(raw, "totalTokens", "total_tokens", "totalTokenCount")
	if total == 0 {
		total = input + output + cacheRead + cacheWrite
	}
	result["input"], result["output"] = input, output
	result["cacheRead"], result["cacheWrite"] = cacheRead, cacheWrite
	if reasoning > 0 {
		result["reasoning"] = reasoning
	}
	result["totalTokens"] = total
	result["cost"] = map[string]any{"input": 0.0, "output": 0.0, "cacheRead": 0.0, "cacheWrite": 0.0, "total": 0.0}
	return result
}

func applyModelPricing(usage map[string]any, pricing ModelPricing, found bool) map[string]any {
	if usage == nil {
		usage = normalizeUsage(nil)
	} else {
		usage = cloneMap(usage)
	}
	if !found {
		return usage
	}
	input := float64(number(usage["input"])) * pricing.Input / 1_000_000
	output := float64(number(usage["output"])) * pricing.Output / 1_000_000
	cacheRead := float64(number(usage["cacheRead"])) * pricing.CacheRead / 1_000_000
	cacheWrite := float64(number(usage["cacheWrite"])) * pricing.CacheWrite / 1_000_000
	usage["cost"] = map[string]any{
		"input": input, "output": output, "cacheRead": cacheRead,
		"cacheWrite": cacheWrite, "total": input + output + cacheRead + cacheWrite,
	}
	return usage
}

func firstUsageNumber(values map[string]any, keys ...string) int {
	for _, key := range keys {
		if value := number(values[key]); value > 0 {
			return value
		}
	}
	return 0
}

func cloneMap(values map[string]any) map[string]any {
	result := make(map[string]any, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

func retryableProviderError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var httpErr *ProviderHTTPError
	if errors.As(err, &httpErr) {
		return httpErr.StatusCode == 408 || httpErr.StatusCode == 409 || httpErr.StatusCode == 429 || httpErr.StatusCode >= 500
	}
	var networkErr net.Error
	return errors.As(err, &networkErr)
}

var contextOverflowPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)prompt is too long`),
	regexp.MustCompile(`(?i)request_too_large`),
	regexp.MustCompile(`(?i)input is too long for requested model`),
	regexp.MustCompile(`(?i)exceeds (?:the )?context window`),
	regexp.MustCompile(`(?i)exceeds (?:the )?(?:model'?s )?maximum context length`),
	regexp.MustCompile(`(?i)input token count.*exceeds the maximum`),
	regexp.MustCompile(`(?i)maximum prompt length is \d+`),
	regexp.MustCompile(`(?i)reduce the length of the messages`),
	regexp.MustCompile(`(?i)maximum context length is \d+ tokens`),
	regexp.MustCompile(`(?i)exceeds the available context size`),
	regexp.MustCompile(`(?i)greater than the context length`),
	regexp.MustCompile(`(?i)context window exceeds limit`),
	regexp.MustCompile(`(?i)exceeded model token limit`),
	regexp.MustCompile(`(?i)model_context_window_exceeded`),
	regexp.MustCompile(`(?i)context[_ ]length[_ ]exceeded`),
	regexp.MustCompile(`(?i)too many tokens`),
	regexp.MustCompile(`(?i)token limit exceeded`),
	regexp.MustCompile(`(?i)range of input length should be`),
}

func isContextOverflowError(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	for _, excluded := range []string{"rate limit", "too many requests", "throttling error", "service unavailable"} {
		if strings.Contains(strings.ToLower(message), excluded) {
			return false
		}
	}
	for _, pattern := range contextOverflowPatterns {
		if pattern.MatchString(message) {
			return true
		}
	}
	var httpErr *ProviderHTTPError
	return errors.As(err, &httpErr) && (httpErr.StatusCode == 400 || httpErr.StatusCode == 413) && strings.TrimSpace(httpErr.Body) == ""
}

func isContextOverflowMessage(message Message, contextWindow int) bool {
	if contextWindow <= 0 {
		return false
	}
	input := number(message.Usage["input"]) + number(message.Usage["cacheRead"])
	switch message.StopReason {
	case "stop":
		return input > contextWindow
	case "length":
		return number(message.Usage["output"]) == 0 && float64(input) >= float64(contextWindow)*0.99
	default:
		return false
	}
}

func (e *execution) snapshot() error {
	e.mu.Lock()
	entries := append([]map[string]any(nil), e.entries...)
	leaf := e.leafID
	e.mu.Unlock()
	return e.emit("session.snapshot", map[string]any{"entries": entries, "leafId": leaf})
}
func (e *execution) appendMessage(message Message, extra map[string]any) {
	payload := map[string]any{"message": message}
	for k, v := range extra {
		payload[k] = v
	}
	e.appendEntry("message", payload)
}
func (e *execution) appendEntry(kind string, payload map[string]any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	id := newID()
	entry := map[string]any{"type": kind, "id": id, "parentId": nil, "timestamp": time.Now().UTC().Format(time.RFC3339Nano)}
	if e.leafID != "" {
		entry["parentId"] = e.leafID
	}
	for k, v := range payload {
		entry[k] = v
	}
	e.entries = append(e.entries, entry)
	e.leafID = id
}

func (e *execution) branch() []map[string]any {
	e.mu.Lock()
	defer e.mu.Unlock()
	return branchEntries(append([]map[string]any(nil), e.entries...), e.leafID)
}
func (e *execution) requiresApproval(call ToolCall) bool {
	if call.Name == "subagent" {
		var input struct {
			Agent, AgentScope    string
			Tasks, Chain         []struct{ Agent string }
			ConfirmProjectAgents *bool
		}
		_ = json.Unmarshal(call.Arguments, &input)
		confirm := input.ConfirmProjectAgents == nil || *input.ConfirmProjectAgents
		if confirm && (input.AgentScope == "project" || input.AgentScope == "both") {
			if input.Agent != "" && !e.runtime.Resources.IsBundledAgentAt(input.Agent, e.command.CWD, input.AgentScope) {
				return true
			}
			for _, task := range append(input.Tasks, input.Chain...) {
				if !e.runtime.Resources.IsBundledAgentAt(task.Agent, e.command.CWD, input.AgentScope) {
					return true
				}
			}
		}
	}
	if call.Name != "bash" {
		e.mu.Lock()
		required := e.mcpApproval[call.Name]
		e.mu.Unlock()
		if required {
			return true
		}
	}
	if call.Name != "bash" {
		return false
	}
	var p struct{ Command string }
	_ = json.Unmarshal(call.Arguments, &p)
	for _, pattern := range []string{`(?i)\brm\s+(-rf?|--recursive)`, `(?i)\bsudo\b`, `(?i)\b(chmod|chown)\b.*777`} {
		if regexp.MustCompile(pattern).MatchString(p.Command) {
			return true
		}
	}
	return false
}
func (e *execution) pollControls(ctx context.Context, poll ControlPoller, done <-chan struct{}) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	seen := map[string]bool{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-ticker.C:
			controls, err := poll(ctx)
			if err != nil {
				continue
			}
			for _, control := range controls {
				if seen[control.ID] {
					continue
				}
				seen[control.ID] = true
				message := Message{Role: "user", Content: control.Content, Timestamp: nowMillis()}
				e.appendEntry("message", map[string]any{"message": message, "delivery": control.Action})
				e.mu.Lock()
				if control.Action == "steer" {
					e.steering = append(e.steering, message)
				} else {
					e.followUps = append(e.followUps, message)
				}
				e.mu.Unlock()
				_ = e.emit("control.accepted", map[string]any{"controlId": control.ID, "action": control.Action})
			}
		}
	}
}

func messagesFromEntries(entries []map[string]any) []Message {
	var out []Message
	start := 0
	compactionIndex := -1
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i]["type"] != "compaction" {
			continue
		}
		compactionIndex = i
		if summary, ok := entries[i]["summary"].(string); ok && summary != "" {
			out = append(out, Message{Role: "user", Content: compactionSummaryPrefix + summary + compactionSummarySuffix, Timestamp: entryTimestamp(entries[i])})
		}
		if rawTail, ok := entries[i]["retainedTail"]; ok {
			data, _ := json.Marshal(rawTail)
			var tail []Message
			if json.Unmarshal(data, &tail) == nil && len(tail) > 0 {
				out = append(out, tail...)
				start = i + 1
				break
			}
		}
		first, _ := entries[i]["firstKeptEntryId"].(string)
		for j := range entries {
			if entries[j]["id"] == first {
				start = j
				break
			}
		}
		break
	}
	for i, entry := range entries {
		if i < start || i == compactionIndex || entry["type"] == "compaction" {
			continue
		}
		if entry["type"] == "branch_summary" {
			if summary, ok := entry["summary"].(string); ok && summary != "" {
				out = append(out, Message{Role: "user", Content: branchSummaryPrefix + summary + branchSummarySuffix, Timestamp: entryTimestamp(entry)})
			}
			continue
		}
		if entry["type"] == "custom_message" {
			if content, ok := entry["content"].(string); ok {
				out = append(out, Message{Role: "user", Content: content, Timestamp: entryTimestamp(entry)})
			}
			continue
		}
		if entry["type"] != "message" {
			continue
		}
		raw, ok := entry["message"]
		if !ok {
			continue
		}
		data, _ := json.Marshal(raw)
		var message Message
		if json.Unmarshal(data, &message) == nil && message.Role != "" {
			out = append(out, message)
		}
	}
	return out
}

func entryTimestamp(entry map[string]any) int64 {
	if value, ok := entry["timestamp"].(string); ok {
		if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
			return parsed.UnixMilli()
		}
	}
	return 0
}

func branchEntries(entries []map[string]any, leaf string) []map[string]any {
	if leaf == "" {
		return entries
	}
	byID := map[string]map[string]any{}
	for _, entry := range entries {
		if id, ok := entry["id"].(string); ok {
			byID[id] = entry
		}
	}
	var reversed []map[string]any
	seen := map[string]bool{}
	for leaf != "" && !seen[leaf] {
		seen[leaf] = true
		entry := byID[leaf]
		if entry == nil {
			break
		}
		reversed = append(reversed, entry)
		parent, _ := entry["parentId"].(string)
		leaf = parent
	}
	out := make([]map[string]any, len(reversed))
	for i := range reversed {
		out[len(reversed)-1-i] = reversed[i]
	}
	return out
}
func restoreTodos(entries []map[string]any) ([]todo, int) {
	todos := []todo{}
	next := 1
	for _, entry := range entries {
		if entry["type"] != "message" || entry["toolName"] != "todo" {
			continue
		}
		details, ok := entry["details"].(map[string]any)
		if !ok {
			continue
		}
		data, _ := json.Marshal(details["todos"])
		_ = json.Unmarshal(data, &todos)
		if value, ok := details["nextId"].(float64); ok {
			next = int(value)
		} else if value, ok := details["nextId"].(int); ok {
			next = value
		}
	}
	return todos, next
}

type sessionSettings struct {
	Provider, Model, Thinking            string
	ActiveTools                          []string
	ActiveToolsSet                       bool
	SteeringMode, FollowUpMode           string
	PlanMode, PlanModeSet, PlanExecuting bool
}

func restoreSettings(entries []map[string]any) sessionSettings {
	var out sessionSettings
	for _, entry := range entries {
		switch entry["type"] {
		case "model_change":
			out.Provider, _ = entry["provider"].(string)
			out.Model, _ = entry["modelId"].(string)
		case "thinking_level_change":
			out.Thinking, _ = entry["thinkingLevel"].(string)
		case "active_tools_change":
			data, _ := json.Marshal(entry["activeToolNames"])
			_ = json.Unmarshal(data, &out.ActiveTools)
			out.ActiveToolsSet = true
		case "custom":
			if entry["customType"] == "active-tools" {
				data, _ := json.Marshal(entry["data"])
				_ = json.Unmarshal(data, &out.ActiveTools)
				out.ActiveToolsSet = true
			} else if entry["customType"] == "queue-modes" {
				if data, ok := entry["data"].(map[string]any); ok {
					out.SteeringMode, _ = data["steeringMode"].(string)
					out.FollowUpMode, _ = data["followUpMode"].(string)
				}
			} else if entry["customType"] == "plan-mode" {
				if data, ok := entry["data"].(map[string]any); ok {
					out.PlanMode, _ = data["enabled"].(bool)
					out.PlanExecuting, _ = data["executing"].(bool)
					out.PlanModeSet = true
				}
			}
		}
		if entry["type"] == "message" {
			data, _ := json.Marshal(entry["message"])
			var message Message
			if json.Unmarshal(data, &message) == nil && message.Role == "assistant" {
				out.Provider, out.Model = first(message.Provider, out.Provider), first(message.Model, out.Model)
			}
		}
	}
	return out
}

func (e *execution) effectivePlanMode() bool {
	if e.command.PlanModeSpecified {
		return e.command.PlanMode
	}
	settings := restoreSettings(e.branch())
	if settings.PlanModeSet {
		return settings.PlanMode
	}
	return e.command.PlanMode
}
func (e *execution) drainQueue(kind string) []Message {
	e.mu.Lock()
	defer e.mu.Unlock()
	queue := &e.steering
	mode := first(e.command.SteeringMode, "one-at-a-time")
	if kind != "steer" {
		queue = &e.followUps
		mode = first(e.command.FollowUpMode, "one-at-a-time")
	}
	if len(*queue) == 0 {
		return nil
	}
	count := len(*queue)
	if mode != "all" {
		count = 1
	}
	messages := append([]Message(nil), (*queue)[:count]...)
	*queue = (*queue)[count:]
	return messages
}

func (e *execution) injectQueuedMessages(kind, agentID string, depth int) []Message {
	messages := e.drainQueue(kind)
	for _, message := range messages {
		_ = e.emit("agent.message_start", map[string]any{"agentId": agentID, "depth": depth, "event": map[string]any{"type": "message_start", "message": message}})
		if depth == 0 {
			e.appendMessage(message, map[string]any{"queue": kind})
		}
		_ = e.emit("agent.message_end", map[string]any{"agentId": agentID, "depth": depth, "event": map[string]any{"type": "message_end", "message": message}})
	}
	return messages
}
func findTool(tools []ToolDefinition, name string) *ToolDefinition {
	for i := range tools {
		if tools[i].Name == name {
			return &tools[i]
		}
	}
	return nil
}
func argumentHash(name string, args json.RawMessage) string {
	var value any
	_ = json.Unmarshal(args, &value)
	normalized, _ := json.Marshal(stable(map[string]any{"toolName": name, "input": value}))
	sum := sha256.Sum256(normalized)
	return hex.EncodeToString(sum[:])
}
func stable(v any) any {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make(map[string]any, len(x))
		for _, k := range keys {
			out[k] = stable(x[k])
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = stable(x[i])
		}
		return out
	default:
		return v
	}
}
func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func nowMillis() int64 { return time.Now().UnixMilli() }
func set(values []string) map[string]bool {
	out := map[string]bool{}
	for _, v := range values {
		out[v] = true
	}
	return out
}
func planTools(names []string) []string {
	disabled := map[string]bool{"edit": true, "write": true, "generate_image": true}
	out := []string{}
	for _, n := range names {
		if !disabled[n] {
			out = append(out, n)
		}
	}
	for _, n := range []string{"read", "bash", "grep", "find", "ls", "questionnaire"} {
		if !contains(out, n) {
			out = append(out, n)
		}
	}
	return out
}
func expandTools(names, mcp []string) []string {
	var out []string
	for _, n := range names {
		if n == "mcp:*" {
			out = append(out, mcp...)
		} else {
			out = append(out, n)
		}
	}
	return out
}
func contains(values []string, target string) bool {
	for _, v := range values {
		if v == target {
			return true
		}
	}
	return false
}
func inside(root, target string) bool {
	root, _ = filepath.Abs(root)
	target, _ = filepath.Abs(target)
	rel, err := filepath.Rel(root, target)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
func definitionNames(tools []ToolDefinition) []string {
	out := make([]string, len(tools))
	for i, t := range tools {
		out[i] = t.Name
	}
	return out
}
func extractPlan(text string) []map[string]any {
	header := regexp.MustCompile(`(?i)\*{0,2}Plan:\*{0,2}\s*\n`).FindStringIndex(text)
	if header == nil {
		return nil
	}
	text = text[header[1]:]
	re := regexp.MustCompile(`(?m)^\s*(\d+)[.)]\s+\*{0,2}([^*\n]+)`)
	matches := re.FindAllStringSubmatch(text, -1)
	out := make([]map[string]any, 0, len(matches))
	for _, m := range matches {
		value := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(m[2]), "**"))
		if len(value) <= 5 || strings.HasPrefix(value, "`") || strings.HasPrefix(value, "/") || strings.HasPrefix(value, "-") {
			continue
		}
		value = cleanPlanStep(value)
		if len(value) <= 3 {
			continue
		}
		out = append(out, map[string]any{"id": len(out) + 1, "step": len(out) + 1, "text": value, "status": "pending", "completed": false})
	}
	return out
}

func cleanPlanStep(value string) string {
	value = regexp.MustCompile(`\*{1,2}([^*]+)\*{1,2}`).ReplaceAllString(value, "$1")
	value = regexp.MustCompile("`([^`]+)`").ReplaceAllString(value, "$1")
	value = regexp.MustCompile(`(?i)^(Use|Run|Execute|Create|Write|Read|Check|Verify|Update|Modify|Add|Remove|Delete|Install)\s+(the\s+)?`).ReplaceAllString(value, "")
	value = strings.Join(strings.Fields(value), " ")
	if value != "" {
		runes := []rune(value)
		runes[0] = []rune(strings.ToUpper(string(runes[0])))[0]
		if len(runes) > 50 {
			value = string(runes[:47]) + "..."
		} else {
			value = string(runes)
		}
	}
	return value
}
func extractDone(text string) []int {
	re := regexp.MustCompile(`(?i)\[DONE:(\d+)\]`)
	var out []int
	for _, m := range re.FindAllStringSubmatch(text, -1) {
		var n int
		_, _ = fmt.Sscan(m[1], &n)
		out = append(out, n)
	}
	return out
}
