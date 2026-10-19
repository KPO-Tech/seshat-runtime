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
func catalogProviderID(catalogID string) types.APIProvider { return ProviderForCatalogID(catalogID) }

// ProviderForCatalogID is catalogProviderID for hosts and tools.
func ProviderForCatalogID(catalogID string) types.APIProvider {
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
		ContextWindow:      model.ContextWindow{MaxTokens: m.ContextWindow, MaxOutputTokens: maxOutput, MaxInputTokens: m.MaxInput},
		DefaultTemperature: 1.0,
		Capabilities:       model.Capabilities{Vision: m.Vision(), Audio: m.Audio(), FunctionCalling: true, Streaming: true, PromptCaching: m.Cost.CacheRead > 0, StructuredOutput: m.StructuredOutput, NoTemperature: m.NoTemperature},
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
		out = append(out, catalogProviderInfo(cp))
	}
	return out
}

// catalogProviderInfo describes a generic provider of the catalog: its name, how it authenticates and its models, newest first.
func catalogProviderInfo(cp catalog.Provider) ProviderInfo {
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
	return info
}

// catalogInfoFor returns the description of a provider that is not built in but that the catalog has, models included.
func catalogInfoFor(p types.APIProvider) (ProviderInfo, bool) {
	cp, ok := catalogProvider(p)
	if !ok {
		return ProviderInfo{}, false
	}
	return catalogProviderInfo(cp), true
}

// catalogBaseURL is the base URL of a generic provider as the catalog gives it, with its variables filled in, or "" when the provider is
// not a generic one or a variable is still missing (a URL with a hole in it must not be called).
func catalogBaseURL(p types.APIProvider) string {
	cp, ok := catalogProvider(p)
	if !ok {
		return ""
	}
	if url := expandCatalogURL(cp.BaseURL); !strings.Contains(url, "${") {
		return url
	}
	return ""
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
var unsuitableMarkers = []string{"realtime", "audio", "transcribe", "tts", "image", "embed", "moderation", "search-preview", "deep-research", "computer-use"}

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

// recentModelsConsidered is how many of the newest models are looked at to pick the default large one.
const recentModelsConsidered = 10

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

// extendWithCatalog gives each built-in provider the models of the catalog. The registry names none of them: the list is the
// catalog's, newest first, with the default large model first and the default small model last (see rankModels).
func extendWithCatalog(curated map[types.APIProvider]ProviderInfo, cat *catalog.Catalog) map[types.APIProvider]ProviderInfo {
	out := make(map[types.APIProvider]ProviderInfo, len(curated))
	for k, v := range curated {
		out[k] = v
	}
	for provider, catalogID := range catalogExtended() {
		info, ok := out[provider]
		cp, found := cat.Provider(catalogID)
		if !ok || !found {
			continue
		}
		models := listModels(provider, info, cp, catalogID, nil)
		if len(models) == 0 {
			continue
		}
		info.Models = models
		out[provider] = info
	}
	return out
}

// rankModels orders the models of a provider (given newest first): the default large model first, the default small one last,
// the others between them, newest first. Nothing is hand-written: the large model is the most expensive of the newest ones (price
// follows capability), the small one the cheapest. A provider whose models have no price gets its newest model as the large one and
// its oldest as the small one.
func rankModels(defaults ModelDefaults, models []catalog.Model) []catalog.Model {
	if len(models) < 2 {
		return models
	}
	large, small := pickByPolicy(defaults, models)
	if large < 0 || small < 0 {
		byPriceLarge, byPriceSmall := pickByPrice(models)
		if large < 0 {
			large = byPriceLarge
		}
		if small < 0 {
			small = byPriceSmall
		}
	}
	if small == large {
		// One model for both sides: keep the large one first and take another, the last, as the small one.
		small = len(models) - 1
		if small == large {
			small = 0
		}
	}
	return arrange(models, large, small)
}

// arrange puts the large model first, the small one last, and the others between them in their order.
func arrange(models []catalog.Model, large, small int) []catalog.Model {
	out := make([]catalog.Model, 0, len(models))
	out = append(out, models[large])
	for i, m := range models {
		if i != large && i != small {
			out = append(out, m)
		}
	}
	return append(out, models[small])
}

// pickByPrice is the choice for a provider the table does not cover.
func pickByPrice(models []catalog.Model) (large, small int) {
	// A model still in alpha or beta is listed but is not picked as a default.
	var stable []int
	for i, m := range models {
		if m.Status == "" {
			stable = append(stable, i)
		}
	}
	if len(stable) == 0 {
		for i := range models {
			stable = append(stable, i)
		}
	}
	if len(stable) > recentModelsConsidered {
		stable = stable[:recentModelsConsidered]
	}
	large = stable[0]
	for _, i := range stable {
		if models[i].Cost.Output > models[large].Cost.Output {
			large = i
		}
	}
	small = -1
	for i, m := range models {
		if i == large || m.Cost.Input <= 0 || m.Status != "" {
			continue
		}
		if small < 0 || m.Cost.Input < models[small].Cost.Input {
			small = i
		}
	}
	if small < 0 {
		small = len(models) - 1
		if small == large {
			small = 0
		}
	}
	return large, small
}

// catalogModelInfo is the registry entry of a catalog model.
func catalogModelInfo(provider types.APIProvider, info ProviderInfo, m catalog.Model) ModelInfo {
	meta := catalogModelMetadata(string(provider), m)
	return ModelInfo{
		Identifier:         m.ID,
		ContextWindow:      m.ContextWindow,
		MaxOutput:          min(meta.ContextWindow.MaxOutputTokens, m.ContextWindow),
		DefaultTemperature: meta.DefaultTemperature,
		SupportsPC:         info.SupportsPC,
		Description:        m.Name,
		Capabilities:       meta.Capabilities,
	}
}

// sendsTemperature reports whether the request's temperature goes into the body: the request sets one and the catalog does not say
// the model rejects it (many reasoning models answer 400 to a temperature).
func sendsTemperature(req types.APIRequest) bool {
	if req.Temperature == nil {
		return false
	}
	meta, ok := model.Global.Lookup(string(req.Model.Provider), req.Model.Model)
	return !ok || !meta.Capabilities.NoTemperature
}
