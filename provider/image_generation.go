package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

const (
	OpenRouterImagesAPI          = "openrouter-images"
	DefaultOpenRouterImageModel  = "google/gemini-3.1-flash-image"
	defaultOpenRouterImagesBase  = "https://openrouter.ai/api/v1"
	defaultImageRequestTimeoutMS = 5 * 60 * 1000
)

// ImageModel describes an image model's input and output capabilities.
// The built-in catalog mirrors Pi's generated OpenRouter image catalog without
// requiring a runtime request to a remote model directory.
type ImageModel struct {
	ID, Provider string
	InputImages  bool
	OutputText   bool
}

var builtinOpenRouterImageModels = map[string]ImageModel{
	"black-forest-labs/flux.2-flex":         {ID: "black-forest-labs/flux.2-flex", Provider: "openrouter", InputImages: true},
	"black-forest-labs/flux.2-klein-4b":     {ID: "black-forest-labs/flux.2-klein-4b", Provider: "openrouter", InputImages: true},
	"black-forest-labs/flux.2-max":          {ID: "black-forest-labs/flux.2-max", Provider: "openrouter", InputImages: true},
	"black-forest-labs/flux.2-pro":          {ID: "black-forest-labs/flux.2-pro", Provider: "openrouter", InputImages: true},
	"bytedance-seed/seedream-4.5":           {ID: "bytedance-seed/seedream-4.5", Provider: "openrouter", InputImages: true},
	"google/gemini-2.5-flash-image":         {ID: "google/gemini-2.5-flash-image", Provider: "openrouter", InputImages: true, OutputText: true},
	"google/gemini-3-pro-image":             {ID: "google/gemini-3-pro-image", Provider: "openrouter", InputImages: true, OutputText: true},
	"google/gemini-3-pro-image-preview":     {ID: "google/gemini-3-pro-image-preview", Provider: "openrouter", InputImages: true, OutputText: true},
	"google/gemini-3.1-flash-image":         {ID: "google/gemini-3.1-flash-image", Provider: "openrouter", InputImages: true, OutputText: true},
	"google/gemini-3.1-flash-image-preview": {ID: "google/gemini-3.1-flash-image-preview", Provider: "openrouter", InputImages: true, OutputText: true},
	"google/gemini-3.1-flash-lite-image":    {ID: "google/gemini-3.1-flash-lite-image", Provider: "openrouter", InputImages: true, OutputText: true},
	"krea/krea-2-large":                     {ID: "krea/krea-2-large", Provider: "openrouter", InputImages: true},
	"krea/krea-2-medium":                    {ID: "krea/krea-2-medium", Provider: "openrouter", InputImages: true},
	"krea/krea-2-medium-turbo":              {ID: "krea/krea-2-medium-turbo", Provider: "openrouter", InputImages: true},
	"microsoft/mai-image-2.5":               {ID: "microsoft/mai-image-2.5", Provider: "openrouter", InputImages: true},
	"openai/gpt-5-image":                    {ID: "openai/gpt-5-image", Provider: "openrouter", InputImages: true, OutputText: true},
	"openai/gpt-5-image-mini":               {ID: "openai/gpt-5-image-mini", Provider: "openrouter", InputImages: true, OutputText: true},
	"openai/gpt-5.4-image-2":                {ID: "openai/gpt-5.4-image-2", Provider: "openrouter", InputImages: true, OutputText: true},
	"openai/gpt-image-1":                    {ID: "openai/gpt-image-1", Provider: "openrouter", InputImages: true},
	"openai/gpt-image-1-mini":               {ID: "openai/gpt-image-1-mini", Provider: "openrouter", InputImages: true},
	"openai/gpt-image-2":                    {ID: "openai/gpt-image-2", Provider: "openrouter", InputImages: true},
	"openrouter/auto":                       {ID: "openrouter/auto", Provider: "openrouter", InputImages: true, OutputText: true},
	"openrouter/auto-beta":                  {ID: "openrouter/auto-beta", Provider: "openrouter", InputImages: true, OutputText: true},
	"recraft/recraft-v3":                    {ID: "recraft/recraft-v3", Provider: "openrouter", InputImages: true},
	"recraft/recraft-v4":                    {ID: "recraft/recraft-v4", Provider: "openrouter", InputImages: true},
	"recraft/recraft-v4-pro":                {ID: "recraft/recraft-v4-pro", Provider: "openrouter", InputImages: true},
	"recraft/recraft-v4-pro-vector":         {ID: "recraft/recraft-v4-pro-vector", Provider: "openrouter", InputImages: true},
	"recraft/recraft-v4-vector":             {ID: "recraft/recraft-v4-vector", Provider: "openrouter", InputImages: true},
	"recraft/recraft-v4.1":                  {ID: "recraft/recraft-v4.1", Provider: "openrouter", InputImages: true},
	"recraft/recraft-v4.1-pro":              {ID: "recraft/recraft-v4.1-pro", Provider: "openrouter", InputImages: true},
	"recraft/recraft-v4.1-pro-vector":       {ID: "recraft/recraft-v4.1-pro-vector", Provider: "openrouter", InputImages: true},
	"recraft/recraft-v4.1-utility":          {ID: "recraft/recraft-v4.1-utility", Provider: "openrouter", InputImages: true},
	"recraft/recraft-v4.1-utility-pro":      {ID: "recraft/recraft-v4.1-utility-pro", Provider: "openrouter", InputImages: true},
	"recraft/recraft-v4.1-vector":           {ID: "recraft/recraft-v4.1-vector", Provider: "openrouter", InputImages: true},
	"sourceful/riverflow-v2-fast":           {ID: "sourceful/riverflow-v2-fast", Provider: "openrouter", InputImages: true},
	"sourceful/riverflow-v2-pro":            {ID: "sourceful/riverflow-v2-pro", Provider: "openrouter", InputImages: true},
	"sourceful/riverflow-v2.5-fast":         {ID: "sourceful/riverflow-v2.5-fast", Provider: "openrouter", InputImages: true},
	"sourceful/riverflow-v2.5-pro":          {ID: "sourceful/riverflow-v2.5-pro", Provider: "openrouter", InputImages: true},
	"x-ai/grok-imagine-image-quality":       {ID: "x-ai/grok-imagine-image-quality", Provider: "openrouter", InputImages: true},
}

// BuiltinImageModels returns a sorted copy of Pi's current static OpenRouter
// image-model capabilities. Custom model IDs remain supported by the provider.
func BuiltinImageModels() []ImageModel {
	models := make([]ImageModel, 0, len(builtinOpenRouterImageModels))
	for _, model := range builtinOpenRouterImageModels {
		models = append(models, model)
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models
}

// OpenRouterImageGeneratorConfig configures Pi-compatible image generation
// through OpenRouter's non-streaming chat-completions image API.
type OpenRouterImageGeneratorConfig struct {
	APIKey, BaseURL, DefaultModel string
	Headers                       map[string]string
	TimeoutMS                     int
}

type openRouterImageGenerator struct {
	apiKey, baseURL, defaultModel string
	headers                       map[string]string
	client                        *http.Client
}

func NewOpenRouterImageGenerator(config OpenRouterImageGeneratorConfig) ImageGenerator {
	timeoutMS := config.TimeoutMS
	if timeoutMS <= 0 {
		timeoutMS = defaultImageRequestTimeoutMS
	}
	return &openRouterImageGenerator{
		apiKey:       strings.TrimSpace(config.APIKey),
		baseURL:      strings.TrimRight(first(strings.TrimSpace(config.BaseURL), defaultOpenRouterImagesBase), "/"),
		defaultModel: first(strings.TrimSpace(config.DefaultModel), DefaultOpenRouterImageModel),
		headers:      cloneHeaders(config.Headers),
		client:       &http.Client{Timeout: time.Duration(timeoutMS) * time.Millisecond},
	}
}

// NewOpenRouterImageGeneratorFromEnv configures the optional built-in image
// generator when OPENROUTER_API_KEY is present. Runtime.New uses this helper.
func NewOpenRouterImageGeneratorFromEnv() ImageGenerator {
	apiKey := strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY"))
	if apiKey == "" {
		return nil
	}
	return NewOpenRouterImageGenerator(OpenRouterImageGeneratorConfig{
		APIKey:       apiKey,
		BaseURL:      os.Getenv("AGENT_IMAGE_BASE_URL"),
		DefaultModel: os.Getenv("AGENT_IMAGE_MODEL"),
	})
}

func (p *openRouterImageGenerator) GenerateImage(ctx context.Context, request ImageGenerationRequest) ImageGenerationResult {
	model := first(strings.TrimSpace(request.Model), p.defaultModel)
	result := ImageGenerationResult{
		API:        OpenRouterImagesAPI,
		Provider:   "openrouter",
		Model:      model,
		StopReason: "stop",
		Timestamp:  nowMillis(),
	}
	if p.apiKey == "" {
		return imageGenerationFailure(result, ctx, errors.New("OPENROUTER_API_KEY is required for image generation"))
	}
	if strings.TrimSpace(request.Prompt) == "" && len(request.Images) == 0 {
		return imageGenerationFailure(result, ctx, errors.New("an image prompt or reference image is required"))
	}
	if known, ok := builtinOpenRouterImageModels[model]; ok && len(request.Images) > 0 && !known.InputImages {
		return imageGenerationFailure(result, ctx, fmt.Errorf("image model %q does not accept reference images", model))
	}
	content := make([]map[string]any, 0, len(request.Images)+1)
	if request.Prompt != "" {
		content = append(content, map[string]any{"type": "text", "text": request.Prompt})
	}
	for index, image := range request.Images {
		if strings.TrimSpace(image.MimeType) == "" || strings.TrimSpace(image.Data) == "" {
			return imageGenerationFailure(result, ctx, fmt.Errorf("reference image %d requires mimeType and data", index+1))
		}
		content = append(content, map[string]any{
			"type":      "image_url",
			"image_url": map[string]any{"url": "data:" + image.MimeType + ";base64," + image.Data},
		})
	}
	modalities := []string{"image"}
	if modelInfo, ok := builtinOpenRouterImageModels[model]; ok && modelInfo.OutputText {
		modalities = append(modalities, "text")
	}
	body := map[string]any{
		"model":      model,
		"messages":   []map[string]any{{"role": "user", "content": content}},
		"stream":     false,
		"modalities": modalities,
	}
	headers := cloneHeaders(p.headers)
	headers["Authorization"] = "Bearer " + p.apiKey
	var out struct {
		ID      string         `json:"id"`
		Usage   map[string]any `json:"usage"`
		Choices []struct {
			Message struct {
				Content any `json:"content"`
				Images  []struct {
					ImageURL json.RawMessage `json:"image_url"`
				} `json:"images"`
			} `json:"message"`
		} `json:"choices"`
	}
	_, _, err := doJSON(ctx, p.client, p.baseURL+"/chat/completions", mergeHeaders(headers, request.Headers), body, &out)
	if err != nil {
		return imageGenerationFailure(result, ctx, err)
	}
	result.ResponseID = out.ID
	result.Usage = imageGenerationUsage(out.Usage)
	if len(out.Choices) == 0 {
		return imageGenerationFailure(result, ctx, errors.New("image provider returned no choices"))
	}
	result.Text = contentText(out.Choices[0].Message.Content)
	for _, generated := range out.Choices[0].Message.Images {
		if image, ok := generatedImageDataURL(generated.ImageURL); ok {
			result.Images = append(result.Images, image)
		}
	}
	return result
}

func imageGenerationFailure(result ImageGenerationResult, ctx context.Context, err error) ImageGenerationResult {
	result.StopReason = "error"
	if errors.Is(ctx.Err(), context.Canceled) {
		result.StopReason = "aborted"
	}
	result.ErrorMessage = err.Error()
	return result
}

func generatedImageDataURL(raw json.RawMessage) (Image, bool) {
	var value string
	if json.Unmarshal(raw, &value) != nil {
		var wrapped struct {
			URL string `json:"url"`
		}
		if json.Unmarshal(raw, &wrapped) != nil {
			return Image{}, false
		}
		value = wrapped.URL
	}
	if !strings.HasPrefix(value, "data:") {
		return Image{}, false
	}
	meta, data, ok := strings.Cut(strings.TrimPrefix(value, "data:"), ",")
	if !ok || !strings.HasSuffix(meta, ";base64") || data == "" {
		return Image{}, false
	}
	mimeType := strings.TrimSuffix(meta, ";base64")
	if mimeType == "" {
		return Image{}, false
	}
	return Image{MimeType: mimeType, Data: data}, true
}

func imageGenerationUsage(raw map[string]any) map[string]any {
	if raw == nil {
		return nil
	}
	prompt := int(number(raw["prompt_tokens"]))
	completion := int(number(raw["completion_tokens"]))
	details, _ := raw["prompt_tokens_details"].(map[string]any)
	cacheRead := int(number(details["cached_tokens"]))
	cacheWrite := int(number(details["cache_write_tokens"]))
	if cacheWrite > 0 {
		cacheRead = max(0, cacheRead-cacheWrite)
	}
	input := max(0, prompt-cacheRead-cacheWrite)
	return map[string]any{
		"input":      input,
		"output":     completion,
		"cacheRead":  cacheRead,
		"cacheWrite": cacheWrite,
		"total":      input + completion + cacheRead + cacheWrite,
	}
}
