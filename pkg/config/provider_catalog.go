package config

import (
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"

	internalproviders "github.com/KPO-Tech/seshat-runtime/internal/providers"
	"github.com/KPO-Tech/seshat-runtime/pkg/runtimepath"
	"github.com/KPO-Tech/seshat-runtime/pkg/sdk"
)

// ProviderSetupField is one value needed to set a provider up: its key, label and description, the environment variable that can carry it, whether it is a secret and whether it is required.
type ProviderSetupField struct {
	Key         string
	Label       string
	Description string
	EnvVar      string
	Secret      bool
	Required    bool
}

// ProviderInfo describes a provider: its name, display name and description, how it authenticates (AuthType, and every accepted type in AuthTypes), whether it accepts images (SupportsCVMM) and supports prompt caching (SupportsPC), its models, the fields to set it up and a hint about the setup.
type ProviderInfo struct {
	Name         sdk.APIProvider
	DisplayName  string
	Description  string
	AuthType     string
	AuthTypes    []string
	SupportsCVMM bool
	SupportsPC   bool
	Models       []ModelInfo
	SetupFields  []ProviderSetupField
	SetupHint    string
}

// ModelInfo describes a model of a provider: its identifier, its context window and maximum output in tokens, whether it supports prompt caching, its pricing and a description.
type ModelInfo struct {
	Identifier    string
	ContextWindow int
	MaxOutput     int
	SupportsPC    bool
	Pricing       string
	Description   string
}

var providerOrder = []sdk.APIProvider{
	sdk.APIProviderAnthropic,
	sdk.APIProviderOpenAI,
	sdk.APIProviderCodex,
	sdk.APIProviderGemini,
	sdk.APIProviderZAi,
	sdk.APIProviderOpenRouter,
	sdk.APIProviderDeepSeek,
	sdk.APIProviderOpenCode,
	sdk.APIProviderMistral,
	sdk.APIProviderMiniMax,
	sdk.APIProviderKimi,
	sdk.APIProviderOllama,
}

// AvailableProviders returns the providers the runtime knows, in the order of the catalogue, with their models and setup fields.
func AvailableProviders() []ProviderInfo {
	raw := internalproviders.AllProvidersInfo()
	providers := make([]ProviderInfo, 0, len(raw))

	for _, provider := range providerOrder {
		info, ok := raw[provider]
		if !ok {
			continue
		}
		providers = append(providers, providerInfoFromInternal(provider, info))
	}

	return providers
}

// AvailableCatalogProviders returns the generic providers of the models.dev catalog, sorted by name: the OpenAI-compatible ones the
// runtime does not have built in, about 190 of them. They are kept out of AvailableProviders so that a host that lists providers
// does not suddenly list them all; a host that wants them offers them apart, with a search. A provider that only a downloaded
// catalog knows has "Unverified" in its description and needs its URL confirmed before a key is sent to it.
func AvailableCatalogProviders() []ProviderInfo {
	raw := internalproviders.CatalogProviders()
	out := make([]ProviderInfo, 0, len(raw))
	for _, info := range raw {
		out = append(out, providerInfoFromInternal(sdk.APIProvider(info.Name), info))
	}
	return out
}

// GetProviderInfo returns the catalogue entry of a provider, and false when the provider is unknown.
func GetProviderInfo(provider sdk.APIProvider) (ProviderInfo, bool) {
	info, ok := internalproviders.GetProviderInfo(provider)
	if !ok {
		return ProviderInfo{}, false
	}
	return providerInfoFromInternal(provider, info), true
}

// ResolveProvider turns a provider name as a user may write it (for example "claude", "gpt", "aws", "z.ai") into a provider, or returns an empty provider when the name is not recognised.
func ResolveProvider(raw string) sdk.APIProvider {
	provider, ok := parseProvider(raw)
	if !ok {
		return ""
	}
	return provider
}

// ProviderForModel returns the provider whose catalogue lists the model identifier, compared without regard to case, or an empty provider when no catalogue lists it.
func ProviderForModel(model string) sdk.APIProvider {
	normalized := strings.ToLower(strings.TrimSpace(model))
	if normalized == "" {
		return ""
	}

	for _, provider := range AvailableProviders() {
		for _, candidate := range provider.Models {
			if strings.EqualFold(candidate.Identifier, normalized) {
				return provider.Name
			}
		}
	}

	return ""
}

// ProviderCredentialEnvVars returns the environment variables that can carry the credentials of a provider (for example ANTHROPIC_API_KEY), or none for a provider that does not use an API key (Ollama, Bedrock, Vertex).
func ProviderCredentialEnvVars(provider sdk.APIProvider) []string {
	switch provider {
	case sdk.APIProviderAnthropic:
		return []string{"ANTHROPIC_API_KEY"}
	case sdk.APIProviderOpenAI:
		return []string{"OPENAI_API_KEY"}
	case sdk.APIProviderCodex:
		return []string{"CODEX_API_KEY", "CODEX_ACCESS_TOKEN"}
	case sdk.APIProviderGemini:
		return []string{"GOOGLE_API_KEY", "GOOGLE_GEMINI_API_KEY"}
	case sdk.APIProviderZAi:
		return []string{"ZHIPUAI_API_KEY", "Z_AI_API_KEY"}
	case sdk.APIProviderOpenRouter:
		return []string{"OPENROUTER_API_KEY"}
	case sdk.APIProviderDeepSeek:
		return []string{"DEEPSEEK_API_KEY"}
	case sdk.APIProviderOpenCode:
		return []string{"OPENCODE_API_KEY"}
	case sdk.APIProviderMistral:
		return []string{"MISTRAL_API_KEY"}
	case sdk.APIProviderMiniMax:
		return []string{"MINIMAX_API_KEY"}
	case sdk.APIProviderKimi:
		return []string{"MOONSHOT_API_KEY", "KIMI_API_KEY"}
	case sdk.APIProviderOllama:
		return nil
	default:
		// A generic provider of the models.dev catalog names its own variables.
		return internalproviders.CatalogCredentialEnvVars(provider)
	}
}

// ResolveAPIKey returns the API key to use for provider: the one in the environment when it is set, otherwise the key of config.
func ResolveAPIKey(config Config, provider sdk.APIProvider) string {
	if key := resolveAPIKeyFromEnv(provider); key != "" {
		return key
	}
	return strings.TrimSpace(config.APIKey)
}

// ValidateProviderSetup checks that config holds what provider needs: an API key for most providers, the environment variables that complete the base URL of a generic catalog provider (the account id of Cloudflare Workers AI), and nothing for Ollama. The error names what is missing.
func ValidateProviderSetup(config Config, provider sdk.APIProvider) error {
	switch provider {
	case sdk.APIProviderOllama:
		return nil
	default:
		if len(ProviderCredentialEnvVars(provider)) == 0 {
			return nil
		}
		if ResolveAPIKey(config, provider) == "" {
			return fmt.Errorf("provider %s requires API key", provider)
		}
		if missing := internalproviders.CatalogURLVariables(provider); len(missing) > 0 {
			return fmt.Errorf("provider %s requires %s", provider, strings.Join(missing, ", "))
		}
		return nil
	}
}

// ApplyRuntimeEnv exports the runtime root of config to the environment, where the runtime reads it. A blank value is not exported.
func ApplyRuntimeEnv(config Config) {
	setEnvIfPresent(runtimepath.EnvRuntimeRoot, EffectiveRuntimeRoot(config))
}

func parseProvider(raw string) (sdk.APIProvider, bool) {
	switch strings.TrimSpace(strings.ToLower(raw)) {
	case "anthropic", "claude":
		return sdk.APIProviderAnthropic, true
	case "openai", "gpt":
		return sdk.APIProviderOpenAI, true
	case "codex":
		return sdk.APIProviderCodex, true
	case "ollama":
		return sdk.APIProviderOllama, true
	case "gemini", "google-ai":
		return sdk.APIProviderGemini, true
	case "z-ai", "z.ai", "zai":
		return sdk.APIProviderZAi, true
	case "openrouter":
		return sdk.APIProviderOpenRouter, true
	case "deepseek", "deep-seek":
		return sdk.APIProviderDeepSeek, true
	case "opencode", "opencode-zen", "opencode_zen":
		return sdk.APIProviderOpenCode, true
	case "mistral", "mistral-ai", "mistralai":
		return sdk.APIProviderMistral, true
	case "minimax":
		return sdk.APIProviderMiniMax, true
	case "kimi", "moonshot":
		return sdk.APIProviderKimi, true
	default:
		// A generic provider of the models.dev catalog is named by its catalog id ("groq", "siliconflow"...).
		if id := strings.TrimSpace(strings.ToLower(raw)); internalproviders.IsCatalogProvider(sdk.APIProvider(id)) {
			return sdk.APIProvider(id), true
		}
		return "", false
	}
}

func providerInfoFromInternal(provider sdk.APIProvider, info internalproviders.ProviderInfo) ProviderInfo {
	models := make([]ModelInfo, 0, len(info.Models))
	for _, model := range info.Models {
		models = append(models, ModelInfo{
			Identifier:    model.Identifier,
			ContextWindow: model.ContextWindow,
			MaxOutput:     model.MaxOutput,
			SupportsPC:    model.SupportsPC,
			Pricing:       model.Pricing,
			Description:   model.Description,
		})
	}

	sort.Slice(models, func(i, j int) bool {
		return strings.ToLower(models[i].Identifier) < strings.ToLower(models[j].Identifier)
	})

	return ProviderInfo{
		Name:         provider,
		DisplayName:  info.DisplayName,
		Description:  info.Description,
		AuthType:     info.AuthType,
		AuthTypes:    append([]string(nil), info.AuthTypes...),
		SupportsCVMM: info.SupportsCVMM,
		SupportsPC:   info.SupportsPC,
		Models:       models,
		SetupFields:  setupFieldsForProvider(provider),
		SetupHint:    setupHintForProvider(provider),
	}
}

func setupFieldsForProvider(provider sdk.APIProvider) []ProviderSetupField {
	fields := make([]ProviderSetupField, 0, 4)
	if envVars := ProviderCredentialEnvVars(provider); len(envVars) > 0 {
		fields = append(fields, ProviderSetupField{
			Key:         "api_key",
			Label:       "API key",
			Description: "Credential used for requests",
			EnvVar:      envVars[0],
			Secret:      true,
			Required:    true,
		})
	}

	switch provider {
	case sdk.APIProviderOllama:
		fields = append(fields, ProviderSetupField{
			Key:         "provider_base_url",
			Label:       "Ollama endpoint",
			Description: "Leave blank for http://localhost:11434",
			EnvVar:      "OLLAMA_HOST",
		})
	}

	return fields
}

func setupHintForProvider(provider sdk.APIProvider) string {
	switch provider {
	case sdk.APIProviderOllama:
		return "No API key required. Leave endpoint blank to use http://localhost:11434."
	default:
		return ""
	}
}

func resolveAPIKeyFromEnv(provider sdk.APIProvider) string {
	for _, envVar := range ProviderCredentialEnvVars(provider) {
		if value := strings.TrimSpace(os.Getenv(envVar)); value != "" {
			return value
		}
	}
	return ""
}

func setEnvIfPresent(key, value string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	_ = os.Setenv(key, value)
}

// ProviderNames returns the names of the providers of the catalogue, in catalogue order.
func ProviderNames() []sdk.APIProvider {
	out := make([]sdk.APIProvider, 0, len(providerOrder))
	for _, provider := range providerOrder {
		if _, ok := GetProviderInfo(provider); ok {
			out = append(out, provider)
		}
	}
	return out
}

// IsKnownProvider reports whether provider is in the catalogue.
func IsKnownProvider(provider sdk.APIProvider) bool {
	return slices.Contains(ProviderNames(), provider)
}
