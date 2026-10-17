package memory

import (
	"context"

	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// CompactionStrategy abstracts the compaction algorithm used by the query loop.
// Callers pass the current conversation state; the strategy decides whether and
// how to compact, returning a canonical CompactionResult.
//
// The Engine type is the default implementation. Swap the strategy in tests or
// to add new algorithms (e.g., importance-weighted, semantic chunking) without
// touching the loop.
type CompactionStrategy interface {
	// AutoCompact compacts messages if the token budget is exceeded.
	// It respects the circuit breaker via TrackingState.ConsecutiveFailures.
	// When no compaction is needed it returns the original messages unchanged
	// with DidCompact == false.
	AutoCompact(
		ctx context.Context,
		systemPrompt string,
		messages []types.Message,
		model types.ModelIdentifier,
		sessionID types.SessionID,
		turnID types.TurnID,
		tracking *TrackingState,
	) (CompactionResult, error)

	// CheckBlockingLimit reports whether systemPrompt+messages fit within the
	// hard blocking limit for model (BlockingLimitTokens), returning a typed
	// error (ErrCodeCompactFull) if not - even after AutoCompact has already
	// tried. Called right after AutoCompact so an oversized request fails
	// locally with a clear reason instead of at the provider (M1 in TODO.md).
	CheckBlockingLimit(systemPrompt string, messages []types.Message, model types.ModelIdentifier, tracking *TrackingState) error
}

// Verify Engine satisfies CompactionStrategy at compile time.
var _ CompactionStrategy = (*Engine)(nil)
