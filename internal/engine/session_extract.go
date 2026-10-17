package engine

import (
	"context"
	"log/slog"
	"time"

	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// memoryExtractionTimeout bounds one extraction, which runs after the session is closed and must not hang on a slow
// provider.
const memoryExtractionTimeout = 90 * time.Second

// defaultMemoryUser is the owner of memories when no authenticated user is known (a single-user terminal session), as
// the memory tools do.
const defaultMemoryUser = "local"

// MemoryExtractor reads a finished conversation and stores what is worth remembering. The default is
// longterm.Extractor.
type MemoryExtractor interface {
	Extract(ctx context.Context, userID string, messages []types.Message) error
}

// rememberUserID keeps the authenticated user of this turn, if the host carried one in ctx.
func (s *Session) rememberUserID(ctx context.Context) {
	if id := types.AgentUserIDFromContext(ctx); id != "" {
		s.userID = id
	}
}

// extractMemories hands the closed session's conversation to the configured extractor, in the background: closing
// must not wait for a model call. A failure is logged and otherwise ignored.
func (s *Session) extractMemories(messages []types.Message) {
	if s == nil || s.engine == nil || s.engine.config == nil || s.engine.config.MemoryExtractor == nil || len(messages) == 0 {
		return
	}
	extractor := s.engine.config.MemoryExtractor
	user := s.userID
	if user == "" {
		user = defaultMemoryUser
	}
	sessionID := s.state.SessionID
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), memoryExtractionTimeout)
		defer cancel()
		if err := extractor.Extract(ctx, user, messages); err != nil {
			slog.Warn("memory extraction failed", "session_id", sessionID, "error", err)
		}
	}()
}
