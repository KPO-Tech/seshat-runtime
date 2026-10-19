package providers

import (
	"sync"
	"testing"

	"github.com/KPO-Tech/seshat-runtime/internal/catalog"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// useFakeAnthropic installs a small catalog for the test and restores the real one after it.
func useFakeAnthropic(t *testing.T) {
	t.Helper()
	original := Catalog()
	t.Cleanup(func() { SetCatalog(original) })
	SetCatalog(&catalog.Catalog{Source: "test", Providers: map[string]catalog.Provider{
		"anthropic": {ID: "anthropic", Name: "Anthropic", Models: map[string]catalog.Model{
			"sonnet-2": {ID: "sonnet-2", Family: "claude-sonnet", ContextWindow: 1000, ReleaseDate: "2026-05-01", Cost: catalog.Cost{Input: 3, Output: 15}},
			"sonnet-1": {ID: "sonnet-1", Family: "claude-sonnet", ContextWindow: 1000, ReleaseDate: "2026-01-01", Cost: catalog.Cost{Input: 3, Output: 15}},
			"opus-2":   {ID: "opus-2", Family: "claude-opus", ContextWindow: 1000, ReleaseDate: "2026-04-01", Cost: catalog.Cost{Input: 15, Output: 75}},
			"haiku-2":  {ID: "haiku-2", Family: "claude-haiku", ContextWindow: 1000, ReleaseDate: "2026-03-01", Cost: catalog.Cost{Input: 1, Output: 5}},
			"haiku-1":  {ID: "haiku-1", Family: "claude-haiku", ContextWindow: 1000, ReleaseDate: "2026-02-01", Cost: catalog.Cost{Input: 0.5, Output: 2}},
		}},
	}})
}

func ends(t *testing.T, policy ModelPolicy) (large, small string) {
	t.Helper()
	info, ok := ProviderInfoWithPolicy(types.APIProviderAnthropic, policy)
	if !ok || len(info.Models) < 2 {
		t.Fatalf("no models: %+v", info)
	}
	return info.Models[0].Identifier, info.Models[len(info.Models)-1].Identifier
}

func TestHostPolicyOverridesTheRuntimePolicyPerSide(t *testing.T) {
	useFakeAnthropic(t)
	if large, small := ends(t, nil); large != "sonnet-2" || small != "haiku-2" {
		t.Fatalf("runtime policy: large %q small %q, want sonnet-2 / haiku-2", large, small)
	}
	// The host changes only the large side, by family: the small side stays the runtime's.
	if large, small := ends(t, ModelPolicy{types.APIProviderAnthropic: {Large: []string{"claude-opus"}}}); large != "opus-2" || small != "haiku-2" {
		t.Fatalf("family override: large %q small %q, want opus-2 / haiku-2", large, small)
	}
	// An exact id wins too, even over a newer model of the family.
	if large, small := ends(t, ModelPolicy{types.APIProviderAnthropic: {Large: []string{"sonnet-1"}, Small: []string{"haiku-1"}}}); large != "sonnet-1" || small != "haiku-1" {
		t.Fatalf("id override: large %q small %q, want sonnet-1 / haiku-1", large, small)
	}
}

// First point of attention: a host entry that names something the catalog no longer has must not break anything or leave a provider
// without defaults: it falls through to the runtime's policy.
func TestHostEntryThatTheCatalogDoesNotKnowFallsBackToTheRuntimePolicy(t *testing.T) {
	useFakeAnthropic(t)
	stale := ModelPolicy{types.APIProviderAnthropic: {Large: []string{"claude-retired-9", "no-such-family"}, Small: []string{"gone-model"}}}
	if large, small := ends(t, stale); large != "sonnet-2" || small != "haiku-2" {
		t.Fatalf("stale entries: large %q small %q, want the runtime's sonnet-2 / haiku-2", large, small)
	}
	// And a deprecated model named by id is not picked either.
	SetCatalog(&catalog.Catalog{Source: "test", Providers: map[string]catalog.Provider{
		"anthropic": {ID: "anthropic", Models: map[string]catalog.Model{
			"old": {ID: "old", ContextWindow: 1000, Status: "deprecated", ReleaseDate: "2025-01-01", Cost: catalog.Cost{Input: 1, Output: 1}},
			"a":   {ID: "a", ContextWindow: 1000, ReleaseDate: "2026-01-01", Cost: catalog.Cost{Input: 5, Output: 9}},
			"b":   {ID: "b", ContextWindow: 1000, ReleaseDate: "2026-02-01", Cost: catalog.Cost{Input: 1, Output: 2}},
		}},
	}})
	large, small := ends(t, ModelPolicy{types.APIProviderAnthropic: {Large: []string{"old"}}})
	if large == "old" || small == "old" {
		t.Fatalf("a deprecated model was picked: %q / %q", large, small)
	}
	// A provider with no entry anywhere still gets the price rule.
	if _, ok := DefaultModelWithPolicy(types.APIProviderAnthropic, ModelPolicy{"nobody": {Large: []string{"x"}}}); !ok {
		t.Fatal("a policy about another provider must not remove the defaults of this one")
	}
}

// Second point of attention: policies are values passed in. Two hosts in one process, running at the same time, each see their own
// choice, and neither changes the runtime's policy, the shared list or the other's result. Run with -race.
func TestPoliciesDoNotLeakBetweenHostsOrIntoSharedState(t *testing.T) {
	useFakeAnthropic(t)
	hostA := ModelPolicy{types.APIProviderAnthropic: {Large: []string{"claude-opus"}}}
	hostB := ModelPolicy{types.APIProviderAnthropic: {Large: []string{"sonnet-1"}}}
	sharedBefore := AllProvidersInfo()[types.APIProviderAnthropic].Models[0].Identifier
	runtimeBefore := append([]string(nil), runtimeModelPolicy["anthropic"].Large...)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(3)
		go func() {
			defer wg.Done()
			if large, _ := ends(t, hostA); large != "opus-2" {
				t.Errorf("host A saw %q", large)
			}
		}()
		go func() {
			defer wg.Done()
			if large, _ := ends(t, hostB); large != "sonnet-1" {
				t.Errorf("host B saw %q", large)
			}
		}()
		go func() {
			defer wg.Done()
			if large, _ := ends(t, nil); large != "sonnet-2" {
				t.Errorf("no policy saw %q", large)
			}
		}()
	}
	wg.Wait()

	if got := AllProvidersInfo()[types.APIProviderAnthropic].Models[0].Identifier; got != sharedBefore {
		t.Fatalf("the shared list changed from %q to %q", sharedBefore, got)
	}
	if got := runtimeModelPolicy["anthropic"].Large; len(got) != len(runtimeBefore) || got[0] != runtimeBefore[0] {
		t.Fatalf("the runtime policy was modified: %v", got)
	}
	// What a host gets from DefaultModelPolicy is a copy: editing it changes nothing shared.
	copyOfPolicy := DefaultModelPolicy()
	d := copyOfPolicy[types.APIProviderAnthropic]
	d.Large[0] = "tampered"
	if runtimeModelPolicy["anthropic"].Large[0] == "tampered" {
		t.Fatal("DefaultModelPolicy returned a slice that shares memory with the runtime's policy")
	}
	// The host's own policy is not modified by being used.
	if hostA[types.APIProviderAnthropic].Large[0] != "claude-opus" || len(hostA[types.APIProviderAnthropic].Large) != 1 {
		t.Fatalf("the host's policy was modified: %v", hostA)
	}
}

func TestSmallModelWithPolicy(t *testing.T) {
	useFakeAnthropic(t)
	if got, _ := SmallModelWithPolicy(types.APIProviderAnthropic, ModelPolicy{types.APIProviderAnthropic: {Small: []string{"claude-haiku"}}}); got != "haiku-2" {
		t.Fatalf("small = %q", got)
	}
}
