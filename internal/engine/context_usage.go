package engine

import (
	"context"
	"encoding/json"
	"log/slog"

	compact "github.com/KPO-Tech/seshat-runtime/internal/runtime/memory"
	contextusage "github.com/KPO-Tech/seshat-runtime/internal/tools/special/context_usage"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

const (
	contextUsageSourceEstimate = "estimate"
	contextUsageSourceReported = "reported"

	// bytesPerToken is the rough ratio used to size tool definitions, which have no provider-side count before the
	// first response.
	bytesPerToken = 4
)

// usageReporter is what a compaction strategy offers to report where the window stands. The default Engine has it; a
// strategy that does not simply reports nothing.
type usageReporter interface {
	UsedTokens(systemPrompt string, messages []types.Message, tracking *compact.TrackingState) int
	Budget(model types.ModelIdentifier) compact.ContextBudget
	ShouldCompact(systemPrompt string, messages []types.Message, model types.ModelIdentifier, tracking *compact.TrackingState) bool
}

// observationMasker is what a compaction strategy offers to clear old tool results before summarizing. The default
// Engine has it.
type observationMasker interface {
	MaskObservations(messages []types.Message) ([]types.Message, compact.MaskResult)
}

// maskAtPercent is how close to the automatic compaction threshold, in percent, the conversation must be for old tool
// results to be cleared. Masking runs well before summarizing: it is cheaper and loses less.
const maskAtPercent = 70

// maybeMaskObservations clears old tool results once the conversation nears the compaction threshold. The messages are
// changed in place, so the result holds from then on (a cleared result is small and is not cleared again) instead of
// flipping back and forth as the size crosses the line.
func (l *Loop) maybeMaskObservations(state *MutableState, req RunRequest) {
	if !l.config.AutoCompact {
		return
	}
	reporter, ok := l.compactor.(usageReporter)
	masker, canMask := l.compactor.(observationMasker)
	if !ok || !canMask {
		return
	}
	budget := reporter.Budget(req.Model)
	if budget.AutoCompactAtTokens <= 0 {
		return
	}
	used := reporter.UsedTokens(req.SystemPrompt, state.Messages, compactionTracking(state, req))
	if used*100 < budget.AutoCompactAtTokens*maskAtPercent {
		return
	}
	masked, result := masker.MaskObservations(state.Messages)
	if result.Cleared == 0 {
		return
	}
	state.Messages = masked
	// The provider's figure described the messages as they were before.
	state.UsageAnchor = nil
	slog.Info("old tool results cleared to save context", "session_id", req.SessionID, "results", result.Cleared, "freed_chars", result.FreedChars)
}

// toolDefinitionTokens estimates what the tool definitions of a request cost, to be added to the messages' size.
func toolDefinitionTokens(tools []types.APIToolDefinition) int {
	if len(tools) == 0 {
		return 0
	}
	raw, err := json.Marshal(tools)
	if err != nil {
		return 0
	}
	return len(raw) / bytesPerToken
}

// compactionTracking is what the compaction strategy needs to size the next request: the failure count, the cost of
// the tool definitions, and the last size the provider reported.
func compactionTracking(state *MutableState, req RunRequest) *compact.TrackingState {
	return &compact.TrackingState{
		ConsecutiveFailures: state.AutoCompactFailureCount,
		ExtraTokens:         toolDefinitionTokens(req.ProviderTools),
		Anchor:              state.UsageAnchor,
	}
}

// recordUsageAnchor remembers the size the provider reported for the request just answered, plus the answer itself,
// and how many messages that covers. Not every provider reports usage; a zero figure leaves the anchor unset.
func recordUsageAnchor(state *MutableState, usage *types.TokenUsage) {
	state.UsageAnchor = nil
	if usage == nil {
		return
	}
	// Anthropic reports cache reads and writes apart from input tokens; for the other providers those are zero.
	tokens := usage.InputTokens + usage.CacheReadInputTokens + usage.CacheCreationInputTokens + usage.OutputTokens
	if tokens <= 0 {
		return
	}
	state.UsageAnchor = &compact.UsageAnchor{Tokens: tokens, MessageCount: len(state.Messages)}
}

// emitContextUsage tells the host how full the window is. It does nothing without an emitter or a strategy that can
// report.
func (l *Loop) emitContextUsage(ctx context.Context, state *MutableState, req RunRequest) {
	emit, ok := ctx.Value(types.RuntimeEventEmitterKey).(func(types.RuntimeEvent))
	if !ok || emit == nil {
		return
	}
	reporter, ok := l.compactor.(usageReporter)
	if !ok {
		return
	}
	source := contextUsageSourceEstimate
	if state.UsageAnchor != nil {
		source = contextUsageSourceReported
	}
	budget := reporter.Budget(req.Model)
	if budget.WindowTokens <= 0 {
		return
	}
	usage := &types.ContextUsageRuntimeEvent{
		UsedTokens:          reporter.UsedTokens(req.SystemPrompt, state.Messages, compactionTracking(state, req)),
		WindowTokens:        budget.WindowTokens,
		AutoCompactAtTokens: budget.AutoCompactAtTokens,
		BlockingLimitTokens: budget.BlockingLimitTokens,
		Source:              source,
	}
	contextusage.Record(req.SessionID, contextusage.Snapshot{
		UsedTokens:          usage.UsedTokens,
		WindowTokens:        usage.WindowTokens,
		AutoCompactAtTokens: usage.AutoCompactAtTokens,
		BlockingLimitTokens: usage.BlockingLimitTokens,
		Source:              usage.Source,
	})
	emit(types.RuntimeEvent{
		Type:              types.RuntimeEventTypeContextUsage,
		SessionID:         req.SessionID,
		TurnID:            req.TurnID,
		ContextUsageEvent: usage,
	})
}

// runtimeEventEmitter returns the host's emitter carried by ctx, if any.
func runtimeEventEmitter(ctx context.Context) func(types.RuntimeEvent) {
	emit, _ := ctx.Value(types.RuntimeEventEmitterKey).(func(types.RuntimeEvent))
	return emit
}

// emitCompacting tells the host that a compaction is about to run, when the strategy says one is due.
func (l *Loop) emitCompacting(ctx context.Context, state *MutableState, req RunRequest) {
	emit := runtimeEventEmitter(ctx)
	reporter, ok := l.compactor.(usageReporter)
	if emit == nil || !ok {
		return
	}
	tracking := compactionTracking(state, req)
	if !reporter.ShouldCompact(req.SystemPrompt, state.Messages, req.Model, tracking) {
		return
	}
	emit(types.RuntimeEvent{
		Type:      types.RuntimeEventTypeCompacting,
		SessionID: req.SessionID,
		TurnID:    req.TurnID,
		CompactionEvent: &types.CompactionRuntimeEvent{
			PreCompactTokens: reporter.UsedTokens(req.SystemPrompt, state.Messages, tracking),
		},
	})
}

// emitCompacted tells the host that the conversation was compacted, as soon as it was rather than at the end of the
// turn.
func (l *Loop) emitCompacted(ctx context.Context, req RunRequest, pre, post int) {
	emit := runtimeEventEmitter(ctx)
	if emit == nil {
		return
	}
	emit(types.RuntimeEvent{
		Type:            types.RuntimeEventTypeCompaction,
		SessionID:       req.SessionID,
		TurnID:          req.TurnID,
		CompactionEvent: &types.CompactionRuntimeEvent{PreCompactTokens: pre, PostCompactTokens: post},
	})
}
