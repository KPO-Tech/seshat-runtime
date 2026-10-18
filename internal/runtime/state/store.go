package state

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// Store manages session persistence
type Store struct {
	backend Backend

	// mu protects concurrent access
	mu sync.RWMutex
}

type backendCloser interface {
	Close() error
}

// NewStore creates a new session store
func NewStore(baseDir string) (*Store, error) {
	backend, err := NewFilesystemBackend(baseDir)
	if err != nil {
		return nil, err
	}
	return NewStoreWithBackend(backend)
}

// NewStoreWithBackend creates a store using a custom backend so callers can
// plug alternative persistence layers such as SQL or document databases.
func NewStoreWithBackend(backend Backend) (*Store, error) {
	if backend == nil {
		return nil, fmt.Errorf("backend is required")
	}

	return &Store{
		backend: backend,
	}, nil
}

// SaveSession saves a session to disk. Title is preserved from any
// already-persisted record rather than taken from the incoming metadata:
// UpdateSessionTitle is the only place in this package that ever writes a
// real Title, and the in-memory Session.state.Metadata a caller's snapshot
// comes from is never updated in place when that happens (the async
// auto-title flow only touches the store, by design, since a session may
// be closed/reloaded by the time it completes). Without this, a routine
// save issued after the title lands - e.g. persisting state at the end of
// the very turn that raced the title generation - would silently regress
// the title back to its stale placeholder. A session with no existing
// record yet (first save, at creation) keeps whatever Title the caller
// supplied.
func (s *Store) SaveSession(sessionID types.SessionID, metadata *types.SessionMetadata) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if existing, err := s.backend.LoadSession(sessionID); err == nil && existing != nil && existing.Title != "" {
		metadata.Title = existing.Title
	}

	return s.backend.SaveSession(sessionID, metadata)
}

// LoadSession loads session metadata from disk
func (s *Store) LoadSession(sessionID types.SessionID) (*types.SessionMetadata, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.backend.LoadSession(sessionID)
}

// UpdateSessionTitle atomically updates only the Title field of a session's
// persisted metadata. It is safe to call concurrently with other operations.
func (s *Store) UpdateSessionTitle(sessionID types.SessionID, title string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	metadata, err := s.backend.LoadSession(sessionID)
	if err != nil {
		return fmt.Errorf("load session for title update: %w", err)
	}
	metadata.Title = title
	metadata.UpdatedAt = time.Now()
	return s.backend.SaveSession(sessionID, metadata)
}

// DeleteSession deletes a session from disk
func (s *Store) DeleteSession(sessionID types.SessionID) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.backend.DeleteSession(sessionID)
}

// ListSessions lists all sessions
func (s *Store) ListSessions() ([]types.SessionID, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.backend.ListSessions()
}

// AppendTranscriptEntry appends an entry to the transcript
func (s *Store) AppendTranscriptEntry(sessionID types.SessionID, entry types.TranscriptEntry) error {
	return s.AppendTranscriptEntries(sessionID, []types.TranscriptEntry{entry})
}

// LoadCanonicalMessages restores the canonical message list for a session.
func (s *Store) LoadCanonicalMessages(sessionID types.SessionID) ([]types.Message, error) {
	entries, err := s.LoadTranscript(sessionID)
	if err != nil {
		return nil, err
	}
	return canonicalMessages(entries), nil
}

// LoadHistory restores the whole conversation of a session: the messages compactions archived, oldest first, and the
// canonical messages the model still sees.
func (s *Store) LoadHistory(sessionID types.SessionID) (archived, current []types.Message, err error) {
	entries, err := s.LoadTranscript(sessionID)
	if err != nil {
		return nil, nil, err
	}
	for _, e := range entries {
		if m, ok := types.ArchivedMessageFromTranscriptEntry(e); ok {
			archived = append(archived, m)
		}
	}
	return archived, canonicalMessages(entries), nil
}

// SaveCanonicalMessages replaces the transcript with the canonical message list.
func (s *Store) SaveCanonicalMessages(sessionID types.SessionID, messages []types.Message) error {
	return s.ReplaceTranscript(sessionID, canonicalTranscriptEntries(messages))
}

// RestoreSessionState reloads both metadata and canonical messages together so
// SDK/session callers do not reimplement transcript restoration piecemeal.
func (s *Store) RestoreSessionState(sessionID types.SessionID) (*types.SessionMetadata, []types.Message, error) {
	metadata, err := s.LoadSession(sessionID)
	if err != nil {
		return nil, nil, err
	}
	messages, err := s.LoadCanonicalMessages(sessionID)
	if err != nil {
		return nil, nil, err
	}
	checkpoint, err := s.LoadCheckpoint(sessionID)
	if err != nil {
		return nil, nil, err
	}
	if checkpoint != nil {
		matches, err := checkpointMatchesMessages(checkpoint, messages)
		if err != nil {
			return nil, nil, err
		}
		if !matches {
			return nil, nil, fmt.Errorf("checkpoint mismatch for session %s", sessionID)
		}
		applyCheckpointMetadata(metadata, checkpoint)
	}
	if err := types.ValidateCompactionBoundary(messages); err != nil {
		return nil, nil, fmt.Errorf("invalid compaction transcript for session %s: %w", sessionID, err)
	}
	return metadata, messages, nil
}

// SaveSessionState persists metadata and the canonical transcript together.
func (s *Store) SaveSessionState(sessionID types.SessionID, metadata *types.SessionMetadata, previousMessages []types.Message, currentMessages []types.Message) error {
	// Save metadata FIRST to satisfy foreign key constraint
	if err := s.SaveSession(sessionID, metadata); err != nil {
		return err
	}
	// Then sync transcripts
	if err := s.SyncTranscriptMessages(sessionID, previousMessages, currentMessages); err != nil {
		return err
	}
	applyCanonicalTranscriptSummary(metadata, currentMessages)
	checkpoint, err := buildCheckpoint(sessionID, metadata, currentMessages)
	if err != nil {
		return err
	}
	return s.SaveCheckpoint(sessionID, checkpoint)
}

// AppendTranscriptEntries appends multiple transcript entries atomically.
func (s *Store) AppendTranscriptEntries(sessionID types.SessionID, entries []types.TranscriptEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(entries) == 0 {
		return nil
	}

	return s.backend.AppendTranscriptEntries(sessionID, entries)
}

// ReplaceTranscript replaces the transcript file with the provided entries.
func (s *Store) ReplaceTranscript(sessionID types.SessionID, entries []types.TranscriptEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.backend.ReplaceTranscript(sessionID, entries)
}

// LoadTranscript loads the transcript for a session
func (s *Store) LoadTranscript(sessionID types.SessionID) ([]types.TranscriptEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.backend.LoadTranscript(sessionID)
}

// SearchTranscriptsByContent returns session IDs whose transcript contains needle.
func (s *Store) SearchTranscriptsByContent(needle string, limit int) ([]types.SessionID, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.backend.SearchTranscriptsByContent(needle, limit)
}

// SaveCheckpoint saves a checkpoint for recovery
func (s *Store) SaveCheckpoint(sessionID types.SessionID, checkpoint *Checkpoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.backend.SaveCheckpoint(sessionID, checkpoint)
}

// LoadCheckpoint loads the latest checkpoint for a session
func (s *Store) LoadCheckpoint(sessionID types.SessionID) (*Checkpoint, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.backend.LoadCheckpoint(sessionID)
}

// Close releases backend-owned resources when the configured backend supports
// explicit shutdown.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	closer, ok := s.backend.(backendCloser)
	if !ok {
		return nil
	}
	return closer.Close()
}

func buildMessagesHash(messages []types.Message) (string, error) {
	hash, err := types.CanonicalTranscriptHash(messages)
	if err != nil {
		return "", fmt.Errorf("failed to build canonical transcript hash for checkpoint: %w", err)
	}
	return hash, nil
}

func buildCheckpoint(sessionID types.SessionID, metadata *types.SessionMetadata, messages []types.Message) (*Checkpoint, error) {
	messagesHash, err := buildMessagesHash(messages)
	if err != nil {
		return nil, err
	}
	turnNumber := 0
	status := ""
	if metadata != nil {
		turnNumber = metadata.TotalTurns
		status = string(metadata.Status)
	}
	return &Checkpoint{
		SessionID:    sessionID,
		TurnNumber:   turnNumber,
		MessagesHash: messagesHash,
		Timestamp:    time.Now().UTC().Unix(),
		Metadata: map[string]any{
			"status":               status,
			"canonical_transcript": canonicalTranscriptSummary(messages),
		},
	}, nil
}

func checkpointStatus(checkpoint *Checkpoint) string {
	if checkpoint == nil || checkpoint.Metadata == nil {
		return ""
	}
	status, _ := checkpoint.Metadata["status"].(string)
	return status
}

func checkpointCanonicalSummary(checkpoint *Checkpoint) map[string]any {
	if checkpoint == nil || checkpoint.Metadata == nil {
		return nil
	}
	summary, _ := checkpoint.Metadata["canonical_transcript"].(map[string]any)
	return summary
}

func checkpointMatchesMessages(checkpoint *Checkpoint, messages []types.Message) (bool, error) {
	if checkpoint == nil {
		return true, nil
	}
	messagesHash, err := buildMessagesHash(messages)
	if err != nil {
		return false, err
	}
	if checkpoint.MessagesHash != "" && checkpoint.MessagesHash != messagesHash {
		// Fallback 1: check if legacy hash matches
		legacyHash, err := types.LegacyCanonicalTranscriptHash(messages)
		if err == nil && checkpoint.MessagesHash == legacyHash {
			return true, nil
		}

		// Fallback 2: compare logical transcript summaries
		expectedSummary := checkpointCanonicalSummary(checkpoint)
		if expectedSummary != nil {
			currentSummary := canonicalTranscriptSummary(messages)
			if summariesMatch(expectedSummary, currentSummary) {
				slog.Warn("Session checkpoint hash mismatch detected, but transcript summary matches. Proceeding.",
					"session_id", checkpoint.SessionID,
					"expected_hash", checkpoint.MessagesHash,
					"computed_hash", messagesHash)
				return true, nil
			}
		}
		return false, nil
	}
	return true, nil
}

func summariesMatch(a, b map[string]any) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	keys := []string{"message_count", "turn_count", "tool_results", "first_user_message"}
	for _, k := range keys {
		va := a[k]
		vb := b[k]

		// Normalize numeric types (json unmarshals as float64, while in-memory uses int)
		if ka, ok := va.(float64); ok {
			va = int(ka)
		}
		if kb, ok := vb.(float64); ok {
			vb = int(kb)
		}

		if va != vb {
			return false
		}
	}
	return true
}

func applyCheckpointMetadata(metadata *types.SessionMetadata, checkpoint *Checkpoint) {
	if metadata == nil || checkpoint == nil {
		return
	}
	if status := checkpointStatus(checkpoint); status != "" {
		metadata.Status = types.SessionStatus(status)
	}
	if summary := checkpointCanonicalSummary(checkpoint); summary != nil {
		if metadata.Additional == nil {
			metadata.Additional = make(map[string]any)
		}
		metadata.Additional["canonical_transcript"] = summary
	}
}

// GetSessionInfo gets basic information about a session
func (s *Store) GetSessionInfo(sessionID types.SessionID) (*SessionInfo, error) {
	metadata, err := s.LoadSession(sessionID)
	if err != nil {
		return nil, err
	}

	info := &SessionInfo{
		ID:          metadata.ID,
		Status:      metadata.Status,
		CreatedAt:   metadata.CreatedAt.Unix(),
		UpdatedAt:   metadata.UpdatedAt.Unix(),
		TotalTurns:  metadata.TotalTurns,
		TotalTokens: metadata.TotalTokens,
		Title:       metadata.Title,
	}
	if metadata.Additional != nil {
		if ct, ok := metadata.Additional["canonical_transcript"].(map[string]any); ok {
			if v, ok := ct["first_user_message"].(string); ok {
				info.Preview = v
			}
		}
		if profile, _ := metadata.Additional["tool_surface_profile"].(string); profile == "subagent" {
			info.Subagent = true
		}
		if parent, _ := metadata.Additional[types.SessionMetaParentSessionID].(string); parent != "" {
			info.ParentID = types.SessionID(parent)
			info.Subagent = true
		}
	}
	return info, nil
}

// GetAllSessionsInfo gets information about all sessions
func (s *Store) GetAllSessionsInfo() ([]*SessionInfo, error) {
	sessionIDs, err := s.ListSessions()
	if err != nil {
		return nil, err
	}

	infos := make([]*SessionInfo, 0, len(sessionIDs))
	for _, sessionID := range sessionIDs {
		info, err := s.GetSessionInfo(sessionID)
		if err != nil {
			// Skip sessions that can't be loaded
			continue
		}
		infos = append(infos, info)
	}

	return infos, nil
}

// CleanupOldSessions removes sessions older than the specified duration
func (s *Store) CleanupOldSessions(maxAge int64) (int, error) {
	infos, err := s.GetAllSessionsInfo()
	if err != nil {
		return 0, err
	}

	now := currentTime()
	removed := 0

	for _, info := range infos {
		age := now - info.UpdatedAt
		if age > maxAge {
			if err := s.DeleteSession(info.ID); err == nil {
				removed++
			}
		}
	}

	return removed, nil
}

func currentTime() int64 {
	return time.Now().Unix()
}
