package provider

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

type openAIProvider struct {
	base, key string
	client    *http.Client
	dialect   string
	headers   map[string]string
}

type mistralProvider struct{ openAIProvider }

func (p *openAIProvider) Complete(ctx context.Context, in CompletionRequest) (Completion, error) {
	in.Messages = normalizeMistralMessages(p.dialect, in.Messages)
	messages := make([]map[string]any, 0, len(in.Messages)+1)
	messages = append(messages, map[string]any{"role": "system", "content": in.System})
	for _, m := range in.Messages {
		replayState := canReplayProviderState(p.dialect, m, in)
		item := map[string]any{"role": m.Role}
		if p.dialect == "mistral" && m.Role == "assistant" && m.Thinking != "" {
			content := []map[string]any{{"type": "thinking", "thinking": []map[string]any{{"type": "text", "text": m.Thinking}}}}
			if m.Content != "" {
				content = append(content, map[string]any{"type": "text", "text": m.Content})
			}
			item["content"] = content
		} else if len(m.Images) > 0 && (m.Role == "user" || m.Role == "tool") {
			content := []map[string]any{}
			if m.Content != "" {
				content = append(content, map[string]any{"type": "text", "text": m.Content})
			}
			for _, image := range m.Images {
				content = append(content, map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:" + image.MimeType + ";base64," + image.Data}})
			}
			item["content"] = content
		} else if m.Content != "" {
			item["content"] = m.Content
		}
		if replayState && p.dialect != "mistral" && m.Role == "assistant" && m.Thinking != "" {
			if field := openAIReplayReasoningField(m, p.dialect, in.Model); field != "" {
				item[field] = m.Thinking
			}
		}
		if m.ToolCallID != "" {
			item["tool_call_id"] = m.ToolCallID
		}
		if len(m.ToolCalls) > 0 {
			calls := make([]map[string]any, 0, len(m.ToolCalls))
			reasoningDetails := make([]any, 0, len(m.ToolCalls))
			for _, call := range m.ToolCalls {
				calls = append(calls, map[string]any{"id": call.ID, "type": "function", "function": map[string]any{"name": call.Name, "arguments": string(call.Arguments)}})
				if raw, _ := call.Metadata["thoughtSignature"].(string); replayState && raw != "" {
					var detail any
					if json.Unmarshal([]byte(raw), &detail) == nil {
						reasoningDetails = append(reasoningDetails, detail)
					}
				}
			}
			item["tool_calls"] = calls
			if len(reasoningDetails) > 0 {
				item["reasoning_details"] = reasoningDetails
			}
		}
		messages = append(messages, item)
	}
	tools := make([]map[string]any, 0, len(in.Tools))
	for _, tool := range in.Tools {
		tools = append(tools, map[string]any{"type": "function", "function": map[string]any{"name": tool.Name, "description": tool.Description, "parameters": tool.Parameters}})
	}
	body := map[string]any{"model": in.Model, "messages": messages}
	if p.dialect == "mistral" && in.SessionID != "" {
		body["prompt_cache_key"] = in.SessionID
	}
	if in.MaxTokens > 0 {
		if p.dialect == "mistral" {
			body["max_tokens"] = in.MaxTokens
		} else if usesOpenAIMaxCompletionTokens(p.dialect) {
			body["max_completion_tokens"] = in.MaxTokens
		} else {
			body["max_tokens"] = in.MaxTokens
		}
	}
	if p.dialect == "mistral" {
		applyMistralReasoning(body, in.Model, in.ThinkingLevel)
	} else if effort := openAIEffort(in.ThinkingLevel); effort != "" && supportsOpenAIReasoningEffort(p.dialect) {
		body["reasoning_effort"] = effort
	}
	if len(tools) > 0 {
		body["tools"] = tools
	}
	var out struct {
		Error *struct {
			Code, Type, Message string
		} `json:"error"`
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Usage        map[string]any
			Message      struct {
				Content          any               `json:"content"`
				ReasoningContent string            `json:"reasoning_content"`
				Reasoning        string            `json:"reasoning"`
				ReasoningText    string            `json:"reasoning_text"`
				ReasoningDetails []json.RawMessage `json:"reasoning_details"`
				ToolCalls        []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string          `json:"name"`
						Arguments json.RawMessage `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage map[string]any `json:"usage"`
	}
	headers := cloneHeaders(p.headers)
	if p.key != "" {
		headers["Authorization"] = "Bearer " + p.key
	}
	if p.dialect == "mistral" && in.SessionID != "" {
		headers["x-affinity"] = in.SessionID
	}
	if p.dialect == "cloudflare-workers-ai" && in.SessionID != "" {
		headers["x-session-affinity"] = in.SessionID
	}
	headers = mergeHeaders(headers, in.Headers)
	status, responseHeaders, err := doJSON(ctx, p.client, p.base+"/chat/completions", headers, body, &out)
	if err != nil {
		return Completion{}, err
	}
	if out.Error != nil {
		return Completion{}, fmt.Errorf("provider %s: %s", first(out.Error.Code, out.Error.Type, "error"), first(out.Error.Message, "provider returned an error"))
	}
	if len(out.Choices) == 0 {
		return Completion{}, fmt.Errorf("provider returned no choices")
	}
	message := out.Choices[0].Message
	thinking, thinkingSignature := openAIReasoningValue(message.ReasoningContent, message.Reasoning, message.ReasoningText)
	if thinking == "" {
		thinking = contentThinking(message.Content)
	}
	usage := out.Usage
	if usage == nil {
		usage = out.Choices[0].Usage
	}
	stopReason, errorMessage := openAICompatibleStopReason(p.dialect, out.Choices[0].FinishReason)
	result := Completion{Text: contentText(message.Content), Thinking: thinking, ThinkingSignature: thinkingSignature, Usage: usage, StopReason: stopReason, ErrorMessage: errorMessage, ResponseStatus: status, ResponseHeaders: responseHeaders}
	for _, call := range out.Choices[0].Message.ToolCalls {
		result.ToolCalls = append(result.ToolCalls, ToolCall{ID: call.ID, Name: call.Function.Name, Arguments: call.Function.Arguments})
	}
	applyOpenAIReasoningDetails(result.ToolCalls, message.ReasoningDetails)
	if errorMessage != "" {
		return result, fmt.Errorf("%s", errorMessage)
	}
	return result, nil
}

func openAIReplayReasoningField(message Message, dialect, model string) string {
	switch message.ThinkingSignature {
	case "reasoning_content", "reasoning", "reasoning_text":
		return message.ThinkingSignature
	}
	if requiresReasoningContent(dialect, model) {
		return "reasoning_content"
	}
	return ""
}

func openAIReasoningValue(reasoningContent, reasoning, reasoningText string) (string, string) {
	switch {
	case reasoningContent != "":
		return reasoningContent, "reasoning_content"
	case reasoning != "":
		return reasoning, "reasoning"
	case reasoningText != "":
		return reasoningText, "reasoning_text"
	default:
		return "", ""
	}
}

func openAICompatibleStopReason(dialect, reason string) (string, string) {
	reason = strings.ToLower(strings.TrimSpace(reason))
	if reason == "" {
		return "", ""
	}
	if dialect == "mistral" {
		switch reason {
		case "stop":
			return "stop", ""
		case "length", "model_length":
			return "length", ""
		case "tool_calls":
			return "toolUse", ""
		case "error":
			return "error", "Provider finish_reason: error"
		default:
			return "stop", ""
		}
	}
	switch reason {
	case "stop", "end":
		return "stop", ""
	case "length":
		return "length", ""
	case "function_call", "tool_calls":
		return "toolUse", ""
	default:
		return "error", "Provider finish_reason: " + reason
	}
}

func applyOpenAIReasoningDetails(calls []ToolCall, details []json.RawMessage) {
	for _, raw := range details {
		var detail struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(raw, &detail) != nil || detail.ID == "" {
			continue
		}
		for index := range calls {
			if calls[index].ID != detail.ID {
				continue
			}
			if calls[index].Metadata == nil {
				calls[index].Metadata = map[string]any{}
			}
			calls[index].Metadata["thoughtSignature"] = string(raw)
			break
		}
	}
}

func normalizeMistralMessages(dialect string, messages []Message) []Message {
	if dialect != "mistral" {
		return messages
	}
	copyMessages := make([]Message, len(messages))
	copy(copyMessages, messages)
	messages = copyMessages
	ids := map[string]string{}
	normalize := func(id string) string {
		if value := ids[id]; value != "" {
			return value
		}
		clean := regexp.MustCompile(`[^a-zA-Z0-9]`).ReplaceAllString(id, "")
		if len(clean) != 9 {
			sum := sha256.Sum256([]byte(id))
			clean = fmt.Sprintf("%x", sum)[:9]
		}
		ids[id] = clean
		return clean
	}
	for i := range messages {
		messages[i].ToolCalls = append([]ToolCall(nil), messages[i].ToolCalls...)
		if messages[i].ToolCallID != "" {
			messages[i].ToolCallID = normalize(messages[i].ToolCallID)
		}
		for j := range messages[i].ToolCalls {
			messages[i].ToolCalls[j].ID = normalize(messages[i].ToolCalls[j].ID)
		}
	}
	return messages
}

func applyMistralReasoning(body map[string]any, model, level string) {
	if level == "" || level == "off" {
		return
	}
	switch model {
	case "mistral-small-2603", "mistral-small-latest", "mistral-medium-3.5":
		body["reasoning_effort"] = "high"
	default:
		body["prompt_mode"] = "reasoning"
	}
}

func usesOpenAIMaxCompletionTokens(dialect string) bool {
	switch dialect {
	case "openai", "openai-chat", "openai-completions":
		return true
	default:
		return false
	}
}

func supportsOpenAIReasoningEffort(dialect string) bool {
	switch dialect {
	case "openai", "openai-chat", "openai-completions", "xai":
		return true
	default:
		return false
	}
}

func requiresReasoningContent(dialect, model string) bool {
	if dialect == "deepseek" {
		return true
	}
	return (dialect == "opencode" || dialect == "opencode-go") && strings.HasPrefix(strings.ToLower(model), "deepseek-")
}
