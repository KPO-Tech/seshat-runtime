package sdk

import (
	"fmt"

	compact "github.com/KPO-Tech/seshat-runtime/internal/runtime/memory"
)

// buildCompactConfig is the compaction configuration of a client: the defaults, the summary model and the shares of
// the window the configuration sets.
func buildCompactConfig(config *ClientConfig) (*compact.Config, error) {
	cfg := compact.DefaultConfig()
	cfg.SummaryModel = config.CompactSummaryModel
	if err := applyCompactionShares(cfg, config.CompactThreshold, config.CompactTarget); err != nil {
		return nil, err
	}
	return cfg, nil
}

// applyCompactionShares sets the compaction threshold and target from the client configuration. A zero keeps the
// default; a value outside (0, 1), or a target not below the threshold, is refused rather than silently ignored.
func applyCompactionShares(cfg *compact.Config, threshold, target float64) error {
	if threshold < 0 || threshold >= 1 {
		return fmt.Errorf("compact_threshold must be between 0 and 1 (exclusive of 1), got %v", threshold)
	}
	if target < 0 || target >= 1 {
		return fmt.Errorf("compact_target must be between 0 and 1 (exclusive of 1), got %v", target)
	}
	if threshold > 0 {
		cfg.AutoCompactThreshold = threshold
	}
	if target > 0 {
		cfg.CompactTargetPercentage = target
	}
	if cfg.CompactTargetPercentage >= cfg.AutoCompactThreshold {
		return fmt.Errorf("compact_target (%v) must be below compact_threshold (%v)", cfg.CompactTargetPercentage, cfg.AutoCompactThreshold)
	}
	return nil
}
