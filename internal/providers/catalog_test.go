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

func TestBuiltInProviderListsAreTheCatalogModels(t *testing.T) {
	original := Catalog()
	t.Cleanup(func() { SetCatalog(original) })

	fake := &catalog.Catalog{Source: "test", Providers: map[string]catalog.Provider{
		"anthropic": {ID: "anthropic", Name: "Anthropic", Models: map[string]catalog.Model{
			"claude-future-9":  {ID: "claude-future-9", Name: "Claude Future 9", ContextWindow: 2000000, MaxOutput: 200000, ReleaseDate: "2027-01-01", InputModalities: []string{"image"}, Cost: catalog.Cost{Input: 10, Output: 50}},
			"claude-retired-1": {ID: "claude-retired-1", Name: "Retired", ContextWindow: 100000, ReleaseDate: "2027-02-01", Status: "deprecated"},
			"claude-mid-1":     {ID: "claude-mid-1", Name: "Mid", ContextWindow: 200000, ReleaseDate: "2026-06-01", Cost: catalog.Cost{Input: 3, Output: 15}},
			"claude-tiny-1":    {ID: "claude-tiny-1", Name: "Tiny", ContextWindow: 100000, ReleaseDate: "2026-01-01", Cost: catalog.Cost{Input: 0.5, Output: 2}},
		}},
	}}
	SetCatalog(fake)

	got := AllProvidersInfo()[types.APIProviderAnthropic]
	var ids []string
	for _, m := range got.Models {
		ids = append(ids, m.Identifier)
	}
	want := []string{"claude-future-9", "claude-mid-1", "claude-tiny-1"}
	if len(ids) != len(want) {
		t.Fatalf("models = %v, want %v (the deprecated one is left out)", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("models = %v, want %v", ids, want)
		}
	}
	if info, ok := GetModelInfo(types.APIProviderAnthropic, "claude-future-9"); !ok || info.ContextWindow != 2000000 || !info.Capabilities.Vision {
		t.Errorf("GetModelInfo = %+v, %v, want the catalog model with its window and image input", info, ok)
	}
	if def, ok := DefaultModelIdentifier(types.APIProviderAnthropic); !ok || def.Model != "claude-future-9" {
		t.Errorf("default = %+v, %v", def, ok)
	}
}

func TestEveryBuiltInProviderThatTheCatalogExtendsHasModelsFromIt(t *testing.T) {
	for provider, info := range AllProvidersInfo() {
		if _, extended := catalogExtended()[provider]; !extended {
			continue
		}
		if len(info.Models) == 0 {
			t.Errorf("%s has no model: the registry names none, so the catalog must supply them", provider)
		}
		for _, m := range info.Models {
			if m.ContextWindow <= 0 {
				t.Errorf("%s: model %s has no context window", provider, m.Identifier)
			}
		}
	}
}

func TestRankModelsPutsTheDearestRecentModelFirstAndTheCheapestLast(t *testing.T) {
	models := []catalog.Model{ // newest first
		{ID: "mid", Cost: catalog.Cost{Input: 1, Output: 4}},
		{ID: "flagship", Cost: catalog.Cost{Input: 5, Output: 20}},
		{ID: "tiny", Cost: catalog.Cost{Input: 0.1, Output: 0.4}},
		{ID: "old", Cost: catalog.Cost{Input: 2, Output: 8}},
	}
	got := rankModels(models)
	ids := []string{got[0].ID, got[1].ID, got[2].ID, got[3].ID}
	want := []string{"flagship", "mid", "old", "tiny"}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("order = %v, want %v", ids, want)
		}
	}
	unpriced := rankModels([]catalog.Model{{ID: "new"}, {ID: "older"}, {ID: "oldest"}})
	if unpriced[0].ID != "new" || unpriced[2].ID != "oldest" {
		t.Fatalf("without prices the newest is large and the oldest small: %v", unpriced)
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

func TestReasoningIsReadFromWhicheverFieldCarriesIt(t *testing.T) {
	for name, body := range map[string]string{
		"reasoning_content": `{"choices":[{"message":{"content":"a","reasoning_content":"think"}}]}`,
		"reasoning":         `{"choices":[{"message":{"content":"a","reasoning":"think"}}]}`,
		"reasoning_details": `{"choices":[{"message":{"content":"a","reasoning_details":[{"text":"thi"},{"summary":"nk"}]}}]}`,
	} {
		resp, err := decodeOpenAIResponse(strings.NewReader(body), types.ModelIdentifier{})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		thinking, _ := resp.Content[0].(types.ThinkingContent)
		if thinking.Thinking != "think" {
			t.Errorf("%s: thinking = %q", name, thinking.Thinking)
		}
	}
}

func TestStreamReasoningDetailsBecomeThinking(t *testing.T) {
	var r openAIReasoning
	if err := json.Unmarshal([]byte(`{"reasoning_details":[{"text":"ab"},{"text":"c"}]}`), &r); err != nil {
		t.Fatal(err)
	}
	if got := r.reasoningText(); got != "abc" {
		t.Fatalf("reasoningText = %q", got)
	}
}

func TestTemperatureIsOmittedForAModelThatRejectsIt(t *testing.T) {
	model.Global.Register(model.Metadata{ID: "no-temp-model", Provider: "no-temp-prov", Capabilities: model.Capabilities{NoTemperature: true}})
	temp := 0.3
	req := types.APIRequest{Model: types.ModelIdentifier{Provider: "no-temp-prov", Model: "no-temp-model"}, Temperature: &temp}
	if sendsTemperature(req) {
		t.Fatal("the temperature must not be sent to a model the catalog says rejects it")
	}
	req.Model.Model = "unknown-model"
	if !sendsTemperature(req) {
		t.Fatal("a model the catalog does not know receives the temperature the request sets")
	}
	req.Temperature = nil
	if sendsTemperature(req) {
		t.Fatal("no temperature set, nothing to send")
	}
}

func TestContextWindowUsesTheInputLimitWhenItIsBelowTheWindow(t *testing.T) {
	model.Global.Register(model.Metadata{ID: "input-capped", Provider: "cap-prov", ContextWindow: model.ContextWindow{MaxTokens: 400000, MaxInputTokens: 272000, MaxOutputTokens: 128000}})
	cw := types.GetContextWindow(types.ModelIdentifier{Provider: "cap-prov", Model: "input-capped"})
	if cw.MaxTokens != 272000 {
		t.Fatalf("MaxTokens = %d, want the input limit 272000", cw.MaxTokens)
	}
}

func TestRegistryDefaultsExistInTheCatalog(t *testing.T) {
	for provider, info := range curatedProvidersInfo() {
		catalogID, native := "", false
		for id, p := range nativeProviders {
			if p == provider {
				catalogID, native = id, true
			}
		}
		if !native || len(info.Models) == 0 {
			continue
		}
		for _, m := range []ModelInfo{info.Models[0], info.Models[len(info.Models)-1]} {
			if _, ok := Catalog().Model(catalogID, m.Identifier); !ok {
				t.Errorf("%s: default model %q is not in the catalog", provider, m.Identifier)
			}
		}
	}
}

func TestProvidersWithoutACatalogURLAreCallableThroughTheEndpointTable(t *testing.T) {
	for _, id := range []string{"groq", "xai", "cerebras", "togetherai", "deepinfra", "cohere"} {
		p := types.APIProvider(id)
		if !IsCatalogProvider(p) {
			t.Errorf("%s is not a catalog provider", id)
			continue
		}
		cfg, ok := ConfigForCatalogProvider(p)
		if !ok || !strings.HasPrefix(cfg.BaseURL, "https://") {
			t.Errorf("%s: config = %+v, %v", id, cfg, ok)
		}
	}
}
