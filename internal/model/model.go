// Package model provides a centralised registry of LLM model metadata:
// context windows, pricing, and capability flags (vision, audio, etc.).
//
// Consumption pattern:
//
//	meta, ok := model.Global.Lookup(modelID)
//	if ok && meta.Capabilities.Vision {
//	    // attach image content
//	}
package model

import "sync"

// Capabilities describes what a specific model variant can do.
type Capabilities struct {
	// Vision indicates the model can process image content blocks.
	Vision bool
	// Audio indicates the model can process audio content.
	Audio bool
	// FunctionCalling indicates tool/function-call support.
	FunctionCalling bool
	// Streaming indicates server-sent-event streaming support.
	Streaming bool
	// PromptCaching indicates provider-level prompt caching support.
	PromptCaching bool
	// StructuredOutput indicates native JSON schema output support.
	StructuredOutput bool
	// NoTemperature indicates the model rejects the temperature parameter: it must not be sent.
	NoTemperature bool
}

// Pricing holds per-token costs in USD per 1 million tokens (MTok).
// Zero values mean pricing is unknown or not applicable.
type Pricing struct {
	// InputPerMTok is the cost for input (prompt) tokens.
	InputPerMTok float64
	// OutputPerMTok is the cost for output (completion) tokens.
	OutputPerMTok float64
	// CacheReadPerMTok is the discounted cost for cache-hit input tokens.
	CacheReadPerMTok float64
	// CacheWritePerMTok is the premium charged when writing to the prompt cache.
	CacheWritePerMTok float64
}

// ContextWindow describes the token budget for a model.
type ContextWindow struct {
	// MaxTokens is the maximum combined input+output tokens.
	MaxTokens int
	// MaxOutputTokens is the cap on generated tokens per call.
	MaxOutputTokens int
	// MaxInputTokens caps the prompt alone when it is below MaxTokens; 0 means the prompt can use the window.
	MaxInputTokens int
}

// Metadata aggregates all model-level metadata.
type Metadata struct {
	// ID is the canonical model identifier string (e.g. "claude-3-5-sonnet-20241022").
	ID string
	// Provider is the upstream provider name (e.g. "anthropic").
	Provider string
	// Description is a human-readable summary.
	Description string
	// ContextWindow contains token budget information.
	ContextWindow ContextWindow
	// DefaultTemperature is the provider-recommended sampling temperature.
	DefaultTemperature float64
	// Capabilities lists what the model supports.
	Capabilities Capabilities
	// Pricing holds per-token cost data (zero = unknown).
	Pricing Pricing
}

// Registry is a lookup table from (provider, modelID) → Metadata.
// It is safe for concurrent use: the catalog of providers adds entries while sessions look models up.
type Registry struct {
	mu      sync.RWMutex
	entries map[registryKey]Metadata
}

type registryKey struct{ provider, modelID string }

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{entries: make(map[registryKey]Metadata)}
}

// Register adds or replaces a model entry.
func (r *Registry) Register(m Metadata) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries[registryKey{m.Provider, m.ID}] = m
}

// RegisterIfAbsent adds a model entry unless the registry already has that (provider, model) pair, and reports whether it
// added it. It lets a broader source (the models.dev catalog) fill in what the curated one does not know without overriding it.
func (r *Registry) RegisterIfAbsent(m Metadata) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := registryKey{m.Provider, m.ID}
	if _, ok := r.entries[key]; ok {
		return false
	}
	r.entries[key] = m
	return true
}

// Lookup returns the Metadata for a (provider, modelID) pair.
// provider may be empty to fall back to a model-ID-only search across all providers.
func (r *Registry) Lookup(provider, modelID string) (Metadata, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if m, ok := r.entries[registryKey{provider, modelID}]; ok {
		return m, true
	}
	// Provider-agnostic fallback: return the first match on model ID.
	if provider == "" {
		for k, v := range r.entries {
			if k.modelID == modelID {
				return v, true
			}
		}
	}
	return Metadata{}, false
}

// ContextWindowFor returns the ContextWindow for the given (provider, modelID),
// or (128000, 4096) as a conservative default when the model is unknown.
func (r *Registry) ContextWindowFor(provider, modelID string) ContextWindow {
	if m, ok := r.Lookup(provider, modelID); ok {
		return m.ContextWindow
	}
	return ContextWindow{MaxTokens: 128000, MaxOutputTokens: 4096}
}

// VisionCapable returns true when the model supports image content blocks.
func (r *Registry) VisionCapable(provider, modelID string) bool {
	m, ok := r.Lookup(provider, modelID)
	return ok && m.Capabilities.Vision
}

// All returns a copy of every registered model.
func (r *Registry) All() []Metadata {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Metadata, 0, len(r.entries))
	for _, v := range r.entries {
		out = append(out, v)
	}
	return out
}

// Global is the package-level registry. It starts empty and is populated at
// startup by internal/providers (model_sync.go init), which is the single
// source of truth for model metadata. Tests may construct their own registry
// with NewRegistry().
var Global = NewRegistry()
