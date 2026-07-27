package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

type anthropicProvider struct {
	name, base, key string
	client          *http.Client
	headers         map[string]string
}

func (p *anthropicProvider) requestHeaders(in CompletionRequest) map[string]string {
	headers := cloneHeaders(p.headers)
	headers["anthropic-version"] = "2023-06-01"
	if strings.Contains(p.key, "sk-ant-oat") {
		delete(headers, "x-api-key")
		headers["Authorization"] = "Bearer " + p.key
		headers["anthropic-dangerous-direct-browser-access"] = "true"
		headers["anthropic-beta"] = "claude-code-20250219,oauth-2025-04-20"
		headers["x-app"] = "cli"
	} else if p.key != "" {
		headers["x-api-key"] = p.key
	}
	if in.ThinkingLevel != "" && in.ThinkingLevel != "off" && !usesAnthropicAdaptiveThinking(in.Model) && !strings.Contains(headers["anthropic-beta"], "interleaved-thinking-2025-05-14") {
		headers["anthropic-beta"] = strings.Trim(strings.Join([]string{headers["anthropic-beta"], "interleaved-thinking-2025-05-14"}, ","), ",")
	}
	return headers
}

func (p *anthropicProvider) Complete(ctx context.Context, in CompletionRequest) (Completion, error) {
	messages := make([]map[string]any, 0, len(in.Messages))
	for _, m := range in.Messages {
		replayState := canReplayProviderState(p.name, m, in)
		if m.Role == "tool" {
			blocks := []map[string]any{{"type": "text", "text": m.Content}}
			for _, image := range m.Images {
				blocks = append(blocks, map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": image.MimeType, "data": image.Data}})
			}
			toolResult := map[string]any{"type": "tool_result", "tool_use_id": m.ToolCallID, "content": blocks}
			if m.IsError {
				toolResult["is_error"] = true
			}
			messages = appendAnthropic(messages, map[string]any{"role": "user", "content": []map[string]any{toolResult}})
			continue
		}
		role := m.Role
		if role != "assistant" {
			role = "user"
		}
		content := []map[string]any{}
		if m.Thinking != "" {
			if replayState && m.ThinkingSignature != "" {
				content = append(content, map[string]any{"type": "thinking", "thinking": m.Thinking, "signature": m.ThinkingSignature})
			} else {
				content = append(content, map[string]any{"type": "text", "text": m.Thinking})
			}
		}
		if m.Content != "" {
			content = append(content, map[string]any{"type": "text", "text": m.Content})
		}
		for _, image := range m.Images {
			content = append(content, map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": image.MimeType, "data": image.Data}})
		}
		for _, call := range m.ToolCalls {
			var input any
			_ = json.Unmarshal(call.Arguments, &input)
			content = append(content, map[string]any{"type": "tool_use", "id": call.ID, "name": call.Name, "input": input})
		}
		messages = appendAnthropic(messages, map[string]any{"role": role, "content": content})
	}
	tools := make([]map[string]any, 0, len(in.Tools))
	for _, tool := range in.Tools {
		tools = append(tools, map[string]any{"name": tool.Name, "description": tool.Description, "input_schema": tool.Parameters})
	}
	maxTokens := firstPositive(in.MaxTokens, 8192)
	body := map[string]any{"model": in.Model, "system": in.System, "messages": messages, "max_tokens": maxTokens}
	applyAnthropicThinking(body, in.Model, in.ThinkingLevel, maxTokens)
	if len(tools) > 0 {
		body["tools"] = tools
	}
	var out struct {
		Content []struct {
			Type, Text, Thinking, Signature, ID, Name string
			Input                                     json.RawMessage `json:"input"`
		} `json:"content"`
		Usage       map[string]any `json:"usage"`
		StopReason  string         `json:"stop_reason"`
		StopDetails *struct {
			Explanation string `json:"explanation"`
		} `json:"stop_details"`
	}
	status, responseHeaders, err := doJSON(ctx, p.client, p.base+"/messages", mergeHeaders(p.requestHeaders(in), in.Headers), body, &out)
	if err != nil {
		return Completion{}, err
	}
	result := Completion{Usage: out.Usage, StopReason: normalizeStopReason(out.StopReason), ResponseStatus: status, ResponseHeaders: responseHeaders}
	if out.StopReason == "refusal" {
		result.ErrorMessage = "The model refused to complete the request"
		if out.StopDetails != nil && out.StopDetails.Explanation != "" {
			result.ErrorMessage = out.StopDetails.Explanation
		}
	}
	var texts []string
	for _, block := range out.Content {
		if block.Type == "text" {
			texts = append(texts, block.Text)
		}
		if block.Type == "thinking" {
			result.Thinking += block.Thinking
			result.ThinkingSignature += block.Signature
		}
		if block.Type == "tool_use" {
			result.ToolCalls = append(result.ToolCalls, ToolCall{ID: block.ID, Name: block.Name, Arguments: block.Input})
		}
	}
	result.Text = strings.Join(texts, "\n")
	return result, nil
}

func thinkingBudget(level string) int {
	switch level {
	case "minimal":
		return 1024
	case "low":
		return 2048
	case "medium":
		return 8192
	case "high", "xhigh", "max":
		return 16384
	default:
		return 0
	}
}

func applyAnthropicThinking(body map[string]any, model, level string, maxTokens int) {
	if level == "off" {
		body["thinking"] = map[string]any{"type": "disabled"}
		return
	}
	if level == "" {
		return
	}
	if usesAnthropicAdaptiveThinking(model) {
		body["thinking"] = map[string]any{"type": "adaptive", "display": "summarized"}
		body["output_config"] = map[string]any{"effort": anthropicEffort(model, level)}
		return
	}
	budget := min(thinkingBudget(level), max(0, maxTokens-1024))
	if budget >= 1024 {
		body["thinking"] = map[string]any{"type": "enabled", "budget_tokens": budget, "display": "summarized"}
	}
}

func usesAnthropicAdaptiveThinking(model string) bool {
	model = strings.ToLower(model)
	return strings.Contains(model, "claude-fable-5") ||
		strings.Contains(model, "claude-sonnet-5") ||
		strings.Contains(model, "claude-sonnet-4-6") ||
		strings.Contains(model, "claude-opus-4-6") ||
		strings.Contains(model, "claude-opus-4-7") ||
		strings.Contains(model, "claude-opus-4-8")
}

func anthropicEffort(model, level string) string {
	switch level {
	case "minimal", "low":
		return "low"
	case "medium":
		return "medium"
	case "xhigh":
		model = strings.ToLower(model)
		if strings.Contains(model, "claude-fable-5") ||
			strings.Contains(model, "claude-sonnet-5") ||
			strings.Contains(model, "claude-opus-4-7") ||
			strings.Contains(model, "claude-opus-4-8") {
			return "xhigh"
		}
		return "high"
	case "max":
		return "max"
	default:
		return "high"
	}
}

func appendAnthropic(messages []map[string]any, item map[string]any) []map[string]any {
	if len(messages) == 0 || messages[len(messages)-1]["role"] != item["role"] {
		return append(messages, item)
	}
	previous, _ := messages[len(messages)-1]["content"].([]map[string]any)
	current, _ := item["content"].([]map[string]any)
	messages[len(messages)-1]["content"] = append(previous, current...)
	return messages
}
