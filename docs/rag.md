# RAG System in Seshat

Seshat includes a built-in Retrieval-Augmented Generation (RAG) system. Agents can ingest documents, query knowledge bases, and retrieve relevant content into their context without any external vector database or embedding service.

> Full documentation: [seshat-ai.com/docs/concepts/memory-rag](https://seshat-ai.com/docs/concepts/memory-rag)

---

## What RAG does

RAG lets an agent answer questions about a body of documents that is too large to fit in the context window. Instead of pasting the entire document, the agent retrieves only the relevant chunks.

Seshat's RAG pipeline:

1. **Ingestion** - documents are parsed, chunked, embedded and indexed.
2. **Retrieval** - at query time, the most relevant chunks are fetched using hybrid search.
3. **Augmentation** - retrieved chunks are injected into the agent's context alongside the user's question.

---

## Document ingestion

Seshat can convert rich documents before indexing through a configured document reader. The local/native readers are preferred when available; an HTTP document-reader service can be supplied through `document_reader_url` for OCR-heavy or layout-heavy formats:

| Format | Support |
|---|---|
| PDF | Full text extraction, tables, figures |
| DOCX / PPTX / XLSX | Full extraction |
| Markdown | Native |
| HTML | Via fetch |
| Audio (MP3, WAV) | Transcription via Whisper |

The embedded RAG service is used through three tools, active whenever a RAG service is attached to the client: `rag_ingest` (text into a named corpus), `rag_search` and `rag_delete`. There is no `seshat rag` command. From Go, use `rag.NewService(...)`, `Ingest` and `Search` (a complete example is in [Retrieval and knowledge](https://seshat-ai.com/en/docs/concepts/rag) on seshat-ai.com).

### Default reader

`pkg/documentreading` is the engine's default reader for a host. `Convert` / `ConvertBytes` read DOCX, PPTX and XLSX natively and PDFs page by page (`pdfsmart`): a page keeps its own text layer unless it carries an image, has almost no text, or has garbled text, and only those pages go to the optional external converter. Scans, audio and images need that converter; without one they yield no result rather than an error, so a host with nothing configured still reads every native format. External output is checked for garbled text as well. A caller that cannot accept a missed borderless table or chart (invoices, financial reports) should send those documents to the converter whole instead.

### Document-aware chunking

The core RAG service can now accept both extracted text and the original document bytes through `rag.IngestRequest.Data`. Plain text chunkers continue to split `IngestRequest.Text` as before. Chunkers that implement `rag.DocumentChunker` can use the original bytes to preserve document structure.

`rag.NewHybridDocumentChunker` provides a document-aware chunker. It calls the configured document reader's hybrid chunk path and maps returned structure onto neutral Seshat chunk metadata:

- headings;
- captions;
- page numbers;
- document item references;
- raw text when it differs from contextualized chunk text;
- token count when the backend returns it.

This path is intended for rich Knowledge ingestion, especially PDF, DOCX, PPTX, XLSX, and other structured enterprise documents. The existing paragraph and semantic chunkers remain useful fallbacks for local/simple text ingestion.

### Chunking markdown (`HeadingChunker`, `TableChunker`)

What the readers write is markdown, and `rag.HeadingChunker` (the fallback of the `structured` profile, 512 tokens at most with 64 of overlap) cuts it along its structure instead of at a fixed length:

- it reads blocks: headings (`#`, numbered outlines such as `1.2 Title`, and structural words such as Chapter/Chapitre/Capítulo/Kapitel, Article/Artigo/Artikel), paragraphs, lists, tables and fenced code. A numbered list is a list, not a series of headings, and the lines of a code block are never headings;
- a block is never cut in the middle of what it is: a paragraph is cut between sentences (the next piece starts with the last sentences of the one before, up to the profile's overlap), a list between items, a table between rows (each piece repeats the header row) and code between lines (each piece is fenced again);
- small sections are joined to their neighbours up to the size limit, their headings written in the text, so no chunk is a title and one line;
- each chunk starts with the path of headings it sits under, and carries it in `Metadata["heading_path"]` (titles only, joined with ` > `); a chunk that is only a table or only code has `chunk_type` `table` or `code`;
- a PDF's text has a marker line before each page (`documentreading.Result.PagedMarkdown`, `<!-- page 3 -->`); the chunker removes the markers from the text and gives the pages a chunk covers in `Metadata["page_numbers"]`, as a JSON array (`[3,4]`). Slides and sheets are already in the heading path (`Slide 3 > Title`, `Sheet1`);
- a heading with nothing under it is not lost: its title is in the path of what follows, or, when nothing follows it, it is written as text.

Both are public: `rag.NewHeadingChunker(profile)` and `rag.NewTableChunker(profile)`, with `rag.RecommendedChunkProfile(rag.ChunkProfileStructured)` for the profile hosts use; they are `DocumentChunker`s, so `rag.NewCachedDocumentChunker` wraps them.

`rag.TableChunker` is the same engine with every table kept apart, as its own chunk or chunks. `chunk_quality_test.go` holds both to measures on real reader output (`testdata/chunking`): no word of the source missing from the chunks, no table piece without its header, no unbalanced code fence, no chunk over the limit, and few chunks under 40 tokens; `RAG_CHUNK_CORPUS=<dir of .md files>` runs the same checks on a bigger corpus.

For repeated ingestion of the same document, wrap a document-aware chunker with `rag.NewCachedDocumentChunker`. The cache key includes the document content, filename, cache schema version, and chunker options when the chunker exposes a cache fingerprint. `rag.NewArtifactChunkCache` persists cached chunks through the runtime artifact store, while `rag.NewMemoryChunkCache` is useful for tests and short-lived local runs.

---

## Hybrid search

Seshat combines two retrieval strategies for better results than pure vector search:

| Strategy | How it works | Good for |
|---|---|---|
| **BM25** | Keyword-based, exact term matching | Named entities, code identifiers, precise terms |
| **Vector (semantic)** | Embedding similarity | Conceptual queries, paraphrase matching |

Results from both are blended, then optionally re-ranked by a reranker, before being presented to the agent. Every vector store (SQLite, HNSW, memory, pgvector, OpenSearch, Qdrant, Chroma) blends the same way, so a hybrid search ranks alike whichever one holds the corpus:

- **Candidates.** Each ranking is read to `max(100, 10 x topK)` hits (at most 500), not to `topK`. A chunk that is only fifteenth in each list is still a good answer; with 20 candidates it is lost.
- **Scores, not ranks.** Each list is divided by its best score, the vector list is weighted `1 - HybridWeight` and the keyword list `HybridWeight`, and a chunk found by both gets both. Reciprocal rank fusion was measured and gave nothing more than the keyword ranking alone at 512 and 768 token chunks.
- **A keyword hit the vectors rank low is found.** The blend does not only reorder the vector hits.

These choices come from the retrieval benchmark of SeshatOS (`seshat-intelligence/benchmarks/chunk_bench`, 192 questions on 19 documents): reading 100 candidates instead of 20 finds the answer among the first five results 3 to 4 points more often at 512 and 768 token chunks, and blending scores instead of ranks adds about 4 to 5 points of MRR there. At 256 token chunks, and on questions whose answer needs several passages, it made no difference.

**Qdrant and Chroma** have no ranked keyword search, only "which documents hold this word". Their keyword side is one lookup per word of the query (16 at most, run together, 200 documents each): a document scores the sum of the weights of the query words it holds, a word held by few documents weighing more than a word held by many. It is the IDF part of BM25, without the term frequency or the length normalisation. Qdrant answers a lookup from a full text index on the text of the points (created on the first hybrid search of a collection); Chroma has no word index, so each lookup is a case-insensitive whole-word regular expression that it evaluates on every document of the collection (or of the filter): on a large collection a hybrid search costs one scan per word.

**pgvector.** The keyword side reads a `text_search` column (a `tsvector` PostgreSQL keeps from `text`, with a GIN index; PostgreSQL 12 or later) and joins the words of the query by OR, ranked by `ts_rank_cd`. `PgVectorOptions.TextSearchConfig` is the text search configuration (`simple` by default: every word as it is, any language; `english`, `french`... stem and drop stop words of one language). PostgreSQL has no IDF, so on a large corpus OpenSearch (real BM25) ranks keywords better. A failing keyword query is an error, not a silent fallback to vector search.

---

## Vector backends

The CLI defaults to the embedded local-first path: HNSW when available, with a SQLite fallback. For larger knowledge bases or enterprise deployments, Seshat can use OpenSearch as the RAG vector store:

```bash
SESHAT_VECTOR_STORE=opensearch
OPENSEARCH_ADDRESSES=http://localhost:9200
OPENSEARCH_INDEX_PREFIX=seshat-rag
OPENSEARCH_KNN=true
OPENSEARCH_BULK_SIZE=500
```

Optional authentication:

```bash
OPENSEARCH_USERNAME=seshat
OPENSEARCH_PASSWORD=...

# or
OPENSEARCH_API_KEY=...
```

OpenSearch uses one index per Seshat namespace. Text search uses OpenSearch BM25, vector search uses `knn_vector` when `OPENSEARCH_KNN=true`, and hybrid search is fused by Seshat so ranking behavior remains consistent with the rest of the runtime. Ingestion uses the OpenSearch `_bulk` API; `OPENSEARCH_BULK_SIZE` controls how many records are sent per request and defaults to 500.

To run the optional integration test against a local or remote OpenSearch cluster:

```bash
OPENSEARCH_INTEGRATION_URL=http://localhost:9200 go test ./internal/vector -run TestOpenSearchStore -count=1
```

---

## Embedding models

Seshat uses local embedding models via [Ollama](https://ollama.com) or any OpenAI-compatible API. Configure them with the environment:

```bash
RAG_EMBEDDING_URL=http://localhost:11434
RAG_EMBEDDING_MODEL=nomic-embed-text
RAG_EMBEDDING_API_KEY=...        # not needed for Ollama
RAG_EMBEDDING_PROVIDER=ollama    # "openai" or "ollama", detected if empty
```

Models worth trying (IDs checked against OpenRouter's embeddings list, 2026-10; any OpenAI-compatible `/embeddings` endpoint works, for example `RAG_EMBEDDING_URL=https://openrouter.ai/api/v1`):

| Model | Where | Notes |
|---|---|---|
| `nomic-embed-text`, `bge-m3`, `qwen3-embedding` | Ollama (local) | Free, no key; `bge-m3` is multilingual (8K context). |
| `openai/text-embedding-3-small` / `-large` | OpenRouter, OpenAI | Cheap default / higher quality. |
| `qwen/qwen3-embedding-8b` | OpenRouter | 32K context, very cheap. |
| `google/gemini-embedding-2` | OpenRouter, Gemini | Multimodal-capable, 8K context. |
| `voyageai/voyage-4` (`-lite`, `-large`, `voyage-code-4`) | OpenRouter, Voyage | 32K context; `voyage-code-4` for code. |
| `mistralai/mistral-embed-2312` | OpenRouter, Mistral | Rejects oversized batches; lower `BatchSize`. |
| `nvidia/nemotron-3-embed-1b:free`, `liquid/lfm-2.5-embedding-350m:free` | OpenRouter (free) | Rate-limited; fine for tests. |

Changing the model changes the vector size: re-ingest a corpus after switching.

In Go, `embedder.NewFromEnv()` (package `pkg/rag/embedder`) returns an embedder, or `nil` when the variables are not set, which gives keyword-only search.

---

## Rerank models

An optional second pass reorders the retrieved chunks with a rerank model. Choose a hosted provider with `RAG_RERANK_PROVIDER` (its endpoint, request shape and default model come with it, the key is read from the provider's variable or `RAG_RERANK_API_KEY`), or point `RAG_RERANK_URL` at a self-hosted server (TEI, vLLM). `RAG_RERANK_URL`, `RAG_RERANK_MODEL` and `RAG_RERANK_API_KEY` override what the provider sets. A provider named without its key, or unknown, is skipped and the other settings apply.

| `RAG_RERANK_PROVIDER` | Models (first is the default) | Key variable |
|---|---|---|
| `cohere` | `rerank-v3.5`, `rerank-v5.0-pro`, `rerank-v5.0-fast`, `rerank-v4.0-pro`, `rerank-v4.0-fast` | `COHERE_API_KEY` |
| `voyage` | `rerank-2.5`, `rerank-2.5-lite`, `rerank-3`, `rerank-3-lite` | `VOYAGE_API_KEY` |
| `jina` | `jina-reranker-v3.5`, `jina-reranker-v3`, `jina-reranker-m0`, `jina-reranker-v2-base-multilingual` | `JINA_API_KEY` |
| `openrouter` | `cohere/rerank-v3.5`, `cohere/rerank-4-pro`, `cohere/rerank-4-fast`, `voyageai/rerank-2.5`, `voyageai/rerank-2.5-lite`, `qwen/qwen3-reranker-8b`, `nvidia/llama-nemotron-rerank-vl-1b-v2:free` | `OPENROUTER_API_KEY` |
| `langsearch` | `langsearch-reranker-v1` | `LANGSEARCH_API_KEY` |

For a free local setup, serve `BAAI/bge-reranker-v2-m3` with TEI and set `RAG_RERANK_URL=http://localhost:8080/rerank`.

---

## Agent tools

`rag_ingest`, `rag_search` and `rag_delete` are registered when a RAG service is attached (`ClientConfig.RAGService`). `rag_search` takes `corpus_id`, `query`, `top_k` (default 5), `hybrid_weight` and an optional metadata `filter`.

---

## Related docs

- [Memory and Compaction](./memory.md) - session memory and the agent memory tool
- [MCP Client](./mcp.md) - connect external knowledge servers via MCP
- [Planning Mode](./planning.md) - how the agent decides when to search vs act
- [Tools](./tools.md) - full built-in tool reference

## Reranking

A reranker is optional (`RAG_RERANK_URL`: a TEI, vLLM or Cohere-compatible `/rerank` endpoint such as `bge-reranker-v2-m3`; `RAG_RERANK_MODEL`, `RAG_RERANK_API_KEY`; `LANGSEARCH_API_KEY` for the hosted default). Nothing is required for search: without one, the retrieval order stands, and so it does when the reranker fails (a warning is logged).

When one is set, the search first checks whether the first stage is sure of its answer: when its best chunk beats the second by at least 20% of its own score (`RAG_RERANK_MARGIN`, 0.2 by default, 0 asks the reranker every time), the reranker is not asked and the retrieval order stands. Otherwise the search reads a pool of `max(topK, 10)` chunks, the reranker scores each (question, chunk) pair, the scores of the whole pool are normalised (min-max, unless they are already within 0 and 1), blended as `(1 - w) x retrieval + w x reranker` and the list is cut to `topK`. `w` is `RAG_RERANK_WEIGHT` or a request's `RerankWeight`, 0.7 by default.

Those defaults come from the retrieval benchmark of SeshatCloud (192 questions on 19 documents, hybrid bge-m3 + BM25 as the first stage, `seshat-intelligence/benchmarks/chunk_bench/rerank.py`):

| Reranker | Parameters | MRR of the hybrid retriever 0.810 becomes | Time per pair, laptop GPU |
|---|---|---|---|
| mMARCO MiniLM L12 | 118M | 0.852 (+0.043 [+0.010, +0.075]) | 68 ms |
| bge-reranker-base | 278M | 0.846 (+0.036, not different from 0) | 236 ms |
| bge-reranker-v2-m3 | 568M | 0.904 (+0.094 [+0.054, +0.137]), hit@1 0.714 to 0.865 | 819 ms |
| Jina jina-reranker-v2-base-multilingual (API, 64 of the questions) | n/a | 0.850 against 0.787 (+0.063 [+0.008, +0.122]) | 181 ms |
| Cohere rerank-v3.5 (API) | n/a | 0.917 (+0.107 [+0.062, +0.156]), alone 0.928 (+0.118), hit@1 0.714 to 0.875 | 37 ms |

- The best weight depends on the reranker: 0.5 for the two small models (the reranker alone gained nothing), equal within 0.006 of MRR for bge-reranker-v2-m3, 1.0 for Cohere rerank-v3.5 (MRR 0.909, 0.917, 0.928 at 0.5, 0.7, 1.0). Against the best weight of each of the five, 0.7 never loses more than 0.012 of MRR, 0.5 up to 0.019 and 1.0 up to 0.020: 0.7 is the default.
- A pool of 20 chunks was not better than 10 for any of them, and costs twice the time. A pool of 5 kept 83% of the gain of bge-reranker-v2-m3 on single-fact questions but none on the questions whose answer needs several passages, so the pool stays at 10.
- Skipping the reranker when the margin of the first stage is 0.2 or more lost nothing (MRR 0.902 with and without, on the single-fact questions; 0.421 against 0.425 on the multi-passage ones) and saved 24% and 8% of the pairs; a margin of 0.1 saved 54% of them and lost a fifth of the gain.
- Cheap lexical features with no model (the share of the question's words and word pairs in the chunk) added +0.02 of MRR on the single-fact questions and lost 0.004 to 0.010 on the multi-passage ones: they are not used. With no reranker, the hybrid order is the result.
- The gain is in the order of the first results (hit@1, MRR), not in whether the answer is among the first five (+0.01 to +0.03, not different from 0).
- Time per pair is that of a 4 GB laptop GPU that heats up; a server GPU is dozens of times faster. A cross-encoder of this size is a service to deploy next to the server, not something to ask a laptop for at each question.

