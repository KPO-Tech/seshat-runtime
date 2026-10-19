package providers

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/KPO-Tech/seshat-runtime/internal/catalog"
	"github.com/KPO-Tech/seshat-runtime/internal/model"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// aGenericProvider returns a generic catalog provider with a model, for tests that do not depend on which one.
func aGenericProvider(t *testing.T) (ProviderInfo, ModelInfo) {
	t.Helper()
	for _, p := range CatalogProviders() {
		if len(p.Models) > 0 && p.Models[0].ContextWindow > 0 {
			return p, p.Models[0]
		}
	}
	t.Fatal("the catalog has no generic provider with a model")
	return ProviderInfo{}, ModelInfo{}
}

func TestGenericProvidersComeFromTheCatalogAndLeaveOurBuiltInOnesAlone(t *testing.T) {
	providersList := CatalogProviders()
	if len(providersList) < 100 {
		t.Fatalf("%d generic providers, want at least 100", len(providersList))
	}
	for _, p := range providersList {
		if _, builtIn := nativeProviders[p.Name]; builtIn {
			t.Errorf("%s is built in and must not be offered as a generic provider", p.Name)
		}
		if !IsCatalogProvider(types.APIProvider(p.Name)) {
			t.Errorf("%s is listed but IsCatalogProvider says no", p.Name)
		}
	}
	for _, builtIn := range []types.APIProvider{types.APIProviderAnthropic, types.APIProviderOpenAI, types.APIProviderZAi, types.APIProviderGemini} {
		if IsCatalogProvider(builtIn) {
			t.Errorf("%s is built in, not a generic provider", builtIn)
		}
	}
}

func TestGenericProviderIsCalledWithTheOpenAICompatibleProtocol(t *testing.T) {
	info, m := aGenericProvider(t)
	provider := types.APIProvider(info.Name)

	var gotPath, gotAuth string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`))
	}))
	defer server.Close()

	config, ok := ConfigForCatalogProvider(provider)
	if !ok {
		t.Fatalf("%s has no config although it is in the embedded catalog", provider)
	}
	if !strings.HasPrefix(config.BaseURL, "https://") {
		t.Fatalf("base URL = %q, want the catalog's HTTPS URL", config.BaseURL)
	}
	config.BaseURL = server.URL // the fake server stands in for the provider
	client := NewClientWithConfig("test-key", config)

	resp, err := client.CreateMessage(t.Context(), types.APIRequest{
		Model:     types.ModelIdentifier{Provider: provider, Model: m.Identifier},
		MaxTokens: 16,
		Messages:  []types.Message{types.UserMessage("u", "hi")},
	})
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	if gotPath != "/chat/completions" {
		t.Errorf("path = %q, want /chat/completions", gotPath)
	}
	if gotAuth != "Bearer test-key" {
		t.Errorf("Authorization = %q, want a bearer token", gotAuth)
	}
	if gotBody["model"] != m.Identifier {
		t.Errorf("body model = %v, want %s", gotBody["model"], m.Identifier)
	}
	if _, ok := gotBody["messages"]; !ok {
		t.Errorf("body has no messages: %v", gotBody)
	}
	if resp == nil || len(resp.Content) == 0 {
		t.Fatalf("response = %+v, want the assistant's reply decoded", resp)
	}
}

func TestGenericProviderKeyComesFromTheVariableTheCatalogNames(t *testing.T) {
	info, _ := aGenericProvider(t)
	cp, _ := Catalog().Provider(info.Name)
	if len(cp.Env) == 0 {
		t.Skipf("%s names no variable", info.Name)
	}
	if got := providerEnvVar(types.APIProvider(info.Name)); got != cp.Env[0] {
		t.Errorf("env var = %q, want %q from the catalog", got, cp.Env[0])
	}
	if got := providerEnvVar(types.APIProviderOpenAI); got != "OPENAI_API_KEY" {
		t.Errorf("a built-in provider keeps its own variable, got %q", got)
	}
}

func TestCatalogFillsInModelsOurCatalogDoesNotKnowAndNeverOverridesIt(t *testing.T) {
	info, m := aGenericProvider(t)
	if got := model.Global.ContextWindowFor(info.Name, m.Identifier); got.MaxTokens != m.ContextWindow {
		t.Errorf("%s/%s window = %d, want the catalog's %d", info.Name, m.Identifier, got.MaxTokens, m.ContextWindow)
	}
	if curated, ok := GetModelInfo(types.APIProviderZAi, "glm-5.3"); ok {
		if got := model.Global.ContextWindowFor(string(types.APIProviderZAi), "glm-5.3"); got.MaxTokens != curated.ContextWindow {
			t.Errorf("glm-5.3 window = %d, want our curated %d", got.MaxTokens, curated.ContextWindow)
		}
	}
}

func TestAnOutputLimitEqualToTheWindowIsNotUsed(t *testing.T) {
	got := catalogModelMetadata("p", catalog.Model{ID: "m", ContextWindow: 262144, MaxOutput: 262144})
	if got.ContextWindow.MaxOutputTokens != defaultCatalogMaxOutput {
		t.Errorf("output = %d, want the default %d when the catalog repeats the window", got.ContextWindow.MaxOutputTokens, defaultCatalogMaxOutput)
	}
	real := catalogModelMetadata("p", catalog.Model{ID: "m", ContextWindow: 200000, MaxOutput: 64000})
	if real.ContextWindow.MaxOutputTokens != 64000 {
		t.Errorf("a real limit must be kept, got %d", real.ContextWindow.MaxOutputTokens)
	}
}

// TestAProviderOnlyADownloadedCatalogKnowsGetsNoConfigOfItsOwn: its base URL would come from a downloaded file, so a key must
// not be sent there until the host has had the user confirm the URL and builds the Config itself.
func TestAProviderOnlyADownloadedCatalogKnowsGetsNoConfigOfItsOwn(t *testing.T) {
	original := Catalog()
	t.Cleanup(func() { SetCatalog(original) })

	downloaded := &catalog.Catalog{Source: "fetched", Providers: map[string]catalog.Provider{
		"shady": {ID: "shady", Name: "Shady", BaseURL: "https://collector.example/v1", Env: []string{"SHADY_KEY"}, Protocol: "@ai-sdk/openai-compatible",
			Models: map[string]catalog.Model{"s-1": {ID: "s-1", ContextWindow: 100000}}},
	}}
	SetCatalog(catalog.Merge(original, downloaded))

	if !IsCatalogProvider("shady") {
		t.Fatal("the provider is known to the catalog")
	}
	if config, ok := ConfigForCatalogProvider("shady"); ok {
		t.Fatalf("an unverified provider got a config with URL %q: a key would go to a URL nobody confirmed", config.BaseURL)
	}
	if GetProviderConfig("shady") != nil {
		t.Fatal("GetProviderConfig must not hand out a config for an unverified provider")
	}
	var found bool
	for _, p := range CatalogProviders() {
		if p.Name == "shady" {
			found = true
			if !strings.Contains(p.Description, "Unverified") {
				t.Errorf("description = %q, want it to say the provider is unverified", p.Description)
			}
		}
	}
	if !found {
		t.Error("the unverified provider must still be listed, so the host can ask the user")
	}
}

func TestBuiltInProviderListsGainTheCatalogModelsWithoutLosingTheirEnds(t *testing.T) {
	original := Catalog()
	t.Cleanup(func() { SetCatalog(original) })
	before := AllProvidersInfo()[types.APIProviderAnthropic]
	if len(before.Models) < 3 {
		t.Fatal("the curated Anthropic list is expected to have models")
	}
	large, small := before.Models[0].Identifier, before.Models[len(before.Models)-1].Identifier
	curatedDescription := before.Models[0].Description

	fake := &catalog.Catalog{Source: "test", Providers: map[string]catalog.Provider{
		"anthropic": {ID: "anthropic", Name: "Anthropic", Models: map[string]catalog.Model{
			"claude-future-9":  {ID: "claude-future-9", Name: "Claude Future 9", ContextWindow: 2000000, MaxOutput: 200000, ReleaseDate: "2027-01-01", InputModalities: []string{"image"}},
			"claude-retired-1": {ID: "claude-retired-1", Name: "Retired", ContextWindow: 100000, ReleaseDate: "2027-02-01", Status: "deprecated"},
			large:              {ID: large, Name: "Overridden name", ContextWindow: 5},
		}},
	}}
	SetCatalog(fake)

	got := AllProvidersInfo()[types.APIProviderAnthropic]
	if got.Models[0].Identifier != large || got.Models[len(got.Models)-1].Identifier != small {
		t.Fatalf("the ends of the list moved: first %q last %q, want %q and %q (they are the default large and small models)", got.Models[0].Identifier, got.Models[len(got.Models)-1].Identifier, large, small)
	}
	if got.Models[0].Description != curatedDescription || got.Models[0].ContextWindow == 5 {
		t.Fatalf("a model we list by hand must not be replaced by the catalog's data: %+v", got.Models[0])
	}
	var future, retired bool
	for _, m := range got.Models {
		future = future || m.Identifier == "claude-future-9"
		retired = retired || m.Identifier == "claude-retired-1"
	}
	if !future {
		t.Error("a model only the catalog has must be listed")
	}
	if retired {
		t.Error("a deprecated model must not be listed")
	}
	if info, ok := GetModelInfo(types.APIProviderAnthropic, "claude-future-9"); !ok || info.ContextWindow != 2000000 || !info.Capabilities.Vision {
		t.Errorf("GetModelInfo = %+v, %v, want the catalog model with its window and image input", info, ok)
	}
}

func TestEveryBuiltInProviderThatTheCatalogExtendsKeepsItsCuratedModels(t *testing.T) {
	curated := curatedProvidersInfo()
	for provider, info := range AllProvidersInfo() {
		have := map[string]bool{}
		for _, m := range info.Models {
			have[m.Identifier] = true
		}
		for _, m := range curated[provider].Models {
			if !have[m.Identifier] {
				t.Errorf("%s lost its curated model %s", provider, m.Identifier)
			}
		}
		if len(info.Models) > len(curated[provider].Models)+maxCatalogModelsPerProvider {
			t.Errorf("%s has %d models, more than curated plus the cap", provider, len(info.Models))
		}
	}
}

func TestListsLeaveOutModelsTheChatClientCannotUse(t *testing.T) {
	all := map[string]catalog.Model{
		"gpt-5.6":                    {ID: "gpt-5.6"},
		"gpt-5.5-pro":                {ID: "gpt-5.5-pro"},
		"gpt-5.3-codex":              {ID: "gpt-5.3-codex"},
		"gpt-realtime-2.1":           {ID: "gpt-realtime-2.1"},
		"gpt-image-2":                {ID: "gpt-image-2"},
		"claude-opus-4-5":            {ID: "claude-opus-4-5"},
		"claude-opus-4-5-20251101":   {ID: "claude-opus-4-5-20251101"},
		"claude-only-dated-20251101": {ID: "claude-only-dated-20251101"},
		"text-embedding-3":           {ID: "text-embedding-3"},
	}
	for id, want := range map[string]bool{
		"gpt-5.6":                    false,
		"gpt-5.5-pro":                true,
		"gpt-5.3-codex":              true,
		"gpt-realtime-2.1":           true,
		"gpt-image-2":                true,
		"claude-opus-4-5":            false,
		"claude-opus-4-5-20251101":   true,
		"claude-only-dated-20251101": false,
		"text-embedding-3":           true,
	} {
		if got := unsuitableForChat(types.APIProviderOpenAI, id, all); got != want {
			t.Errorf("unsuitableForChat(openai, %q) = %v, want %v", id, got, want)
		}
	}
	if unsuitableForChat(types.APIProviderAnthropic, "claude-pro-x", all) {
		t.Error("the -pro rule is OpenAI's: it must not hide other providers' models")
	}
}
