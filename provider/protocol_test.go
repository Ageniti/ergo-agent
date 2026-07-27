package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAnthropicCompletePreservesThinkingToolsAndErrors(t *testing.T) {
	var body map[string]any
	var beta string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		beta = r.Header.Get("anthropic-beta")
		if r.Header.Get("x-api-key") != "anthropic-key" || r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Errorf("headers=%v", r.Header)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		_, _ = w.Write([]byte(`{
			"content":[
				{"type":"thinking","thinking":"reason","signature":"signature"},
				{"type":"text","text":"answer"},
				{"type":"tool_use","id":"call-1","name":"read","input":{"path":"README.md"}}
			],
			"usage":{"input_tokens":10,"output_tokens":5},
			"stop_reason":"tool_use"
		}`))
	}))
	defer server.Close()
	provider := &anthropicProvider{base: server.URL, key: "anthropic-key", client: server.Client(), headers: map[string]string{}}
	request := CompletionRequest{
		Model:         "claude-sonnet-4-6",
		System:        "system",
		ThinkingLevel: "high",
		MaxTokens:     4096,
		Messages: []Message{
			{Role: "assistant", ToolCalls: []ToolCall{{ID: "old-call", Name: "bash", Arguments: json.RawMessage(`{"command":"false"}`)}}},
			{Role: "tool", ToolCallID: "old-call", ToolName: "bash", Content: "failed", IsError: true},
		},
		Tools: []ToolDefinition{{Name: "read", Description: "read a file", Parameters: map[string]any{"type": "object"}}},
	}
	result, err := provider.Complete(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "answer" || result.Thinking != "reason" || result.ThinkingSignature != "signature" || result.StopReason != "toolUse" {
		t.Fatalf("result=%+v", result)
	}
	if len(result.ToolCalls) != 1 || result.ToolCalls[0].ID != "call-1" || string(result.ToolCalls[0].Arguments) != `{"path":"README.md"}` {
		t.Fatalf("tool calls=%+v", result.ToolCalls)
	}
	if strings.Contains(beta, "interleaved-thinking-2025-05-14") {
		t.Fatalf("adaptive-thinking model must not receive the legacy interleaved beta: %q", beta)
	}
	messages := body["messages"].([]any)
	toolResult := messages[1].(map[string]any)["content"].([]any)[0].(map[string]any)
	if toolResult["is_error"] != true {
		t.Fatalf("tool result=%v", toolResult)
	}
	thinking := body["thinking"].(map[string]any)
	outputConfig := body["output_config"].(map[string]any)
	if thinking["type"] != "adaptive" || outputConfig["effort"] != "high" {
		t.Fatalf("thinking=%v output_config=%v", thinking, outputConfig)
	}
}

func TestAnthropicBudgetThinkingUsesInterleavedBeta(t *testing.T) {
	provider := &anthropicProvider{key: "key", headers: map[string]string{}}
	request := CompletionRequest{Model: "claude-sonnet-4-5", ThinkingLevel: "high"}
	headers := provider.requestHeaders(request)
	if !strings.Contains(headers["anthropic-beta"], "interleaved-thinking-2025-05-14") {
		t.Fatalf("headers=%v", headers)
	}
	body := map[string]any{}
	applyAnthropicThinking(body, request.Model, request.ThinkingLevel, 4096)
	thinking := body["thinking"].(map[string]any)
	if thinking["type"] != "enabled" || thinking["budget_tokens"].(int) != 3072 {
		t.Fatalf("thinking=%v", thinking)
	}
}

func TestAnthropicOAuthTokenUsesBearerHeaders(t *testing.T) {
	provider := &anthropicProvider{key: "sk-ant-oat-test", headers: map[string]string{}}
	headers := provider.requestHeaders(CompletionRequest{})
	if headers["Authorization"] != "Bearer sk-ant-oat-test" || headers["x-api-key"] != "" {
		t.Fatalf("headers=%v", headers)
	}
	if !strings.Contains(headers["anthropic-beta"], "oauth-2025-04-20") || headers["x-app"] != "cli" {
		t.Fatalf("headers=%v", headers)
	}
}

func TestGeminiUsesHeaderAuthAndReplaysProviderSignaturesAndIDs(t *testing.T) {
	var requests []map[string]any
	var paths, apiKeys []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		requests = append(requests, body)
		paths = append(paths, r.URL.String())
		apiKeys = append(apiKeys, r.Header.Get("x-goog-api-key"))
		_, _ = w.Write([]byte(`{
			"candidates":[{"finishReason":"STOP","content":{"parts":[
				{"text":"answer","thoughtSignature":"dGV4dA=="},
				{"functionCall":{"id":"provider-call","name":"read","args":{"path":"README.md"}},"thoughtSignature":"dG9vbA=="}
			]}}],
			"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2}
		}`))
	}))
	defer server.Close()
	provider := &googleProvider{base: server.URL, key: "gemini-key", client: server.Client(), headers: map[string]string{}}
	firstResult, err := provider.Complete(context.Background(), CompletionRequest{
		Model: "gemini-3-flash",
		Messages: []Message{
			{Role: "user", Content: "inspect"},
		},
		ThinkingLevel: "high",
	})
	if err != nil {
		t.Fatal(err)
	}
	if firstResult.Text != "answer" || firstResult.TextSignature != "dGV4dA==" || len(firstResult.ToolCalls) != 1 {
		t.Fatalf("result=%+v", firstResult)
	}
	call := firstResult.ToolCalls[0]
	if call.ID != "provider-call" || call.Metadata["providerCallID"] != true || call.Metadata["thoughtSignature"] != "dG9vbA==" {
		t.Fatalf("call=%+v", call)
	}
	assistant := Message{Role: "assistant", Content: firstResult.Text, TextSignature: firstResult.TextSignature, ToolCalls: firstResult.ToolCalls}
	_, err = provider.Complete(context.Background(), CompletionRequest{
		Model: "gemini-3-flash",
		Messages: []Message{
			{Role: "user", Content: "inspect"},
			assistant,
			{Role: "tool", ToolCallID: call.ID, ToolName: call.Name, Content: "contents"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 || strings.Contains(paths[0], "key=") || apiKeys[0] != "gemini-key" {
		t.Fatalf("paths=%v apiKeys=%v", paths, apiKeys)
	}
	generation := requests[0]["generationConfig"].(map[string]any)
	thinking := generation["thinkingConfig"].(map[string]any)
	if thinking["thinkingLevel"] != "HIGH" {
		t.Fatalf("thinking=%v", thinking)
	}
	contents := requests[1]["contents"].([]any)
	assistantParts := contents[1].(map[string]any)["parts"].([]any)
	if assistantParts[0].(map[string]any)["thoughtSignature"] != "dGV4dA==" {
		t.Fatalf("assistant parts=%v", assistantParts)
	}
	functionCall := assistantParts[1].(map[string]any)["functionCall"].(map[string]any)
	if functionCall["id"] != "provider-call" {
		t.Fatalf("function call=%v", functionCall)
	}
	functionResponse := contents[2].(map[string]any)["parts"].([]any)[0].(map[string]any)["functionResponse"].(map[string]any)
	if functionResponse["id"] != "provider-call" || functionResponse["name"] != "read" {
		t.Fatalf("function response=%v", functionResponse)
	}
	if functionResponse["response"].(map[string]any)["output"] != "contents" {
		t.Fatalf("function response=%v", functionResponse)
	}
}

func TestGeminiGroupsParallelFunctionResponses(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"done"}]}}]}`))
	}))
	defer server.Close()
	provider := &googleProvider{base: server.URL, key: "key", client: server.Client(), headers: map[string]string{}}
	_, err := provider.Complete(context.Background(), CompletionRequest{
		Model: "gemini-3-flash",
		Messages: []Message{
			{Role: "assistant", ToolCalls: []ToolCall{
				{ID: "one", Name: "read", Arguments: json.RawMessage(`{}`)},
				{ID: "two", Name: "grep", Arguments: json.RawMessage(`{}`)},
			}},
			{Role: "tool", ToolCallID: "one", ToolName: "read", Content: "one"},
			{Role: "tool", ToolCallID: "two", ToolName: "grep", Content: "two"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	contents := body["contents"].([]any)
	if len(contents) != 2 {
		t.Fatalf("contents=%v", contents)
	}
	parts := contents[1].(map[string]any)["parts"].([]any)
	if len(parts) != 2 {
		t.Fatalf("parallel responses=%v", parts)
	}
}

func TestGoogleOnlyReplaysValidSameModelSignatures(t *testing.T) {
	provider := &googleProvider{name: "google"}
	contents := provider.requestContents(CompletionRequest{
		Model: "gemini-3-flash",
		Messages: []Message{{
			Role:              "assistant",
			Provider:          "google",
			Model:             "gemini-2.5-pro",
			Content:           "answer",
			TextSignature:     "dGV4dA==",
			Thinking:          "reason",
			ThinkingSignature: "invalid-signature",
			ToolCalls: []ToolCall{{
				ID:        "call",
				Name:      "read",
				Arguments: json.RawMessage(`{}`),
				Metadata:  map[string]any{"thoughtSignature": "dG9vbA=="},
			}},
		}},
	})
	parts := contents[0]["parts"].([]map[string]any)
	for _, part := range parts {
		if part["thoughtSignature"] != nil || part["thought"] != nil {
			t.Fatalf("cross-model signature replayed: %v", parts)
		}
	}
}

func TestGoogleToolResultImagesUseModelSpecificWireShape(t *testing.T) {
	provider := &googleProvider{}
	messages := []Message{
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "call", Name: "inspect", Arguments: json.RawMessage(`{}`)}}},
		{Role: "tool", ToolCallID: "call", ToolName: "inspect", Images: []Image{{MimeType: "image/png", Data: "aQ=="}}},
	}
	gemini3 := provider.requestContents(CompletionRequest{Model: "gemini-3-flash", Messages: messages})
	if len(gemini3) != 2 {
		t.Fatalf("gemini 3 contents=%v", gemini3)
	}
	response3 := gemini3[1]["parts"].([]map[string]any)[0]["functionResponse"].(map[string]any)
	if len(response3["parts"].([]map[string]any)) != 1 {
		t.Fatalf("gemini 3 response=%v", response3)
	}
	gemini2 := provider.requestContents(CompletionRequest{Model: "gemini-2.5-pro", Messages: messages})
	if len(gemini2) != 3 {
		t.Fatalf("gemini 2 contents=%v", gemini2)
	}
	response2 := gemini2[1]["parts"].([]map[string]any)[0]["functionResponse"].(map[string]any)
	if response2["parts"] != nil || gemini2[2]["role"] != "user" {
		t.Fatalf("gemini 2 contents=%v", gemini2)
	}
}

func TestAzureResponsesNormalizesEndpointAndMapsDeployment(t *testing.T) {
	var path, apiKey string
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, apiKey = r.URL.String(), r.Header.Get("api-key")
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"azure"}]}]}`))
	}))
	defer server.Close()
	t.Setenv("AZURE_OPENAI_BASE_URL", server.URL)
	t.Setenv("AZURE_OPENAI_API_KEY", "azure-key")
	t.Setenv("AZURE_OPENAI_API_VERSION", "2026-01-01")
	t.Setenv("AZURE_OPENAI_DEPLOYMENT_NAME_MAP", "gpt-5=my-deployment")
	provider, err := (HTTPProviderFactory{}).ProviderForModel("azure-openai-responses", "gpt-5", 1000)
	if err != nil {
		t.Fatal(err)
	}
	result, err := provider.Complete(context.Background(), CompletionRequest{Model: "gpt-5"})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/openai/v1/responses?api-version=2026-01-01" || apiKey != "azure-key" || body["model"] != "my-deployment" || result.Text != "azure" {
		t.Fatalf("path=%q apiKey=%q body=%v result=%+v", path, apiKey, body, result)
	}
}

func TestResponsesCompleteReturnsStructuredProviderError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"status":"failed","error":{"code":"bad_request","message":"invalid input"}}`)
	}))
	defer server.Close()
	provider := &responsesProvider{base: server.URL, client: server.Client(), headers: map[string]string{}}
	_, err := provider.Complete(context.Background(), CompletionRequest{Model: "gpt"})
	if err == nil || !strings.Contains(err.Error(), "bad_request") || !strings.Contains(err.Error(), "invalid input") {
		t.Fatalf("err=%v", err)
	}
}

func TestDeepSeekCompatibilityUsesMaxTokensAndReplaysReasoning(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"content":"answer","reasoning_content":"reason"}}]}`))
	}))
	defer server.Close()
	provider := &openAIProvider{base: server.URL, key: "key", dialect: "deepseek", client: server.Client(), headers: map[string]string{}}
	result, err := provider.Complete(context.Background(), CompletionRequest{
		Model:         "deepseek-v4-pro",
		MaxTokens:     123,
		ThinkingLevel: "high",
		Messages:      []Message{{Role: "assistant", Content: "prior", Thinking: "prior reasoning"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Thinking != "reason" || body["max_tokens"] != float64(123) || body["reasoning_effort"] != nil {
		t.Fatalf("body=%v result=%+v", body, result)
	}
	messages := body["messages"].([]any)
	if messages[1].(map[string]any)["reasoning_content"] != "prior reasoning" {
		t.Fatalf("messages=%v", messages)
	}
}

func TestOpenAICompatibleCompleteCapturesReasoningDetailsAndChoiceUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{
			"choices":[{
				"finish_reason":"tool_calls",
				"usage":{"total_tokens":7},
				"message":{
					"reasoning":"reason",
					"tool_calls":[{"id":"call-1","function":{"name":"read","arguments":"{\"path\":\"README.md\"}"}}],
					"reasoning_details":[{"type":"reasoning.encrypted","id":"call-1","data":"opaque"}]
				}
			}]
		}`)
	}))
	defer server.Close()
	provider := &openAIProvider{base: server.URL, client: server.Client(), dialect: "openrouter", headers: map[string]string{}}
	result, err := provider.Complete(context.Background(), CompletionRequest{Model: "model"})
	if err != nil {
		t.Fatal(err)
	}
	if result.StopReason != "toolUse" || result.ThinkingSignature != "reasoning" || result.Usage["total_tokens"].(float64) != 7 {
		t.Fatalf("result=%+v", result)
	}
	if len(result.ToolCalls) != 1 || result.ToolCalls[0].Metadata["thoughtSignature"] == nil {
		t.Fatalf("calls=%+v", result.ToolCalls)
	}
}

func TestOpenAICompatibleCompleteReportsStructuredProviderError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"error":{"type":"invalid_request_error","message":"bad input"}}`)
	}))
	defer server.Close()
	provider := &openAIProvider{base: server.URL, client: server.Client(), headers: map[string]string{}}
	_, err := provider.Complete(context.Background(), CompletionRequest{Model: "model"})
	if err == nil || !strings.Contains(err.Error(), "invalid_request_error") || !strings.Contains(err.Error(), "bad input") {
		t.Fatalf("err=%v", err)
	}
}

func TestOpenAICompatibleReasoningDetailsAreCapturedAndReplayed(t *testing.T) {
	var requests []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		requests = append(requests, body)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"reasoning\":\"think\",\"tool_calls\":[{\"index\":0,\"id\":\"call-1\",\"function\":{\"name\":\"read\",\"arguments\":\"{\\\"path\\\":\\\"README.md\\\"}\"}}],\"reasoning_details\":[{\"type\":\"reasoning.encrypted\",\"id\":\"call-1\",\"data\":\"opaque\"}]},\"finish_reason\":\"tool_calls\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	provider := &openAIProvider{base: server.URL, client: server.Client(), dialect: "openrouter", headers: map[string]string{}}
	firstResult, err := provider.Stream(context.Background(), CompletionRequest{Model: "model", Messages: []Message{{Role: "user", Content: "go"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if firstResult.ThinkingSignature != "reasoning" || len(firstResult.ToolCalls) != 1 || firstResult.ToolCalls[0].Metadata["thoughtSignature"] == nil {
		t.Fatalf("result=%+v", firstResult)
	}
	assistant := Message{Role: "assistant", Thinking: firstResult.Thinking, ThinkingSignature: firstResult.ThinkingSignature, ToolCalls: firstResult.ToolCalls}
	if _, err := provider.Stream(context.Background(), CompletionRequest{Model: "model", Messages: []Message{{Role: "user", Content: "go"}, assistant}}, nil); err != nil {
		t.Fatal(err)
	}
	messages := requests[1]["messages"].([]any)
	replayed := messages[2].(map[string]any)
	if replayed["reasoning"] != "think" || len(replayed["reasoning_details"].([]any)) != 1 {
		t.Fatalf("replayed=%v", replayed)
	}
}
