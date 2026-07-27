package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

type piMessagesProvider struct {
	name    string
	base    string
	client  *http.Client
	headers map[string]string
}

func (p *piMessagesProvider) ProviderHeaders(CompletionRequest) map[string]string {
	return cloneHeaders(p.headers)
}

func (p *piMessagesProvider) Complete(ctx context.Context, request CompletionRequest) (Completion, error) {
	return p.Stream(ctx, request, nil)
}

func (p *piMessagesProvider) Stream(ctx context.Context, request CompletionRequest, onDelta func(CompletionDelta) error) (Completion, error) {
	body := map[string]any{
		"model": request.Model,
		"context": map[string]any{
			"systemPrompt": request.System,
			"messages":     piMessagesHistory(request.Messages),
			"tools":        piMessagesTools(request.Tools),
		},
		"options": map[string]any{
			"maxTokens": request.MaxTokens,
			"reasoning": request.ThinkingLevel,
			"sessionId": request.SessionID,
		},
	}
	result := Completion{}
	calls := map[int]*toolCallAccumulator{}
	terminal := false
	status, responseHeaders, err := streamJSON(ctx, p.client, p.base+"/messages", mergeHeaders(cloneHeaders(p.headers), request.Headers), body, func(data []byte) error {
		var event struct {
			Type             string          `json:"type"`
			ContentIndex     int             `json:"contentIndex"`
			Delta            string          `json:"delta"`
			Content          string          `json:"content"`
			ContentSignature string          `json:"contentSignature"`
			ID               string          `json:"id"`
			ToolName         string          `json:"toolName"`
			ToolCall         json.RawMessage `json:"toolCall"`
			Reason           string          `json:"reason"`
			Usage            map[string]any  `json:"usage"`
			ErrorMessage     string          `json:"errorMessage"`
		}
		if err := json.Unmarshal(data, &event); err != nil {
			return err
		}
		switch event.Type {
		case "text_delta":
			result.Text += event.Delta
			if onDelta != nil {
				return onDelta(CompletionDelta{Text: event.Delta})
			}
		case "text_end":
			if result.Text == "" {
				result.Text = event.Content
			}
			if event.ContentSignature != "" {
				result.TextSignature = event.ContentSignature
			}
		case "thinking_delta":
			result.Thinking += event.Delta
			if onDelta != nil {
				return onDelta(CompletionDelta{Thinking: event.Delta})
			}
		case "thinking_end":
			if result.Thinking == "" {
				result.Thinking = event.Content
			}
			if event.ContentSignature != "" {
				result.ThinkingSignature = event.ContentSignature
			}
		case "toolcall_start":
			calls[event.ContentIndex] = &toolCallAccumulator{ID: event.ID, Name: event.ToolName}
			if onDelta != nil {
				return onDelta(CompletionDelta{ToolCallID: event.ID, ToolName: event.ToolName})
			}
		case "toolcall_delta":
			call := calls[event.ContentIndex]
			if call == nil {
				call = &toolCallAccumulator{}
				calls[event.ContentIndex] = call
			}
			call.Arguments.WriteString(event.Delta)
			if onDelta != nil {
				return onDelta(CompletionDelta{ToolCallID: call.ID, ToolName: call.Name, ToolArgumentsDelta: event.Delta})
			}
		case "toolcall_end":
			var call struct {
				ID               string          `json:"id"`
				Name             string          `json:"name"`
				Arguments        json.RawMessage `json:"arguments"`
				ThoughtSignature string          `json:"thoughtSignature"`
			}
			if len(event.ToolCall) > 0 {
				if err := json.Unmarshal(event.ToolCall, &call); err != nil {
					return err
				}
				accumulator := calls[event.ContentIndex]
				if accumulator == nil {
					accumulator = &toolCallAccumulator{}
					calls[event.ContentIndex] = accumulator
				}
				accumulator.ID, accumulator.Name = call.ID, call.Name
				if len(call.Arguments) > 0 {
					accumulator.Arguments.Reset()
					accumulator.Arguments.Write(call.Arguments)
				}
			}
		case "done":
			terminal = true
			result.StopReason = normalizeStopReason(event.Reason)
			result.Usage = event.Usage
		case "error":
			terminal = true
			return fmt.Errorf("pi-messages: %s", first(event.ErrorMessage, "provider returned an error"))
		}
		return nil
	})
	result.ToolCalls = toolCallsFromAccumulators(calls)
	result.ResponseStatus, result.ResponseHeaders = status, responseHeaders
	if err != nil {
		return result, err
	}
	if !terminal {
		return result, fmt.Errorf("pi-messages stream ended before a terminal event")
	}
	return result, nil
}

func piMessagesHistory(messages []Message) []map[string]any {
	result := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		switch message.Role {
		case "assistant":
			content := []map[string]any{}
			if message.Thinking != "" {
				block := map[string]any{"type": "thinking", "thinking": message.Thinking}
				if message.ThinkingSignature != "" {
					block["thinkingSignature"] = message.ThinkingSignature
				}
				content = append(content, block)
			}
			if message.Content != "" {
				block := map[string]any{"type": "text", "text": message.Content}
				if message.TextSignature != "" {
					block["textSignature"] = message.TextSignature
				}
				content = append(content, block)
			}
			for _, call := range message.ToolCalls {
				var arguments any = map[string]any{}
				if len(call.Arguments) > 0 {
					_ = json.Unmarshal(call.Arguments, &arguments)
				}
				block := map[string]any{"type": "toolCall", "id": call.ID, "name": call.Name, "arguments": arguments}
				if signature, _ := call.Metadata["thoughtSignature"].(string); signature != "" {
					block["thoughtSignature"] = signature
				}
				content = append(content, block)
			}
			result = append(result, map[string]any{
				"role":      "assistant",
				"content":   content,
				"api":       first(message.API, string(ProviderAPIPiMessages)),
				"provider":  message.Provider,
				"model":     message.Model,
				"timestamp": message.Timestamp,
			})
		case "tool":
			result = append(result, map[string]any{
				"role":       "toolResult",
				"toolCallId": message.ToolCallID,
				"toolName":   message.ToolName,
				"content":    piMessageContent(message),
				"isError":    message.IsError,
				"timestamp":  message.Timestamp,
			})
		default:
			result = append(result, map[string]any{
				"role":      "user",
				"content":   piMessageContent(message),
				"timestamp": message.Timestamp,
			})
		}
	}
	return result
}

func piMessageContent(message Message) []map[string]any {
	content := []map[string]any{}
	if message.Content != "" {
		content = append(content, map[string]any{"type": "text", "text": message.Content})
	}
	for _, image := range message.Images {
		content = append(content, map[string]any{"type": "image", "data": image.Data, "mimeType": image.MimeType})
	}
	return content
}

func piMessagesTools(tools []ToolDefinition) []map[string]any {
	result := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		result = append(result, map[string]any{"name": tool.Name, "description": tool.Description, "parameters": tool.Parameters})
	}
	return result
}
