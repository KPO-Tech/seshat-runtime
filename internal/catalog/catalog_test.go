package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestEmbeddedCatalogHasTheProvidersAndModelsWeRelyOn(t *testing.T) {
	cat, err := Embedded()
	if err != nil {
		t.Fatal(err)
	}
	if len(cat.Providers) < minProviders {
		t.Fatalf("embedded catalog has %d providers, want at least %d", len(cat.Providers), minProviders)
	}
	for _, p := range cat.Providers {
		if p.Unverified {
			t.Fatalf("%s: every provider of the embedded catalog is verified", p.ID)
		}
	}
	zai, ok := cat.Provider("zai")
	if !ok {
		t.Fatal("z.ai is missing")
	}
	air, ok := zai.Models["glm-4.5-air"]
	if !ok || air.ContextWindow <= 0 {
		t.Fatalf("glm-4.5-air (the summary model of a default compaction rule) = %+v, ok=%v", air, ok)
	}
	if n := len(cat.OpenAICompatibleProviders()); n < 100 {
		t.Fatalf("%d OpenAI-compatible providers in the embedded catalog, want at least 100", n)
	}
}

func TestOpenAICompatibleNeedsTheProtocolAndABaseURL(t *testing.T) {
	cases := []struct {
		p    Provider
		want bool
	}{
		{Provider{Protocol: protocolOpenAICompatible, BaseURL: "https://api.example.com/v1"}, true},
		{Provider{Protocol: protocolOpenAICompatible}, false},
		{Provider{Protocol: "@ai-sdk/anthropic", BaseURL: "https://api.example.com"}, false},
		{Provider{Protocol: protocolOpenAICompatible, BaseURL: "  "}, false},
	}
	for _, c := range cases {
		if got := c.p.OpenAICompatible(); got != c.want {
			t.Errorf("%+v: got %v, want %v", c.p, got, c.want)
		}
	}
}

func TestOutputIsKnownDistrustsALimitEqualToTheWindow(t *testing.T) {
	if (Model{ContextWindow: 262144, MaxOutput: 262144}).OutputIsKnown() {
		t.Error("an output limit equal to the context window usually means unknown")
	}
	if (Model{ContextWindow: 262144}).OutputIsKnown() {
		t.Error("no output limit is not a known one")
	}
	if !(Model{ContextWindow: 200000, MaxOutput: 64000}).OutputIsKnown() {
		t.Error("a limit below the window is a real one")
	}
}

func fakeTrusted() *Catalog {
	return &Catalog{Source: "embedded", Providers: map[string]Provider{
		"acme": {ID: "acme", Name: "Acme", BaseURL: "https://api.acme.example/v1", Env: []string{"ACME_API_KEY"}, Protocol: protocolOpenAICompatible,
			Models: map[string]Model{"a-1": {ID: "a-1", ContextWindow: 100000}, "a-old": {ID: "a-old", ContextWindow: 8000}}},
	}}
}

func TestMergeNeverMovesAVerifiedProviderToAnotherURL(t *testing.T) {
	fetched := &Catalog{Source: "fetched", Providers: map[string]Provider{
		"acme": {ID: "acme", Name: "Acme AI", BaseURL: "https://evil.example/v1", Env: []string{"STEAL_KEY"}, Protocol: "@ai-sdk/anthropic",
			Models: map[string]Model{"a-1": {ID: "a-1", ContextWindow: 200000}, "a-2": {ID: "a-2", ContextWindow: 300000}}},
	}}
	merged := Merge(fakeTrusted(), fetched)
	p := merged.Providers["acme"]
	if p.BaseURL != "https://api.acme.example/v1" || p.Env[0] != "ACME_API_KEY" || p.Protocol != protocolOpenAICompatible {
		t.Fatalf("a fetched catalog moved a verified provider: %+v", p)
	}
	if p.Unverified {
		t.Fatal("a provider of the trusted catalog stays verified")
	}
	if p.Name != "Acme AI" {
		t.Errorf("name = %q, want the fetched name", p.Name)
	}
	if p.Models["a-1"].ContextWindow != 200000 || p.Models["a-2"].ContextWindow != 300000 {
		t.Errorf("models must take the fetched data and gain the new ones: %+v", p.Models)
	}
	if _, kept := p.Models["a-old"]; !kept {
		t.Error("a model the fetched catalog dropped must be kept")
	}
}

func TestMergeMarksNewProvidersUnverified(t *testing.T) {
	fetched := &Catalog{Source: "fetched", Providers: map[string]Provider{
		"newcomer": {ID: "newcomer", Name: "Newcomer", BaseURL: "https://api.newcomer.example/v1", Protocol: protocolOpenAICompatible, Models: map[string]Model{"n-1": {ID: "n-1"}}},
	}}
	merged := Merge(fakeTrusted(), fetched)
	got, ok := merged.Providers["newcomer"]
	if !ok || !got.Unverified {
		t.Fatalf("a provider that is only in the fetched catalog must be added as unverified: %+v, ok=%v", got, ok)
	}
	if merged.Providers["acme"].Unverified {
		t.Error("the trusted provider must stay verified")
	}
}

// modelsDevBody builds a catalog in the models.dev format with n providers.
func modelsDevBody(n int) []byte {
	providers := map[string]any{}
	for i := 0; i < n; i++ {
		providers[fmt.Sprintf("p%d", i)] = map[string]any{
			"name": fmt.Sprintf("Provider %d", i), "npm": "@ai-sdk/openai-compatible", "api": fmt.Sprintf("https://p%d.example/v1", i), "env": []string{fmt.Sprintf("P%d_KEY", i)},
			"models": map[string]any{
				"chat":  map[string]any{"name": "Chat", "tool_call": true, "reasoning": true, "limit": map[string]int{"context": 128000, "output": 8000}, "modalities": map[string][]string{"input": {"text", "image"}, "output": {"text"}}, "cost": map[string]float64{"input": 1, "output": 2, "cache_read": 0.1}},
				"embed": map[string]any{"name": "Embed", "tool_call": false, "limit": map[string]int{"context": 8000, "output": 0}, "modalities": map[string][]string{"input": {"text"}, "output": {"text"}}},
				"image": map[string]any{"name": "Image", "tool_call": true, "limit": map[string]int{"context": 0, "output": 0}, "modalities": map[string][]string{"input": {"text"}, "output": {"image"}}},
			},
		}
	}
	raw, _ := json.Marshal(providers)
	return raw
}

func TestParseKeepsOnlyModelsAnAgentCanUse(t *testing.T) {
	cat, err := Parse(modelsDevBody(minProviders), "test")
	if err != nil {
		t.Fatal(err)
	}
	p := cat.Providers["p0"]
	if len(p.Models) != 1 {
		t.Fatalf("models = %v, want only the one that produces text and calls tools", p.Models)
	}
	m := p.Models["chat"]
	if !m.Vision() || !m.Reasoning || m.ContextWindow != 128000 || m.Cost.Input != 1 || m.Cost.CacheRead != 0.1 {
		t.Fatalf("model = %+v", m)
	}
	if !p.Unverified || !p.OpenAICompatible() {
		t.Fatalf("a parsed provider is unverified and, with a URL, OpenAI-compatible: %+v", p)
	}
}

func TestParseRefusesACatalogThatIsTooSmall(t *testing.T) {
	if _, err := Parse(modelsDevBody(3), "test"); err == nil {
		t.Fatal("a catalog of three providers is an error page or a truncated file")
	}
	if _, err := Parse([]byte("<html>502 Bad Gateway</html>"), "test"); err == nil {
		t.Fatal("a body that is not JSON must be refused")
	}
}

func TestRefreshKeepsTheFetchAndLoadMergesItOverTheEmbeddedCatalog(t *testing.T) {
	body := modelsDevBody(minProviders)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want a plain GET", r.Method)
		}
		_, _ = w.Write(body)
	}))
	defer server.Close()
	dir := t.TempDir()

	if _, ok := CacheAge(dir); ok {
		t.Fatal("no cache yet")
	}
	fetched, err := RefreshIfStale(context.Background(), server.Client(), server.URL, dir)
	if err != nil || !fetched {
		t.Fatalf("RefreshIfStale = %v, %v, want a fetch", fetched, err)
	}
	if again, err := RefreshIfStale(context.Background(), server.Client(), server.URL, dir); err != nil || again {
		t.Fatalf("a fresh cache must not be fetched again: %v, %v", again, err)
	}

	cat, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := cat.Provider("p0"); !ok || !p.Unverified {
		t.Fatalf("a provider that came with the fetch must be present and unverified: %+v, ok=%v", p, ok)
	}
	if p, _ := cat.Provider("zai"); p.Unverified {
		t.Fatal("embedded providers stay verified after a merge")
	}
}

func TestLoadIgnoresADamagedCacheAndNeverNeedsTheNetwork(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, cacheFileName), []byte("not gzip"), 0o644); err != nil {
		t.Fatal(err)
	}
	cat, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cat.Source != "embedded" {
		t.Fatalf("source = %q, want the embedded catalog when the cache is damaged", cat.Source)
	}
	if empty, err := Load(""); err != nil || empty.Source != "embedded" {
		t.Fatalf("without a cache directory the embedded catalog is used: %v, %v", empty, err)
	}
}

func TestFetchRefusesAFailingServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "down", http.StatusBadGateway) }))
	defer server.Close()
	if _, _, err := Fetch(context.Background(), server.Client(), server.URL); err == nil {
		t.Fatal("a 502 must be an error")
	}
}

func TestSortedModelsPutsNewestFirstAndDeprecatedLast(t *testing.T) {
	p := Provider{Models: map[string]Model{
		"old":  {ID: "old", ReleaseDate: "2024-01-01"},
		"new":  {ID: "new", ReleaseDate: "2026-05-01"},
		"gone": {ID: "gone", ReleaseDate: "2026-09-01", Status: "deprecated"},
	}}
	var ids []string
	for _, m := range p.SortedModels() {
		ids = append(ids, m.ID)
	}
	if fmt.Sprint(ids) != "[new old gone]" {
		t.Fatalf("order = %v, want [new old gone]", ids)
	}
}
