package providers

import (
	"context"

	internalproviders "github.com/KPO-Tech/seshat-runtime/internal/providers"
	internaltypes "github.com/KPO-Tech/seshat-runtime/internal/types"
)

type (
	// Client represents an API client
	Client = internalproviders.Client
	// Config is the connection settings of a provider: its API key, base URL, region and project, the aliases that map a short model name to a model identifier, custom headers, and the routing policy.
	Config = internalproviders.Config
	// DiscoveryResult reports the availability and authentication status of one provider.
	DiscoveryResult = internalproviders.DiscoveryResult
	// FetchedModel is a provider-neutral model description returned by FetchModels
	// and StaticModels. Callers convert it to their own storage type.
	FetchedModel = internalproviders.FetchedModel
	// ModelInfo describes a single model offered by a provider.
	// Capabilities contains fine-grained per-model feature flags.
	// The Pricing field is a legacy string; structured pricing lives in model.Metadata.
	ModelInfo = internalproviders.ModelInfo
	// ProviderInfo describes a provider of the catalogue: its name, how it authenticates, whether it accepts images and supports prompt caching, and the models it offers.
	ProviderInfo = internalproviders.ProviderInfo
)

// NewClient creates a new API client
func NewClient(apiKey string, providerType internaltypes.APIProvider) *Client {
	return internalproviders.NewClient(apiKey, providerType)
}

// NewClientWithConfig builds a provider client from an explicit Config
// (provider type, base URL, etc.) rather than just a provider type - needed
// by callers resolving a specific org/platform provider setting (custom
// base URL, self-hosted endpoint) instead of using provider defaults.
func NewClientWithConfig(apiKey string, config *Config) *Client {
	return internalproviders.NewClientWithConfig(apiKey, config)
}

// StaticModels returns the hardcoded catalog for a provider.
// Returns nil for Ollama, which has no static model list (100% dynamic).
func StaticModels(providerName string) []FetchedModel {
	return internalproviders.StaticModels(providerName)
}

// FetchModels retrieves the live model list from a provider API.
// For Ollama it calls /api/tags and enriches each model via /api/show.
// For OpenAI-compatible providers it calls /v1/models.
// Returns an error so the caller can fall back to StaticModels.
func FetchModels(ctx context.Context, providerName, baseURL, apiKey string) ([]FetchedModel, error) {
	return internalproviders.FetchModels(ctx, providerName, baseURL, apiKey)
}

// DefaultBaseURL returns the default API base URL for a provider.
// These are sync/discovery URLs (no /v1 suffix); client endpoints live in config.go.
func DefaultBaseURL(providerName string) string {
	return internalproviders.DefaultBaseURL(providerName)
}

// NeedsCatalogReseed returns true when none of the supplied model IDs exist in
// the provider's static catalog — meaning models from the wrong provider were
// seeded, or the list is empty.
func NeedsCatalogReseed(providerName string, knownModelIDs []string) bool {
	return internalproviders.NeedsCatalogReseed(providerName, knownModelIDs)
}

// ResolveProviderFromString turns a provider name as a user may write it (for example "claude", "gpt", "aws") into a provider.
func ResolveProviderFromString(s string) internaltypes.APIProvider {
	return internalproviders.ResolveProviderFromString(s)
}

// GetModelInfo returns the catalogue entry of a model of a provider, matched on its exact identifier, and false when there is none.
func GetModelInfo(provider internaltypes.APIProvider, model string) (ModelInfo, bool) {
	return internalproviders.GetModelInfo(provider, model)
}

// AllProvidersInfo returns the catalogue: every provider with its models.
func AllProvidersInfo() map[internaltypes.APIProvider]ProviderInfo {
	return internalproviders.AllProvidersInfo()
}

// DiscoverProviders scans the environment for configured providers. It checks
// known env vars and pings Ollama's local endpoint. Results are sorted by
// recommendation priority; available providers appear before unavailable ones.
//
// The context is used for the Ollama HTTP health check only.
func DiscoverProviders(ctx context.Context) []DiscoveryResult {
	return internalproviders.DiscoverProviders(ctx)
}

// GetProviderConfig returns a copy of the default configuration of a provider, which the caller can complete with a key or override, or nil when the provider has none.
func GetProviderConfig(provider internaltypes.APIProvider) *Config {
	return internalproviders.GetProviderConfig(provider)
}

type (
	// ModelDefaults names the default large and small model of a provider as families of the models.dev catalog ("claude-sonnet") or
	// exact model ids, tried in order.
	ModelDefaults = internalproviders.ModelDefaults
	// ModelPolicy is a set of ModelDefaults by provider. A host writes only what it changes: its entries are tried first and the
	// runtime's after them, so an entry naming a model the catalog no longer has falls back to the runtime's choice. It is a value
	// passed to the functions below; nothing is stored in the package.
	ModelPolicy = internalproviders.ModelPolicy
)

// DefaultModelPolicy returns a copy of the runtime's own default models, to read or complete.
func DefaultModelPolicy() ModelPolicy { return internalproviders.DefaultModelPolicy() }

// ProviderInfoWithPolicy returns the catalogue entry of a provider with its models ordered under the host's policy: the default large
// model first, the small one last.
func ProviderInfoWithPolicy(provider internaltypes.APIProvider, policy ModelPolicy) (ProviderInfo, bool) {
	return internalproviders.ProviderInfoWithPolicy(provider, policy)
}

// DefaultModelWithPolicy returns the default large model of a provider under the host's policy.
func DefaultModelWithPolicy(provider internaltypes.APIProvider, policy ModelPolicy) (internaltypes.ModelIdentifier, bool) {
	return internalproviders.DefaultModelWithPolicy(provider, policy)
}

// SmallModelWithPolicy returns the default small model of a provider under the host's policy.
func SmallModelWithPolicy(provider internaltypes.APIProvider, policy ModelPolicy) (string, bool) {
	return internalproviders.SmallModelWithPolicy(provider, policy)
}
