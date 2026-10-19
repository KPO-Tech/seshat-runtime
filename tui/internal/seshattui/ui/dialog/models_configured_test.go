package dialog

import (
	"testing"

	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/config"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/csync"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/ui/common"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/ui/styles"
)

func modelGroupTitles(t *testing.T, cfg *config.Config, onboarding bool) []string {
	t.Helper()
	st := styles.SeshatDark()
	com := &common.Common{Workspace: panelStubWorkspace{cfg: cfg}, Styles: &st}
	m, err := NewModels(com, onboarding, "")
	if err != nil {
		t.Fatalf("NewModels: %v", err)
	}
	var titles []string
	for _, g := range m.list.groups {
		titles = append(titles, g.Title)
	}
	return titles
}

func newModelsConfig() *config.Config {
	return &config.Config{
		Providers:    csync.NewMap[string, config.ProviderConfig](),
		Models:       map[config.SelectedModelType]config.SelectedModel{},
		RecentModels: map[config.SelectedModelType][]config.SelectedModel{},
	}
}

func TestModelPickerListsOnlyTheConfiguredProviders(t *testing.T) {
	cfg := newModelsConfig()
	cfg.Providers.Set("z-ai", config.ProviderConfig{ID: "z-ai", APIKey: "key"})

	titles := modelGroupTitles(t, cfg, false)
	if len(titles) != 1 {
		t.Fatalf("groups = %v, want only the configured provider", titles)
	}
}

func TestModelPickerOffersTheMainProvidersWhenNothingIsConfigured(t *testing.T) {
	titles := modelGroupTitles(t, newModelsConfig(), false)
	mainCount := len(config.MainProviderIDs())
	if len(titles) == 0 || len(titles) > mainCount {
		t.Fatalf("groups = %d, want the main providers (at most %d), not the whole catalog", len(titles), mainCount)
	}
}

func TestOnboardingOffersTheMainProvidersEvenWhenOneIsConfigured(t *testing.T) {
	cfg := newModelsConfig()
	cfg.Providers.Set("z-ai", config.ProviderConfig{ID: "z-ai", APIKey: "key"})
	titles := modelGroupTitles(t, cfg, true)
	if len(titles) <= 1 {
		t.Fatalf("onboarding must offer the main providers to choose from, got %v", titles)
	}
}
