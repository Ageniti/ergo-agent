package engine

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type fakeFactory struct{ provider Provider }

func (f fakeFactory) Provider(string, int) (Provider, error) { return f.provider, nil }

type captureFactory struct {
	provider Provider
	name     string
}

func (f *captureFactory) Provider(name string, _ int) (Provider, error) {
	f.name = name
	return f.provider, nil
}

type modelAwareCaptureFactory struct {
	provider Provider
	models   []string
}

func (f *modelAwareCaptureFactory) Provider(string, int) (Provider, error) {
	return f.provider, nil
}

func (f *modelAwareCaptureFactory) ProviderForModel(_, model string, _ int) (Provider, error) {
	f.models = append(f.models, model)
	return f.provider, nil
}

type fakeProvider struct {
	mu       sync.Mutex
	calls    int
	requests []CompletionRequest
}

type captureProvider struct{ request CompletionRequest }

func (p *captureProvider) Complete(_ context.Context, request CompletionRequest) (Completion, error) {
	p.request = request
	return Completion{Text: "resumed"}, nil
}

type queuedProvider struct {
	requests  []CompletionRequest
	responses []Completion
}

type failingProvider struct{ err error }

func (p failingProvider) Complete(context.Context, CompletionRequest) (Completion, error) {
	return Completion{}, p.err
}

type retryingStreamProvider struct{ calls int }

func (p *retryingStreamProvider) Complete(context.Context, CompletionRequest) (Completion, error) {
	return Completion{}, errors.New("stream expected")
}
func (p *retryingStreamProvider) Stream(_ context.Context, _ CompletionRequest, delta func(CompletionDelta) error) (Completion, error) {
	p.calls++
	if p.calls == 1 {
		_ = delta(CompletionDelta{Text: "stale"})
		return Completion{}, &ProviderHTTPError{StatusCode: 500, Status: "500", Body: "retry"}
	}
	_ = delta(CompletionDelta{Text: "fresh"})
	return Completion{Text: "fresh", StopReason: "stop"}, nil
}

func (p *queuedProvider) Complete(_ context.Context, request CompletionRequest) (Completion, error) {
	p.requests = append(p.requests, request)
	if len(p.responses) == 0 {
		return Completion{}, errors.New("no queued completion")
	}
	response := p.responses[0]
	p.responses = p.responses[1:]
	return response, nil
}

func (f *fakeProvider) Complete(_ context.Context, request CompletionRequest) (Completion, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, request)
	f.calls++
	if f.calls == 1 {
		return Completion{ToolCalls: []ToolCall{{ID: "todo-1", Name: "todo", Arguments: json.RawMessage(`{"action":"add","text":"ship Go runtime"}`)}}}, nil
	}
	return Completion{Text: "done", Usage: map[string]any{"input_tokens": 10}}, nil
}

func TestNativeLoopUsesLocalPromptAndTool(t *testing.T) {
	root := filepath.Clean("../..")
	provider := &fakeProvider{}
	runtime := New(root)
	runtime.Providers = fakeFactory{provider}
	var events []Event
	payload := map[string]any{"runId": "run-1", "sessionId": "session-1", "agentId": "coding-agent", "prompt": "test", "cwd": t.TempDir(), "provider": "fake", "model": "fake-model", "operation": "prompt"}
	err := runtime.Run(context.Background(), payload, nil, func(event Event) error { events = append(events, event); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if provider.calls != 2 {
		t.Fatalf("calls=%d", provider.calls)
	}
	if !strings.HasPrefix(provider.requests[0].System, "You are Ergo, an expert coding assistant operating inside the Ergo Agent Runtime") {
		t.Fatalf("official prompt missing: %q", provider.requests[0].System)
	}
	if !hasTool(provider.requests[0].Tools, "todo") {
		t.Fatal("todo tool missing")
	}
	if events[len(events)-1].Type != "agent.session_shutdown" {
		t.Fatalf("last event=%s", events[len(events)-1].Type)
	}
	order := []string{"agent.agent_start", "agent.turn_start", "agent.message_start", "agent.message_end"}
	next := 0
	for _, event := range events {
		if next < len(order) && event.Type == order[next] {
			next++
		}
	}
	if next != len(order) {
		t.Fatalf("Pi lifecycle order not observed: matched %d/%d", next, len(order))
	}
	foundTodo := false
	foundCompleted := false
	for _, event := range events {
		if event.Type == "todo.updated" {
			foundTodo = true
		}
		if event.Type == "run.completed" {
			foundCompleted = true
		}
	}
	if !foundTodo {
		t.Fatal("todo.updated missing")
	}
	if !foundCompleted {
		t.Fatal("run.completed missing")
	}
}

type truncatedToolProvider struct {
	calls int
	seen  []Message
}

func (p *truncatedToolProvider) Complete(_ context.Context, request CompletionRequest) (Completion, error) {
	p.calls++
	if p.calls == 1 {
		return Completion{StopReason: "length", ToolCalls: []ToolCall{{ID: "write-1", Name: "write", Arguments: json.RawMessage(`{"path":"must-not-exist","content":"bad"}`)}}}, nil
	}
	p.seen = append([]Message(nil), request.Messages...)
	return Completion{Text: "recovered", StopReason: "stop"}, nil
}

func TestPiHarnessDoesNotExecuteLengthTruncatedToolCalls(t *testing.T) {
	root := filepath.Clean("../..")
	workspace := t.TempDir()
	provider := &truncatedToolProvider{}
	runtime := New(root)
	runtime.Providers = fakeFactory{provider}
	payload := map[string]any{"runId": "truncated", "sessionId": "session", "agentId": "coding-agent", "prompt": "test", "cwd": workspace, "provider": "fake", "model": "fake", "operation": "prompt"}
	if err := runtime.Run(context.Background(), payload, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "must-not-exist")); !os.IsNotExist(err) {
		t.Fatalf("truncated tool call executed: %v", err)
	}
	if len(provider.seen) == 0 || provider.seen[len(provider.seen)-1].Role != "tool" || !strings.Contains(provider.seen[len(provider.seen)-1].Content, "was not executed") {
		t.Fatalf("tool error not returned to model: %+v", provider.seen)
	}
}

func TestPiHarnessProviderFailureCompletesAssistantEventSequence(t *testing.T) {
	runtime := New(filepath.Clean("../.."))
	runtime.Providers = fakeFactory{failingProvider{err: errors.New("provider unavailable")}}
	var events []Event
	payload := map[string]any{"runId": "failed", "sessionId": "session", "agentId": "coding-agent", "prompt": "test", "cwd": t.TempDir(), "provider": "fake", "model": "fake", "operation": "prompt", "retryEnabled": false}
	if err := runtime.Run(context.Background(), payload, nil, func(event Event) error { events = append(events, event); return nil }); err != nil {
		t.Fatal(err)
	}
	foundErrorMessage, foundTurnEnd, foundAgentEnd := false, false, false
	for _, event := range events {
		if event.Type == "agent.message_end" {
			encoded, _ := json.Marshal(event.Payload)
			foundErrorMessage = foundErrorMessage || strings.Contains(string(encoded), `"stop_reason":"error"`)
		}
		foundTurnEnd = foundTurnEnd || event.Type == "agent.turn_end"
		foundAgentEnd = foundAgentEnd || event.Type == "agent.agent_end"
	}
	if !foundErrorMessage || !foundTurnEnd || !foundAgentEnd {
		t.Fatalf("error sequence message=%v turn=%v agent=%v", foundErrorMessage, foundTurnEnd, foundAgentEnd)
	}
}

func TestStreamingRetryClearsFailedPartialContent(t *testing.T) {
	provider := &retryingStreamProvider{}
	runtime := New(filepath.Clean("../.."))
	runtime.Providers = fakeFactory{provider}
	var final Message
	payload := map[string]any{
		"runId": "retry-stream", "sessionId": "session", "agentId": "coding-agent",
		"prompt": "test", "cwd": t.TempDir(), "provider": "fake", "model": "fake",
		"operation": "prompt", "retryBaseDelayMs": 1,
	}
	if err := runtime.Run(context.Background(), payload, nil, func(event Event) error {
		if event.Type == "agent.message_end" {
			if nested, ok := event.Payload["event"].(map[string]any); ok {
				if message, ok := nested["message"].(Message); ok && message.Role == "assistant" {
					final = message
				}
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if provider.calls != 2 || final.Content != "fresh" || strings.Contains(final.Content, "stale") {
		t.Fatalf("calls=%d final=%+v", provider.calls, final)
	}
}

func TestPiHarnessSplitTurnCompactionUsesTwoSummariesAndRetainedTail(t *testing.T) {
	entries := []map[string]any{
		{"id": "u1", "parentId": nil, "type": "message", "message": Message{Role: "user", Content: "old request"}},
		{"id": "a1", "parentId": "u1", "type": "message", "message": Message{Role: "assistant", Content: strings.Repeat("old work", 20)}},
		{"id": "u2", "parentId": "a1", "type": "message", "message": Message{Role: "user", Content: "large request"}},
		{"id": "a2", "parentId": "u2", "type": "message", "message": Message{Role: "assistant", Content: strings.Repeat("recent work", 20)}},
	}
	preparation := prepareCompaction(entries, 1)
	if preparation == nil || !preparation.SplitTurn || len(preparation.TurnPrefix) == 0 || len(preparation.RetainedTail) == 0 {
		t.Fatalf("preparation=%+v", preparation)
	}
	provider := &queuedProvider{responses: []Completion{{Text: "history", Usage: map[string]any{"input": 1, "totalTokens": 1}}, {Text: "prefix", Usage: map[string]any{"input": 2, "totalTokens": 2}}}}
	execution := &execution{runtime: New(t.TempDir()), ctx: context.Background(), command: Command{SessionID: "session", CompactionKeepRecent: 1, CompactionReserve: 100}, sink: func(Event) error { return nil }, entries: entries, leafID: "a2", approvedCalls: map[string]bool{}, approvedHashes: map[string]bool{}, deniedHashes: map[string]bool{}, mcpApproval: map[string]bool{}}
	compacted, err := execution.compact(context.Background(), provider, "model", "off", "")
	if err != nil || !compacted || len(provider.requests) != 2 {
		t.Fatalf("compacted=%v requests=%d err=%v", compacted, len(provider.requests), err)
	}
	branch := execution.branch()
	last := branch[len(branch)-1]
	if !strings.Contains(last["summary"].(string), "**Turn Context (split turn):**") || usageTokens(last["usage"].(map[string]any)) != 3 {
		t.Fatalf("entry=%+v", last)
	}
	messages := messagesFromEntries(branch)
	if len(messages) < 2 || !strings.Contains(messages[0].Content, "history") {
		t.Fatalf("messages=%+v", messages)
	}
}

func TestProjectRoleScopeAndBuiltInLock(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "agents"), 0755); err != nil {
		t.Fatal(err)
	}
	official := "---\nname: worker\ndescription: bundled worker\nrole: meta\ntools: read, bash, edit, write\n---\nofficial"
	if err := os.WriteFile(filepath.Join(root, "agents", "worker.md"), []byte(official), 0644); err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, ".pi", "agents"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, ".pi", "agents", "arbitrary-filename.md"), []byte("---\nname: custom\ndescription: Project specialist\ntools: read\n---\nproject role"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, ".pi", "agents", "worker.md"), []byte("---\nname: worker\ndescription: override\nrole: main\n---\noverride"), 0644); err != nil {
		t.Fatal(err)
	}
	resources := Resources{Root: root}
	custom, err := resources.AgentAt("custom", project, "project")
	if err != nil || custom.Body != "project role" {
		t.Fatalf("custom=%+v err=%v", custom, err)
	}
	worker, err := resources.AgentAt("worker", project, "both")
	if err != nil || worker.Body != "official" {
		t.Fatalf("worker=%+v err=%v", worker, err)
	}
}

func TestPiFrontmatterUsesYAMLAndNormalizesBody(t *testing.T) {
	meta, body := frontmatter("---\r\nname: 'quoted-agent'\r\ndescription: |-\r\n  First line\r\n  Second line\r\ndisable-model-invocation: true\r\n---\r\n\r\n  Instructions  \r\n")
	if meta["name"] != "quoted-agent" || meta["description"] != "First line\nSecond line" || meta["disable-model-invocation"] != "true" || body != "Instructions" {
		t.Fatalf("meta=%+v body=%q", meta, body)
	}
}

func TestCustomProjectSubagentRequiresPermissionByDefault(t *testing.T) {
	execution := &execution{runtime: New(filepath.Clean("../..")), mcpApproval: map[string]bool{}}
	custom := ToolCall{Name: "subagent", Arguments: json.RawMessage(`{"agent":"domain-expert","task":"work","agentScope":"project"}`)}
	if !execution.requiresApproval(custom) {
		t.Fatal("custom project agent did not require approval")
	}
	builtin := ToolCall{Name: "subagent", Arguments: json.RawMessage(`{"agent":"coding-agent","task":"work","agentScope":"both"}`)}
	if execution.requiresApproval(builtin) {
		t.Fatal("locked built-in role unexpectedly required project-role approval")
	}
	optOut := ToolCall{Name: "subagent", Arguments: json.RawMessage(`{"agent":"domain-expert","task":"work","agentScope":"project","confirmProjectAgents":false}`)}
	if execution.requiresApproval(optOut) {
		t.Fatal("explicit trusted-project opt-out was ignored")
	}
}

func TestPiSubagentParallelPreservesFailuresAndChainStopsWithDetails(t *testing.T) {
	tools := &toolset{cwd: t.TempDir(), subagent: func(_ context.Context, agent, task, _, _ string) (string, error) {
		if agent == "bad" {
			return "", fmt.Errorf("failed %s", task)
		}
		return agent + ":" + task, nil
	}}
	parallel, err := tools.runSubagent(context.Background(), json.RawMessage(`{"tasks":[{"agent":"one","task":"a"},{"agent":"bad","task":"b"},{"agent":"three","task":"c"}]}`))
	if err != nil || !strings.Contains(parallel.Text, "Parallel: 2/3 succeeded") || !strings.Contains(parallel.Text, "### [bad] failed") || !strings.Contains(parallel.Text, "three:c") {
		t.Fatalf("parallel=%+v err=%v", parallel, err)
	}
	detailJSON, _ := json.Marshal(parallel.Details)
	if !strings.Contains(string(detailJSON), `"error":"failed b"`) || !strings.Contains(string(detailJSON), `"output":"one:a"`) {
		t.Fatalf("parallel details=%s", detailJSON)
	}
	chain, err := tools.runSubagent(context.Background(), json.RawMessage(`{"chain":[{"agent":"one","task":"a"},{"agent":"bad","task":"{previous}"},{"agent":"three","task":"never"}]}`))
	if err == nil || chain.Text != "Chain stopped at step 2 (bad): failed one:a" {
		t.Fatalf("chain=%+v err=%v", chain, err)
	}
	detailJSON, _ = json.Marshal(chain.Details)
	if strings.Contains(string(detailJSON), "never") || !strings.Contains(string(detailJSON), `"step":2`) {
		t.Fatalf("chain details=%s", detailJSON)
	}
}

func TestPlanModeBlocksMutation(t *testing.T) {
	tools := &toolset{cwd: t.TempDir(), planMode: true}
	_, err := tools.bash(context.Background(), json.RawMessage(`{"command":"rm -rf output"}`))
	if err == nil || !strings.Contains(err.Error(), "plan mode") {
		t.Fatalf("err=%v", err)
	}
	result, err := tools.bash(context.Background(), json.RawMessage(`{"command":"pwd"}`))
	if err != nil || strings.TrimSpace(result.Text) == "" {
		t.Fatalf("result=%q err=%v", result.Text, err)
	}
}

func TestPlanModeMatchesOfficialDestructiveCommandRules(t *testing.T) {
	for _, command := range []string{"git branch -D old", "systemctl restart mysql", "service nginx stop", "vim README.md", "ls; rm output"} {
		if safePlanCommand(command) {
			t.Fatalf("unsafe command accepted: %s", command)
		}
	}
	for _, command := range []string{"git branch", "git config --get user.name", "sed -n '1,5p' README.md", "wget -O - https://example.com"} {
		if !safePlanCommand(command) {
			t.Fatalf("read-only command rejected: %s", command)
		}
	}
}

func TestPlanModeRestoresFromSessionAndExplicitInputWins(t *testing.T) {
	entries := []map[string]any{{"type": "custom", "customType": "plan-mode", "data": map[string]any{"enabled": true, "executing": false}}}
	settings := restoreSettings(entries)
	if !settings.PlanModeSet || !settings.PlanMode || settings.PlanExecuting {
		t.Fatalf("settings=%+v", settings)
	}
	execution := &execution{command: Command{}, entries: entries}
	if !execution.effectivePlanMode() {
		t.Fatal("stored plan mode was not restored")
	}
	execution.command.PlanModeSpecified = true
	execution.command.PlanMode = false
	if execution.effectivePlanMode() {
		t.Fatal("explicit plan mode did not override stored state")
	}
}

func TestOfficialPlanAndExecutionPromptsAreInjectedAsContextMessages(t *testing.T) {
	resources := Resources{Root: filepath.Clean("../..")}
	plan, err := resources.ModePrompt(true, false, "analyze")
	if err != nil || !strings.HasPrefix(plan, "[PLAN MODE ACTIVE]") || !strings.Contains(plan, "Do NOT attempt to make changes") {
		t.Fatalf("plan=%q err=%v", plan, err)
	}
	execute, err := resources.ModePrompt(false, true, "Execute\n\nPlan:\n2. implement\n3. test")
	if err != nil || !strings.Contains(execute, "Remaining steps:\n2. implement\n3. test") || strings.Contains(execute, "{{REMAINING_STEPS}}") {
		t.Fatalf("execute=%q err=%v", execute, err)
	}
}

func TestPiHarnessSummaryMessagesUseExactWrappers(t *testing.T) {
	entries := []map[string]any{
		{"id": "compact", "type": "compaction", "summary": "old work", "firstKeptEntryId": "kept"},
		{"id": "kept", "type": "message", "message": Message{Role: "user", Content: "continue"}},
		{"id": "branch", "type": "branch_summary", "summary": "side work"},
	}
	messages := messagesFromEntries(entries)
	if len(messages) != 3 {
		t.Fatalf("messages=%+v", messages)
	}
	if messages[0].Content != "The conversation history before this point was compacted into the following summary:\n\n<summary>\nold work\n</summary>" {
		t.Fatalf("compaction wrapper=%q", messages[0].Content)
	}
	if messages[2].Content != "The following is a summary of a branch that this conversation came back from:\n\n<summary>\nside work</summary>" {
		t.Fatalf("branch wrapper=%q", messages[2].Content)
	}
}

func TestPiHarnessSkillsPromptIsExact(t *testing.T) {
	got := formatSkillsForSystemPrompt([]Skill{{Name: "visible", Description: "Use <this> & that", Path: "/skills/visible/SKILL.md"}, {Name: "hidden", Description: "hidden", Path: "/hidden/SKILL.md", DisableModelInvocation: true}})
	want := `The following skills provide specialized instructions for specific tasks.
Read the full skill file when the task matches its description.
When a skill file references a relative path, resolve it against the absolute skill directory shown in base_dir.
Replace every literal {baseDir} placeholder in skill instructions with that base_dir before executing commands.

<available_skills>
  <skill>
    <name>visible</name>
    <description>Use &lt;this&gt; &amp; that</description>
    <location>/skills/visible/SKILL.md</location>
    <base_dir>/skills/visible</base_dir>
  </skill>
</available_skills>`
	if got != want {
		t.Fatalf("skills prompt mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestSkillOperationExpandsBaseDirPlaceholder(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "skills", "node-helper")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: node-helper\ndescription: Run a Node helper\n---\nRun `node {baseDir}/search.js`."
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	command := Command{Operation: "skill", SkillName: "node-helper", CWD: t.TempDir()}
	runtime := New(root)
	if err := runtime.expandPromptOperation(&command, true, nil); err != nil {
		t.Fatal(err)
	}
	if command.Operation != "prompt" {
		t.Fatalf("operation=%q", command.Operation)
	}
	if strings.Contains(command.Prompt, "{baseDir}/search.js") || !strings.Contains(command.Prompt, "node "+skillDir+"/search.js") {
		t.Fatalf("baseDir was not expanded in prompt: %s", command.Prompt)
	}
	if !strings.Contains(command.Prompt, `base_dir="`+skillDir+`"`) {
		t.Fatalf("base_dir metadata missing from prompt: %s", command.Prompt)
	}
}

func TestPiHarnessToolValidationAndLegacyEditPreparation(t *testing.T) {
	tools := (&toolset{cwd: t.TempDir()}).definitions([]string{"read", "edit"})
	read := findTool(tools, "read")
	_, err := validateToolCall(read, ToolCall{Name: "read", Arguments: json.RawMessage(`{"offset":1,"extra":true}`)})
	if err == nil || !strings.Contains(err.Error(), "$.path: required property is missing") || strings.Contains(err.Error(), "$.extra: unexpected property") {
		t.Fatalf("validation error=%v", err)
	}
	prepared, err := validateToolCall(read, ToolCall{Name: "read", Arguments: json.RawMessage(`{"path":"x","extra":true}`)})
	if err != nil || !strings.Contains(string(prepared.Arguments), `"extra":true`) {
		t.Fatalf("Pi Type.Object additional-property behavior: prepared=%s err=%v", prepared.Arguments, err)
	}
	edit := findTool(tools, "edit")
	prepared, err = validateToolCall(edit, ToolCall{Name: "edit", Arguments: json.RawMessage(`{"path":"x","oldText":"a","newText":"b"}`)})
	if err != nil || !strings.Contains(string(prepared.Arguments), `"edits":[{"newText":"b","oldText":"a"}]`) {
		t.Fatalf("prepared=%s err=%v", prepared.Arguments, err)
	}
	coercionTool := ToolDefinition{Name: "coerce", Parameters: map[string]any{"type": "object", "properties": map[string]any{
		"count": map[string]any{"type": "integer", "minimum": 1},
		"flag":  map[string]any{"type": "boolean"},
		"mode":  map[string]any{"anyOf": []any{map[string]any{"const": "fast"}, map[string]any{"const": "safe"}}},
	}, "required": []string{"count", "flag", "mode"}, "additionalProperties": false}}
	prepared, err = validateToolCall(&coercionTool, ToolCall{Name: "coerce", Arguments: json.RawMessage(`{"count":"42","flag":"true","mode":"safe"}`)})
	if err != nil || string(prepared.Arguments) != `{"count":42,"flag":true,"mode":"safe"}` {
		t.Fatalf("coerced=%s err=%v", prepared.Arguments, err)
	}
	_, err = validateToolCall(&coercionTool, ToolCall{Name: "coerce", Arguments: json.RawMessage(`{"count":0,"flag":"1","mode":"other"}`)})
	if err == nil || !strings.Contains(err.Error(), "expected number >= 1") || !strings.Contains(err.Error(), "expected boolean") || !strings.Contains(err.Error(), "at least one schema") {
		t.Fatalf("constraint validation error=%v", err)
	}
}

func TestPiBuiltInToolEdgeCompatibility(t *testing.T) {
	root := t.TempDir()
	toolset := &toolset{cwd: root}
	tools := toolset.definitions([]string{"read", "write", "find", "ls"})
	find := findTool(tools, "find")
	if find.Description != "Search for files by glob pattern. Returns matching file paths relative to the search directory. Respects .gitignore. Output is truncated to 1000 results or 50KB (whichever is hit first)." {
		t.Fatalf("find description=%q", find.Description)
	}
	ls := findTool(tools, "ls")
	if ls.Description != "List directory contents. Returns entries sorted alphabetically, with '/' suffix for directories. Includes dotfiles. Output is truncated to 500 entries or 50KB (whichever is hit first)." {
		t.Fatalf("ls description=%q", ls.Description)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := findTool(tools, "read").Execute(ctx, json.RawMessage(`{"path":"missing"}`))
	if err == nil || err.Error() != "Operation aborted" {
		t.Fatalf("canceled read error=%v", err)
	}
	result, err := findTool(tools, "write").Execute(context.Background(), json.RawMessage(`{"path":"unicode.txt","content":"é😀"}`))
	if err != nil || result.Text != "Successfully wrote 3 bytes to unicode.txt" {
		t.Fatalf("write result=%q err=%v", result.Text, err)
	}
	_, err = ls.Execute(context.Background(), json.RawMessage(`{"path":"unicode.txt"}`))
	if err == nil || err.Error() != "Not a directory: "+filepath.Join(root, "unicode.txt") {
		t.Fatalf("ls file error=%v", err)
	}
	_, err = find.Execute(context.Background(), json.RawMessage(`{"pattern":"*","path":"missing"}`))
	if err == nil || err.Error() != "Path not found: "+filepath.Join(root, "missing") {
		t.Fatalf("find missing error=%v", err)
	}
}

func TestPiCodingSystemPromptGolden(t *testing.T) {
	root := t.TempDir()
	config := t.TempDir()
	t.Setenv("AGENT_CONFIG_DIR", config)
	if err := os.MkdirAll(filepath.Join(root, "prompts", "system"), 0755); err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile(filepath.Join("..", "..", "prompts", "system", "coding-agent.md"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "prompts", "system", "coding-agent.md"), source, 0644); err != nil {
		t.Fatal(err)
	}
	cwd := filepath.Join(root, "workspace")
	if err := os.MkdirAll(cwd, 0755); err != nil {
		t.Fatal(err)
	}
	tools := (&toolset{cwd: cwd}).definitions([]string{"read", "bash", "edit", "write"})
	got, err := (Resources{Root: root}).BuildSystemPrompt(AgentDefinition{Name: "coding-agent", SystemPrompt: "prompts/system/coding-agent.md"}, cwd, tools, false)
	if err != nil {
		t.Fatal(err)
	}
	want := "You are Ergo, an expert coding assistant operating inside the Ergo Agent Runtime. You help users by reading files, executing commands, editing code, and writing new files.\n\n" +
		"Available tools:\n- read: Read file contents\n- bash: Execute bash commands (ls, grep, find, etc.)\n- edit: Make precise file edits with exact text replacement, including multiple disjoint edits in one call\n- write: Create or overwrite files\n\n" +
		"In addition to the tools above, you may have access to other custom tools depending on the project.\n\nGuidelines:\n" +
		"- Use bash for file operations like ls, rg, find\n- Use read to examine files instead of cat or sed.\n- Use edit for precise changes (edits[].oldText must match exactly)\n- When changing multiple separate locations in one file, use one edit call with multiple entries in edits[] instead of multiple edit calls\n- Each edits[].oldText is matched against the original file, not after earlier edits are applied. Do not emit overlapping or nested edits. Merge nearby changes into one edit.\n- Keep edits[].oldText as small as possible while still being unique in the file. Do not pad with large unchanged regions.\n- Use write only for new files or complete rewrites.\n- Be concise in your responses\n- Show file paths clearly when working with files\n\n" +
		"Ergo documentation (read only when the user asks about Ergo itself, its SDK, architecture, extensions, skills, packages, or Pi compatibility):\n" +
		"- Main documentation: " + filepath.Join(root, "docs", "PI-PARITY.md") + "\n- Additional docs: " + filepath.Join(root, "docs") + "\n- Examples: " + filepath.Join(root, "docs") + " (extensions, custom tools, SDK)\n" +
		"- When reading Ergo docs or examples, resolve docs/... under Additional docs and examples/... under Examples, not the current working directory\n- Relevant documents include ARCHITECTURE.md, AGENT-PACKAGES.md, STANDALONE-AGENTS.md, PROMPT-TEMPLATES.md, SECURITY.md, CONFORMANCE.md, and PI-PARITY.md\n- When working on Ergo topics, read the relevant docs and examples and follow Markdown cross-references before implementing\n- Always read the relevant Ergo Markdown files completely before making SDK or Runtime changes\nCurrent working directory: " + filepath.ToSlash(cwd) + "\n"
	if got != want {
		t.Fatalf("system prompt mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestAgentProfilePromptUsesPiAppendOrder(t *testing.T) {
	root := t.TempDir()
	promptDir := filepath.Join(root, "prompts", "system")
	if err := os.MkdirAll(promptDir, 0755); err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile(filepath.Join("..", "..", "prompts", "system", "coding-agent.md"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(promptDir, "coding-agent.md"), source, 0644); err != nil {
		t.Fatal(err)
	}
	skillDir := filepath.Join(root, "skills", "test-skill")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}
	skill := "---\nname: test-skill\ndescription: test skill\n---\nInstructions"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skill), 0644); err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte("project instructions"), 0644); err != nil {
		t.Fatal(err)
	}
	definition := Agent{
		Name:         "specialist",
		SystemPrompt: "prompts/system/coding-agent.md",
		Body:         "SPECIALIST ROLE PROMPT",
	}
	tools := (&toolset{cwd: cwd}).definitions([]string{"read"})
	prompt, err := (Resources{Root: root}).BuildSystemPrompt(definition, cwd, tools, false)
	if err != nil {
		t.Fatal(err)
	}
	roleIndex := strings.Index(prompt, "SPECIALIST ROLE PROMPT")
	projectIndex := strings.Index(prompt, "<project_context>")
	skillsIndex := strings.Index(prompt, "<available_skills>")
	cwdIndex := strings.Index(prompt, "Current working directory:")
	if roleIndex < 0 || projectIndex < 0 || skillsIndex < 0 || cwdIndex < 0 {
		t.Fatalf("missing prompt sections: role=%d project=%d skills=%d cwd=%d", roleIndex, projectIndex, skillsIndex, cwdIndex)
	}
	if !(roleIndex < projectIndex && projectIndex < skillsIndex && skillsIndex < cwdIndex) {
		t.Fatalf("Pi append order changed: role=%d project=%d skills=%d cwd=%d", roleIndex, projectIndex, skillsIndex, cwdIndex)
	}
}

func TestMCPElicitationWaitsForOriginalInteractionResponse(t *testing.T) {
	var requested string
	execution := &execution{runtime: New(t.TempDir()), ctx: context.Background(), command: Command{RunID: "run", SessionID: "session", AgentID: "agent"}, sink: func(event Event) error {
		if event.Type == "input.requested" {
			requested, _ = event.Payload["interactionId"].(string)
		}
		return nil
	}, interactionPoller: func(_ context.Context, id string) (InteractionReply, error) {
		if id == requested && id != "" {
			return InteractionReply{Ready: true, Response: map[string]any{"action": "accept", "content": map[string]any{"answer": "yes"}}}, nil
		}
		return InteractionReply{}, nil
	}}
	result, err := execution.waitForMCPElicitation(context.Background(), &mcp.ElicitParams{Message: "continue?"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != "accept" || result.Content["answer"] != "yes" || requested == "" {
		t.Fatalf("result=%+v requested=%q", result, requested)
	}
}

func TestMCPElicitationIDIsStableAcrossECSReplay(t *testing.T) {
	requestA := map[string]any{"message": "choose", "requestedSchema": map[string]any{"type": "object", "properties": map[string]any{"x": map[string]any{"type": "string"}}}}
	requestB := map[string]any{"requestedSchema": map[string]any{"properties": map[string]any{"x": map[string]any{"type": "string"}}, "type": "object"}, "message": "choose"}
	first := stableElicitationID("run-1", 1, requestA)
	if first != stableElicitationID("run-1", 1, requestB) {
		t.Fatal("map ordering changed stable interaction ID")
	}
	if first == stableElicitationID("run-1", 2, requestA) || first == stableElicitationID("run-2", 1, requestA) {
		t.Fatal("interaction ID does not distinguish run/call sequence")
	}
}

func TestDurableCheckpointResumesPendingToolWithoutRegeneratingIt(t *testing.T) {
	root := filepath.Clean("../..")
	provider := &captureProvider{}
	runtime := New(root)
	runtime.Providers = fakeFactory{provider}
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "state.txt"), []byte("checkpoint"), 0644); err != nil {
		t.Fatal(err)
	}
	assistant := Message{Role: "assistant", ToolCalls: []ToolCall{{ID: "read-1", Name: "read", Arguments: json.RawMessage(`{"path":"state.txt"}`)}}}
	entries := []map[string]any{{"type": "message", "id": "assistant-entry", "parentId": nil, "message": assistant}}
	payload := map[string]any{"runId": "run", "sessionId": "session", "agentId": "coding-agent", "prompt": "must not be appended again", "cwd": workspace, "provider": "fake", "model": "fake", "operation": "prompt", "sessionEntries": entries, "sessionLeafId": "assistant-entry"}
	if err := runtime.Run(context.Background(), payload, nil, func(Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if len(provider.request.Messages) != 2 || provider.request.Messages[0].Role != "assistant" || provider.request.Messages[1].Role != "tool" || provider.request.Messages[1].Content != "checkpoint" {
		t.Fatalf("messages=%+v", provider.request.Messages)
	}
	for _, message := range provider.request.Messages {
		if strings.Contains(message.Content, "must not be appended") {
			t.Fatal("original prompt was duplicated during checkpoint resume")
		}
	}
}

func TestCompactionKeepsRecentContextAndRestoresSummary(t *testing.T) {
	entries := []map[string]any{}
	parent := ""
	add := func(id, role, content string) {
		var parentValue any
		if parent != "" {
			parentValue = parent
		}
		entries = append(entries, map[string]any{"type": "message", "id": id, "parentId": parentValue, "message": Message{Role: role, Content: content}})
		parent = id
	}
	add("u1", "user", strings.Repeat("old request ", 200))
	add("a1", "assistant", strings.Repeat("old response ", 200))
	add("u2", "user", strings.Repeat("recent request ", 200))
	add("a2", "assistant", "recent response")
	preparation := prepareCompaction(entries, 100)
	if preparation == nil || preparation.FirstKeptID != "u2" {
		t.Fatalf("preparation=%+v", preparation)
	}
	entries = append(entries, map[string]any{"type": "compaction", "id": "c1", "parentId": "a2", "summary": "old work summary", "firstKeptEntryId": "u2"})
	messages := messagesFromEntries(entries)
	if len(messages) != 3 || !strings.Contains(messages[0].Content, "old work summary") || messages[1].Content != strings.Repeat("recent request ", 200) {
		t.Fatalf("messages=%+v", messages)
	}
}

func TestQuestionnaireProducesDurableInteractionRequest(t *testing.T) {
	tools := &toolset{cwd: t.TempDir()}
	result, err := tools.questionnaire(context.Background(), json.RawMessage(`{"questions":[{"id":"scope","prompt":"Which scope?","options":[{"value":"all","label":"Everything"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	request, ok := result.Details["questionnaire"].(map[string]any)
	if !ok || request["questions"] == nil {
		t.Fatalf("details=%+v", result.Details)
	}
}

func TestSkillsDiscoverNestedAgentSkills(t *testing.T) {
	root, project := t.TempDir(), t.TempDir()
	skillDir := filepath.Join(project, ".agents", "skills", "nested", "review")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: review\ndescription: Review changes\n---\nInstructions"), 0644); err != nil {
		t.Fatal(err)
	}
	skills, err := (Resources{Root: root}).Skills(project, "project")
	if err != nil || len(skills) != 1 || skills[0].Name != "review" {
		t.Fatalf("skills=%+v err=%v", skills, err)
	}
}

func TestLocalPiPackageFiltersContributeAgentsSkillsAndPrompts(t *testing.T) {
	project, pkg := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, ".pi"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(pkg, "knowledge"), 0755); err != nil {
		t.Fatal(err)
	}
	settings, _ := json.Marshal(map[string]any{"packages": []any{map[string]any{
		"source":  pkg,
		"agents":  []string{"knowledge/researcher.md"},
		"skills":  []string{"knowledge/SKILL.md"},
		"prompts": []string{"knowledge/review.md"},
	}}})
	if err := os.WriteFile(filepath.Join(project, ".pi", "settings.json"), settings, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "knowledge", "SKILL.md"), []byte("---\nname: packaged-skill\ndescription: package skill\n---\nUse it"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "knowledge", "review.md"), []byte("---\nname: packaged-prompt\ndescription: package prompt\n---\nReview $1"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "knowledge", "researcher.md"), []byte("---\nname: packaged-researcher\ndescription: package meta agent\nrole: meta\ntools: read\n---\nResearch carefully"), 0644); err != nil {
		t.Fatal(err)
	}
	resources := Resources{Root: t.TempDir()}
	skills, err := resources.Skills(project, "project")
	if err != nil || len(skills) != 1 || skills[0].Name != "packaged-skill" {
		t.Fatalf("skills=%+v err=%v", skills, err)
	}
	templates := resources.TemplatesAt(project)
	if len(templates) != 1 || templates[0].Name != "review" {
		t.Fatalf("templates=%+v", templates)
	}
	packagedAgent, err := resources.AgentAt("packaged-researcher", project, "project")
	if err != nil || packagedAgent.Role != AgentRoleMeta || packagedAgent.Body != "Research carefully" {
		t.Fatalf("agent=%+v err=%v", packagedAgent, err)
	}
	definitions := resources.AgentDefinitionsAt(project, "project")
	if len(definitions) != 1 || definitions[0].Name != "packaged-researcher" {
		t.Fatalf("definitions=%+v", definitions)
	}
	if diagnostics := resources.PackageDiagnostics(project); len(diagnostics) != 0 {
		t.Fatalf("diagnostics=%+v", diagnostics)
	}
}

func TestLocalPiPackageManifestAutoloadsAgents(t *testing.T) {
	project, pkg := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, ".pi"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(pkg, "agents"), 0755); err != nil {
		t.Fatal(err)
	}
	settings, _ := json.Marshal(map[string]any{"packages": []string{pkg}})
	if err := os.WriteFile(filepath.Join(project, ".pi", "settings.json"), settings, 0644); err != nil {
		t.Fatal(err)
	}
	manifest, _ := json.Marshal(map[string]any{"pi": map[string]any{"agents": []string{"agents/*.md"}}})
	if err := os.WriteFile(filepath.Join(pkg, "package.json"), manifest, 0644); err != nil {
		t.Fatal(err)
	}
	profile := "---\nname: manifest-agent\ndescription: manifest package agent\nrole: sub\ntools: read\n---\nDo focused work"
	if err := os.WriteFile(filepath.Join(pkg, "agents", "manifest-agent.md"), []byte(profile), 0644); err != nil {
		t.Fatal(err)
	}
	resources := Resources{Root: t.TempDir()}
	got, err := resources.AgentAt("manifest-agent", project, "project")
	if err != nil || got.Role != AgentRoleSub {
		t.Fatalf("agent=%+v err=%v", got, err)
	}
}

func TestAgentPackageExampleExportsAgentAndPromptTemplate(t *testing.T) {
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, ".pi"), 0755); err != nil {
		t.Fatal(err)
	}
	pkg, err := filepath.Abs("../../examples/agent-package")
	if err != nil {
		t.Fatal(err)
	}
	settings, _ := json.Marshal(map[string]any{"packages": []string{pkg}})
	if err := os.WriteFile(filepath.Join(project, ".pi", "settings.json"), settings, 0644); err != nil {
		t.Fatal(err)
	}
	resources := Resources{Root: t.TempDir()}
	profile, err := resources.AgentAt("example-meta", project, "project")
	if err != nil || profile.Role != AgentRoleMeta {
		t.Fatalf("agent=%+v err=%v", profile, err)
	}
	templates := resources.TemplatesAtScope(project, true)
	if len(templates) != 1 || templates[0].Name != "repo-review" {
		t.Fatalf("templates=%+v", templates)
	}
	if got := substituteTemplateArgs(templates[0].Body, []string{"security"}); !strings.Contains(got, "Focus on security.") {
		t.Fatalf("expanded prompt=%q", got)
	}
}

func TestPackageFiltersSupportOfficialGlobAndOverrideForms(t *testing.T) {
	root := t.TempDir()
	paths := []string{
		filepath.Join(root, "skills", "a", "SKILL.md"),
		filepath.Join(root, "skills", "private", "SKILL.md"),
		filepath.Join(root, "skills", "forced", "SKILL.md"),
	}
	got := applyPackagePatterns(paths, []string{"skills/**", "!skills/private/**", "!skills/forced/**", "+skills/forced", "-skills/a"}, root)
	if len(got) != 1 || got[0] != paths[2] {
		t.Fatalf("paths=%v", got)
	}
}

func TestPromptTemplateSupportsAllOfficialArgumentFormsWithoutRecursion(t *testing.T) {
	content := `$1|$2|$@|$ARGUMENTS|${3:-fallback}|${@:-none}|${@:2}|${@:2:2}`
	got := substituteTemplateArgs(content, []string{"one", "$2", "three", "four"})
	want := `one|$2|one $2 three four|one $2 three four|three|one $2 three four|$2 three four|$2 three`
	if got != want {
		t.Fatalf("got=%q want=%q", got, want)
	}
	if got := substituteTemplateArgs(`${1:-default}/${@:-empty}`, nil); got != "default/empty" {
		t.Fatalf("defaults=%q", got)
	}
}

func TestSiblingToolCallsParallelUnlessAnyToolIsSequential(t *testing.T) {
	makeExecution := func() *execution {
		runtime := New(t.TempDir())
		return &execution{runtime: runtime, ctx: context.Background(), command: Command{RunID: "run", SessionID: "session", AgentID: "agent"}, sink: func(Event) error { return nil }, approvedCalls: map[string]bool{}, approvedHashes: map[string]bool{}, deniedHashes: map[string]bool{}, mcpApproval: map[string]bool{}}
	}
	var running, maximum atomic.Int32
	execute := func(context.Context, json.RawMessage) (ToolResult, error) {
		current := running.Add(1)
		for {
			old := maximum.Load()
			if current <= old || maximum.CompareAndSwap(old, current) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		running.Add(-1)
		return ToolResult{Text: "ok"}, nil
	}
	calls := []ToolCall{{ID: "1", Name: "one", Arguments: json.RawMessage(`{}`)}, {ID: "2", Name: "two", Arguments: json.RawMessage(`{}`)}}
	parallel := []ToolDefinition{{Name: "one", Execute: execute}, {Name: "two", Execute: execute}}
	if batch := makeExecution().executeToolBatch(context.Background(), "agent", 0, calls, parallel); len(batch.messages) != 2 || maximum.Load() != 2 {
		t.Fatalf("parallel messages=%d maximum=%d", len(batch.messages), maximum.Load())
	}
	running.Store(0)
	maximum.Store(0)
	sequential := []ToolDefinition{{Name: "one", Execute: execute, ExecutionMode: "sequential"}, {Name: "two", Execute: execute}}
	if batch := makeExecution().executeToolBatch(context.Background(), "agent", 0, calls, sequential); len(batch.messages) != 2 || maximum.Load() != 1 {
		t.Fatalf("sequential messages=%d maximum=%d", len(batch.messages), maximum.Load())
	}
}

func TestPiHarnessTerminatesOnlyWhenEveryToolResultRequestsIt(t *testing.T) {
	makeExecution := func() *execution {
		runtime := New(t.TempDir())
		return &execution{runtime: runtime, ctx: context.Background(), command: Command{RunID: "run", SessionID: "session", AgentID: "agent"}, sink: func(Event) error { return nil }, approvedCalls: map[string]bool{}, approvedHashes: map[string]bool{}, deniedHashes: map[string]bool{}, mcpApproval: map[string]bool{}}
	}
	calls := []ToolCall{{ID: "1", Name: "one", Arguments: json.RawMessage(`{}`)}, {ID: "2", Name: "two", Arguments: json.RawMessage(`{}`)}}
	terminating := func(context.Context, json.RawMessage) (ToolResult, error) {
		return ToolResult{Text: "stop", Terminate: true}, nil
	}
	continuing := func(context.Context, json.RawMessage) (ToolResult, error) { return ToolResult{Text: "continue"}, nil }
	all := makeExecution().executeToolBatch(context.Background(), "agent", 0, calls, []ToolDefinition{{Name: "one", Execute: terminating}, {Name: "two", Execute: terminating}})
	if !all.terminate {
		t.Fatal("all-terminate batch continued")
	}
	mixed := makeExecution().executeToolBatch(context.Background(), "agent", 0, calls, []ToolDefinition{{Name: "one", Execute: terminating}, {Name: "two", Execute: continuing}})
	if mixed.terminate {
		t.Fatal("mixed batch terminated")
	}
}

func TestEditMatchesOfficialMultipleFuzzyAndLineEndingBehavior(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.txt")
	if err := os.WriteFile(path, []byte("\ufeffalpha  \r\n“beta”\r\ngamma\r\n"), 0644); err != nil {
		t.Fatal(err)
	}
	tools := &toolset{cwd: dir}
	result, err := tools.edit(context.Background(), json.RawMessage(`{"path":"sample.txt","edits":[{"oldText":"alpha","newText":"first"},{"oldText":"\"beta\"","newText":"second"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := "\ufefffirst\r\nsecond\r\ngamma\r\n"; string(got) != want {
		t.Fatalf("content=%q want=%q", got, want)
	}
	if result.Details["patch"] == "" || result.Details["firstChangedLine"] != 1 {
		t.Fatalf("details=%+v", result.Details)
	}
}

func TestEditRejectsOverlappingAndAcceptsStringifiedEdits(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.txt")
	if err := os.WriteFile(path, []byte("one two three"), 0644); err != nil {
		t.Fatal(err)
	}
	tools := &toolset{cwd: dir}
	_, err := tools.edit(context.Background(), json.RawMessage(`{"path":"sample.txt","edits":[{"oldText":"one two","newText":"x"},{"oldText":"two three","newText":"y"}]}`))
	if err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("expected overlap error, got %v", err)
	}
	result, err := tools.edit(context.Background(), json.RawMessage(`{"path":"sample.txt","edits":"[{\"oldText\":\"two\",\"newText\":\"2\"}]"}`))
	if err != nil || !strings.Contains(result.Text, "1 block") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestBashTimeoutUsesSecondsAndPersistsTruncatedOutput(t *testing.T) {
	tools := &toolset{cwd: t.TempDir()}
	_, err := tools.bash(context.Background(), json.RawMessage(`{"command":"sleep 1","timeout":0.02}`))
	if err == nil || !strings.Contains(err.Error(), "0.02 seconds") {
		t.Fatalf("timeout error=%v", err)
	}
	result, err := tools.bash(context.Background(), json.RawMessage(`{"command":"yes x | head -n 3000"}`))
	if err != nil {
		t.Fatal(err)
	}
	fullPath, _ := result.Details["fullOutputPath"].(string)
	if fullPath == "" || !strings.Contains(result.Text, "Full output:") {
		t.Fatalf("result=%+v", result)
	}
	defer os.Remove(fullPath)
	data, err := os.ReadFile(fullPath)
	if err != nil || len(data) != 6000 {
		t.Fatalf("full output bytes=%d err=%v", len(data), err)
	}
}

func TestExtensionStateChangesApplyToCurrentRunNextTurn(t *testing.T) {
	provider := &fakeProvider{}
	runtime := New(filepath.Clean("../.."))
	factory := &modelAwareCaptureFactory{provider: provider}
	runtime.Providers = factory
	changed := false
	runtime.RegisterExtension(Extension{Name: "dynamic-state", Action: func(_ context.Context, extension *ExtensionContext, event *ExtensionEvent) error {
		if event.Type == "agent.tool_execution_end" && !changed {
			changed = true
			extension.SetModel("fake", "next-model")
			extension.SetThinkingLevel("high")
			extension.SetActiveTools([]string{"read"})
		}
		return nil
	}})
	payload := map[string]any{"runId": "extension-run", "sessionId": "extension-session", "agentId": "coding-agent", "prompt": "test", "cwd": t.TempDir(), "provider": "fake", "model": "initial-model", "operation": "prompt"}
	if err := runtime.Run(context.Background(), payload, nil, func(Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 2 {
		t.Fatalf("requests=%d", len(provider.requests))
	}
	if len(factory.models) != 2 || factory.models[0] != "initial-model" || factory.models[1] != "next-model" {
		t.Fatalf("resolved models=%v", factory.models)
	}
	second := provider.requests[1]
	if second.Model != "next-model" || second.ThinkingLevel != "high" {
		t.Fatalf("second model=%q thinking=%q", second.Model, second.ThinkingLevel)
	}
	if names := definitionNames(second.Tools); len(names) != 1 || names[0] != "read" {
		t.Fatalf("tools=%v", names)
	}
}

func TestHeadlessExtensionContextExposesLivePiState(t *testing.T) {
	root, cwd := filepath.Clean("../.."), t.TempDir()
	runtime := New(root)
	runtime.Providers = fakeFactory{&captureProvider{}}
	checked := false
	runtime.RegisterExtension(Extension{Action: func(_ context.Context, extension *ExtensionContext, event *ExtensionEvent) error {
		if event.Type != "agent.agent_start" {
			return nil
		}
		checked = true
		if extension.CWD() != cwd || extension.IsIdle() || extension.Signal() == nil {
			t.Fatalf("cwd=%q idle=%v signal=%v", extension.CWD(), extension.IsIdle(), extension.Signal())
		}
		if !strings.Contains(extension.SystemPrompt(), "expert coding assistant") || len(extension.AllTools()) == 0 {
			t.Fatalf("system=%q tools=%v", extension.SystemPrompt(), definitionNames(extension.AllTools()))
		}
		usage := extension.ContextUsage()
		if usage == nil || usage.ContextWindow != defaultContextWindow || usage.Tokens == nil {
			t.Fatalf("usage=%v", usage)
		}
		extension.Compact()
		return nil
	}})
	payload := map[string]any{"runId": "context-run", "sessionId": "context-session", "agentId": "coding-agent", "prompt": "test", "cwd": cwd, "provider": "fake", "model": "fake", "operation": "prompt"}
	if err := runtime.Run(context.Background(), payload, nil, nil); err != nil {
		t.Fatal(err)
	}
	if !checked {
		t.Fatal("extension context was not inspected")
	}
}

func TestExtensionToolCallArgumentsAreMutableAndSharedInOrder(t *testing.T) {
	runtime := New(t.TempDir())
	runtime.RegisterExtension(Extension{BeforeToolCall: func(_ context.Context, event ToolCallEvent) (ToolCallDecision, error) {
		event.Input["value"] = "mutated"
		return ToolCallDecision{}, nil
	}})
	runtime.RegisterExtension(Extension{BeforeToolCall: func(_ context.Context, event ToolCallEvent) (ToolCallDecision, error) {
		if event.Input["value"] != "mutated" {
			t.Fatalf("later extension input=%v", event.Input)
		}
		event.Input["added"] = true
		return ToolCallDecision{}, nil
	}})
	tool := ToolDefinition{
		Name: "custom", Parameters: map[string]any{
			"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string"}},
			"required": []string{"value"}, "additionalProperties": false,
		},
		Execute: func(_ context.Context, raw json.RawMessage) (ToolResult, error) {
			var input map[string]any
			_ = json.Unmarshal(raw, &input)
			if input["value"] != "mutated" || input["added"] != true {
				t.Fatalf("tool input=%v", input)
			}
			return ToolResult{Text: "ok"}, nil
		},
	}
	execution := &execution{
		runtime: runtime, ctx: context.Background(), command: Command{RunID: "run"},
		sink: func(Event) error { return nil }, approvedCalls: map[string]bool{},
		approvedHashes: map[string]bool{}, deniedHashes: map[string]bool{}, mcpApproval: map[string]bool{},
	}
	outcome := execution.executeOneTool(context.Background(), "agent", 0, ToolCall{ID: "call", Name: "custom", Arguments: json.RawMessage(`{"value":"original"}`)}, []ToolDefinition{tool})
	if outcome.err != nil || outcome.result.Text != "ok" {
		t.Fatalf("outcome=%+v", outcome)
	}
}

func TestSessionLifecycleReasonsMatchNewResumeAndError(t *testing.T) {
	root := filepath.Clean("../..")
	runtime := New(root)
	runtime.Providers = fakeFactory{&captureProvider{}}
	startReason, shutdownReason := "", ""
	runtime.RegisterExtension(Extension{
		SessionStart: func(_ context.Context, event SessionLifecycleEvent) error {
			startReason = event.Reason
			return nil
		},
		BeforeAgentStart: func(context.Context, *AgentStartEvent) error {
			return errors.New("extension failed")
		},
		SessionShutdown: func(_ context.Context, event SessionLifecycleEvent) error {
			shutdownReason = event.Reason
			return nil
		},
	})
	payload := map[string]any{
		"runId": "lifecycle-run", "sessionId": "lifecycle-session", "agentId": "coding-agent",
		"prompt": "test", "cwd": t.TempDir(), "provider": "fake", "model": "fake", "operation": "prompt",
		"sessionEntries": []map[string]any{{"id": "existing", "type": "custom"}},
	}
	if err := runtime.Run(context.Background(), payload, nil, nil); err == nil {
		t.Fatal("extension failure was ignored")
	}
	if startReason != "resume" || shutdownReason != "error" {
		t.Fatalf("start=%q shutdown=%q", startReason, shutdownReason)
	}
}

func TestPromptTemplateUsesSingleRuntimeLifecycle(t *testing.T) {
	config := t.TempDir()
	t.Setenv("AGENT_CONFIG_DIR", config)
	if err := os.MkdirAll(filepath.Join(config, "prompts"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config, "prompts", "review.md"), []byte("Review $1"), 0644); err != nil {
		t.Fatal(err)
	}
	provider := &captureProvider{}
	runtime := New(filepath.Clean("../.."))
	runtime.Providers = fakeFactory{provider}
	var starts, shutdowns int
	runtime.RegisterExtension(Extension{
		SessionStart: func(context.Context, SessionLifecycleEvent) error { starts++; return nil },
		SessionShutdown: func(context.Context, SessionLifecycleEvent) error {
			shutdowns++
			return nil
		},
	})
	payload := map[string]any{
		"runId": "template-run", "sessionId": "template-session", "agentId": "coding-agent",
		"cwd": t.TempDir(), "provider": "fake", "model": "fake", "operation": "prompt_template",
		"templateName": "review", "templateArgs": []string{"main.go"},
	}
	if err := runtime.Run(context.Background(), payload, nil, nil); err != nil {
		t.Fatal(err)
	}
	if starts != 1 || shutdowns != 1 || len(provider.request.Messages) == 0 {
		t.Fatalf("starts=%d shutdowns=%d request=%+v", starts, shutdowns, provider.request)
	}
	if got := provider.request.Messages[len(provider.request.Messages)-1].Content; got != "Review main.go" {
		t.Fatalf("expanded prompt=%q", got)
	}
}

func TestUntrustedProjectResourcesAreExcluded(t *testing.T) {
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, ".pi", "prompts"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "AGENTS.md"), []byte("PROJECT SECRET INSTRUCTIONS"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, ".pi", "prompts", "private.md"), []byte("private prompt"), 0644); err != nil {
		t.Fatal(err)
	}
	provider := &captureProvider{}
	runtime := New(filepath.Clean("../.."))
	runtime.Providers = fakeFactory{provider}
	checked := false
	runtime.RegisterExtension(Extension{Action: func(_ context.Context, extension *ExtensionContext, event *ExtensionEvent) error {
		if event.Type == "agent.agent_start" {
			checked = true
			if extension.IsProjectTrusted() {
				t.Fatal("project unexpectedly trusted")
			}
		}
		return nil
	}})
	trusted := false
	payload := map[string]any{
		"runId": "untrusted-run", "sessionId": "untrusted-session", "agentId": "coding-agent",
		"prompt": "test", "cwd": project, "provider": "fake", "model": "fake", "operation": "prompt",
		"projectTrusted": trusted,
	}
	if err := runtime.Run(context.Background(), payload, nil, nil); err != nil {
		t.Fatal(err)
	}
	if !checked || strings.Contains(provider.request.System, "PROJECT SECRET INSTRUCTIONS") {
		t.Fatalf("checked=%v system=%q", checked, provider.request.System)
	}
	if templates := runtime.Resources.TemplatesAtScope(project, false); len(templates) != 0 {
		t.Fatalf("untrusted project templates=%+v", templates)
	}
}

func TestContextUsageUnknownImmediatelyAfterCompaction(t *testing.T) {
	runtime := New(t.TempDir())
	execution := &execution{
		runtime: runtime, ctx: context.Background(), command: Command{ContextWindow: 1000},
		entries: []map[string]any{{"type": "compaction", "summary": "summary"}},
	}
	usage := (&ExtensionContext{execution: execution}).ContextUsage()
	if usage == nil || usage.Tokens != nil || usage.Percent != nil || usage.ContextWindow != 1000 {
		t.Fatalf("usage=%+v", usage)
	}
	execution.entries = append(execution.entries, map[string]any{"type": "message", "message": Message{
		Role: "assistant", StopReason: "stop", Usage: map[string]any{"totalTokens": 250},
	}})
	usage = (&ExtensionContext{execution: execution}).ContextUsage()
	if usage.Tokens == nil || *usage.Tokens != 250 || usage.Percent == nil || *usage.Percent != 25 {
		t.Fatalf("post-compaction usage=%+v", usage)
	}
}

func TestProviderHeadersHookMutatesFinalRequest(t *testing.T) {
	provider := &captureProvider{}
	runtime := New(t.TempDir())
	runtime.RegisterExtension(Extension{BeforeProviderHeaders: func(_ context.Context, event *ProviderHeadersEvent) error {
		event.Headers["traceparent"] = "00-test"
		delete(event.Headers, "remove-me")
		return nil
	}})
	execution := &execution{runtime: runtime, ctx: context.Background(), command: Command{}, sink: func(Event) error { return nil }}
	_, err := execution.complete(context.Background(), provider, CompletionRequest{Headers: map[string]string{"remove-me": "yes"}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if provider.request.Headers["traceparent"] != "00-test" {
		t.Fatalf("headers=%v", provider.request.Headers)
	}
	if _, exists := provider.request.Headers["remove-me"]; exists {
		t.Fatalf("deleted header remains: %v", provider.request.Headers)
	}
}

func TestContextCommandReceivesFullExtensionContext(t *testing.T) {
	runtime := New(filepath.Clean("../.."))
	runtime.RegisterExtension(Extension{ContextCommands: map[string]func(context.Context, *ExtensionContext, string) (string, error){
		"state": func(_ context.Context, extension *ExtensionContext, args string) (string, error) {
			if extension.CWD() == "" || args != "now" || !extension.IsProjectTrusted() {
				t.Fatalf("cwd=%q args=%q trusted=%v", extension.CWD(), args, extension.IsProjectTrusted())
			}
			return "ok", nil
		},
	}})
	payload := map[string]any{
		"runId": "command-run", "sessionId": "command-session", "agentId": "coding-agent",
		"cwd": t.TempDir(), "provider": "fake", "model": "fake", "operation": "extension_command",
		"commandName": "state", "commandArgs": "now",
	}
	if err := runtime.Run(context.Background(), payload, nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestContextOverflowDetectionMatchesProviderFamilies(t *testing.T) {
	cases := []string{
		"prompt is too long: 210000 tokens > 200000 maximum",
		"Your input exceeds the context window of this model",
		"The input token count exceeds the maximum number of tokens allowed",
		"model_context_window_exceeded",
	}
	for _, message := range cases {
		if !isContextOverflowError(errors.New(message)) {
			t.Fatalf("overflow not detected: %q", message)
		}
	}
	if isContextOverflowError(errors.New("rate limit: too many tokens requested per minute")) {
		t.Fatal("rate limit misclassified as context overflow")
	}
}

func TestGoPackageManagerPersistsLocalResourcePackage(t *testing.T) {
	config, project, pkg := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("AGENT_CONFIG_DIR", config)
	if err := os.MkdirAll(filepath.Join(project, ".pi"), 0755); err != nil {
		t.Fatal(err)
	}
	manager := PackageManager{CWD: project}
	path, err := manager.Install(context.Background(), pkg, "project", true)
	if err != nil || path != pkg {
		t.Fatalf("path=%q err=%v", path, err)
	}
	configured, diagnostics := configuredPackages(project)
	if len(diagnostics) != 0 || len(configured) != 1 || configured[0].Source != pkg || configured[0].Scope != "project" {
		t.Fatalf("configured=%+v diagnostics=%+v", configured, diagnostics)
	}
	if err := manager.Remove(pkg, "project", true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(pkg); err != nil {
		t.Fatalf("local package was deleted: %v", err)
	}
	configured, _ = configuredPackages(project)
	if len(configured) != 0 {
		t.Fatalf("package still configured: %+v", configured)
	}
}

func TestGoPackageManagerRejectsGlobalMutationByDefault(t *testing.T) {
	t.Setenv("AGENT_ALLOW_GLOBAL_PACKAGE_MUTATIONS", "false")
	_, err := (PackageManager{CWD: t.TempDir()}).Install(context.Background(), t.TempDir(), "user", true)
	if err == nil || !strings.Contains(err.Error(), "global package mutation is disabled") {
		t.Fatalf("err=%v", err)
	}
}

func TestNPMPackageExtractionRejectsTraversal(t *testing.T) {
	var archive bytes.Buffer
	gzipWriter := gzip.NewWriter(&archive)
	tarWriter := tar.NewWriter(gzipWriter)
	body := []byte("escape")
	if err := tarWriter.WriteHeader(&tar.Header{Name: "package/../../escape", Mode: 0644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := extractPackageTarGZ(bytes.NewReader(archive.Bytes()), t.TempDir()); err == nil {
		t.Fatal("unsafe archive path was accepted")
	}
}

func TestNPMPackageExtractionRejectsExpandedFileLimit(t *testing.T) {
	var archive bytes.Buffer
	gzipWriter := gzip.NewWriter(&archive)
	tarWriter := tar.NewWriter(gzipWriter)
	if err := tarWriter.WriteHeader(&tar.Header{Name: "package/huge", Mode: 0644, Size: 129 << 20, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	// Deliberately omit the declared body. Extraction must reject the header's
	// expanded size before attempting to consume it.
	_ = tarWriter.Close()
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := extractPackageTarGZ(bytes.NewReader(archive.Bytes()), t.TempDir()); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized expanded file was accepted: %v", err)
	}
}

func TestSubagentScopeDefaultsToCurrentAgentScope(t *testing.T) {
	gotScope := ""
	tools := &toolset{
		cwd:           t.TempDir(),
		subagentScope: "project",
		subagentNames: []string{"child"},
		subagent: func(_ context.Context, _, _, _, scope string) (string, error) {
			gotScope = scope
			return "done", nil
		},
	}
	if _, err := tools.runSubagent(context.Background(), json.RawMessage(`{"agent":"child","task":"work"}`)); err != nil {
		t.Fatal(err)
	}
	if gotScope != "project" {
		t.Fatalf("child scope=%q, want inherited project scope", gotScope)
	}
}

func TestReviewerUsesEnforcedReadOnlyGitTool(t *testing.T) {
	reviewer, err := (Resources{Root: filepath.Clean("../..")}).Agent("reviewer")
	if err != nil {
		t.Fatal(err)
	}
	if contains(reviewer.Tools, "bash") || !contains(reviewer.Tools, "git_read") {
		t.Fatalf("reviewer tools are not capability-safe: %v", reviewer.Tools)
	}
	tools := &toolset{cwd: t.TempDir()}
	if _, err := tools.gitRead(context.Background(), json.RawMessage(`{"operation":"commit"}`)); err == nil {
		t.Fatal("mutation-capable Git operation was accepted")
	}
}

func TestPackagedAgentFailsFastWhenRequiredHostToolIsMissing(t *testing.T) {
	config, project := t.TempDir(), t.TempDir()
	t.Setenv("AGENT_CONFIG_DIR", config)
	if err := os.MkdirAll(filepath.Join(project, ".pi"), 0755); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "researcher-package")
	resources := Resources{Root: filepath.Clean("../..")}
	if _, err := resources.BuildAgentPackage(output, AgentPackageBuildOptions{EntryAgent: "web-researcher"}); err != nil {
		t.Fatal(err)
	}
	settings, _ := json.Marshal(map[string]any{"packages": []string{output}})
	if err := os.WriteFile(filepath.Join(project, ".pi", "settings.json"), settings, 0644); err != nil {
		t.Fatal(err)
	}
	runtime := New(filepath.Clean("../.."))
	runtime.Providers = &captureFactory{provider: &captureProvider{}}
	err := runtime.Run(context.Background(), map[string]any{
		"agentId": "web-researcher", "agentScope": "project", "projectTrusted": true,
		"prompt": "research", "cwd": project, "provider": "fake", "model": "fake",
	}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "web_search") {
		t.Fatalf("missing required package tool was not rejected: %v", err)
	}
}

func TestOfficialPiPromptBundleHashes(t *testing.T) {
	constants := map[string]string{
		summarizationSystemPrompt:     "c464889dcfa60441e642f291445b49523f263e6fb2725d0c25075543a2ec3f8f",
		summarizationPrompt:           "9b00aa68df1a64279bc36e9093367f638701d48ec82e3d08436f65092a515f9b",
		updateSummarizationPrompt:     "240c52982209146eae47d73c7172f6ba1dab60f44520bcb6a6ec1a883fef2ec7",
		turnPrefixSummarizationPrompt: "9aeeb36ea731a8497d38abb03c2da351d81bcade4b2ae389bb6ae74300cf6ba5",
		branchSummaryPrompt:           "76cf34f4204cc5465282460c9a3099301d9c8c0a672eda0e9d48ee7af422cec1",
	}
	for content, expected := range constants {
		if actual := fmt.Sprintf("%x", sha256.Sum256([]byte(content))); actual != expected {
			t.Fatalf("official prompt drift: got %s want %s", actual, expected)
		}
	}
	root := filepath.Clean("../..")
	files := map[string]string{
		"prompts/modes/plan.md":         "cff2d719d55522d36372655ba8799a769580eac4959e98f1f2176890a81b88b7",
		"prompts/modes/execute-plan.md": "93603921389656e88a39e4955e0953149c4db227d073699251b3e98b9b4df129",
		"agents/scout.md":               "a5d584400a202a0ab630e1c2e0aa1d03cb0ad8f0e1152605e1ef798c15c18327",
		"agents/planner.md":             "adf18af664e2043da97c44e2ed3ae87a71501d186f83b0dbd85045192160b7c7",
		"agents/reviewer.md":            "fe7645e26ddce12ecc413bb4cbc87bd24d21b540482d9301f765c11a8a64301a",
		"agents/worker.md":              "069c0858bfdad462a20f2d7644ac6428a23d814d87a35a8b923f2a900063e29b",
	}
	for path, expected := range files {
		data, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		if actual := fmt.Sprintf("%x", sha256.Sum256(data)); actual != expected {
			t.Fatalf("%s drift: got %s want %s", path, actual, expected)
		}
	}
}

func TestCustomAgentScopeAndModelProviderResolution(t *testing.T) {
	config, project := t.TempDir(), t.TempDir()
	t.Setenv("AGENT_CONFIG_DIR", config)
	if err := os.MkdirAll(filepath.Join(config, "agents"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(project, ".pi", "agents"), 0755); err != nil {
		t.Fatal(err)
	}
	userAgent := "---\nname: analyst\ndescription: user role\nmodel: claude-sonnet-4-5\ntools: read\n---\nuser body"
	projectAgent := strings.ReplaceAll(userAgent, "user role", "project role")
	if err := os.WriteFile(filepath.Join(config, "agents", "analyst.md"), []byte(userAgent), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, ".pi", "agents", "analyst.md"), []byte(projectAgent), 0644); err != nil {
		t.Fatal(err)
	}
	resources := Resources{Root: filepath.Clean("../..")}
	user, err := resources.AgentAt("analyst", project, "user")
	if err != nil || user.Description != "user role" {
		t.Fatalf("user=%+v err=%v", user, err)
	}
	projectRole, err := resources.AgentAt("analyst", project, "both")
	if err != nil || projectRole.Description != "project role" {
		t.Fatalf("project=%+v err=%v", projectRole, err)
	}
	if providerForModel(user.Model) != "anthropic" || providerForModel("gemini-2.5-pro") != "google" {
		t.Fatal("model provider inference failed")
	}
}

func TestCustomAgentWithoutToolsUsesPiCodingDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.md")
	if err := os.WriteFile(path, []byte("---\nname: custom\ndescription: custom role\n---\nDo work"), 0644); err != nil {
		t.Fatal(err)
	}
	definition, err := loadAgentDefinition(path, "")
	if err != nil {
		t.Fatal(err)
	}
	expected := []string{"read", "bash", "edit", "write"}
	if strings.Join(definition.Tools, ",") != strings.Join(expected, ",") {
		t.Fatalf("tools=%v", definition.Tools)
	}
}

func TestAgentRolesAndDelegationPolicy(t *testing.T) {
	resources := Resources{Root: filepath.Clean("../..")}
	defaultAgent, err := resources.Agent("")
	if err != nil {
		t.Fatal(err)
	}
	if defaultAgent.Name != "chief-agent" || defaultAgent.Role != AgentRoleMain {
		t.Fatalf("default agent=%+v", defaultAgent)
	}
	wantRoles := map[string]AgentRole{
		"chief-agent":    AgentRoleMain,
		"coding-agent":   AgentRoleSub,
		"worker":         AgentRoleMeta,
		"scout":          AgentRoleMeta,
		"planner":        AgentRoleMeta,
		"reviewer":       AgentRoleMeta,
		"web-researcher": AgentRoleMeta,
	}
	for name, want := range wantRoles {
		definition, err := resources.Agent(name)
		if err != nil {
			t.Fatal(err)
		}
		if definition.Role != want {
			t.Fatalf("%s role=%q want=%q", name, definition.Role, want)
		}
	}
	main, err := resources.Agent("chief-agent")
	if err != nil {
		t.Fatal(err)
	}
	coding, err := resources.Agent("coding-agent")
	if err != nil {
		t.Fatal(err)
	}
	if main.SystemPrompt != "prompts/system/chief-agent.md" || coding.SystemPrompt != "prompts/system/coding-agent.md" {
		t.Fatalf("unexpected profile prompts: main=%+v coding=%+v", main, coding)
	}
	if strings.Join(main.Delegates, ",") != "*" {
		t.Fatalf("chief delegates=%v", main.Delegates)
	}
	if got, want := strings.Join(coding.Delegates, ","), "scout,planner,reviewer,worker,web-researcher"; got != want {
		t.Fatalf("coding delegates=%q want=%q", got, want)
	}
	if main.SystemPrompt == coding.SystemPrompt || main.Body != "" || coding.Body != "" {
		t.Fatalf("chief and coding prompt boundaries collapsed: main=%+v coding=%+v", main, coding)
	}
	if strings.Join(main.Tools, ",") != strings.Join(coding.Tools, ",") {
		t.Fatalf("main tools=%v coding tools=%v", main.Tools, coding.Tools)
	}
	mainCWD := t.TempDir()
	mainPrompt, err := resources.BuildSystemPrompt(main, mainCWD, (&toolset{cwd: mainCWD}).definitions(main.Tools), false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(mainPrompt, "expert general-purpose agent") || strings.Contains(mainPrompt, "expert coding assistant") {
		t.Fatalf("chief prompt is not domain neutral: %q", mainPrompt)
	}
	for _, test := range []struct {
		caller, target AgentRole
		want           bool
	}{
		{AgentRoleMain, AgentRoleSub, true},
		{AgentRoleMain, AgentRoleMeta, true},
		{AgentRoleMain, AgentRoleMain, false},
		{AgentRoleSub, AgentRoleMeta, true},
		{AgentRoleSub, AgentRoleSub, false},
		{AgentRoleSub, AgentRoleMain, false},
		{AgentRoleMeta, AgentRoleMeta, false},
	} {
		if got := canDelegateAgent(test.caller, test.target); got != test.want {
			t.Fatalf("%s -> %s = %t, want %t", test.caller, test.target, got, test.want)
		}
	}
	planner := Agent{Name: "planner", Role: AgentRoleMeta}
	reviewer := Agent{Name: "reviewer", Role: AgentRoleMeta}
	if !canDelegateTo(Agent{Name: "restricted", Role: AgentRoleSub, Delegates: []string{"planner"}}, planner) {
		t.Fatal("explicitly allowed Meta Agent was denied")
	}
	if canDelegateTo(Agent{Name: "restricted", Role: AgentRoleSub, Delegates: []string{"planner"}}, reviewer) {
		t.Fatal("Meta Agent outside the explicit allowlist was allowed")
	}
	if canDelegateTo(Agent{Name: "deny-default", Role: AgentRoleSub}, planner) {
		t.Fatal("empty delegate allowlist did not deny delegation")
	}
	if !canDelegateTo(Agent{Name: "orchestrator", Role: AgentRoleMain, Delegates: []string{"*"}}, Agent{Name: "child", Role: AgentRoleSub}) {
		t.Fatal("explicit wildcard did not allow a role-compatible target")
	}
	if canDelegateTo(Agent{Name: "orchestrator", Role: AgentRoleMain, Delegates: []string{"*"}}, Agent{Name: "other-main", Role: AgentRoleMain}) {
		t.Fatal("wildcard bypassed the role hierarchy")
	}
	path := filepath.Join(t.TempDir(), "invalid.md")
	if err := os.WriteFile(path, []byte("---\nname: invalid\ndescription: invalid role\nrole: root\n---\nbody"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadAgentDefinition(path, ""); err == nil {
		t.Fatal("invalid role was accepted")
	}
}

func TestProfileCanRunDirectlyWithItsOwnModelDefault(t *testing.T) {
	provider := &captureProvider{}
	factory := &captureFactory{provider: provider}
	runtime := New(filepath.Clean("../.."))
	runtime.Providers = factory
	var started []string
	err := runtime.Run(context.Background(), map[string]any{
		"agentId": "scout",
		"prompt":  "Inspect the repository.",
		"cwd":     t.TempDir(),
	}, nil, func(event Event) error {
		if event.Type == "agent.agent_start" {
			started = append(started, event.AgentID)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if factory.name != "anthropic" || provider.request.Model != "claude-haiku-4-5" {
		t.Fatalf("profile defaults were not applied: provider=%q request=%+v", factory.name, provider.request)
	}
	if strings.Join(started, ",") != "scout" {
		t.Fatalf("profile was not run directly: started=%v", started)
	}
	if !strings.Contains(provider.request.System, "You are a scout.") {
		t.Fatalf("scout prompt missing: %q", provider.request.System)
	}
}

func TestProjectPackagedAgentCanRunDirectlyWithAgentScope(t *testing.T) {
	config, project, pkg := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("AGENT_CONFIG_DIR", config)
	if err := os.MkdirAll(filepath.Join(project, ".pi"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(pkg, "agents"), 0755); err != nil {
		t.Fatal(err)
	}
	settings, _ := json.Marshal(map[string]any{"packages": []string{pkg}})
	if err := os.WriteFile(filepath.Join(project, ".pi", "settings.json"), settings, 0644); err != nil {
		t.Fatal(err)
	}
	manifest := `{"pi":{"agents":["agents/*.md"]}}`
	if err := os.WriteFile(filepath.Join(pkg, "package.json"), []byte(manifest), 0644); err != nil {
		t.Fatal(err)
	}
	profile := "---\nname: direct-package-agent\ndescription: direct package entry\nrole: meta\ntools: read\n---\nPackage-only instructions"
	if err := os.WriteFile(filepath.Join(pkg, "agents", "direct.md"), []byte(profile), 0644); err != nil {
		t.Fatal(err)
	}

	provider := &captureProvider{}
	runtime := New(filepath.Clean("../.."))
	runtime.Providers = &captureFactory{provider: provider}
	err := runtime.Run(context.Background(), map[string]any{
		"agentId":        "direct-package-agent",
		"agentScope":     "project",
		"prompt":         "Work directly.",
		"cwd":            project,
		"provider":       "fake",
		"model":          "fake",
		"projectTrusted": true,
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(provider.request.System, "Package-only instructions") {
		t.Fatalf("system=%q", provider.request.System)
	}
}

func TestSDKCustomRootExampleLoadsOnlyItsCustomProfileAndPrompt(t *testing.T) {
	root := filepath.Clean("../../examples/sdk-app/custom-root/resources")
	resources := Resources{Root: root}
	definitions := resources.AgentDefinitionsAt("", "user")
	if len(definitions) != 1 || definitions[0].Name != "only-meta" {
		t.Fatalf("definitions=%+v", definitions)
	}
	definition := definitions[0]
	cwd := t.TempDir()
	tools := (&toolset{cwd: cwd}).definitions(definition.Tools)
	system, err := resources.BuildSystemPrompt(definition, cwd, tools, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(system, "focused AI Agent") || !strings.Contains(system, "Never modify files") {
		t.Fatalf("system=%q", system)
	}
}

func TestBundledAgentProfilesUseGenericAgentTypeAndAreOptional(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agents")
	if err := os.MkdirAll(path, 0755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: optional-specialist\ndescription: optional specialist\nrole: sub\ntools: read, subagent\ndelegates: planner, reviewer, planner\n---\nPrompt text"
	profilePath := filepath.Join(path, "optional.md")
	if err := os.WriteFile(profilePath, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	var profile Agent
	definition, err := (Resources{Root: root}).Agent("optional-specialist")
	if err != nil {
		t.Fatal(err)
	}
	profile = definition
	if profile.Role != AgentRoleSub || strings.Join(profile.Tools, ",") != "read,subagent" || strings.Join(profile.Delegates, ",") != "planner,reviewer" || profile.Body != "Prompt text" {
		t.Fatalf("profile=%+v", profile)
	}
	if err := os.Remove(profilePath); err != nil {
		t.Fatal(err)
	}
	if _, err := (Resources{Root: root}).Agent("optional-specialist"); err == nil {
		t.Fatal("removed optional profile remained available")
	}
}

func TestCustomAgentCanUseTheGenericMainRole(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.md")
	body := "---\nname: custom\ndescription: custom role\nrole: main\n---\nbody"
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	definition, err := loadAgentDefinition(path, "")
	if err != nil || definition.Role != AgentRoleMain {
		t.Fatalf("definition=%+v err=%v", definition, err)
	}
}

func TestSubagentToolOnlyExposesAvailableAgents(t *testing.T) {
	toolByName := func(names []string) ToolDefinition {
		tools := (&toolset{subagentNames: names}).definitions([]string{"subagent"})
		if len(tools) != 1 {
			t.Fatalf("tools=%v", definitionNames(tools))
		}
		return tools[0]
	}
	mainTool := toolByName([]string{"coding-agent", "planner", "reviewer", "scout", "worker"})
	mainNames, _ := mainTool.Parameters["properties"].(map[string]any)["agent"].(map[string]any)["enum"].([]string)
	if strings.Join(mainNames, ",") != "coding-agent,planner,reviewer,scout,worker" {
		t.Fatalf("main visible agents=%v", mainNames)
	}
	subTool := toolByName([]string{"planner", "reviewer", "scout", "worker"})
	subNames, _ := subTool.Parameters["properties"].(map[string]any)["agent"].(map[string]any)["enum"].([]string)
	if strings.Join(subNames, ",") != "planner,reviewer,scout,worker" {
		t.Fatalf("sub visible agents=%v", subNames)
	}
	if strings.Contains(subTool.Description, "chief-agent") || strings.Contains(subTool.Description, "coding-agent") {
		t.Fatalf("subagent description leaked unavailable agents: %q", subTool.Description)
	}
	if tools := (&toolset{}).definitions([]string{"subagent"}); len(tools) != 0 {
		t.Fatalf("meta agent received subagent tool: %v", definitionNames(tools))
	}
}

func TestRuntimeSubagentToolUsesProfileDelegateAllowlist(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "agents"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "prompts", "system"), 0755); err != nil {
		t.Fatal(err)
	}
	system := "You are an Agent.\n{{TOOLS}}\n{{APPEND_SYSTEM_PROMPT}}"
	if err := os.WriteFile(filepath.Join(root, "prompts", "system", "base.md"), []byte(system), 0644); err != nil {
		t.Fatal(err)
	}
	profiles := map[string]string{
		"caller.md":  "---\nname: caller\ndescription: restricted caller\nrole: sub\ntools: read, subagent\ndelegates: allowed-meta\nsystem-prompt: prompts/system/base.md\n---\nDelegate carefully.",
		"allowed.md": "---\nname: allowed-meta\ndescription: allowed target\nrole: meta\ntools: read\nsystem-prompt: prompts/system/base.md\n---\nAllowed.",
		"blocked.md": "---\nname: blocked-meta\ndescription: blocked target\nrole: meta\ntools: read\nsystem-prompt: prompts/system/base.md\n---\nBlocked.",
	}
	for name, body := range profiles {
		if err := os.WriteFile(filepath.Join(root, "agents", name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	provider := &captureProvider{}
	runtime := New(root)
	runtime.Providers = fakeFactory{provider}
	if err := runtime.Run(context.Background(), map[string]any{
		"agentId": "caller", "prompt": "Delegate.", "cwd": t.TempDir(), "provider": "fake", "model": "fake",
	}, nil, nil); err != nil {
		t.Fatal(err)
	}
	var subagent ToolDefinition
	for _, tool := range provider.request.Tools {
		if tool.Name == "subagent" {
			subagent = tool
			break
		}
	}
	if subagent.Name == "" {
		t.Fatal("allowed delegate did not expose the subagent tool")
	}
	properties, _ := subagent.Parameters["properties"].(map[string]any)
	agentSchema, _ := properties["agent"].(map[string]any)
	names, _ := agentSchema["enum"].([]string)
	if strings.Join(names, ",") != "allowed-meta" {
		t.Fatalf("visible delegates=%v", names)
	}

	adversarial := &queuedProvider{responses: []Completion{
		{ToolCalls: []ToolCall{{ID: "blocked-call", Name: "subagent", Arguments: json.RawMessage(`{"agent":"blocked-meta","task":"bypass the allowlist"}`)}}},
		{Text: "stopped"},
	}}
	runtime = New(root)
	runtime.Providers = fakeFactory{adversarial}
	if err := runtime.Run(context.Background(), map[string]any{
		"agentId": "caller", "prompt": "Delegate.", "cwd": t.TempDir(), "provider": "fake", "model": "fake",
	}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(adversarial.requests) != 2 {
		t.Fatalf("provider requests=%d", len(adversarial.requests))
	}
	messages := adversarial.requests[1].Messages
	if len(messages) == 0 || !strings.Contains(messages[len(messages)-1].Content, "expected one of allowed-meta") {
		t.Fatalf("blocked delegation result=%+v", messages)
	}
}

func TestQuestionnaireUsesStableIDAndConsumesDurableReply(t *testing.T) {
	runtime := New(t.TempDir())
	call := ToolCall{ID: "question-call", Name: "questionnaire", Arguments: json.RawMessage(`{"questions":[{"id":"choice","prompt":"Choose","options":[{"value":"a","label":"A"}]}]}`)}
	tool := (&toolset{}).definitions([]string{"questionnaire"})
	var requestedID string
	firstExecution := &execution{runtime: runtime, ctx: context.Background(), command: Command{RunID: "run", SessionID: "session"}, sink: func(event Event) error {
		if event.Type == "input.requested" {
			requestedID, _ = event.Payload["interactionId"].(string)
		}
		return nil
	}, approvedCalls: map[string]bool{}, approvedHashes: map[string]bool{}, deniedHashes: map[string]bool{}, mcpApproval: map[string]bool{}, interactionPoller: func(context.Context, string) (InteractionReply, error) { return InteractionReply{}, nil }}
	firstOutcome := firstExecution.executeOneTool(context.Background(), "agent", 0, call, tool)
	if firstOutcome.err != nil || firstExecution.pendingInput != 1 || requestedID == "" {
		t.Fatalf("first=%+v pending=%d id=%q", firstOutcome, firstExecution.pendingInput, requestedID)
	}
	secondExecution := &execution{runtime: runtime, ctx: context.Background(), command: Command{RunID: "run", SessionID: "session"}, sink: func(Event) error { return nil }, approvedCalls: map[string]bool{}, approvedHashes: map[string]bool{}, deniedHashes: map[string]bool{}, mcpApproval: map[string]bool{}, interactionPoller: func(_ context.Context, id string) (InteractionReply, error) {
		if id != requestedID {
			t.Fatalf("resume id=%q want=%q", id, requestedID)
		}
		return InteractionReply{Ready: true, Response: map[string]any{"choice": "a"}}, nil
	}}
	secondOutcome := secondExecution.executeOneTool(context.Background(), "agent", 0, call, tool)
	if secondOutcome.err != nil || secondExecution.pendingInput != 0 || !strings.Contains(secondOutcome.result.Text, `"choice":"a"`) {
		t.Fatalf("second=%+v pending=%d", secondOutcome, secondExecution.pendingInput)
	}
}

func TestApprovalIDIsStableAcrossLeaseRecovery(t *testing.T) {
	first := stableApprovalID("run", "call", "hash")
	if first == "" || first != stableApprovalID("run", "call", "hash") || first == stableApprovalID("run", "call", "other") {
		t.Fatalf("approval ids are not stable and scoped: %q", first)
	}
}

func TestEventPipelineIsSerializedAndFailureIsSticky(t *testing.T) {
	runtime := New(t.TempDir())
	var active, maximum atomic.Int32
	var sequenceMu sync.Mutex
	sequences := []uint64{}
	runner := &execution{
		runtime: runtime,
		ctx:     context.Background(),
		command: Command{RunID: "run", SessionID: "session"},
		sink: func(event Event) error {
			current := active.Add(1)
			defer active.Add(-1)
			for {
				previous := maximum.Load()
				if current <= previous || maximum.CompareAndSwap(previous, current) {
					break
				}
			}
			time.Sleep(time.Millisecond)
			sequenceMu.Lock()
			sequences = append(sequences, event.Sequence)
			sequenceMu.Unlock()
			return nil
		},
	}
	var wait sync.WaitGroup
	for i := 0; i < 24; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if err := runner.emit("test", map[string]any{}); err != nil {
				t.Errorf("emit: %v", err)
			}
		}()
	}
	wait.Wait()
	if maximum.Load() != 1 {
		t.Fatalf("event sink concurrency=%d", maximum.Load())
	}
	for index, sequence := range sequences {
		if sequence != uint64(index+1) {
			t.Fatalf("sequences=%v", sequences)
		}
	}

	sinkCalls := 0
	failed := &execution{runtime: runtime, ctx: context.Background(), command: Command{RunID: "run"}, sink: func(Event) error {
		sinkCalls++
		return errors.New("journal unavailable")
	}}
	if err := failed.emit("first", nil); err == nil || err.Error() != "journal unavailable" {
		t.Fatalf("first error=%v", err)
	}
	if err := failed.emit("second", nil); err == nil || err.Error() != "journal unavailable" || sinkCalls != 1 {
		t.Fatalf("sticky error=%v sinkCalls=%d", err, sinkCalls)
	}
}

func TestParallelToolsPrepareSeriallyAndReturnInCallOrder(t *testing.T) {
	var prepared []string
	runtime := New(t.TempDir())
	runtime.RegisterExtension(Extension{BeforeToolCall: func(_ context.Context, event ToolCallEvent) (ToolCallDecision, error) {
		prepared = append(prepared, event.Call.Name)
		return ToolCallDecision{}, nil
	}})
	tools := []ToolDefinition{
		{Name: "slow", ExecutionMode: "parallel", Execute: func(context.Context, json.RawMessage) (ToolResult, error) {
			time.Sleep(10 * time.Millisecond)
			return ToolResult{Text: "slow"}, nil
		}},
		{Name: "fast", ExecutionMode: "parallel", Execute: func(context.Context, json.RawMessage) (ToolResult, error) {
			return ToolResult{Text: "fast"}, nil
		}},
	}
	execution := &execution{
		runtime: runtime, ctx: context.Background(), command: Command{RunID: "run"},
		sink: func(Event) error { return nil }, approvedCalls: map[string]bool{},
		approvedHashes: map[string]bool{}, deniedHashes: map[string]bool{}, mcpApproval: map[string]bool{},
	}
	batch := execution.executeToolBatch(context.Background(), "agent", 0, []ToolCall{
		{ID: "1", Name: "slow", Arguments: json.RawMessage(`{}`)},
		{ID: "2", Name: "fast", Arguments: json.RawMessage(`{}`)},
	}, tools)
	if strings.Join(prepared, ",") != "slow,fast" {
		t.Fatalf("preparation order=%v", prepared)
	}
	if len(batch.messages) != 2 || batch.messages[0].Content != "slow" || batch.messages[1].Content != "fast" {
		t.Fatalf("message order=%+v", batch.messages)
	}
}

func TestSequentialToolResultMessagePrecedesNextToolStart(t *testing.T) {
	runtime := New(t.TempDir())
	eventTypes := []string{}
	execution := &execution{
		runtime: runtime, ctx: context.Background(), command: Command{RunID: "run"},
		sink: func(event Event) error {
			eventTypes = append(eventTypes, event.Type)
			return nil
		},
		approvedCalls: map[string]bool{}, approvedHashes: map[string]bool{},
		deniedHashes: map[string]bool{}, mcpApproval: map[string]bool{},
	}
	tools := []ToolDefinition{
		{Name: "one", ExecutionMode: "sequential", Execute: func(context.Context, json.RawMessage) (ToolResult, error) {
			return ToolResult{Text: "one"}, nil
		}},
		{Name: "two", ExecutionMode: "parallel", Execute: func(context.Context, json.RawMessage) (ToolResult, error) {
			return ToolResult{Text: "two"}, nil
		}},
	}
	execution.executeToolBatch(context.Background(), "agent", 0, []ToolCall{
		{ID: "1", Name: "one", Arguments: json.RawMessage(`{}`)},
		{ID: "2", Name: "two", Arguments: json.RawMessage(`{}`)},
	}, tools)
	wantPrefix := []string{
		"agent.tool_execution_start", "agent.tool_execution_end",
		"agent.message_start", "agent.message_end",
		"agent.tool_execution_start",
	}
	if len(eventTypes) < len(wantPrefix) || strings.Join(eventTypes[:len(wantPrefix)], ",") != strings.Join(wantPrefix, ",") {
		t.Fatalf("event order=%v", eventTypes)
	}
}

func TestExtensionToolRegistrationMatchesPiOverrideRules(t *testing.T) {
	builtin := []ToolDefinition{{Name: "read", Description: "builtin"}}
	firstExtension := extensionTools([]ToolDefinition{
		{Name: "read", Description: "first"},
		{Name: "extra", Description: "extra"},
		{Name: "read", Description: "last"},
	})
	for _, tool := range firstExtension {
		builtin = upsertTool(builtin, tool)
	}
	if len(builtin) != 2 || builtin[0].Description != "last" || builtin[1].Name != "extra" {
		t.Fatalf("extension registration=%+v", builtin)
	}
	// The runtime's claimed-name set prevents later extensions from replacing
	// the first extension that registered the same name.
	claimed := map[string]bool{"read": true}
	later := ToolDefinition{Name: "read", Description: "later extension"}
	if !claimed[later.Name] {
		builtin = upsertTool(builtin, later)
	}
	if builtin[0].Description != "last" {
		t.Fatalf("later extension replaced first extension: %+v", builtin)
	}
}

func TestSkillDiscoveryFollowsSymlinkDirectoriesWithoutCycles(t *testing.T) {
	root, target := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "SKILL.md"), []byte("---\ndescription: linked skill\n---\nUse it"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "linked-skill")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(root, filepath.Join(target, "cycle")); err != nil {
		t.Fatal(err)
	}
	skills := scanSkills(root, true)
	if len(skills) != 1 || skills[0].Name != "linked-skill" || skills[0].Description != "linked skill" {
		t.Fatalf("skills=%+v", skills)
	}
}

func TestSkillDiscoveryHonorsNestedIgnoreFilesAndNegation(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"ignored", "kept", "nested/blocked", "nested/allowed"} {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		content := "---\nname: " + strings.ReplaceAll(name, "/", "-") + "\ndescription: test\n---\nBody"
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("ignored/\nkept/\n!kept/\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", ".fdignore"), []byte("blocked/\n"), 0644); err != nil {
		t.Fatal(err)
	}
	skills := scanSkills(root, true)
	names := []string{}
	for _, skill := range skills {
		names = append(names, skill.Name)
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "kept,nested-allowed" {
		t.Fatalf("skills=%v", names)
	}
}

func TestPlanExtractionMatchesOfficialCleaningAndDoneMarkers(t *testing.T) {
	steps := extractPlan("Intro **Plan:**\n7. Use the `read` tool to inspect files\n9) Execute tests thoroughly\n10. /skip\n")
	if len(steps) != 2 || steps[0]["id"] != 1 || steps[0]["text"] != "Read tool to inspect files" || steps[1]["text"] != "Tests thoroughly" {
		t.Fatalf("steps=%+v", steps)
	}
	done := extractDone("[done:1] [DONE:3]")
	if len(done) != 2 || done[0] != 1 || done[1] != 3 {
		t.Fatalf("done=%v", done)
	}
}

func TestProjectContextLoadsGlobalAndAllAncestorsWithOfficialPrecedence(t *testing.T) {
	config, root := t.TempDir(), t.TempDir()
	t.Setenv("AGENT_CONFIG_DIR", config)
	child := filepath.Join(root, "one", "two")
	if err := os.MkdirAll(child, 0755); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		filepath.Join(config, "AGENTS.md"):      "global",
		filepath.Join(root, "AGENTS.md"):        "root",
		filepath.Join(root, "one", "AGENTS.md"): "parent-agents",
		filepath.Join(root, "one", "CLAUDE.md"): "parent-claude-ignored",
		filepath.Join(child, "CLAUDE.md"):       "child",
	} {
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	files := loadProjectContextFiles(child)
	if len(files) != 4 || files[0].Content != "global" || files[1].Content != "root" || files[2].Content != "parent-agents" || files[3].Content != "child" {
		t.Fatalf("files=%+v", files)
	}
}

func TestMCPImageResultIsBase64Encoded(t *testing.T) {
	result := mcpToolResult(&mcp.CallToolResult{Content: []mcp.Content{&mcp.ImageContent{Data: []byte{0, 1, 2}, MIMEType: "image/png"}}})
	if len(result.Images) != 1 || result.Images[0].Data != "AAEC" {
		t.Fatalf("images=%+v", result.Images)
	}
}

func TestImageProcessingResizesAndConvertsForInlineProviders(t *testing.T) {
	source := image.NewRGBA(image.Rect(0, 0, 2100, 100))
	for x := 0; x < 2100; x++ {
		source.Set(x, 0, color.RGBA{R: byte(x), A: 255})
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, source); err != nil {
		t.Fatal(err)
	}
	processed, err := processInlineImage(encoded.Bytes(), "image/bmp")
	if err != nil {
		t.Fatal(err)
	}
	if processed.Width != 2000 || processed.OriginalWidth != 2100 || processed.MIME != "image/png" || !strings.Contains(processed.Hint, "original 2100x100") {
		t.Fatalf("processed=%+v", processed)
	}
}

func TestUsageNormalizationPreservesRawAndAddsPiAccounting(t *testing.T) {
	openAI := normalizeUsage(map[string]any{"prompt_tokens": 100, "completion_tokens": 20, "total_tokens": 120, "prompt_tokens_details": map[string]any{"cached_tokens": 30}})
	if openAI["input"] != 70 || openAI["cacheRead"] != 30 || openAI["output"] != 20 || openAI["totalTokens"] != 120 || openAI["total_tokens"] != 120 {
		t.Fatalf("openai=%v", openAI)
	}
	anthropic := normalizeUsage(map[string]any{"input_tokens": 70, "output_tokens": 20, "cache_read_input_tokens": 30})
	if anthropic["input"] != 70 || anthropic["cacheRead"] != 30 || anthropic["totalTokens"] != 120 {
		t.Fatalf("anthropic=%v", anthropic)
	}
	priced := applyModelPricing(anthropic, ModelPricing{Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75}, true)
	cost := priced["cost"].(map[string]any)
	for key, want := range map[string]float64{"input": 0.00021, "output": 0.0003, "cacheRead": 0.000009, "total": 0.000519} {
		if math.Abs(cost[key].(float64)-want) > 1e-12 {
			t.Fatalf("cost=%v", cost)
		}
	}
}

func TestCustomMessageParticipatesInContextButCustomEntryDoesNot(t *testing.T) {
	entries := []map[string]any{
		{"type": "custom", "content": "state-only"},
		{"type": "custom_message", "customType": "notice", "content": "model-visible", "display": false},
	}
	messages := messagesFromEntries(entries)
	if len(messages) != 1 || messages[0].Role != "user" || messages[0].Content != "model-visible" {
		t.Fatalf("messages=%+v", messages)
	}
	if message, ok := entryMessage(entries[1]); !ok || message.Content != "model-visible" {
		t.Fatalf("compaction message=%+v ok=%v", message, ok)
	}
}

func TestEmptyActiveToolSetRemainsExplicitAcrossSessionRestore(t *testing.T) {
	settings := restoreSettings([]map[string]any{{"type": "custom", "customType": "active-tools", "data": []any{}}})
	if !settings.ActiveToolsSet || len(settings.ActiveTools) != 0 {
		t.Fatalf("settings=%+v", settings)
	}
	execution := &execution{}
	context := &ExtensionContext{execution: execution}
	context.SetActiveTools([]string{})
	_, _, _, tools, set := execution.runtimeOverrides()
	if !set || len(tools) != 0 {
		t.Fatalf("tools=%v set=%v", tools, set)
	}
}

func TestPiSessionStateRestoresModelFromLatestAssistantMessage(t *testing.T) {
	entries := []map[string]any{
		{"type": "model_change", "provider": "openai", "modelId": "old"},
		{"type": "message", "message": Message{Role: "assistant", Provider: "anthropic", Model: "claude-new", Timestamp: 123}},
	}
	settings := restoreSettings(entries)
	if settings.Provider != "anthropic" || settings.Model != "claude-new" {
		t.Fatalf("settings=%+v", settings)
	}
}

func TestExtensionQueuedMessageIsPersistedOnlyWhenInjected(t *testing.T) {
	execution := &execution{runtime: New(t.TempDir()), ctx: context.Background(), command: Command{SteeringMode: "one-at-a-time"}, sink: func(Event) error { return nil }}
	extension := &ExtensionContext{execution: execution}
	extension.SendUserMessage("steer", "steer")
	if len(execution.entries) != 0 {
		t.Fatal("queued message was persisted before injection")
	}
	messages := execution.injectQueuedMessages("steer", "agent", 0)
	if len(messages) != 1 || len(execution.entries) != 1 {
		t.Fatalf("messages=%d entries=%d", len(messages), len(execution.entries))
	}
}
