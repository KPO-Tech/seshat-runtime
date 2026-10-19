package providers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// The helpers that seed and sync the model list of a provider setting must understand the generic providers of the catalog: they used
// to answer with Anthropic's models and no base URL for any name they did not know.
func TestModelHelpersKnowTheGenericProvidersOfTheCatalog(t *testing.T) {
	for _, name := range []string{"groq", "togetherai", "fireworks-ai"} {
		provider := ResolveProviderFromString(name)
		if provider != types.APIProvider(name) {
			t.Fatalf("%s resolves to %q, want itself", name, provider)
		}
		static := StaticModels(name)
		if len(static) == 0 {
			t.Fatalf("%s: no static models", name)
		}
		if strings.HasPrefix(static[0].ModelID, "claude-") {
			t.Errorf("%s: static models are Anthropic's: %s", name, static[0].ModelID)
		}
		info, ok := GetModelInfo(provider, static[0].ModelID)
		if !ok || info.ContextWindow <= 0 {
			t.Errorf("%s: GetModelInfo(%s) = %+v, %v", name, static[0].ModelID, info, ok)
		}
		if NeedsCatalogReseed(name, []string{static[0].ModelID}) {
			t.Errorf("%s: its own models must not trigger a reseed", name)
		}
		if !NeedsCatalogReseed(name, []string{"claude-sonnet-5-5"}) {
			t.Errorf("%s: another provider's models must trigger a reseed", name)
		}
		if !strings.HasPrefix(DefaultBaseURL(name), "https://") {
			t.Errorf("%s: base URL = %q", name, DefaultBaseURL(name))
		}
	}
}

func TestWorkersAIAliasPointsAtTheGenericProvider(t *testing.T) {
	for _, name := range []string{"workers-ai", "cloudflare"} {
		if got := ResolveProviderFromString(name); got != "cloudflare-workers-ai" {
			t.Errorf("%s resolves to %q", name, got)
		}
	}
}

func TestAnUnknownNameStillDefaultsToAnthropic(t *testing.T) {
	if got := ResolveProviderFromString("no-such-provider"); got != types.APIProviderAnthropic {
		t.Fatalf("got %q", got)
	}
}

func TestModelsRouteFollowsTheVersionOfTheBaseURL(t *testing.T) {
	for base, want := range map[string]string{
		"https://api.groq.com/openai/v1": "https://api.groq.com/openai/v1/models",
		"https://api.z.ai/api/paas/v4":   "https://api.z.ai/api/paas/v4/models",
		"https://api.openai.com":         "https://api.openai.com/v1/models",
		"https://host.test/api":          "https://host.test/api/v1/models",
	} {
		if got := modelsURL(base); got != want {
			t.Errorf("modelsURL(%q) = %q, want %q", base, got, want)
		}
	}
}

func TestFetchModelsUsesTheModelsRouteOfAVersionedBaseURL(t *testing.T) {
	var path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = w.Write([]byte(`{"data":[{"id":"model-a"},{"id":"model-b"}]}`))
	}))
	defer server.Close()
	models, err := FetchModels(t.Context(), "some-provider", server.URL+"/openai/v1", "key")
	if err != nil || len(models) != 2 || path != "/openai/v1/models" {
		t.Fatalf("models %v, err %v, path %q", models, err, path)
	}
}
