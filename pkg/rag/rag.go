package rag

import (
	internalrag "github.com/KPO-Tech/seshat-runtime/internal/rag"
	publicreader "github.com/KPO-Tech/seshat-runtime/pkg/documentreader"
	publicstorage "github.com/KPO-Tech/seshat-runtime/pkg/storage"
	publicvector "github.com/KPO-Tech/seshat-runtime/pkg/vector"
)

type (
	// Chunk is a piece of a document as it was split for indexing: its key, its text, its position in the document and its metadata.
	Chunk = internalrag.Chunk
	// ChunkCache stores document-aware chunking outputs under a deterministic key.
	ChunkCache = internalrag.ChunkCache
	// ChunkCacheKeyProvider lets chunkers include their own options in cache keys.
	ChunkCacheKeyProvider = internalrag.ChunkCacheKeyProvider
	// ChunkProfile describes the chunking policy to use for document ingestion.
	// Token limits are intentionally conservative: large-context LLMs can read
	// more, but retrieval quality usually degrades when indexed chunks become too
	// broad or noisy.
	ChunkProfile = internalrag.ChunkProfile
	// ChunkProfileName identifies a recommended chunking profile for RAG
	// ingestion. The profile controls indexing granularity; answer-time context
	// expansion should still be used when a large LLM needs neighboring chunks.
	ChunkProfileName = internalrag.ChunkProfileName
	// Chunker splits a text into indexable chunks.
	// The context allows implementations that call remote services (e.g. SemanticChunker).
	Chunker = internalrag.Chunker
	// CachedDocumentChunker wraps a DocumentChunker with a deterministic cache.
	//
	// The cache key includes document bytes/text, filename, the cache schema
	// version, and the wrapped chunker's option fingerprint when available.
	CachedDocumentChunker = internalrag.CachedDocumentChunker
	// Document is the original source material plus its text representation.
	// Plain text chunkers can ignore Data. Document-aware chunkers can prefer Data
	// to preserve layout, headings, page numbers, tables, and captions.
	Document = internalrag.Document
	// DocumentChunker is implemented by chunkers that can split the original
	// document, not just extracted plain text.
	DocumentChunker = internalrag.DocumentChunker
	// HybridDocumentChunker chunks rich documents with a document-aware hybrid
	// chunker. The backend can be local, docling-serve, seshat-intelligence, or
	// any other document reader implementing documentreader.HybridChunker. It falls back to a
	// plain text chunker unless FailOnError is set.
	HybridDocumentChunker = internalrag.HybridDocumentChunker
	// Embedder turns texts into embedding vectors, one vector per text and in the same order.
	Embedder = internalrag.Embedder
	// Enricher generates synthetic questions per chunk - questions that chunk's
	// content would answer - so they can be indexed alongside it. This is a
	// known retrieval-recall technique: chunk text is usually declarative, but
	// users phrase queries as questions, so embedding the chunk together with
	// questions it answers closes that vocabulary gap. Optional: nil (the
	// default) disables enrichment entirely - see Service.SetEnricher. Costs one
	// LLM call per chunk, so it is opt-in, never a forced default.
	Enricher = internalrag.Enricher
	// EnrichmentCache stores per-chunk enrichment results (synthetic questions)
	// under a deterministic key - the same content-hash-keying idea as
	// ChunkCache, so a re-ingest of unchanged chunks doesn't re-bill the LLM.
	EnrichmentCache = internalrag.EnrichmentCache
	// EnricherCacheKeyProvider lets an Enricher include its own configuration
	// (model, questions-per-chunk, prompt version) in cache keys, so changing
	// that configuration doesn't silently reuse stale enrichment results - the
	// same purpose ChunkCacheKeyProvider serves for chunkers.
	EnricherCacheKeyProvider = internalrag.EnricherCacheKeyProvider
	// CachedEnricher wraps an Enricher with a deterministic cache, keyed by
	// chunk text plus the wrapped enricher's option fingerprint when available.
	CachedEnricher = internalrag.CachedEnricher
	// ArtifactEnrichmentCache persists cached enrichment results in the
	// configured artifact store.
	ArtifactEnrichmentCache = internalrag.ArtifactEnrichmentCache
	// MemoryEnrichmentCache is useful for tests and short-lived local runs.
	MemoryEnrichmentCache = internalrag.MemoryEnrichmentCache
	// IngestRequest is a document to index in a corpus: its text and, when available, its original bytes (for chunkers that read the structure of the document), its name, and a file id that makes ingesting the same file again replace its previous vectors instead of duplicating them.
	IngestRequest = internalrag.IngestRequest
	// IngestResult reports an ingestion: the artifact that holds the document and the number of chunks that were indexed.
	IngestResult = internalrag.IngestResult
	// EmbeddingError is what Ingest returns when the embedder failed, as opposed to the reading, the chunking or the storing: a host can
	// then ingest the document again with IngestRequest.WithoutVectors, so that it is found by its words. It wraps the embedder's error.
	EmbeddingError = internalrag.EmbeddingError
	// Reranker reorders a candidate set of documents by semantic relevance.
	// Implementations are optional — when nil the RAG service returns vector
	// results in score order without a second-pass rerank.
	//
	// Rerank returns (indices, scores) where indices[i] is the position of the
	// i-th most relevant document in the original docs slice. Pass topN=0 to
	// return all results.
	Reranker = internalrag.Reranker
	// SearchRequest is a query against a corpus: how many results to return (TopK), how keyword search is blended with vector similarity (HybridWeight), how the score of a reranker is blended with the retrieval score (RerankWeight), and an optional filter on the metadata.
	SearchRequest = internalrag.SearchRequest
	// SearchResponse holds the results of a search, ordered by relevance, with the corpus they come from.
	SearchResponse = internalrag.SearchResponse
	// SearchResult is a chunk found by a search: its key, text, score and metadata.
	SearchResult = internalrag.SearchResult
	// Service ingests documents into a corpus and searches it: it splits the text into chunks, embeds them and keeps them in a vector store, and keeps the original document in an artifact store when one is given. A reranker can be added to reorder the results of a search.
	Service = internalrag.Service
	// VectorStore abstracts the vector storage/search engine used by RAG.
	// Concrete implementations target SQLite, pgvector, Qdrant, Chroma, or any
	// future provider — the rag.Service and knowledge.Service call only this interface.
	VectorStore = publicvector.Store
	// ArtifactChunkCache persists cached chunks in the configured artifact store.
	ArtifactChunkCache = internalrag.ArtifactChunkCache
	// MemoryChunkCache is useful for tests and short-lived local runs.
	MemoryChunkCache = internalrag.MemoryChunkCache

	// ParagraphChunker splits on blank lines with a hard character cap per
	// chunk. The default Chunker (see DefaultChunker) when none is given.
	ParagraphChunker = internalrag.ParagraphChunker

	// SemanticChunker groups sentences into chunks by embedding-similarity
	// instead of blind paragraph splitting - meaningfully better retrieval
	// quality, at the cost of one extra embedding call per sentence during
	// ingest. Falls back to ParagraphChunker behavior if the embedder errors.
	SemanticChunker = internalrag.SemanticChunker

	// HeadingChunker cuts a markdown document (what the document readers write) along its own structure: headings,
	// paragraphs, lists, tables and fenced code. A block is never cut in the middle of what it is, small sections
	// are joined to their neighbours, and each chunk carries the path of headings it sits under in its text and in
	// Metadata["heading_path"] (and the pages it covers in Metadata["page_numbers"] when the text has page
	// markers). It is the chunker the hosts use for documents they have read natively.
	HeadingChunker = internalrag.HeadingChunker

	// TableChunker keeps GFM/Markdown table rows intact as their own
	// chunk(s), splitting an oversized table by data row while repeating
	// the header row on every piece, instead of a generic splitter cutting
	// through the middle of a table. Falls back to HeadingChunker behavior
	// for a document with no table.
	TableChunker = internalrag.TableChunker

	// QAChunker splits a question/answer-formatted document (FAQs, support
	// docs) into one chunk per Q/A pair, detected either from explicit
	// "Q:"/"A:" labels or from Markdown headings ending in "?". Falls back
	// to HeadingChunker behavior for a document with no QA structure.
	QAChunker = internalrag.QAChunker
)

const (
	ChunkProfileSmall      = internalrag.ChunkProfileSmall
	ChunkProfileMedium     = internalrag.ChunkProfileMedium
	ChunkProfileLarge      = internalrag.ChunkProfileLarge
	ChunkProfileStructured = internalrag.ChunkProfileStructured
	ChunkProfileTable      = internalrag.ChunkProfileTable
	ChunkProfileQA         = internalrag.ChunkProfileQA
	ChunkProfileCustom     = internalrag.ChunkProfileCustom
)

// NewService creates a Service. artifacts may be nil, in which case the original documents are not stored, and a nil chunker means the default chunker.
func NewService(artifacts publicstorage.ArtifactStore, vectors publicvector.Store, embedder Embedder, chunker Chunker) *Service {
	return internalrag.NewService(artifacts, vectors, embedder, chunker)
}

// DefaultChunker returns a ParagraphChunker with sensible defaults - what
// NewService uses internally when chunker is nil.
func DefaultChunker() Chunker {
	return internalrag.DefaultChunker()
}

// NewHybridDocumentChunker creates a document-aware chunker backed by the given
// reader.
func NewHybridDocumentChunker(client publicreader.HybridChunker, opts publicreader.ChunkOptions) *HybridDocumentChunker {
	return internalrag.NewHybridDocumentChunker(client, opts)
}

// NewHybridDocumentChunkerForProfile creates a document-aware chunker using one of
// Seshat's recommended chunking profiles. When the backend is unavailable
// or fails, the fallback depends on the profile: "structured" falls back to
// HeadingChunker (heading/numbering-hierarchy aware), "table" to
// TableChunker (keeps table rows intact), "qa" to QAChunker (one chunk per
// detected Q/A pair) - every other profile keeps the plain fallback
// unchanged.
func NewHybridDocumentChunkerForProfile(client publicreader.HybridChunker, profile ChunkProfile, opts publicreader.ChunkOptions) *HybridDocumentChunker {
	return internalrag.NewHybridDocumentChunkerForProfile(client, profile, opts)
}

// DefaultChunkProfile returns the default enterprise RAG profile.
func DefaultChunkProfile() ChunkProfile {
	return internalrag.DefaultChunkProfile()
}

// RecommendedChunkProfile returns one of Seshat's built-in chunk profiles.
func RecommendedChunkProfile(name ChunkProfileName) (ChunkProfile, bool) {
	return internalrag.RecommendedChunkProfile(name)
}

// NewCustomChunkProfile validates and returns a custom chunk profile.
func NewCustomChunkProfile(maxTokens, overlapTokens int) (ChunkProfile, error) {
	return internalrag.NewCustomChunkProfile(maxTokens, overlapTokens)
}

// DocumentReaderChunkOptionsForProfile maps a RAG chunk profile to hybrid
// document-reader chunk options while preserving caller-provided structural
// options.
func DocumentReaderChunkOptionsForProfile(profile ChunkProfile, opts publicreader.ChunkOptions) publicreader.ChunkOptions {
	return internalrag.DocumentReaderChunkOptionsForProfile(profile, opts)
}

// NewCachedDocumentChunker wraps chunker so that the chunks of a document that was already split are read from cache instead of being computed again.
func NewCachedDocumentChunker(chunker DocumentChunker, cache ChunkCache) *CachedDocumentChunker {
	return internalrag.NewCachedDocumentChunker(chunker, cache)
}

// NewArtifactChunkCache creates a chunk cache that keeps the chunks as artifacts in store.
func NewArtifactChunkCache(store publicstorage.ArtifactStore) *ArtifactChunkCache {
	return internalrag.NewArtifactChunkCache(store)
}

// NewMemoryChunkCache creates a chunk cache held in memory, safe for concurrent use.
func NewMemoryChunkCache() *MemoryChunkCache {
	return internalrag.NewMemoryChunkCache()
}

// DocumentChunkCacheKey returns the cache key of a document for a given chunker: a SHA-256 of the file name, the chunker key and the bytes of the document (or its text when it has no bytes).
func DocumentChunkCacheKey(doc Document, chunkerKey string) string {
	return internalrag.DocumentChunkCacheKey(doc, chunkerKey)
}

// NewCachedEnricher wraps enricher with a deterministic cache (see
// internal/rag.CachedEnricher's doc comment) so a re-ingest of unchanged
// chunks doesn't re-bill the LLM for enrichment.
func NewCachedEnricher(enricher Enricher, cache EnrichmentCache) *CachedEnricher {
	return internalrag.NewCachedEnricher(enricher, cache)
}

// NewArtifactEnrichmentCache creates a cache that keeps the generated questions of each chunk as artifacts in store.
func NewArtifactEnrichmentCache(store publicstorage.ArtifactStore) *ArtifactEnrichmentCache {
	return internalrag.NewArtifactEnrichmentCache(store)
}

// NewMemoryEnrichmentCache creates a cache of the generated questions of each chunk, held in memory and safe for concurrent use.
func NewMemoryEnrichmentCache() *MemoryEnrichmentCache {
	return internalrag.NewMemoryEnrichmentCache()
}

// ChunkEnrichmentCacheKey builds the deterministic cache key for a chunk's
// enrichment result, matching what CachedEnricher uses internally - useful
// for callers that want to pre-warm or invalidate the cache directly.
func ChunkEnrichmentCacheKey(chunkText, enricherKey string) string {
	return internalrag.ChunkEnrichmentCacheKey(chunkText, enricherKey)
}

// NewSemanticChunker creates a SemanticChunker. threshold <= 0 uses the
// default (0.3) - see SemanticChunker's doc for the tradeoff it makes.
func NewSemanticChunker(embedder Embedder, threshold float32) *SemanticChunker {
	return internalrag.NewSemanticChunker(embedder, threshold)
}

// NewHeadingChunker creates a structure-aware chunker for markdown, cutting chunks of at most profile.MaxTokens
// tokens with profile.OverlapTokens of overlap where a paragraph has to be cut. Use
// RecommendedChunkProfile(ChunkProfileStructured) for the profile hosts use.
func NewHeadingChunker(profile ChunkProfile) *HeadingChunker {
	return internalrag.NewHeadingChunker(profile)
}

// NewTableChunker creates a table-aware chunker using one of Seshat's
// recommended chunk profiles - see TableChunker's own doc comment.
func NewTableChunker(profile ChunkProfile) *TableChunker {
	return internalrag.NewTableChunker(profile)
}

// NewQAChunker creates a QA-pair-aware chunker using one of Seshat's
// recommended chunk profiles - see QAChunker's own doc comment.
func NewQAChunker(profile ChunkProfile) *QAChunker {
	return internalrag.NewQAChunker(profile)
}

// ArtifactKey builds the deterministic artifact key for a file within a
// corpus (format: "rag/{corpusID}/{fileID}"), matching what Ingest uses
// internally when IngestRequest.FileID is set. Useful for a caller building
// their own delete-by-file tooling on top of Service.DeleteFileChunks.
func ArtifactKey(corpusID, fileID string) string {
	return internalrag.ArtifactKey(corpusID, fileID)
}
