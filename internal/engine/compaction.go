package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const (
	defaultContextWindow        = 200000
	defaultCompactionReserve    = 16384
	defaultCompactionKeepRecent = 20000
	compactionImageChars        = 4800
	compactionToolResultChars   = 2000
)

const compactionSummaryPrefix = `The conversation history before this point was compacted into the following summary:

<summary>
`

const compactionSummarySuffix = `
</summary>`

const branchSummaryPrefix = `The following is a summary of a branch that this conversation came back from:

<summary>
`

const branchSummarySuffix = `</summary>`

const summarizationSystemPrompt = `You are a context summarization assistant. Your task is to read a conversation between a user and an AI assistant, then produce a structured summary following the exact format specified.

Do NOT continue the conversation. Do NOT respond to any questions in the conversation. ONLY output the structured summary.`

const summarizationPrompt = `The messages above are a conversation to summarize. Create a structured context checkpoint summary that another LLM will use to continue the work.

Use this EXACT format:

## Goal
[What is the user trying to accomplish? Can be multiple items if the session covers different tasks.]

## Constraints & Preferences
- [Any constraints, preferences, or requirements mentioned by user]
- [Or "(none)" if none were mentioned]

## Progress
### Done
- [x] [Completed tasks/changes]

### In Progress
- [ ] [Current work]

### Blocked
- [Issues preventing progress, if any]

## Key Decisions
- **[Decision]**: [Brief rationale]

## Next Steps
1. [Ordered list of what should happen next]

## Critical Context
- [Any data, examples, or references needed to continue]
- [Or "(none)" if not applicable]

Keep each section concise. Preserve exact file paths, function names, and error messages.`

const updateSummarizationPrompt = `The messages above are NEW conversation messages to incorporate into the existing summary provided in <previous-summary> tags.

Update the existing structured summary with new information. RULES:
- PRESERVE all existing information from the previous summary
- ADD new progress, decisions, and context from the new messages
- UPDATE the Progress section: move items from "In Progress" to "Done" when completed
- UPDATE "Next Steps" based on what was accomplished
- PRESERVE exact file paths, function names, and error messages
- If something is no longer relevant, you may remove it

Use this EXACT format:

## Goal
[Preserve existing goals, add new ones if the task expanded]

## Constraints & Preferences
- [Preserve existing, add new ones discovered]

## Progress
### Done
- [x] [Include previously done items AND newly completed items]

### In Progress
- [ ] [Current work - update based on progress]

### Blocked
- [Current blockers - remove if resolved]

## Key Decisions
- **[Decision]**: [Brief rationale] (preserve all previous, add new)

## Next Steps
1. [Update based on current state]

## Critical Context
- [Preserve important context, add new if needed]

Keep each section concise. Preserve exact file paths, function names, and error messages.`

const branchSummaryPrompt = `Create a structured summary of this conversation branch for context when returning later.

Use this EXACT format:

## Goal
[What was the user trying to accomplish in this branch?]

## Constraints & Preferences
- [Any constraints, preferences, or requirements mentioned]
- [Or "(none)" if none were mentioned]

## Progress
### Done
- [x] [Completed tasks/changes]

### In Progress
- [ ] [Work that was started but not finished]

### Blocked
- [Issues preventing progress, if any]

## Key Decisions
- **[Decision]**: [Brief rationale]

## Next Steps
1. [What should happen next to continue this work]

Keep each section concise. Preserve exact file paths, function names, and error messages.`

const turnPrefixSummarizationPrompt = `This is the PREFIX of a turn that was too large to keep. The SUFFIX (recent work) is retained.

Summarize the prefix to provide context for the retained suffix:

## Original Request
[What did the user ask for in this turn?]

## Early Progress
- [Key decisions and work done in the prefix]

## Context for Suffix
- [Information needed to understand the retained recent work]

Be concise. Focus on what's needed to understand the kept suffix.`

type compactionPreparation struct {
	FirstKeptID     string
	Messages        []Message
	TurnPrefix      []Message
	RetainedTail    []Message
	SplitTurn       bool
	PreviousSummary string
	TokensBefore    int
	ReadFiles       []string
	ModifiedFiles   []string
}

func estimateMessageTokens(message Message) int {
	chars := len([]rune(message.Content)) + len([]rune(message.Thinking)) + len(message.Images)*compactionImageChars
	for _, call := range message.ToolCalls {
		chars += len([]rune(call.Name)) + len(call.Arguments)
	}
	return (chars + 3) / 4
}

func usageTokens(usage map[string]any) int {
	for _, key := range []string{"totalTokens", "total_tokens"} {
		if value := number(usage[key]); value > 0 {
			return value
		}
	}
	return number(usage["input"]) + number(usage["output"]) + number(usage["cacheRead"]) + number(usage["cacheWrite"])
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

func estimateContextTokens(messages []Message) int {
	lastUsage, lastIndex := 0, -1
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "assistant" && messages[i].StopReason != "aborted" && messages[i].StopReason != "error" && usageTokens(messages[i].Usage) > 0 {
			lastUsage, lastIndex = usageTokens(messages[i].Usage), i
			break
		}
	}
	if lastIndex < 0 {
		for _, message := range messages {
			lastUsage += estimateMessageTokens(message)
		}
		return lastUsage
	}
	for _, message := range messages[lastIndex+1:] {
		lastUsage += estimateMessageTokens(message)
	}
	return lastUsage
}

func prepareCompaction(entries []map[string]any, keepRecent int) *compactionPreparation {
	if len(entries) == 0 || entries[len(entries)-1]["type"] == "compaction" {
		return nil
	}
	boundary := 0
	previous := ""
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i]["type"] == "compaction" {
			previous, _ = entries[i]["summary"].(string)
			first, _ := entries[i]["firstKeptEntryId"].(string)
			for j := range entries {
				if entries[j]["id"] == first {
					boundary = j
					break
				}
			}
			break
		}
	}
	all := messagesFromEntries(entries)
	tokensBefore := estimateContextTokens(all)
	validCuts := []int{}
	for i := boundary; i < len(entries); i++ {
		typeName, _ := entries[i]["type"].(string)
		if typeName == "branch_summary" || typeName == "custom_message" {
			validCuts = append(validCuts, i)
			continue
		}
		if message, ok := entryMessage(entries[i]); ok && (message.Role == "user" || message.Role == "assistant") {
			validCuts = append(validCuts, i)
		}
	}
	if len(validCuts) == 0 {
		return nil
	}
	accumulated, cut := 0, validCuts[0]
	for i := len(entries) - 1; i >= boundary; i-- {
		message, ok := entryMessage(entries[i])
		if !ok {
			continue
		}
		accumulated += estimateMessageTokens(message)
		if accumulated >= keepRecent {
			for _, candidate := range validCuts {
				if candidate >= i {
					cut = candidate
					break
				}
			}
			break
		}
	}
	for cut > boundary && entries[cut-1]["type"] != "message" && entries[cut-1]["type"] != "compaction" {
		cut--
	}
	if cut >= len(entries) {
		return nil
	}
	turnStart := -1
	cutMessage, cutIsMessage := entryMessage(entries[cut])
	if !cutIsMessage || cutMessage.Role != "user" {
		for i := cut; i >= boundary; i-- {
			if entries[i]["type"] == "branch_summary" || entries[i]["type"] == "custom_message" {
				turnStart = i
				break
			}
			if message, ok := entryMessage(entries[i]); ok && message.Role == "user" {
				turnStart = i
				break
			}
		}
	}
	splitTurn := turnStart >= 0
	historyEnd := cut
	if splitTurn {
		historyEnd = turnStart
	}
	firstID, _ := entries[cut]["id"].(string)
	if firstID == "" {
		return nil
	}
	var summarized []Message
	reads, writes := map[string]bool{}, map[string]bool{}
	for i := boundary; i < historyEnd; i++ {
		if message, ok := entryMessage(entries[i]); ok {
			summarized = append(summarized, message)
			for _, call := range message.ToolCalls {
				var args map[string]any
				_ = json.Unmarshal(call.Arguments, &args)
				path, _ := args["path"].(string)
				if path == "" {
					continue
				}
				switch call.Name {
				case "read":
					reads[path] = true
				case "write", "edit":
					writes[path] = true
				}
			}
		}
	}
	turnPrefix := []Message{}
	if splitTurn {
		for i := turnStart; i < cut; i++ {
			if message, ok := entryMessage(entries[i]); ok {
				turnPrefix = append(turnPrefix, message)
			}
		}
	}
	retainedTail := []Message{}
	for i := cut; i < len(entries); i++ {
		if message, ok := entryMessage(entries[i]); ok {
			retainedTail = append(retainedTail, message)
		}
	}
	if len(summarized) == 0 && len(turnPrefix) == 0 {
		return nil
	}
	readFiles, modifiedFiles := []string{}, []string{}
	for path := range reads {
		if !writes[path] {
			readFiles = append(readFiles, path)
		}
	}
	for path := range writes {
		modifiedFiles = append(modifiedFiles, path)
	}
	sort.Strings(readFiles)
	sort.Strings(modifiedFiles)
	return &compactionPreparation{FirstKeptID: firstID, Messages: summarized, TurnPrefix: turnPrefix, RetainedTail: retainedTail, SplitTurn: splitTurn, PreviousSummary: previous, TokensBefore: tokensBefore, ReadFiles: readFiles, ModifiedFiles: modifiedFiles}
}

func entryMessage(entry map[string]any) (Message, bool) {
	if entry["type"] == "custom_message" {
		content, ok := entry["content"].(string)
		return Message{Role: "user", Content: content, Timestamp: entryTimestamp(entry)}, ok
	}
	if entry["type"] != "message" {
		return Message{}, false
	}
	data, _ := json.Marshal(entry["message"])
	var message Message
	if json.Unmarshal(data, &message) != nil || message.Role == "" {
		return Message{}, false
	}
	return message, true
}

func serializeConversation(messages []Message) string {
	parts := make([]string, 0, len(messages))
	for _, message := range messages {
		content := message.Content
		if message.Role == "tool" && len([]rune(content)) > compactionToolResultChars {
			runes := []rune(content)
			content = string(runes[:compactionToolResultChars]) + fmt.Sprintf("\n\n[... %d more characters truncated]", len(runes)-compactionToolResultChars)
		}
		switch message.Role {
		case "user":
			parts = append(parts, "[User]: "+content)
		case "assistant":
			if message.Thinking != "" {
				parts = append(parts, "[Assistant thinking]: "+message.Thinking)
			}
			if content != "" {
				parts = append(parts, "[Assistant]: "+content)
			}
			if len(message.ToolCalls) > 0 {
				calls := []string{}
				for _, call := range message.ToolCalls {
					calls = append(calls, call.Name+"("+string(call.Arguments)+")")
				}
				parts = append(parts, "[Assistant tool calls]: "+strings.Join(calls, "; "))
			}
		case "tool":
			parts = append(parts, "[Tool result]: "+content)
		}
	}
	return strings.Join(parts, "\n\n")
}

func (e *execution) compact(ctx context.Context, provider Provider, model, thinking, custom string) (bool, error) {
	return e.compactWithReason(ctx, provider, model, thinking, custom, "manual", false, false)
}

func (e *execution) compactWithReason(ctx context.Context, provider Provider, model, thinking, custom, reason string, willRetry, fromExtension bool) (bool, error) {
	keep := e.command.CompactionKeepRecent
	if keep <= 0 {
		keep = defaultCompactionKeepRecent
	}
	preparation := prepareCompaction(e.branch(), keep)
	if preparation == nil {
		return false, nil
	}
	for _, extension := range e.runtime.extensions() {
		if extension.BeforeCompact == nil {
			continue
		}
		event := &CompactionEvent{Preparation: preparation, BranchEntries: e.branch(), CustomInstructions: custom, Reason: reason, WillRetry: willRetry}
		decision, err := extension.BeforeCompact(ctx, event)
		if err != nil {
			return false, err
		}
		custom = event.CustomInstructions
		if decision.CustomInstructions != "" {
			custom = decision.CustomInstructions
		}
		if decision.Cancel {
			return false, nil
		}
	}
	reserve := e.command.CompactionReserve
	if reserve <= 0 {
		reserve = defaultCompactionReserve
	}
	summarize := func(messages []Message, previous, instructions string, maxTokens int) (Completion, error) {
		prompt := "<conversation>\n" + serializeConversation(messages) + "\n</conversation>\n\n"
		if previous != "" {
			prompt += "<previous-summary>\n" + previous + "\n</previous-summary>\n\n"
		}
		return e.complete(ctx, provider, CompletionRequest{SessionID: e.command.SessionID, Model: model, System: summarizationSystemPrompt, Messages: []Message{{Role: "user", Content: prompt + instructions}}, ThinkingLevel: thinking, MaxTokens: min(maxTokens, firstPositive(e.command.MaxOutputTokens, maxTokens))}, nil, nil)
	}
	base := summarizationPrompt
	if preparation.PreviousSummary != "" {
		base = updateSummarizationPrompt
	}
	if custom != "" {
		base += "\n\nAdditional focus: " + custom
	}
	summary := ""
	usage := map[string]any(nil)
	if preparation.SplitTurn && len(preparation.TurnPrefix) > 0 {
		history := "No prior history."
		if len(preparation.Messages) > 0 {
			completion, err := summarize(preparation.Messages, preparation.PreviousSummary, base, reserve*8/10)
			if err != nil {
				return false, err
			}
			history, usage = completion.Text, completion.Usage
		}
		prefix, err := summarize(preparation.TurnPrefix, "", turnPrefixSummarizationPrompt, reserve/2)
		if err != nil {
			return false, err
		}
		summary = history + "\n\n---\n\n**Turn Context (split turn):**\n\n" + prefix.Text
		usage = combineUsageMaps(usage, prefix.Usage)
	} else {
		completion, err := summarize(preparation.Messages, preparation.PreviousSummary, base, reserve*8/10)
		if err != nil {
			return false, err
		}
		summary, usage = completion.Text, completion.Usage
	}
	if len(preparation.ReadFiles) > 0 {
		summary += "\n\n<read-files>\n" + strings.Join(preparation.ReadFiles, "\n") + "\n</read-files>"
	}
	if len(preparation.ModifiedFiles) > 0 {
		summary += "\n\n<modified-files>\n" + strings.Join(preparation.ModifiedFiles, "\n") + "\n</modified-files>"
	}
	entry := map[string]any{"summary": summary, "firstKeptEntryId": preparation.FirstKeptID, "retainedTail": preparation.RetainedTail, "tokensBefore": preparation.TokensBefore, "usage": usage, "details": map[string]any{"readFiles": preparation.ReadFiles, "modifiedFiles": preparation.ModifiedFiles}}
	e.appendEntry("compaction", entry)
	_ = e.emit("session.compacted", map[string]any{"summary": summary, "firstKeptEntryId": preparation.FirstKeptID, "tokensBefore": preparation.TokensBefore, "usage": usage})
	for _, extension := range e.runtime.extensions() {
		if extension.AfterCompact != nil {
			if err := extension.AfterCompact(ctx, CompactedEvent{Entry: entry, FromExtension: fromExtension, Reason: reason, WillRetry: willRetry}); err != nil {
				return false, err
			}
		}
	}
	return true, nil
}

func combineUsageMaps(firstUsage, secondUsage map[string]any) map[string]any {
	if firstUsage == nil {
		return secondUsage
	}
	if secondUsage == nil {
		return firstUsage
	}
	result := map[string]any{}
	for key, value := range firstUsage {
		result[key] = value
	}
	for key, value := range secondUsage {
		if nested, ok := value.(map[string]any); ok {
			existing, _ := result[key].(map[string]any)
			result[key] = combineUsageMaps(existing, nested)
			continue
		}
		if _, exists := result[key]; exists {
			result[key] = number(result[key]) + number(value)
		} else {
			result[key] = value
		}
	}
	return result
}

func firstPositive(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func (e *execution) summarizeAbandonedBranch(ctx context.Context, target string) error {
	current := e.branch()
	targetBranch := branchEntries(append([]map[string]any(nil), e.entries...), target)
	targetIDs := map[string]bool{}
	for _, entry := range targetBranch {
		if id, _ := entry["id"].(string); id != "" {
			targetIDs[id] = true
		}
	}
	start := 0
	for i := len(current) - 1; i >= 0; i-- {
		if id, _ := current[i]["id"].(string); targetIDs[id] {
			start = i + 1
			break
		}
	}
	if start >= len(current) {
		e.leafID = target
		return nil
	}
	messages := []Message{}
	for _, entry := range current[start:] {
		if message, ok := entryMessage(entry); ok {
			messages = append(messages, message)
		}
	}
	if len(messages) == 0 {
		e.leafID = target
		return nil
	}
	provider, err := providerFromFactory(e.runtime.Providers, e.command.Provider, e.command.Model, e.command.ProviderTimeoutMS)
	if err != nil {
		return err
	}
	instructions := branchSummaryPrompt
	if e.command.CustomInstructions != "" {
		if e.command.ReplaceInstructions {
			instructions = e.command.CustomInstructions
		} else {
			instructions += "\n\nAdditional focus: " + e.command.CustomInstructions
		}
	}
	prompt := "<conversation>\n" + serializeConversation(messages) + "\n</conversation>\n\n" + instructions
	completion, err := e.complete(ctx, provider, CompletionRequest{SessionID: e.command.SessionID, Model: e.command.Model, System: summarizationSystemPrompt, Messages: []Message{{Role: "user", Content: prompt}}, ThinkingLevel: e.command.ThinkingLevel}, nil, nil)
	if err != nil {
		return err
	}
	fromID, _ := current[len(current)-1]["id"].(string)
	e.leafID = target
	e.appendEntry("branch_summary", map[string]any{"summary": "The user explored a different conversation branch before returning here.\nSummary of that exploration:\n\n" + completion.Text, "fromId": fromID, "usage": completion.Usage})
	return e.emit("session.branch_summarized", map[string]any{"fromId": fromID, "targetEntryId": target, "usage": completion.Usage})
}
