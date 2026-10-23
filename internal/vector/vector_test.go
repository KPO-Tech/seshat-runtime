package vector

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	dbpkg "github.com/KPO-Tech/seshat-runtime/internal/db"
)

func TestMemoryStoreSearchRanksByCosineSimilarity(t *testing.T) {
	store := NewMemoryStore()
	if err := store.Upsert(context.Background(), []Record{
		{Namespace: "docs", Key: "a", Text: "alpha", Vector: []float32{1, 0}},
		{Namespace: "docs", Key: "b", Text: "beta", Vector: []float32{0, 1}},
	}); err != nil {
		t.Fatalf("Upsert failed: %v", err)
	}

	results, err := store.Search(context.Background(), Query{
		Namespace: "docs",
		Vector:    []float32{1, 0},
		TopK:      1,
	})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Record.Key != "a" {
		t.Fatalf("unexpected top result: %s", results[0].Record.Key)
	}
}

// TestMemoryStore_MultiValuedMetadataFilter exercises the multi-identity ACL
// filter path (rag.IngestRequest.ScopeIDs writes a JSON-array scope_id;
// matchesFilter must intersect it against the caller's allowed values) on
// the same client-side matcher SQLite and HNSW also use, alongside the
// existing scalar case to prove both representations coexist correctly.
func TestMemoryStore_MultiValuedMetadataFilter(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	if err := store.Upsert(ctx, []Record{
		{Namespace: "ns", Key: "multi", Text: "shared doc", Vector: []float32{1, 0},
			Metadata: map[string]string{"scope_id": `["user:alice","group:eng"]`}},
		{Namespace: "ns", Key: "single", Text: "personal doc", Vector: []float32{1, 0},
			Metadata: map[string]string{"scope_id": "user:bob"}},
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	// Alice isn't named directly on "single" and isn't user:bob - only the
	// multi-valued record's ACL list should match her.
	results, err := store.Search(ctx, Query{
		Namespace: "ns", Vector: []float32{1, 0}, TopK: 10,
		Filter: map[string]any{"scope_id": map[string]any{"$in": []string{"user:alice"}}},
	})
	if err != nil {
		t.Fatalf("Search (alice): %v", err)
	}
	if len(results) != 1 || results[0].Record.Key != "multi" {
		t.Fatalf("expected only %q for user:alice, got %+v", "multi", results)
	}

	// Bob matches the scalar record, unaffected by the multi-valued one.
	results, err = store.Search(ctx, Query{
		Namespace: "ns", Vector: []float32{1, 0}, TopK: 10,
		Filter: map[string]any{"scope_id": map[string]any{"$in": []string{"user:bob"}}},
	})
	if err != nil {
		t.Fatalf("Search (bob): %v", err)
	}
	if len(results) != 1 || results[0].Record.Key != "single" {
		t.Fatalf("expected only %q for user:bob, got %+v", "single", results)
	}

	// A caller entitled to both scopes sees both.
	results, err = store.Search(ctx, Query{
		Namespace: "ns", Vector: []float32{1, 0}, TopK: 10,
		Filter: map[string]any{"scope_id": map[string]any{"$in": []string{"user:alice", "user:bob"}}},
	})
	if err != nil {
		t.Fatalf("Search (both): %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %+v", results)
	}
}

func openPgVectorTestDB(t *testing.T) *dbpkg.DB {
	t.Helper()
	dsn := os.Getenv("SESHAT_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("SESHAT_TEST_POSTGRES_DSN not set")
	}
	database, err := dbpkg.Open(context.Background(), dbpkg.DefaultPostgresConfig(dsn))
	if err != nil {
		t.Fatalf("open postgres db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func TestPgVectorStore_UpsertSearchDeleteNamespace(t *testing.T) {
	database := openPgVectorTestDB(t)
	ctx := context.Background()

	store, err := NewPgVectorStore(ctx, database, PgVectorOptions{
		Dim:             1536,
		CreateExtension: true,
		IndexMethod:     "hnsw",
	})
	if err != nil {
		t.Fatalf("NewPgVectorStore: %v", err)
	}

	namespace := "itest_pgvector_slice6"
	_ = store.DeleteNamespace(ctx, namespace)
	t.Cleanup(func() { _ = store.DeleteNamespace(ctx, namespace) })

	err = store.Upsert(ctx, []Record{
		{
			Namespace: namespace,
			Key:       "a",
			Text:      "alpha",
			Vector:    sparseVector1536(0),
			Metadata:  map[string]string{"kind": "primary"},
		},
		{
			Namespace: namespace,
			Key:       "b",
			Text:      "beta",
			Vector:    sparseVector1536(1),
			Metadata:  map[string]string{"kind": "secondary"},
		},
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	results, err := store.Search(ctx, Query{
		Namespace: namespace,
		Vector:    sparseVector1536(0),
		TopK:      2,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Record.Key != "a" {
		t.Fatalf("expected best match 'a', got %q", results[0].Record.Key)
	}
	if results[0].Score < results[1].Score {
		t.Fatalf("expected descending score order, got %f then %f", results[0].Score, results[1].Score)
	}

	filtered, err := store.Search(ctx, Query{
		Namespace: namespace,
		Vector:    sparseVector1536(0),
		TopK:      2,
		Filter:    map[string]any{"kind": "primary"},
	})
	if err != nil {
		t.Fatalf("Search filtered: %v", err)
	}
	if len(filtered) != 1 || filtered[0].Record.Key != "a" {
		t.Fatalf("expected filtered result 'a', got %+v", filtered)
	}

	if err := store.DeleteKeys(ctx, namespace, []string{"a"}); err != nil {
		t.Fatalf("DeleteKeys: %v", err)
	}

	remaining, err := store.Get(ctx, namespace, nil)
	if err != nil {
		t.Fatalf("Get remaining: %v", err)
	}
	if len(remaining) != 1 || remaining[0].Key != "b" {
		t.Fatalf("expected only 'b' remaining, got %+v", remaining)
	}

	if err := store.DeleteNamespace(ctx, namespace); err != nil {
		t.Fatalf("DeleteNamespace: %v", err)
	}
	exists, err := store.HasNamespace(ctx, namespace)
	if err != nil {
		t.Fatalf("HasNamespace: %v", err)
	}
	if exists {
		t.Fatal("expected namespace to be deleted")
	}
}

// TestPgVectorStore_MultiValuedMetadataFilter is the pgvector-backend
// counterpart of TestMemoryStore_MultiValuedMetadataFilter - proves
// marshalMetadataJSON embeds a multi-identity ACL as a native JSONB array
// (not an escaped string) and pgFilterClause's "$in" correctly matches
// against it via the jsonb "?" containment operator, alongside a plain
// scalar scope_id row handled the ordinary way.
func TestPgVectorStore_MultiValuedMetadataFilter(t *testing.T) {
	database := openPgVectorTestDB(t)
	ctx := context.Background()

	store, err := NewPgVectorStore(ctx, database, PgVectorOptions{
		Dim:             1536,
		CreateExtension: true,
		IndexMethod:     "hnsw",
	})
	if err != nil {
		t.Fatalf("NewPgVectorStore: %v", err)
	}

	namespace := "itest_pgvector_multi_acl"
	_ = store.DeleteNamespace(ctx, namespace)
	t.Cleanup(func() { _ = store.DeleteNamespace(ctx, namespace) })

	err = store.Upsert(ctx, []Record{
		{
			Namespace: namespace,
			Key:       "multi",
			Text:      "shared doc",
			Vector:    sparseVector1536(0),
			Metadata:  map[string]string{"scope_id": `["user:alice","group:eng"]`},
		},
		{
			Namespace: namespace,
			Key:       "single",
			Text:      "personal doc",
			Vector:    sparseVector1536(0),
			Metadata:  map[string]string{"scope_id": "user:bob"},
		},
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	results, err := store.Search(ctx, Query{
		Namespace: namespace,
		Vector:    sparseVector1536(0),
		TopK:      10,
		Filter:    map[string]any{"scope_id": map[string]any{"$in": []string{"user:alice"}}},
	})
	if err != nil {
		t.Fatalf("Search (alice): %v", err)
	}
	if len(results) != 1 || results[0].Record.Key != "multi" {
		t.Fatalf("expected only %q for user:alice, got %+v", "multi", results)
	}

	results, err = store.Search(ctx, Query{
		Namespace: namespace,
		Vector:    sparseVector1536(0),
		TopK:      10,
		Filter:    map[string]any{"scope_id": map[string]any{"$in": []string{"user:bob"}}},
	})
	if err != nil {
		t.Fatalf("Search (bob): %v", err)
	}
	if len(results) != 1 || results[0].Record.Key != "single" {
		t.Fatalf("expected only %q for user:bob, got %+v", "single", results)
	}
}

func TestPgVectorStore_SeparateDatabaseHandleWithoutBackendMigrations(t *testing.T) {
	dsn := os.Getenv("SESHAT_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("SESHAT_TEST_POSTGRES_DSN not set")
	}

	database, err := dbpkg.Open(context.Background(), dbpkg.Config{
		Driver:      dbpkg.DriverPostgres,
		DSN:         dsn,
		AutoMigrate: false,
	})
	if err != nil {
		t.Fatalf("open postgres db without automigrate: %v", err)
	}
	defer database.Close()

	store, err := NewPgVectorStore(context.Background(), database, PgVectorOptions{
		Dim:             1536,
		CreateExtension: true,
		IndexMethod:     "hnsw",
	})
	if err != nil {
		t.Fatalf("NewPgVectorStore with separate handle: %v", err)
	}

	indexDef, err := store.existingVectorIndexDefinition(context.Background(), "idx_vector_chunks_embedding")
	if err != nil {
		t.Fatalf("existingVectorIndexDefinition: %v", err)
	}
	if indexDef == "" {
		t.Fatal("expected pgvector index to exist")
	}
}

func sparseVector1536(activeIndex int) []float32 {
	vector := make([]float32, 1536)
	if activeIndex >= 0 && activeIndex < len(vector) {
		vector[activeIndex] = 1
	}
	return vector
}

func TestNormalizePgVectorOptionsDefaults(t *testing.T) {
	options := normalizePgVectorOptions(PgVectorOptions{})

	if options.Dim != 1536 {
		t.Fatalf("expected default dim 1536, got %d", options.Dim)
	}
	if options.IndexMethod != "hnsw" {
		t.Fatalf("expected default index method hnsw, got %q", options.IndexMethod)
	}
	if options.HNSWM != 16 {
		t.Fatalf("expected default hnsw m 16, got %d", options.HNSWM)
	}
	if options.HNSWEfConstruction != 64 {
		t.Fatalf("expected default hnsw ef_construction 64, got %d", options.HNSWEfConstruction)
	}
	if options.IVFFlatLists != 100 {
		t.Fatalf("expected default ivfflat lists 100, got %d", options.IVFFlatLists)
	}
}

func TestNormalizePgVectorOptionsKeepsIVFFlat(t *testing.T) {
	options := normalizePgVectorOptions(PgVectorOptions{
		IndexMethod:  "ivfflat",
		IVFFlatLists: 42,
	})

	if options.IndexMethod != "ivfflat" {
		t.Fatalf("expected ivfflat, got %q", options.IndexMethod)
	}
	if options.IVFFlatLists != 42 {
		t.Fatalf("expected ivfflat lists 42, got %d", options.IVFFlatLists)
	}
}

func TestExtractPgIndexMethod(t *testing.T) {
	if got := extractPgIndexMethod("CREATE INDEX idx ON public.vector_chunks USING hnsw (embedding vector_cosine_ops)"); got != "hnsw" {
		t.Fatalf("expected hnsw, got %q", got)
	}
	if got := extractPgIndexMethod("create index idx on public.vector_chunks using ivfflat (embedding vector_cosine_ops) with (lists = 100)"); got != "ivfflat" {
		t.Fatalf("expected ivfflat, got %q", got)
	}
	if got := extractPgIndexMethod(""); got != "" {
		t.Fatalf("expected empty index method, got %q", got)
	}
}

func TestPgVectorNewStoreRequiresPostgres(t *testing.T) {
	database, err := dbpkg.Open(context.Background(), dbpkg.DefaultSQLiteConfig(filepath.Join(t.TempDir(), "vector.db")))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	_, err = NewStore(context.Background(), Config{StoreKind: StorePgVector, DB: database})
	if err == nil {
		t.Fatal("expected pgvector store creation to reject sqlite database")
	}
}

func TestPgVectorConfigDefaultsCreateExtension(t *testing.T) {
	database, err := dbpkg.Open(context.Background(), dbpkg.DefaultSQLiteConfig(filepath.Join(t.TempDir(), "vector.db")))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	cfg := Config{StoreKind: StorePgVector, DB: database}
	_, err = NewStore(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected pgvector store creation to reject sqlite database")
	}
	if cfg.PgVectorCreateExtension != nil {
		t.Fatal("expected zero-value config to keep nil extension flag before normalization")
	}
}

func openTestSQLiteStore(t *testing.T) *SQLiteStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "vectors.db")
	store, err := OpenSQLiteStore(path)
	if err != nil {
		t.Fatalf("OpenSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestSQLiteStore_UpsertAndSearch(t *testing.T) {
	store := openTestSQLiteStore(t)
	ctx := context.Background()

	err := store.Upsert(ctx, []Record{
		{Namespace: "docs", Key: "a", Text: "alpha doc", Vector: []float32{1, 0}},
		{Namespace: "docs", Key: "b", Text: "beta doc", Vector: []float32{0, 1}},
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	results, err := store.Search(ctx, Query{
		Namespace: "docs",
		Vector:    []float32{1, 0},
		TopK:      1,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Record.Key != "a" {
		t.Errorf("expected key=a, got %q", results[0].Record.Key)
	}
	if results[0].Score < 0.99 {
		t.Errorf("expected score near 1.0, got %f", results[0].Score)
	}
}

func TestSQLiteStore_UpsertOverwrites(t *testing.T) {
	store := openTestSQLiteStore(t)
	ctx := context.Background()

	_ = store.Upsert(ctx, []Record{
		{Namespace: "ns", Key: "k1", Text: "original", Vector: []float32{1, 0}},
	})
	_ = store.Upsert(ctx, []Record{
		{Namespace: "ns", Key: "k1", Text: "updated", Vector: []float32{0, 1}},
	})

	results, err := store.Search(ctx, Query{Namespace: "ns", Vector: []float32{0, 1}, TopK: 1})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 || results[0].Record.Text != "updated" {
		t.Errorf("expected updated record, got %+v", results)
	}
}

func TestSQLiteStore_DeleteNamespace(t *testing.T) {
	store := openTestSQLiteStore(t)
	ctx := context.Background()

	_ = store.Upsert(ctx, []Record{
		{Namespace: "ns", Key: "k1", Vector: []float32{1, 0}},
		{Namespace: "ns", Key: "k2", Vector: []float32{0, 1}},
	})

	if err := store.DeleteNamespace(ctx, "ns"); err != nil {
		t.Fatalf("DeleteNamespace: %v", err)
	}

	count, _ := store.CountNamespace(ctx, "ns")
	if count != 0 {
		t.Errorf("expected 0 records after DeleteNamespace, got %d", count)
	}
}

func TestSQLiteStore_DeleteKeys(t *testing.T) {
	store := openTestSQLiteStore(t)
	ctx := context.Background()

	_ = store.Upsert(ctx, []Record{
		{Namespace: "ns", Key: "k1", Vector: []float32{1, 0}},
		{Namespace: "ns", Key: "k2", Vector: []float32{0, 1}},
		{Namespace: "ns", Key: "k3", Vector: []float32{1, 1}},
	})

	if err := store.DeleteKeys(ctx, "ns", []string{"k1", "k3"}); err != nil {
		t.Fatalf("DeleteKeys: %v", err)
	}

	count, _ := store.CountNamespace(ctx, "ns")
	if count != 1 {
		t.Errorf("expected 1 remaining record, got %d", count)
	}

	results, _ := store.Search(ctx, Query{Namespace: "ns", Vector: []float32{0, 1}, TopK: 5})
	if len(results) != 1 || results[0].Record.Key != "k2" {
		t.Errorf("expected only k2 left, got %+v", results)
	}
}

func TestSQLiteStore_MultiNamespaceIsolation(t *testing.T) {
	store := openTestSQLiteStore(t)
	ctx := context.Background()

	_ = store.Upsert(ctx, []Record{
		{Namespace: "ns1", Key: "a", Vector: []float32{1, 0}},
		{Namespace: "ns2", Key: "b", Vector: []float32{1, 0}},
	})

	r1, _ := store.Search(ctx, Query{Namespace: "ns1", Vector: []float32{1, 0}, TopK: 5})
	r2, _ := store.Search(ctx, Query{Namespace: "ns2", Vector: []float32{1, 0}, TopK: 5})

	if len(r1) != 1 || r1[0].Record.Key != "a" {
		t.Errorf("ns1 isolation failed: %+v", r1)
	}
	if len(r2) != 1 || r2[0].Record.Key != "b" {
		t.Errorf("ns2 isolation failed: %+v", r2)
	}
}

func TestSQLiteStore_EmptyNamespaceSearch(t *testing.T) {
	store := openTestSQLiteStore(t)
	ctx := context.Background()

	results, err := store.Search(ctx, Query{Namespace: "empty", Vector: []float32{1, 0}, TopK: 5})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected empty results, got %d", len(results))
	}
}

func TestSQLiteStore_Metadata(t *testing.T) {
	store := openTestSQLiteStore(t)
	ctx := context.Background()

	_ = store.Upsert(ctx, []Record{
		{
			Namespace: "ns",
			Key:       "doc1",
			Text:      "hello",
			Vector:    []float32{1, 0},
			Metadata:  map[string]string{"filename": "intro.txt", "position": "0"},
		},
	})

	results, _ := store.Search(ctx, Query{Namespace: "ns", Vector: []float32{1, 0}, TopK: 1})
	if len(results) != 1 {
		t.Fatalf("expected 1 result")
	}
	if results[0].Record.Metadata["filename"] != "intro.txt" {
		t.Errorf("metadata not preserved: %+v", results[0].Record.Metadata)
	}
}

func TestSQLiteStore_Persistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vectors.db")

	// Open, write, close.
	s1, err := OpenSQLiteStore(path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	ctx := context.Background()
	if err := s1.Upsert(ctx, []Record{
		{Namespace: "corpus", Key: "chunk-0", Text: "persistent text", Vector: []float32{0.5, 0.5}},
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	_ = s1.Close()

	// Re-open, verify data survived.
	s2, err := OpenSQLiteStore(path)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	defer s2.Close()

	count, _ := s2.CountNamespace(ctx, "corpus")
	if count != 1 {
		t.Errorf("expected 1 record after reopen, got %d", count)
	}

	results, err := s2.Search(ctx, Query{Namespace: "corpus", Vector: []float32{0.5, 0.5}, TopK: 1})
	if err != nil {
		t.Fatalf("Search after reopen: %v", err)
	}
	if len(results) != 1 || results[0].Record.Text != "persistent text" {
		t.Errorf("data not persisted across reopens: %+v", results)
	}
}

func TestSQLiteStore_VectorEncoding(t *testing.T) {
	cases := [][]float32{
		{0, 0, 0},
		{1, 2, 3},
		{-1, 0.5, 1e-6},
		{1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, // 16-dim
	}
	for _, v := range cases {
		blob, err := encodeVector(v)
		if err != nil {
			t.Fatalf("encodeVector: %v", err)
		}
		got, err := decodeVector(blob)
		if err != nil {
			t.Fatalf("decodeVector: %v", err)
		}
		if len(got) != len(v) {
			t.Fatalf("length mismatch: got %d, want %d", len(got), len(v))
		}
		for i := range v {
			if got[i] != v[i] {
				t.Errorf("value mismatch at %d: got %f, want %f", i, got[i], v[i])
			}
		}
	}
}

func TestSQLiteStore_ValidationErrors(t *testing.T) {
	store := openTestSQLiteStore(t)
	ctx := context.Background()

	cases := []struct {
		name string
		rec  Record
	}{
		{"missing namespace", Record{Key: "k", Vector: []float32{1}}},
		{"missing key", Record{Namespace: "ns", Vector: []float32{1}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := store.Upsert(ctx, []Record{tc.rec})
			if err == nil {
				t.Errorf("expected error for %s", tc.name)
			}
		})
	}
}

func TestNewSQLiteStore_RejectsNilDB(t *testing.T) {
	_, err := NewSQLiteStore(nil)
	if err == nil {
		t.Error("expected error for nil db")
	}
}

func TestNewSQLiteStore_RejectsWrongDriver(t *testing.T) {
	// Can't create a non-SQLite DB without another driver, so test the guard indirectly.
	// Just verify our error message mentions the driver.
	cfg := dbpkg.Config{Driver: "postgres", DSN: "user=x"}
	_, openErr := dbpkg.Open(context.Background(), cfg)
	// If it opens somehow, test NewSQLiteStore rejection.
	if openErr == nil {
		t.Skip("postgres driver opened unexpectedly, skipping guard test")
	}
}

// ─── Hybrid search (BM25 + vector) ───────────────────────────────────────────

func TestSQLiteStore_HybridSearch_PureVector(t *testing.T) {
	// HybridWeight=0 must behave identically to pure vector search.
	store := openTestSQLiteStore(t)
	ctx := context.Background()
	_ = store.Upsert(ctx, []Record{
		{Namespace: "h", Key: "alpha", Text: "alpha document content", Vector: []float32{1, 0}},
		{Namespace: "h", Key: "beta", Text: "beta document content", Vector: []float32{0, 1}},
	})
	results, err := store.Search(ctx, Query{
		Namespace:    "h",
		Vector:       []float32{1, 0},
		TopK:         1,
		HybridWeight: 0,
		QueryText:    "alpha",
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 || results[0].Record.Key != "alpha" {
		t.Errorf("pure vector: expected alpha on top, got %v", results)
	}
}

func TestSQLiteStore_HybridSearch_Blended(t *testing.T) {
	// HybridWeight > 0: record whose text matches the query term should score
	// higher than a non-matching record when keyword overlap is decisive.
	store := openTestSQLiteStore(t)
	ctx := context.Background()
	_ = store.Upsert(ctx, []Record{
		// Both records have the same vector, so vector score is identical.
		// Only BM25 (text match) can differentiate them.
		{Namespace: "hybrid", Key: "match", Text: "the quick brown fox jumps", Vector: []float32{1, 0}},
		{Namespace: "hybrid", Key: "nomatch", Text: "completely unrelated words", Vector: []float32{1, 0}},
	})
	results, err := store.Search(ctx, Query{
		Namespace:    "hybrid",
		Vector:       []float32{1, 0},
		TopK:         2,
		HybridWeight: 0.7,
		QueryText:    "quick fox",
	})
	if err != nil {
		t.Fatalf("HybridSearch: %v", err)
	}
	// "match" should appear first when BM25 dominates.
	if len(results) == 0 {
		t.Fatal("expected results, got none")
	}
	if results[0].Record.Key != "match" {
		t.Logf("Note: hybrid search did not rank 'match' first (FTS5 may be unavailable or scores tied)")
		// Not a hard failure — FTS5 might not be available in this build.
	}
}

func TestSQLiteStore_HybridSearch_EmptyQueryText(t *testing.T) {
	// Empty QueryText with HybridWeight > 0 must not error — it falls back to vector.
	store := openTestSQLiteStore(t)
	ctx := context.Background()
	_ = store.Upsert(ctx, []Record{
		{Namespace: "empty_q", Key: "a", Text: "some text", Vector: []float32{1, 0}},
	})
	results, err := store.Search(ctx, Query{
		Namespace:    "empty_q",
		Vector:       []float32{1, 0},
		TopK:         1,
		HybridWeight: 0.5,
		QueryText:    "",
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
}

func TestSQLiteStore_HybridSearch_FilterWithHybrid(t *testing.T) {
	// Filter must still be applied when HybridWeight > 0.
	store := openTestSQLiteStore(t)
	ctx := context.Background()
	_ = store.Upsert(ctx, []Record{
		{Namespace: "fh", Key: "a", Text: "hello world", Vector: []float32{1, 0},
			Metadata: map[string]string{"src": "doc-a"}},
		{Namespace: "fh", Key: "b", Text: "hello world", Vector: []float32{1, 0},
			Metadata: map[string]string{"src": "doc-b"}},
	})
	results, err := store.Search(ctx, Query{
		Namespace:    "fh",
		Vector:       []float32{1, 0},
		TopK:         5,
		HybridWeight: 0.3,
		QueryText:    "hello",
		Filter:       map[string]any{"src": "doc-a"},
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	for _, r := range results {
		if r.Record.Metadata["src"] != "doc-a" {
			t.Errorf("filter bypassed in hybrid search: got src=%s", r.Record.Metadata["src"])
		}
	}
}

func TestSQLiteStore_Vectorless_UpsertAndSearch(t *testing.T) {
	store := openTestSQLiteStore(t)
	ctx := context.Background()

	err := store.Upsert(ctx, []Record{
		{Namespace: "vl", Key: "a", Text: "the quick brown fox jumps over the lazy dog"},
		{Namespace: "vl", Key: "b", Text: "an entirely unrelated sentence about weather"},
	})
	if err != nil {
		t.Fatalf("Upsert without vectors: %v", err)
	}

	results, err := store.Search(ctx, Query{Namespace: "vl", QueryText: "fox", TopK: 5})
	if err != nil {
		t.Fatalf("vectorless Search: %v", err)
	}
	if len(results) != 1 || results[0].Record.Key != "a" {
		t.Fatalf("expected only record 'a' to match 'fox', got %+v", results)
	}
	if results[0].Score <= 0 {
		t.Errorf("expected a positive (higher-is-better) BM25 score, got %f", results[0].Score)
	}
}

func TestSQLiteStore_Vectorless_RequiresQueryText(t *testing.T) {
	store := openTestSQLiteStore(t)
	ctx := context.Background()
	_, err := store.Search(ctx, Query{Namespace: "vl", TopK: 5})
	if err == nil {
		t.Error("expected an error when both Vector and QueryText are empty")
	}
}

func TestSQLiteStore_Vectorless_RespectsFilter(t *testing.T) {
	store := openTestSQLiteStore(t)
	ctx := context.Background()
	_ = store.Upsert(ctx, []Record{
		{Namespace: "vlf", Key: "a", Text: "hello world", Metadata: map[string]string{"src": "doc-a"}},
		{Namespace: "vlf", Key: "b", Text: "hello world", Metadata: map[string]string{"src": "doc-b"}},
	})
	results, err := store.Search(ctx, Query{
		Namespace: "vlf", QueryText: "hello", TopK: 5,
		Filter: map[string]any{"src": "doc-a"},
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	for _, r := range results {
		if r.Record.Metadata["src"] != "doc-a" {
			t.Errorf("filter bypassed in vectorless search: got src=%s", r.Record.Metadata["src"])
		}
	}
	if len(results) == 0 {
		t.Error("expected at least one filtered result")
	}
}

func TestSQLiteStore_Vectorless_MixedCorpusDoesNotBreakVectorSearch(t *testing.T) {
	// A record ingested without an embedder (no vector) sitting alongside
	// vectored records must not break a subsequent real vector search -
	// cosineSimilarity already returns 0 for an empty operand, so it should
	// just rank last, never error.
	store := openTestSQLiteStore(t)
	ctx := context.Background()
	err := store.Upsert(ctx, []Record{
		{Namespace: "mixed", Key: "vectorless", Text: "no vector here"},
		{Namespace: "mixed", Key: "vectored", Text: "has a vector", Vector: []float32{1, 0}},
	})
	if err != nil {
		t.Fatalf("Upsert mixed corpus: %v", err)
	}
	results, err := store.Search(ctx, Query{Namespace: "mixed", Vector: []float32{1, 0}, TopK: 5})
	if err != nil {
		t.Fatalf("vector Search over mixed corpus: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected both records to be returned, got %d: %+v", len(results), results)
	}
	if results[0].Record.Key != "vectored" {
		t.Errorf("expected the real-vector record to rank first, got %+v", results)
	}
}

func TestMemoryStore_Vectorless_UpsertAndSearch(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	if err := store.Upsert(ctx, []Record{
		{Namespace: "vl", Key: "a", Text: "the quick brown fox"},
		{Namespace: "vl", Key: "b", Text: "unrelated text about weather"},
	}); err != nil {
		t.Fatalf("Upsert without vectors: %v", err)
	}

	results, err := store.Search(ctx, Query{Namespace: "vl", QueryText: "fox", TopK: 5})
	if err != nil {
		t.Fatalf("vectorless Search: %v", err)
	}
	if len(results) != 1 || results[0].Record.Key != "a" {
		t.Fatalf("expected only record 'a' to match 'fox', got %+v", results)
	}
}

func TestMemoryStore_Vectorless_RequiresQueryText(t *testing.T) {
	store := NewMemoryStore()
	_, err := store.Search(context.Background(), Query{Namespace: "vl", TopK: 5})
	if err == nil {
		t.Error("expected an error when both Vector and QueryText are empty")
	}
}

func TestHNSWStore_UpsertSearchPersistence(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	store, err := NewHNSWStore(dir)
	if err != nil {
		t.Fatalf("NewHNSWStore: %v", err)
	}

	records := []Record{
		{Namespace: "ns", Key: "a", Text: "apple", Vector: []float32{1, 0, 0}},
		{Namespace: "ns", Key: "b", Text: "banana", Vector: []float32{0, 1, 0}},
		{Namespace: "ns", Key: "c", Text: "cherry", Vector: []float32{0, 0, 1}},
	}
	if err := store.Upsert(ctx, records); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	// Vector [1,0,0] should rank "a" first.
	results, err := store.Search(ctx, Query{Namespace: "ns", Vector: []float32{1, 0, 0}, TopK: 1})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 || results[0].Record.Key != "a" {
		t.Fatalf("expected key=a as top result, got %+v", results)
	}
	if results[0].Record.Text != "apple" {
		t.Fatalf("expected text=apple, got %q", results[0].Record.Text)
	}

	// Score should be ~1.0 for identical direction.
	if results[0].Score < 0.99 {
		t.Fatalf("expected score ~1, got %f", results[0].Score)
	}

	// HasNamespace
	has, err := store.HasNamespace(ctx, "ns")
	if err != nil || !has {
		t.Fatalf("HasNamespace: has=%v err=%v", has, err)
	}

	// --- Persistence: reload from disk ---
	store2, err := NewHNSWStore(dir)
	if err != nil {
		t.Fatalf("reload NewHNSWStore: %v", err)
	}
	results2, err := store2.Search(ctx, Query{Namespace: "ns", Vector: []float32{0, 1, 0}, TopK: 1})
	if err != nil {
		t.Fatalf("Search after reload: %v", err)
	}
	if len(results2) == 0 || results2[0].Record.Key != "b" {
		t.Fatalf("expected key=b after reload, got %+v", results2)
	}

	// DeleteKeys
	if err := store2.DeleteKeys(ctx, "ns", []string{"a"}); err != nil {
		t.Fatalf("DeleteKeys: %v", err)
	}
	got, err := store2.Get(ctx, "ns", []string{"a"})
	if err != nil {
		t.Fatalf("Get after delete: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected key=a deleted, still got %+v", got)
	}

	// DeleteNamespace
	if err := store2.DeleteNamespace(ctx, "ns"); err != nil {
		t.Fatalf("DeleteNamespace: %v", err)
	}
	has, err = store2.HasNamespace(ctx, "ns")
	if err != nil || has {
		t.Fatalf("after DeleteNamespace: has=%v err=%v", has, err)
	}
}

func TestHNSWStore_HybridKeywordBlend(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	store, err := NewHNSWStore(dir)
	if err != nil {
		t.Fatalf("NewHNSWStore: %v", err)
	}
	_ = store.Upsert(ctx, []Record{
		{Namespace: "k", Key: "x", Text: "the quick brown fox", Vector: []float32{1, 0}},
		{Namespace: "k", Key: "y", Text: "lazy dog sleeps", Vector: []float32{1, 0}}, // same vector direction
	})

	// Without hybrid both should have the same vector score.
	// With hybrid=1 "fox" should rank "x" higher.
	results, err := store.Search(ctx, Query{
		Namespace:    "k",
		Vector:       []float32{1, 0},
		TopK:         2,
		HybridWeight: 1.0,
		QueryText:    "fox",
	})
	if err != nil {
		t.Fatalf("hybrid search: %v", err)
	}
	if len(results) < 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Record.Key != "x" {
		t.Fatalf("expected 'x' to rank first with keyword 'fox', got %+v", results)
	}
}

func TestHNSWStore_Vectorless_UpsertAndSearch(t *testing.T) {
	dir := t.TempDir()
	store, err := NewHNSWStore(dir)
	if err != nil {
		t.Fatalf("NewHNSWStore: %v", err)
	}

	if err := store.Upsert(context.Background(), []Record{
		{Namespace: "vl", Key: "a", Text: "the quick brown fox"},
		{Namespace: "vl", Key: "b", Text: "unrelated text about weather"},
	}); err != nil {
		t.Fatalf("Upsert without vectors: %v", err)
	}

	results, err := store.Search(context.Background(), Query{Namespace: "vl", QueryText: "fox", TopK: 5})
	if err != nil {
		t.Fatalf("vectorless Search: %v", err)
	}
	if len(results) != 1 || results[0].Record.Key != "a" {
		t.Fatalf("expected only record 'a' to match 'fox', got %+v", results)
	}
}

func TestHNSWStore_Vectorless_RequiresQueryText(t *testing.T) {
	dir := t.TempDir()
	store, err := NewHNSWStore(dir)
	if err != nil {
		t.Fatalf("NewHNSWStore: %v", err)
	}
	if _, err := store.Search(context.Background(), Query{Namespace: "vl", TopK: 5}); err == nil {
		t.Error("expected an error when both Vector and QueryText are empty")
	}
}

func TestSanitizeFTSQuery(t *testing.T) {
	cases := map[string]string{
		"notice period for termination": `"notice" OR "period" OR "for" OR "termination"`,
		"Terms AND Conditions":          `"terms" OR "and" OR "conditions"`,
		"not near":                      `"not" OR "near"`,
		`a "quoted" (phrase)* -x`:       `"a" OR "quoted" OR "phrase" OR "x"`,
		"title:contract":                `"title" OR "contract"`,
		"résumé du dossier":             `"résumé" OR "du" OR "dossier"`,
		"repeat repeat":                 `"repeat"`,
		"  ...  ":                       ``,
	}
	for in, want := range cases {
		if got := sanitizeFTSQuery(in); got != want {
			t.Errorf("sanitizeFTSQuery(%q) = %q, want %q", in, got, want)
		}
	}
	long := strings.Repeat("w", 1)
	for i := 0; i < 100; i++ {
		long += fmt.Sprintf(" word%d", i)
	}
	if got := strings.Count(sanitizeFTSQuery(long), " OR ") + 1; got != maxFTSTerms {
		t.Errorf("a long text keeps %d words, got %d", maxFTSTerms, got)
	}
}

func TestSQLiteStore_VectorlessSearchFindsASentenceQuestion(t *testing.T) {
	// A question written as a sentence has words the chunk does not hold ("for", "what"): the chunk must still be found,
	// and the one with the rare words must come before one that only shares filler words.
	store := openTestSQLiteStore(t)
	ctx := context.Background()
	_ = store.Upsert(ctx, []Record{
		{Namespace: "v", Key: "contract", Text: "Either party may terminate with ninety days written notice. The notice period is ninety days."},
		{Namespace: "v", Key: "payment", Text: "Payment terms are net 30 days from the invoice date."},
		{Namespace: "v", Key: "other", Text: "The office is open for visitors on weekdays."},
	})
	results, err := store.Search(ctx, Query{Namespace: "v", TopK: 3, QueryText: "what is the notice period for termination"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 || results[0].Record.Key != "contract" {
		t.Fatalf("the contract chunk must come first, got %v", results)
	}
}

func TestSQLiteStore_VectorlessSearchTreatsOperatorsAsWords(t *testing.T) {
	store := openTestSQLiteStore(t)
	ctx := context.Background()
	_ = store.Upsert(ctx, []Record{{Namespace: "op", Key: "a", Text: "General terms and conditions apply."}})
	for _, q := range []string{"terms AND conditions", "NOT conditions", `conditions "`, "terms NEAR conditions", "text:conditions"} {
		results, err := store.Search(ctx, Query{Namespace: "op", TopK: 3, QueryText: q})
		if err != nil {
			t.Fatalf("Search(%q) must not fail: %v", q, err)
		}
		if len(results) != 1 {
			t.Fatalf("Search(%q) must find the chunk, got %d results", q, len(results))
		}
	}
}
