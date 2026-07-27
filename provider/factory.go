package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"golang.org/x/oauth2/google"
)

type HTTPProviderFactory struct{}

func (p *openAIProvider) ProviderHeaders(in CompletionRequest) map[string]string {
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
	return headers
}

func (p *anthropicProvider) ProviderHeaders(in CompletionRequest) map[string]string {
	return p.requestHeaders(in)
}

func (p *googleProvider) ProviderHeaders(CompletionRequest) map[string]string {
	headers := cloneHeaders(p.headers)
	if p.key != "" {
		headers["x-goog-api-key"] = p.key
	}
	return headers
}

func (factory HTTPProviderFactory) Provider(name string, timeoutMS int) (Provider, error) {
	return factory.ProviderForModel(name, "", timeoutMS)
}

func (HTTPProviderFactory) ProviderForModel(name, model string, timeoutMS int) (Provider, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return nil, fmt.Errorf("provider is required")
	}
	if name == "amazon-bedrock" || name == "bedrock" || name == "bedrock-converse-stream" {
		region := first(os.Getenv("AWS_REGION"), os.Getenv("AWS_DEFAULT_REGION"), "us-east-1")
		options := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(region)}
		if timeoutMS > 0 {
			options = append(options, awsconfig.WithHTTPClient(&http.Client{Timeout: time.Duration(timeoutMS) * time.Millisecond}))
		}
		cfg, err := awsconfig.LoadDefaultConfig(context.Background(), options...)
		if err != nil {
			return nil, fmt.Errorf("load AWS configuration: %w", err)
		}
		return &bedrockProvider{client: bedrockruntime.NewFromConfig(cfg)}, nil
	}
	key := regexp.MustCompile(`[^A-Z0-9]+`).ReplaceAllString(strings.ToUpper(name), "_")
	config, keyEnvs, known, err := providerCatalogConfig(name, model)
	if err != nil {
		return nil, err
	}
	if !known {
		config = HTTPProviderConfig{Name: name, API: ProviderAPIOpenAICompletions}
	}
	if rawAPI := os.Getenv("AGENT_PROVIDER_" + key + "_API"); strings.TrimSpace(rawAPI) != "" {
		config.API, err = parseProviderAPI(rawAPI)
		if err != nil {
			return nil, fmt.Errorf("AGENT_PROVIDER_%s_API: %w", key, err)
		}
	}
	if base := strings.TrimSpace(os.Getenv("AGENT_PROVIDER_" + key + "_BASE_URL")); base != "" {
		config.BaseURL = base
	}
	config.APIKey = first(strings.TrimSpace(os.Getenv("AGENT_PROVIDER_"+key+"_API_KEY")), providerEnvValue(keyEnvs))
	customHeaders, err := providerHeaders(key)
	if err != nil {
		return nil, err
	}
	config.Headers = customHeaders
	config.APIVersion = first(os.Getenv("AZURE_OPENAI_API_VERSION"), "v1")
	if name == "azure-openai-responses" {
		config.ModelMap = parseNameMap(os.Getenv("AZURE_OPENAI_DEPLOYMENT_NAME_MAP"))
	}
	if name == "cloudflare-ai-gateway" {
		if config.APIKey != "" && !hasHeader(config.Headers, "cf-aig-authorization") {
			config.Headers["cf-aig-authorization"] = "Bearer " + config.APIKey
		}
		config.DisableDefaultAuth = true
	}
	timeout := 10 * time.Minute
	if timeoutMS > 0 {
		timeout = time.Duration(timeoutMS) * time.Millisecond
	}
	if config.API == ProviderAPIGoogleVertex {
		if config.BaseURL == "" {
			project := first(os.Getenv("GOOGLE_CLOUD_PROJECT"), os.Getenv("GCLOUD_PROJECT"))
			location := first(os.Getenv("GOOGLE_CLOUD_LOCATION"), "us-central1")
			if project == "" {
				return nil, fmt.Errorf("GOOGLE_CLOUD_PROJECT is required for google-vertex")
			}
			config.BaseURL = fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/publishers/google", location, project, location)
		}
		if config.APIKey != "" {
			config.Headers["x-goog-api-key"] = config.APIKey
			return &googleProvider{name: config.Name, base: strings.TrimRight(config.BaseURL, "/"), client: &http.Client{Timeout: timeout}, headers: config.Headers}, nil
		}
		client, err := google.DefaultClient(context.Background(), "https://www.googleapis.com/auth/cloud-platform")
		if err != nil {
			return nil, fmt.Errorf("load Google Application Default Credentials: %w", err)
		}
		client.Timeout = timeout
		return &googleProvider{name: config.Name, base: strings.TrimRight(config.BaseURL, "/"), client: client, headers: config.Headers}, nil
	}
	if name == "azure-openai-responses" && strings.TrimSpace(os.Getenv("AGENT_PROVIDER_"+key+"_BASE_URL")) == "" {
		config.BaseURL = first(os.Getenv("AZURE_OPENAI_BASE_URL"), config.BaseURL)
		if config.BaseURL == "" {
			if resource := os.Getenv("AZURE_OPENAI_RESOURCE_NAME"); resource != "" {
				config.BaseURL = "https://" + resource + ".openai.azure.com/openai/v1"
			}
		}
		config.BaseURL = normalizeAzureOpenAIBaseURL(config.BaseURL)
	}
	if config.BaseURL == "" {
		return nil, fmt.Errorf("AGENT_PROVIDER_%s_BASE_URL is required for provider %q", key, name)
	}
	if strings.Contains(config.BaseURL, "{CLOUDFLARE_ACCOUNT_ID}") {
		return nil, fmt.Errorf("CLOUDFLARE_ACCOUNT_ID or AGENT_PROVIDER_%s_BASE_URL is required for %s", key, name)
	}
	if strings.Contains(config.BaseURL, "{CLOUDFLARE_GATEWAY_ID}") {
		return nil, fmt.Errorf("CLOUDFLARE_GATEWAY_ID or AGENT_PROVIDER_%s_BASE_URL is required for %s", key, name)
	}
	if name == "openai" || name == "openai-chat" || name == "openai-completions" || name == "openai-responses" {
		if organization := os.Getenv("OPENAI_ORGANIZATION"); organization != "" {
			config.Headers["OpenAI-Organization"] = organization
		}
		if project := os.Getenv("OPENAI_PROJECT"); project != "" {
			config.Headers["OpenAI-Project"] = project
		}
	}
	return NewHTTPProvider(config, timeoutMS)
}

func NewHTTPProvider(config HTTPProviderConfig, timeoutMS int) (Provider, error) {
	config.Name = strings.ToLower(strings.TrimSpace(config.Name))
	if config.API == "" {
		return nil, fmt.Errorf("provider API is required")
	}
	if strings.TrimSpace(config.BaseURL) == "" {
		return nil, fmt.Errorf("provider base URL is required")
	}
	if strings.TrimSpace(config.APIKey) == "" && !hasProviderAuthorization(config.Headers) {
		return nil, fmt.Errorf("provider API key is required")
	}
	timeout := 10 * time.Minute
	if timeoutMS > 0 {
		timeout = time.Duration(timeoutMS) * time.Millisecond
	}
	client := &http.Client{Timeout: timeout}
	base := strings.TrimRight(config.BaseURL, "/")
	headers := cloneHeaders(config.Headers)
	apiKey := config.APIKey
	if config.DisableDefaultAuth {
		apiKey = ""
	}
	switch config.API {
	case ProviderAPIOpenAICompletions:
		return &openAIProvider{base: base, key: apiKey, client: client, dialect: config.Name, headers: headers}, nil
	case ProviderAPIOpenAIResponses:
		dialect := config.Name
		if !config.DisableDefaultAuth && config.APIKey != "" {
			switch dialect {
			case "openai-codex":
				headers["Authorization"] = "Bearer " + config.APIKey
				if account := first(os.Getenv("OPENAI_ACCOUNT_ID"), codexAccountID(config.APIKey)); account != "" {
					headers["ChatGPT-Account-Id"] = account
				}
				headers["originator"] = "pi"
				headers["User-Agent"] = "pi (go-native-agent-runtime)"
				headers["OpenAI-Beta"] = "responses=experimental"
			case "azure-openai-responses":
				headers["api-key"] = config.APIKey
			default:
				headers["Authorization"] = "Bearer " + config.APIKey
			}
		}
		return &responsesProvider{base: base, client: client, headers: headers, dialect: dialect, apiVersion: first(config.APIVersion, "v1"), modelMap: config.ModelMap}, nil
	case ProviderAPIAnthropicMessages:
		return &anthropicProvider{name: config.Name, base: base, key: apiKey, client: client, headers: headers}, nil
	case ProviderAPIGoogleGenerative:
		if !config.DisableDefaultAuth {
			headers["x-goog-api-key"] = config.APIKey
		}
		return &googleProvider{name: config.Name, base: base, key: apiKey, client: client, headers: headers}, nil
	case ProviderAPIMistral:
		return &mistralProvider{openAIProvider: openAIProvider{base: base, key: apiKey, client: client, dialect: "mistral", headers: headers}}, nil
	case ProviderAPIPiMessages:
		if !config.DisableDefaultAuth && config.APIKey != "" && !hasHeader(headers, "authorization") {
			headers["Authorization"] = "Bearer " + config.APIKey
		}
		return &piMessagesProvider{name: config.Name, base: base, client: client, headers: headers}, nil
	default:
		return nil, fmt.Errorf("provider API %q requires a specialized factory", config.API)
	}
}

func hasProviderAuthorization(headers map[string]string) bool {
	for key, value := range headers {
		if strings.TrimSpace(value) == "" {
			continue
		}
		switch strings.ToLower(key) {
		case "authorization", "x-api-key", "x-goog-api-key", "api-key", "cf-aig-authorization":
			return true
		}
	}
	return false
}

func hasHeader(headers map[string]string, name string) bool {
	for key := range headers {
		if strings.EqualFold(key, name) {
			return true
		}
	}
	return false
}

func normalizeAzureOpenAIBaseURL(base string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" || strings.HasSuffix(base, "/openai/v1") {
		return base
	}
	return base + "/openai/v1"
}

func parseNameMap(value string) map[string]string {
	result := map[string]string{}
	for _, item := range strings.Split(value, ",") {
		key, mapped, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		key, mapped = strings.TrimSpace(key), strings.TrimSpace(mapped)
		if key != "" && mapped != "" {
			result[key] = mapped
		}
	}
	return result
}

func providerHeaders(key string) (map[string]string, error) {
	headers := map[string]string{}
	raw := strings.TrimSpace(os.Getenv("AGENT_PROVIDER_" + key + "_HEADERS"))
	if raw == "" {
		return headers, nil
	}
	if err := json.Unmarshal([]byte(raw), &headers); err != nil {
		return nil, fmt.Errorf("decode AGENT_PROVIDER_%s_HEADERS: %w", key, err)
	}
	return headers, nil
}

func cloneHeaders(source map[string]string) map[string]string {
	result := make(map[string]string, len(source)+2)
	for key, value := range source {
		result[key] = value
	}
	return result
}

func codexAccountID(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims map[string]any
	if json.Unmarshal(payload, &claims) != nil {
		return ""
	}
	for _, key := range []string{"chatgpt_account_id", "account_id"} {
		if value, _ := claims[key].(string); value != "" {
			return value
		}
	}
	if auth, _ := claims["https://api.openai.com/auth"].(map[string]any); auth != nil {
		for _, key := range []string{"chatgpt_account_id", "account_id"} {
			if value, _ := auth[key].(string); value != "" {
				return value
			}
		}
	}
	return ""
}
