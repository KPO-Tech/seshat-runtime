package config

import "testing"

// TestProvidersKeepRegistryOrderAndDefaults guards the order of the model lists: the first model of a provider is its default, the
// last its small model. The catalog view in pkg/config sorts models by name, and the dialog used to take its defaults from that
// sorted list.
func TestProvidersKeepRegistryOrderAndDefaults(t *testing.T) {
	list, err := Providers(nil)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string][]string{}
	large := map[string]string{}
	small := map[string]string{}
	for _, p := range list {
		var ids []string
		for _, m := range p.Models {
			ids = append(ids, m.ID)
		}
		byID[string(p.ID)] = ids
		large[string(p.ID)] = p.DefaultLargeModelID
		small[string(p.ID)] = p.DefaultSmallModelID
	}

	// The registry names no model: the lists are the catalog's. The contract is structural: the default large model is the first
	// of the list, the small one is in the list, and a provider with two models uses the same for both.
	for _, id := range []string{"anthropic", "openai", "z-ai", "gemini", "deepseek"} {
		ids := byID[id]
		if len(ids) == 0 {
			t.Errorf("%s has no model", id)
			continue
		}
		if large[id] != ids[0] {
			t.Errorf("%s: default large model %q is not the first of the list %v", id, large[id], ids)
		}
		found := false
		for _, m := range ids {
			found = found || m == small[id]
		}
		if !found {
			t.Errorf("%s: default small model %q is not in the list", id, small[id])
		}
		if len(ids) <= 2 && small[id] != large[id] {
			t.Errorf("%s: with %d models the same one serves both, got %q / %q", id, len(ids), large[id], small[id])
		}
	}
}

// model_defaults in the configuration changes the default of one provider and nothing else.
func TestModelDefaultsOfTheConfigurationReachTheProviderList(t *testing.T) {
	cfg := &Config{Options: &Options{ModelDefaults: map[string]ModelDefaultsEntry{"anthropic": {Large: []string{"claude-opus"}}}}}
	policy := modelPolicyFrom(cfg)
	if modelPolicyFrom(&Config{Options: &Options{}}) != nil || modelPolicyFrom(nil) != nil {
		t.Fatal("no model_defaults, no policy")
	}
	var plain, changed []string
	for _, p := range buildSeshatProviders(nil) {
		if p.ID == "anthropic" {
			plain = []string{p.DefaultLargeModelID, p.DefaultSmallModelID}
		}
	}
	for _, p := range buildSeshatProviders(policy) {
		if p.ID == "anthropic" {
			changed = []string{p.DefaultLargeModelID, p.DefaultSmallModelID}
		}
	}
	if plain[0] == changed[0] || plain[1] != changed[1] {
		t.Fatalf("plain %v, with an opus large default %v: only the large model should change", plain, changed)
	}
}
