package provider

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

type builtinProviderSpec struct {
	API      ProviderAPI
	BaseURLs map[ProviderAPI]string
	KeyEnv   []string
}

var builtinProviderCatalog = map[string]builtinProviderSpec{
	"ant-ling": {
		API:      ProviderAPIOpenAICompletions,
		BaseURLs: providerURLs(ProviderAPIOpenAICompletions, "https://api.ant-ling.com/v1"),
		KeyEnv:   []string{"ANT_LING_API_KEY"},
	},
	"anthropic": {
		API:      ProviderAPIAnthropicMessages,
		BaseURLs: providerURLs(ProviderAPIAnthropicMessages, "https://api.anthropic.com/v1"),
		KeyEnv:   []string{"ANTHROPIC_OAUTH_TOKEN", "ANTHROPIC_API_KEY"},
	},
	"azure-openai-responses": {
		API:    ProviderAPIOpenAIResponses,
		KeyEnv: []string{"AZURE_OPENAI_API_KEY"},
	},
	"cerebras": {
		API:      ProviderAPIOpenAICompletions,
		BaseURLs: providerURLs(ProviderAPIOpenAICompletions, "https://api.cerebras.ai/v1"),
		KeyEnv:   []string{"CEREBRAS_API_KEY"},
	},
	"cloudflare-ai-gateway": {
		API: ProviderAPIOpenAIResponses,
		BaseURLs: map[ProviderAPI]string{
			ProviderAPIAnthropicMessages: "https://gateway.ai.cloudflare.com/v1/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}/anthropic/v1",
			ProviderAPIOpenAICompletions: "https://gateway.ai.cloudflare.com/v1/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}/compat",
			ProviderAPIOpenAIResponses:   "https://gateway.ai.cloudflare.com/v1/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}/openai",
		},
		KeyEnv: []string{"CLOUDFLARE_API_KEY"},
	},
	"cloudflare-workers-ai": {
		API:      ProviderAPIOpenAICompletions,
		BaseURLs: providerURLs(ProviderAPIOpenAICompletions, "https://api.cloudflare.com/client/v4/accounts/{CLOUDFLARE_ACCOUNT_ID}/ai/v1"),
		KeyEnv:   []string{"CLOUDFLARE_API_KEY"},
	},
	"deepseek": {
		API:      ProviderAPIOpenAICompletions,
		BaseURLs: providerURLs(ProviderAPIOpenAICompletions, "https://api.deepseek.com"),
		KeyEnv:   []string{"DEEPSEEK_API_KEY"},
	},
	"fireworks": {
		API: ProviderAPIAnthropicMessages,
		BaseURLs: map[ProviderAPI]string{
			ProviderAPIAnthropicMessages: "https://api.fireworks.ai/inference/v1",
			ProviderAPIOpenAICompletions: "https://api.fireworks.ai/inference/v1",
		},
		KeyEnv: []string{"FIREWORKS_API_KEY"},
	},
	"google": {
		API:      ProviderAPIGoogleGenerative,
		BaseURLs: providerURLs(ProviderAPIGoogleGenerative, "https://generativelanguage.googleapis.com/v1beta"),
		KeyEnv:   []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"},
	},
	"google-vertex": {
		API:    ProviderAPIGoogleVertex,
		KeyEnv: []string{"GOOGLE_CLOUD_API_KEY"},
	},
	"groq": {
		API:      ProviderAPIOpenAICompletions,
		BaseURLs: providerURLs(ProviderAPIOpenAICompletions, "https://api.groq.com/openai/v1"),
		KeyEnv:   []string{"GROQ_API_KEY"},
	},
	"huggingface": {
		API:      ProviderAPIOpenAICompletions,
		BaseURLs: providerURLs(ProviderAPIOpenAICompletions, "https://router.huggingface.co/v1"),
		KeyEnv:   []string{"HF_TOKEN"},
	},
	"kimi-coding": {
		API:      ProviderAPIAnthropicMessages,
		BaseURLs: providerURLs(ProviderAPIAnthropicMessages, "https://api.kimi.com/coding/v1"),
		KeyEnv:   []string{"KIMI_API_KEY"},
	},
	"minimax": {
		API:      ProviderAPIAnthropicMessages,
		BaseURLs: providerURLs(ProviderAPIAnthropicMessages, "https://api.minimax.io/anthropic/v1"),
		KeyEnv:   []string{"MINIMAX_API_KEY"},
	},
	"minimax-cn": {
		API:      ProviderAPIAnthropicMessages,
		BaseURLs: providerURLs(ProviderAPIAnthropicMessages, "https://api.minimaxi.com/anthropic/v1"),
		KeyEnv:   []string{"MINIMAX_CN_API_KEY"},
	},
	"mistral": {
		API:      ProviderAPIMistral,
		BaseURLs: providerURLs(ProviderAPIMistral, "https://api.mistral.ai/v1"),
		KeyEnv:   []string{"MISTRAL_API_KEY"},
	},
	"moonshotai": {
		API:      ProviderAPIOpenAICompletions,
		BaseURLs: providerURLs(ProviderAPIOpenAICompletions, "https://api.moonshot.ai/v1"),
		KeyEnv:   []string{"MOONSHOT_API_KEY"},
	},
	"moonshotai-cn": {
		API:      ProviderAPIOpenAICompletions,
		BaseURLs: providerURLs(ProviderAPIOpenAICompletions, "https://api.moonshot.cn/v1"),
		KeyEnv:   []string{"MOONSHOT_API_KEY"},
	},
	"nvidia": {
		API:      ProviderAPIOpenAICompletions,
		BaseURLs: providerURLs(ProviderAPIOpenAICompletions, "https://integrate.api.nvidia.com/v1"),
		KeyEnv:   []string{"NVIDIA_API_KEY"},
	},
	"openai": {
		API:      ProviderAPIOpenAIResponses,
		BaseURLs: providerURLs(ProviderAPIOpenAIResponses, "https://api.openai.com/v1"),
		KeyEnv:   []string{"OPENAI_API_KEY"},
	},
	"openai-chat": {
		API:      ProviderAPIOpenAICompletions,
		BaseURLs: providerURLs(ProviderAPIOpenAICompletions, "https://api.openai.com/v1"),
		KeyEnv:   []string{"OPENAI_API_KEY"},
	},
	"openai-completions": {
		API:      ProviderAPIOpenAICompletions,
		BaseURLs: providerURLs(ProviderAPIOpenAICompletions, "https://api.openai.com/v1"),
		KeyEnv:   []string{"OPENAI_API_KEY"},
	},
	"openai-responses": {
		API:      ProviderAPIOpenAIResponses,
		BaseURLs: providerURLs(ProviderAPIOpenAIResponses, "https://api.openai.com/v1"),
		KeyEnv:   []string{"OPENAI_API_KEY"},
	},
	"openai-codex": {
		API:      ProviderAPIOpenAIResponses,
		BaseURLs: providerURLs(ProviderAPIOpenAIResponses, "https://chatgpt.com/backend-api/codex"),
		KeyEnv:   []string{"OPENAI_CODEX_TOKEN", "OPENAI_API_KEY"},
	},
	"opencode": {
		API: ProviderAPIOpenAICompletions,
		BaseURLs: map[ProviderAPI]string{
			ProviderAPIAnthropicMessages: "https://opencode.ai/zen/v1",
			ProviderAPIGoogleGenerative:  "https://opencode.ai/zen/v1",
			ProviderAPIOpenAICompletions: "https://opencode.ai/zen/v1",
			ProviderAPIOpenAIResponses:   "https://opencode.ai/zen/v1",
		},
		KeyEnv: []string{"OPENCODE_API_KEY"},
	},
	"opencode-go": {
		API: ProviderAPIOpenAICompletions,
		BaseURLs: map[ProviderAPI]string{
			ProviderAPIAnthropicMessages: "https://opencode.ai/zen/go/v1",
			ProviderAPIOpenAICompletions: "https://opencode.ai/zen/go/v1",
			ProviderAPIOpenAIResponses:   "https://opencode.ai/zen/go/v1",
		},
		KeyEnv: []string{"OPENCODE_API_KEY"},
	},
	"openrouter": {
		API:      ProviderAPIOpenAICompletions,
		BaseURLs: providerURLs(ProviderAPIOpenAICompletions, "https://openrouter.ai/api/v1"),
		KeyEnv:   []string{"OPENROUTER_API_KEY"},
	},
	"qwen-token-plan": {
		API:      ProviderAPIOpenAICompletions,
		BaseURLs: providerURLs(ProviderAPIOpenAICompletions, "https://token-plan.ap-southeast-1.maas.aliyuncs.com/compatible-mode/v1"),
		KeyEnv:   []string{"QWEN_TOKEN_PLAN_API_KEY"},
	},
	"qwen-token-plan-cn": {
		API:      ProviderAPIOpenAICompletions,
		BaseURLs: providerURLs(ProviderAPIOpenAICompletions, "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1"),
		KeyEnv:   []string{"QWEN_TOKEN_PLAN_CN_API_KEY"},
	},
	"radius": {
		API:      ProviderAPIPiMessages,
		BaseURLs: providerURLs(ProviderAPIPiMessages, "https://radius.pi.dev"),
		KeyEnv:   []string{"RADIUS_API_KEY"},
	},
	"together": {
		API:      ProviderAPIOpenAICompletions,
		BaseURLs: providerURLs(ProviderAPIOpenAICompletions, "https://api.together.ai/v1"),
		KeyEnv:   []string{"TOGETHER_API_KEY"},
	},
	"vercel-ai-gateway": {
		API:      ProviderAPIAnthropicMessages,
		BaseURLs: providerURLs(ProviderAPIAnthropicMessages, "https://ai-gateway.vercel.sh/v1"),
		KeyEnv:   []string{"AI_GATEWAY_API_KEY"},
	},
	"xai": {
		API: ProviderAPIOpenAICompletions,
		BaseURLs: map[ProviderAPI]string{
			ProviderAPIOpenAICompletions: "https://api.x.ai/v1",
			ProviderAPIOpenAIResponses:   "https://api.x.ai/v1",
		},
		KeyEnv: []string{"XAI_API_KEY"},
	},
	"xiaomi": {
		API:      ProviderAPIOpenAICompletions,
		BaseURLs: providerURLs(ProviderAPIOpenAICompletions, "https://api.xiaomimimo.com/v1"),
		KeyEnv:   []string{"XIAOMI_API_KEY"},
	},
	"xiaomi-token-plan-cn": {
		API:      ProviderAPIOpenAICompletions,
		BaseURLs: providerURLs(ProviderAPIOpenAICompletions, "https://token-plan-cn.xiaomimimo.com/v1"),
		KeyEnv:   []string{"XIAOMI_TOKEN_PLAN_CN_API_KEY"},
	},
	"xiaomi-token-plan-ams": {
		API:      ProviderAPIOpenAICompletions,
		BaseURLs: providerURLs(ProviderAPIOpenAICompletions, "https://token-plan-ams.xiaomimimo.com/v1"),
		KeyEnv:   []string{"XIAOMI_TOKEN_PLAN_AMS_API_KEY"},
	},
	"xiaomi-token-plan-sgp": {
		API:      ProviderAPIOpenAICompletions,
		BaseURLs: providerURLs(ProviderAPIOpenAICompletions, "https://token-plan-sgp.xiaomimimo.com/v1"),
		KeyEnv:   []string{"XIAOMI_TOKEN_PLAN_SGP_API_KEY"},
	},
	"zai": {
		API:      ProviderAPIOpenAICompletions,
		BaseURLs: providerURLs(ProviderAPIOpenAICompletions, "https://api.z.ai/api/coding/paas/v4"),
		KeyEnv:   []string{"ZAI_API_KEY"},
	},
	"zai-coding-cn": {
		API:      ProviderAPIOpenAICompletions,
		BaseURLs: providerURLs(ProviderAPIOpenAICompletions, "https://open.bigmodel.cn/api/coding/paas/v4"),
		KeyEnv:   []string{"ZAI_CODING_CN_API_KEY"},
	},
}

func providerURLs(api ProviderAPI, baseURL string) map[ProviderAPI]string {
	return map[ProviderAPI]string{api: baseURL}
}

func BuiltinProviderNames() []string {
	names := make([]string, 0, len(builtinProviderCatalog)+3)
	for name := range builtinProviderCatalog {
		names = append(names, name)
	}
	names = append(names, "amazon-bedrock", "bedrock", "bedrock-converse-stream")
	sort.Strings(names)
	return names
}

func providerCatalogConfig(name, model string) (HTTPProviderConfig, []string, bool, error) {
	spec, ok := builtinProviderCatalog[name]
	if !ok {
		return HTTPProviderConfig{}, nil, false, nil
	}
	api := providerAPIForModel(name, model, spec.API)
	base := spec.BaseURLs[api]
	if base == "" && name != "azure-openai-responses" && name != "google-vertex" {
		return HTTPProviderConfig{}, nil, true, fmt.Errorf("provider %q does not support API %q for model %q", name, api, model)
	}
	config := HTTPProviderConfig{Name: name, API: api, BaseURL: base}
	if strings.Contains(base, "{CLOUDFLARE_ACCOUNT_ID}") {
		account := strings.TrimSpace(os.Getenv("CLOUDFLARE_ACCOUNT_ID"))
		if account != "" {
			config.BaseURL = strings.ReplaceAll(config.BaseURL, "{CLOUDFLARE_ACCOUNT_ID}", account)
		}
	}
	if strings.Contains(base, "{CLOUDFLARE_GATEWAY_ID}") {
		gateway := strings.TrimSpace(os.Getenv("CLOUDFLARE_GATEWAY_ID"))
		if gateway != "" {
			config.BaseURL = strings.ReplaceAll(config.BaseURL, "{CLOUDFLARE_GATEWAY_ID}", gateway)
		}
	}
	return config, spec.KeyEnv, true, nil
}

func providerAPIForModel(provider, model string, fallback ProviderAPI) ProviderAPI {
	model = strings.ToLower(strings.TrimSpace(model))
	switch provider {
	case "xai":
		if model == "grok-4.5" {
			return ProviderAPIOpenAIResponses
		}
	case "fireworks":
		if strings.Contains(model, "glm-5p2") {
			return ProviderAPIOpenAICompletions
		}
		return ProviderAPIAnthropicMessages
	case "opencode":
		switch {
		case strings.HasPrefix(model, "gpt-"), model == "grok-4.5":
			return ProviderAPIOpenAIResponses
		case strings.HasPrefix(model, "claude-"), model == "qwen3.5-plus", model == "qwen3.6-plus":
			return ProviderAPIAnthropicMessages
		case strings.HasPrefix(model, "gemini-"):
			return ProviderAPIGoogleGenerative
		}
	case "opencode-go":
		switch {
		case model == "grok-4.5":
			return ProviderAPIOpenAIResponses
		case model == "minimax-m3", model == "qwen3.7-max", model == "qwen3.7-plus":
			return ProviderAPIAnthropicMessages
		}
	case "cloudflare-ai-gateway":
		switch {
		case strings.HasPrefix(model, "claude-"):
			return ProviderAPIAnthropicMessages
		case strings.HasPrefix(model, "gpt-"), strings.HasPrefix(model, "o1"), strings.HasPrefix(model, "o3"), strings.HasPrefix(model, "o4"):
			return ProviderAPIOpenAIResponses
		case strings.HasPrefix(model, "workers-ai/"):
			return ProviderAPIOpenAICompletions
		}
	}
	return fallback
}

func providerEnvValue(names []string) string {
	for _, name := range names {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}

func parseProviderAPI(value string) (ProviderAPI, error) {
	api := ProviderAPI(strings.ToLower(strings.TrimSpace(value)))
	switch api {
	case ProviderAPIOpenAICompletions,
		ProviderAPIOpenAIResponses,
		ProviderAPIAnthropicMessages,
		ProviderAPIGoogleGenerative,
		ProviderAPIGoogleVertex,
		ProviderAPIMistral,
		ProviderAPIPiMessages:
		return api, nil
	default:
		return "", fmt.Errorf("unsupported provider API %q", value)
	}
}
