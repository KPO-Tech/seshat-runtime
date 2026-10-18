package memory

import "github.com/KPO-Tech/seshat-runtime/internal/types"

// TrackingState carries circuit-breaker state across loop iterations, and what the loop knows about the size of the
// request beyond the messages it hands over.
type TrackingState struct {
	ConsecutiveFailures int `json:"consecutive_failures"`

	// ExtraTokens is the size of the request outside the system prompt and the messages: the tool definitions, which
	// cost context on every request (about 36,000 tokens for the whole builtin registry).
	ExtraTokens int `json:"extra_tokens,omitempty"`

	// Manual marks a compaction the user asked for. The automatic rules assume the window is nearly full: they keep
	// half of it as it is, and skip the summary when trimming tool results is enough. A manual compaction is asked for
	// whatever the size, so it always summarizes and keeps only the most recent fifth of the conversation.
	Manual bool `json:"manual,omitempty"`

	// Anchor is the last size the provider reported. When set, the size of the request is that figure plus an estimate
	// of the messages added since, which is exact for everything the provider counted (tools included).
	Anchor *UsageAnchor `json:"anchor,omitempty"`
}

// UsageAnchor is the size of the conversation as the provider counted it, and how many messages that covered.
type UsageAnchor struct {
	Tokens       int `json:"tokens"`
	MessageCount int `json:"message_count"`
}

// ContextBudget is where a model's window stands against the compaction thresholds.
type ContextBudget struct {
	WindowTokens        int `json:"window_tokens"`
	AutoCompactAtTokens int `json:"auto_compact_at_tokens"`
	BlockingLimitTokens int `json:"blocking_limit_tokens"`
}

// CompactionResult is the canonical compaction outcome.
type CompactionResult struct {
	Messages            []types.Message           `json:"messages"`
	DidCompact          bool                      `json:"did_compact"`
	UsedMicroCompact    bool                      `json:"used_micro_compact"`
	UsedSummaryCompact  bool                      `json:"used_summary_compact"`
	PreCompactTokens    int                       `json:"pre_compact_tokens"`
	PostCompactTokens   int                       `json:"post_compact_tokens"`
	TargetTokens        int                       `json:"target_tokens"`
	Summary             string                    `json:"summary,omitempty"`
	ConsecutiveFailures int                       `json:"consecutive_failures"`
	RuntimeMetadata     *types.CompactionMetadata `json:"runtime_metadata,omitempty"`
}
