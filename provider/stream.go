package provider

import (
	"context"
	"encoding/json"
	"fmt"
)

func (p *openAIProvider) Stream(ctx context.Context, in CompletionRequest, onDelta func(CompletionDelta) error) (Completion, error) {
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
			calls := []map[string]any{}
			reasoningDetails := []any{}
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
	tools := []map[string]any{}
	for _, tool := range in.Tools {
		tools = append(tools, map[string]any{"type": "function", "function": map[string]any{"name": tool.Name, "description": tool.Description, "parameters": tool.Parameters}})
	}
	body := map[string]any{"model": in.Model, "messages": messages, "stream": true, "stream_options": map[string]any{"include_usage": true}}
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
	if len(tools) > 0 {
		body["tools"] = tools
	}
	if p.dialect == "mistral" {
		applyMistralReasoning(body, in.Model, in.ThinkingLevel)
	} else if effort := openAIEffort(in.ThinkingLevel); effort != "" && supportsOpenAIReasoningEffort(p.dialect) {
		body["reasoning_effort"] = effort
	}
	result := Completion{}
	calls := map[int]*toolCallAccumulator{}
	pendingReasoningDetails := map[string]string{}
	terminal := false
	streamError := ""
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
	status, responseHeaders, err := streamJSON(ctx, p.client, p.base+"/chat/completions", headers, body, func(data []byte) error {
		var chunk struct {
			Error *struct {
				Code, Type, Message string
			} `json:"error"`
			Choices []struct {
				FinishReason string `json:"finish_reason"`
				Usage        map[string]any
				Delta        struct {
					Content          any               `json:"content"`
					ReasoningContent string            `json:"reasoning_content"`
					Reasoning        string            `json:"reasoning"`
					ReasoningText    string            `json:"reasoning_text"`
					ReasoningDetails []json.RawMessage `json:"reasoning_details"`
					ToolCalls        []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
			Usage map[string]any `json:"usage"`
		}
		if err := json.Unmarshal(data, &chunk); err != nil {
			return err
		}
		if chunk.Error != nil {
			return fmt.Errorf("provider %s: %s", first(chunk.Error.Code, chunk.Error.Type, "error"), first(chunk.Error.Message, "provider returned an error"))
		}
		if chunk.Usage != nil {
			result.Usage = chunk.Usage
		}
		for _, choice := range chunk.Choices {
			if choice.FinishReason != "" {
				terminal = true
				result.StopReason, streamError = openAICompatibleStopReason(p.dialect, choice.FinishReason)
				result.ErrorMessage = streamError
			}
			if chunk.Usage == nil && choice.Usage != nil {
				result.Usage = choice.Usage
			}
			text := contentText(choice.Delta.Content)
			thinking, thinkingSignature := openAIReasoningValue(choice.Delta.ReasoningContent, choice.Delta.Reasoning, choice.Delta.ReasoningText)
			if thinking == "" {
				thinking = contentThinking(choice.Delta.Content)
			}
			if thinking != "" {
				result.Thinking += thinking
				if result.ThinkingSignature == "" {
					result.ThinkingSignature = thinkingSignature
				}
				if onDelta != nil {
					if err := onDelta(CompletionDelta{Thinking: thinking}); err != nil {
						return err
					}
				}
			}
			if text != "" {
				result.Text += text
				if onDelta != nil {
					if err := onDelta(CompletionDelta{Text: text}); err != nil {
						return err
					}
				}
			}
			for _, delta := range choice.Delta.ToolCalls {
				call := calls[delta.Index]
				if call == nil {
					call = &toolCallAccumulator{}
					calls[delta.Index] = call
				}
				if delta.ID != "" {
					call.ID = delta.ID
					if signature := pendingReasoningDetails[delta.ID]; signature != "" {
						if call.Metadata == nil {
							call.Metadata = map[string]any{}
						}
						call.Metadata["thoughtSignature"] = signature
						delete(pendingReasoningDetails, delta.ID)
					}
				}
				call.Name += delta.Function.Name
				call.Arguments.WriteString(delta.Function.Arguments)
				if onDelta != nil && (delta.ID != "" || delta.Function.Name != "" || delta.Function.Arguments != "") {
					if err := onDelta(CompletionDelta{ToolCallID: delta.ID, ToolName: delta.Function.Name, ToolArgumentsDelta: delta.Function.Arguments}); err != nil {
						return err
					}
				}
			}
			for _, raw := range choice.Delta.ReasoningDetails {
				var detail struct {
					ID string `json:"id"`
				}
				if json.Unmarshal(raw, &detail) != nil || detail.ID == "" {
					continue
				}
				signature := string(raw)
				matched := false
				for _, call := range calls {
					if call != nil && call.ID == detail.ID {
						if call.Metadata == nil {
							call.Metadata = map[string]any{}
						}
						call.Metadata["thoughtSignature"] = signature
						matched = true
						break
					}
				}
				if !matched {
					pendingReasoningDetails[detail.ID] = signature
				}
			}
		}
		return nil
	})
	result.ToolCalls = toolCallsFromAccumulators(calls)
	result.ResponseStatus, result.ResponseHeaders = status, responseHeaders
	if err == nil && streamError != "" {
		err = fmt.Errorf("%s", streamError)
	}
	if err == nil && !terminal {
		err = fmt.Errorf("stream ended without finish_reason")
	}
	return result, err
}

func (p *anthropicProvider) Stream(ctx context.Context, in CompletionRequest, onDelta func(CompletionDelta) error) (Completion, error) {
	messages := []map[string]any{}
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
	tools := []map[string]any{}
	for _, tool := range in.Tools {
		tools = append(tools, map[string]any{"name": tool.Name, "description": tool.Description, "input_schema": tool.Parameters})
	}
	maxTokens := firstPositive(in.MaxTokens, 8192)
	body := map[string]any{"model": in.Model, "system": in.System, "messages": messages, "max_tokens": maxTokens, "stream": true}
	applyAnthropicThinking(body, in.Model, in.ThinkingLevel, maxTokens)
	if len(tools) > 0 {
		body["tools"] = tools
	}
	result := Completion{}
	calls := map[int]*toolCallAccumulator{}
	terminal := false
	status, responseHeaders, err := streamJSON(ctx, p.client, p.base+"/messages", mergeHeaders(p.requestHeaders(in), in.Headers), body, func(data []byte) error {
		var event struct {
			Type         string `json:"type"`
			Index        int    `json:"index"`
			ContentBlock struct {
				Type  string `json:"type"`
				ID    string `json:"id"`
				Name  string `json:"name"`
				Input any    `json:"input"`
			} `json:"content_block"`
			Delta struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				Thinking    string `json:"thinking"`
				Signature   string `json:"signature"`
				PartialJSON string `json:"partial_json"`
				StopReason  string `json:"stop_reason"`
				StopDetails *struct {
					Explanation string `json:"explanation"`
				} `json:"stop_details"`
			} `json:"delta"`
			StopReason string `json:"stop_reason"`
			Message    struct {
				Usage map[string]any `json:"usage"`
			} `json:"message"`
			Usage map[string]any `json:"usage"`
			Error *struct {
				Type, Message string
			} `json:"error"`
		}
		if err := json.Unmarshal(data, &event); err != nil {
			return err
		}
		if event.Type == "error" {
			return fmt.Errorf("Anthropic %s: %s", first(event.Error.Type, "error"), first(event.Error.Message, "provider returned an error"))
		}
		if event.Message.Usage != nil {
			result.Usage = event.Message.Usage
		}
		if event.Usage != nil {
			if result.Usage == nil {
				result.Usage = map[string]any{}
			}
			for key, value := range event.Usage {
				result.Usage[key] = value
			}
		}
		stopReason := first(event.Delta.StopReason, event.StopReason)
		if stopReason != "" {
			result.StopReason = normalizeStopReason(stopReason)
			if stopReason == "refusal" {
				result.ErrorMessage = "The model refused to complete the request"
				if event.Delta.StopDetails != nil && event.Delta.StopDetails.Explanation != "" {
					result.ErrorMessage = event.Delta.StopDetails.Explanation
				}
			}
		}
		if event.Type == "content_block_start" && event.ContentBlock.Type == "tool_use" {
			calls[event.Index] = &toolCallAccumulator{ID: event.ContentBlock.ID, Name: event.ContentBlock.Name}
			if event.ContentBlock.Input != nil {
				if encoded, marshalErr := json.Marshal(event.ContentBlock.Input); marshalErr == nil && string(encoded) != "{}" {
					calls[event.Index].Arguments.Write(encoded)
				}
			}
			if onDelta != nil {
				if err := onDelta(CompletionDelta{ToolCallID: event.ContentBlock.ID, ToolName: event.ContentBlock.Name}); err != nil {
					return err
				}
			}
		}
		if event.Type == "content_block_delta" {
			if event.Delta.Text != "" {
				result.Text += event.Delta.Text
				if onDelta != nil {
					if err := onDelta(CompletionDelta{Text: event.Delta.Text}); err != nil {
						return err
					}
				}
			}
			if event.Delta.Thinking != "" {
				result.Thinking += event.Delta.Thinking
				if onDelta != nil {
					if err := onDelta(CompletionDelta{Thinking: event.Delta.Thinking}); err != nil {
						return err
					}
				}
			}
			if event.Delta.Signature != "" {
				result.ThinkingSignature += event.Delta.Signature
			}
			if event.Delta.PartialJSON != "" {
				if calls[event.Index] == nil {
					calls[event.Index] = &toolCallAccumulator{}
				}
				calls[event.Index].Arguments.WriteString(event.Delta.PartialJSON)
				if onDelta != nil {
					if err := onDelta(CompletionDelta{ToolCallID: calls[event.Index].ID, ToolName: calls[event.Index].Name, ToolArgumentsDelta: event.Delta.PartialJSON}); err != nil {
						return err
					}
				}
			}
		}
		if event.Type == "message_stop" {
			terminal = true
		}
		return nil
	})
	result.ToolCalls = toolCallsFromAccumulators(calls)
	result.ResponseStatus, result.ResponseHeaders = status, responseHeaders
	if err == nil && !terminal {
		err = fmt.Errorf("Anthropic stream ended before message_stop")
	}
	return result, err
}

func (p *googleProvider) Stream(ctx context.Context, in CompletionRequest, onDelta func(CompletionDelta) error) (Completion, error) {
	contents := p.requestContents(in)
	declarations := []map[string]any{}
	for _, tool := range in.Tools {
		declarations = append(declarations, map[string]any{"name": tool.Name, "description": tool.Description, "parameters": tool.Parameters})
	}
	body := map[string]any{"systemInstruction": map[string]any{"parts": []map[string]any{{"text": in.System}}}, "contents": contents}
	generation := map[string]any{}
	if in.MaxTokens > 0 {
		generation["maxOutputTokens"] = in.MaxTokens
	}
	if thinking := googleThinkingConfig(in.Model, in.ThinkingLevel); thinking != nil {
		generation["thinkingConfig"] = thinking
	}
	if len(generation) > 0 {
		body["generationConfig"] = generation
	}
	if len(declarations) > 0 {
		body["tools"] = []map[string]any{{"functionDeclarations": declarations}}
	}
	result := Completion{}
	url := p.base + "/models/" + in.Model + ":streamGenerateContent?alt=sse"
	headers := cloneHeaders(p.headers)
	if p.key != "" {
		headers["x-goog-api-key"] = p.key
	}
	callIDs := map[string]bool{}
	status, responseHeaders, err := streamJSON(ctx, p.client, url, mergeHeaders(headers, in.Headers), body, func(data []byte) error {
		var chunk struct {
			Error *struct {
				Code, Status, Message string
			} `json:"error"`
			Candidates []struct {
				FinishReason string `json:"finishReason"`
				Content      struct {
					Parts []struct {
						Text             string `json:"text"`
						Thought          bool   `json:"thought"`
						ThoughtSignature string `json:"thoughtSignature"`
						FunctionCall     *struct {
							ID   string `json:"id"`
							Name string `json:"name"`
							Args any    `json:"args"`
						} `json:"functionCall"`
					} `json:"parts"`
				} `json:"content"`
			} `json:"candidates"`
			Usage map[string]any `json:"usageMetadata"`
		}
		if err := json.Unmarshal(data, &chunk); err != nil {
			return err
		}
		if chunk.Error != nil {
			return fmt.Errorf("Google %s: %s", first(chunk.Error.Code, chunk.Error.Status, "error"), first(chunk.Error.Message, "provider returned an error"))
		}
		if chunk.Usage != nil {
			result.Usage = chunk.Usage
		}
		for _, candidate := range chunk.Candidates {
			if candidate.FinishReason != "" {
				result.StopReason = googleStopReason(candidate.FinishReason)
			}
			for _, part := range candidate.Content.Parts {
				if part.FunctionCall == nil && validGoogleThoughtSignature(part.ThoughtSignature) {
					if part.Thought {
						result.ThinkingSignature = part.ThoughtSignature
					} else {
						result.TextSignature = part.ThoughtSignature
					}
				}
				if part.Text != "" && part.Thought {
					result.Thinking += part.Text
					if onDelta != nil {
						if err := onDelta(CompletionDelta{Thinking: part.Text}); err != nil {
							return err
						}
					}
				} else if part.Text != "" {
					result.Text += part.Text
					if onDelta != nil {
						if err := onDelta(CompletionDelta{Text: part.Text}); err != nil {
							return err
						}
					}
				}
				if part.FunctionCall != nil {
					args, _ := json.Marshal(part.FunctionCall.Args)
					metadata := map[string]any{}
					id := part.FunctionCall.ID
					if id == "" || callIDs[id] {
						id = newID()
					} else {
						metadata["providerCallID"] = true
					}
					callIDs[id] = true
					if validGoogleThoughtSignature(part.ThoughtSignature) {
						metadata["thoughtSignature"] = part.ThoughtSignature
					}
					result.ToolCalls = append(result.ToolCalls, ToolCall{ID: id, Name: part.FunctionCall.Name, Arguments: args, Metadata: metadata})
					if onDelta != nil {
						if err := onDelta(CompletionDelta{ToolCallID: id, ToolName: part.FunctionCall.Name, ToolArgumentsDelta: string(args)}); err != nil {
							return err
						}
					}
				}
			}
		}
		return nil
	})
	if len(result.ToolCalls) > 0 {
		result.StopReason = "toolUse"
	}
	result.ResponseStatus, result.ResponseHeaders = status, responseHeaders
	return result, err
}
