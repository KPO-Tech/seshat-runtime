package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	compact "github.com/KPO-Tech/seshat-runtime/internal/runtime/memory"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// minMessagesToCompact is the shortest conversation worth compacting: a request and its answer leave nothing to
// summarize.
const minMessagesToCompact = 4

// ErrNothingToCompact is returned by Session.Compact when the conversation is too short to shrink.
var ErrNothingToCompact = errors.New("the conversation is too short to compact")

// ErrCompactionWouldNotShrink is returned by Session.Compact when summarizing would leave the conversation no smaller,
// which happens when it is small. The conversation is left as it is.
var ErrCompactionWouldNotShrink = errors.New("compacting would not make the conversation smaller")

// manualCompactor is what a compaction strategy offers to compact on demand. The default Engine has it.
type manualCompactor interface {
	Compact(
		ctx context.Context,
		systemPrompt string,
		messages []types.Message,
		model types.ModelIdentifier,
		sessionID types.SessionID,
		turnID types.TurnID,
		tracking *compact.TrackingState,
	) (compact.CompactionResult, error)
}

// CompactionOutcome is what a manual compaction did.
type CompactionOutcome struct {
	PreTokens  int
	PostTokens int
}

// Compact summarizes the older part of the conversation now, instead of waiting for the automatic threshold. It runs
// like a turn: it fails when one is in progress, and it emits context.compacting then context.compacted.
func (s *Session) Compact(ctx context.Context) (CompactionOutcome, error) {
	if err := s.beginTurn(); err != nil {
		return CompactionOutcome{}, err
	}
	defer s.endTurn()

	compactor, ok := s.engine.loop.compactor.(manualCompactor)
	if !ok || s.engine.loop.compactor == nil {
		return CompactionOutcome{}, errors.New("this session has no compactor")
	}
	previous := s.state.CloneMessages()
	if len(previous) < minMessagesToCompact {
		return CompactionOutcome{}, ErrNothingToCompact
	}
	apiReq, err := s.buildAPIRequest(ctx)
	if err != nil {
		return CompactionOutcome{}, fmt.Errorf("failed to build the request to size: %w", err)
	}
	tracking := &compact.TrackingState{ExtraTokens: toolDefinitionTokens(apiReq.Tools), Manual: true}

	s.emitRuntimeEvent(types.RuntimeEvent{
		Type:      types.RuntimeEventTypeCompacting,
		SessionID: s.state.SessionID,
		TurnID:    s.state.TurnID,
	})
	result, err := compactor.Compact(ctx, apiReq.SystemPrompt, previous, s.config.Model, s.state.SessionID, s.state.TurnID, tracking)
	if err != nil {
		return CompactionOutcome{}, fmt.Errorf("compaction failed: %w", err)
	}
	slog.Debug("manual compaction", "session_id", s.state.SessionID, "messages_before", len(previous), "messages_after", len(result.Messages),
		"pre_tokens", result.PreCompactTokens, "post_tokens", result.PostCompactTokens, "summary_chars", len(result.Summary))
	if result.PostCompactTokens >= result.PreCompactTokens {
		return CompactionOutcome{PreTokens: result.PreCompactTokens, PostTokens: result.PostCompactTokens}, ErrCompactionWouldNotShrink
	}
	s.state.ReplaceMessages(result.Messages)
	if err := s.persistSessionState(previous); err != nil {
		return CompactionOutcome{}, fmt.Errorf("failed to persist the compacted session: %w", err)
	}
	s.emitRuntimeEvent(types.RuntimeEvent{
		Type:            types.RuntimeEventTypeCompaction,
		SessionID:       s.state.SessionID,
		TurnID:          s.state.TurnID,
		CompactionEvent: &types.CompactionRuntimeEvent{PreCompactTokens: result.PreCompactTokens, PostCompactTokens: result.PostCompactTokens},
	})
	return CompactionOutcome{PreTokens: result.PreCompactTokens, PostTokens: result.PostCompactTokens}, nil
}
