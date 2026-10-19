package providers

import (
	"strings"

	"github.com/KPO-Tech/seshat-runtime/internal/model"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

type ProviderInfo struct {
	Name         string
	DisplayName  string
	Description  string
	AuthType     string
	AuthTypes    []string
	SupportsCVMM bool
	SupportsPC   bool
	Models       []ModelInfo
}

// ModelInfo describes a single model offered by a provider.
// Capabilities contains fine-grained per-model feature flags.
// The Pricing field is a legacy string; structured pricing lives in model.Metadata.
type ModelInfo struct {
	Identifier         string
	ContextWindow      int
	MaxOutput          int
	DefaultTemperature float64
	SupportsPC         bool
	Pricing            string
	Description        string
	Capabilities       model.Capabilities
}

// curatedProvidersInfo is the part of the provider catalog written by hand: the providers, how they authenticate, and the models whose
// description or limits we set ourselves. AllProvidersInfo extends it with the models of the models.dev catalog.
func curatedProvidersInfo() map[types.APIProvider]ProviderInfo {
	return map[types.APIProvider]ProviderInfo{
		types.APIProviderAnthropic: {
			Name:         "anthropic",
			DisplayName:  "Anthropic",
			Description:  "Direct API - Claude models",
			AuthType:     "api_key",
			AuthTypes:    []string{"api_key"},
			SupportsCVMM: true,
			SupportsPC:   true,
		},
		types.APIProviderOpenAI: {
			Name:         "openai",
			DisplayName:  "OpenAI",
			Description:  "Direct API - GPT models",
			AuthType:     "api_key",
			AuthTypes:    []string{"api_key"},
			SupportsCVMM: true,
			SupportsPC:   true,
		},
		types.APIProviderCodex: {
			Name:         "codex",
			DisplayName:  "Codex",
			Description:  "ChatGPT Pro subscription — Responses API via chatgpt.com",
			AuthType:     "oauth",
			AuthTypes:    []string{"oauth"},
			SupportsCVMM: false,
			SupportsPC:   false,
			// gpt-5.3-codex/gpt-5.2-codex/gpt-5.4-codex all confirmed broken by
			// a real ChatGPT-account session ("The 'gpt-5.X-codex' model is
			// not supported when using Codex with a ChatGPT account"). The
			// three below are confirmed working via that same live session
			// (both from the account's own VS Code Codex picker and a direct
			// API smoke test, 2026-08-28).
			//
			// gpt-5.6-terra, gpt-5.6-luna and gpt-5.4 were confirmed working in
			// that same session. Their context/output limits are not published
			// for the ChatGPT-account route, so terra/luna reuse the gpt-5.6-sol
			// numbers (a conservative underestimate of the API limits).
			Models: []ModelInfo{
				{Identifier: "gpt-5.6-sol", ContextWindow: 200000, MaxOutput: 100000, DefaultTemperature: 1.0, Description: "GPT-5.6-Sol — Flagship Codex model via ChatGPT Pro subscription.",
					Capabilities: model.Capabilities{FunctionCalling: true, Streaming: true}},
				{Identifier: "gpt-5.6-terra", ContextWindow: 200000, MaxOutput: 100000, DefaultTemperature: 1.0, Description: "GPT-5.6-Terra — Codex model via ChatGPT Pro subscription.",
					Capabilities: model.Capabilities{FunctionCalling: true, Streaming: true}},
				{Identifier: "gpt-5.5", ContextWindow: 200000, MaxOutput: 100000, DefaultTemperature: 1.0, Description: "GPT-5.5 — Codex model available via ChatGPT Pro subscription.",
					Capabilities: model.Capabilities{FunctionCalling: true, Streaming: true}},
				{Identifier: "gpt-5.4", ContextWindow: 272000, MaxOutput: 32768, DefaultTemperature: 1.0, Description: "GPT-5.4 — Codex model via ChatGPT Pro subscription.",
					Capabilities: model.Capabilities{Vision: true, FunctionCalling: true, Streaming: true}},
				{Identifier: "gpt-5.6-luna", ContextWindow: 200000, MaxOutput: 100000, DefaultTemperature: 1.0, Description: "GPT-5.6-Luna — Fast, cost-efficient model via Codex.",
					Capabilities: model.Capabilities{Vision: true, FunctionCalling: true, Streaming: true}},
				{Identifier: "gpt-5.4-mini", ContextWindow: 272000, MaxOutput: 16384, DefaultTemperature: 1.0, Description: "GPT-5.4-Mini — Fast, cost-efficient model via Codex (ChatGPT Pro subscription).",
					Capabilities: model.Capabilities{Vision: true, FunctionCalling: true, Streaming: true}},
			},
		},
		types.APIProviderMistral: {
			Name:         "mistral",
			DisplayName:  "Mistral AI",
			Description:  "Direct API - Mistral models (OpenAI-compatible)",
			AuthType:     "api_key",
			AuthTypes:    []string{"api_key"},
			SupportsCVMM: true,
			SupportsPC:   false,
		},
		types.APIProviderGemini: {
			Name:         "gemini",
			DisplayName:  "Google Gemini",
			Description:  "Google's Gemini models",
			AuthType:     "api_key",
			AuthTypes:    []string{"api_key"},
			SupportsCVMM: true,
			SupportsPC:   false,
		},
		types.APIProviderMiniMax: {
			Name:         "minimax",
			DisplayName:  "MiniMax",
			Description:  "Chinese AI platform (OpenAI-compatible chat completions)",
			AuthType:     "api_key",
			AuthTypes:    []string{"api_key"},
			SupportsCVMM: true,
			SupportsPC:   true,
		},
		types.APIProviderOpenRouter: {
			Name:         "openrouter",
			DisplayName:  "OpenRouter",
			Description:  "Unified API for 300+ models",
			AuthType:     "api_key",
			AuthTypes:    []string{"api_key"},
			SupportsCVMM: true,
			// OpenRouter does support prompt caching for some underlying
			// models (e.g. Anthropic's cache_control), but only when the
			// request is sent in the native Anthropic wire shape — this
			// codebase routes OpenRouter through the OpenAI-compatible
			// adapter (chat/completions body), which has no cache_control
			// field at all, so no request this codebase sends actually gets
			// cached today. false reflects what's actually implemented,
			// not what OpenRouter could theoretically support.
			SupportsPC: false,
		},
		types.APIProviderDeepSeek: {
			Name:         "deepseek",
			DisplayName:  "DeepSeek",
			Description:  "Direct API - DeepSeek models (OpenAI-compatible)",
			AuthType:     "api_key",
			AuthTypes:    []string{"api_key"},
			SupportsCVMM: true,
			SupportsPC:   false,
			// deepseek-chat/deepseek-reasoner/deepseek-coder-v2 (the
			// previous catalog here) are legacy V3.1-era model IDs;
			// DeepSeek's own pricing docs (api-docs.deepseek.com, checked
			// 2026-08-28) list only the V4 lineup below, with no mention of
			// the old names at all.
		},
		types.APIProviderOpenCode: {
			Name:         "opencode",
			DisplayName:  "OpenCode Zen",
			Description:  "Curated model gateway (OpenAI-compatible)",
			AuthType:     "api_key",
			AuthTypes:    []string{"api_key"},
			SupportsCVMM: true,
			SupportsPC:   false,
		},
		types.APIProviderZAi: {
			Name:         "z-ai",
			DisplayName:  "Z.ai",
			Description:  "GLM models (Chinese AI, OpenAI-compatible)",
			AuthType:     "api_key",
			AuthTypes:    []string{"api_key"},
			SupportsCVMM: true,
			SupportsPC:   true,
		},
		// Ollama: no static model list — models are discovered dynamically from the
		// local Ollama API (/api/tags). The frontend shows an empty list until the
		// user triggers a sync, which populates the DB with locally installed models.
		types.APIProviderOllama: {
			Name:         "ollama",
			DisplayName:  "Ollama",
			Description:  "Local LLM inference — auto-detected from localhost:11434",
			AuthType:     "none",
			AuthTypes:    []string{"none"},
			SupportsCVMM: false,
			SupportsPC:   false,
			Models:       nil,
		},
		types.APIProviderKimi: {
			Name:         "kimi",
			DisplayName:  "Kimi (Moonshot AI)",
			Description:  "Direct API - Kimi/Moonshot models (OpenAI-compatible)",
			AuthType:     "api_key",
			AuthTypes:    []string{"api_key"},
			SupportsCVMM: true,
			// Moonshot caches automatically server-side (same-prefix requests
			// hit the cache with no cache_control marker or other client-side
			// action required), unlike Anthropic's opt-in mechanism — so this
			// is accurate regardless of what this codebase's own request
			// building does. See
			// https://platform.moonshot.ai/docs/guide/use-context-caching-feature-of-kimi-api.
			SupportsPC: true,
			// The moonshot-v1-* generation and kimi-k2.5 are being sunset by
			// Moonshot (new registrations already blocked, full platform
			// sunset ~Aug 31 2026) in favor of the kimi-k2.6/k2.7/k3 lineup -
			// see https://platform.kimi.ai/docs/models.
		},
	}
}

// OllamaModelLookup returns a name → ModelInfo map of context-window hints for
// popular Ollama models. It is used as a best-effort fallback when /api/show
// does not return context length data. The authoritative dynamic model list
// comes from providers.FetchModels (internal/providers/fetch.go).
func OllamaModelLookup() map[string]ModelInfo {
	return map[string]ModelInfo{
		"qwen2.5-coder:32b": {Identifier: "qwen2.5-coder:32b", ContextWindow: 131072, MaxOutput: 8192, DefaultTemperature: 0.7},
		"qwen2.5-coder:7b":  {Identifier: "qwen2.5-coder:7b", ContextWindow: 32768, MaxOutput: 8192, DefaultTemperature: 0.7},
		"llama3.2":          {Identifier: "llama3.2", ContextWindow: 131072, MaxOutput: 8192, DefaultTemperature: 0.8},
		"llama3.1:8b":       {Identifier: "llama3.1:8b", ContextWindow: 131072, MaxOutput: 8192, DefaultTemperature: 0.8},
		"mistral:7b":        {Identifier: "mistral:7b", ContextWindow: 32768, MaxOutput: 4096, DefaultTemperature: 0.7},
		"deepseek-r1:7b":    {Identifier: "deepseek-r1:7b", ContextWindow: 131072, MaxOutput: 8192, DefaultTemperature: 0.6},
		"phi4":              {Identifier: "phi4", ContextWindow: 16384, MaxOutput: 4096, DefaultTemperature: 0.7},
		"gemma3:12b":        {Identifier: "gemma3:12b", ContextWindow: 131072, MaxOutput: 8192, DefaultTemperature: 1.0},
	}
}

func ResolveProviderFromString(s string) types.APIProvider {
	switch strings.ToLower(s) {
	case "anthropic", "claude":
		return types.APIProviderAnthropic
	case "openai", "gpt":
		return types.APIProviderOpenAI
	case "ollama":
		return types.APIProviderOllama
	case "gemini", "google-ai":
		return types.APIProviderGemini
	case "z-ai", "z.ai", "zai":
		return types.APIProviderZAi
	case "openrouter":
		return types.APIProviderOpenRouter
	case "minimax":
		return types.APIProviderMiniMax
	case "mistral", "mistral-ai", "mistralai":
		return types.APIProviderMistral
	case "codex":
		return types.APIProviderCodex
	case "deepseek", "deep-seek":
		return types.APIProviderDeepSeek
	case "opencode", "opencode-zen", "opencode_zen":
		return types.APIProviderOpenCode
	case "kimi", "moonshot", "moonshot-ai", "moonshotai":
		return types.APIProviderKimi
	// Cloudflare Workers AI used to be a built-in provider ("workers-ai"); it is a generic provider of the catalog now.
	case "workers-ai", "workers_ai", "cloudflare":
		return types.APIProvider("cloudflare-workers-ai")
	default:
		// A generic provider of the catalog resolves to itself; any other unknown name keeps the historical default.
		if p := types.APIProvider(strings.ToLower(s)); IsCatalogProvider(p) {
			return p
		}
		return types.APIProviderAnthropic
	}
}

// GetProviderInfo describes a provider: a built-in one, or a generic provider of the models.dev catalog.
func GetProviderInfo(provider types.APIProvider) (ProviderInfo, bool) {
	if info, ok := AllProvidersInfo()[provider]; ok {
		return info, true
	}
	return catalogInfoFor(provider)
}

func GetModelInfo(provider types.APIProvider, model string) (ModelInfo, bool) {
	info, ok := GetProviderInfo(provider)
	if !ok {
		return ModelInfo{}, false
	}
	for _, m := range info.Models {
		if m.Identifier == model {
			return m, true
		}
	}
	return ModelInfo{}, false
}

func DefaultModelIdentifier(provider types.APIProvider) (types.ModelIdentifier, bool) {
	info, ok := GetProviderInfo(provider)
	if !ok || len(info.Models) == 0 {
		return types.ModelIdentifier{}, false
	}
	return types.ModelIdentifier{
		Provider: provider,
		Model:    info.Models[0].Identifier,
	}, true
}
