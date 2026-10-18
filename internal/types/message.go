package types

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Role represents the role of a message sender
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleSystem    Role = "system"
)

// ContentType represents the type of content in a message block
type ContentType string

const (
	ContentTypeText       ContentType = "text"
	ContentTypeImage      ContentType = "image"
	ContentTypeToolUse    ContentType = "tool_use"
	ContentTypeToolResult ContentType = "tool_result"
	ContentTypeThinking   ContentType = "thinking"
)

// ContentBlock represents a single block of content in a message
type ContentBlock interface {
	ContentType() ContentType
}

// TextContent represents a text content block
type TextContent struct {
	Text string `json:"text"`
	// CacheControl, when set, instructs Anthropic to cache all content up to
	// and including this block. Only ever set on the last block of the last
	// message in a request, mirroring how tool/system-prompt caching mark
	// their own trailing breakpoint.
	CacheControl *PromptCacheControl `json:"cache_control,omitempty"`
}

func (TextContent) ContentType() ContentType { return ContentTypeText }

// ImageContent represents an image content block
type ImageContent struct {
	Source struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
	} `json:"source"`
}

func (ImageContent) ContentType() ContentType { return ContentTypeImage }

// ToolUseContent represents a tool use content block
type ToolUseContent struct {
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Input    map[string]any  `json:"input"`
	Metadata *map[string]any `json:"metadata,omitempty"`
}

func (ToolUseContent) ContentType() ContentType { return ContentTypeToolUse }

// ToolResultContent represents a tool result content block
type ToolResultContent struct {
	ToolUseID string          `json:"tool_use_id"`
	Content   string          `json:"content"`
	IsError   bool            `json:"is_error,omitempty"`
	Metadata  *map[string]any `json:"metadata,omitempty"`
	// CacheControl, when set, instructs Anthropic to cache all content up to
	// and including this block. See TextContent.CacheControl.
	CacheControl *PromptCacheControl `json:"cache_control,omitempty"`
}

func (ToolResultContent) ContentType() ContentType { return ContentTypeToolResult }

// ThinkingContent represents a thinking content block (extended thinking)
type ThinkingContent struct {
	Thinking string `json:"thinking"`
}

func (ThinkingContent) ContentType() ContentType { return ContentTypeThinking }

// Message represents a single message in the conversation
type Message struct {
	ID        MessageID        `json:"id"`
	Role      Role             `json:"role"`
	Content   []ContentBlock   `json:"content"`
	Timestamp time.Time        `json:"timestamp"`
	Metadata  *MessageMetadata `json:"metadata,omitempty"`
}

// CompactionMetadata describes a runtime-owned compaction rewrite that has been
// materialized into the canonical transcript.
type CompactionMetadata struct {
	Kind                    string    `json:"kind,omitempty"`
	PreCompactTokens        int       `json:"pre_compact_tokens,omitempty"`
	PostCompactTokens       int       `json:"post_compact_tokens,omitempty"`
	TargetTokens            int       `json:"target_tokens,omitempty"`
	PreservedMessages       int       `json:"preserved_messages,omitempty"`
	PreservedTurns          int       `json:"preserved_turns,omitempty"`
	PreservedToolPairs      int       `json:"preserved_tool_pairs,omitempty"`
	BoundaryVersion         int       `json:"boundary_version,omitempty"`
	FirstPreservedMessageID MessageID `json:"first_preserved_message_id,omitempty"`
	LastPreservedMessageID  MessageID `json:"last_preserved_message_id,omitempty"`
	PreservedTailHash       string    `json:"preserved_tail_hash,omitempty"`
}

const (
	CompactionBoundaryVersionV1 = 1
)

// MessageMetadata contains optional metadata about a message.
type MessageMetadata struct {
	TurnID string `json:"turn_id,omitempty"`

	StopReason string `json:"stop_reason,omitempty"`

	StopSequence *string `json:"stop_sequence,omitempty"`

	Usage *TokenUsage `json:"usage,omitempty"`

	Compaction *CompactionMetadata `json:"compaction,omitempty"`
}

// TokenUsage represents token usage information
type TokenUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`

	// CachedTokens represents tokens served from cache (OpenAI style).
	CachedTokens int `json:"cached_tokens,omitempty"`

	// CacheReadInputTokens is the number of tokens read from the Anthropic
	// prompt cache (served at reduced cost). Set by Anthropic responses.
	CacheReadInputTokens int `json:"cache_read_input_tokens,omitempty"`

	// CacheCreationInputTokens is the number of tokens written into the
	// Anthropic prompt cache (billed at a 25% premium on first write).
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`

	// ReasoningTokens is hidden chain-of-thought token usage billed as part
	// of OutputTokens by reasoning models (OpenAI Responses API style:
	// output_tokens_details.reasoning_tokens). Without this, a reasoning
	// model's large InputTokens:OutputTokens ratio is unexplainable from
	// telemetry alone - it's impossible to tell whether a lopsided ratio
	// comes from resent context, a cache miss, or genuine hidden reasoning.
	ReasoningTokens int `json:"reasoning_tokens,omitempty"`
}

// StopReason constants
const (
	StopReasonEndTurn      = "end_turn"
	StopReasonMaxTokens    = "max_tokens"
	StopReasonStopSequence = "stop_sequence"
	StopReasonToolUse      = "tool_use"
)

// UserMessage is a helper to create a user message
func UserMessage(id string, content string) Message {
	return Message{
		ID:      MessageID(id),
		Role:    RoleUser,
		Content: []ContentBlock{TextContent{Text: content}},
	}
}

// UserMessageWithImage is a helper to create a user message with an image
func UserMessageWithImage(id string, text string, images ...ImageContent) Message {
	blocks := make([]ContentBlock, 0, len(images)+1)
	blocks = append(blocks, TextContent{Text: text})
	for _, img := range images {
		blocks = append(blocks, img)
	}
	return Message{
		ID:      MessageID(id),
		Role:    RoleUser,
		Content: blocks,
	}
}

// AssistantMessage is a helper to create an assistant message
func AssistantMessage(id string, content []ContentBlock) Message {
	return Message{
		ID:      MessageID(id),
		Role:    RoleAssistant,
		Content: content,
	}
}

// SystemMessage creates a system message (for internal use)
func SystemMessage(id string, content string) Message {
	return Message{
		ID:      MessageID(id),
		Role:    RoleSystem,
		Content: []ContentBlock{TextContent{Text: content}},
	}
}

func marshalContentBlocks(blocks []ContentBlock) ([]json.RawMessage, error) {
	encoded := make([]json.RawMessage, 0, len(blocks))
	for _, block := range blocks {
		if block == nil {
			continue
		}

		var payload map[string]any
		switch content := block.(type) {
		case TextContent:
			payload = map[string]any{"type": ContentTypeText, "text": content.Text}
			if content.CacheControl != nil {
				payload["cache_control"] = content.CacheControl
			}
		case ImageContent:
			payload = map[string]any{"type": ContentTypeImage, "source": content.Source}
		case ToolUseContent:
			payload = map[string]any{"type": ContentTypeToolUse, "id": content.ID, "name": content.Name, "input": content.Input}
			if content.Metadata != nil {
				payload["metadata"] = *content.Metadata
			}
		case ToolResultContent:
			payload = map[string]any{"type": ContentTypeToolResult, "tool_use_id": content.ToolUseID, "content": content.Content, "is_error": content.IsError}
			if content.Metadata != nil {
				payload["metadata"] = *content.Metadata
			}
			if content.CacheControl != nil {
				payload["cache_control"] = content.CacheControl
			}
		case ThinkingContent:
			payload = map[string]any{"type": ContentTypeThinking, "thinking": content.Thinking}
		default:
			return nil, fmt.Errorf("unsupported content block type %T", block)
		}

		data, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		encoded = append(encoded, data)
	}
	return encoded, nil
}

func unmarshalContentBlocks(rawBlocks []json.RawMessage) ([]ContentBlock, error) {
	blocks := make([]ContentBlock, 0, len(rawBlocks))
	for _, raw := range rawBlocks {
		var meta struct {
			Type ContentType `json:"type"`
		}
		if err := json.Unmarshal(raw, &meta); err != nil {
			return nil, err
		}

		switch meta.Type {
		case ContentTypeText:
			var content struct {
				Text         string              `json:"text"`
				CacheControl *PromptCacheControl `json:"cache_control"`
			}
			if err := json.Unmarshal(raw, &content); err != nil {
				return nil, err
			}
			blocks = append(blocks, TextContent{Text: content.Text, CacheControl: content.CacheControl})
		case ContentTypeImage:
			var content struct {
				Source struct {
					Type      string `json:"type"`
					MediaType string `json:"media_type"`
					Data      string `json:"data"`
				} `json:"source"`
			}
			if err := json.Unmarshal(raw, &content); err != nil {
				return nil, err
			}
			image := ImageContent{}
			image.Source = content.Source
			blocks = append(blocks, image)
		case ContentTypeToolUse:
			var content struct {
				ID       string         `json:"id"`
				Name     string         `json:"name"`
				Input    map[string]any `json:"input"`
				Metadata map[string]any `json:"metadata"`
			}
			if err := json.Unmarshal(raw, &content); err != nil {
				return nil, err
			}
			toolUse := ToolUseContent{ID: content.ID, Name: content.Name, Input: content.Input}
			if content.Metadata != nil {
				toolUse.Metadata = &content.Metadata
			}
			blocks = append(blocks, toolUse)
		case ContentTypeToolResult:
			var content struct {
				ToolUseID    string              `json:"tool_use_id"`
				Content      string              `json:"content"`
				IsError      bool                `json:"is_error"`
				Metadata     map[string]any      `json:"metadata"`
				CacheControl *PromptCacheControl `json:"cache_control"`
			}
			if err := json.Unmarshal(raw, &content); err != nil {
				return nil, err
			}
			toolResult := ToolResultContent{ToolUseID: content.ToolUseID, Content: content.Content, IsError: content.IsError, CacheControl: content.CacheControl}
			if content.Metadata != nil {
				toolResult.Metadata = &content.Metadata
			}
			blocks = append(blocks, toolResult)
		case ContentTypeThinking:
			var content struct {
				Thinking string `json:"thinking"`
			}
			if err := json.Unmarshal(raw, &content); err != nil {
				return nil, err
			}
			blocks = append(blocks, ThinkingContent{Thinking: content.Thinking})
		default:
			return nil, fmt.Errorf("unsupported content block type %q", meta.Type)
		}
	}
	return blocks, nil
}

func (m Message) MarshalJSON() ([]byte, error) {
	type messageAlias struct {
		ID        MessageID         `json:"id"`
		Role      Role              `json:"role"`
		Content   []json.RawMessage `json:"content"`
		Timestamp time.Time         `json:"timestamp"`
		Metadata  *MessageMetadata  `json:"metadata,omitempty"`
	}

	content, err := marshalContentBlocks(m.Content)
	if err != nil {
		return nil, err
	}

	return json.Marshal(messageAlias{
		ID:        m.ID,
		Role:      m.Role,
		Content:   content,
		Timestamp: m.Timestamp,
		Metadata:  m.Metadata,
	})
}

func (m *Message) UnmarshalJSON(data []byte) error {
	type messageAlias struct {
		ID        MessageID         `json:"id"`
		Role      Role              `json:"role"`
		Content   []json.RawMessage `json:"content"`
		Timestamp time.Time         `json:"timestamp"`
		Metadata  *MessageMetadata  `json:"metadata,omitempty"`
	}

	var aux messageAlias
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	content, err := unmarshalContentBlocks(aux.Content)
	if err != nil {
		return err
	}

	m.ID = aux.ID
	m.Role = aux.Role
	m.Content = content
	m.Timestamp = aux.Timestamp
	m.Metadata = aux.Metadata
	return nil
}

func (t TranscriptEntry) MarshalJSON() ([]byte, error) {
	type transcriptAlias struct {
		ID        MessageID         `json:"id"`
		Type      EntryType         `json:"type"`
		Role      Role              `json:"role,omitempty"`
		Content   []json.RawMessage `json:"content,omitempty"`
		Timestamp time.Time         `json:"timestamp"`
		TurnID    TurnID            `json:"turn_id,omitempty"`
		Metadata  map[string]any    `json:"metadata,omitempty"`
	}

	content, err := marshalContentBlocks(t.Content)
	if err != nil {
		return nil, err
	}

	return json.Marshal(transcriptAlias{
		ID:        t.ID,
		Type:      t.Type,
		Role:      t.Role,
		Content:   content,
		Timestamp: t.Timestamp,
		TurnID:    t.TurnID,
		Metadata:  t.Metadata,
	})
}

func (t *TranscriptEntry) UnmarshalJSON(data []byte) error {
	type transcriptAlias struct {
		ID        MessageID         `json:"id"`
		Type      EntryType         `json:"type"`
		Role      Role              `json:"role,omitempty"`
		Content   []json.RawMessage `json:"content,omitempty"`
		Timestamp time.Time         `json:"timestamp"`
		TurnID    TurnID            `json:"turn_id,omitempty"`
		Metadata  map[string]any    `json:"metadata,omitempty"`
	}

	var aux transcriptAlias
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	content, err := unmarshalContentBlocks(aux.Content)
	if err != nil {
		return err
	}

	t.ID = aux.ID
	t.Type = aux.Type
	t.Role = aux.Role
	t.Content = content
	t.Timestamp = aux.Timestamp
	t.TurnID = aux.TurnID
	t.Metadata = aux.Metadata
	return nil
}

func TranscriptEntryFromMessage(message Message, turnID TurnID) TranscriptEntry {
	effectiveTurnID := turnID
	if message.Metadata != nil && message.Metadata.TurnID != "" {
		effectiveTurnID = TurnID(message.Metadata.TurnID)
	}

	return TranscriptEntry{
		ID:        message.ID,
		Type:      canonicalTranscriptEntryType(message),
		Role:      message.Role,
		Content:   message.Content,
		Timestamp: message.Timestamp,
		TurnID:    effectiveTurnID,
		Metadata:  transcriptMetadataFromMessageMetadata(message.Metadata),
	}
}

func MessageFromTranscriptEntry(entry TranscriptEntry) (Message, bool) {
	if entry.Type != EntryTypeMessage && entry.Type != EntryTypeCompact && entry.Type != EntryTypeSystem {
		return Message{}, false
	}
	return Message{
		ID:        entry.ID,
		Role:      entry.Role,
		Content:   entry.Content,
		Timestamp: entry.Timestamp,
		Metadata:  messageMetadataFromTranscriptEntry(entry),
	}, true
}

// ArchivedTranscriptEntryFromMessage is TranscriptEntryFromMessage for a message a compaction removed from the
// conversation: the same entry, typed archived.
func ArchivedTranscriptEntryFromMessage(message Message, turnID TurnID) TranscriptEntry {
	entry := TranscriptEntryFromMessage(message, turnID)
	entry.Type = EntryTypeArchived
	return entry
}

// ArchivedMessageFromTranscriptEntry reads back a message archived by a compaction.
func ArchivedMessageFromTranscriptEntry(entry TranscriptEntry) (Message, bool) {
	if entry.Type != EntryTypeArchived {
		return Message{}, false
	}
	entry.Type = EntryTypeMessage
	return MessageFromTranscriptEntry(entry)
}

// IsCompactionArtifact reports whether a message is the summary or the marker a compaction wrote, which stand for the
// messages they replaced and are not history in their own right.
func IsCompactionArtifact(message Message) bool {
	return (message.Metadata != nil && message.Metadata.Compaction != nil) || strings.HasPrefix(string(message.ID), "compact_")
}

func TranscriptEntriesFromMessages(messages []Message, turnID TurnID) []TranscriptEntry {
	entries := make([]TranscriptEntry, 0, len(messages))
	for _, message := range messages {
		entries = append(entries, TranscriptEntryFromMessage(message, turnID))
	}
	return entries
}

func transcriptMetadataFromMessageMetadata(metadata *MessageMetadata) map[string]any {
	if metadata == nil {
		return nil
	}

	result := make(map[string]any)
	if metadata.TurnID != "" {
		result["turn_id"] = metadata.TurnID
	}
	if metadata.StopReason != "" {
		result["stop_reason"] = metadata.StopReason
	}
	if metadata.StopSequence != nil {
		result["stop_sequence"] = *metadata.StopSequence
	}
	if metadata.Usage != nil {
		usageMap := map[string]any{
			"input_tokens":  metadata.Usage.InputTokens,
			"output_tokens": metadata.Usage.OutputTokens,
		}
		if metadata.Usage.CacheReadInputTokens > 0 {
			usageMap["cache_read_input_tokens"] = metadata.Usage.CacheReadInputTokens
		}
		if metadata.Usage.CacheCreationInputTokens > 0 {
			usageMap["cache_creation_input_tokens"] = metadata.Usage.CacheCreationInputTokens
		}
		result["usage"] = usageMap
	}
	if metadata.Compaction != nil {
		result["compaction"] = map[string]any{
			"kind":                 metadata.Compaction.Kind,
			"pre_compact_tokens":   metadata.Compaction.PreCompactTokens,
			"post_compact_tokens":  metadata.Compaction.PostCompactTokens,
			"target_tokens":        metadata.Compaction.TargetTokens,
			"preserved_messages":   metadata.Compaction.PreservedMessages,
			"preserved_turns":      metadata.Compaction.PreservedTurns,
			"preserved_tool_pairs": metadata.Compaction.PreservedToolPairs,
		}
		if metadata.Compaction.BoundaryVersion != 0 {
			result["compaction"].(map[string]any)["boundary_version"] = metadata.Compaction.BoundaryVersion
		}
		if metadata.Compaction.FirstPreservedMessageID != "" {
			result["compaction"].(map[string]any)["first_preserved_message_id"] = metadata.Compaction.FirstPreservedMessageID
		}
		if metadata.Compaction.LastPreservedMessageID != "" {
			result["compaction"].(map[string]any)["last_preserved_message_id"] = metadata.Compaction.LastPreservedMessageID
		}
		if metadata.Compaction.PreservedTailHash != "" {
			result["compaction"].(map[string]any)["preserved_tail_hash"] = metadata.Compaction.PreservedTailHash
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func compactionMetadataFromMap(raw map[string]any) *CompactionMetadata {
	if raw == nil {
		return nil
	}
	metadata := &CompactionMetadata{}
	if kind, ok := raw["kind"].(string); ok {
		metadata.Kind = kind
	}
	if value, ok := intValue(raw["pre_compact_tokens"]); ok {
		metadata.PreCompactTokens = value
	}
	if value, ok := intValue(raw["post_compact_tokens"]); ok {
		metadata.PostCompactTokens = value
	}
	if value, ok := intValue(raw["target_tokens"]); ok {
		metadata.TargetTokens = value
	}
	if value, ok := intValue(raw["preserved_messages"]); ok {
		metadata.PreservedMessages = value
	}
	if value, ok := intValue(raw["preserved_turns"]); ok {
		metadata.PreservedTurns = value
	}
	if value, ok := intValue(raw["preserved_tool_pairs"]); ok {
		metadata.PreservedToolPairs = value
	}
	if value, ok := intValue(raw["boundary_version"]); ok {
		metadata.BoundaryVersion = value
	}
	switch value := raw["first_preserved_message_id"].(type) {
	case string:
		metadata.FirstPreservedMessageID = MessageID(value)
	case MessageID:
		metadata.FirstPreservedMessageID = value
	}
	switch value := raw["last_preserved_message_id"].(type) {
	case string:
		metadata.LastPreservedMessageID = MessageID(value)
	case MessageID:
		metadata.LastPreservedMessageID = value
	}
	if value, ok := raw["preserved_tail_hash"].(string); ok {
		metadata.PreservedTailHash = value
	}
	if metadata.Kind == "" &&
		metadata.PreCompactTokens == 0 &&
		metadata.PostCompactTokens == 0 &&
		metadata.TargetTokens == 0 &&
		metadata.PreservedMessages == 0 &&
		metadata.PreservedTurns == 0 &&
		metadata.PreservedToolPairs == 0 &&
		metadata.BoundaryVersion == 0 &&
		metadata.FirstPreservedMessageID == "" &&
		metadata.LastPreservedMessageID == "" &&
		metadata.PreservedTailHash == "" {
		return nil
	}
	return metadata
}

func canonicalTranscriptEntryType(message Message) EntryType {
	if message.Metadata != nil && message.Metadata.Compaction != nil {
		return EntryTypeCompact
	}
	if message.Role == RoleSystem {
		return EntryTypeSystem
	}
	return EntryTypeMessage
}

func IsCanonicalTranscriptMessage(message Message) bool {
	return message.Role == RoleUser || message.Role == RoleAssistant || message.Role == RoleSystem
}

func CanonicalTranscriptMessages(messages []Message) []Message {
	canonical := make([]Message, 0, len(messages))
	for _, message := range messages {
		if !IsCanonicalTranscriptMessage(message) {
			continue
		}
		canonical = append(canonical, message)
	}
	return canonical
}

func CanonicalTranscriptEntriesFromMessages(messages []Message, turnID TurnID) []TranscriptEntry {
	canonical := CanonicalTranscriptMessages(messages)
	entries := make([]TranscriptEntry, 0, len(canonical))
	for _, message := range canonical {
		entries = append(entries, TranscriptEntryFromMessage(message, turnID))
	}
	return entries
}

func CanonicalMessagesFromTranscriptEntries(entries []TranscriptEntry) []Message {
	messages := make([]Message, 0, len(entries))
	for _, entry := range entries {
		if entry.Type != EntryTypeMessage && entry.Type != EntryTypeCompact && entry.Type != EntryTypeSystem {
			continue
		}
		message, ok := MessageFromTranscriptEntry(entry)
		if !ok {
			continue
		}
		messages = append(messages, message)
	}
	return messages
}

// LegacyCanonicalTranscriptHash calculates the hash of the raw marshaled entries
// without recursively normalizing nested objects to maps.
func LegacyCanonicalTranscriptHash(messages []Message) (string, error) {
	entries := CanonicalTranscriptEntriesFromMessages(messages, "")
	payload, err := json.Marshal(entries)
	if err != nil {
		return "", fmt.Errorf("failed to marshal canonical transcript entries: %w", err)
	}
	hash := sha256.Sum256(payload)
	return hex.EncodeToString(hash[:]), nil
}

// CanonicalTranscriptHash computes a stable, normalized SHA-256 hash of the
// canonical transcript entries. It round-trips the JSON payload through a generic
// unmarshaling step to convert any nested structures (such as GitDiff) to generic
// maps, ensuring key ordering is consistently alphabetical.
func CanonicalTranscriptHash(messages []Message) (string, error) {
	entries := CanonicalTranscriptEntriesFromMessages(messages, "")
	payload, err := json.Marshal(entries)
	if err != nil {
		return "", fmt.Errorf("failed to marshal canonical transcript entries: %w", err)
	}

	var raw any
	if err := json.Unmarshal(payload, &raw); err != nil {
		return "", fmt.Errorf("failed to unmarshal canonical transcript entries for normalization: %w", err)
	}

	normalizedPayload, err := json.Marshal(raw)
	if err != nil {
		return "", fmt.Errorf("failed to marshal normalized canonical transcript entries: %w", err)
	}

	hash := sha256.Sum256(normalizedPayload)
	return hex.EncodeToString(hash[:]), nil
}

func HasCompactionMetadata(message Message) bool {
	return message.Metadata != nil && message.Metadata.Compaction != nil
}

func CompactionMetadataFromMessages(messages []Message) *CompactionMetadata {
	metadata, _, _, ok := ActiveCompactionBoundary(messages)
	if !ok {
		return nil
	}
	return metadata
}

func CountCompactionMessages(messages []Message) int {
	count := 0
	for _, message := range messages {
		if HasCompactionMetadata(message) {
			count++
		}
	}
	return count
}

func CountDistinctTurnIDs(messages []Message) int {
	seen := make(map[string]bool)
	for _, message := range messages {
		if message.Metadata == nil || message.Metadata.TurnID == "" {
			continue
		}
		seen[message.Metadata.TurnID] = true
	}
	return len(seen)
}

func CountToolResultMessages(messages []Message) int {
	count := 0
	for _, message := range messages {
		for _, block := range message.Content {
			if _, ok := block.(ToolResultContent); ok {
				count++
				break
			}
		}
	}
	return count
}

func CloneCompactionMetadata(metadata *CompactionMetadata) *CompactionMetadata {
	if metadata == nil {
		return nil
	}
	cloned := *metadata
	return &cloned
}

func CloneMessageMetadata(metadata *MessageMetadata) *MessageMetadata {
	if metadata == nil {
		return nil
	}
	cloned := *metadata
	if metadata.StopSequence != nil {
		stopSequence := *metadata.StopSequence
		cloned.StopSequence = &stopSequence
	}
	if metadata.Usage != nil {
		usage := *metadata.Usage
		cloned.Usage = &usage
	}
	cloned.Compaction = CloneCompactionMetadata(metadata.Compaction)
	return &cloned
}

func WithCompactionMetadata(message Message, metadata *CompactionMetadata) Message {
	cloned := message
	cloned.Metadata = CloneMessageMetadata(message.Metadata)
	if cloned.Metadata == nil {
		cloned.Metadata = &MessageMetadata{}
	}
	cloned.Metadata.Compaction = CloneCompactionMetadata(metadata)
	return cloned
}

func transcriptMetadataSignature(metadata *MessageMetadata) string {
	if metadata == nil {
		return ""
	}
	stopSequence := ""
	if metadata.StopSequence != nil {
		stopSequence = *metadata.StopSequence
	}
	inputTokens := 0
	outputTokens := 0
	if metadata.Usage != nil {
		inputTokens = metadata.Usage.InputTokens
		outputTokens = metadata.Usage.OutputTokens
	}
	compactionSignature := ""
	if metadata.Compaction != nil {
		compactionSignature = metadata.Compaction.Kind + ":" +
			fmt.Sprintf("%d:%d:%d:%d:%d:%d:%d:%s:%s:%s",
				metadata.Compaction.PreCompactTokens,
				metadata.Compaction.PostCompactTokens,
				metadata.Compaction.TargetTokens,
				metadata.Compaction.PreservedMessages,
				metadata.Compaction.PreservedTurns,
				metadata.Compaction.PreservedToolPairs,
				metadata.Compaction.BoundaryVersion,
				metadata.Compaction.FirstPreservedMessageID,
				metadata.Compaction.LastPreservedMessageID,
				metadata.Compaction.PreservedTailHash,
			)
	}
	return fmt.Sprintf("%s|%s|%s|%d|%d|%s", metadata.TurnID, metadata.StopReason, stopSequence, inputTokens, outputTokens, compactionSignature)
}

func MessagesEquivalentForTranscript(left Message, right Message) bool {
	if left.ID != right.ID || left.Role != right.Role {
		return false
	}
	if left.Timestamp.UnixNano() != right.Timestamp.UnixNano() {
		return false
	}
	return transcriptMetadataSignature(left.Metadata) == transcriptMetadataSignature(right.Metadata)
}

func CanonicalTranscriptPrefix(messages []Message, prefix []Message) bool {
	if len(prefix) > len(messages) {
		return false
	}
	for i := range prefix {
		if !MessagesEquivalentForTranscript(messages[i], prefix[i]) {
			return false
		}
	}
	return true
}

func CloneMessages(messages []Message) []Message {
	return append([]Message(nil), messages...)
}

func CanonicalTranscriptMessageCount(messages []Message) int {
	return len(CanonicalTranscriptMessages(messages))
}

func HasCompactionBoundaryDescriptor(metadata *CompactionMetadata) bool {
	if metadata == nil {
		return false
	}
	return metadata.BoundaryVersion != 0 ||
		metadata.FirstPreservedMessageID != "" ||
		metadata.LastPreservedMessageID != "" ||
		metadata.PreservedTailHash != ""
}

func ActiveCompactionBoundary(messages []Message) (*CompactionMetadata, int, int, bool) {
	boundaryStart := -1
	for i := range messages {
		if HasCompactionMetadata(messages[i]) {
			boundaryStart = i
			break
		}
	}
	if boundaryStart < 0 {
		return nil, 0, 0, false
	}

	boundaryEnd := boundaryStart
	for boundaryEnd < len(messages) && HasCompactionMetadata(messages[boundaryEnd]) {
		boundaryEnd++
	}
	return messages[boundaryStart].Metadata.Compaction, boundaryStart, boundaryEnd, true
}

func ActiveCompactionPreservedTail(messages []Message) ([]Message, *CompactionMetadata, bool) {
	metadata, _, tailStart, ok := ActiveCompactionBoundary(messages)
	if !ok {
		return nil, nil, false
	}
	return messages[tailStart:], metadata, true
}

func ValidateCompactionBoundary(messages []Message) error {
	preserved, metadata, ok := ActiveCompactionPreservedTail(messages)
	if !ok || !HasCompactionBoundaryDescriptor(metadata) {
		return nil
	}
	if metadata.BoundaryVersion != CompactionBoundaryVersionV1 {
		return fmt.Errorf("unsupported compaction boundary version %d", metadata.BoundaryVersion)
	}
	if metadata.PreservedMessages != len(preserved) {
		return fmt.Errorf("compaction boundary preserved message count mismatch: metadata=%d actual=%d", metadata.PreservedMessages, len(preserved))
	}
	if metadata.PreservedTurns != CountDistinctTurnIDs(preserved) {
		return fmt.Errorf("compaction boundary preserved turn count mismatch: metadata=%d actual=%d", metadata.PreservedTurns, CountDistinctTurnIDs(preserved))
	}
	if metadata.PreservedToolPairs != countPreservedToolPairs(preserved) {
		return fmt.Errorf("compaction boundary preserved tool pair count mismatch: metadata=%d actual=%d", metadata.PreservedToolPairs, countPreservedToolPairs(preserved))
	}
	if len(preserved) == 0 {
		if metadata.FirstPreservedMessageID != "" || metadata.LastPreservedMessageID != "" {
			return fmt.Errorf("compaction boundary preserved message ids set on empty tail")
		}
	} else {
		if metadata.FirstPreservedMessageID == "" || metadata.LastPreservedMessageID == "" {
			return fmt.Errorf("compaction boundary missing preserved message ids")
		}
		if preserved[0].ID != metadata.FirstPreservedMessageID {
			return fmt.Errorf("compaction boundary first preserved message mismatch: metadata=%s actual=%s", metadata.FirstPreservedMessageID, preserved[0].ID)
		}
		if preserved[len(preserved)-1].ID != metadata.LastPreservedMessageID {
			return fmt.Errorf("compaction boundary last preserved message mismatch: metadata=%s actual=%s", metadata.LastPreservedMessageID, preserved[len(preserved)-1].ID)
		}
	}
	if metadata.PreservedTailHash == "" {
		return fmt.Errorf("compaction boundary missing preserved tail hash")
	}
	actualHash, err := CanonicalTranscriptHash(preserved)
	if err != nil {
		return err
	}
	if metadata.PreservedTailHash != actualHash {
		// Fallback: check if the legacy hash matches
		legacyHash, err := LegacyCanonicalTranscriptHash(preserved)
		if err == nil && metadata.PreservedTailHash == legacyHash {
			return nil
		}
		return fmt.Errorf("compaction boundary preserved tail hash mismatch")
	}
	if err := validatePreservedTailToolResults(preserved); err != nil {
		return err
	}
	return nil
}

func collectToolUseIDs(message Message) []string {
	ids := make([]string, 0)
	for _, block := range message.Content {
		if toolUse, ok := block.(ToolUseContent); ok {
			ids = append(ids, toolUse.ID)
		}
	}
	return ids
}

func collectToolResultIDs(message Message) []string {
	ids := make([]string, 0)
	for _, block := range message.Content {
		if toolResult, ok := block.(ToolResultContent); ok {
			ids = append(ids, toolResult.ToolUseID)
		}
	}
	return ids
}

func countPreservedToolPairs(messages []Message) int {
	firstSeenToolUse := make(map[string]int)
	for i, message := range messages {
		for _, toolUseID := range collectToolUseIDs(message) {
			if toolUseID == "" {
				continue
			}
			if _, exists := firstSeenToolUse[toolUseID]; !exists {
				firstSeenToolUse[toolUseID] = i
			}
		}
	}
	count := 0
	for i, message := range messages {
		for _, toolUseID := range collectToolResultIDs(message) {
			if toolUseID == "" {
				continue
			}
			if toolUseIndex, exists := firstSeenToolUse[toolUseID]; exists && toolUseIndex <= i {
				count++
			}
		}
	}
	return count
}

func validatePreservedTailToolResults(messages []Message) error {
	firstSeenToolUse := make(map[string]int)
	for i, message := range messages {
		for _, toolUseID := range collectToolUseIDs(message) {
			if toolUseID == "" {
				continue
			}
			if _, exists := firstSeenToolUse[toolUseID]; !exists {
				firstSeenToolUse[toolUseID] = i
			}
		}
	}
	for i, message := range messages {
		for _, toolUseID := range collectToolResultIDs(message) {
			if toolUseID == "" {
				return fmt.Errorf("compaction boundary preserved tail contains tool_result without tool_use_id")
			}
			toolUseIndex, exists := firstSeenToolUse[toolUseID]
			if !exists {
				return fmt.Errorf("compaction boundary preserved tail contains orphaned tool_result for tool_use_id %s", toolUseID)
			}
			if toolUseIndex > i {
				return fmt.Errorf("compaction boundary preserved tail contains tool_result before tool_use for tool_use_id %s", toolUseID)
			}
		}
	}
	return nil
}

func messageMetadataFromTranscriptEntry(entry TranscriptEntry) *MessageMetadata {
	metadata := &MessageMetadata{}
	if entry.TurnID != "" {
		metadata.TurnID = entry.TurnID.String()
	}

	if entry.Metadata != nil {
		if turnID, ok := entry.Metadata["turn_id"].(string); ok && turnID != "" {
			metadata.TurnID = turnID
		}
		if stopReason, ok := entry.Metadata["stop_reason"].(string); ok {
			metadata.StopReason = stopReason
		}
		if stopSequence, ok := entry.Metadata["stop_sequence"].(string); ok && stopSequence != "" {
			metadata.StopSequence = &stopSequence
		}
		if usageMap, ok := entry.Metadata["usage"].(map[string]any); ok {
			usage := &TokenUsage{}
			if inputTokens, ok := intValue(usageMap["input_tokens"]); ok {
				usage.InputTokens = inputTokens
			}
			if outputTokens, ok := intValue(usageMap["output_tokens"]); ok {
				usage.OutputTokens = outputTokens
			}
			if cacheRead, ok := intValue(usageMap["cache_read_input_tokens"]); ok {
				usage.CacheReadInputTokens = cacheRead
			}
			if cacheCreation, ok := intValue(usageMap["cache_creation_input_tokens"]); ok {
				usage.CacheCreationInputTokens = cacheCreation
			}
			metadata.Usage = usage
		}
		if compactionMap, ok := entry.Metadata["compaction"].(map[string]any); ok {
			metadata.Compaction = compactionMetadataFromMap(compactionMap)
		}
	}

	if metadata.TurnID == "" && metadata.StopReason == "" && metadata.StopSequence == nil && metadata.Usage == nil && metadata.Compaction == nil {
		return nil
	}
	return metadata
}

func intValue(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case int32:
		return int(v), true
	case int64:
		return int(v), true
	case float32:
		return int(v), true
	case float64:
		return int(v), true
	default:
		return 0, false
	}
}
