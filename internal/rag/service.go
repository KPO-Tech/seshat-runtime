package rag

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"

	"github.com/KPO-Tech/seshat-runtime/internal/rag/reranker"
	"github.com/KPO-Tech/seshat-runtime/internal/storage"
	"github.com/KPO-Tech/seshat-runtime/internal/vector"
)

// Reranking defaults, from the retrieval benchmark of SeshatCloud (seshat-intelligence/benchmarks/chunk_bench, rerank.py: 192
// questions on 19 documents, hybrid bge-m3 + BM25 as the first stage, three cross-encoders from 118M to 568M parameters):
//
//   - Weight 0.7. The best weight depends on the reranker: 0.5 for a small one (mMARCO MiniLM, bge-reranker-base: the reranker
//     alone gained nothing), 1.0 for the strongest (Cohere rerank-v3.5: MRR .909, .917, .928 at 0.5, 0.7, 1.0), indifferent for
//     bge-reranker-v2-m3 (equal within 0.006). Against the best weight of each of the five rerankers measured, 0.7 never loses more
//     than 0.012 of MRR, 0.5 loses up to 0.019 and 1.0 up to 0.020.
//   - A pool of max(topK, 10) chunks. Reading 20 chunks was not better than 10 for any of the three models, and costs twice
//     the time (a cross-encoder reads every pair). The first stage already puts the answer in its first ten.
//   - The score of the whole pool is normalised and blended, then the list is cut to topK: normalising only the few the
//     reranker returned changes the blend with the server in use.
//
// bge-reranker-v2-m3 moved the answering chunk up by 0.09 of MRR (hit@1 +0.15, both with a 95% interval above 0); the small
// ones by 0.03 to 0.04.
const defaultRerankWeight float32 = 0.7

const rerankMinPool = 10

// defaultRerankMargin: the reranker is not asked when the first stage is sure of its answer, that is when its best chunk beats
// the second by at least this share of its own score ((s1 - s2) / s1). On the benchmark, skipping the questions with a margin of
// 0.2 or more lost nothing (MRR 0.902 with and without the gate on the single-fact questions, 0.421 against 0.425 on the ones
// whose answer needs several passages) and saved 24% and 8% of the pairs a cross-encoder reads; a margin of 0.1 saved more and lost
// a fifth of the gain. Deeper gating (a pool of 5, a margin of 0.05) did not hold on the multi-passage questions. The margin is
// that of the scores of a hybrid search, divided by the best of each list; 0 asks the reranker for every search.
const defaultRerankMargin float32 = 0.2

type Service struct {
	artifacts    storage.ArtifactStore // optional — nil = skip rag-doc blob storage
	vectors      vector.Store
	embedder     Embedder
	chunker      Chunker
	reranker     Reranker // optional — nil = return vector results as-is
	rerankWeight float32
	rerankMargin float32
	enricher     Enricher // optional — nil = skip chunk enrichment
}

func NewService(artifacts storage.ArtifactStore, vectors vector.Store, embedder Embedder, chunker Chunker) *Service {
	if chunker == nil {
		chunker = DefaultChunker()
	}
	return &Service{
		artifacts:    artifacts,
		vectors:      vectors,
		embedder:     embedder,
		chunker:      chunker,
		rerankWeight: defaultRerankWeight,
		rerankMargin: defaultRerankMargin,
	}
}

// SetReranker installs a second-pass reranker applied after vector retrieval.
// Pass nil to disable reranking.
func (s *Service) SetReranker(r Reranker) {
	if s == nil {
		return
	}
	s.reranker = r
}

// SetEnricher installs a chunk-enrichment step run during Ingest, before
// embedding: each chunk's synthetic questions (see Enricher's doc comment)
// are appended to the text that gets embedded, not to the chunk's stored/
// displayed text. Pass nil (the default) to disable enrichment - it costs
// one LLM call per chunk, so it is never on by default.
func (s *Service) SetEnricher(e Enricher) {
	if s == nil {
		return
	}
	s.enricher = e
}

// SetRerankWeight overrides the default blend weight (0.7, see the measures at
// defaultRerankWeight) applied between the reranker's normalized score and
// each result's original retrieval score, for any SearchRequest that doesn't
// set its own RerankWeight. w is clamped to [0,1].
func (s *Service) SetRerankWeight(w float32) {
	if s == nil {
		return
	}
	switch {
	case w < 0:
		w = 0
	case w > 1:
		w = 1
	}
	s.rerankWeight = w
}

// SetRerankMargin sets the margin at or above which the first stage is trusted and the reranker is not asked (see
// defaultRerankMargin). 0 or less asks it for every search.
func (s *Service) SetRerankMargin(m float32) {
	if s == nil {
		return
	}
	if m < 0 {
		m = 0
	}
	s.rerankMargin = m
}

// firstStageIsSure reports whether the best result beats the second by at least the rerank margin of the service.
func (s *Service) firstStageIsSure(results []vector.SearchResult) bool {
	if s.rerankMargin <= 0 || len(results) < 2 {
		return false
	}
	best, second := float32(0), float32(0)
	for _, r := range results {
		switch {
		case r.Score > best:
			best, second = r.Score, best
		case r.Score > second:
			second = r.Score
		}
	}
	return best > 0 && (best-second)/best >= s.rerankMargin
}

// Vectors returns the underlying vector store, e.g. for callers that need to
// inspect raw records directly (tests, admin tooling).
func (s *Service) Vectors() vector.Store {
	if s == nil {
		return nil
	}
	return s.vectors
}

// DeleteNamespace removes all vector records for the given corpus namespace.
func (s *Service) DeleteNamespace(ctx context.Context, namespace string) error {
	if s == nil || s.vectors == nil {
		return nil
	}
	return s.vectors.DeleteNamespace(ctx, namespace)
}

// DeleteFileChunks removes artifactKey's chunk records whose position is
// keepBelow or greater - either stale chunks left over from a re-ingest
// that produced fewer chunks than its previous run (keepBelow = the new
// chunk count), or every chunk for the file when deleting it outright
// (keepBelow = 0).
//
// This lists every record actually in namespace and filters by the
// artifact_key/position metadata Ingest attaches to each chunk, rather than
// guessing an upper bound on how many old chunks might exist: an earlier
// version capped the search at a fixed ceiling past the new count (500 here,
// 2000 for rag_delete's whole-file case), so a file that shrank by more than
// that many chunks - or was simply larger than the ceiling to begin with -
// left orphaned vectors beyond it that no delete call could ever reach,
// still returned by future searches as phantom results.
func (s *Service) DeleteFileChunks(ctx context.Context, namespace, artifactKey string, keepBelow int) error {
	if s == nil || s.vectors == nil {
		return nil
	}
	all, err := s.vectors.Get(ctx, namespace, nil)
	if err != nil {
		return fmt.Errorf("list existing chunks for %q: %w", artifactKey, err)
	}
	keys := make([]string, 0)
	for _, rec := range all {
		if rec.Metadata["artifact_key"] != artifactKey {
			continue
		}
		pos, err := strconv.Atoi(rec.Metadata["position"])
		if err != nil || pos < keepBelow {
			continue
		}
		keys = append(keys, rec.Key)
	}
	if len(keys) == 0 {
		return nil
	}
	return s.vectors.DeleteKeys(ctx, namespace, keys)
}

// ArtifactKey builds the deterministic artifact key for a file within a
// corpus, matching what Ingest uses internally when FileID is set. Exported
// so callers (e.g. a delete-by-file tool) can reconstruct it without
// duplicating the format.
func ArtifactKey(corpusID, fileID string) string {
	return fmt.Sprintf("rag/%s/%s", corpusID, fileID)
}

func (s *Service) Ingest(ctx context.Context, request IngestRequest) (IngestResult, error) {
	if s == nil || s.vectors == nil {
		return IngestResult{}, fmt.Errorf("rag service is not fully configured (a vector store is required)")
	}
	if strings.TrimSpace(request.CorpusID) == "" {
		return IngestResult{}, fmt.Errorf("corpus id is required")
	}
	if strings.TrimSpace(request.Filename) == "" {
		return IngestResult{}, fmt.Errorf("filename is required")
	}
	if strings.TrimSpace(request.Text) == "" && len(request.Data) == 0 {
		return IngestResult{}, fmt.Errorf("text or data is required")
	}
	artifactPayload := []byte(request.Text)
	contentType := "text/plain"
	if len(artifactPayload) == 0 {
		artifactPayload = request.Data
		contentType = storage.DetectContentType(request.Filename)
	}

	// Build the artifact key. When FileID is provided we use a deterministic key so that
	// re-ingesting the same file produces identical chunk keys and vector Upsert replaces
	// in-place instead of creating orphaned duplicate records.
	var artifact storage.ArtifactRef
	if request.FileID != "" {
		deterministicKey := ArtifactKey(request.CorpusID, request.FileID)
		if s.artifacts != nil {
			if _, err := s.artifacts.Put(ctx, deterministicKey, artifactPayload, contentType); err != nil {
				return IngestResult{}, err
			}
		}
		artifact = storage.ArtifactRef{
			Key:  deterministicKey,
			Size: int64(len(artifactPayload)),
		}
	} else if s.artifacts != nil {
		// Fallback for callers that don't supply FileID: timestamp-based key via blob store.
		var err error
		artifact, err = storage.StoreRAGDocumentRef(ctx, s.artifacts, artifactPayload, request.Filename)
		if err != nil {
			return IngestResult{}, err
		}
	} else {
		// Synthetic ref: stable key derived from corpus + filename.
		artifact = storage.ArtifactRef{
			Key:  ArtifactKey(request.CorpusID, request.Filename),
			Size: int64(len(artifactPayload)),
		}
	}

	chunks, err := s.splitChunks(ctx, request)
	if err != nil {
		return IngestResult{}, fmt.Errorf("chunker: %w", err)
	}
	if len(chunks) == 0 {
		return IngestResult{Artifact: artifact}, nil
	}
	// Chunk enrichment (synthetic questions) runs before embedding, if
	// configured: the questions get folded into the text that is embedded,
	// so retrieval benefits from them, but never into chunk.Text itself -
	// records store and return the original, unenriched text (see
	// buildEmbeddingText).
	var embedTexts []string
	if s.enricher != nil && !request.WithoutVectors {
		plain := make([]string, len(chunks))
		for i, chunk := range chunks {
			plain[i] = chunk.Text
		}
		questionSets, err := s.enricher.EnrichChunks(ctx, plain)
		if err != nil {
			return IngestResult{}, fmt.Errorf("enricher: %w", err)
		}
		if len(questionSets) != len(chunks) {
			return IngestResult{}, fmt.Errorf("enricher returned %d results for %d chunks", len(questionSets), len(chunks))
		}
		embedTexts = make([]string, len(chunks))
		for i, chunk := range chunks {
			embedTexts[i] = buildEmbeddingText(chunk.Text, questionSets[i])
		}
	}

	// vectorsOut stays nil when there's no embedder configured - records get
	// stored without a vector (vectorless/BM25-only), see vector.Record.Vector.
	var vectorsOut [][]float32
	if s.embedder != nil && !request.WithoutVectors {
		texts := embedTexts
		if texts == nil {
			texts = make([]string, 0, len(chunks))
			for _, chunk := range chunks {
				texts = append(texts, chunk.Text)
			}
		}
		vectorsOut, err = s.embedder.EmbedTexts(ctx, texts)
		if err != nil {
			return IngestResult{}, &EmbeddingError{Err: err}
		}
		if len(vectorsOut) != len(chunks) {
			return IngestResult{}, &EmbeddingError{Err: fmt.Errorf("embedder returned %d vectors for %d chunks", len(vectorsOut), len(chunks))}
		}
	}
	records := make([]vector.Record, 0, len(chunks))
	for i, chunk := range chunks {
		key := fmt.Sprintf("%s#chunk-%d", artifact.Key, chunk.Position)
		chunk.Key = key
		var vec []float32
		if vectorsOut != nil {
			vec = vectorsOut[i]
		}
		metadata := map[string]string{
			"artifact_key": artifact.Key,
			"filename":     request.Filename,
			"position":     fmt.Sprintf("%d", chunk.Position),
		}
		for k, v := range chunk.Metadata {
			if strings.TrimSpace(k) == "" {
				continue
			}
			metadata[k] = v
		}
		if len(request.ScopeIDs) > 0 {
			// Encoded as a JSON array string - matchesFilter/pgFilterClause
			// both detect and unpack this representation transparently.
			if encoded, err := json.Marshal(request.ScopeIDs); err == nil {
				metadata["scope_id"] = string(encoded)
			}
		} else if request.ScopeID != "" {
			metadata["scope_id"] = request.ScopeID
		}
		records = append(records, vector.Record{
			Namespace: request.CorpusID,
			Key:       key,
			Text:      chunk.Text,
			Vector:    vec,
			Metadata:  metadata,
		})
	}
	if err := s.vectors.Upsert(ctx, records); err != nil {
		return IngestResult{}, err
	}

	// Re-ingesting a file that shrank (fewer chunks than its previous
	// version) would otherwise leave the old version's trailing chunks
	// behind - Upsert only replaces keys present in the new set, it can't
	// know a key from the old ingest no longer exists in the new one.
	// DeleteFileChunks finds every chunk still tagged with this
	// artifact_key at or past the new chunk count, however many there are,
	// so this is safe even when there was no previous version (the common
	// case) or the new version is the same size or longer (finds nothing).
	if request.FileID != "" {
		if err := s.DeleteFileChunks(ctx, request.CorpusID, artifact.Key, len(records)); err != nil {
			return IngestResult{}, fmt.Errorf("cleanup stale chunks: %w", err)
		}
	}

	return IngestResult{
		Artifact: artifact,
		Chunks:   len(records),
	}, nil
}

func (s *Service) splitChunks(ctx context.Context, request IngestRequest) ([]Chunk, error) {
	if documentChunker, ok := s.chunker.(DocumentChunker); ok && len(request.Data) > 0 {
		return documentChunker.SplitDocument(ctx, Document{
			Filename: request.Filename,
			Text:     request.Text,
			Data:     request.Data,
		})
	}
	return s.chunker.Split(ctx, request.Text)
}

// buildEmbeddingText appends a chunk's synthetic questions to its text for
// embedding purposes only - the returned string is never stored or returned
// to search callers, see the vector.Record built from chunk.Text in Ingest.
func buildEmbeddingText(chunkText string, questions []string) string {
	if len(questions) == 0 {
		return chunkText
	}
	var sb strings.Builder
	sb.WriteString(chunkText)
	sb.WriteString("\n\nQuestions this passage answers:\n")
	for _, q := range questions {
		sb.WriteString("- ")
		sb.WriteString(q)
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

func (s *Service) Search(ctx context.Context, request SearchRequest) (SearchResponse, error) {
	if s == nil || s.vectors == nil {
		return SearchResponse{}, fmt.Errorf("rag service is not fully configured (a vector store is required)")
	}
	if strings.TrimSpace(request.CorpusID) == "" {
		return SearchResponse{}, fmt.Errorf("corpus id is required")
	}
	if strings.TrimSpace(request.Query) == "" {
		return SearchResponse{}, fmt.Errorf("query is required")
	}

	topK := request.TopK
	if topK <= 0 {
		topK = 10
	}

	// When a reranker is active, retrieve a larger candidate pool first so the
	// reranker has more material to work with, then trim to the requested TopK.
	fetchK := topK
	useReranker := s.reranker != nil && s.reranker.IsConfigured()
	if useReranker {
		fetchK = max(topK, rerankMinPool)
	}

	// Vectorless (pure keyword/BM25) when no embedder is configured, or the
	// caller explicitly asked for pure keyword scoring - skips the embedding
	// call entirely instead of computing a query vector and discarding it.
	// Reranking still applies on top either way: it scores raw text against
	// the query, it doesn't need embeddings.
	var queryVector []float32
	if s.embedder != nil && request.HybridWeight < 1 {
		embeddings, err := s.embedder.EmbedTexts(ctx, []string{request.Query})
		switch {
		case err != nil && ctx.Err() != nil:
			return SearchResponse{}, err
		case err != nil:
			// The embedding service is busy or down (a 429 on a free plan, a network fault): the passages are still indexed as text, so the
			// search goes on by keywords instead of failing as a whole.
			log.Printf("[rag] embedding the query failed, searching by keywords only: %v", err)
		case len(embeddings) != 1:
			return SearchResponse{}, fmt.Errorf("embedder returned %d vectors for query", len(embeddings))
		default:
			queryVector = embeddings[0]
		}
	}
	results, err := s.vectors.Search(ctx, vector.Query{
		Namespace:    request.CorpusID,
		Vector:       queryVector,
		TopK:         fetchK,
		Filter:       request.Filter,
		HybridWeight: request.HybridWeight,
		QueryText:    request.Query,
	})
	if err != nil {
		return SearchResponse{}, err
	}

	// Apply reranker when configured, unless the first stage is sure of its answer. On failure, keep the retrieval order.
	if useReranker && len(results) > 0 && !s.firstStageIsSure(results) {
		texts := make([]string, 0, len(results))
		for _, r := range results {
			texts = append(texts, r.Record.Text)
		}
		indices, scores, rerankErr := s.reranker.Rerank(ctx, request.Query, texts, 0)
		if rerankErr == nil && len(indices) > 0 {
			normScores := reranker.NormalizeScores(scores)
			weight := request.RerankWeight
			if weight <= 0 {
				weight = s.rerankWeight
			}
			reranked := make([]SearchResult, 0, len(indices))
			for i, idx := range indices {
				if idx < 0 || idx >= len(results) {
					continue
				}
				normScore := float32(0)
				if i < len(normScores) {
					normScore = normScores[i]
				}
				// Blend, don't override: the original retrieval score
				// (results[idx].Score) still counts for weight (1-w), so a
				// confidently-wrong rerank call can't fully bury a strong
				// vector/BM25 match.
				blended := (1-weight)*results[idx].Score + weight*normScore
				reranked = append(reranked, SearchResult{
					Key:      results[idx].Record.Key,
					Text:     results[idx].Record.Text,
					Score:    blended,
					Metadata: results[idx].Record.Metadata,
				})
			}
			// The reranker's own index order no longer determines final
			// order once blended with the retrieval score - re-sort by the
			// blended score itself.
			sort.Slice(reranked, func(i, j int) bool { return reranked[i].Score > reranked[j].Score })
			if len(reranked) > topK {
				reranked = reranked[:topK]
			}
			return SearchResponse{CorpusID: request.CorpusID, Results: reranked}, nil
		}
		if rerankErr != nil {
			log.Printf("[rag] reranker failed, keeping the retrieval order: %v", rerankErr)
		}
		// reranker failed — fall through to vector order below
	}

	// No reranker or reranker failed: return vector results trimmed to topK.
	limit := topK
	if limit > len(results) {
		limit = len(results)
	}
	response := SearchResponse{
		CorpusID: request.CorpusID,
		Results:  make([]SearchResult, 0, limit),
	}
	for _, result := range results[:limit] {
		response.Results = append(response.Results, SearchResult{
			Key:      result.Record.Key,
			Text:     result.Record.Text,
			Score:    result.Score,
			Metadata: result.Record.Metadata,
		})
	}
	return response, nil
}
