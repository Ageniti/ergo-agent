package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	bedrock "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
)

func TestMistralRequestNormalizesToolIDsAndReasoning(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("authorization=%q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("x-affinity") != "session-1" {
			t.Errorf("x-affinity=%q", r.Header.Get("x-affinity"))
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"total_tokens":2}}`))
	}))
	defer server.Close()
	provider := &mistralProvider{openAIProvider{base: server.URL, key: "secret", client: server.Client(), dialect: "mistral"}}
	original := []Message{{Role: "assistant", ToolCalls: []ToolCall{{ID: "long-call_id", Name: "read", Arguments: json.RawMessage(`{"path":"x"}`)}}}, {Role: "tool", ToolCallID: "long-call_id", Content: "x"}}
	if _, err := provider.Complete(context.Background(), CompletionRequest{SessionID: "session-1", Model: "magistral-small-latest", Messages: original, MaxTokens: 42, ThinkingLevel: "high"}); err != nil {
		t.Fatal(err)
	}
	messages := body["messages"].([]any)
	assistant := messages[1].(map[string]any)
	callID := assistant["tool_calls"].([]any)[0].(map[string]any)["id"].(string)
	toolID := messages[2].(map[string]any)["tool_call_id"].(string)
	if len(callID) != 9 || callID != toolID {
		t.Fatalf("call=%q tool=%q", callID, toolID)
	}
	if body["max_tokens"] != float64(42) || body["prompt_mode"] != "reasoning" {
		t.Fatalf("body=%v", body)
	}
	if body["prompt_cache_key"] != "session-1" {
		t.Fatalf("cache key=%v", body["prompt_cache_key"])
	}
	if original[0].ToolCalls[0].ID != "long-call_id" {
		t.Fatal("provider mutated session history")
	}
}

func TestResponsesProviderBuildsCodexAndAzureTransportDetails(t *testing.T) {
	codex := &responsesProvider{base: "https://example.test/backend-api/codex", dialect: "openai-codex", headers: map[string]string{"originator": "pi"}}
	headers := codex.requestHeaders(CompletionRequest{SessionID: "session-1"})
	if codex.endpoint() != "https://example.test/backend-api/codex/responses" || headers["session-id"] != "session-1" || headers["x-client-request-id"] != "session-1" {
		t.Fatalf("endpoint=%q headers=%v", codex.endpoint(), headers)
	}
	azure := &responsesProvider{base: "https://resource.openai.azure.com/openai/v1", dialect: "azure-openai-responses", apiVersion: "v1"}
	if azure.endpoint() != "https://resource.openai.azure.com/openai/v1/responses?api-version=v1" {
		t.Fatalf("azure endpoint=%q", azure.endpoint())
	}
}

func TestResponsesEncryptedReasoningIsRequestedCapturedAndReplayed(t *testing.T) {
	provider := &responsesProvider{}
	output := []responsesOutput{{Type: "reasoning", ID: "reason-1", Encrypted: "cipher"}}
	completion := responseCompletion(output, nil)
	if !strings.Contains(completion.ThinkingSignature, "cipher") {
		t.Fatalf("completion=%+v", completion)
	}
	body := provider.request(CompletionRequest{Model: "gpt", ThinkingLevel: "high", Messages: []Message{{Role: "assistant", ThinkingSignature: completion.ThinkingSignature}}}, false)
	include := body["include"].([]string)
	if len(include) != 1 || include[0] != "reasoning.encrypted_content" {
		t.Fatalf("include=%v", include)
	}
	input := body["input"].([]map[string]any)
	if len(input) != 1 || input[0]["type"] != "reasoning" || input[0]["encrypted_content"] != "cipher" {
		t.Fatalf("input=%v", input)
	}
	provider.dialect = "openai"
	mismatch := provider.request(CompletionRequest{
		Model: "gpt-new",
		Messages: []Message{{
			Role:              "assistant",
			Provider:          "openai",
			Model:             "gpt-old",
			ThinkingSignature: completion.ThinkingSignature,
		}},
	}, false)
	if replayed := mismatch["input"].([]map[string]any); len(replayed) != 0 {
		t.Fatalf("cross-model reasoning replayed: %v", replayed)
	}
}

func TestVertexRequestUsesPublisherURLAndOAuthHeader(t *testing.T) {
	var path, authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, authorization = r.URL.String(), r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"ok"}]}}]}`))
	}))
	defer server.Close()
	provider := &googleProvider{base: server.URL, client: server.Client(), headers: map[string]string{"Authorization": "Bearer adc-token"}}
	result, err := provider.Complete(context.Background(), CompletionRequest{Model: "gemini-2.5-pro", System: "system", Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/models/gemini-2.5-pro:generateContent" || authorization != "Bearer adc-token" || result.Text != "ok" {
		t.Fatalf("path=%q auth=%q result=%+v", path, authorization, result)
	}
}

func TestBedrockInputConvertsToolsImagesAndThinking(t *testing.T) {
	request := CompletionRequest{Model: "anthropic.claude-3-7-sonnet", System: "system", ThinkingLevel: "high", MaxTokens: 4096,
		Messages: []Message{{Role: "user", Content: "look", Images: []Image{{MimeType: "image/png", Data: "aQ=="}}}, {Role: "assistant", ToolCalls: []ToolCall{{ID: "call", Name: "read", Arguments: json.RawMessage(`{"path":"x"}`)}}}, {Role: "tool", ToolCallID: "call", Content: "done"}},
		Tools:    []ToolDefinition{{Name: "read", Description: "read file", Parameters: map[string]any{"type": "object"}}},
	}
	input, err := bedrockInput(request)
	if err != nil {
		t.Fatal(err)
	}
	if len(input.Messages) != 3 || len(input.System) != 1 || input.ToolConfig == nil || len(input.ToolConfig.Tools) != 1 || input.AdditionalModelRequestFields == nil {
		t.Fatalf("input=%+v", input)
	}
	if _, ok := input.Messages[0].Content[1].(*bedrock.ContentBlockMemberImage); !ok {
		t.Fatalf("image block=%T", input.Messages[0].Content[1])
	}
	if !strings.Contains(request.Messages[0].Content, "look") {
		t.Fatal("request mutated")
	}
}

func TestProviderFactoryAppliesCustomHeadersAndCodexJWTAccount(t *testing.T) {
	var custom string
	var path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		custom = r.Header.Get("X-Tenant")
		path = r.URL.Path
		_, _ = w.Write([]byte(`{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`))
	}))
	defer server.Close()
	t.Setenv("AGENT_PROVIDER_OPENAI_BASE_URL", server.URL)
	t.Setenv("AGENT_PROVIDER_OPENAI_API_KEY", "secret")
	t.Setenv("AGENT_PROVIDER_OPENAI_HEADERS", `{"X-Tenant":"ergo"}`)
	provider, err := (HTTPProviderFactory{}).Provider("openai", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Complete(context.Background(), CompletionRequest{Model: "model", Messages: []Message{{Role: "user", Content: "hello"}}}); err != nil {
		t.Fatal(err)
	}
	if custom != "ergo" {
		t.Fatalf("custom header=%q", custom)
	}
	if path != "/responses" {
		t.Fatalf("path=%q", path)
	}
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"chatgpt_account_id":"acct-123"}}`))
	if got := codexAccountID("header." + payload + ".signature"); got != "acct-123" {
		t.Fatalf("account=%q", got)
	}
}

func TestGeminiThoughtSignaturesAreCapturedAndReplayed(t *testing.T) {
	var requests []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		requests = append(requests, body)
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"reason","thought":true,"thoughtSignature":"dGhvdWdodA=="},{"functionCall":{"name":"read","args":{"path":"x"}},"thoughtSignature":"Y2FsbA=="}]}}]}`))
	}))
	defer server.Close()
	provider := &googleProvider{base: server.URL, client: server.Client(), headers: map[string]string{}}
	result, err := provider.Complete(context.Background(), CompletionRequest{Model: "gemini", Messages: []Message{{Role: "user", Content: "go"}}})
	if err != nil {
		t.Fatal(err)
	}
	if result.ThinkingSignature != "dGhvdWdodA==" || result.ToolCalls[0].Metadata["thoughtSignature"] != "Y2FsbA==" {
		t.Fatalf("result=%+v", result)
	}
	assistant := Message{Role: "assistant", Thinking: result.Thinking, ThinkingSignature: result.ThinkingSignature, ToolCalls: result.ToolCalls}
	if _, err := provider.Complete(context.Background(), CompletionRequest{Model: "gemini", Messages: []Message{{Role: "user", Content: "go"}, assistant, {Role: "tool", ToolCallID: result.ToolCalls[0].ID, Content: "ok"}}}); err != nil {
		t.Fatal(err)
	}
	contents := requests[1]["contents"].([]any)
	parts := contents[1].(map[string]any)["parts"].([]any)
	if parts[0].(map[string]any)["thoughtSignature"] != "dGhvdWdodA==" || parts[1].(map[string]any)["thoughtSignature"] != "Y2FsbA==" {
		t.Fatalf("replayed parts=%v", parts)
	}
}

type fakeBedrockClient struct {
	output *bedrockruntime.ConverseOutput
	err    error
}

func (f *fakeBedrockClient) Converse(context.Context, *bedrockruntime.ConverseInput, ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseOutput, error) {
	return f.output, f.err
}

func (f *fakeBedrockClient) ConverseStream(context.Context, *bedrockruntime.ConverseStreamInput, ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseStreamOutput, error) {
	return nil, f.err
}

func TestBedrockCompleteConsumesTextThinkingToolsAndUsage(t *testing.T) {
	client := &fakeBedrockClient{output: &bedrockruntime.ConverseOutput{
		Output: &bedrock.ConverseOutputMemberMessage{Value: bedrock.Message{
			Role: bedrock.ConversationRoleAssistant,
			Content: []bedrock.ContentBlock{
				&bedrock.ContentBlockMemberReasoningContent{Value: &bedrock.ReasoningContentBlockMemberReasoningText{Value: bedrock.ReasoningTextBlock{Text: aws.String("reason"), Signature: aws.String("signature")}}},
				&bedrock.ContentBlockMemberText{Value: "answer"},
				&bedrock.ContentBlockMemberToolUse{Value: bedrock.ToolUseBlock{ToolUseId: aws.String("call"), Name: aws.String("read")}},
			},
		}},
		StopReason: bedrock.StopReasonToolUse,
		Usage:      &bedrock.TokenUsage{InputTokens: aws.Int32(4), OutputTokens: aws.Int32(2), TotalTokens: aws.Int32(6)},
	}}
	provider := &bedrockProvider{client: client}
	result, err := provider.Complete(context.Background(), CompletionRequest{Model: "anthropic.claude", Messages: []Message{{Role: "user", Content: "go"}}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "answer" || result.Thinking != "reason" || result.ThinkingSignature != "signature" || result.StopReason != "toolUse" {
		t.Fatalf("result=%+v", result)
	}
	if len(result.ToolCalls) != 1 || result.ToolCalls[0].ID != "call" || result.ToolCalls[0].Name != "read" {
		t.Fatalf("calls=%+v", result.ToolCalls)
	}
	if result.Usage["total_tokens"] != int32(6) {
		t.Fatalf("usage=%v", result.Usage)
	}
}

type fakeBedrockStreamReader struct {
	events chan bedrock.ConverseStreamOutput
	err    error
}

func (f *fakeBedrockStreamReader) Events() <-chan bedrock.ConverseStreamOutput { return f.events }
func (f *fakeBedrockStreamReader) Close() error                                { return nil }
func (f *fakeBedrockStreamReader) Err() error                                  { return f.err }

func TestBedrockStreamConsumerAggregatesEvents(t *testing.T) {
	events := make(chan bedrock.ConverseStreamOutput, 6)
	index := int32(0)
	events <- &bedrock.ConverseStreamOutputMemberContentBlockStart{Value: bedrock.ContentBlockStartEvent{
		ContentBlockIndex: &index,
		Start:             &bedrock.ContentBlockStartMemberToolUse{Value: bedrock.ToolUseBlockStart{ToolUseId: aws.String("call"), Name: aws.String("read")}},
	}}
	events <- &bedrock.ConverseStreamOutputMemberContentBlockDelta{Value: bedrock.ContentBlockDeltaEvent{
		ContentBlockIndex: &index,
		Delta:             &bedrock.ContentBlockDeltaMemberToolUse{Value: bedrock.ToolUseBlockDelta{Input: aws.String(`{"path":"README.md"}`)}},
	}}
	textIndex := int32(1)
	events <- &bedrock.ConverseStreamOutputMemberContentBlockDelta{Value: bedrock.ContentBlockDeltaEvent{
		ContentBlockIndex: &textIndex,
		Delta:             &bedrock.ContentBlockDeltaMemberText{Value: "done"},
	}}
	events <- &bedrock.ConverseStreamOutputMemberMetadata{Value: bedrock.ConverseStreamMetadataEvent{
		Metrics: &bedrock.ConverseStreamMetrics{LatencyMs: aws.Int64(1)},
		Usage:   &bedrock.TokenUsage{InputTokens: aws.Int32(3), OutputTokens: aws.Int32(2), TotalTokens: aws.Int32(5)},
	}}
	events <- &bedrock.ConverseStreamOutputMemberMessageStop{Value: bedrock.MessageStopEvent{StopReason: bedrock.StopReasonToolUse}}
	close(events)
	reader := &fakeBedrockStreamReader{events: events}
	result, err := consumeBedrockStream(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "done" || result.StopReason != "toolUse" || len(result.ToolCalls) != 1 || string(result.ToolCalls[0].Arguments) != `{"path":"README.md"}` {
		t.Fatalf("result=%+v", result)
	}
	if result.Usage["total_tokens"] != int32(5) {
		t.Fatalf("usage=%v", result.Usage)
	}
}
