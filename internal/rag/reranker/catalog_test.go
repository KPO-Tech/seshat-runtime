package reranker

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func clearRerankEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{"RAG_RERANK_PROVIDER", "RAG_RERANK_URL", "RAG_RERANK_MODEL", "RAG_RERANK_API_KEY",
		"COHERE_API_KEY", "CO_API_KEY", "VOYAGE_API_KEY", "OPENROUTER_API_KEY", "LANGSEARCH_API_KEY"} {
		t.Setenv(name, "")
	}
}

func TestFromEnvProvider(t *testing.T) {
	clearRerankEnv(t)
	t.Setenv("RAG_RERANK_PROVIDER", "Voyage")
	t.Setenv("VOYAGE_API_KEY", "vk")
	cfg := FromEnv()
	if cfg.BaseURL != "https://api.voyageai.com/v1/rerank" || cfg.Model != "rerank-2.5" || cfg.APIKey != "vk" || cfg.Style != StyleVoyage {
		t.Fatalf("voyage config = %+v", cfg)
	}

	t.Setenv("RAG_RERANK_MODEL", "rerank-2.5-lite")
	t.Setenv("RAG_RERANK_URL", "http://localhost:9/rerank")
	t.Setenv("RAG_RERANK_API_KEY", "explicit")
	cfg = FromEnv()
	if cfg.Model != "rerank-2.5-lite" || cfg.BaseURL != "http://localhost:9/rerank" || cfg.APIKey != "explicit" {
		t.Fatalf("explicit variables must override the provider, got %+v", cfg)
	}
}

func TestFromEnvProviderWithoutKeyFallsBack(t *testing.T) {
	clearRerankEnv(t)
	t.Setenv("RAG_RERANK_PROVIDER", "cohere")
	t.Setenv("LANGSEARCH_API_KEY", "ls")
	cfg := FromEnv()
	if cfg.BaseURL != langSearchBaseURL {
		t.Fatalf("a hosted provider without a key must fall back to the other settings, got %+v", cfg)
	}
	t.Setenv("RAG_RERANK_PROVIDER", "nope")
	if cfg := FromEnv(); cfg.BaseURL != langSearchBaseURL {
		t.Fatalf("an unknown provider must fall back, got %+v", cfg)
	}
}

// rerankRequest runs one Rerank against a stub and returns the JSON body received.
func rerankRequest(t *testing.T, cfg Config, response string) map[string]any {
	t.Helper()
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		_, _ = w.Write([]byte(response))
	}))
	defer srv.Close()
	cfg.BaseURL = srv.URL
	idx, scores, err := New(cfg).Rerank(context.Background(), "q", []string{"a", "b"}, 1)
	if err != nil || len(idx) != 1 || idx[0] != 1 || scores[0] != 0.9 {
		t.Fatalf("Rerank = %v %v %v", idx, scores, err)
	}
	return got
}

func TestRequestShapesAndResponseEnvelopes(t *testing.T) {
	cohere := rerankRequest(t, Config{Model: "rerank-v3.5", Style: StyleCohere}, `{"results":[{"index":1,"relevance_score":0.9}]}`)
	if cohere["top_n"] != float64(1) || cohere["texts"] != nil || cohere["top_k"] != nil || cohere["model"] != "rerank-v3.5" {
		t.Errorf("cohere body = %v", cohere)
	}
	voyage := rerankRequest(t, Config{Model: "rerank-2.5", Style: StyleVoyage}, `{"object":"list","data":[{"index":1,"relevance_score":0.9}]}`)
	if voyage["top_k"] != float64(1) || voyage["top_n"] != nil || voyage["texts"] != nil {
		t.Errorf("voyage body = %v", voyage)
	}
	generic := rerankRequest(t, Config{}, `[{"index":1,"score":0.9}]`)
	if generic["texts"] == nil || generic["documents"] == nil || generic["top_n"] != float64(1) {
		t.Errorf("a self-hosted server must still get the union of fields, got %v", generic)
	}
}

func TestTheDefaultOfAProviderStaysTheMeasuredOne(t *testing.T) {
	// A default changes only with a measurement: a newer model is offered after it, never in its place.
	for id, want := range map[string]string{"cohere": "rerank-v3.5", "voyage": "rerank-2.5"} {
		p, ok := ProfileFor(id)
		if !ok || p.DefaultModel() != want {
			t.Errorf("%s: default = %q, want %q", id, p.DefaultModel(), want)
		}
	}
}

func TestEveryProfileIsComplete(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range Profiles() {
		if p.ID == "" || p.DisplayName == "" || p.URL == "" || len(p.Models) == 0 || len(p.EnvVars) == 0 {
			t.Errorf("profile %+v is incomplete", p)
		}
		if seen[p.ID] {
			t.Errorf("provider %q is listed twice", p.ID)
		}
		seen[p.ID] = true
		models := map[string]bool{}
		for _, m := range p.Models {
			if models[m] {
				t.Errorf("%s: model %q is listed twice", p.ID, m)
			}
			models[m] = true
		}
	}
	if p, ok := ProfileFor("jina"); !ok || p.Style != StyleCohere || p.URL != "https://api.jina.ai/v1/rerank" {
		t.Errorf("jina profile = %+v ok=%v", p, ok)
	}
}

func TestProfileFor(t *testing.T) {
	p, ok := ProfileFor(" OpenRouter ")
	if !ok || p.DefaultModel() != "cohere/rerank-v3.5" {
		t.Fatalf("openrouter profile = %+v ok=%v", p, ok)
	}
	if _, ok := ProfileFor("x"); ok {
		t.Fatal("unknown provider must not resolve")
	}
}
