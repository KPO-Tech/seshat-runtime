package memory

import "github.com/KPO-Tech/seshat-runtime/internal/types"

// Config represents compaction configuration.
type Config struct {
	AutoCompactThreshold      float64 `json:"auto_compact_threshold"`
	CompactTargetPercentage   float64 `json:"compact_target_percentage"`
	MaxSummaryTokens          int     `json:"max_summary_tokens"`
	AutoCompactBufferTokens   int     `json:"auto_compact_buffer_tokens"`
	ManualCompactBufferTokens int     `json:"manual_compact_buffer_tokens"`
	MaxConsecutiveFailures    int     `json:"max_consecutive_failures"`
	// SummaryModel overrides which model generates the compaction summary.
	// The zero value means "use the session's own model" (Engine.generateSummary
	// falls back to the model passed into Compact/AutoCompact), so a session on
	// any provider can compact without needing a key for a different one.
	SummaryModel types.ModelIdentifier `json:"summary_model,omitempty"`

	// SummaryModelRules choose the model that writes the summary by the provider and model of the session, first match
	// wins. They apply when SummaryModel is not set, and before the built-in rules (see resolveSummaryModel).
	SummaryModelRules []SummaryModelRule `json:"summary_model_rules,omitempty"`

	// NoDefaultSummaryModels switches off the built-in rules, so that a session without a matching rule of its own
	// summarizes with its own model.
	NoDefaultSummaryModels bool `json:"no_default_summary_models,omitempty"`
}

// DefaultConfig returns default compaction configuration.
func DefaultConfig() *Config {
	return &Config{
		AutoCompactThreshold:      0.85,
		CompactTargetPercentage:   0.50,
		MaxSummaryTokens:          defaultMaxSummaryTokens,
		AutoCompactBufferTokens:   defaultAutoCompactBufferTokens,
		ManualCompactBufferTokens: defaultManualCompactBufferTokens,
		MaxConsecutiveFailures:    defaultMaxConsecutiveCompactFailure,
	}
}
