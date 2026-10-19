package sdk

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/KPO-Tech/seshat-runtime/internal/catalog"
	"github.com/KPO-Tech/seshat-runtime/internal/providers"
)

func TestCatalogProvidersListsTheGenericOnesWithTheirKeyVariable(t *testing.T) {
	list := CatalogProviders()
	if len(list) < 100 {
		t.Fatalf("%d generic providers, want at least 100", len(list))
	}
	var withKey int
	for _, p := range list {
		if p.Unverified {
			t.Errorf("%s: the embedded catalog is verified", p.ID)
		}
		if p.BaseURL == "" || p.ModelCount == 0 {
			t.Errorf("%s: base URL %q and %d models, want both", p.ID, p.BaseURL, p.ModelCount)
		}
		if len(p.KeyEnvVars) > 0 {
			withKey++
		}
	}
	if withKey < len(list)*9/10 {
		t.Errorf("only %d of %d providers name a key variable", withKey, len(list))
	}
}

// fakeModelsDev serves a minimal catalog in the models.dev format: enough providers to be believed, and one that the embedded
// catalog does not know.
func fakeModelsDev(t *testing.T) *httptest.Server {
	t.Helper()
	providersJSON := map[string]any{}
	for i := 0; i < 120; i++ {
		providersJSON[fmt.Sprintf("filler-%d", i)] = map[string]any{
			"name": "Filler", "npm": "@ai-sdk/openai-compatible", "api": "https://filler.example/v1", "env": []string{"FILLER_KEY"},
			"models": map[string]any{"m": map[string]any{"name": "M", "tool_call": true, "limit": map[string]int{"context": 1000, "output": 100}}},
		}
	}
	providersJSON["brand-new"] = map[string]any{
		"name": "Brand New", "npm": "@ai-sdk/openai-compatible", "api": "https://api.brandnew.example/v1", "env": []string{"BRANDNEW_API_KEY"},
		"models": map[string]any{"bn-1": map[string]any{"name": "BN 1", "tool_call": true, "limit": map[string]int{"context": 64000, "output": 4000}}},
	}
	raw, err := json.Marshal(providersJSON)
	if err != nil {
		t.Fatal(err)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(raw) }))
}

func TestRefreshCatalogAddsAProviderAsUnverifiedAndKeepsTheEmbeddedOnesVerified(t *testing.T) {
	original := providers.Catalog()
	t.Cleanup(func() { providers.SetCatalog(original) })
	server := fakeModelsDev(t)
	defer server.Close()
	dir := t.TempDir()

	if err := refreshCatalog(context.Background(), server.Client(), server.URL, dir); err != nil {
		t.Fatal(err)
	}
	var brandNew *CatalogProviderSummary
	for _, p := range CatalogProviders() {
		if p.ID == "brand-new" {
			brandNew = &p
		}
		if p.ID != "brand-new" && len(p.ID) > 7 && p.ID[:7] == "filler-" && !p.Unverified {
			t.Errorf("%s came from the download and must be unverified", p.ID)
		}
	}
	if brandNew == nil {
		t.Fatal("the provider the download added is not listed")
	}
	if !brandNew.Unverified || brandNew.BaseURL != "https://api.brandnew.example/v1" {
		t.Fatalf("brand-new = %+v, want an unverified provider with the downloaded URL", *brandNew)
	}
	if cfg, ok := providers.ConfigForCatalogProvider("brand-new"); ok {
		t.Fatalf("an unverified provider must get no config of its own, got URL %q", cfg.BaseURL)
	}
	if p, _ := catalog.Embedded(); p != nil {
		if _, ok := p.Provider("brand-new"); ok {
			t.Fatal("a refresh must not change the embedded catalog")
		}
	}
}

func TestClientConfigLoadsTheCachedCatalogWithoutTheNetwork(t *testing.T) {
	original := providers.Catalog()
	t.Cleanup(func() { providers.SetCatalog(original) })
	server := fakeModelsDev(t)
	dir := t.TempDir()
	if err := catalog.Refresh(context.Background(), server.Client(), server.URL, dir); err != nil {
		t.Fatal(err)
	}
	server.Close() // from here on the network is gone: nothing may need it

	initCatalog(&ClientConfig{CatalogCacheDir: dir})

	found := false
	for _, p := range CatalogProviders() {
		found = found || p.ID == "brand-new"
	}
	if !found {
		t.Fatal("the cached catalog was not applied")
	}
}
