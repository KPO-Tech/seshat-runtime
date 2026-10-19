package providers

import (
	"sync"

	"github.com/KPO-Tech/seshat-runtime/internal/catalog"
	"github.com/KPO-Tech/seshat-runtime/internal/model"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// nativeProviders maps the id a provider has in the models.dev catalog to the provider this runtime has built in for it. A
// catalog provider in this table is not offered as a generic one: our own entry, with its own protocol quirks, serves it, and the
// catalog only fills in models our curated list does not have.
var nativeProviders = map[string]types.APIProvider{
	"anthropic":             types.APIProviderAnthropic,
	"openai":                types.APIProviderOpenAI,
	"google":                types.APIProviderGemini,
	"mistral":               types.APIProviderMistral,
	"minimax":               types.APIProviderMiniMax,
	"openrouter":            types.APIProviderOpenRouter,
	"deepseek":              types.APIProviderDeepSeek,
	"opencode":              types.APIProviderOpenCode,
	"zai":                   types.APIProviderZAi,
	"moonshotai":            types.APIProviderKimi,
	"cloudflare-workers-ai": types.APIProviderWorkersAI,
	"amazon-bedrock":        types.APIProviderBedrock,
	"google-vertex":         types.APIProviderVertex,
	"azure":                 types.APIProviderFoundry,
}

// defaultCatalogMaxOutput is the output limit given to a catalog model whose limit is unknown (see catalog.Model.OutputIsKnown).
const defaultCatalogMaxOutput = 8192

var (
	catalogMu      sync.RWMutex
	currentCatalog *catalog.Catalog
	// extendedProviders is the curated provider list extended with the catalog's models, computed once per catalog.
	extendedProviders map[types.APIProvider]ProviderInfo
)

func init() {
	if embedded, err := catalog.Embedded(); err == nil {
		SetCatalog(embedded)
	}
}

// SetCatalog makes cat the catalog this package reads, and registers the models it knows that the curated catalog does not, so
// context windows and vision support are known for them. A host calls it with catalog.Load's result after a refresh. The curated
// catalog always wins: a model it lists is never replaced.
func SetCatalog(cat *catalog.Catalog) {
	if cat == nil {
		return
	}
	extended := extendWithCatalog(curatedProvidersInfo(), cat)
	catalogMu.Lock()
	currentCatalog = cat
	extendedProviders = extended
	catalogMu.Unlock()
	for id, p := range cat.Providers {
		ours := catalogProviderID(id)
		for _, m := range p.Models {
			model.Global.RegisterIfAbsent(catalogModelMetadata(string(ours), m))
		}
	}
}

// Catalog returns the catalog this package reads.
func Catalog() *catalog.Catalog {
	catalogMu.RLock()
	defer catalogMu.RUnlock()
	return currentCatalog
}

// catalogProviderID returns the provider id this runtime uses for a catalog provider: our own for a built-in one, the catalog id
// otherwise.
func catalogProviderID(catalogID string) types.APIProvider {
	if native, ok := nativeProviders[catalogID]; ok {
		return native
	}
	return types.APIProvider(catalogID)
}

func catalogModelMetadata(provider string, m catalog.Model) model.Metadata {
	maxOutput := defaultCatalogMaxOutput
	if m.OutputIsKnown() {
		maxOutput = m.MaxOutput
	}
	return model.Metadata{
		ID:                 m.ID,
		Provider:           provider,
		Description:        m.Name,
		ContextWindow:      model.ContextWindow{MaxTokens: m.ContextWindow, MaxOutputTokens: maxOutput},
		DefaultTemperature: 1.0,
		Capabilities:       model.Capabilities{Vision: m.Vision(), Audio: m.Audio(), FunctionCalling: true, Streaming: true, PromptCaching: m.Cost.CacheRead > 0},
		Pricing:            model.Pricing{InputPerMTok: m.Cost.Input, OutputPerMTok: m.Cost.Output, CacheReadPerMTok: m.Cost.CacheRead, CacheWritePerMTok: m.Cost.CacheWrite},
	}
}

// catalogProvider returns the generic provider the catalog has for a provider id: one that is not built in and speaks the
// OpenAI-compatible protocol.
func catalogProvider(p types.APIProvider) (catalog.Provider, bool) {
	cat := Catalog()
	if cat == nil {
		return catalog.Provider{}, false
	}
	if _, builtIn := nativeProviders[string(p)]; builtIn {
		return catalog.Provider{}, false
	}
	cp, ok := cat.Provider(string(p))
	if !ok || !cp.OpenAICompatible() {
		return catalog.Provider{}, false
	}
	return cp, true
}

// IsCatalogProvider reports whether p is a generic provider of the catalog, called with the OpenAI-compatible client.
func IsCatalogProvider(p types.APIProvider) bool {
	_, ok := catalogProvider(p)
	return ok
}

// CatalogProviders lists the generic providers of the catalog, sorted by name, as ProviderInfo. A provider that only a fetched
// catalog knows is marked in its description: the host must have the user confirm its URL before a key is sent (see
// ConfigForCatalogProvider).
func CatalogProviders() []ProviderInfo {
	cat := Catalog()
	if cat == nil {
		return nil
	}
	var out []ProviderInfo
	for _, cp := range cat.OpenAICompatibleProviders() {
		if _, builtIn := nativeProviders[cp.ID]; builtIn {
			continue
		}
		info := ProviderInfo{
			Name:        cp.ID,
			DisplayName: cp.Name,
			Description: "OpenAI-compatible provider from the models.dev catalog (" + cp.BaseURL + ")",
			AuthType:    "api_key",
			AuthTypes:   []string{"api_key"},
		}
		if cp.Unverified {
			info.Description = "Unverified provider from a downloaded catalog: confirm " + cp.BaseURL + " before sending a key"
		}
		for _, m := range cp.SortedModels() {
			if m.Deprecated() || m.ContextWindow <= 0 {
				continue
			}
			maxOutput := defaultCatalogMaxOutput
			if m.OutputIsKnown() {
				maxOutput = m.MaxOutput
			}
			info.Models = append(info.Models, ModelInfo{
				Identifier:         m.ID,
				ContextWindow:      m.ContextWindow,
				MaxOutput:          maxOutput,
				DefaultTemperature: 1.0,
				Description:        m.Name,
				Capabilities:       catalogModelMetadata(cp.ID, m).Capabilities,
			})
		}
		out = append(out, info)
	}
	return out
}

// ConfigForCatalogProvider returns the configuration to call a generic catalog provider: its id and its base URL. It refuses a
// provider that is not in the embedded catalog (Unverified), because the base URL of such a provider comes from a downloaded file
// and the key would go wherever that file says. A host that has shown the URL to the user and had it confirmed builds the Config
// itself, with that URL.
func ConfigForCatalogProvider(p types.APIProvider) (*Config, bool) {
	cp, ok := catalogProvider(p)
	if !ok || cp.Unverified {
		return nil, false
	}
	return &Config{Provider: p, BaseURL: cp.BaseURL}, true
}

// catalogEnvVar returns the environment variable that holds the API key of a generic catalog provider, or "" when p is not one.
func catalogEnvVar(p types.APIProvider) string {
	cp, ok := catalogProvider(p)
	if !ok || len(cp.Env) == 0 {
		return ""
	}
	return cp.Env[0]
}

// CatalogCredentialEnvVars returns every environment variable the catalog names for the API key of a generic provider, or nil when
// p is not one.
func CatalogCredentialEnvVars(p types.APIProvider) []string {
	cp, ok := catalogProvider(p)
	if !ok {
		return nil
	}
	return append([]string(nil), cp.Env...)
}

// maxCatalogModelsPerProvider bounds how many catalog models are added to the list of one built-in provider: the newest ones. An
// aggregator such as OpenRouter lists hundreds.
const maxCatalogModelsPerProvider = 40

// catalogExtended names, for each built-in provider whose model list the catalog extends, its id in the catalog. Bedrock, Vertex and
// Foundry are left out: their model ids carry a region or a deployment, which the catalog does not give in the form we call them
// with. Codex is not in the catalog (it is a ChatGPT subscription route) and Ollama lists the models installed on the machine.
func catalogExtended() map[types.APIProvider]string {
	out := make(map[types.APIProvider]string, len(nativeProviders))
	for catalogID, native := range nativeProviders {
		switch native {
		case types.APIProviderBedrock, types.APIProviderVertex, types.APIProviderFoundry:
			continue
		}
		out[native] = catalogID
	}
	return out
}

// AllProvidersInfo returns the built-in providers with their models: the ones listed by hand, whose description and limits we set,
// followed by the models of the models.dev catalog that the list does not have, newest first. A model added to the catalog therefore
// shows up in the pickers without anyone editing this package. The first model of a list stays the default large one and the last
// the default small one (see TestProvidersKeepRegistryOrderAndDefaults in the terminal app): the catalog's models go between them.
func AllProvidersInfo() map[types.APIProvider]ProviderInfo {
	catalogMu.RLock()
	extended := extendedProviders
	catalogMu.RUnlock()
	if extended == nil {
		return curatedProvidersInfo()
	}
	out := make(map[types.APIProvider]ProviderInfo, len(extended))
	for k, v := range extended {
		out[k] = v
	}
	return out
}

// extendWithCatalog adds the catalog's models to the curated provider list.
func extendWithCatalog(curated map[types.APIProvider]ProviderInfo, cat *catalog.Catalog) map[types.APIProvider]ProviderInfo {
	out := make(map[types.APIProvider]ProviderInfo, len(curated))
	for k, v := range curated {
		out[k] = v
	}
	for provider, catalogID := range catalogExtended() {
		info, ok := out[provider]
		cp, found := cat.Provider(catalogID)
		if !ok || !found || len(info.Models) == 0 {
			continue
		}
		have := make(map[string]bool, len(info.Models))
		for _, m := range info.Models {
			have[m.Identifier] = true
		}
		first := info.Models[0]
		var added []ModelInfo
		for _, m := range cp.SortedModels() {
			if len(added) == maxCatalogModelsPerProvider {
				break
			}
			if have[m.ID] || m.Deprecated() || m.ContextWindow <= 0 {
				continue
			}
			meta := catalogModelMetadata(string(provider), m)
			caps := meta.Capabilities
			// Follow the provider's own conventions for what the catalog does not say.
			caps.PromptCaching = caps.PromptCaching && first.Capabilities.PromptCaching
			caps.StructuredOutput = first.Capabilities.StructuredOutput
			added = append(added, ModelInfo{
				Identifier:         m.ID,
				ContextWindow:      m.ContextWindow,
				MaxOutput:          min(meta.ContextWindow.MaxOutputTokens, m.ContextWindow),
				DefaultTemperature: first.DefaultTemperature,
				SupportsPC:         first.SupportsPC,
				Description:        m.Name,
				Capabilities:       caps,
			})
		}
		if len(added) == 0 {
			continue
		}
		// Keep the first model (default large) first and the last (default small) last.
		models := make([]ModelInfo, 0, len(info.Models)+len(added))
		models = append(models, info.Models[:len(info.Models)-1]...)
		models = append(models, added...)
		models = append(models, info.Models[len(info.Models)-1])
		info.Models = models
		out[provider] = info
	}
	return out
}
