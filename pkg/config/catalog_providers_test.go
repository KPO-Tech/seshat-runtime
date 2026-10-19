package config

import (
	"testing"

	"github.com/KPO-Tech/seshat-runtime/pkg/sdk"
)

// aCatalogProviderWithAKeyVariable returns the id and key variables of a generic catalog provider that names one.
func aCatalogProviderWithAKeyVariable(t *testing.T) (sdk.APIProvider, []string) {
	t.Helper()
	for _, p := range AvailableCatalogProviders() {
		if envs := ProviderCredentialEnvVars(p.Name); len(envs) > 0 && len(p.Models) > 0 {
			return p.Name, envs
		}
	}
	t.Fatal("no generic provider with a key variable and a model")
	return "", nil
}

func TestCatalogProvidersAreListedApartFromTheBuiltInOnes(t *testing.T) {
	generic := AvailableCatalogProviders()
	if len(generic) < 100 {
		t.Fatalf("%d generic providers, want at least 100", len(generic))
	}
	builtIn := map[sdk.APIProvider]bool{}
	for _, p := range AvailableProviders() {
		builtIn[p.Name] = true
	}
	for _, p := range generic {
		if builtIn[p.Name] {
			t.Errorf("%s is both built in and generic", p.Name)
		}
	}
	if len(AvailableProviders()) != len(providerOrder) {
		t.Errorf("AvailableProviders lists %d providers, want the %d built-in ones only: a host that lists providers must not suddenly list 190 more", len(AvailableProviders()), len(providerOrder))
	}
}

func TestAModelStringCanNameACatalogProvider(t *testing.T) {
	provider, _ := aCatalogProviderWithAKeyVariable(t)
	got := ParseModelIdentifier(string(provider) + ":some-model")
	if got.Provider != provider || got.Model != "some-model" {
		t.Fatalf("ParseModelIdentifier = %+v, want provider %s and model some-model (it used to fall back to Anthropic with the whole string as the model)", got, provider)
	}
	if !HasExplicitProviderPrefix(string(provider) + ":some-model") {
		t.Fatal("the provider prefix must be recognised")
	}
	if ResolveProvider("no-such-provider-xyz") != "" {
		t.Fatal("an unknown name must still resolve to nothing")
	}
	if got := ParseModelIdentifier("claude:claude-sonnet-5-5"); got.Provider != sdk.APIProviderAnthropic {
		t.Fatalf("built-in aliases must keep working, got %+v", got)
	}
}

func TestACatalogProviderNeedsItsKeyVariable(t *testing.T) {
	provider, envs := aCatalogProviderWithAKeyVariable(t)
	for _, env := range envs {
		t.Setenv(env, "")
	}
	if err := ValidateProviderSetup(Config{}, provider); err == nil {
		t.Fatalf("%s needs an API key: without one the setup must fail, not pass as a provider that needs none", provider)
	}
	t.Setenv(envs[0], "sk-test")
	if err := ValidateProviderSetup(Config{}, provider); err != nil {
		t.Fatalf("with %s set the setup must pass: %v", envs[0], err)
	}
	if key := ResolveAPIKey(Config{}, provider); key != "sk-test" {
		t.Fatalf("ResolveAPIKey = %q, want the key of %s", key, envs[0])
	}
}

func TestCatalogProviderSetupFieldNamesTheCatalogVariable(t *testing.T) {
	provider, envs := aCatalogProviderWithAKeyVariable(t)
	info := ProviderInfo{}
	for _, p := range AvailableCatalogProviders() {
		if p.Name == provider {
			info = p
		}
	}
	if len(info.SetupFields) == 0 || info.SetupFields[0].EnvVar != envs[0] || !info.SetupFields[0].Required || !info.SetupFields[0].Secret {
		t.Fatalf("setup fields = %+v, want a required secret API key field naming %s", info.SetupFields, envs[0])
	}
}
