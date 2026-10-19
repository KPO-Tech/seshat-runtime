package catalog

import (
	"net/url"
	"testing"
)

// When models.dev starts giving a URL for a provider of the table, the table is out of date: compare the two and decide.
func TestOverridesAreStillNeeded(t *testing.T) {
	raw, err := decodeSnapshot(snapshotFile, "raw", false)
	if err != nil {
		t.Fatal(err)
	}
	for id, o := range endpointOverrides {
		p, ok := raw.Providers[id]
		if !ok {
			t.Errorf("%s: the provider is not in the snapshot any more, drop its override", id)
			continue
		}
		if p.BaseURL != "" {
			t.Errorf("%s: the snapshot now gives the URL %q (ours: %q); compare them and drop the override or correct it", id, p.BaseURL, o.BaseURL)
		}
	}
}

func TestOverridesMakeTheProvidersOpenAICompatible(t *testing.T) {
	cat, err := Embedded()
	if err != nil {
		t.Fatal(err)
	}
	for id, o := range endpointOverrides {
		u, err := url.Parse(o.BaseURL)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			t.Errorf("%s: %q is not an https URL", id, o.BaseURL)
		}
		p := cat.Providers[id]
		if !p.OpenAICompatible() || p.BaseURL != o.BaseURL || len(p.Env) == 0 || len(p.Models) == 0 {
			t.Errorf("%s: not callable after the override: %+v", id, p)
		}
		if o.Source == "" {
			t.Errorf("%s: say where the URL comes from", id)
		}
	}
}

func TestAFetchedCatalogCannotMoveAnOverriddenProvider(t *testing.T) {
	trusted, _ := Embedded()
	fetched := &Catalog{Providers: map[string]Provider{"groq": {ID: "groq", BaseURL: "https://evil.example/v1", Models: map[string]Model{}}}}
	if got := Merge(trusted, fetched).Providers["groq"].BaseURL; got != endpointOverrides["groq"].BaseURL {
		t.Fatalf("groq URL = %q after a fetch", got)
	}
}
