package engine

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// memoryExtractionTimeout bounds one extraction and the consolidation that may follow it, which run after the session is closed and must not hang on a slow
// provider.
const memoryExtractionTimeout = 4 * time.Minute

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
	onDone := s.engine.config.OnMemoryExtracted
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), memoryExtractionTimeout)
		defer cancel()
		if err := extractor.Extract(ctx, user, messages); err != nil {
			slog.Warn("memory extraction failed", "session_id", sessionID, "error", err)
			return
		}
		if onDone != nil {
			onDone(sessionID)
		}
	}()
}

// MemoryRecaller turns long-term memory into the text placed in the system prompt of a new session. The default is
// longterm.Recaller.
type MemoryRecaller interface {
	Recall(ctx context.Context, userID, query string) string
}

// recallLongTermMemory returns the memory block of this session. It is computed at the first request, from the first
// thing the user said, and kept: see Session.recalled.
func (s *Session) recallLongTermMemory(ctx context.Context) string {
	if s == nil || s.engine == nil || s.engine.config == nil || s.engine.config.MemoryRecaller == nil {
		return ""
	}
	s.mu.Lock()
	loaded, recalled := s.recalledLoaded, s.recalled
	s.mu.Unlock()
	if loaded {
		return recalled
	}
	s.rememberUserID(ctx)
	user := s.userID
	if user == "" {
		user = defaultMemoryUser
	}
	recalled = s.engine.config.MemoryRecaller.Recall(ctx, user, firstUserMessageText(s.state.CloneMessages()))
	s.mu.Lock()
	s.recalled, s.recalledLoaded = recalled, true
	s.mu.Unlock()
	return recalled
}

// firstUserMessageText is the text of the first message the user wrote, which says what the session is about.
func firstUserMessageText(messages []types.Message) string {
	for _, m := range messages {
		if m.Role != types.RoleUser {
			continue
		}
		var parts []string
		for _, b := range m.Content {
			if t, ok := b.(types.TextContent); ok && t.Text != "" {
				parts = append(parts, t.Text)
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, " ")
		}
	}
	return ""
}

// joinMemoryContext puts the session memory and the long-term memory side by side, skipping an empty one.
func joinMemoryContext(parts ...string) string {
	var kept []string
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			kept = append(kept, strings.TrimSpace(p))
		}
	}
	return strings.Join(kept, "\n\n")
}
