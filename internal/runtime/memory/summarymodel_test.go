package memory

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/KPO-Tech/seshat-runtime/internal/providers"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

func id(provider types.APIProvider, model string) types.ModelIdentifier {
	return types.ModelIdentifier{Provider: provider, Model: model}
}

func TestARuleMatchesByProviderAndModel(t *testing.T) {
	session := id(types.APIProviderZAi, "glm-4.7")
	cases := []struct {
		rule SummaryModelRule
		want bool
	}{
		{SummaryModelRule{Provider: types.APIProviderZAi, Model: "glm-*"}, true},
		{SummaryModelRule{Provider: types.APIProviderZAi, Model: "glm-4.7"}, true},
		{SummaryModelRule{Provider: types.APIProviderZAi}, true},
		{SummaryModelRule{Model: "glm-*"}, true},
		{SummaryModelRule{}, true},
		{SummaryModelRule{Provider: types.APIProviderZAi, Model: "glm-4.5"}, false},
		{SummaryModelRule{Provider: types.APIProviderZAi, Model: "claude-*"}, false},
		{SummaryModelRule{Provider: types.APIProviderOpenAI, Model: "glm-*"}, false},
	}
	for _, c := range cases {
		if got := c.rule.matches(session); got != c.want {
			t.Errorf("rule %+v on %s: matches = %v, want %v", c.rule, session.Model, got, c.want)
		}
	}
}

func TestResolutionOrderIsExplicitThenRulesThenDefaultsThenTheSession(t *testing.T) {
	zai := id(types.APIProviderZAi, "glm-4.7")

	// Built-in: a z.ai session summarizes with glm-4.5-air, on z.ai.
	e := NewEngine(nil, DefaultConfig())
	if got := e.resolveSummaryModel(zai, 100_000); got != id(types.APIProviderZAi, "glm-4.5-air") {
		t.Errorf("default for z.ai = %v", got)
	}
	if got := e.resolveSummaryModel(id(types.APIProviderAnthropic, "claude-opus-5-5"), 100_000); got != id(types.APIProviderAnthropic, "claude-haiku-5-5") {
		t.Errorf("default for Anthropic = %v", got)
	}
	// A provider with no default and no rule keeps its own model.
	if got := e.resolveSummaryModel(id(types.APIProviderOpenAI, "gpt-5.5"), 100_000); got != id(types.APIProviderOpenAI, "gpt-5.5") {
		t.Errorf("a provider without a rule keeps its own model, got %v", got)
	}

	// The configuration's own rules come before the built-in ones.
	cfg := DefaultConfig()
	cfg.SummaryModelRules = []SummaryModelRule{{Provider: types.APIProviderZAi, Model: "glm-4.7", Use: types.ModelIdentifier{Model: "glm-4.5"}}}
	if got := NewEngine(nil, cfg).resolveSummaryModel(zai, 100_000); got != id(types.APIProviderZAi, "glm-4.5") {
		t.Errorf("a user rule must win over the built-in one, got %v", got)
	}

	// The explicit model wins over everything.
	cfg.SummaryModel = id(types.APIProviderZAi, "glm-5.3-flash")
	if got := NewEngine(nil, cfg).resolveSummaryModel(zai, 100_000); got != id(types.APIProviderZAi, "glm-5.3-flash") {
		t.Errorf("the explicit summary model must win, got %v", got)
	}

	// The built-in rules can be switched off.
	off := DefaultConfig()
	off.NoDefaultSummaryModels = true
	if got := NewEngine(nil, off).resolveSummaryModel(zai, 100_000); got != zai {
		t.Errorf("with the defaults off the session keeps its own model, got %v", got)
	}
}

func TestARuleStaysOnTheSessionsProviderUnlessItNamesAnother(t *testing.T) {
	cfg := DefaultConfig()
	cfg.NoDefaultSummaryModels = true
	cfg.SummaryModelRules = []SummaryModelRule{
		{Provider: types.APIProviderZAi, Model: "glm-*", Use: types.ModelIdentifier{Model: "glm-4.5-air"}},
		{Provider: types.APIProviderOpenAI, Model: "gpt-*", Use: id(types.APIProviderMistral, "mistral-small-latest")},
	}
	e := NewEngine(nil, cfg)
	if got := e.resolveSummaryModel(id(types.APIProviderZAi, "glm-5.3"), 1_000); got.Provider != types.APIProviderZAi {
		t.Errorf("a rule without a provider must stay on the session's provider, got %v", got)
	}
	if got := e.resolveSummaryModel(id(types.APIProviderOpenAI, "gpt-5.5"), 1_000); got.Provider != types.APIProviderMistral {
		t.Errorf("a rule that names another provider is an explicit choice and is followed, got %v", got)
	}
}

func TestARuleModelWithTooSmallAWindowIsSkipped(t *testing.T) {
	// glm-4.5-air has a 128k window; a request of 120,000 tokens plus the summary does not fit it.
	e := NewEngine(nil, DefaultConfig())
	zai := id(types.APIProviderZAi, "glm-5.3-flash") // a 1M window: the session itself can hold it
	if got := e.resolveSummaryModel(zai, 120_000); got != zai {
		t.Errorf("a cheaper model that cannot hold the request must not be used, got %v", got)
	}
	if got := e.resolveSummaryModel(zai, 50_000); got.Model != "glm-4.5-air" {
		t.Errorf("when the request fits the cheaper model it is used, got %v", got)
	}
	// The explicit model is not second-guessed: it is the user's choice, and the request is shrunk to it.
	cfg := DefaultConfig()
	cfg.SummaryModel = id(types.APIProviderZAi, "glm-4.5-air")
	if got := NewEngine(nil, cfg).resolveSummaryModel(zai, 120_000); got.Model != "glm-4.5-air" {
		t.Errorf("an explicit model is kept whatever the size, got %v", got)
	}
}

func TestTheSummaryIsRequestedFromTheResolvedModel(t *testing.T) {
	var mu sync.Mutex
	var models []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		models = append(models, body.Model)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"r","choices":[{"message":{"role":"assistant","content":"1. a\n2. b\n3. c\n4. d\n5. e\n6. f"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	client := providers.NewClientWithConfig("k", &providers.Config{Provider: types.APIProviderOpenAI, BaseURL: server.URL})
	client.SetHTTPClient(server.Client())

	cfg := DefaultConfig()
	cfg.SummaryModelRules = []SummaryModelRule{{Provider: types.APIProviderOpenAI, Model: "gpt-4o", Use: types.ModelIdentifier{Model: "gpt-4o-mini"}}}
	e := NewEngine(client, cfg)
	if _, err := e.generateSummary(t.Context(), []types.Message{types.UserMessage("m1", "hello")}, id(types.APIProviderOpenAI, "gpt-4o"), "s", "t"); err != nil {
		t.Fatal(err)
	}
	if len(models) == 0 || models[0] != "gpt-4o-mini" {
		t.Errorf("the server received model %v, want gpt-4o-mini from the rule", models)
	}
}
