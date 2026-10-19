package config

import (
	"sort"

	"charm.land/catwalk/pkg/catwalk"
	internalproviders "github.com/KPO-Tech/seshat-runtime/internal/providers"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// catalogModelsPerProvider bounds how many models of a generic provider the model dialog lists: the newest ones. Some providers
// list hundreds.
const catalogModelsPerProvider = 60

// isCatalogProviderID reports whether id is a generic provider of the models.dev catalog.
func isCatalogProviderID(id string) bool {
	return internalproviders.IsCatalogProvider(types.APIProvider(id))
}

// catalogProviders returns the generic providers of the models.dev catalog as catwalk providers, so the TUI offers them next to
// the built-in ones: about 190 OpenAI-compatible providers, called with the generic client. A provider that only a downloaded
// catalog knows (Unverified) is left out: the TUI has no step yet to have the user confirm its URL before a key is sent.
func catalogProviders() []catwalk.Provider {
	cat := internalproviders.Catalog()
	if cat == nil {
		return nil
	}
	var out []catwalk.Provider
	for _, info := range internalproviders.CatalogProviders() {
		cp, ok := cat.Provider(info.Name)
		if !ok || cp.Unverified {
			continue
		}
		models := make([]catwalk.Model, 0, len(info.Models))
		for _, m := range info.Models {
			if len(models) == catalogModelsPerProvider {
				break
			}
			cm, _ := cat.Model(info.Name, m.Identifier)
			models = append(models, catwalk.Model{
				ID:               m.Identifier,
				Name:             cm.Name,
				ContextWindow:    int64(m.ContextWindow),
				DefaultMaxTokens: int64(m.MaxOutput),
				CanReason:        cm.Reasoning,
				SupportsImages:   m.Capabilities.Vision,
				CostPer1MIn:      cm.Cost.Input,
				CostPer1MOut:     cm.Cost.Output,
			})
		}
		if len(models) == 0 {
			continue
		}
		out = append(out, catwalk.Provider{
			ID:                  catwalk.InferenceProvider(info.Name),
			Name:                info.DisplayName,
			Type:                catwalk.TypeOpenAICompat,
			APIEndpoint:         cp.BaseURL,
			Models:              models,
			DefaultLargeModelID: models[0].ID,
			DefaultSmallModelID: cheapestModel(models),
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// cheapestModel returns the id of the model with the lowest known input price, which makes a fair default small model. The models
// are newest first: when no price is known, or the newest is also the cheapest, it is the first one.
func cheapestModel(models []catwalk.Model) string {
	best := models[0]
	for _, m := range models[1:] {
		if m.CostPer1MIn > 0 && (best.CostPer1MIn <= 0 || m.CostPer1MIn < best.CostPer1MIn) {
			best = m
		}
	}
	return best.ID
}
