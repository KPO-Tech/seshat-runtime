package config

import (
	_ "embed"
	"encoding/json"
	"strings"

	"charm.land/catwalk/pkg/catwalk"
	internalproviders "github.com/KPO-Tech/seshat-runtime/internal/providers"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
	engineconfig "github.com/KPO-Tech/seshat-runtime/pkg/config"
)

//go:embed extra_models.json
var extraModelsJSON []byte

// extraModelEntry mirrors one entry in extra_models.json.
type extraModelEntry struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	ContextWindow  int64  `json:"context_window"`
	DefaultMaxToks int64  `json:"default_max_tokens"`
	CanReason      bool   `json:"can_reason"`
	SupportsImages bool   `json:"supports_images"`
}

// loadExtraModels parses extra_models.json and returns a map of
// providerID → extra catwalk models. Called once at init time.
func loadExtraModels() map[string][]catwalk.Model {
	var raw map[string][]extraModelEntry
	// Strip the _comment key — json.Unmarshal skips unknown fields by default
	// but the key must still be valid JSON, so no special handling needed.
	if err := json.Unmarshal(extraModelsJSON, &raw); err != nil {
		return nil
	}
	out := make(map[string][]catwalk.Model, len(raw))
	for pid, entries := range raw {
		if pid == "_comment" {
			continue
		}
		models := make([]catwalk.Model, 0, len(entries))
		for _, e := range entries {
			models = append(models, catwalk.Model{
				ID:               e.ID,
				Name:             e.Name,
				ContextWindow:    e.ContextWindow,
				DefaultMaxTokens: e.DefaultMaxToks,
				CanReason:        e.CanReason,
				SupportsImages:   e.SupportsImages,
			})
		}
		out[pid] = models
	}
	return out
}

// apiEndpointFor returns the base API URL used by TestConnection for each
// seshat provider. Must match the URL prefixes in config.go TestConnection.
func apiEndpointFor(providerID string) string {
	switch providerID {
	case "anthropic":
		return "https://api.anthropic.com/v1"
	case "openai":
		return "https://api.openai.com/v1"
	case "gemini":
		return "https://generativelanguage.googleapis.com"
	case "mistral":
		return "https://api.mistral.ai/v1"
	case "deepseek":
		return "https://api.deepseek.com/v1"
	case "openrouter":
		return "https://openrouter.ai/api/v1"
	case "z-ai":
		return "https://api.z.ai/api/paas/v4"
	case "minimax":
		return "https://api.minimax.chat/v1"
	case "opencode":
		return "https://opencode.ai/zen/v1"
	case "kimi":
		return "https://api.moonshot.ai/v1"
	case "ollama":
		return "http://localhost:11434"
	default:
		return ""
	}
}

// catwalkProviderType maps a seshat provider ID to the matching catwalk.Type
// used by the TUI's API key verification flow (TestConnection).
func catwalkProviderType(providerID string) catwalk.Type {
	switch providerID {
	case "anthropic":
		return catwalk.TypeAnthropic
	case "gemini":
		return catwalk.TypeGoogle
	default:
		return catwalk.TypeOpenAI
	}
}

// buildSeshatProviders constructs the canonical []catwalk.Provider list from
// seshat's SDK provider catalog, extended with the models in extra_models.json.
// Ollama is included but its Models list is intentionally empty — the workspace
// populates it at runtime via providers.FetchModels (DetectProviders goroutine).
func buildSeshatProviders() []catwalk.Provider {
	extras := loadExtraModels()

	sdkProviders := engineconfig.AvailableProviders()
	out := make([]catwalk.Provider, 0, len(sdkProviders))

	for _, p := range sdkProviders {
		pid := string(p.Name)

		// Convert the registry models to catwalk.Model, in registry order: the
		// first is the default, the last the small model. The catalog view in
		// pkg/config sorts them by name, which would make the default the
		// alphabetically first one.
		var baseModels []catwalk.Model
		seen := make(map[string]struct{}, len(p.Models))
		if info, ok := internalproviders.GetProviderInfo(types.APIProvider(pid)); ok {
			for _, m := range info.Models {
				baseModels = append(baseModels, catwalk.Model{
					ID:               m.Identifier,
					Name:             m.Identifier,
					ContextWindow:    int64(m.ContextWindow),
					DefaultMaxTokens: int64(m.MaxOutput),
					SupportsImages:   m.Capabilities.Vision,
				})
				seen[m.Identifier] = struct{}{}
			}
		}

		// Append extra models that aren't already in the base list.
		for _, extra := range extras[pid] {
			if extra.ID == "" {
				continue
			}
			if _, dup := seen[extra.ID]; dup {
				continue
			}
			baseModels = append(baseModels, extra)
			seen[extra.ID] = struct{}{}
		}

		// Pick the first model as the default if any exist.
		defaultLarge := ""
		defaultSmall := ""
		if len(baseModels) > 0 {
			defaultLarge = baseModels[0].ID
		}
		// A provider with only two models has no model that is clearly the small
		// one: keep the same one for both rather than the more expensive.
		if len(baseModels) > 2 {
			defaultSmall = baseModels[len(baseModels)-1].ID
		} else if len(baseModels) > 0 {
			defaultSmall = baseModels[0].ID
		}

		out = append(out, catwalk.Provider{
			ID:                  catwalk.InferenceProvider(pid),
			Name:                p.DisplayName,
			Type:                catwalkProviderType(pid),
			APIEndpoint:         apiEndpointFor(pid),
			Models:              baseModels,
			DefaultLargeModelID: defaultLarge,
			DefaultSmallModelID: defaultSmall,
		})
	}

	return append(out, catalogProviders()...)
}

// needsAPIKey returns true when the provider requires an API key to authenticate.
// Used to decide whether to skip the API key input dialog.
func needsAPIKey(providerID string) bool {
	switch strings.ToLower(providerID) {
	case "ollama":
		return false
	default:
		return true
	}
}
