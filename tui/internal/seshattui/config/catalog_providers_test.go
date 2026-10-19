package config

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
)

func TestTheTUIOffersTheCatalogProvidersAfterTheBuiltInOnes(t *testing.T) {
	all := buildSeshatProviders()
	builtIn := map[string]bool{}
	for _, p := range all {
		if p.Type != catwalk.TypeOpenAICompat {
			builtIn[string(p.ID)] = true
		}
	}
	generic := catalogProviders()
	if len(generic) < 100 {
		t.Fatalf("%d generic providers offered, want at least 100", len(generic))
	}
	for _, p := range generic {
		if builtIn[string(p.ID)] {
			t.Errorf("%s is both built in and generic", p.ID)
		}
		if p.Type != catwalk.TypeOpenAICompat || p.APIEndpoint == "" || len(p.Models) == 0 {
			t.Errorf("%s: type %q, endpoint %q, %d models; want an OpenAI-compatible provider with an endpoint and models", p.ID, p.Type, p.APIEndpoint, len(p.Models))
		}
		if len(p.Models) > catalogModelsPerProvider {
			t.Errorf("%s lists %d models, want at most %d", p.ID, len(p.Models), catalogModelsPerProvider)
		}
		large, small := false, false
		for _, m := range p.Models {
			large = large || m.ID == p.DefaultLargeModelID
			small = small || m.ID == p.DefaultSmallModelID
		}
		if !large || !small {
			t.Errorf("%s: default models %q and %q must be among its models", p.ID, p.DefaultLargeModelID, p.DefaultSmallModelID)
		}
	}
	if len(all) != len(generic)+countBuiltIn(all) {
		t.Errorf("the provider list must be the built-in ones followed by the generic ones")
	}
}

func countBuiltIn(all []catwalk.Provider) int {
	n := 0
	for _, p := range all {
		if !isCatalogProviderID(string(p.ID)) {
			n++
		}
	}
	return n
}

func TestCheapestModelPrefersALowerKnownPrice(t *testing.T) {
	models := []catwalk.Model{{ID: "new", CostPer1MIn: 3}, {ID: "mid", CostPer1MIn: 1}, {ID: "free-unknown"}, {ID: "tiny", CostPer1MIn: 0.1}}
	if got := cheapestModel(models); got != "tiny" {
		t.Errorf("got %q, want tiny", got)
	}
	if got := cheapestModel([]catwalk.Model{{ID: "a"}, {ID: "b"}}); got != "a" {
		t.Errorf("without prices the first one stays, got %q", got)
	}
}

// TestConnectionOfACatalogProviderOnlyFailsOnARefusedKey: many of these providers have no /models endpoint.
func TestConnectionOfACatalogProviderOnlyFailsOnARefusedKey(t *testing.T) {
	generic := catalogProviders()
	if len(generic) == 0 {
		t.Fatal("no generic provider")
	}
	id := string(generic[0].ID)

	for _, tc := range []struct {
		status  int
		wantErr bool
	}{
		{http.StatusOK, false},
		{http.StatusNotFound, false},
		{http.StatusUnauthorized, true},
		{http.StatusForbidden, true},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status) }))
		cfg := &ProviderConfig{ID: id, Type: catwalk.TypeOpenAICompat, BaseURL: server.URL, APIKey: "k"}
		err := cfg.TestConnection(IdentityResolver())
		server.Close()
		if (err != nil) != tc.wantErr {
			t.Errorf("status %d: error = %v, want error %v", tc.status, err, tc.wantErr)
		}
	}
}
