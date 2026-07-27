package engine

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	providerpkg "github.com/ageniti/ergo-agent/provider"
)

func TestRuntimeEnablesOpenRouterImageGenerationFromEnvironment(t *testing.T) {
	var model string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		model, _ = body["model"].(string)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"images":[]}}]}`))
	}))
	defer server.Close()

	t.Setenv("OPENROUTER_API_KEY", "router-key")
	t.Setenv("AGENT_IMAGE_BASE_URL", server.URL)
	t.Setenv("AGENT_IMAGE_MODEL", "openai/gpt-image-1")

	runtime := New(t.TempDir())
	if runtime.Images == nil {
		t.Fatal("runtime image generator is nil")
	}
	result := runtime.Images.GenerateImage(context.Background(), ImageGenerationRequest{Prompt: "draw"})
	if result.StopReason != "stop" || model != "openai/gpt-image-1" {
		t.Fatalf("result=%+v model=%q", result, model)
	}
}

type fakeImageGenerator struct {
	request ImageGenerationRequest
	result  ImageGenerationResult
}

func (f *fakeImageGenerator) GenerateImage(_ context.Context, request ImageGenerationRequest) ImageGenerationResult {
	f.request = request
	return f.result
}

func TestGenerateImageToolSupportsWorkspaceReferencesAndAttachments(t *testing.T) {
	root := t.TempDir()
	referenceData := tinyPNGBase64(t)
	decoded, err := base64.StdEncoding.DecodeString(referenceData)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "reference.png"), decoded, 0600); err != nil {
		t.Fatal(err)
	}
	generator := &fakeImageGenerator{result: ImageGenerationResult{
		API:        providerpkg.OpenRouterImagesAPI,
		Provider:   "openrouter",
		Model:      providerpkg.DefaultOpenRouterImageModel,
		ResponseID: "image-1",
		Usage:      map[string]any{"total": 3},
		StopReason: "stop",
		Images:     []Image{{MimeType: "image/png", Data: referenceData}},
	}}
	tools := &toolset{cwd: root, images: generator}
	definitions := tools.definitions([]string{"generate_image"})
	if len(definitions) != 1 {
		t.Fatalf("definitions=%v", definitions)
	}
	result, err := definitions[0].Execute(context.Background(), json.RawMessage(`{"prompt":"make it blue","referencePaths":["reference.png"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if generator.request.Prompt != "make it blue" || len(generator.request.Images) != 1 || len(result.Images) != 1 || !strings.Contains(result.Text, "Generated 1 image") {
		t.Fatalf("request=%+v result=%+v", generator.request, result)
	}
	if result.Details["imageGeneration"].(map[string]any)["responseId"] != "image-1" {
		t.Fatalf("details=%v", result.Details)
	}
	if definitions := (&toolset{cwd: root, planMode: true, images: generator}).definitions([]string{"generate_image"}); len(definitions) != 0 {
		t.Fatalf("plan mode definitions=%v", definitions)
	}
	if contains(planTools([]string{"read", "generate_image"}), "generate_image") {
		t.Fatal("plan mode must not include image generation")
	}
}

func tinyPNGBase64(t *testing.T) string {
	t.Helper()
	var encoded bytes.Buffer
	pixel := image.NewRGBA(image.Rect(0, 0, 1, 1))
	pixel.Set(0, 0, color.RGBA{R: 255, A: 255})
	if err := png.Encode(&encoded, pixel); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(encoded.Bytes())
}
