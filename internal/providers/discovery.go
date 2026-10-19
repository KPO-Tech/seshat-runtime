package providers

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"sort"
	"time"

	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// DiscoveryResult reports the availability and authentication status of one provider.
type DiscoveryResult struct {
	Provider    types.APIProvider
	DisplayName string
	Available   bool   // at least one auth credential is present/reachable
	AuthMethod  string // "api_key", "local", "aws", "gcp"
	EnvVar      string // env var name that was checked (empty for local services)
	Recommended bool   // included in the recommended set for first-time setup
	Priority    int    // lower = higher priority in the recommendation list
	Hint        string // human-readable note for the operator
}

// providerPriority returns the recommendation priority for a provider.
// Lower values surface first. Providers not listed here get priority 99.
func providerPriority(p types.APIProvider) int {
	switch p {
	case types.APIProviderAnthropic:
		return 1
	case types.APIProviderOpenAI:
		return 2
	case types.APIProviderMistral:
		return 3
	case types.APIProviderOllama:
		return 4
	case types.APIProviderGemini:
		return 5
	case types.APIProviderDeepSeek:
		return 6
	case types.APIProviderOpenRouter:
		return 7
	default:
		return 99
	}
}

// DiscoverProviders scans the environment for configured providers. It checks
// known env vars and pings Ollama's local endpoint. Results are sorted by
// recommendation priority; available providers appear before unavailable ones.
//
// The context is used for the Ollama HTTP health check only.
func DiscoverProviders(ctx context.Context) []DiscoveryResult {
	candidates := []struct {
		provider    types.APIProvider
		displayName string
		authMethod  string
		check       func() (bool, string, string) // → (available, envVar/source, hint)
	}{
		{
			types.APIProviderAnthropic, "Anthropic (Claude)", "api_key",
			func() (bool, string, string) {
				ev := providerEnvVar(types.APIProviderAnthropic)
				if os.Getenv(ev) != "" {
					return true, ev, "Best for coding and reasoning tasks"
				}
				return false, ev, fmt.Sprintf("Set %s to enable", ev)
			},
		},
		{
			types.APIProviderOpenAI, "OpenAI (GPT)", "api_key",
			func() (bool, string, string) {
				ev := providerEnvVar(types.APIProviderOpenAI)
				if os.Getenv(ev) != "" {
					return true, ev, "Wide model range (GPT-4o, o3, o4-mini)"
				}
				return false, ev, fmt.Sprintf("Set %s to enable", ev)
			},
		},
		{
			types.APIProviderMistral, "Mistral", "api_key",
			func() (bool, string, string) {
				ev := providerEnvVar(types.APIProviderMistral)
				if os.Getenv(ev) != "" {
					return true, ev, "Fast and cost-effective European provider"
				}
				return false, ev, fmt.Sprintf("Set %s to enable", ev)
			},
		},
		{
			types.APIProviderOllama, "Ollama (local)", "local",
			func() (bool, string, string) {
				if probeOllama(ctx) {
					return true, "localhost:11434", "Local inference, fully private"
				}
				return false, "localhost:11434", "Start Ollama and run `ollama pull <model>`"
			},
		},
		{
			types.APIProviderGemini, "Google Gemini", "api_key",
			func() (bool, string, string) {
				ev := providerEnvVar(types.APIProviderGemini)
				if os.Getenv(ev) != "" {
					return true, ev, "Gemini 2.0 Flash / 1.5 Pro"
				}
				return false, ev, fmt.Sprintf("Set %s to enable", ev)
			},
		},
		{
			types.APIProviderDeepSeek, "DeepSeek", "api_key",
			func() (bool, string, string) {
				ev := providerEnvVar(types.APIProviderDeepSeek)
				if os.Getenv(ev) != "" {
					return true, ev, "DeepSeek-R1 / DeepSeek-V3"
				}
				return false, ev, fmt.Sprintf("Set %s to enable", ev)
			},
		},
		{
			types.APIProviderOpenRouter, "OpenRouter", "api_key",
			func() (bool, string, string) {
				ev := providerEnvVar(types.APIProviderOpenRouter)
				if os.Getenv(ev) != "" {
					return true, ev, "Access to 200+ models via a single key"
				}
				return false, ev, fmt.Sprintf("Set %s to enable", ev)
			},
		},
		{
			types.APIProviderMiniMax, "MiniMax", "api_key",
			func() (bool, string, string) {
				ev := providerEnvVar(types.APIProviderMiniMax)
				if os.Getenv(ev) != "" {
					return true, ev, ""
				}
				return false, ev, fmt.Sprintf("Set %s to enable", ev)
			},
		},
		{
			types.APIProviderZAi, "Z.ai", "api_key",
			func() (bool, string, string) {
				ev := providerEnvVar(types.APIProviderZAi)
				if os.Getenv(ev) != "" {
					return true, ev, ""
				}
				return false, ev, fmt.Sprintf("Set %s to enable", ev)
			},
		},
		{
			types.APIProviderKimi, "Kimi (Moonshot AI)", "api_key",
			func() (bool, string, string) {
				ev := providerEnvVar(types.APIProviderKimi)
				if os.Getenv(ev) != "" {
					return true, ev, "Kimi K3 — 1M-token context, vision, strong agentic coding"
				}
				return false, ev, fmt.Sprintf("Set %s to enable", ev)
			},
		},
	}

	results := make([]DiscoveryResult, 0, len(candidates))
	for _, c := range candidates {
		avail, source, hint := c.check()
		pri := providerPriority(c.provider)
		results = append(results, DiscoveryResult{
			Provider:    c.provider,
			DisplayName: c.displayName,
			Available:   avail,
			AuthMethod:  c.authMethod,
			EnvVar:      source,
			Recommended: pri <= 4, // Anthropic, OpenAI, Mistral, Ollama
			Priority:    pri,
			Hint:        hint,
		})
	}

	sort.Slice(results, func(i, j int) bool {
		// Available first, then by priority, then by name.
		if results[i].Available != results[j].Available {
			return results[i].Available
		}
		if results[i].Priority != results[j].Priority {
			return results[i].Priority < results[j].Priority
		}
		return results[i].DisplayName < results[j].DisplayName
	})

	return results
}

// AvailableProviders returns only the providers that have credentials present.
func AvailableProviders(ctx context.Context) []DiscoveryResult {
	all := DiscoverProviders(ctx)
	available := all[:0]
	for _, r := range all {
		if r.Available {
			available = append(available, r)
		}
	}
	return available
}

// probeOllama returns true when Ollama's local endpoint responds within 500 ms.
func probeOllama(ctx context.Context) bool {
	ollamaURL := os.Getenv("OLLAMA_HOST")
	if ollamaURL == "" {
		ollamaURL = "http://localhost:11434"
	}
	probeCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()

	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, ollamaURL+"/api/tags", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
