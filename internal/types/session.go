package types

import (
	"time"
)

// SessionID uniquely identifies a session
type SessionID string

// TurnID uniquely identifies a turn within a session
type TurnID string

// AgentID uniquely identifies an agent/subagent
type AgentID string

// MessageID uniquely identifies a message
type MessageID string

// SessionStatus represents the status of a session
type SessionStatus string

const (
	SessionStatusActive    SessionStatus = "active"
	SessionStatusInterrupt SessionStatus = "interrupted"
	SessionStatusClosed    SessionStatus = "closed"
)

// SessionMetadataSchemaVersion is the current schema version for persisted session metadata.
// Increment this constant whenever SessionMetadata gains a breaking structural change,
// and add migration logic in engine.migrateSessionMetadata.
const SessionMetadataSchemaVersion = 1

// SessionMetadata contains metadata about a session
// SessionMetaParentSessionID is the SessionMetadata.Additional key under which a
// sub-agent session records the session that launched it.
const SessionMetaParentSessionID = "parent_session_id"

// NewSubagentSessionMetadata is the metadata of a session a sub-agent or a
// background agent task runs in: the given tool surface profile, and the session
// that launched it when that is known. Both places that create such a session use
// it, so the session list can tell a conversation from an agent's session.
func NewSubagentSessionMetadata(surfaceProfile string, parent SessionID, now time.Time) *SessionMetadata {
	metadata := &SessionMetadata{
		Status:        SessionStatusActive,
		CreatedAt:     now,
		UpdatedAt:     now,
		Additional:    map[string]any{"tool_surface_profile": surfaceProfile},
		SchemaVersion: SessionMetadataSchemaVersion,
	}
	if parent != "" {
		metadata.Additional[SessionMetaParentSessionID] = string(parent)
	}
	return metadata
}

type SessionMetadata struct {
	// ID is the unique session identifier
	ID SessionID `json:"id"`

	// Title is the human-readable session name (e.g. "untitled_session_1").
	// Generated once at creation; never reassigned automatically.
	Title string `json:"title,omitempty"`

	// Status is the current status
	Status SessionStatus `json:"status"`

	// CreatedAt is when the session was created
	CreatedAt time.Time `json:"created_at"`

	// UpdatedAt is when the session was last updated
	UpdatedAt time.Time `json:"updated_at"`

	// RootPath is the working directory for this session
	RootPath string `json:"root_path,omitempty"`

	// Model is the model being used
	Model string `json:"model,omitempty"`

	// TotalTurns is the total number of turns completed
	TotalTurns int `json:"total_turns"`

	// TotalTokens is the total tokens used across all turns
	TotalTokens int `json:"total_tokens"`

	// MaxTokens is the context window for the current model
	MaxTokens int `json:"max_tokens,omitempty"`

	// CompactCount tracks how many times we've compacted
	CompactCount int `json:"compact_count"`

	// LastCompactedAt tracks when we last compacted
	LastCompactedAt *time.Time `json:"last_compacted_at,omitempty"`

	// UsageAnchorTokens is the size of the conversation as the provider counted it at the end of the last completed turn,
	// UsageAnchorMessages how many messages that covered and UsageAnchorModel the model that counted. A session that is loaded
	// again for its next turn (a host that keeps no live session) starts the turn's first context figure from them. Cleared
	// whenever the messages are rewritten.
	UsageAnchorTokens   int    `json:"usage_anchor_tokens,omitempty"`
	UsageAnchorMessages int    `json:"usage_anchor_messages,omitempty"`
	UsageAnchorModel    string `json:"usage_anchor_model,omitempty"`

	// Additional metadata
	Additional map[string]any `json:"additional,omitempty"`

	// SchemaVersion is the schema version of this persisted metadata.
	// Zero means pre-versioning (treat as v0 / legacy).
	SchemaVersion int `json:"schema_version,omitempty"`
}

// TurnMetadata contains metadata about a turn
type TurnMetadata struct {
	// TurnID is the unique turn identifier
	TurnID TurnID `json:"turn_id"`

	// SessionID is the parent session
	SessionID SessionID `json:"session_id"`

	// TurnNumber is the sequential turn number (1-indexed)
	TurnNumber int `json:"turn_number"`

	// StartedAt is when the turn started
	StartedAt time.Time `json:"started_at"`

	// CompletedAt is when the turn completed
	CompletedAt *time.Time `json:"completed_at,omitempty"`

	// StopReason is why the turn stopped
	StopReason string `json:"stop_reason,omitempty"`

	// ToolUses is the number of tools used in this turn
	ToolUses int `json:"tool_uses,omitempty"`

	// InputTokens is the tokens used for input
	InputTokens int `json:"input_tokens"`

	// OutputTokens is the tokens used for output
	OutputTokens int `json:"output_tokens"`

	// IsCompacted indicates if this turn was after a compaction
	IsCompacted bool `json:"is_compacted"`

	// Additional metadata
	Additional map[string]any `json:"additional,omitempty"`
}

// TranscriptEntry represents a single entry in the transcript
type TranscriptEntry struct {
	// ID uniquely identifies this entry
	ID MessageID `json:"id"`

	// Type is the type of entry
	Type EntryType `json:"type"`

	// Role is the message role (for message entries)
	Role Role `json:"role,omitempty"`

	// Content is the message content (for message entries)
	Content []ContentBlock `json:"content,omitempty"`

	// Timestamp is when this entry was created
	Timestamp time.Time `json:"timestamp"`

	// TurnID indicates which turn this belongs to
	TurnID TurnID `json:"turn_id,omitempty"`

	// Metadata contains additional information
	Metadata map[string]any `json:"metadata,omitempty"`
}

// EntryType represents the type of transcript entry
type EntryType string

const (
	EntryTypeMessage EntryType = "message"
	EntryTypeTurn    EntryType = "turn"
	EntryTypeCompact EntryType = "compact"
	EntryTypeSystem  EntryType = "system"
	EntryTypeControl EntryType = "control"

	// EntryTypeArchived marks a message that a compaction took out of the conversation the model sees. It stays in the
	// transcript, so the full history can still be read, but it is not part of the canonical messages that are sent to
	// the model or restored as the session.
	EntryTypeArchived EntryType = "archived"
)

// NewSessionID creates a new session ID (wrapper for clarity)
func NewSessionID(id string) SessionID {
	return SessionID(id)
}

// NewTurnID creates a new turn ID (wrapper for clarity)
func NewTurnID(id string) TurnID {
	return TurnID(id)
}

// NewMessageID creates a new message ID (wrapper for clarity)
func NewMessageID(id string) MessageID {
	return MessageID(id)
}

// String returns the string representation
func (s SessionID) String() string {
	return string(s)
}

// String returns the string representation
func (t TurnID) String() string {
	return string(t)
}

// String returns the string representation
func (m MessageID) String() string {
	return string(m)
}
