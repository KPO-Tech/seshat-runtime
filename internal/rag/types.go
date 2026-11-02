package rag

import (
	"context"

	"github.com/KPO-Tech/seshat-runtime/internal/storage"
	"github.com/KPO-Tech/seshat-runtime/internal/vector"
)

type Embedder interface {
	EmbedTexts(ctx context.Context, texts []string) ([][]float32, error)
}

// Enricher generates synthetic questions per chunk - questions that chunk's
// content would answer - so they can be indexed alongside it. This is a
// known retrieval-recall technique: chunk text is usually declarative, but
// users phrase queries as questions, so embedding the chunk together with
// questions it answers closes that vocabulary gap (see RAGFlow's own
// "Extractor" pipeline stage, which this mirrors). Optional: nil (the
// default) disables enrichment entirely - see Service.SetEnricher. Costs one
// LLM call per chunk, so it is opt-in, never a forced default.
type Enricher interface {
	// EnrichChunks returns, for each text in order, the synthetic questions
	// generated for it. Implementations should be best-effort per item (see
	// e.g. internal/rag/enricher.LLMEnricher) - a single chunk's failure
	// returns an empty, not necessarily nil, slice at that index rather than
	// failing the whole batch. A non-nil error here means something more
	// fundamental went wrong (e.g. ctx cancelled) and Ingest should abort,
	// the same contract Embedder.EmbedTexts already has.
	EnrichChunks(ctx context.Context, texts []string) ([][]string, error)
}

type Chunk struct {
	Key      string
	Text     string
	Position int
	Metadata map[string]string
}

type IngestRequest struct {
	CorpusID string
	// FileID, when non-empty, produces a deterministic artifact key ("rag/{CorpusID}/{FileID}").
	// This makes Upsert idempotent: re-ingesting the same file replaces its vectors in-place
	// rather than creating orphaned duplicates alongside the old ones.
	FileID   string
	Filename string
	Text     string
	// Data carries the original document bytes when ingestion can provide
	// them. Chunkers that understand document structure can use it instead of
	// splitting the already-extracted Text representation.
	Data []byte
	// ScopeID tags every chunk produced from this ingest with a permission
	// scope (a workspace ID for shared content, or an owning user ID for
	// personal content). Empty means unscoped — no scope_id metadata is
	// written, and the chunk is invisible to any search that filters on it.
	// Mutually exclusive with ScopeIDs - set at most one of the two.
	ScopeID string
	// ScopeIDs tags every chunk with multiple permission scopes at once
	// (e.g. a connector-synced document visible to several identities:
	// specific users, groups, a domain). Takes precedence over ScopeID when
	// both would apply from a caller's perspective - callers should set
	// only one. Encoded as a JSON array in the scope_id metadata field, so
	// a single "$in" filter transparently matches both the scalar (ScopeID)
	// and multi-valued (ScopeIDs) representations - see vector.matchesFilter
	// and pgvector_store.go's pgFilterClause.
	ScopeIDs []string
	// WithoutVectors indexes the passages for keywords only: the embedder is not called (nor the enricher, whose questions serve the
	// embedding), and the records are stored with no vector, as when no embedder is set. A host sets it for a document whose embedding
	// failed in a way that waiting does not mend, so that the document is still found by its words; it can make the vectors later.
	WithoutVectors bool
}

// EmbeddingError is what Ingest returns when the embedder failed, to tell that failure from one of reading, chunking or storing: a host
// can keep the document for keywords only. It wraps the error of the embedder, so errors.Is and errors.As reach it (a refusal for
// quota, for one), and its text is that error's.
type EmbeddingError struct{ Err error }

func (e *EmbeddingError) Error() string { return e.Err.Error() }
func (e *EmbeddingError) Unwrap() error { return e.Err }

type IngestResult struct {
	Artifact storage.ArtifactRef
	Chunks   int
}

type SearchRequest struct {
	CorpusID string
	Query    string
	TopK     int
	// HybridWeight blends vector similarity with BM25 keyword search.
	// 0 (default) = pure vector; 1 = pure BM25; intermediate = linear blend.
	HybridWeight float32
	// RerankWeight blends the (normalized) reranker score with the original
	// retrieval score, when a reranker is configured - a separate knob from
	// HybridWeight, which blends at retrieval time (vector vs BM25); this
	// one blends after retrieval (retrieval score vs rerank score). Above 0
	// and up to 1, an explicit weight (1 = fully trust the reranker). Left
	// at the zero value (0, the common case for a caller with no opinion)
	// falls back to the service's configured default (see
	// Service.SetRerankWeight) instead of silently disabling the blend - a
	// caller that genuinely wants zero reranker influence should simply not
	// configure a reranker, rather than rely on this field to suppress one.
	// Ignored when no reranker is configured.
	RerankWeight float32
	// Filter restricts results by metadata key-value predicates.
	// Passed through to vector.Query.Filter unchanged.
	Filter map[string]any
}

type SearchResult struct {
	Key      string            `json:"key"`
	Text     string            `json:"text"`
	Score    float32           `json:"score"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

type SearchResponse struct {
	CorpusID string         `json:"corpus_id"`
	Results  []SearchResult `json:"results"`
}

// Document is the original source material plus its text representation.
// Plain text chunkers can ignore Data. Document-aware chunkers can prefer Data
// to preserve layout, headings, page numbers, tables, and captions.
type Document struct {
	Filename string
	Text     string
	Data     []byte
}

// Chunker splits a text into indexable chunks.
// The context allows implementations that call remote services (e.g. SemanticChunker).
type Chunker interface {
	Split(ctx context.Context, text string) ([]Chunk, error)
}

// DocumentChunker is implemented by chunkers that can split the original
// document, not just extracted plain text.
type DocumentChunker interface {
	Chunker
	SplitDocument(ctx context.Context, doc Document) ([]Chunk, error)
}

type VectorStore = vector.Store

// Reranker reorders a candidate set of documents by semantic relevance.
// Implementations are optional — when nil the RAG service returns vector
// results in score order without a second-pass rerank.
//
// Rerank returns (indices, scores) where indices[i] is the position of the
// i-th most relevant document in the original docs slice. Pass topN=0 to
// return all results.
type Reranker interface {
	Rerank(ctx context.Context, query string, docs []string, topN int) (indices []int, scores []float32, err error)
	IsConfigured() bool
}
