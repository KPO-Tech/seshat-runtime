package providers

import (
	"fmt"
	"os"

	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// DefaultSonnetModel and DefaultHaikuModel are the Claude identifiers used as
// defaults across the engine, SDK, and the Anthropic alias map below.
const (
	DefaultSonnetModel = "claude-sonnet-5-5"
	DefaultHaikuModel  = "claude-haiku-5-5"
	DefaultOpusModel   = "claude-opus-5-5"
)

// RoutingConfig defines provider routing and fallback behavior.
type RoutingConfig struct {
	// Priority determines which provider to try first (lower = higher priority).
	Priority int `json:"priority"`

	// FallbackModels defines a chain of models to try if the primary fails.
	FallbackModels []types.ModelIdentifier `json:"fallback_models,omitempty"`

	// FallbackProviders defines a chain of providers to try if all models fail.
	FallbackProviders []types.APIProvider `json:"fallback_providers,omitempty"`

	// CircuitBreaker tracks provider health.
	CircuitBreaker *CircuitBreakerConfig `json:"circuit_breaker,omitempty"`
}

type Config struct {
	Provider          types.APIProvider
	APIKey            string
	BaseURL           string
	ModelAliasMapping map[string]string
	CustomHeaders     map[string]string
	Routing           *RoutingConfig
}

func DefaultConfigs() map[types.APIProvider]*Config {
	return map[types.APIProvider]*Config{
		types.APIProviderAnthropic: {
			Provider: types.APIProviderAnthropic,
			BaseURL:  "https://api.anthropic.com",
			ModelAliasMapping: map[string]string{
				"sonnet": DefaultSonnetModel,
				"haiku":  DefaultHaikuModel,
				"opus":   DefaultOpusModel,
				"fable":  "claude-fable-5-1",
			},
			Routing: &RoutingConfig{
				Priority: 1,
				FallbackModels: []types.ModelIdentifier{
					{Provider: types.APIProviderAnthropic, Model: DefaultSonnetModel},
					{Provider: types.APIProviderAnthropic, Model: DefaultHaikuModel},
				},
				// No default FallbackProviders: a session that picked
				// Anthropic must not silently continue on a different
				// vendor just because an OpenAI key happens to be present
				// (M5f in TODO.md, privacy) - cross-vendor fallback is
				// opt-in only, via ProviderConfig.Routing.FallbackProviders.
				CircuitBreaker: DefaultCircuitBreakerConfig(),
			},
		},
		types.APIProviderOpenAI: {
			Provider: types.APIProviderOpenAI,
			BaseURL:  "https://api.openai.com/v1",
			ModelAliasMapping: map[string]string{
				"default": "gpt-6.1-sol",
				"gpt-6.1": "gpt-6.1-sol",
				"sol":     "gpt-6.1-sol",
				"astra":   "gpt-6-astra",
				"mini":    "gpt-6-luna",
				"luna":    "gpt-6-luna",
				"gpt-5.5": "gpt-5.5",
			},
		},
		types.APIProviderOllama: {
			Provider: types.APIProviderOllama,
			BaseURL:  "http://localhost:11434",
			ModelAliasMapping: map[string]string{
				"qwen-coder": "qwen2.5-coder:7b",
				"deepseek":   "deepseek-r1:14b",
				"llama":      "llama3.1:8b",
				"mistral":    "mistral:7b",
				"codellama":  "codellama:7b",
				"phi":        "phi3:3.8b",
			},
		},
		types.APIProviderGemini: {
			Provider: types.APIProviderGemini,
			BaseURL:  "https://generativelanguage.googleapis.com/v1beta",
			ModelAliasMapping: map[string]string{
				"default":      "gemini-4-argon",
				"gemini-pro":   "gemini-3.1-pro-preview",
				"gemini-flash": "gemini-3.8-flash",
				"flash":        "gemini-3.8-flash",
				"flash-lite":   "gemini-3.5-flash-lite",
				"gemini-2.5":   "gemini-2.5-pro",
			},
		},
		types.APIProviderZAi: {
			Provider: types.APIProviderZAi,
			BaseURL:  "https://api.z.ai/api/paas/v4",
			ModelAliasMapping: map[string]string{
				"default": "glm-5.3",
				"glm-5":   "glm-5",
				"glm-5.3": "glm-5.3",
				"flash":   "glm-5.3-flash",
				"flashx":  "glm-5.3-flashx",
				"glm-4.7": "glm-4.7",
			},
		},
		types.APIProviderOpenRouter: {
			Provider: types.APIProviderOpenRouter,
			BaseURL:  "https://openrouter.ai/api/v1",
			ModelAliasMapping: map[string]string{
				"opus":      "anthropic/claude-opus-5.5",
				"sonnet":    "anthropic/claude-sonnet-5.5",
				"haiku":     "anthropic/claude-haiku-5.5",
				"gpt":       "openai/gpt-6.1-sol",
				"gemini":    "google/gemini-4-argon",
				"deepseek":  "deepseek/deepseek-v4-pro",
				"kimi":      "moonshotai/kimi-k3",
				"glm":       "z-ai/glm-5.3",
				"minimax":   "minimax/minimax-m3",
				"qwen":      "qwen/qwen3.8-max-0902",
				"nemotron":  "nvidia/nemotron-3-super-120b-a12b:free",
				"free":      "nvidia/nemotron-3-super-120b-a12b:free",
				"code-free": "cohere/north-mini-code:free",
			},
		},
		types.APIProviderMiniMax: {
			Provider: types.APIProviderMiniMax,
			// api.minimax.chat is not a real MiniMax domain (verified against
			// platform.minimax.io's own docs); api.minimax.io is the current
			// international host (api.minimaxi.com for mainland China). The
			// old /v1/text/chatcompletion_v2 path is also documented
			// deprecated in favor of the standard OpenAI-compatible
			// /v1/chat/completions, so BaseURL is now a real API root like
			// every other OpenAI-compatible provider here.
			BaseURL: "https://api.minimax.io/v1",
			ModelAliasMapping: map[string]string{
				"m3":        "MiniMax-M3",
				"default":   "MiniMax-M3",
				"m2.7":      "MiniMax-M2.7",
				"m2.7-fast": "MiniMax-M2.7-highspeed",
				"m2.5":      "MiniMax-M2.5",
				"m2.5-fast": "MiniMax-M2.5-highspeed",
				"m2.1":      "MiniMax-M2.1",
				"m2":        "MiniMax-M2",
			},
		},
		types.APIProviderMistral: {
			Provider: types.APIProviderMistral,
			BaseURL:  "https://api.mistral.ai/v1",
			ModelAliasMapping: map[string]string{
				"large":    "mistral-large-latest",
				"medium":   "mistral-medium-latest",
				"small":    "mistral-small-latest",
				"code":     "codestral-latest",
				"devstral": "devstral-latest",
			},
		},
		types.APIProviderKimi: {
			Provider: types.APIProviderKimi,
			// International endpoint. api.moonshot.cn is a separate China
			// mainland deployment with its own accounts/billing - keys from
			// one region 401 on the other's endpoint, so this must not be
			// guessed from the "kimi"/"moonshot" name alone.
			BaseURL: "https://api.moonshot.ai/v1",
			ModelAliasMapping: map[string]string{
				"k3":        "kimi-k3",
				"default":   "kimi-k3",
				"k2.7":      "kimi-k2.7-code",
				"k2.7code":  "kimi-k2.7-code",
				"code":      "kimi-k2.7-code",
				"k2.6":      "kimi-k2.6",
				"k2.7-fast": "kimi-k2.7-code-highspeed",
			},
		},
		types.APIProviderCodex: {
			Provider: types.APIProviderCodex,
			BaseURL:  "https://chatgpt.com/backend-api/codex",
			// gpt-5.2-codex/gpt-5.3-codex previously targeted here are
			// confirmed broken against a real ChatGPT-account session (see
			// the comment on this provider's catalog entry in registry.go)
			// - these aliases now point at the models that catalog confirms
			// actually work. The "5.2"/"5.3" shorthand aliases are removed
			// rather than silently repointed to a different version.
			ModelAliasMapping: map[string]string{
				"default":    "gpt-5.6-sol",
				"codex":      "gpt-5.6-sol",
				"terra":      "gpt-5.6-terra",
				"luna":       "gpt-5.6-luna",
				"mini":       "gpt-5.4-mini",
				"codex-mini": "gpt-5.4-mini",
				"5.5":        "gpt-5.5",
			},
		},
		types.APIProviderDeepSeek: {
			Provider: types.APIProviderDeepSeek,
			BaseURL:  "https://api.deepseek.com/v1",
			// deepseek-chat/deepseek-reasoner (targeted by these aliases
			// previously) are legacy V3.1-era model IDs; DeepSeek's own
			// pricing docs (api-docs.deepseek.com, checked 2026-08-28) list
			// only the V4 lineup below, with no mention of the old names.
			ModelAliasMapping: map[string]string{
				"default":  "deepseek-flash",
				"chat":     "deepseek-flash",
				"flash":    "deepseek-flash",
				"pro":      "deepseek-v4-pro",
				"reasoner": "deepseek-v4-pro",
				"vision":   "deepseek-flash",
				// Retired by DeepSeek but still accepted; point at the
				// current ID so catalogs and sessions converge on it.
				"deepseek-v4-flash":            "deepseek-flash",
				"deepseek-v4-flash-vision-exp": "deepseek-flash",
			},
		},
		types.APIProviderOpenCode: {
			Provider: types.APIProviderOpenCode,
			BaseURL:  "https://opencode.ai/zen/v1",
			ModelAliasMapping: map[string]string{
				"default": "claude-sonnet-5-5",
				"sonnet":  "claude-sonnet-5-5",
				"opus":    "claude-opus-5-5",
				"haiku":   "claude-haiku-5-5",
				"gpt":     "gpt-6.1-sol",
				"codex":   "gpt-5.3-codex",
				"glm":     "glm-5.3",
			},
		},
	}
}

func GetProviderConfig(provider types.APIProvider) *Config {
	config, ok := DefaultConfigs()[provider]
	if !ok {
		// A generic provider of the models.dev catalog has no default config of its own: its base URL comes from the embedded catalog.
		if catalogConfig, ok := ConfigForCatalogProvider(provider); ok {
			return catalogConfig
		}
		return nil
	}
	// Return an isolated copy so callers can inject keys or overrides safely.
	return cloneConfig(config)
}

func (c *Config) ResolveModel(model string) string {
	if c == nil || c.ModelAliasMapping == nil {
		return model
	}
	if mapped, ok := c.ModelAliasMapping[model]; ok {
		return mapped
	}
	return model
}

func cloneConfig(config *Config) *Config {
	if config == nil {
		return nil
	}

	cloned := *config
	if config.ModelAliasMapping != nil {
		cloned.ModelAliasMapping = make(map[string]string, len(config.ModelAliasMapping))
		for key, value := range config.ModelAliasMapping {
			cloned.ModelAliasMapping[key] = value
		}
	}
	if config.CustomHeaders != nil {
		cloned.CustomHeaders = make(map[string]string, len(config.CustomHeaders))
		for key, value := range config.CustomHeaders {
			cloned.CustomHeaders[key] = value
		}
	}
	if config.Routing != nil {
		routing := *config.Routing
		if config.Routing.FallbackModels != nil {
			routing.FallbackModels = append([]types.ModelIdentifier(nil), config.Routing.FallbackModels...)
		}
		if config.Routing.FallbackProviders != nil {
			routing.FallbackProviders = append([]types.APIProvider(nil), config.Routing.FallbackProviders...)
		}
		if config.Routing.CircuitBreaker != nil {
			// Clone the circuit breaker config and create a new circuit breaker instance
			breaker := NewCircuitBreakerWithConfig(config.Routing.CircuitBreaker)
			routing.CircuitBreaker = &CircuitBreakerConfig{
				MaxFailures:      config.Routing.CircuitBreaker.MaxFailures,
				CallTimeout:      config.Routing.CircuitBreaker.CallTimeout,
				ResetTimeout:     config.Routing.CircuitBreaker.ResetTimeout,
				HalfOpenMaxCalls: config.Routing.CircuitBreaker.HalfOpenMaxCalls,
				ReadyToTrip:      config.Routing.CircuitBreaker.ReadyToTrip,
				OnStateChange:    config.Routing.CircuitBreaker.OnStateChange,
				CircuitBreaker:   breaker,
			}
		}
		cloned.Routing = &routing
	}

	return &cloned
}

func (c *Config) GetBaseURL() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return defaultBaseURLs[c.Provider]
}

func (c *Config) GetEndpoint(model string) string {
	resolvedModel := c.ResolveModel(model)

	switch c.Provider {
	case types.APIProviderAnthropic:
		return c.GetBaseURL() + "/v1/messages"

	case types.APIProviderOpenAI, types.APIProviderOpenRouter, types.APIProviderZAi, types.APIProviderMistral, types.APIProviderDeepSeek, types.APIProviderOpenCode, types.APIProviderKimi, types.APIProviderMiniMax:
		// MiniMax used to need a special case here: its old BaseURL
		// (api.minimax.chat/v1/text/chatcompletion_v2) was already a full
		// endpoint path, so appending "/chat/completions" produced a 404.
		// That domain and path are gone now — MiniMax's BaseURL is a real
		// API root (api.minimax.io/v1) like every other provider in this
		// case, using the current, non-deprecated /v1/chat/completions.
		return c.GetBaseURL() + "/chat/completions"

	case types.APIProviderGemini:
		return c.GetBaseURL() + "/models/" + resolvedModel + ":generateContent"

	case types.APIProviderOllama:
		return c.GetBaseURL() + "/api/chat"

	case types.APIProviderCodex:
		return c.GetBaseURL() + "/responses"

	default:
		// A generic provider of the models.dev catalog speaks the OpenAI chat completions protocol, whose base URL is a root
		// like https://api.example.com/v1.
		if IsCatalogProvider(c.Provider) {
			return c.GetBaseURL() + "/chat/completions"
		}
		return c.GetBaseURL() + "/v1/messages"
	}
}

func (c *Config) IsSupportedModel(model string) bool {
	return true
}

var defaultBaseURLs = map[types.APIProvider]string{
	types.APIProviderAnthropic:  "https://api.anthropic.com",
	types.APIProviderOpenAI:     "https://api.openai.com/v1",
	types.APIProviderOllama:     "http://localhost:11434",
	types.APIProviderGemini:     "https://generativelanguage.googleapis.com/v1beta",
	types.APIProviderZAi:        "https://api.z.ai/api/paas/v4",
	types.APIProviderOpenRouter: "https://openrouter.ai/api/v1",
	types.APIProviderMiniMax:    "https://api.minimax.io/v1",
	types.APIProviderMistral:    "https://api.mistral.ai/v1",
	types.APIProviderKimi:       "https://api.moonshot.ai/v1",
	types.APIProviderCodex:      "https://chatgpt.com/backend-api/codex",
	types.APIProviderDeepSeek:   "https://api.deepseek.com/v1",
	types.APIProviderOpenCode:   "https://opencode.ai/zen/v1",
}

func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func ValidateProviderConfig(config *Config) error {
	if config == nil {
		return fmt.Errorf("provider config is nil")
	}

	switch config.Provider {
	case types.APIProviderGemini:
		if config.APIKey == "" {
			return fmt.Errorf("Gemini requires GOOGLE_GEMINI_API_KEY to be set")
		}

	case types.APIProviderZAi:
		if config.APIKey == "" {
			return fmt.Errorf("Z.ai requires Z_AI_API_KEY to be set")
		}

	case types.APIProviderMiniMax:
		if config.APIKey == "" {
			return fmt.Errorf("MiniMax requires MINIMAX_API_KEY to be set")
		}

	case types.APIProviderOpenRouter:
		if config.APIKey == "" {
			return fmt.Errorf("OpenRouter requires OPENROUTER_API_KEY to be set")
		}

	case types.APIProviderMistral:
		if config.APIKey == "" {
			return fmt.Errorf("Mistral requires MISTRAL_API_KEY to be set")
		}

	case types.APIProviderKimi:
		if config.APIKey == "" {
			return fmt.Errorf("Kimi requires KIMI_API_KEY to be set")
		}

	case types.APIProviderDeepSeek:
		if config.APIKey == "" {
			return fmt.Errorf("DeepSeek requires DEEPSEEK_API_KEY to be set")
		}

	case types.APIProviderOpenCode:
		if config.APIKey == "" {
			return fmt.Errorf("OpenCode requires OPENCODE_API_KEY to be set")
		}
	}

	return nil
}
