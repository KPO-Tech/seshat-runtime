// Package catalog is Seshat's view of the public models.dev catalog of providers and models: which providers exist, the
// base URL and API key variable of each, and what each model can do (context window, output limit, image input, reasoning,
// cost, status). It is the data opencode builds its provider list from.
//
// Two rules govern how the data is used:
//
//   - The catalog is data about models, not an authority over where a key is sent. The base URL, the API key variable and the
//     protocol of a provider come from the snapshot embedded in this binary (reviewed when it was built). A catalog fetched
//     later can update models and add providers, but a provider it adds is Unverified: its URL must be shown to the user and
//     confirmed before a key is sent to it. See Merge.
//   - Our own catalog of models (internal/providers) wins over this one. The catalog's limits are community data: some are
//     placeholders (an output limit equal to the context window usually means "unknown").
package catalog

import (
	"sort"
	"strings"
)

// Cost is the price of a model in US dollars per million tokens. A zero value means the price is unknown.
type Cost struct {
	Input      float64
	Output     float64
	CacheRead  float64
	CacheWrite float64
}

// Model describes one model of a provider.
type Model struct {
	ID            string
	Name          string
	ContextWindow int
	// MaxOutput is the output limit, 0 when unknown. See OutputIsKnown.
	MaxOutput int
	// InputModalities lists what the model accepts besides text: "image", "audio", "video", "pdf".
	InputModalities []string
	Reasoning       bool
	Cost            Cost
	// Status is "", "alpha", "beta" or "deprecated".
	Status      string
	ReleaseDate string
}

// Vision reports whether the model accepts images.
func (m Model) Vision() bool { return m.accepts("image") }

// Audio reports whether the model accepts audio.
func (m Model) Audio() bool { return m.accepts("audio") }

func (m Model) accepts(modality string) bool {
	for _, x := range m.InputModalities {
		if x == modality {
			return true
		}
	}
	return false
}

// Deprecated reports whether the provider has retired or is retiring the model.
func (m Model) Deprecated() bool { return m.Status == "deprecated" }

// OutputIsKnown reports whether the output limit is a real one. The catalog often repeats the context window when the limit is
// not known, so a limit equal to the window, or a missing one, is not trusted.
func (m Model) OutputIsKnown() bool {
	return m.MaxOutput > 0 && m.MaxOutput < m.ContextWindow
}

// Provider describes one provider of the catalog.
type Provider struct {
	ID   string
	Name string
	// BaseURL is the API base URL, empty for providers that have no fixed one (Anthropic, OpenAI, Gemini, AWS...).
	BaseURL string
	// Env lists the environment variables that can hold the API key, the first one first.
	Env []string
	// Protocol is the SDK package the catalog names for the provider: "@ai-sdk/openai-compatible" for most.
	Protocol string
	Models   map[string]Model
	// Unverified is true for a provider that is not in the embedded snapshot: its BaseURL comes from a catalog fetched at run
	// time and must be confirmed by the user before an API key is sent to it.
	Unverified bool
}

// protocolOpenAICompatible is the catalog's name for providers that speak the OpenAI chat completions protocol.
const protocolOpenAICompatible = "@ai-sdk/openai-compatible"

// OpenAICompatible reports whether the provider can be called with the generic OpenAI-compatible client: it speaks that
// protocol and the catalog gives its base URL.
func (p Provider) OpenAICompatible() bool {
	return p.Protocol == protocolOpenAICompatible && strings.TrimSpace(p.BaseURL) != ""
}

// SortedModels returns the models, newest first, then by id. Deprecated models come last.
func (p Provider) SortedModels() []Model {
	out := make([]Model, 0, len(p.Models))
	for _, m := range p.Models {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Deprecated() != out[j].Deprecated() {
			return !out[i].Deprecated()
		}
		if out[i].ReleaseDate != out[j].ReleaseDate {
			return out[i].ReleaseDate > out[j].ReleaseDate
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Catalog is a set of providers.
type Catalog struct {
	Providers map[string]Provider
	// Source says where the data came from: "embedded", "cache" or the URL it was fetched from.
	Source string
}

// Provider returns the provider with that id.
func (c *Catalog) Provider(id string) (Provider, bool) {
	if c == nil {
		return Provider{}, false
	}
	p, ok := c.Providers[id]
	return p, ok
}

// Model returns a model of a provider.
func (c *Catalog) Model(providerID, modelID string) (Model, bool) {
	p, ok := c.Provider(providerID)
	if !ok {
		return Model{}, false
	}
	m, ok := p.Models[modelID]
	return m, ok
}

// OpenAICompatibleProviders returns the providers the generic OpenAI-compatible client can call, sorted by name.
func (c *Catalog) OpenAICompatibleProviders() []Provider {
	if c == nil {
		return nil
	}
	var out []Provider
	for _, p := range c.Providers {
		if p.OpenAICompatible() {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}
