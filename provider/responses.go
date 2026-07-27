package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

type responsesProvider struct {
	base                string
	headers             map[string]string
	client              *http.Client
	dialect, apiVersion string
	modelMap            map[string]string
}

func (p *responsesProvider) ProviderHeaders(in CompletionRequest) map[string]string {
	return p.requestHeaders(in)
}

func (p *responsesProvider) request(in CompletionRequest, stream bool) map[string]any {
	input := []map[string]any{}
	for _, message := range in.Messages {
		replayState := canReplayProviderState(p.dialect, message, in)
		switch message.Role {
		case "tool":
			input = append(input, map[string]any{"type": "function_call_output", "call_id": message.ToolCallID, "output": message.Content})
		case "assistant":
			if replayState && message.ThinkingSignature != "" {
				var reasoning []map[string]any
				if json.Unmarshal([]byte(message.ThinkingSignature), &reasoning) == nil {
					input = append(input, reasoning...)
				}
			}
			if message.Content != "" {
				input = append(input, map[string]any{"role": "assistant", "content": []map[string]any{{"type": "output_text", "text": message.Content}}})
			}
			for _, call := range message.ToolCalls {
				input = append(input, map[string]any{"type": "function_call", "call_id": call.ID, "name": call.Name, "arguments": string(call.Arguments)})
			}
		default:
			content := []map[string]any{}
			if message.Content != "" {
				content = append(content, map[string]any{"type": "input_text", "text": message.Content})
			}
			for _, image := range message.Images {
				content = append(content, map[string]any{"type": "input_image", "image_url": "data:" + image.MimeType + ";base64," + image.Data})
			}
			input = append(input, map[string]any{"role": "user", "content": content})
		}
	}
	tools := []map[string]any{}
	for _, tool := range in.Tools {
		tools = append(tools, map[string]any{"type": "function", "name": tool.Name, "description": tool.Description, "parameters": tool.Parameters, "strict": false})
	}
	body := map[string]any{"model": first(p.modelMap[in.Model], in.Model), "instructions": in.System, "input": input, "stream": stream, "store": false}
	if in.SessionID != "" {
		body["prompt_cache_key"] = in.SessionID
	}
	if len(tools) > 0 {
		body["tools"] = tools
	}
	if in.MaxTokens > 0 {
		body["max_output_tokens"] = in.MaxTokens
	}
	if effort := openAIResponsesEffort(in.Model, in.ThinkingLevel); effort != "" {
		reasoning := map[string]any{"effort": effort}
		if in.ThinkingLevel != "off" {
			reasoning["summary"] = "auto"
			body["include"] = []string{"reasoning.encrypted_content"}
		}
		body["reasoning"] = reasoning
	}
	return body
}

func openAIResponsesEffort(model, level string) string {
	model = strings.ToLower(model)
	switch level {
	case "off":
		if strings.HasPrefix(model, "gpt-5.1") ||
			strings.HasPrefix(model, "gpt-5.2") ||
			strings.HasPrefix(model, "gpt-5.3-codex") ||
			strings.HasPrefix(model, "gpt-5.4") ||
			strings.HasPrefix(model, "gpt-5.5") ||
			strings.HasPrefix(model, "gpt-5.6") {
			return "none"
		}
	case "minimal", "low", "medium", "high":
		return level
	case "xhigh":
		if supportsOpenAIExtendedEffort(model) {
			return "xhigh"
		}
		return "high"
	case "max":
		if strings.HasPrefix(model, "gpt-5.6-") {
			return "max"
		}
		if supportsOpenAIExtendedEffort(model) {
			return "xhigh"
		}
		return "high"
	}
	return ""
}

func supportsOpenAIExtendedEffort(model string) bool {
	return strings.HasPrefix(model, "gpt-5.2") ||
		strings.HasPrefix(model, "gpt-5.3") ||
		strings.HasPrefix(model, "gpt-5.4") ||
		strings.HasPrefix(model, "gpt-5.5") ||
		strings.HasPrefix(model, "gpt-5.6")
}

func (p *responsesProvider) endpoint() string {
	url := p.base + "/responses"
	if p.dialect == "azure-openai-responses" && !strings.Contains(url, "api-version=") {
		separator := "?"
		if strings.Contains(url, "?") {
			separator = "&"
		}
		url += separator + "api-version=" + p.apiVersion
	}
	return url
}

func (p *responsesProvider) requestHeaders(in CompletionRequest) map[string]string {
	headers := make(map[string]string, len(p.headers)+2)
	for key, value := range p.headers {
		headers[key] = value
	}
	if p.dialect == "openai-codex" && in.SessionID != "" {
		headers["session-id"], headers["x-client-request-id"] = in.SessionID, in.SessionID
	}
	return headers
}

type responsesOutput struct {
	Type      string                        `json:"type"`
	ID        string                        `json:"id"`
	CallID    string                        `json:"call_id"`
	Name      string                        `json:"name"`
	Arguments string                        `json:"arguments"`
	Encrypted string                        `json:"encrypted_content"`
	Summary   []struct{ Type, Text string } `json:"summary"`
	Content   []struct {
		Type, Text, Refusal string
	} `json:"content"`
}

func responseCompletion(output []responsesOutput, usage map[string]any) Completion {
	result := Completion{Usage: usage}
	texts := []string{}
	for _, item := range output {
		if item.Type == "function_call" {
			result.ToolCalls = append(result.ToolCalls, ToolCall{ID: item.CallID, Name: item.Name, Arguments: json.RawMessage(item.Arguments)})
		}
		for _, content := range item.Content {
			if content.Type == "output_text" && content.Text != "" {
				texts = append(texts, content.Text)
			}
			if content.Type == "refusal" && content.Refusal != "" {
				texts = append(texts, content.Refusal)
			}
		}
		if item.Type == "reasoning" {
			for _, summary := range item.Summary {
				if summary.Text != "" {
					result.Thinking += summary.Text
				}
			}
		}
	}
	result.Text = strings.Join(texts, "\n")
	result.ThinkingSignature = responseReasoningSignature(output)
	return result
}

func responseReasoningSignature(output []responsesOutput) string {
	items := []map[string]any{}
	for _, item := range output {
		if item.Type != "reasoning" || item.Encrypted == "" {
			continue
		}
		value := map[string]any{"type": "reasoning", "encrypted_content": item.Encrypted}
		if item.ID != "" {
			value["id"] = item.ID
		}
		if len(item.Summary) > 0 {
			value["summary"] = item.Summary
		}
		items = append(items, value)
	}
	if len(items) == 0 {
		return ""
	}
	data, _ := json.Marshal(items)
	return string(data)
}

func (p *responsesProvider) Complete(ctx context.Context, in CompletionRequest) (Completion, error) {
	var out struct {
		Output []responsesOutput `json:"output"`
		Usage  map[string]any    `json:"usage"`
		Error  *struct {
			Code, Message string
		} `json:"error"`
		Status            string `json:"status"`
		IncompleteDetails struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
	}
	status, responseHeaders, err := doJSON(ctx, p.client, p.endpoint(), mergeHeaders(p.requestHeaders(in), in.Headers), p.request(in, false), &out)
	if err != nil {
		return Completion{}, err
	}
	if out.Error != nil {
		return Completion{}, fmt.Errorf("responses API %s: %s", first(out.Error.Code, "error"), first(out.Error.Message, "provider returned an error"))
	}
	result := responseCompletion(out.Output, out.Usage)
	result.StopReason = normalizeStopReason(first(out.IncompleteDetails.Reason, out.Status))
	result.ResponseStatus, result.ResponseHeaders = status, responseHeaders
	return result, nil
}

func (p *responsesProvider) Stream(ctx context.Context, in CompletionRequest, onDelta func(CompletionDelta) error) (Completion, error) {
	result := Completion{}
	calls := map[int]*toolCallAccumulator{}
	terminal := false
	status, responseHeaders, err := streamJSON(ctx, p.client, p.endpoint(), mergeHeaders(p.requestHeaders(in), in.Headers), p.request(in, true), func(data []byte) error {
		var event struct {
			Type, Delta, Arguments string
			Code, Message          string
			OutputIndex            int             `json:"output_index"`
			Item                   responsesOutput `json:"item"`
			Response               struct {
				Output []responsesOutput `json:"output"`
				Usage  map[string]any    `json:"usage"`
				Error  *struct {
					Message string `json:"message"`
				} `json:"error"`
				Status            string `json:"status"`
				IncompleteDetails struct {
					Reason string `json:"reason"`
				} `json:"incomplete_details"`
			} `json:"response"`
		}
		if err := json.Unmarshal(data, &event); err != nil {
			return err
		}
		switch event.Type {
		case "response.output_text.delta", "response.refusal.delta":
			result.Text += event.Delta
			if onDelta != nil {
				return onDelta(CompletionDelta{Text: event.Delta})
			}
		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			result.Thinking += event.Delta
			if onDelta != nil {
				return onDelta(CompletionDelta{Thinking: event.Delta})
			}
		case "response.output_item.added":
			if event.Item.Type == "function_call" {
				calls[event.OutputIndex] = &toolCallAccumulator{ID: event.Item.CallID, Name: event.Item.Name}
				if onDelta != nil {
					if err := onDelta(CompletionDelta{ToolCallID: event.Item.CallID, ToolName: event.Item.Name}); err != nil {
						return err
					}
				}
			}
			if event.Item.Type == "reasoning" && event.Item.Encrypted != "" {
				result.ThinkingSignature = responseReasoningSignature([]responsesOutput{event.Item})
			}
		case "response.function_call_arguments.delta":
			if calls[event.OutputIndex] == nil {
				calls[event.OutputIndex] = &toolCallAccumulator{}
			}
			calls[event.OutputIndex].Arguments.WriteString(event.Delta)
			if onDelta != nil {
				if err := onDelta(CompletionDelta{ToolCallID: calls[event.OutputIndex].ID, ToolName: calls[event.OutputIndex].Name, ToolArgumentsDelta: event.Delta}); err != nil {
					return err
				}
			}
		case "response.function_call_arguments.done":
			if calls[event.OutputIndex] == nil {
				calls[event.OutputIndex] = &toolCallAccumulator{}
			}
			previous := calls[event.OutputIndex].Arguments.String()
			if event.Arguments != "" && event.Arguments != previous {
				calls[event.OutputIndex].Arguments.Reset()
				calls[event.OutputIndex].Arguments.WriteString(event.Arguments)
				if onDelta != nil && strings.HasPrefix(event.Arguments, previous) {
					if delta := strings.TrimPrefix(event.Arguments, previous); delta != "" {
						if err := onDelta(CompletionDelta{ToolCallID: calls[event.OutputIndex].ID, ToolName: calls[event.OutputIndex].Name, ToolArgumentsDelta: delta}); err != nil {
							return err
						}
					}
				}
			}
		case "response.output_item.done":
			switch event.Item.Type {
			case "function_call":
				call := calls[event.OutputIndex]
				if call == nil {
					call = &toolCallAccumulator{}
					calls[event.OutputIndex] = call
				}
				call.ID, call.Name = event.Item.CallID, event.Item.Name
				if event.Item.Arguments != "" {
					call.Arguments.Reset()
					call.Arguments.WriteString(event.Item.Arguments)
				}
			case "reasoning":
				if event.Item.Encrypted != "" {
					result.ThinkingSignature = responseReasoningSignature([]responsesOutput{event.Item})
				}
				if result.Thinking == "" {
					for _, summary := range event.Item.Summary {
						result.Thinking += summary.Text
					}
				}
			case "message":
				if result.Text == "" {
					completion := responseCompletion([]responsesOutput{event.Item}, nil)
					result.Text = completion.Text
				}
			}
		case "response.completed", "response.incomplete":
			terminal = true
			result.Usage = event.Response.Usage
			result.StopReason = normalizeStopReason(first(event.Response.IncompleteDetails.Reason, event.Response.Status))
			final := responseCompletion(event.Response.Output, event.Response.Usage)
			if result.Text == "" {
				result.Text = final.Text
			}
			if result.Thinking == "" {
				result.Thinking = final.Thinking
			}
			for index, item := range event.Response.Output {
				if item.Type != "function_call" {
					continue
				}
				call := calls[index]
				if call == nil {
					call = &toolCallAccumulator{}
					calls[index] = call
				}
				call.ID, call.Name = item.CallID, item.Name
				if item.Arguments != "" {
					call.Arguments.Reset()
					call.Arguments.WriteString(item.Arguments)
				}
			}
			if signature := responseReasoningSignature(event.Response.Output); signature != "" {
				result.ThinkingSignature = signature
			}
		case "response.failed", "error":
			terminal = true
			if event.Message != "" {
				return fmt.Errorf("responses API %s: %s", first(event.Code, "error"), event.Message)
			}
			if event.Response.Error != nil {
				return fmt.Errorf("responses API: %s", event.Response.Error.Message)
			}
			return fmt.Errorf("responses API stream failed")
		}
		return nil
	})
	result.ToolCalls = toolCallsFromAccumulators(calls)
	result.ResponseStatus, result.ResponseHeaders = status, responseHeaders
	if err == nil && !terminal {
		err = fmt.Errorf("responses API stream ended before a terminal event")
	}
	return result, err
}
