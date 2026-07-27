package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	bedrock "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
)

type bedrockConverseClient interface {
	Converse(context.Context, *bedrockruntime.ConverseInput, ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseOutput, error)
	ConverseStream(context.Context, *bedrockruntime.ConverseStreamInput, ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseStreamOutput, error)
}

type bedrockStreamReader interface {
	Events() <-chan bedrock.ConverseStreamOutput
	Close() error
	Err() error
}

type bedrockProvider struct{ client bedrockConverseClient }

func (p *bedrockProvider) Complete(ctx context.Context, request CompletionRequest) (Completion, error) {
	input, err := bedrockInput(request)
	if err != nil {
		return Completion{}, err
	}
	output, err := p.client.Converse(ctx, input)
	if err != nil {
		return Completion{}, fmt.Errorf("Bedrock Converse: %w", err)
	}
	result := Completion{Usage: bedrockUsage(output.Usage), StopReason: normalizeStopReason(string(output.StopReason)), ResponseStatus: 200}
	message, ok := output.Output.(*bedrock.ConverseOutputMemberMessage)
	if !ok {
		return Completion{}, fmt.Errorf("Bedrock Converse returned no message")
	}
	consumeBedrockBlocks(message.Value.Content, &result)
	return result, nil
}

func (p *bedrockProvider) Stream(ctx context.Context, request CompletionRequest, onDelta func(CompletionDelta) error) (Completion, error) {
	input, err := bedrockInput(request)
	if err != nil {
		return Completion{}, err
	}
	streamOutput, err := p.client.ConverseStream(ctx, &bedrockruntime.ConverseStreamInput{
		ModelId: input.ModelId, Messages: input.Messages, System: input.System,
		InferenceConfig: input.InferenceConfig, ToolConfig: input.ToolConfig,
		AdditionalModelRequestFields: input.AdditionalModelRequestFields,
	})
	if err != nil {
		return Completion{}, fmt.Errorf("Bedrock ConverseStream: %w", err)
	}
	stream := streamOutput.GetStream()
	defer stream.Close()
	return consumeBedrockStream(stream, onDelta)
}

func consumeBedrockStream(stream bedrockStreamReader, onDelta func(CompletionDelta) error) (Completion, error) {
	result := Completion{ResponseStatus: 200}
	calls := map[int]*toolCallAccumulator{}
	for event := range stream.Events() {
		switch value := event.(type) {
		case *bedrock.ConverseStreamOutputMemberContentBlockStart:
			if start, ok := value.Value.Start.(*bedrock.ContentBlockStartMemberToolUse); ok {
				index := int(aws.ToInt32(value.Value.ContentBlockIndex))
				calls[index] = &toolCallAccumulator{ID: aws.ToString(start.Value.ToolUseId), Name: aws.ToString(start.Value.Name)}
				if onDelta != nil {
					if err := onDelta(CompletionDelta{ToolCallID: aws.ToString(start.Value.ToolUseId), ToolName: aws.ToString(start.Value.Name)}); err != nil {
						return result, err
					}
				}
			}
		case *bedrock.ConverseStreamOutputMemberContentBlockDelta:
			index := int(aws.ToInt32(value.Value.ContentBlockIndex))
			switch delta := value.Value.Delta.(type) {
			case *bedrock.ContentBlockDeltaMemberText:
				result.Text += delta.Value
				if onDelta != nil {
					if err := onDelta(CompletionDelta{Text: delta.Value}); err != nil {
						return result, err
					}
				}
			case *bedrock.ContentBlockDeltaMemberToolUse:
				if calls[index] == nil {
					calls[index] = &toolCallAccumulator{}
				}
				calls[index].Arguments.WriteString(aws.ToString(delta.Value.Input))
				if onDelta != nil {
					if err := onDelta(CompletionDelta{ToolCallID: calls[index].ID, ToolName: calls[index].Name, ToolArgumentsDelta: aws.ToString(delta.Value.Input)}); err != nil {
						return result, err
					}
				}
			case *bedrock.ContentBlockDeltaMemberReasoningContent:
				switch reasoning := delta.Value.(type) {
				case *bedrock.ReasoningContentBlockDeltaMemberText:
					result.Thinking += reasoning.Value
					if onDelta != nil {
						if err := onDelta(CompletionDelta{Thinking: reasoning.Value}); err != nil {
							return result, err
						}
					}
				case *bedrock.ReasoningContentBlockDeltaMemberSignature:
					result.ThinkingSignature += reasoning.Value
				}
			}
		case *bedrock.ConverseStreamOutputMemberMetadata:
			result.Usage = bedrockUsage(value.Value.Usage)
		case *bedrock.ConverseStreamOutputMemberMessageStop:
			result.StopReason = normalizeStopReason(string(value.Value.StopReason))
		}
	}
	if err := stream.Err(); err != nil {
		return result, fmt.Errorf("Bedrock ConverseStream: %w", err)
	}
	result.ToolCalls = toolCallsFromAccumulators(calls)
	return result, nil
}

func bedrockInput(request CompletionRequest) (*bedrockruntime.ConverseInput, error) {
	messages := make([]bedrock.Message, 0, len(request.Messages))
	appendMessage := func(role bedrock.ConversationRole, blocks []bedrock.ContentBlock) {
		if len(blocks) == 0 {
			blocks = []bedrock.ContentBlock{&bedrock.ContentBlockMemberText{Value: "<empty>"}}
		}
		if len(messages) > 0 && messages[len(messages)-1].Role == role {
			messages[len(messages)-1].Content = append(messages[len(messages)-1].Content, blocks...)
			return
		}
		messages = append(messages, bedrock.Message{Role: role, Content: blocks})
	}
	for _, message := range request.Messages {
		blocks := []bedrock.ContentBlock{}
		role := bedrock.ConversationRoleUser
		if message.Role == "assistant" {
			role = bedrock.ConversationRoleAssistant
		}
		if message.Role == "tool" {
			content := []bedrock.ToolResultContentBlock{&bedrock.ToolResultContentBlockMemberText{Value: first(message.Content, "<empty>")}}
			for _, image := range message.Images {
				block, err := bedrockImage(image)
				if err != nil {
					return nil, err
				}
				content = append(content, &bedrock.ToolResultContentBlockMemberImage{Value: block})
			}
			toolResult := bedrock.ToolResultBlock{ToolUseId: aws.String(message.ToolCallID), Content: content}
			if message.IsError {
				toolResult.Status = bedrock.ToolResultStatusError
			}
			blocks = append(blocks, &bedrock.ContentBlockMemberToolResult{Value: toolResult})
			appendMessage(role, blocks)
			continue
		}
		if message.Content != "" {
			blocks = append(blocks, &bedrock.ContentBlockMemberText{Value: message.Content})
		}
		if message.Thinking != "" {
			if canReplayProviderState("", message, request) && message.ThinkingSignature != "" && strings.Contains(strings.ToLower(request.Model), "claude") {
				blocks = append(blocks, &bedrock.ContentBlockMemberReasoningContent{Value: &bedrock.ReasoningContentBlockMemberReasoningText{Value: bedrock.ReasoningTextBlock{Text: aws.String(message.Thinking), Signature: aws.String(message.ThinkingSignature)}}})
			} else {
				blocks = append(blocks, &bedrock.ContentBlockMemberText{Value: message.Thinking})
			}
		}
		for _, image := range message.Images {
			block, err := bedrockImage(image)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, &bedrock.ContentBlockMemberImage{Value: block})
		}
		for _, call := range message.ToolCalls {
			var arguments any = map[string]any{}
			if len(call.Arguments) > 0 {
				if err := json.Unmarshal(call.Arguments, &arguments); err != nil {
					return nil, fmt.Errorf("decode tool arguments for %s: %w", call.Name, err)
				}
			}
			blocks = append(blocks, &bedrock.ContentBlockMemberToolUse{Value: bedrock.ToolUseBlock{ToolUseId: aws.String(call.ID), Name: aws.String(call.Name), Input: document.NewLazyDocument(arguments)}})
		}
		appendMessage(role, blocks)
	}
	input := &bedrockruntime.ConverseInput{ModelId: aws.String(request.Model), Messages: messages}
	if request.System != "" {
		input.System = []bedrock.SystemContentBlock{&bedrock.SystemContentBlockMemberText{Value: request.System}}
	}
	if request.MaxTokens > 0 {
		value := int32(request.MaxTokens)
		input.InferenceConfig = &bedrock.InferenceConfiguration{MaxTokens: &value}
	}
	if len(request.Tools) > 0 {
		tools := make([]bedrock.Tool, 0, len(request.Tools))
		for _, tool := range request.Tools {
			tools = append(tools, &bedrock.ToolMemberToolSpec{Value: bedrock.ToolSpecification{Name: aws.String(tool.Name), Description: aws.String(tool.Description), InputSchema: &bedrock.ToolInputSchemaMemberJson{Value: document.NewLazyDocument(tool.Parameters)}}})
		}
		input.ToolConfig = &bedrock.ToolConfiguration{Tools: tools}
	}
	if request.ThinkingLevel != "" && request.ThinkingLevel != "off" && strings.Contains(strings.ToLower(request.Model), "claude") {
		fields := map[string]any{}
		if usesAnthropicAdaptiveThinking(request.Model) {
			fields["thinking"] = map[string]any{"type": "adaptive", "display": "summarized"}
			fields["output_config"] = map[string]any{"effort": anthropicEffort(request.Model, request.ThinkingLevel)}
		} else {
			fields["thinking"] = map[string]any{"type": "enabled", "budget_tokens": thinkingBudget(request.ThinkingLevel), "display": "summarized"}
			fields["anthropic_beta"] = []string{"interleaved-thinking-2025-05-14"}
		}
		input.AdditionalModelRequestFields = document.NewLazyDocument(fields)
	}
	return input, nil
}

func bedrockImage(image Image) (bedrock.ImageBlock, error) {
	data, err := base64.StdEncoding.DecodeString(image.Data)
	if err != nil {
		return bedrock.ImageBlock{}, fmt.Errorf("decode %s image: %w", image.MimeType, err)
	}
	var format bedrock.ImageFormat
	switch image.MimeType {
	case "image/jpeg", "image/jpg":
		format = bedrock.ImageFormatJpeg
	case "image/png":
		format = bedrock.ImageFormatPng
	case "image/gif":
		format = bedrock.ImageFormatGif
	case "image/webp":
		format = bedrock.ImageFormatWebp
	default:
		return bedrock.ImageBlock{}, fmt.Errorf("unsupported Bedrock image type %q", image.MimeType)
	}
	return bedrock.ImageBlock{Format: format, Source: &bedrock.ImageSourceMemberBytes{Value: data}}, nil
}

func consumeBedrockBlocks(blocks []bedrock.ContentBlock, result *Completion) {
	texts := []string{}
	for _, raw := range blocks {
		switch block := raw.(type) {
		case *bedrock.ContentBlockMemberText:
			texts = append(texts, block.Value)
		case *bedrock.ContentBlockMemberToolUse:
			var arguments any
			if block.Value.Input != nil {
				_ = block.Value.Input.UnmarshalSmithyDocument(&arguments)
			}
			encoded, _ := json.Marshal(arguments)
			result.ToolCalls = append(result.ToolCalls, ToolCall{ID: aws.ToString(block.Value.ToolUseId), Name: aws.ToString(block.Value.Name), Arguments: encoded})
		case *bedrock.ContentBlockMemberReasoningContent:
			if thinking, ok := block.Value.(*bedrock.ReasoningContentBlockMemberReasoningText); ok {
				result.Thinking += aws.ToString(thinking.Value.Text)
				result.ThinkingSignature += aws.ToString(thinking.Value.Signature)
			}
		}
	}
	result.Text = strings.Join(texts, "\n")
}

func bedrockUsage(usage *bedrock.TokenUsage) map[string]any {
	if usage == nil {
		return nil
	}
	return map[string]any{"input_tokens": aws.ToInt32(usage.InputTokens), "output_tokens": aws.ToInt32(usage.OutputTokens), "total_tokens": aws.ToInt32(usage.TotalTokens), "cache_read_input_tokens": aws.ToInt32(usage.CacheReadInputTokens), "cache_write_input_tokens": aws.ToInt32(usage.CacheWriteInputTokens)}
}
