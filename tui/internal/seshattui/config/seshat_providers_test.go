package config

import "testing"

func TestKimiProviderUsesInternationalEndpoint(t *testing.T) {
	if got := apiEndpointFor("kimi"); got != "https://api.moonshot.ai/v1" {
		t.Fatalf("apiEndpointFor(kimi) = %q, want https://api.moonshot.ai/v1", got)
	}
	providers := buildSeshatProviders(nil)
	found := false
	for _, provider := range providers {
		if provider.ID == "kimi" {
			found = true
			if provider.APIEndpoint != "https://api.moonshot.ai/v1" {
				t.Fatalf("Kimi APIEndpoint = %q, want https://api.moonshot.ai/v1", provider.APIEndpoint)
			}
			if len(provider.Models) == 0 {
				t.Fatal("expected Kimi models in TUI provider catalog")
			}
		}
	}
	if !found {
		t.Fatal("expected Kimi in TUI provider catalog")
	}
}
