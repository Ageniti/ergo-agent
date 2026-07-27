package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func TestBuiltinProviderCatalogUsesProtocolFamilies(t *testing.T) {
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "account")
	t.Setenv("CLOUDFLARE_GATEWAY_ID", "gateway")
	tests := []struct {
		provider string
		model    string
		api      ProviderAPI
		base     string
	}{
		{"openai", "gpt-5.4", ProviderAPIOpenAIResponses, "api.openai.com/v1"},
		{"openai-chat", "gpt-4o", ProviderAPIOpenAICompletions, "api.openai.com/v1"},
		{"google", "gemini-3-flash", ProviderAPIGoogleGenerative, "generativelanguage.googleapis.com/v1beta"},
		{"deepseek", "deepseek-v4-pro", ProviderAPIOpenAICompletions, "api.deepseek.com"},
		{"kimi-coding", "kimi-k2.5", ProviderAPIAnthropicMessages, "api.kimi.com/coding/v1"},
		{"xai", "grok-4.5", ProviderAPIOpenAIResponses, "api.x.ai/v1"},
		{"xai", "grok-4.3", ProviderAPIOpenAICompletions, "api.x.ai/v1"},
		{"opencode", "claude-sonnet-4-6", ProviderAPIAnthropicMessages, "opencode.ai/zen/v1"},
		{"opencode", "gemini-3-flash", ProviderAPIGoogleGenerative, "opencode.ai/zen/v1"},
		{"opencode", "gpt-5.4", ProviderAPIOpenAIResponses, "opencode.ai/zen/v1"},
		{"fireworks", "accounts/fireworks/models/glm-5p2", ProviderAPIOpenAICompletions, "api.fireworks.ai/inference/v1"},
		{"fireworks", "accounts/fireworks/models/kimi-k2p7-code", ProviderAPIAnthropicMessages, "api.fireworks.ai/inference/v1"},
		{"cloudflare-ai-gateway", "claude-sonnet-4-6", ProviderAPIAnthropicMessages, "/account/gateway/anthropic/v1"},
		{"cloudflare-ai-gateway", "gpt-5.4", ProviderAPIOpenAIResponses, "/account/gateway/openai"},
		{"radius", "auto", ProviderAPIPiMessages, "radius.pi.dev"},
	}
	for _, test := range tests {
		t.Run(test.provider+"/"+test.model, func(t *testing.T) {
			config, _, found, err := providerCatalogConfig(test.provider, test.model)
			if err != nil {
				t.Fatal(err)
			}
			if !found || config.API != test.api || !strings.Contains(config.BaseURL, test.base) {
				t.Fatalf("found=%v config=%+v", found, config)
			}
		})
	}
	names := BuiltinProviderNames()
	for _, required := range []string{"amazon-bedrock", "anthropic", "cloudflare-workers-ai", "deepseek", "google", "openai", "openrouter", "radius"} {
		if !slices.Contains(names, required) {
			t.Fatalf("built-in provider %q missing from %v", required, names)
		}
	}
	if slices.Contains(names, "github-copilot") {
		t.Fatal("OAuth-only GitHub Copilot must not be advertised by the headless API-key catalog")
	}
}

func TestProviderFactoryMakesOpenAIResponsesTheDefault(t *testing.T) {
	var path, authorization string
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, authorization = r.URL.Path, r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		_, _ = w.Write([]byte(`{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`))
	}))
	defer server.Close()
	t.Setenv("AGENT_PROVIDER_OPENAI_BASE_URL", server.URL)
	t.Setenv("OPENAI_API_KEY", "openai-key")
	provider, err := (HTTPProviderFactory{}).ProviderForModel("openai", "gpt-5.4", 1000)
	if err != nil {
		t.Fatal(err)
	}
	result, err := provider.Complete(context.Background(), CompletionRequest{Model: "gpt-5.4", ThinkingLevel: "xhigh"})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/responses" || authorization != "Bearer openai-key" || result.Text != "ok" {
		t.Fatalf("path=%q authorization=%q result=%+v", path, authorization, result)
	}
	reasoning := body["reasoning"].(map[string]any)
	if reasoning["effort"] != "xhigh" || body["store"] != false {
		t.Fatalf("body=%v", body)
	}
}

func TestProviderFactoryRetainsExplicitOpenAICompletionsCompatibility(t *testing.T) {
	var path string
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"content":"ok"}}]}`))
	}))
	defer server.Close()
	t.Setenv("AGENT_PROVIDER_OPENAI_CHAT_BASE_URL", server.URL)
	t.Setenv("AGENT_PROVIDER_OPENAI_CHAT_API_KEY", "chat-key")
	provider, err := (HTTPProviderFactory{}).ProviderForModel("openai-chat", "gpt-4o", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Complete(context.Background(), CompletionRequest{Model: "gpt-4o", MaxTokens: 123}); err != nil {
		t.Fatal(err)
	}
	if path != "/chat/completions" || body["max_completion_tokens"] != float64(123) {
		t.Fatalf("path=%q body=%v", path, body)
	}
}

func TestCustomProviderSelectsProtocolWithEnvironment(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/messages" {
			t.Errorf("path=%q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"custom"}],"stop_reason":"end_turn"}`))
	}))
	defer server.Close()
	t.Setenv("AGENT_PROVIDER_PRIVATE_API", string(ProviderAPIAnthropicMessages))
	t.Setenv("AGENT_PROVIDER_PRIVATE_BASE_URL", server.URL)
	t.Setenv("AGENT_PROVIDER_PRIVATE_API_KEY", "private-key")
	provider, err := (HTTPProviderFactory{}).ProviderForModel("private", "model", 1000)
	if err != nil {
		t.Fatal(err)
	}
	result, err := provider.Complete(context.Background(), CompletionRequest{Model: "model"})
	if err != nil || result.Text != "custom" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestGeminiCredentialResolutionPrefersGeminiAPIKey(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "gemini-key")
	t.Setenv("GOOGLE_API_KEY", "legacy-key")
	provider, err := (HTTPProviderFactory{}).ProviderForModel("google", "gemini-3-flash", 1000)
	if err != nil {
		t.Fatal(err)
	}
	google, ok := provider.(*googleProvider)
	if !ok || google.key != "gemini-key" {
		t.Fatalf("provider=%T key=%q", provider, google.key)
	}
}

func TestCloudflareGatewayOwnsAuthenticationHeaders(t *testing.T) {
	t.Setenv("CLOUDFLARE_API_KEY", "cloudflare-key")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "account")
	t.Setenv("CLOUDFLARE_GATEWAY_ID", "gateway")
	provider, err := (HTTPProviderFactory{}).ProviderForModel("cloudflare-ai-gateway", "claude-sonnet-4-6", 1000)
	if err != nil {
		t.Fatal(err)
	}
	anthropic, ok := provider.(*anthropicProvider)
	if !ok {
		t.Fatalf("provider=%T", provider)
	}
	headers := anthropic.ProviderHeaders(CompletionRequest{})
	if headers["cf-aig-authorization"] != "Bearer cloudflare-key" || headers["Authorization"] != "" || headers["x-api-key"] != "" {
		t.Fatalf("headers=%v", headers)
	}
	if !strings.Contains(anthropic.base, "/account/gateway/anthropic/v1") {
		t.Fatalf("base=%q", anthropic.base)
	}
}

func TestCloudflareGatewayCustomBaseDoesNotRequirePlaceholderEnvironment(t *testing.T) {
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "")
	t.Setenv("CLOUDFLARE_GATEWAY_ID", "")
	t.Setenv("AGENT_PROVIDER_CLOUDFLARE_AI_GATEWAY_BASE_URL", "https://gateway.example/v1")
	t.Setenv("AGENT_PROVIDER_CLOUDFLARE_AI_GATEWAY_HEADERS", `{"cf-aig-authorization":"Bearer gateway-token"}`)
	provider, err := (HTTPProviderFactory{}).ProviderForModel("cloudflare-ai-gateway", "gpt-5.4", 1000)
	if err != nil {
		t.Fatal(err)
	}
	responses, ok := provider.(*responsesProvider)
	if !ok || responses.base != "https://gateway.example/v1" || responses.headers["cf-aig-authorization"] != "Bearer gateway-token" {
		t.Fatalf("provider=%T value=%+v", provider, responses)
	}
}

func TestHeaderOwnedCustomProviderDoesNotRequireAPIKey(t *testing.T) {
	provider, err := NewHTTPProvider(HTTPProviderConfig{
		Name:               "gateway",
		API:                ProviderAPIOpenAICompletions,
		BaseURL:            "https://gateway.test/v1",
		Headers:            map[string]string{"Authorization": "Bearer gateway-token"},
		DisableDefaultAuth: true,
	}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	headers := provider.(*openAIProvider).ProviderHeaders(CompletionRequest{})
	if headers["Authorization"] != "Bearer gateway-token" {
		t.Fatalf("headers=%v", headers)
	}
}

type recordingModelFactory struct {
	name, model string
	provider    Provider
}

type captureProvider struct {
	request CompletionRequest
}

func (p *captureProvider) Complete(_ context.Context, request CompletionRequest) (Completion, error) {
	p.request = request
	return Completion{}, nil
}

func (f *recordingModelFactory) Provider(name string, _ int) (Provider, error) {
	f.name = name
	return f.provider, nil
}

func (f *recordingModelFactory) ProviderForModel(name, model string, _ int) (Provider, error) {
	f.name, f.model = name, model
	return f.provider, nil
}

func TestProviderRegistryPreservesModelAwareResolution(t *testing.T) {
	fallback := &recordingModelFactory{provider: &captureProvider{}}
	registry := NewProviderRegistry(fallback)
	if _, err := registry.ProviderForModel("xai", "grok-4.5", 1000); err != nil {
		t.Fatal(err)
	}
	if fallback.name != "xai" || fallback.model != "grok-4.5" {
		t.Fatalf("fallback=%+v", fallback)
	}
	override := &captureProvider{}
	if err := registry.Register("xai", override); err != nil {
		t.Fatal(err)
	}
	got, err := registry.ProviderForModel("xai", "other", 1000)
	if err != nil || got != override {
		t.Fatalf("provider=%T err=%v", got, err)
	}
	registry.Unregister("xai")
	if _, err := registry.ProviderForModel("xai", "grok-4.3", 1000); err != nil {
		t.Fatal(err)
	}
	if fallback.model != "grok-4.3" {
		t.Fatalf("model=%q", fallback.model)
	}
}
