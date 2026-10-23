package vector

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"unicode"

	dbpkg "github.com/KPO-Tech/seshat-runtime/internal/db"
)

// SQLiteStore is a persistent vector.Store backed by SQLite.
//
// Vectors are stored as raw IEEE-754 little-endian float32 BLOBs for compact
// storage and fast decode. Cosine similarity search runs in Go after loading
// the namespace's vectors from SQLite — adequate for typical RAG corpora
// (thousands of chunks). The table schema is registered as migration
// 005_rag_vector_store in internal/db/schema.go.
type SQLiteStore struct {
	db *dbpkg.DB
}

// NewSQLiteStore wraps an already-open, already-migrated DB handle.
func NewSQLiteStore(database *dbpkg.DB) (*SQLiteStore, error) {
	if database == nil {
		return nil, fmt.Errorf("database is required")
	}
	if database.Driver() != dbpkg.DriverSQLite {
		return nil, fmt.Errorf("sqlite vector store requires sqlite database, got %q", database.Driver())
	}
	return &SQLiteStore{db: database}, nil
}

// OpenSQLiteStore opens (or creates) a SQLite file at path and initializes the
// schema, returning a ready-to-use SQLiteStore.
func OpenSQLiteStore(path string) (*SQLiteStore, error) {
	database, err := dbpkg.Open(context.Background(), dbpkg.DefaultSQLiteConfig(path))
	if err != nil {
		return nil, fmt.Errorf("open sqlite vector store: %w", err)
	}
	return NewSQLiteStore(database)
}

// Close releases the underlying database connection.
func (s *SQLiteStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Upsert inserts or replaces records in the vector_records table.
func (s *SQLiteStore) Upsert(ctx context.Context, records []Record) error {
	if len(records) == 0 {
		return nil
	}
	tx, err := s.db.SQL().BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin upsert transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO vector_records (namespace, key, text, vector, metadata)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(namespace, key) DO UPDATE SET
		   text     = excluded.text,
		   vector   = excluded.vector,
		   metadata = excluded.metadata`)
	if err != nil {
		return fmt.Errorf("prepare upsert: %w", err)
	}
	defer stmt.Close()

	for _, r := range records {
		if r.Namespace == "" {
			return fmt.Errorf("vector namespace is required")
		}
		if r.Key == "" {
			return fmt.Errorf("vector key is required")
		}
		// Vector may be empty: a vectorless (BM25-only) record still needs
		// a row so the FTS5 index below can find it. encodeVector(nil)
		// produces a zero-length (not NULL) BLOB, satisfying the NOT NULL
		// column - decodeVector treats it as an empty vector, and
		// cosineSimilarity already returns 0 for empty operands, so a mixed
		// corpus (some records with vectors, some without) degrades safely
		// rather than erroring.
		blob, err := encodeVector(r.Vector)
		if err != nil {
			return fmt.Errorf("encode vector for key %q: %w", r.Key, err)
		}
		meta, err := json.Marshal(r.Metadata)
		if err != nil {
			return fmt.Errorf("encode metadata for key %q: %w", r.Key, err)
		}
		if _, err := stmt.ExecContext(ctx, r.Namespace, r.Key, r.Text, blob, string(meta)); err != nil {
			return fmt.Errorf("upsert record %q: %w", r.Key, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit upsert: %w", err)
	}

	// Sync FTS5 index (best-effort; the table may not exist on old DBs).
	for _, r := range records {
		_, _ = s.db.SQL().ExecContext(ctx,
			`DELETE FROM vector_records_fts WHERE namespace = ? AND key = ?`,
			r.Namespace, r.Key)
		_, _ = s.db.SQL().ExecContext(ctx,
			`INSERT INTO vector_records_fts(namespace, key, text) VALUES(?, ?, ?)`,
			r.Namespace, r.Key, r.Text)
	}
	return nil
}

// Search performs cosine similarity search over the given namespace.
// When query.HybridWeight > 0 and query.QueryText is set, the best BM25 hits of the
// FTS5 index and the best vector hits are blended (see blendHybrid).
// When query.Vector is empty, this is a vectorless query: it skips the
// vector scan entirely and ranks purely by FTS5 BM25 (see searchTextOnly) -
// cheaper than the vector path too, since it lets the FTS5 index do the
// work instead of loading and scoring every record in Go.
func (s *SQLiteStore) Search(ctx context.Context, query Query) ([]SearchResult, error) {
	if query.Namespace == "" {
		return nil, fmt.Errorf("vector query namespace is required")
	}
	topK := query.TopK
	if topK <= 0 {
		topK = 5
	}
	if len(query.Vector) == 0 {
		if strings.TrimSpace(query.QueryText) == "" {
			return nil, fmt.Errorf("vector query values or query text are required")
		}
		return s.searchTextOnly(ctx, query, topK)
	}

	// Load all vector records for the namespace.
	rows, err := s.db.SQL().QueryContext(ctx,
		`SELECT key, text, vector, metadata FROM vector_records WHERE namespace = ?`,
		query.Namespace)
	if err != nil {
		return nil, fmt.Errorf("query vector_records: %w", err)
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var key, text, metaJSON string
		var blob []byte
		if err := rows.Scan(&key, &text, &blob, &metaJSON); err != nil {
			return nil, fmt.Errorf("scan vector record: %w", err)
		}
		vec, err := decodeVector(blob)
		if err != nil {
			return nil, fmt.Errorf("decode vector for key %q: %w", key, err)
		}
		var meta map[string]string
		if err := json.Unmarshal([]byte(metaJSON), &meta); err != nil {
			meta = nil
		}
		r := Record{
			Namespace: query.Namespace,
			Key:       key,
			Text:      text,
			Vector:    vec,
			Metadata:  meta,
		}
		if len(query.Filter) > 0 && !matchesFilter(r, query.Filter) {
			continue
		}
		score := cosineSimilarity(query.Vector, vec)
		results = append(results, SearchResult{Record: r, Score: score})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate vector_records: %w", err)
	}

	sort.Slice(results, func(i, j int) bool { return results[i].Score > results[j].Score })

	// Hybrid: the best vector hits and the best FTS5 BM25 hits are blended (see hybrid.go). A chunk the vectors rank low but
	// the words match is found by the second list; the vector list is cut to the candidates of the search, not to topK.
	// Without an FTS5 index (an old database) the search stays a vector search.
	if hw := query.HybridWeight; hw > 0 && strings.TrimSpace(query.QueryText) != "" && len(results) > 0 {
		candidates := hybridCandidates(topK)
		if keywordResults, err := s.searchTextOnly(ctx, query, candidates); err == nil && len(keywordResults) > 0 {
			return blendHybrid(results[:min(len(results), candidates)], keywordResults, hw, topK), nil
		}
	}

	if len(results) > topK {
		results = results[:topK]
	}
	return results, nil
}

// searchTextOnly ranks purely by FTS5 BM25, with no vector involved at all -
// used for vectorless corpora (no embedder configured) and for explicit
// pure-keyword queries. bm25() scores are negative (more negative = better
// match); negated here so the returned Score follows this package's
// "higher is better" convention like every other backend's Score.
func (s *SQLiteStore) searchTextOnly(ctx context.Context, query Query, topK int) ([]SearchResult, error) {
	ftsQuery := sanitizeFTSQuery(query.QueryText)
	if ftsQuery == "" {
		return nil, nil
	}

	// Filtering happens client-side after fetch (matchesFilter, same as the
	// vector path), so over-fetch when a filter is present to leave enough
	// candidates after rows get dropped.
	fetchLimit := topK
	if len(query.Filter) > 0 && fetchLimit < 200 {
		fetchLimit = 200
	}

	rows, err := s.db.SQL().QueryContext(ctx,
		`SELECT v.key, v.text, v.metadata, bm25(vector_records_fts) AS score
		 FROM vector_records_fts f
		 JOIN vector_records v ON v.namespace = f.namespace AND v.key = f.key
		 WHERE f.namespace = ? AND vector_records_fts MATCH ?
		 ORDER BY score
		 LIMIT ?`,
		query.Namespace, ftsQuery, fetchLimit)
	if err != nil {
		return nil, fmt.Errorf("fts5 text search: %w", err)
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var key, text, metaJSON string
		var raw float64
		if err := rows.Scan(&key, &text, &metaJSON, &raw); err != nil {
			return nil, fmt.Errorf("scan fts5 result: %w", err)
		}
		var meta map[string]string
		if err := json.Unmarshal([]byte(metaJSON), &meta); err != nil {
			meta = nil
		}
		r := Record{Namespace: query.Namespace, Key: key, Text: text, Metadata: meta}
		if len(query.Filter) > 0 && !matchesFilter(r, query.Filter) {
			continue
		}
		results = append(results, SearchResult{Record: r, Score: float32(-raw)})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate fts5 results: %w", err)
	}
	if len(results) > topK {
		results = results[:topK]
	}
	return results, nil
}

// maxFTSTerms bounds the words of a text search: a pasted paragraph must not become a query of hundreds of terms.
const maxFTSTerms = 32

// sanitizeFTSQuery turns free text into an FTS5 query that finds a chunk holding any of its words.
//
// The words are cut where the index cuts them (anything that is not a letter or a digit), each is quoted so that FTS5 reads it as a
// plain word (never as AND, OR, NOT or NEAR, nor as a column filter or a prefix), and they are joined with OR. FTS5's default would
// require every word, so a question written as a sentence ("notice period for termination") found nothing as soon as one of its words,
// "for", was missing from the chunk. With OR the ranking does the work: bm25 gives a rare word a high weight and a word found in most
// chunks almost none, so a chunk that holds the rare words comes first and the filler words do not decide anything.
func sanitizeFTSQuery(q string) string {
	words := strings.FieldsFunc(q, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	seen := make(map[string]bool, len(words))
	terms := make([]string, 0, len(words))
	for _, word := range words {
		word = strings.ToLower(word)
		if seen[word] {
			continue
		}
		seen[word] = true
		terms = append(terms, `"`+word+`"`)
		if len(terms) == maxFTSTerms {
			break
		}
	}
	return strings.Join(terms, " OR ")
}

// Get retrieves records by key. If keys is nil/empty, all records in the namespace
// are returned (without their vector blobs decoded, which would be expensive).
func (s *SQLiteStore) Get(ctx context.Context, namespace string, keys []string) ([]Record, error) {
	var (
		sqlRows *sql.Rows
		err     error
	)
	if len(keys) == 0 {
		sqlRows, err = s.db.SQL().QueryContext(ctx,
			`SELECT key, text, metadata FROM vector_records WHERE namespace = ?`, namespace)
	} else {
		// Build parameterized IN clause.
		placeholders := make([]string, len(keys))
		args := make([]any, 0, len(keys)+1)
		args = append(args, namespace)
		for i, k := range keys {
			placeholders[i] = "?"
			args = append(args, k)
		}
		sqlRows, err = s.db.SQL().QueryContext(ctx,
			`SELECT key, text, metadata FROM vector_records WHERE namespace = ? AND key IN (`+
				strings.Join(placeholders, ",")+`)`,
			args...)
	}
	if err != nil {
		return nil, fmt.Errorf("get vector records: %w", err)
	}
	defer sqlRows.Close()

	var results []Record
	for sqlRows.Next() {
		var key, text, metaJSON string
		if err := sqlRows.Scan(&key, &text, &metaJSON); err != nil {
			return nil, fmt.Errorf("scan vector record: %w", err)
		}
		var meta map[string]string
		if metaJSON != "" && metaJSON != "{}" {
			if err := json.Unmarshal([]byte(metaJSON), &meta); err != nil {
				log.Printf("[vector/sqlite] metadata unmarshal warning for key %q in namespace %q: %v", key, namespace, err)
			}
		}
		results = append(results, Record{
			Namespace: namespace,
			Key:       key,
			Text:      text,
			Metadata:  meta,
		})
	}
	if err := sqlRows.Err(); err != nil {
		return nil, fmt.Errorf("iterate vector_records: %w", err)
	}
	return results, nil
}

// HasNamespace reports whether at least one record exists in the namespace.
func (s *SQLiteStore) HasNamespace(ctx context.Context, namespace string) (bool, error) {
	var exists bool
	err := s.db.SQL().QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM vector_records WHERE namespace = ? LIMIT 1)`, namespace).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("has namespace: %w", err)
	}
	return exists, nil
}

// DeleteNamespace removes all records for a namespace.
func (s *SQLiteStore) DeleteNamespace(ctx context.Context, namespace string) error {
	_, err := s.db.SQL().ExecContext(ctx,
		`DELETE FROM vector_records WHERE namespace = ?`, namespace)
	if err != nil {
		return fmt.Errorf("delete namespace %q: %w", namespace, err)
	}
	// Sync FTS5 (best-effort).
	_, _ = s.db.SQL().ExecContext(ctx,
		`DELETE FROM vector_records_fts WHERE namespace = ?`, namespace)
	return nil
}

// DeleteKeys removes specific keys within a namespace.
func (s *SQLiteStore) DeleteKeys(ctx context.Context, namespace string, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	tx, err := s.db.SQL().BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin delete transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx,
		`DELETE FROM vector_records WHERE namespace = ? AND key = ?`)
	if err != nil {
		return fmt.Errorf("prepare delete: %w", err)
	}
	defer stmt.Close()

	for _, key := range keys {
		if _, err := stmt.ExecContext(ctx, namespace, key); err != nil {
			return fmt.Errorf("delete key %q: %w", key, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit delete: %w", err)
	}
	// Sync FTS5 (best-effort; outside main transaction to avoid FTS5 locking issues).
	for _, key := range keys {
		_, _ = s.db.SQL().ExecContext(ctx,
			`DELETE FROM vector_records_fts WHERE namespace = ? AND key = ?`, namespace, key)
	}
	return nil
}

// CountNamespace returns the number of records stored under a namespace.
// Not part of Store interface — useful for tests and diagnostics.
func (s *SQLiteStore) CountNamespace(ctx context.Context, namespace string) (int, error) {
	var count int
	err := s.db.SQL().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM vector_records WHERE namespace = ?`, namespace).Scan(&count)
	if err != nil && err != sql.ErrNoRows {
		return 0, err
	}
	return count, nil
}

// encodeVector serialises []float32 as a little-endian IEEE 754 BLOB.
func encodeVector(v []float32) ([]byte, error) {
	buf := new(bytes.Buffer)
	if err := binary.Write(buf, binary.LittleEndian, v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// decodeVector deserialises a little-endian IEEE 754 BLOB into []float32.
func decodeVector(b []byte) ([]float32, error) {
	if len(b)%4 != 0 {
		return nil, fmt.Errorf("invalid vector blob length %d (not a multiple of 4)", len(b))
	}
	v := make([]float32, len(b)/4)
	if err := binary.Read(bytes.NewReader(b), binary.LittleEndian, v); err != nil {
		return nil, err
	}
	return v, nil
}
