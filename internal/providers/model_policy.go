package providers

import (
	"github.com/KPO-Tech/seshat-runtime/internal/catalog"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// ModelDefaults says which models a provider uses when the user has not chosen: the large one (first of the provider's list) and the
// small one (last, used for titles and compaction). Each entry is a family of the catalog ("claude-sonnet") or an exact model id.
// The entries are tried in order and the first that the provider has wins; for a family the newest stable model is taken. A side left
// empty is not settled by this policy.
type ModelDefaults struct {
	Large []string
	Small []string
}

// ModelPolicy is a set of ModelDefaults by provider, as a host writes them. It is a plain value passed to the functions below: the
// package keeps none, so two hosts (or two clients) in one process can use different policies without touching each other.
type ModelPolicy map[types.APIProvider]ModelDefaults

// runtimeModelPolicy is the runtime's own choice of default models, by family of the catalog (never by model id, so a new version of
// the family is picked up by the next catalog). Keyed by the provider's id in the catalog.
var runtimeModelPolicy = map[string]ModelDefaults{
	"anthropic":  {Large: []string{"claude-sonnet"}, Small: []string{"claude-haiku"}},
	"openai":     {Large: []string{"gpt-sol", "gpt"}, Small: []string{"gpt-luna", "gpt-mini"}},
	"google":     {Large: []string{"gemini", "gemini-pro"}, Small: []string{"gemini-flash-lite", "gemini-flash"}},
	"mistral":    {Large: []string{"mistral-large"}, Small: []string{"mistral-small"}},
	"openrouter": {Large: []string{"claude-sonnet"}, Small: []string{"claude-haiku"}},
	"opencode":   {Large: []string{"claude-sonnet"}, Small: []string{"claude-haiku"}},
	"deepseek":   {Large: []string{"deepseek-flash"}, Small: []string{"deepseek-flash"}},
	"zai":        {Large: []string{"glm"}, Small: []string{"glm-flash"}},
	"moonshotai": {Large: []string{"kimi-k3", "kimi-k2"}, Small: []string{"kimi-k2"}},
}

// DefaultModelPolicy returns a copy of the runtime's policy, keyed by the runtime's provider names, for a host to read, complete or
// change before passing it back.
func DefaultModelPolicy() ModelPolicy {
	out := make(ModelPolicy, len(runtimeModelPolicy))
	for catalogID, d := range runtimeModelPolicy {
		out[ProviderForCatalogID(catalogID)] = ModelDefaults{Large: append([]string(nil), d.Large...), Small: append([]string(nil), d.Small...)}
	}
	return out
}

// effectiveDefaults is what applies to a provider: the host's entries first, the runtime's after them. A host therefore only writes
// what it changes, and an entry of the host that names a model or family the catalog no longer has falls through to the runtime's.
func effectiveDefaults(provider types.APIProvider, catalogID string, host ModelPolicy) ModelDefaults {
	base := runtimeModelPolicy[catalogID]
	over := host[provider]
	return ModelDefaults{
		Large: append(append([]string(nil), over.Large...), base.Large...),
		Small: append(append([]string(nil), over.Small...), base.Small...),
	}
}

// pickByPolicy returns the indexes of the models (newest first) the defaults settle, -1 for a side they do not settle.
func pickByPolicy(d ModelDefaults, models []catalog.Model) (large, small int) {
	return findModel(d.Large, models), findModel(d.Small, models)
}

func findModel(entries []string, models []catalog.Model) int {
	for _, entry := range entries {
		for i, m := range models {
			if m.ID == entry && !m.Deprecated() {
				return i
			}
		}
		for i, m := range models { // newest first: the newest stable model of the family
			if m.Family == entry && m.Status == "" {
				return i
			}
		}
	}
	return -1
}

// listModels is the model list of a built-in provider: the catalog's eligible models, newest first, with the default large model
// first and the small one last under the given host policy (nil for the runtime's own).
func listModels(provider types.APIProvider, info ProviderInfo, cp catalog.Provider, catalogID string, host ModelPolicy) []ModelInfo {
	var eligible []catalog.Model
	for _, m := range cp.SortedModels() {
		if m.Deprecated() || m.ContextWindow <= 0 || unsuitableForChat(provider, m.ID, cp.Models) {
			continue
		}
		eligible = append(eligible, m)
	}
	models := make([]ModelInfo, 0, len(eligible))
	for _, m := range rankModels(effectiveDefaults(provider, catalogID, host), eligible) {
		models = append(models, catalogModelInfo(provider, info, m))
	}
	return models
}

// ProviderInfoWithPolicy is GetProviderInfo with the host's policy applied to the order of the models. It builds a new value and
// changes nothing shared. A provider whose list the catalog does not build (Codex, Ollama) comes back as it is.
func ProviderInfoWithPolicy(provider types.APIProvider, host ModelPolicy) (ProviderInfo, bool) {
	info, ok := GetProviderInfo(provider)
	if !ok {
		return ProviderInfo{}, false
	}
	catalogID, native := catalogIDOf(provider)
	cat := Catalog()
	if !native || cat == nil {
		return info, true
	}
	cp, found := cat.Provider(catalogID)
	if !found {
		return info, true
	}
	if models := listModels(provider, info, cp, catalogID, host); len(models) > 0 {
		info.Models = models
	}
	return info, true
}

// DefaultModelWithPolicy is DefaultModelIdentifier under the host's policy: the first model of the list.
func DefaultModelWithPolicy(provider types.APIProvider, host ModelPolicy) (types.ModelIdentifier, bool) {
	info, ok := ProviderInfoWithPolicy(provider, host)
	if !ok || len(info.Models) == 0 {
		return types.ModelIdentifier{}, false
	}
	return types.ModelIdentifier{Provider: provider, Model: info.Models[0].Identifier}, true
}

// SmallModelWithPolicy returns the small model of a provider under the host's policy: the last of the list when it has several.
func SmallModelWithPolicy(provider types.APIProvider, host ModelPolicy) (string, bool) {
	info, ok := ProviderInfoWithPolicy(provider, host)
	if !ok || len(info.Models) == 0 {
		return "", false
	}
	return info.Models[len(info.Models)-1].Identifier, true
}

// catalogIDOf returns the catalog id of a built-in provider.
func catalogIDOf(provider types.APIProvider) (string, bool) {
	for catalogID, native := range nativeProviders {
		if native == provider {
			return catalogID, true
		}
	}
	return "", false
}
