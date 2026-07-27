package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestOpenRouterImageGeneratorUsesPiCompatibleRequestAndResponseShape(t *testing.T) {
	var body map[string]any
	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		authorization = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		_, _ = w.Write([]byte(`{
			"id":"image-1",
			"usage":{"prompt_tokens":12,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":3,"cache_write_tokens":1}},
			"choices":[{"message":{
				"content":"Rendered.",
				"images":[
					{"image_url":"data:image/png;base64,ZmFrZS1wbmc="},
					{"image_url":{"url":"https://cdn.example/ignored.png"}}
				]
			}}]
		}`))
	}))
	defer server.Close()
	generator := NewOpenRouterImageGenerator(OpenRouterImageGeneratorConfig{APIKey: "router-key", BaseURL: server.URL})
	result := generator.GenerateImage(context.Background(), ImageGenerationRequest{
		Prompt: "A red circle",
		Images: []Image{{MimeType: "image/png", Data: "aQ=="}},
	})
	if result.StopReason != "stop" || result.ResponseID != "image-1" || result.Text != "Rendered." || len(result.Images) != 1 {
		t.Fatalf("result=%+v", result)
	}
	if authorization != "Bearer router-key" {
		t.Fatalf("authorization=%q", authorization)
	}
	if body["model"] != DefaultOpenRouterImageModel || !reflect.DeepEqual(body["modalities"], []any{"image", "text"}) || body["stream"] != false {
		t.Fatalf("body=%v", body)
	}
	content := body["messages"].([]any)[0].(map[string]any)["content"].([]any)
	if len(content) != 2 || content[0].(map[string]any)["type"] != "text" || content[1].(map[string]any)["type"] != "image_url" {
		t.Fatalf("content=%v", content)
	}
	if result.Usage["input"] != 9 || result.Usage["cacheRead"] != 2 || result.Usage["cacheWrite"] != 1 || result.Usage["total"] != 17 {
		t.Fatalf("usage=%v", result.Usage)
	}
}

func TestOpenRouterImageGeneratorUsesCatalogModalitiesAndNeverReturnsErrors(t *testing.T) {
	var modalities any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		modalities = body["modalities"]
		_, _ = w.Write([]byte(`{"choices":[{"message":{"images":[]}}]}`))
	}))
	defer server.Close()
	generator := NewOpenRouterImageGenerator(OpenRouterImageGeneratorConfig{APIKey: "key", BaseURL: server.URL})
	result := generator.GenerateImage(context.Background(), ImageGenerationRequest{Model: "black-forest-labs/flux.2-pro", Prompt: "draw"})
	if result.StopReason != "stop" || !reflect.DeepEqual(modalities, []any{"image"}) {
		t.Fatalf("result=%+v modalities=%v", result, modalities)
	}
	failing := NewOpenRouterImageGenerator(OpenRouterImageGeneratorConfig{APIKey: "key", BaseURL: "://invalid"})
	result = failing.GenerateImage(context.Background(), ImageGenerationRequest{Prompt: "draw"})
	if result.StopReason != "error" || result.ErrorMessage == "" {
		t.Fatalf("result=%+v", result)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result = generator.GenerateImage(ctx, ImageGenerationRequest{Prompt: "draw"})
	if result.StopReason != "aborted" {
		t.Fatalf("result=%+v", result)
	}
}

func TestBuiltinImageModelsMirrorPiOpenRouterCatalog(t *testing.T) {
	models := BuiltinImageModels()
	if len(models) != 39 {
		t.Fatalf("models=%d", len(models))
	}
	var foundDefault, foundFlux bool
	for _, model := range models {
		if model.ID == DefaultOpenRouterImageModel && model.InputImages && model.OutputText {
			foundDefault = true
		}
		if model.ID == "black-forest-labs/flux.2-pro" && model.InputImages && !model.OutputText {
			foundFlux = true
		}
	}
	if !foundDefault || !foundFlux {
		t.Fatalf("models=%v", models)
	}
}

func TestOpenRouterImageGeneratorFromEnvironment(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "router-key")
	t.Setenv("AGENT_IMAGE_BASE_URL", "https://images.example/v1")
	t.Setenv("AGENT_IMAGE_MODEL", "openai/gpt-image-1")

	generator, ok := NewOpenRouterImageGeneratorFromEnv().(*openRouterImageGenerator)
	if !ok {
		t.Fatalf("image generator=%T", generator)
	}
	if generator.apiKey != "router-key" || generator.baseURL != "https://images.example/v1" || generator.defaultModel != "openai/gpt-image-1" {
		t.Fatalf("generator=%+v", generator)
	}
}
