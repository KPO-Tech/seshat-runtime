package sdk

import (
	"testing"

	compact "github.com/KPO-Tech/seshat-runtime/internal/runtime/memory"
)

func TestApplyCompactionSharesKeepsDefaultsAtZero(t *testing.T) {
	cfg := compact.DefaultConfig()
	if err := applyCompactionShares(cfg, 0, 0); err != nil {
		t.Fatal(err)
	}
	if cfg.AutoCompactThreshold != compact.DefaultConfig().AutoCompactThreshold || cfg.CompactTargetPercentage != compact.DefaultConfig().CompactTargetPercentage {
		t.Errorf("defaults changed: %+v", cfg)
	}
}

func TestApplyCompactionSharesSetsAndValidates(t *testing.T) {
	cfg := compact.DefaultConfig()
	if err := applyCompactionShares(cfg, 0.7, 0.3); err != nil {
		t.Fatal(err)
	}
	if cfg.AutoCompactThreshold != 0.7 || cfg.CompactTargetPercentage != 0.3 {
		t.Errorf("not applied: %+v", cfg)
	}
	for _, c := range []struct{ th, tg float64 }{{1, 0}, {-0.1, 0}, {0, 1.2}, {0.4, 0.4}, {0.4, 0.6}, {0, 0.9}} {
		if err := applyCompactionShares(compact.DefaultConfig(), c.th, c.tg); err == nil {
			t.Errorf("threshold %v target %v must be refused", c.th, c.tg)
		}
	}
}

func TestCompactModelRulesReachTheCompactionConfig(t *testing.T) {
	cfg, err := buildCompactConfig(&ClientConfig{
		NoDefaultCompactModels: true,
		CompactModelRules: []CompactModelRule{
			{Provider: "zai", Model: "glm-*", Use: ModelIdentifier{Model: "glm-4.5-air"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.NoDefaultSummaryModels || len(cfg.SummaryModelRules) != 1 {
		t.Fatalf("rules not passed on: %+v", cfg)
	}
	r := cfg.SummaryModelRules[0]
	if r.Provider != "zai" || r.Model != "glm-*" || r.Use.Model != "glm-4.5-air" {
		t.Errorf("rule changed on the way: %+v", r)
	}
}
