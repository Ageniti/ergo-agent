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

func TestOpenAIStreamAggregatesTextToolCallsAndUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		chunks := []string{`{"choices":[{"delta":{"content":"hel"}}]}`, `{"choices":[{"delta":{"content":"lo","tool_calls":[{"index":0,"id":"call-1","function":{"name":"to","arguments":"{\"x\":"}}]}}]}`, `{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"do","arguments":"1}"}}]},"finish_reason":"tool_calls"}],"usage":{"total_tokens":7}}`}
		for _, chunk := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", chunk)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	provider := &openAIProvider{base: server.URL, key: "test", client: server.Client()}
	var deltas []string
	var argumentDeltas string
	result, err := provider.Stream(context.Background(), CompletionRequest{Model: "test", System: "system", Messages: []Message{{Role: "user", Content: "go"}}}, func(delta CompletionDelta) error {
		deltas = append(deltas, delta.Text)
		argumentDeltas += delta.ToolArgumentsDelta
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "hello" || strings.Join(deltas, "") != "hello" {
		t.Fatalf("text=%q deltas=%v", result.Text, deltas)
	}
	if len(result.ToolCalls) != 1 || result.ToolCalls[0].Name != "todo" || string(result.ToolCalls[0].Arguments) != `{"x":1}` {
		t.Fatalf("calls=%+v", result.ToolCalls)
	}
	if argumentDeltas != `{"x":1}` {
		t.Fatalf("argument deltas=%q", argumentDeltas)
	}
	if result.Usage["total_tokens"].(float64) != 7 {
		t.Fatalf("usage=%v", result.Usage)
	}
}

func TestOpenAIStreamRequiresFinishReasonAndRejectsProviderErrorReasons(t *testing.T) {
	t.Run("missing finish reason", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
		}))
		defer server.Close()
		provider := &openAIProvider{base: server.URL, client: server.Client()}
		result, err := provider.Stream(context.Background(), CompletionRequest{Model: "model"}, nil)
		if err == nil || !strings.Contains(err.Error(), "finish_reason") || result.Text != "partial" {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})
	t.Run("provider error reason", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"},\"finish_reason\":\"network_error\"}]}\n\n")
		}))
		defer server.Close()
		provider := &openAIProvider{base: server.URL, client: server.Client()}
		result, err := provider.Stream(context.Background(), CompletionRequest{Model: "model"}, nil)
		if err == nil || !strings.Contains(err.Error(), "network_error") || result.StopReason != "error" {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})
	t.Run("structured stream error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, "data: {\"error\":{\"type\":\"rate_limit_error\",\"message\":\"slow down\"}}\n\n")
		}))
		defer server.Close()
		provider := &openAIProvider{base: server.URL, client: server.Client()}
		_, err := provider.Stream(context.Background(), CompletionRequest{Model: "model"}, nil)
		if err == nil || !strings.Contains(err.Error(), "rate_limit_error") || !strings.Contains(err.Error(), "slow down") {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestResponsesStreamAggregatesReasoningTextAndToolArguments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		events := []string{
			`{"type":"response.reasoning_summary_text.delta","delta":"think"}`,
			`{"type":"response.output_text.delta","delta":"done"}`,
			`{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","call_id":"call-2","name":"read"}}`,
			`{"type":"response.function_call_arguments.delta","output_index":1,"delta":"{\"path\":"}`,
			`{"type":"response.function_call_arguments.delta","output_index":1,"delta":"\"README.md\"}"}`,
			`{"type":"response.completed","response":{"usage":{"total_tokens":12}}}`,
		}
		for _, event := range events {
			fmt.Fprintf(w, "data: %s\n\n", event)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	provider := &responsesProvider{base: server.URL, headers: map[string]string{"Authorization": "Bearer test"}, client: server.Client()}
	var textDelta, thinkingDelta string
	result, err := provider.Stream(context.Background(), CompletionRequest{Model: "gpt", Messages: []Message{{Role: "user", Content: "inspect"}}}, func(delta CompletionDelta) error {
		textDelta += delta.Text
		thinkingDelta += delta.Thinking
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "done" || textDelta != "done" || thinkingDelta != "think" {
		t.Fatalf("result=%+v deltas=%q/%q", result, textDelta, thinkingDelta)
	}
	if len(result.ToolCalls) != 1 || result.ToolCalls[0].Name != "read" || string(result.ToolCalls[0].Arguments) != `{"path":"README.md"}` {
		t.Fatalf("calls=%+v", result.ToolCalls)
	}
	if result.Usage["total_tokens"].(float64) != 12 {
		t.Fatalf("usage=%v", result.Usage)
	}
}

func TestResponsesStreamUsesDoneArgumentsAndRefusalEvents(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		events := []string{
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"call","name":"read"}}`,
			`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"path\":"}`,
			`{"type":"response.function_call_arguments.done","output_index":0,"arguments":"{\"path\":\"README.md\"}"}`,
			`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","call_id":"call","name":"read","arguments":"{\"path\":\"README.md\"}"}}`,
			`{"type":"response.refusal.delta","output_index":1,"delta":"cannot comply"}`,
			`{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":2,"output_tokens":1}}}`,
		}
		for _, event := range events {
			fmt.Fprintf(w, "data: %s\n\n", event)
		}
	}))
	defer server.Close()
	provider := &responsesProvider{base: server.URL, headers: map[string]string{}, client: server.Client()}
	result, err := provider.Stream(context.Background(), CompletionRequest{Model: "gpt"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "cannot comply" || len(result.ToolCalls) != 1 || string(result.ToolCalls[0].Arguments) != `{"path":"README.md"}` {
		t.Fatalf("result=%+v", result)
	}
}

func TestResponsesStreamRequiresTerminalEventAndReportsTopLevelErrors(t *testing.T) {
	t.Run("missing terminal", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n")
		}))
		defer server.Close()
		provider := &responsesProvider{base: server.URL, headers: map[string]string{}, client: server.Client()}
		result, err := provider.Stream(context.Background(), CompletionRequest{Model: "gpt"}, nil)
		if err == nil || !strings.Contains(err.Error(), "terminal event") || result.Text != "partial" {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})
	t.Run("top-level error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, "data: {\"type\":\"error\",\"code\":\"rate_limit\",\"message\":\"slow down\"}\n\n")
		}))
		defer server.Close()
		provider := &responsesProvider{base: server.URL, headers: map[string]string{}, client: server.Client()}
		_, err := provider.Stream(context.Background(), CompletionRequest{Model: "gpt"}, nil)
		if err == nil || !strings.Contains(err.Error(), "rate_limit") || !strings.Contains(err.Error(), "slow down") {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestAnthropicStreamAggregatesNestedStopReasonUsageAndTools(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		events := []string{
			`{"type":"message_start","message":{"usage":{"input_tokens":4}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call-1","name":"read","input":{}}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"README.md\"}"}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"thinking_delta","thinking":"reason"}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"signature_delta","signature":"signature"}}`,
			`{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"done"}}`,
			`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":3}}`,
			`{"type":"message_stop"}`,
		}
		for _, event := range events {
			fmt.Fprintf(w, "data: %s\n\n", event)
		}
	}))
	defer server.Close()
	provider := &anthropicProvider{base: server.URL, key: "key", headers: map[string]string{}, client: server.Client()}
	var callStart string
	result, err := provider.Stream(context.Background(), CompletionRequest{Model: "claude"}, func(delta CompletionDelta) error {
		if delta.ToolCallID != "" {
			callStart = delta.ToolCallID
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "done" || result.Thinking != "reason" || result.ThinkingSignature != "signature" || result.StopReason != "toolUse" {
		t.Fatalf("result=%+v", result)
	}
	if callStart != "call-1" || len(result.ToolCalls) != 1 || string(result.ToolCalls[0].Arguments) != `{"path":"README.md"}` {
		t.Fatalf("callStart=%q calls=%+v", callStart, result.ToolCalls)
	}
	if result.Usage["input_tokens"].(float64) != 4 || result.Usage["output_tokens"].(float64) != 3 {
		t.Fatalf("usage=%v", result.Usage)
	}
}

func TestAnthropicStreamRequiresMessageStop(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"text\":\"partial\"}}\n\n")
	}))
	defer server.Close()
	provider := &anthropicProvider{base: server.URL, key: "key", headers: map[string]string{}, client: server.Client()}
	result, err := provider.Stream(context.Background(), CompletionRequest{Model: "claude"}, nil)
	if err == nil || !strings.Contains(err.Error(), "message_stop") || result.Text != "partial" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestAnthropicStreamReportsStructuredErrorEvent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "data: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"busy\"}}\n\n")
	}))
	defer server.Close()
	provider := &anthropicProvider{base: server.URL, key: "key", headers: map[string]string{}, client: server.Client()}
	_, err := provider.Stream(context.Background(), CompletionRequest{Model: "claude"}, nil)
	if err == nil || !strings.Contains(err.Error(), "overloaded_error") || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("err=%v", err)
	}
}

func TestGoogleStreamPreservesHeaderSignaturesAndProviderCallID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-goog-api-key") != "gemini-key" || r.URL.Query().Get("key") != "" {
			t.Errorf("url=%s headers=%v", r.URL.String(), r.Header)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		chunks := []string{
			`{"candidates":[{"content":{"parts":[{"text":"reason","thought":true,"thoughtSignature":"cmVhc29u"}]}}]}`,
			`{"candidates":[{"content":{"parts":[{"text":"answer","thoughtSignature":"dGV4dA=="},{"functionCall":{"id":"call-1","name":"read","args":{"path":"README.md"}},"thoughtSignature":"Y2FsbA=="}]},"finishReason":"STOP"}],"usageMetadata":{"totalTokenCount":5}}`,
		}
		for _, chunk := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", chunk)
		}
	}))
	defer server.Close()
	provider := &googleProvider{base: server.URL, key: "gemini-key", headers: map[string]string{}, client: server.Client()}
	result, err := provider.Stream(context.Background(), CompletionRequest{Model: "gemini-3-flash"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "answer" || result.TextSignature != "dGV4dA==" || result.ThinkingSignature != "cmVhc29u" {
		t.Fatalf("result=%+v", result)
	}
	if len(result.ToolCalls) != 1 || result.ToolCalls[0].ID != "call-1" || result.ToolCalls[0].Metadata["providerCallID"] != true {
		t.Fatalf("calls=%+v", result.ToolCalls)
	}
}

func TestGoogleStreamReportsStructuredErrorEvent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "data: {\"error\":{\"code\":\"RESOURCE_EXHAUSTED\",\"message\":\"quota\"}}\n\n")
	}))
	defer server.Close()
	provider := &googleProvider{base: server.URL, headers: map[string]string{}, client: server.Client()}
	_, err := provider.Stream(context.Background(), CompletionRequest{Model: "gemini-3-flash"}, nil)
	if err == nil || !strings.Contains(err.Error(), "RESOURCE_EXHAUSTED") || !strings.Contains(err.Error(), "quota") {
		t.Fatalf("err=%v", err)
	}
}

func TestPiMessagesProviderTranslatesContextAndStreamEvents(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer radius-key" {
			t.Errorf("authorization=%q", r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		events := []string{
			`{"type":"start"}`,
			`{"type":"text_delta","contentIndex":0,"delta":"hello"}`,
			`{"type":"text_end","contentIndex":0,"content":"hello","contentSignature":"text-signature"}`,
			`{"type":"toolcall_start","contentIndex":1,"id":"call","toolName":"read"}`,
			`{"type":"toolcall_delta","contentIndex":1,"delta":"{\"path\":\"README.md\"}"}`,
			`{"type":"toolcall_end","contentIndex":1,"toolCall":{"id":"call","name":"read","arguments":{"path":"README.md"}}}`,
			`{"type":"done","reason":"toolUse","usage":{"input":3,"output":2,"totalTokens":5}}`,
		}
		for _, event := range events {
			fmt.Fprintf(w, "data: %s\n\n", event)
		}
	}))
	defer server.Close()
	provider := &piMessagesProvider{base: server.URL, headers: map[string]string{"Authorization": "Bearer radius-key"}, client: server.Client()}
	result, err := provider.Stream(context.Background(), CompletionRequest{
		Model:     "auto",
		System:    "system",
		SessionID: "session",
		Messages: []Message{
			{Role: "user", Content: "inspect"},
			{Role: "assistant", Content: "working", ToolCalls: []ToolCall{{ID: "old", Name: "bash", Arguments: json.RawMessage(`{"command":"pwd"}`)}}},
			{Role: "tool", ToolCallID: "old", ToolName: "bash", Content: "workspace"},
		},
		Tools: []ToolDefinition{{Name: "read", Parameters: map[string]any{"type": "object"}}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "hello" || result.TextSignature != "text-signature" || result.StopReason != "toolUse" || len(result.ToolCalls) != 1 {
		t.Fatalf("result=%+v", result)
	}
	contextValue := body["context"].(map[string]any)
	if contextValue["systemPrompt"] != "system" || len(contextValue["messages"].([]any)) != 3 || len(contextValue["tools"].([]any)) != 1 {
		t.Fatalf("body=%v", body)
	}
}

func TestMistralStreamHandlesTypedThinkingAndTextContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":[{\"type\":\"thinking\",\"thinking\":[{\"type\":\"text\",\"text\":\"reason\"}]},{\"type\":\"text\",\"text\":\"answer\"}]},\"finish_reason\":\"stop\"}],\"usage\":{\"total_tokens\":4}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	provider := &mistralProvider{openAIProvider{base: server.URL, key: "key", dialect: "mistral", client: server.Client(), headers: map[string]string{}}}
	result, err := provider.Stream(context.Background(), CompletionRequest{Model: "magistral-small-latest", ThinkingLevel: "high"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "answer" || result.Thinking != "reason" || result.StopReason != "stop" || result.Usage["total_tokens"].(float64) != 4 {
		t.Fatalf("result=%+v", result)
	}
}
