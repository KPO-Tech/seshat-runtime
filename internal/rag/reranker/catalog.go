package reranker

import "strings"

// Request shapes. The default ("") sends the fields of every known server in one
// request (documents and texts, top_n), which suits a self-hosted TEI or vLLM
// instance; a hosted provider with a strict schema gets exactly its own fields.
const (
	StyleCohere = "cohere" // {model, query, documents, top_n} -> {results: [{index, relevance_score}]}
	StyleVoyage = "voyage" // {model, query, documents, top_k} -> {data: [{index, relevance_score}]}
)

// Profile describes a hosted rerank provider: where it is, which key it takes,
// the request shape it expects and its models. It is the rerank counterpart of
// the chat catalog in internal/providers; RAG_RERANK_PROVIDER selects one.
type Profile struct {
	ID          string
	DisplayName string
	URL         string
	EnvVars     []string // environment variables that can carry the API key
	Style       string
	// Models lists the rerank models; the first is the default.
	Models []string
}

// DefaultModel returns the model used when none is configured.
func (p Profile) DefaultModel() string {
	if len(p.Models) == 0 {
		return ""
	}
	return p.Models[0]
}

// profiles is the rerank catalog, checked against each provider's own
// documentation and OpenRouter's model list (2026-10, models re-read 2026-10-30).
// The first model of a provider is its default and only changes with a measurement
// (see docs/rag.md): the newer models are offered after it.
var profiles = []Profile{
	{
		ID: "cohere", DisplayName: "Cohere", URL: "https://api.cohere.com/v2/rerank",
		EnvVars: []string{"COHERE_API_KEY", "CO_API_KEY"}, Style: StyleCohere,
		Models: []string{"rerank-v3.5", "rerank-v5.0-pro", "rerank-v5.0-fast", "rerank-v4.0-pro", "rerank-v4.0-fast"},
	},
	{
		ID: "voyage", DisplayName: "Voyage AI", URL: "https://api.voyageai.com/v1/rerank",
		EnvVars: []string{"VOYAGE_API_KEY"}, Style: StyleVoyage,
		Models: []string{"rerank-2.5", "rerank-2.5-lite", "rerank-3", "rerank-3-lite"},
	},
	{
		// Jina answers the Cohere shape ({model, query, documents, top_n} -> {results: [{index, relevance_score}]}).
		ID: "jina", DisplayName: "Jina AI", URL: "https://api.jina.ai/v1/rerank",
		EnvVars: []string{"JINA_API_KEY"}, Style: StyleCohere,
		Models: []string{"jina-reranker-v3.5", "jina-reranker-v3", "jina-reranker-m0", "jina-reranker-v2-base-multilingual"},
	},
	{
		ID: "openrouter", DisplayName: "OpenRouter", URL: "https://openrouter.ai/api/v1/rerank",
		EnvVars: []string{"OPENROUTER_API_KEY"}, Style: StyleCohere,
		Models: []string{
			"cohere/rerank-v3.5", "cohere/rerank-4-pro", "cohere/rerank-4-fast",
			"voyageai/rerank-2.5", "voyageai/rerank-2.5-lite",
			"qwen/qwen3-reranker-8b",
			"nvidia/llama-nemotron-rerank-vl-1b-v2:free",
		},
	},
	{
		ID: "langsearch", DisplayName: "LangSearch", URL: langSearchBaseURL,
		EnvVars: []string{"LANGSEARCH_API_KEY"},
		Models:  []string{langSearchModel},
	},
}

// Profiles returns the rerank providers this build knows.
func Profiles() []Profile {
	out := make([]Profile, len(profiles))
	copy(out, profiles)
	return out
}

// ProfileFor returns the profile of a provider ID, case-insensitively.
func ProfileFor(id string) (Profile, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	for _, p := range profiles {
		if p.ID == id {
			return p, true
		}
	}
	return Profile{}, false
}
