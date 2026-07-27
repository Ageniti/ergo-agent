package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

type googleProvider struct {
	name, base, key string
	client          *http.Client
	headers         map[string]string
}

func (p *googleProvider) requestContents(in CompletionRequest) []map[string]any {
	callNames := map[string]string{}
	providerCallIDs := map[string]bool{}
	contents := []map[string]any{}
	for _, message := range in.Messages {
		replaySignatures := p.canReplaySignatures(message, in.Model)
		if message.Role == "tool" {
			responseText := message.Content
			if responseText == "" && len(message.Images) > 0 {
				responseText = "(see attached image)"
			}
			response := map[string]any{"output": responseText}
			if message.IsError {
				response = map[string]any{"error": responseText}
			}
			functionResponse := map[string]any{"name": first(message.ToolName, callNames[message.ToolCallID]), "response": response}
			if requiresGoogleToolCallID(in.Model) || providerCallIDs[message.ToolCallID] {
				functionResponse["id"] = message.ToolCallID
			}
			imageParts := make([]map[string]any, 0, len(message.Images))
			for _, image := range message.Images {
				imageParts = append(imageParts, map[string]any{"inlineData": map[string]any{"mimeType": image.MimeType, "data": image.Data}})
			}
			if len(imageParts) > 0 && supportsGoogleMultimodalFunctionResponse(in.Model) {
				functionResponse["parts"] = imageParts
			}
			contents = appendGoogleContent(contents, "user", []map[string]any{{"functionResponse": functionResponse}}, true)
			if len(imageParts) > 0 && !supportsGoogleMultimodalFunctionResponse(in.Model) {
				parts := append([]map[string]any{{"text": "Tool result image:"}}, imageParts...)
				contents = append(contents, map[string]any{"role": "user", "parts": parts})
			}
			continue
		}
		role := "user"
		if message.Role == "assistant" {
			role = "model"
		}
		parts := []map[string]any{}
		if message.Thinking != "" {
			part := map[string]any{"text": message.Thinking}
			if replaySignatures {
				part["thought"] = true
			}
			if replaySignatures && validGoogleThoughtSignature(message.ThinkingSignature) {
				part["thoughtSignature"] = message.ThinkingSignature
			}
			parts = append(parts, part)
		}
		if message.Content != "" {
			part := map[string]any{"text": message.Content}
			if replaySignatures && validGoogleThoughtSignature(message.TextSignature) {
				part["thoughtSignature"] = message.TextSignature
			}
			parts = append(parts, part)
		}
		for _, image := range message.Images {
			parts = append(parts, map[string]any{"inlineData": map[string]any{"mimeType": image.MimeType, "data": image.Data}})
		}
		for _, call := range message.ToolCalls {
			var args any
			_ = json.Unmarshal(call.Arguments, &args)
			callNames[call.ID] = call.Name
			functionCall := map[string]any{"name": call.Name, "args": args}
			if requiresGoogleToolCallID(in.Model) || call.Metadata["providerCallID"] == true {
				functionCall["id"] = call.ID
			}
			if call.Metadata["providerCallID"] == true {
				providerCallIDs[call.ID] = true
			}
			part := map[string]any{"functionCall": functionCall}
			if signature, _ := call.Metadata["thoughtSignature"].(string); replaySignatures && validGoogleThoughtSignature(signature) {
				part["thoughtSignature"] = signature
			}
			parts = append(parts, part)
		}
		if len(parts) > 0 {
			contents = appendGoogleContent(contents, role, parts, false)
		}
	}
	return contents
}

func (p *googleProvider) canReplaySignatures(message Message, model string) bool {
	return canReplayProviderState(p.name, message, CompletionRequest{Model: model})
}

func validGoogleThoughtSignature(signature string) bool {
	if signature == "" || len(signature)%4 != 0 {
		return false
	}
	return regexp.MustCompile(`^[A-Za-z0-9+/]+={0,2}$`).MatchString(signature)
}

func supportsGoogleMultimodalFunctionResponse(model string) bool {
	match := regexp.MustCompile(`^gemini(?:-live)?-(\d+)`).FindStringSubmatch(strings.ToLower(model))
	if len(match) != 2 {
		return true
	}
	major, err := strconv.Atoi(match[1])
	return err != nil || major >= 3
}

func googleStopReason(reason string) string {
	switch strings.ToUpper(strings.TrimSpace(reason)) {
	case "":
		return ""
	case "STOP":
		return "stop"
	case "MAX_TOKENS":
		return "length"
	default:
		return "error"
	}
}

func (p *googleProvider) Complete(ctx context.Context, in CompletionRequest) (Completion, error) {
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
	var out struct {
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
	headers := cloneHeaders(p.headers)
	if p.key != "" {
		headers["x-goog-api-key"] = p.key
	}
	url := p.base + "/models/" + in.Model + ":generateContent"
	status, responseHeaders, err := doJSON(ctx, p.client, url, mergeHeaders(headers, in.Headers), body, &out)
	if err != nil {
		return Completion{}, err
	}
	if len(out.Candidates) == 0 {
		return Completion{}, fmt.Errorf("provider returned no candidates")
	}
	result := Completion{Usage: out.Usage, StopReason: googleStopReason(out.Candidates[0].FinishReason), ResponseStatus: status, ResponseHeaders: responseHeaders}
	texts := []string{}
	callIDs := map[string]bool{}
	for _, part := range out.Candidates[0].Content.Parts {
		if part.FunctionCall == nil && validGoogleThoughtSignature(part.ThoughtSignature) {
			if part.Thought {
				result.ThinkingSignature = part.ThoughtSignature
			} else {
				result.TextSignature = part.ThoughtSignature
			}
		}
		if part.Text != "" && part.Thought {
			result.Thinking += part.Text
		} else if part.Text != "" {
			texts = append(texts, part.Text)
		}
		if part.FunctionCall != nil {
			args, _ := json.Marshal(part.FunctionCall.Args)
			metadata := map[string]any{}
			if validGoogleThoughtSignature(part.ThoughtSignature) {
				metadata["thoughtSignature"] = part.ThoughtSignature
			}
			id := part.FunctionCall.ID
			if id == "" || callIDs[id] {
				id = newID()
			} else {
				metadata["providerCallID"] = true
			}
			callIDs[id] = true
			result.ToolCalls = append(result.ToolCalls, ToolCall{ID: id, Name: part.FunctionCall.Name, Arguments: args, Metadata: metadata})
		}
	}
	result.Text = strings.Join(texts, "\n")
	if len(result.ToolCalls) > 0 {
		result.StopReason = "toolUse"
	}
	return result, nil
}

func openAIEffort(level string) string {
	switch level {
	case "minimal", "low", "medium", "high":
		return level
	case "xhigh", "max":
		return "high"
	}
	return ""
}

func appendGoogleContent(contents []map[string]any, role string, parts []map[string]any, toolResult bool) []map[string]any {
	if toolResult && len(contents) > 0 && contents[len(contents)-1]["role"] == "user" {
		if previous, ok := contents[len(contents)-1]["parts"].([]map[string]any); ok {
			onlyResponses := true
			for _, part := range previous {
				if _, ok := part["functionResponse"]; !ok {
					onlyResponses = false
					break
				}
			}
			if onlyResponses {
				contents[len(contents)-1]["parts"] = append(previous, parts...)
				return contents
			}
		}
	}
	return append(contents, map[string]any{"role": role, "parts": parts})
}

func requiresGoogleToolCallID(model string) bool {
	model = strings.ToLower(model)
	return strings.HasPrefix(model, "claude-") || strings.HasPrefix(model, "gpt-oss-")
}

func googleThinkingConfig(model, level string) map[string]any {
	if level == "" {
		return nil
	}
	model = strings.ToLower(model)
	isGemini3Pro := regexp.MustCompile(`gemini-3(?:\.\d+)?-pro`).MatchString(model)
	isGemini3Flash := regexp.MustCompile(`gemini-3(?:\.\d+)?-flash`).MatchString(model) ||
		model == "gemini-flash-latest" || model == "gemini-flash-lite-latest"
	isGemma4 := regexp.MustCompile(`gemma-?4`).MatchString(model)
	if isGemini3Pro || isGemini3Flash || isGemma4 {
		if level == "off" {
			if isGemini3Pro {
				return map[string]any{"thinkingLevel": "LOW"}
			}
			return map[string]any{"thinkingLevel": "MINIMAL"}
		}
		thinkingLevel := "HIGH"
		switch level {
		case "minimal":
			thinkingLevel = "MINIMAL"
		case "low":
			thinkingLevel = "LOW"
		case "medium":
			thinkingLevel = "MEDIUM"
		}
		if isGemini3Pro && (thinkingLevel == "MINIMAL" || thinkingLevel == "MEDIUM") {
			thinkingLevel = "LOW"
		}
		return map[string]any{"includeThoughts": true, "thinkingLevel": thinkingLevel}
	}
	if level == "off" {
		return map[string]any{"thinkingBudget": 0}
	}
	return map[string]any{"includeThoughts": true, "thinkingBudget": thinkingBudget(level)}
}
