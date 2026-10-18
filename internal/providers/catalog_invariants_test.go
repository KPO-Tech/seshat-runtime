package providers

import (
	"testing"

	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// TestCatalogEntriesAreWellFormed guards the hand-maintained catalog: it feeds the context windows that decide when a
// conversation is compacted, and the output limits requests are sent with.
func TestCatalogEntriesAreWellFormed(t *testing.T) {
	for provider, info := range AllProvidersInfo() {
		seen := map[string]bool{}
		for _, m := range info.Models {
			if m.Identifier == "" {
				t.Errorf("%s: a model without an identifier", provider)
				continue
			}
			if seen[m.Identifier] {
				t.Errorf("%s: %s is listed twice", provider, m.Identifier)
			}
			seen[m.Identifier] = true
			if m.ContextWindow <= 0 || m.MaxOutput <= 0 {
				t.Errorf("%s/%s: context %d and output %d must both be positive", provider, m.Identifier, m.ContextWindow, m.MaxOutput)
			}
			if m.MaxOutput > m.ContextWindow {
				t.Errorf("%s/%s: output %d is larger than the context window %d", provider, m.Identifier, m.MaxOutput, m.ContextWindow)
			}
			if m.DefaultTemperature <= 0 {
				t.Errorf("%s/%s: no default temperature", provider, m.Identifier)
			}
		}
	}
}

// TestSummaryModelsOfTheDefaultCompactionRulesAreInTheCatalog: compaction picks a small model of the session's family by rule
// (z.ai sessions get glm-4.5-air, Anthropic sessions claude-haiku-5-5). A model the catalog does not know has no known
// window, and the rule would be skipped.
func TestSummaryModelsOfTheDefaultCompactionRulesAreInTheCatalog(t *testing.T) {
	for _, want := range []struct {
		provider types.APIProvider
		model    string
	}{
		{types.APIProviderZAi, "glm-4.5-air"},
		{types.APIProviderAnthropic, "claude-haiku-5-5"},
	} {
		if _, ok := GetModelInfo(want.provider, want.model); !ok {
			t.Errorf("%s/%s is the summary model of a default compaction rule but is not in the catalog", want.provider, want.model)
		}
	}
}
