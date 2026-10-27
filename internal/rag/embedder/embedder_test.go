package embedder

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestOpenAIEmbedder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" || r.Method != http.MethodPost {
			http.Error(w, "unexpected path/method", http.StatusBadRequest)
			return
		}
		var req struct {
			Input []string `json:"input"`
			Model string   `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		data := make([]map[string]any, len(req.Input))
		for i := range req.Input {
			data[i] = map[string]any{"embedding": []float32{0.1 * float32(i+1), 0.2, 0.3}}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer srv.Close()

	e := New(&Config{BaseURL: srv.URL, Model: "test-model", Provider: ProviderOpenAI})
	vecs, err := e.EmbedTexts(context.Background(), []string{"hello", "world"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vecs) != 2 {
		t.Fatalf("expected 2 vectors, got %d", len(vecs))
	}
	if vecs[0][0] != 0.1 || vecs[1][0] != 0.2 {
		t.Errorf("unexpected vector values: %v, %v", vecs[0], vecs[1])
	}
}

func TestOllamaEmbedder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" || r.Method != http.MethodPost {
			http.Error(w, "unexpected path/method", http.StatusBadRequest)
			return
		}
		var req struct {
			Input    []string `json:"input"`
			Model    string   `json:"model"`
			Truncate bool     `json:"truncate"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		if !req.Truncate {
			http.Error(w, "expected truncate=true", http.StatusBadRequest)
			return
		}
		embeddings := make([][]float32, len(req.Input))
		for i := range req.Input {
			embeddings[i] = []float32{float32(i) * 0.5, 1.0}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": embeddings})
	}))
	defer srv.Close()

	e := New(&Config{BaseURL: srv.URL, Model: "nomic-embed-text", Provider: ProviderOllama})
	vecs, err := e.EmbedTexts(context.Background(), []string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vecs) != 3 {
		t.Fatalf("expected 3 vectors, got %d", len(vecs))
	}
}

func TestEmbedTextsEmpty(t *testing.T) {
	e := New(&Config{BaseURL: "http://localhost:9999", Model: "m", Provider: ProviderOpenAI})
	vecs, err := e.EmbedTexts(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error on empty input: %v", err)
	}
	if len(vecs) != 0 {
		t.Fatalf("expected no vectors, got %d", len(vecs))
	}
}

func TestDetectProvider(t *testing.T) {
	cases := []struct {
		url  string
		want Provider
	}{
		{"http://localhost:11434", ProviderOllama},
		{"http://192.168.1.1:11434/api", ProviderOllama},
		{"https://api.openai.com/v1", ProviderOpenAI},
		{"http://my-llm-proxy.internal/v1", ProviderOpenAI},
	}
	for _, tc := range cases {
		got := detectProvider(tc.url)
		if got != tc.want {
			t.Errorf("detectProvider(%q) = %q, want %q", tc.url, got, tc.want)
		}
	}
}

func TestFromEnv(t *testing.T) {
	t.Setenv("RAG_EMBEDDING_URL", "http://localhost:11434")
	t.Setenv("RAG_EMBEDDING_MODEL", "nomic-embed-text")
	t.Setenv("RAG_EMBEDDING_API_KEY", "")
	t.Setenv("RAG_EMBEDDING_PROVIDER", "")

	cfg := FromEnv()
	if cfg.BaseURL != "http://localhost:11434" {
		t.Errorf("BaseURL = %q", cfg.BaseURL)
	}
	if cfg.Model != "nomic-embed-text" {
		t.Errorf("Model = %q", cfg.Model)
	}
	if cfg.Provider != ProviderOllama {
		t.Errorf("Provider = %q (expected ollama auto-detect)", cfg.Provider)
	}
}

func TestFromEnvExplicitProvider(t *testing.T) {
	t.Setenv("RAG_EMBEDDING_URL", "https://api.openai.com/v1")
	t.Setenv("RAG_EMBEDDING_MODEL", "text-embedding-3-small")
	t.Setenv("RAG_EMBEDDING_PROVIDER", "openai")

	cfg := FromEnv()
	if cfg.Provider != ProviderOpenAI {
		t.Errorf("Provider = %q, want openai", cfg.Provider)
	}
}

func TestNewFromEnvNilWhenUnconfigured(t *testing.T) {
	os.Unsetenv("RAG_EMBEDDING_URL")
	os.Unsetenv("RAG_EMBEDDING_MODEL")
	e := NewFromEnv()
	if e != nil {
		t.Errorf("expected nil when env vars not set, got %v", e)
	}
}

func TestConfigIsConfigured(t *testing.T) {
	cases := []struct {
		cfg  *Config
		want bool
	}{
		{nil, false},
		{&Config{}, false},
		{&Config{BaseURL: "http://x"}, false},
		{&Config{Model: "m"}, false},
		{&Config{BaseURL: "http://x", Model: "m"}, true},
	}
	for _, tc := range cases {
		if got := tc.cfg.IsConfigured(); got != tc.want {
			t.Errorf("IsConfigured(%+v) = %v, want %v", tc.cfg, got, tc.want)
		}
	}
}

// A provider that refuses a request holding too many tokens must not fail a document whose chunks are dense: the group is sent again
// in halves until it fits, and the vectors come back in the order of the texts.
func TestEmbedTextsSplitsAGroupTheProviderRefusesForItsTokens(t *testing.T) {
	const limit = 10 // texts per request this provider accepts
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		requests++
		if len(req.Input) > limit {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"object":"error","message":"Too many tokens overall, split into more batches."}`))
			return
		}
		data := make([]map[string]any, len(req.Input))
		for i, text := range req.Input {
			data[i] = map[string]any{"embedding": []float32{float32(len(text)), 0.2, 0.3}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer srv.Close()

	texts := make([]string, 40)
	for i := range texts {
		texts[i] = string(make([]byte, i+1))
	}
	vectors, err := New(&Config{BaseURL: srv.URL, Model: "m", Provider: ProviderOpenAI, BatchSize: 40}).EmbedTexts(context.Background(), texts)
	if err != nil {
		t.Fatal(err)
	}
	if len(vectors) != len(texts) {
		t.Fatalf("got %d vectors for %d texts", len(vectors), len(texts))
	}
	for i, v := range vectors {
		if int(v[0]) != i+1 {
			t.Fatalf("vector %d belongs to a text of %d characters: the order was lost", i, int(v[0]))
		}
	}
	if requests < 4 {
		t.Fatalf("a group of 40 over a limit of %d was sent in %d requests", limit, requests)
	}
}

func TestEmbedTextsStopsWhenASingleTextIsOverTheLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":"Too many tokens overall"}`))
	}))
	defer srv.Close()
	_, err := New(&Config{BaseURL: srv.URL, Model: "m", Provider: ProviderOpenAI}).EmbedTexts(context.Background(), []string{"a", "b", "c"})
	if err == nil {
		t.Fatal("a text that no request can hold must be an error")
	}
}
