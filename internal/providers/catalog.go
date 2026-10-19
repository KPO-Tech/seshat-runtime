package providers

import (
	"os"
	"regexp"
	"strings"
	"sync"

	"github.com/KPO-Tech/seshat-runtime/internal/catalog"
	"github.com/KPO-Tech/seshat-runtime/internal/model"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// nativeProviders maps the id a provider has in the models.dev catalog to the provider this runtime has built in for it. A
// catalog provider in this table is not offered as a generic one: our own entry, with its own protocol quirks, serves it, and the
// catalog only fills in models our curated list does not have.
var nativeProviders = map[string]types.APIProvider{
	"anthropic":  types.APIProviderAnthropic,
	"openai":     types.APIProviderOpenAI,
	"google":     types.APIProviderGemini,
	"mistral":    types.APIProviderMistral,
	"minimax":    types.APIProviderMiniMax,
	"openrouter": types.APIProviderOpenRouter,
	"deepseek":   types.APIProviderDeepSeek,
	"opencode":   types.APIProviderOpenCode,
	"zai":        types.APIProviderZAi,
	"moonshotai": types.APIProviderKimi,
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
			if m.Deprecated() || m.ContextWindow <= 0 || unsuitableForChat(types.APIProvider(cp.ID), m.ID, cp.Models) {
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
	return &Config{Provider: p, BaseURL: expandCatalogURL(cp.BaseURL)}, true
}

// catalogURLVariable matches a ${NAME} placeholder in a base URL of the catalog (Cloudflare Workers AI carries the account id there).
var catalogURLVariable = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// expandCatalogURL replaces the ${NAME} placeholders of a catalog base URL with the environment variables of that name. A variable
// that is not set stays as it is, so the URL is visibly incomplete (see CatalogURLVariables). Only the URL of a verified provider is
// expanded, which is the one that comes from the embedded catalog.
func expandCatalogURL(url string) string {
	return catalogURLVariable.ReplaceAllStringFunc(url, func(m string) string {
		name := catalogURLVariable.FindStringSubmatch(m)[1]
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return v
		}
		return m
	})
}

// CatalogURLVariables returns the environment variables the base URL of a generic provider needs and that are not set, or nil.
func CatalogURLVariables(p types.APIProvider) []string {
	cp, ok := catalogProvider(p)
	if !ok {
		return nil
	}
	var missing []string
	for _, m := range catalogURLVariable.FindAllStringSubmatch(expandCatalogURL(cp.BaseURL), -1) {
		missing = append(missing, m[1])
	}
	return missing
}

// credentialVariables keeps, among the environment variables a catalog entry lists, the ones that carry a secret (a key, a token).
// The others (an account id, a region) configure the URL. When none looks like a secret, every one is kept.
func credentialVariables(env []string) []string {
	var out []string
	for _, name := range env {
		upper := strings.ToUpper(name)
		if strings.Contains(upper, "KEY") || strings.Contains(upper, "TOKEN") || strings.Contains(upper, "SECRET") {
			out = append(out, name)
		}
	}
	if len(out) == 0 {
		return append([]string(nil), env...)
	}
	return out
}

// catalogEnvVar returns the environment variable that holds the API key of a generic catalog provider, or "" when p is not one.
func catalogEnvVar(p types.APIProvider) string {
	vars := CatalogCredentialEnvVars(p)
	if len(vars) == 0 {
		return ""
	}
	return vars[0]
}

// CatalogCredentialEnvVars returns the environment variables that can hold the API key of a generic provider (the ones of its catalog
// entry that look like a secret), or nil when p is not one.
func CatalogCredentialEnvVars(p types.APIProvider) []string {
	cp, ok := catalogProvider(p)
	if !ok {
		return nil
	}
	return credentialVariables(cp.Env)
}

// unsuitableMarkers are parts of a model id that mark a model the chat client cannot use: speech, realtime and image models, embeddings.
var unsuitableMarkers = []string{"realtime", "audio", "transcribe", "tts", "image", "embed", "moderation", "search-preview"}

// datedSnapshot matches the date a provider appends to a model id to pin a version (claude-opus-4-5-20251101, gpt-4o-2024-08-06).
var datedSnapshot = regexp.MustCompile(`-(\d{8}|\d{4}-\d{2}-\d{2})$`)

// unsuitableForChat reports whether a catalog model should not be listed in a picker of the chat client: a model made for another
// use (see unsuitableMarkers), one of OpenAI's pro and codex models, which are served by the Responses API only, and a dated copy of
// a model that is also listed without its date. The catalog is data about models, not about which of our protocols can call them.
func unsuitableForChat(provider types.APIProvider, id string, all map[string]catalog.Model) bool {
	lower := strings.ToLower(id)
	for _, marker := range unsuitableMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	if provider == types.APIProviderOpenAI && (strings.Contains(lower, "-pro") || strings.Contains(lower, "codex")) {
		return true
	}
	if loc := datedSnapshot.FindStringIndex(id); loc != nil {
		if _, undated := all[id[:loc[0]]]; undated {
			return true
		}
	}
	return false
}

// maxCatalogModelsPerProvider bounds how many catalog models are added to the list of one built-in provider: the newest ones. An
// aggregator such as OpenRouter lists hundreds.
const maxCatalogModelsPerProvider = 40

// catalogExtended names, for each built-in provider whose model list the catalog extends, its id in the catalog. Codex is not in the
// catalog (it is a ChatGPT subscription route) and Ollama lists the models installed on the machine.
func catalogExtended() map[types.APIProvider]string {
	out := make(map[types.APIProvider]string, len(nativeProviders))
	for catalogID, native := range nativeProviders {
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
			if have[m.ID] || m.Deprecated() || m.ContextWindow <= 0 || unsuitableForChat(provider, m.ID, cp.Models) {
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
