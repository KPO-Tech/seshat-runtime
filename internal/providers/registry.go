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
			Models: []ModelInfo{
				{Identifier: "claude-sonnet-5-5", ContextWindow: 1000000, MaxOutput: 128000, DefaultTemperature: 1.0, SupportsPC: true, Description: "Claude Sonnet 5.5 — Best combination of speed and intelligence. Default for agentic coding and knowledge work.",
					Capabilities: model.Capabilities{Vision: true, FunctionCalling: true, Streaming: true, PromptCaching: true, StructuredOutput: true}},
				{Identifier: "claude-opus-5-5", ContextWindow: 1000000, MaxOutput: 128000, DefaultTemperature: 1.0, SupportsPC: true, Description: "Claude Opus 5.5 — Long-running agentic coding and knowledge work.",
					Capabilities: model.Capabilities{Vision: true, FunctionCalling: true, Streaming: true, PromptCaching: true, StructuredOutput: true}},
				{Identifier: "claude-fable-5-1", ContextWindow: 1000000, MaxOutput: 128000, DefaultTemperature: 1.0, SupportsPC: true, Description: "Claude Fable 5.1 — Most capable: demanding reasoning and long-horizon agentic work.",
					Capabilities: model.Capabilities{Vision: true, FunctionCalling: true, Streaming: true, PromptCaching: true, StructuredOutput: true}},
				{Identifier: "claude-sonnet-5", ContextWindow: 1000000, MaxOutput: 128000, DefaultTemperature: 1.0, SupportsPC: true, Description: "Claude Sonnet 5 — Previous-generation Sonnet.",
					Capabilities: model.Capabilities{Vision: true, FunctionCalling: true, Streaming: true, PromptCaching: true, StructuredOutput: true}},
				{Identifier: "claude-opus-5", ContextWindow: 1000000, MaxOutput: 128000, DefaultTemperature: 1.0, SupportsPC: true, Description: "Claude Opus 5 — Previous-generation Opus.",
					Capabilities: model.Capabilities{Vision: true, FunctionCalling: true, Streaming: true, PromptCaching: true, StructuredOutput: true}},
				{Identifier: "claude-opus-4-8", ContextWindow: 1000000, MaxOutput: 128000, DefaultTemperature: 1.0, SupportsPC: true, Description: "Claude Opus 4.8 — Legacy Opus, still available.",
					Capabilities: model.Capabilities{Vision: true, FunctionCalling: true, Streaming: true, PromptCaching: true, StructuredOutput: true}},
				{Identifier: "claude-sonnet-4-6", ContextWindow: 1000000, MaxOutput: 128000, DefaultTemperature: 1.0, SupportsPC: true, Description: "Claude Sonnet 4.6 — Legacy Sonnet, still available.",
					Capabilities: model.Capabilities{Vision: true, FunctionCalling: true, Streaming: true, PromptCaching: true, StructuredOutput: true}},
				{Identifier: "claude-fable-5", ContextWindow: 1000000, MaxOutput: 128000, DefaultTemperature: 1.0, SupportsPC: true, Description: "Claude Fable 5 — 1M context, reasoning, image input.",
					Capabilities: model.Capabilities{Vision: true, FunctionCalling: true, Streaming: true, PromptCaching: true, StructuredOutput: true}},
				{Identifier: "claude-opus-4-7", ContextWindow: 1000000, MaxOutput: 128000, DefaultTemperature: 1.0, SupportsPC: true, Description: "Claude Opus 4.7 — 1M context, reasoning, image input.",
					Capabilities: model.Capabilities{Vision: true, FunctionCalling: true, Streaming: true, PromptCaching: true, StructuredOutput: true}},
				{Identifier: "claude-opus-4-6", ContextWindow: 1000000, MaxOutput: 128000, DefaultTemperature: 1.0, SupportsPC: true, Description: "Claude Opus 4.6 — 1M context, reasoning, image input.",
					Capabilities: model.Capabilities{Vision: true, FunctionCalling: true, Streaming: true, PromptCaching: true, StructuredOutput: true}},
				{Identifier: "claude-opus-4-5", ContextWindow: 200000, MaxOutput: 64000, DefaultTemperature: 1.0, SupportsPC: true, Description: "Claude Opus 4.5 — 200k context, reasoning, image input.",
					Capabilities: model.Capabilities{Vision: true, FunctionCalling: true, Streaming: true, PromptCaching: true, StructuredOutput: true}},
				{Identifier: "claude-haiku-4-5", ContextWindow: 200000, MaxOutput: 64000, DefaultTemperature: 1.0, SupportsPC: true, Description: "Claude Haiku 4.5 — 200k context, reasoning, image input.",
					Capabilities: model.Capabilities{Vision: true, FunctionCalling: true, Streaming: true, PromptCaching: true, StructuredOutput: true}},
				{Identifier: "claude-sonnet-4-5", ContextWindow: 200000, MaxOutput: 64000, DefaultTemperature: 1.0, SupportsPC: true, Description: "Claude Sonnet 4.5 — 200k context, reasoning, image input.",
					Capabilities: model.Capabilities{Vision: true, FunctionCalling: true, Streaming: true, PromptCaching: true, StructuredOutput: true}},
				{Identifier: "claude-haiku-5-5", ContextWindow: 1000000, MaxOutput: 128000, DefaultTemperature: 1.0, SupportsPC: true, Description: "Claude Haiku 5.5 — Fastest Claude model for high-volume, latency-sensitive tasks.",
					Capabilities: model.Capabilities{Vision: true, FunctionCalling: true, Streaming: true, PromptCaching: true, StructuredOutput: true}},
			},
		},
		types.APIProviderOpenAI: {
			Name:         "openai",
			DisplayName:  "OpenAI",
			Description:  "Direct API - GPT models",
			AuthType:     "api_key",
			AuthTypes:    []string{"api_key"},
			SupportsCVMM: true,
			SupportsPC:   true,
			Models: []ModelInfo{
				{Identifier: "gpt-6.1-sol", ContextWindow: 1050000, MaxOutput: 128000, DefaultTemperature: 1.0, SupportsPC: true, Description: "GPT-6.1 Sol — Current flagship for coding, research, and agentic work.",
					Capabilities: model.Capabilities{Vision: true, FunctionCalling: true, Streaming: true, PromptCaching: true, StructuredOutput: true}},
				{Identifier: "gpt-6-astra", ContextWindow: 1050000, MaxOutput: 128000, DefaultTemperature: 1.0, SupportsPC: true, Description: "GPT-6 Astra — Highest-capability GPT-6 tier for the hardest problems.",
					Capabilities: model.Capabilities{Vision: true, FunctionCalling: true, Streaming: true, PromptCaching: true, StructuredOutput: true}},
				{Identifier: "gpt-6-sol", ContextWindow: 1050000, MaxOutput: 128000, DefaultTemperature: 1.0, SupportsPC: true, Description: "GPT-6 Sol — Balanced GPT-6 model.",
					Capabilities: model.Capabilities{Vision: true, FunctionCalling: true, Streaming: true, PromptCaching: true, StructuredOutput: true}},
				{Identifier: "gpt-5.6-terra", ContextWindow: 1050000, MaxOutput: 128000, DefaultTemperature: 1.0, SupportsPC: true, Description: "GPT-5.6 Terra — Previous-generation mid-tier model.",
					Capabilities: model.Capabilities{Vision: true, FunctionCalling: true, Streaming: true, PromptCaching: true, StructuredOutput: true}},
				{Identifier: "gpt-5.5", ContextWindow: 272000, MaxOutput: 32768, DefaultTemperature: 1.0, SupportsPC: true, Description: "GPT-5.5 — Previous-generation frontier model.",
					Capabilities: model.Capabilities{Vision: true, FunctionCalling: true, Streaming: true, PromptCaching: true, StructuredOutput: true}},
				{Identifier: "gpt-5.6", ContextWindow: 1050000, MaxOutput: 128000, DefaultTemperature: 1.0, SupportsPC: true, Description: "GPT-5.6 — 1.1M context, reasoning, image input.",
					Capabilities: model.Capabilities{Vision: true, FunctionCalling: true, Streaming: true, PromptCaching: true, StructuredOutput: true}},
				{Identifier: "gpt-5.6-sol", ContextWindow: 1050000, MaxOutput: 128000, DefaultTemperature: 1.0, SupportsPC: true, Description: "GPT-5.6 Sol — 1.1M context, reasoning, image input.",
					Capabilities: model.Capabilities{Vision: true, FunctionCalling: true, Streaming: true, PromptCaching: true, StructuredOutput: true}},
				{Identifier: "gpt-5.6-luna", ContextWindow: 1050000, MaxOutput: 128000, DefaultTemperature: 1.0, SupportsPC: true, Description: "GPT-5.6 Luna — 1.1M context, reasoning, image input.",
					Capabilities: model.Capabilities{Vision: true, FunctionCalling: true, Streaming: true, PromptCaching: true, StructuredOutput: true}},
				{Identifier: "gpt-5.4", ContextWindow: 1050000, MaxOutput: 128000, DefaultTemperature: 1.0, SupportsPC: true, Description: "GPT-5.4 — 1.1M context, reasoning, image input.",
					Capabilities: model.Capabilities{Vision: true, FunctionCalling: true, Streaming: true, PromptCaching: true, StructuredOutput: true}},
				{Identifier: "gpt-5.4-mini", ContextWindow: 400000, MaxOutput: 128000, DefaultTemperature: 1.0, SupportsPC: true, Description: "GPT-5.4 mini — 400k context, reasoning, image input.",
					Capabilities: model.Capabilities{Vision: true, FunctionCalling: true, Streaming: true, PromptCaching: true, StructuredOutput: true}},
				{Identifier: "gpt-5.4-nano", ContextWindow: 400000, MaxOutput: 128000, DefaultTemperature: 1.0, SupportsPC: true, Description: "GPT-5.4 nano — 400k context, reasoning, image input.",
					Capabilities: model.Capabilities{Vision: true, FunctionCalling: true, Streaming: true, PromptCaching: true, StructuredOutput: true}},
				{Identifier: "gpt-6-luna", ContextWindow: 1050000, MaxOutput: 128000, DefaultTemperature: 1.0, SupportsPC: true, Description: "GPT-6 Luna — Small, fast, and cost-efficient model.",
					Capabilities: model.Capabilities{Vision: true, FunctionCalling: true, Streaming: true, PromptCaching: true, StructuredOutput: true}},
			},
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
			Models: []ModelInfo{
				{Identifier: "mistral-large-latest", ContextWindow: 1048576, MaxOutput: 262144, DefaultTemperature: 0.7, Description: "Mistral Large 4 — Top-tier reasoning model for complex multi-step tasks.",
					Capabilities: model.Capabilities{Vision: true, FunctionCalling: true, Streaming: true}},
				{Identifier: "mistral-medium-latest", ContextWindow: 262144, MaxOutput: 209715, DefaultTemperature: 0.7, Description: "Mistral Medium 3.5 — Balanced capability and cost, vision-capable.",
					Capabilities: model.Capabilities{Vision: true, FunctionCalling: true, Streaming: true}},
				{Identifier: "devstral-latest", ContextWindow: 262144, MaxOutput: 209715, DefaultTemperature: 0.7, Description: "Devstral — Agentic coding model.",
					Capabilities: model.Capabilities{FunctionCalling: true, Streaming: true}},
				{Identifier: "codestral-latest", ContextWindow: 256000, MaxOutput: 204800, DefaultTemperature: 0.7, Description: "Codestral — Code generation and completion.",
					Capabilities: model.Capabilities{FunctionCalling: true, Streaming: true}},
				{Identifier: "ministral-14b-latest", ContextWindow: 262144, MaxOutput: 209715, DefaultTemperature: 0.7, Description: "Ministral 14B — Compact, capable open-weight model.",
					Capabilities: model.Capabilities{Vision: true, FunctionCalling: true, Streaming: true}},
				{Identifier: "mistral-small-latest", ContextWindow: 262144, MaxOutput: 209715, DefaultTemperature: 0.7, Description: "Mistral Small 4 — Efficient and capable at low cost and latency.",
					Capabilities: model.Capabilities{Vision: true, FunctionCalling: true, Streaming: true}},
			},
		},
		types.APIProviderGemini: {
			Name:         "gemini",
			DisplayName:  "Google Gemini",
			Description:  "Google's Gemini models",
			AuthType:     "api_key",
			AuthTypes:    []string{"api_key"},
			SupportsCVMM: true,
			SupportsPC:   false,
			Models: []ModelInfo{
				{Identifier: "gemini-4-argon", ContextWindow: 1048576, MaxOutput: 65536, DefaultTemperature: 1.0, Description: "Gemini 4 Argon — Google's most capable model. 1M context, multimodal.",
					Capabilities: model.Capabilities{Vision: true, Audio: true, FunctionCalling: true, Streaming: true}},
				{Identifier: "gemini-3.8-flash", ContextWindow: 1048576, MaxOutput: 65536, DefaultTemperature: 1.0, Description: "Gemini 3.8 Flash — Fast, capable multimodal model.",
					Capabilities: model.Capabilities{Vision: true, Audio: true, FunctionCalling: true, Streaming: true}},
				{Identifier: "gemini-3.6-flash", ContextWindow: 1048576, MaxOutput: 65536, DefaultTemperature: 1.0, Description: "Gemini 3.6 Flash — Previous-generation Flash.",
					Capabilities: model.Capabilities{Vision: true, Audio: true, FunctionCalling: true, Streaming: true}},
				{Identifier: "gemini-3.1-pro-preview", ContextWindow: 1048576, MaxOutput: 65536, DefaultTemperature: 1.0, Description: "Gemini 3.1 Pro (preview) — Pro-tier reasoning.",
					Capabilities: model.Capabilities{Vision: true, Audio: true, FunctionCalling: true, Streaming: true}},
				{Identifier: "gemini-2.5-pro", ContextWindow: 1048576, MaxOutput: 65536, DefaultTemperature: 1.0, Description: "Gemini 2.5 Pro — Stable Pro-tier model.",
					Capabilities: model.Capabilities{Vision: true, Audio: true, FunctionCalling: true, Streaming: true}},
				{Identifier: "gemini-2.5-flash", ContextWindow: 1048576, MaxOutput: 65536, DefaultTemperature: 1.0, Description: "Gemini 2.5 Flash — Stable, fast model.",
					Capabilities: model.Capabilities{Vision: true, Audio: true, FunctionCalling: true, Streaming: true}},
				{Identifier: "gemini-3.7-flash", ContextWindow: 1048576, MaxOutput: 65536, DefaultTemperature: 1.0, Description: "Gemini 3.7 Flash — 1.0M context, reasoning, image input.",
					Capabilities: model.Capabilities{Vision: true, Audio: true, FunctionCalling: true, Streaming: true}},
				{Identifier: "gemini-3.5-flash", ContextWindow: 1048576, MaxOutput: 65536, DefaultTemperature: 1.0, Description: "Gemini 3.5 Flash — 1.0M context, reasoning, image input.",
					Capabilities: model.Capabilities{Vision: true, Audio: true, FunctionCalling: true, Streaming: true}},
				{Identifier: "gemini-3.1-flash-lite", ContextWindow: 1048576, MaxOutput: 65536, DefaultTemperature: 1.0, Description: "Gemini 3.1 Flash Lite — 1.0M context, reasoning, image input.",
					Capabilities: model.Capabilities{Vision: true, Audio: true, FunctionCalling: true, Streaming: true}},
				{Identifier: "gemini-flash-latest", ContextWindow: 1048576, MaxOutput: 65536, DefaultTemperature: 1.0, Description: "Gemini Flash Latest — 1.0M context, reasoning, image input.",
					Capabilities: model.Capabilities{Vision: true, Audio: true, FunctionCalling: true, Streaming: true}},
				{Identifier: "gemini-flash-lite-latest", ContextWindow: 1048576, MaxOutput: 65536, DefaultTemperature: 1.0, Description: "Gemini Flash-Lite Latest — 1.0M context, reasoning, image input.",
					Capabilities: model.Capabilities{Vision: true, Audio: true, FunctionCalling: true, Streaming: true}},
				{Identifier: "gemini-3.5-flash-lite", ContextWindow: 1048576, MaxOutput: 65536, DefaultTemperature: 1.0, Description: "Gemini 3.5 Flash-Lite — Cheapest and fastest for high-volume tasks.",
					Capabilities: model.Capabilities{Vision: true, Audio: true, FunctionCalling: true, Streaming: true}},
			},
		},
		types.APIProviderMiniMax: {
			Name:         "minimax",
			DisplayName:  "MiniMax",
			Description:  "Chinese AI platform (OpenAI-compatible chat completions)",
			AuthType:     "api_key",
			AuthTypes:    []string{"api_key"},
			SupportsCVMM: true,
			SupportsPC:   true,
			Models: []ModelInfo{
				{Identifier: "MiniMax-M3", ContextWindow: 1048576, MaxOutput: 512000, DefaultTemperature: 1.0, SupportsPC: true, Description: "MiniMax M3 — Latest flagship. 1M context, multimodal input, long-horizon agentic work.",
					Capabilities: model.Capabilities{Vision: true, FunctionCalling: true, Streaming: true, PromptCaching: true}},
				{Identifier: "MiniMax-M2.7", ContextWindow: 204800, MaxOutput: 128000, DefaultTemperature: 1.0, SupportsPC: true, Description: "MiniMax M2.7 — Strong long-horizon agentic model.",
					Capabilities: model.Capabilities{Vision: true, FunctionCalling: true, Streaming: true, PromptCaching: true}},
				{Identifier: "MiniMax-M2.5", ContextWindow: 204800, MaxOutput: 128000, DefaultTemperature: 1.0, SupportsPC: true, Description: "MiniMax M2.5 — Complex multi-step tasks, coding and reasoning.",
					Capabilities: model.Capabilities{Vision: true, FunctionCalling: true, Streaming: true, PromptCaching: true}},
				{Identifier: "MiniMax-M2.7-highspeed", ContextWindow: 204800, MaxOutput: 131072, DefaultTemperature: 1.0, SupportsPC: true, Description: "MiniMax-M2.7-highspeed — 205k context, reasoning.",
					Capabilities: model.Capabilities{FunctionCalling: true, Streaming: true, PromptCaching: true}},
				{Identifier: "MiniMax-M2.5-highspeed", ContextWindow: 204800, MaxOutput: 131072, DefaultTemperature: 1.0, SupportsPC: true, Description: "MiniMax-M2.5-highspeed — 205k context, reasoning.",
					Capabilities: model.Capabilities{FunctionCalling: true, Streaming: true, PromptCaching: true}},
				{Identifier: "MiniMax-M2", ContextWindow: 204800, MaxOutput: 131072, DefaultTemperature: 1.0, SupportsPC: true, Description: "MiniMax-M2 — 205k context, reasoning.",
					Capabilities: model.Capabilities{FunctionCalling: true, Streaming: true}},
				{Identifier: "MiniMax-M2.1", ContextWindow: 204800, MaxOutput: 128000, DefaultTemperature: 1.0, Description: "MiniMax M2.1 — Multilingual model for coding and analysis.",
					Capabilities: model.Capabilities{Vision: true, FunctionCalling: true, Streaming: true}},
			},
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
			Models: []ModelInfo{
				{Identifier: "anthropic/claude-sonnet-5.5", ContextWindow: 1000000, MaxOutput: 128000, DefaultTemperature: 1.0, Description: "Claude Sonnet 5.5 via OpenRouter.",
					Capabilities: model.Capabilities{Vision: true, FunctionCalling: true, Streaming: true}},
				{Identifier: "anthropic/claude-opus-5.5", ContextWindow: 1000000, MaxOutput: 128000, DefaultTemperature: 1.0, Description: "Claude Opus 5.5 via OpenRouter.",
					Capabilities: model.Capabilities{Vision: true, FunctionCalling: true, Streaming: true}},
				{Identifier: "openai/gpt-6.1-sol", ContextWindow: 1050000, MaxOutput: 128000, DefaultTemperature: 1.0, Description: "GPT-6.1 Sol via OpenRouter.",
					Capabilities: model.Capabilities{Vision: true, FunctionCalling: true, Streaming: true}},
				{Identifier: "google/gemini-4-argon", ContextWindow: 1048576, MaxOutput: 65536, DefaultTemperature: 1.0, Description: "Gemini 4 Argon via OpenRouter.",
					Capabilities: model.Capabilities{Vision: true, FunctionCalling: true, Streaming: true}},
				{Identifier: "deepseek/deepseek-v4-pro", ContextWindow: 1048576, MaxOutput: 384000, DefaultTemperature: 1.0, Description: "DeepSeek V4 Pro via OpenRouter.",
					Capabilities: model.Capabilities{FunctionCalling: true, Streaming: true}},
				{Identifier: "moonshotai/kimi-k3", ContextWindow: 1048576, MaxOutput: 131072, DefaultTemperature: 1.0, Description: "Kimi K3 via OpenRouter.",
					Capabilities: model.Capabilities{Vision: true, FunctionCalling: true, Streaming: true}},
				{Identifier: "nvidia/nemotron-3-super-120b-a12b:free", ContextWindow: 262144, MaxOutput: 65536, DefaultTemperature: 0.7, Description: "NVIDIA Nemotron 3 Super 120B (free tier, rate-limited) via OpenRouter. Tested with tool calls and tool-result turns.",
					Capabilities: model.Capabilities{FunctionCalling: true, Streaming: true}},
				{Identifier: "cohere/north-mini-code:free", ContextWindow: 256000, MaxOutput: 64000, DefaultTemperature: 0.7, Description: "Cohere North Mini Code (free tier, rate-limited) via OpenRouter. Agentic coding, fastest of the free models tested.",
					Capabilities: model.Capabilities{FunctionCalling: true, Streaming: true}},
				{Identifier: "nvidia/nemotron-3.5-lightning:free", ContextWindow: 1000000, MaxOutput: 65536, DefaultTemperature: 0.7, Description: "NVIDIA Nemotron 3.5 Lightning (free tier, rate-limited) via OpenRouter. 1M context, light and fast.",
					Capabilities: model.Capabilities{FunctionCalling: true, Streaming: true}},
				{Identifier: "anthropic/claude-haiku-5.5", ContextWindow: 1000000, MaxOutput: 128000, DefaultTemperature: 1.0, Description: "Claude Haiku 5.5 via OpenRouter — fast and cheap.",
					Capabilities: model.Capabilities{Vision: true, FunctionCalling: true, Streaming: true}},
			},
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
			Models: []ModelInfo{
				{Identifier: "deepseek-flash", ContextWindow: 1048576, MaxOutput: 384000, DefaultTemperature: 0.7, Description: "DeepSeek V4.1 Flash — Fast, cost-effective default. 1M context, thinking and non-thinking modes, image input.",
					Capabilities: model.Capabilities{Vision: true, FunctionCalling: true, Streaming: true}},
				{Identifier: "deepseek-v4-pro", ContextWindow: 1048576, MaxOutput: 384000, DefaultTemperature: 0.7, Description: "DeepSeek V4 Pro (0813) — Flagship for demanding reasoning and coding. 1M context.",
					Capabilities: model.Capabilities{FunctionCalling: true, Streaming: true}},
			},
		},
		types.APIProviderOpenCode: {
			Name:         "opencode",
			DisplayName:  "OpenCode Zen",
			Description:  "Curated model gateway (OpenAI-compatible)",
			AuthType:     "api_key",
			AuthTypes:    []string{"api_key"},
			SupportsCVMM: true,
			SupportsPC:   false,
			Models: []ModelInfo{
				{Identifier: "claude-sonnet-5-5", ContextWindow: 1000000, MaxOutput: 128000, DefaultTemperature: 1.0, Description: "Claude Sonnet 5.5 via OpenCode Zen.",
					Capabilities: model.Capabilities{Vision: true, FunctionCalling: true, Streaming: true}},
				{Identifier: "claude-opus-5-5", ContextWindow: 1000000, MaxOutput: 128000, DefaultTemperature: 1.0, Description: "Claude Opus 5.5 via OpenCode Zen.",
					Capabilities: model.Capabilities{Vision: true, FunctionCalling: true, Streaming: true}},
				{Identifier: "gpt-6.1-sol", ContextWindow: 1050000, MaxOutput: 128000, DefaultTemperature: 1.0, Description: "GPT-6.1 Sol via OpenCode Zen.",
					Capabilities: model.Capabilities{Vision: true, FunctionCalling: true, Streaming: true}},
				{Identifier: "gemini-4-argon", ContextWindow: 1048576, MaxOutput: 65536, DefaultTemperature: 1.0, Description: "Gemini 4 Argon via OpenCode Zen.",
					Capabilities: model.Capabilities{Vision: true, FunctionCalling: true, Streaming: true}},
				{Identifier: "grok-4.7", ContextWindow: 500000, MaxOutput: 128000, DefaultTemperature: 1.0, Description: "Grok 4.7 via OpenCode Zen.",
					Capabilities: model.Capabilities{Vision: true, FunctionCalling: true, Streaming: true}},
				{Identifier: "kimi-k3", ContextWindow: 1048576, MaxOutput: 131072, DefaultTemperature: 1.0, Description: "Kimi K3 via OpenCode Zen.",
					Capabilities: model.Capabilities{Vision: true, FunctionCalling: true, Streaming: true}},
				{Identifier: "glm-5.3", ContextWindow: 1048576, MaxOutput: 131072, DefaultTemperature: 0.7, Description: "GLM-5.3 via OpenCode Zen.",
					Capabilities: model.Capabilities{FunctionCalling: true, Streaming: true}},
				{Identifier: "deepseek-v4-pro", ContextWindow: 1048576, MaxOutput: 384000, DefaultTemperature: 0.7, Description: "DeepSeek V4 Pro via OpenCode Zen.",
					Capabilities: model.Capabilities{FunctionCalling: true, Streaming: true}},
				{Identifier: "minimax-m3", ContextWindow: 1048576, MaxOutput: 131072, DefaultTemperature: 1.0, Description: "MiniMax M3 via OpenCode Zen.",
					Capabilities: model.Capabilities{Vision: true, FunctionCalling: true, Streaming: true}},
				{Identifier: "claude-haiku-5-5", ContextWindow: 1000000, MaxOutput: 128000, DefaultTemperature: 1.0, Description: "Claude Haiku 5.5 via OpenCode Zen.",
					Capabilities: model.Capabilities{Vision: true, FunctionCalling: true, Streaming: true}},
			},
		},
		types.APIProviderZAi: {
			Name:         "z-ai",
			DisplayName:  "Z.ai",
			Description:  "GLM models (Chinese AI, OpenAI-compatible)",
			AuthType:     "api_key",
			AuthTypes:    []string{"api_key"},
			SupportsCVMM: true,
			SupportsPC:   true,
			Models: []ModelInfo{
				{Identifier: "glm-5.3", ContextWindow: 1048576, MaxOutput: 131072, DefaultTemperature: 0.7, SupportsPC: true, Description: "GLM-5.3 — Latest flagship GLM. 1M context, reasoning, advanced tool use.",
					Capabilities: model.Capabilities{FunctionCalling: true, Streaming: true, PromptCaching: true}},
				{Identifier: "glm-5.2", ContextWindow: 1048576, MaxOutput: 131072, DefaultTemperature: 0.7, SupportsPC: true, Description: "GLM-5.2 — Previous flagship, 1M context.",
					Capabilities: model.Capabilities{FunctionCalling: true, Streaming: true, PromptCaching: true}},
				{Identifier: "glm-5.1", ContextWindow: 200000, MaxOutput: 128000, DefaultTemperature: 0.7, SupportsPC: true, Description: "GLM-5.1 — Long-horizon autonomous execution with tool use.",
					Capabilities: model.Capabilities{FunctionCalling: true, Streaming: true, PromptCaching: true}},
				{Identifier: "glm-5", ContextWindow: 200000, MaxOutput: 128000, DefaultTemperature: 0.7, Description: "GLM-5 — Agentic and coding model.",
					Capabilities: model.Capabilities{FunctionCalling: true, Streaming: true, PromptCaching: true}},
				{Identifier: "glm-4.7", ContextWindow: 200000, MaxOutput: 128000, DefaultTemperature: 0.7, Description: "GLM-4.7 — Stable previous generation.",
					Capabilities: model.Capabilities{FunctionCalling: true, Streaming: true}},
				{Identifier: "glm-5.3-flashx", ContextWindow: 1048576, MaxOutput: 131072, DefaultTemperature: 0.7, Description: "GLM-5.3 FlashX — Faster, cheaper GLM-5.3 tier.",
					Capabilities: model.Capabilities{FunctionCalling: true, Streaming: true}},
				{Identifier: "glm-5v-turbo", ContextWindow: 200000, MaxOutput: 131072, DefaultTemperature: 0.7, SupportsPC: true, Description: "GLM-5V-Turbo — 200k context, reasoning, image input.",
					Capabilities: model.Capabilities{Vision: true, FunctionCalling: true, Streaming: true, PromptCaching: true}},
				{Identifier: "glm-5-turbo", ContextWindow: 200000, MaxOutput: 131072, DefaultTemperature: 0.7, SupportsPC: true, Description: "GLM-5-Turbo — 200k context, reasoning.",
					Capabilities: model.Capabilities{FunctionCalling: true, Streaming: true, PromptCaching: true}},
				{Identifier: "glm-4.7-flash", ContextWindow: 200000, MaxOutput: 131072, DefaultTemperature: 0.7, SupportsPC: true, Description: "GLM-4.7-Flash — 200k context, reasoning.",
					Capabilities: model.Capabilities{FunctionCalling: true, Streaming: true}},
				{Identifier: "glm-4.7-flashx", ContextWindow: 200000, MaxOutput: 131072, DefaultTemperature: 0.7, SupportsPC: true, Description: "GLM-4.7-FlashX — 200k context, reasoning.",
					Capabilities: model.Capabilities{FunctionCalling: true, Streaming: true, PromptCaching: true}},
				{Identifier: "glm-4.6", ContextWindow: 204800, MaxOutput: 131072, DefaultTemperature: 0.7, SupportsPC: true, Description: "GLM-4.6 — 205k context, reasoning.",
					Capabilities: model.Capabilities{FunctionCalling: true, Streaming: true, PromptCaching: true}},
				{Identifier: "glm-4.6v", ContextWindow: 128000, MaxOutput: 32768, DefaultTemperature: 0.7, SupportsPC: true, Description: "GLM-4.6V — 128k context, reasoning, image input.",
					Capabilities: model.Capabilities{Vision: true, FunctionCalling: true, Streaming: true}},
				{Identifier: "glm-4.5", ContextWindow: 131072, MaxOutput: 98304, DefaultTemperature: 0.7, SupportsPC: true, Description: "GLM-4.5 — 131k context, reasoning.",
					Capabilities: model.Capabilities{FunctionCalling: true, Streaming: true, PromptCaching: true}},
				{Identifier: "glm-4.5-air", ContextWindow: 131072, MaxOutput: 98304, DefaultTemperature: 0.7, SupportsPC: true, Description: "GLM-4.5-Air — 131k context, reasoning.",
					Capabilities: model.Capabilities{FunctionCalling: true, Streaming: true, PromptCaching: true}},
				{Identifier: "glm-4.5-flash", ContextWindow: 131072, MaxOutput: 98304, DefaultTemperature: 0.7, SupportsPC: true, Description: "GLM-4.5-Flash — 131k context, reasoning.",
					Capabilities: model.Capabilities{FunctionCalling: true, Streaming: true}},
				{Identifier: "glm-5.3-flash", ContextWindow: 1048576, MaxOutput: 131072, DefaultTemperature: 0.7, Description: "GLM-5.3 Flash — Fastest and cheapest GLM-5.3 tier, image/video input.",
					Capabilities: model.Capabilities{Vision: true, FunctionCalling: true, Streaming: true}},
			},
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
			Models: []ModelInfo{
				{Identifier: "kimi-k3", ContextWindow: 1048576, MaxOutput: 1048576, DefaultTemperature: 0.7, SupportsPC: true, Description: "Kimi K3 — Flagship native multimodal agentic model. 1M context, vision, reasoning mode.",
					Capabilities: model.Capabilities{Vision: true, FunctionCalling: true, Streaming: true, PromptCaching: true}},
				{Identifier: "kimi-k2.7-code", ContextWindow: 262144, MaxOutput: 262144, DefaultTemperature: 0.7, SupportsPC: true, Description: "Kimi K2.7 Code — Coding specialist, always thinking. Long-horizon agentic coding over large repositories.",
					Capabilities: model.Capabilities{FunctionCalling: true, Streaming: true, PromptCaching: true}},
				{Identifier: "kimi-k2.7-code-highspeed", ContextWindow: 262144, MaxOutput: 262144, DefaultTemperature: 0.7, SupportsPC: true, Description: "Kimi K2.7 Code (high-speed) — Same coding specialist tuned for faster output.",
					Capabilities: model.Capabilities{FunctionCalling: true, Streaming: true, PromptCaching: true}},
				{Identifier: "kimi-k2.6", ContextWindow: 262144, MaxOutput: 262144, DefaultTemperature: 0.7, SupportsPC: true, Description: "Kimi K2.6 — Multimodal model with vision, thinking mode and agent orchestration.",
					Capabilities: model.Capabilities{Vision: true, FunctionCalling: true, Streaming: true, PromptCaching: true}},
			},
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
	default:
		return types.APIProviderAnthropic
	}
}

func GetProviderInfo(provider types.APIProvider) (ProviderInfo, bool) {
	info, ok := AllProvidersInfo()[provider]
	return info, ok
}

func GetModelInfo(provider types.APIProvider, model string) (ModelInfo, bool) {
	info, ok := AllProvidersInfo()[provider]
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
