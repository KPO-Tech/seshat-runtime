package embedder

import internalembedder "github.com/KPO-Tech/seshat-runtime/internal/rag/embedder"

type (
	// Config holds the configuration for a provider-backed embedder.
	Config = internalembedder.Config
	// Embedder calls a remote embedding API to produce dense float vectors.
	Embedder = internalembedder.Embedder
	// Provider selects which embedding API format to use.
	Provider = internalembedder.Provider
	// RateLimitedError is a provider's refusal for quota. The embedder has already waited it out as far as it holds; what reaches the
	// caller carries the wait the provider asked for and whether the quota is a daily one, so that the caller can pause instead of failing.
	RateLimitedError = internalembedder.RateLimitedError
)

// New creates an Embedder from an explicit Config.
func New(cfg *Config) *Embedder {
	return internalembedder.New(cfg)
}

// FromEnv reads embedding configuration from RAG_EMBEDDING_URL /
// RAG_EMBEDDING_MODEL / RAG_EMBEDDING_API_KEY / RAG_EMBEDDING_PROVIDER.
func FromEnv() *Config {
	return internalembedder.FromEnv()
}

// NewFromEnv creates an Embedder from environment variables (see FromEnv).
// Returns nil if the required variables aren't set - check with
// Config.IsConfigured() before calling if you need to distinguish
// "not configured" from a construction failure.
func NewFromEnv() *Embedder {
	return internalembedder.NewFromEnv()
}

// DetectProviderPublic is the exported version of detectProvider for use outside this package.
func DetectProviderPublic(baseURL string) Provider {
	return internalembedder.DetectProviderPublic(baseURL)
}
