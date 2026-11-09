package types

import (
	"encoding/json"
	"io"
	"testing"
	"time"
)

// --- from message_test.go ---

func TestMessageCreation(t *testing.T) {
	// Test creating a user message
	msg := UserMessage("test-id", "Hello, World!")

	if msg.ID != MessageID("test-id") {
		t.Errorf("Expected ID to be 'test-id', got '%s'", msg.ID)
	}

	if msg.Role != RoleUser {
		t.Errorf("Expected Role to be 'user', got '%s'", msg.Role)
	}

	if len(msg.Content) != 1 {
		t.Errorf("Expected 1 content block, got %d", len(msg.Content))
	}
}

func TestToolUseContent(t *testing.T) {
	toolUse := ToolUseContent{
		ID:   "tool-1",
		Name: "test_tool",
		Input: map[string]any{
			"param1": "value1",
		},
	}

	if toolUse.Name != "test_tool" {
		t.Errorf("Expected tool name 'test_tool', got '%s'", toolUse.Name)
	}

	if toolUse.Input["param1"] != "value1" {
		t.Errorf("Expected param1 to be 'value1'")
	}
}

// TestTextAndToolResultContentCacheControlRoundTrip verifies the fix that let
// conversation/tool-result history participate in Anthropic prompt caching:
// previously TextContent and ToolResultContent had no CacheControl field at
// all, so nothing outside the (already-cached) system prompt and tool
// definitions could ever be marked, no matter what request-building logic
// did. Both the wire marshal shape and the round trip through Message's
// custom (Un)MarshalJSON must preserve it.
func TestTextAndToolResultContentCacheControlRoundTrip(t *testing.T) {
	cc := NewEphemeralPromptCacheControl()

	msg := Message{
		ID:   "m1",
		Role: RoleUser,
		Content: []ContentBlock{
			TextContent{Text: "hello", CacheControl: cc},
			ToolResultContent{ToolUseID: "t1", Content: "result", CacheControl: cc},
		},
	}

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var raw struct {
		Content []struct {
			Type         string `json:"type"`
			CacheControl *struct {
				Type string `json:"type"`
			} `json:"cache_control"`
		} `json:"content"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}
	if len(raw.Content) != 2 {
		t.Fatalf("expected 2 content blocks, got %d", len(raw.Content))
	}
	for i, block := range raw.Content {
		if block.CacheControl == nil || block.CacheControl.Type != "ephemeral" {
			t.Errorf("block %d (%s): expected ephemeral cache_control on the wire, got %+v", i, block.Type, block.CacheControl)
		}
	}

	var roundTripped Message
	if err := json.Unmarshal(data, &roundTripped); err != nil {
		t.Fatalf("Message.UnmarshalJSON: %v", err)
	}
	text, ok := roundTripped.Content[0].(TextContent)
	if !ok || text.CacheControl == nil || text.CacheControl.Type != "ephemeral" {
		t.Errorf("TextContent.CacheControl did not round-trip: %+v", roundTripped.Content[0])
	}
	toolResult, ok := roundTripped.Content[1].(ToolResultContent)
	if !ok || toolResult.CacheControl == nil || toolResult.CacheControl.Type != "ephemeral" {
		t.Errorf("ToolResultContent.CacheControl did not round-trip: %+v", roundTripped.Content[1])
	}
}

func TestPermissionResult(t *testing.T) {
	// Test Allow
	result := AllowWithUpdatedInput(nil)
	if !result.IsAllowed() {
		t.Error("Expected AllowWithUpdatedInput(nil) to return allowed result")
	}

	// Test Deny
	result = Deny("test reason")
	if !result.IsDenied() {
		t.Error("Expected Deny() to return denied result")
	}

	// Test Ask
	result = Ask("test question")
	if !result.IsAsk() {
		t.Error("Expected Ask() to return ask result")
	}
}

func TestSessionID(t *testing.T) {
	sessionID := NewSessionID("session-123")
	if sessionID.String() != "session-123" {
		t.Errorf("Expected 'session-123', got '%s'", sessionID.String())
	}
}

func TestTokenUsage(t *testing.T) {
	usage := TokenUsage{
		InputTokens:  100,
		OutputTokens: 200,
	}

	total := usage.InputTokens + usage.OutputTokens
	if total != 300 {
		t.Errorf("Expected total 300, got %d", total)
	}
}

func TestTranscriptRoundTripPreservesMessageMetadata(t *testing.T) {
	now := time.Now().UTC().Round(time.Second)
	stopSequence := "STOP"
	message := Message{
		ID:        MessageID("m1"),
		Role:      RoleAssistant,
		Content:   []ContentBlock{TextContent{Text: "done"}},
		Timestamp: now,
		Metadata: &MessageMetadata{
			TurnID:       "turn-1",
			StopReason:   StopReasonEndTurn,
			StopSequence: &stopSequence,
			Usage: &TokenUsage{
				InputTokens:  12,
				OutputTokens: 34,
			},
		},
	}

	entry := TranscriptEntryFromMessage(message, TurnID("ignored"))
	restored, ok := MessageFromTranscriptEntry(entry)
	if !ok {
		t.Fatal("expected transcript entry to restore a message")
	}
	if restored.Metadata == nil {
		t.Fatal("expected restored metadata")
	}
	if restored.Metadata.TurnID != "turn-1" {
		t.Fatalf("expected turn id turn-1, got %q", restored.Metadata.TurnID)
	}
	if restored.Metadata.StopReason != StopReasonEndTurn {
		t.Fatalf("expected stop reason %q, got %q", StopReasonEndTurn, restored.Metadata.StopReason)
	}
	if restored.Metadata.StopSequence == nil || *restored.Metadata.StopSequence != stopSequence {
		t.Fatalf("expected stop sequence %q", stopSequence)
	}
	if restored.Metadata.Usage == nil || restored.Metadata.Usage.InputTokens != 12 || restored.Metadata.Usage.OutputTokens != 34 {
		t.Fatalf("expected usage 12/34, got %#v", restored.Metadata.Usage)
	}
}

func TestTranscriptRoundTripPreservesCompactionMetadata(t *testing.T) {
	now := time.Now().UTC().Round(time.Second)
	message := Message{
		ID:        MessageID("compact_summary"),
		Role:      RoleSystem,
		Content:   []ContentBlock{TextContent{Text: "summary"}},
		Timestamp: now,
		Metadata: &MessageMetadata{
			TurnID: "compact",
			Compaction: &CompactionMetadata{
				Kind:                    "summary",
				PreCompactTokens:        1000,
				PostCompactTokens:       200,
				TargetTokens:            250,
				PreservedMessages:       3,
				PreservedTurns:          1,
				PreservedToolPairs:      1,
				BoundaryVersion:         CompactionBoundaryVersionV1,
				FirstPreservedMessageID: MessageID("tail-1"),
				LastPreservedMessageID:  MessageID("tail-3"),
				PreservedTailHash:       "hash-123",
			},
		},
	}

	entry := TranscriptEntryFromMessage(message, TurnID("ignored"))
	restored, ok := MessageFromTranscriptEntry(entry)
	if !ok {
		t.Fatal("expected transcript entry to restore a message")
	}
	if restored.Metadata == nil || restored.Metadata.Compaction == nil {
		t.Fatal("expected compaction metadata")
	}
	if restored.Metadata.Compaction.Kind != "summary" {
		t.Fatalf("expected compaction kind summary, got %q", restored.Metadata.Compaction.Kind)
	}
	if restored.Metadata.Compaction.PreCompactTokens != 1000 || restored.Metadata.Compaction.PostCompactTokens != 200 {
		t.Fatalf("unexpected compaction token metadata: %#v", restored.Metadata.Compaction)
	}
	if restored.Metadata.Compaction.PreservedMessages != 3 || restored.Metadata.Compaction.PreservedTurns != 1 || restored.Metadata.Compaction.PreservedToolPairs != 1 {
		t.Fatalf("unexpected compaction preservation metadata: %#v", restored.Metadata.Compaction)
	}
	if restored.Metadata.Compaction.BoundaryVersion != CompactionBoundaryVersionV1 {
		t.Fatalf("expected boundary version %d, got %d", CompactionBoundaryVersionV1, restored.Metadata.Compaction.BoundaryVersion)
	}
	if restored.Metadata.Compaction.FirstPreservedMessageID != MessageID("tail-1") || restored.Metadata.Compaction.LastPreservedMessageID != MessageID("tail-3") {
		t.Fatalf("unexpected preserved message ids: %#v", restored.Metadata.Compaction)
	}
	if restored.Metadata.Compaction.PreservedTailHash != "hash-123" {
		t.Fatalf("unexpected preserved tail hash %q", restored.Metadata.Compaction.PreservedTailHash)
	}
}

func TestTokenUsageCacheFieldsRoundTrip(t *testing.T) {
	msg := Message{
		ID:      "m1",
		Role:    RoleAssistant,
		Content: []ContentBlock{TextContent{Text: "hello"}},
		Metadata: &MessageMetadata{
			StopReason: "end_turn",
			Usage: &TokenUsage{
				InputTokens:              500,
				OutputTokens:             120,
				CacheReadInputTokens:     450,
				CacheCreationInputTokens: 50,
			},
		},
	}

	entry := TranscriptEntryFromMessage(msg, "turn-1")
	restored, ok := MessageFromTranscriptEntry(entry)
	if !ok {
		t.Fatal("MessageFromTranscriptEntry returned false")
	}
	if restored.Metadata == nil || restored.Metadata.Usage == nil {
		t.Fatal("restored message has no usage metadata")
	}
	u := restored.Metadata.Usage
	if u.InputTokens != 500 {
		t.Errorf("InputTokens: got %d, want 500", u.InputTokens)
	}
	if u.OutputTokens != 120 {
		t.Errorf("OutputTokens: got %d, want 120", u.OutputTokens)
	}
	if u.CacheReadInputTokens != 450 {
		t.Errorf("CacheReadInputTokens: got %d, want 450", u.CacheReadInputTokens)
	}
	if u.CacheCreationInputTokens != 50 {
		t.Errorf("CacheCreationInputTokens: got %d, want 50", u.CacheCreationInputTokens)
	}
}

func TestTokenUsageCacheFieldsZeroNotPersisted(t *testing.T) {
	msg := Message{
		ID:      "m2",
		Role:    RoleAssistant,
		Content: []ContentBlock{TextContent{Text: "hi"}},
		Metadata: &MessageMetadata{
			Usage: &TokenUsage{
				InputTokens:  100,
				OutputTokens: 40,
			},
		},
	}
	entry := TranscriptEntryFromMessage(msg, "turn-2")
	usageMap, ok := entry.Metadata["usage"].(map[string]any)
	if !ok {
		t.Fatal("usage not in transcript metadata")
	}
	if _, has := usageMap["cache_read_input_tokens"]; has {
		t.Error("cache_read_input_tokens should be absent when zero")
	}
	if _, has := usageMap["cache_creation_input_tokens"]; has {
		t.Error("cache_creation_input_tokens should be absent when zero")
	}
}

// --- from session_test.go ---

func TestSessionMetadataSchemaVersion_IsSet(t *testing.T) {
	if SessionMetadataSchemaVersion == 0 {
		t.Fatal("SessionMetadataSchemaVersion must be > 0")
	}
}

func TestSessionMetadata_SchemaVersionSerializes(t *testing.T) {
	meta := SessionMetadata{
		ID:            SessionID("test-session"),
		SchemaVersion: SessionMetadataSchemaVersion,
	}

	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	var loaded SessionMetadata
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if loaded.SchemaVersion != SessionMetadataSchemaVersion {
		t.Fatalf("expected SchemaVersion %d after round-trip, got %d",
			SessionMetadataSchemaVersion, loaded.SchemaVersion)
	}
}

func TestSessionMetadata_LegacyZeroVersionRoundTrip(t *testing.T) {
	// Simulate a pre-versioning persisted session (no schema_version field)
	legacy := `{"id":"legacy-session","status":"active","total_turns":3}`

	var meta SessionMetadata
	if err := json.Unmarshal([]byte(legacy), &meta); err != nil {
		t.Fatalf("json.Unmarshal legacy: %v", err)
	}
	// Legacy sessions have SchemaVersion == 0 (zero value)
	if meta.SchemaVersion != 0 {
		t.Fatalf("expected legacy SchemaVersion 0, got %d", meta.SchemaVersion)
	}
	if meta.ID != "legacy-session" {
		t.Fatalf("expected ID 'legacy-session', got %q", meta.ID)
	}
}

// --- from tools_test.go ---

func TestStreamToolOutputReader(t *testing.T) {
	output := NewStreamToolOutput([]byte("hello"))

	data, err := io.ReadAll(output.Reader())
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("expected reader output %q, got %q", "hello", string(data))
	}
}

// What a session writes after a compaction is not part of what the compaction preserved: the session must still restore.
func TestACompactedSessionStillValidatesAfterItGoesOn(t *testing.T) {
	kept := []Message{
		{ID: "tail-1", Role: RoleAssistant, Content: []ContentBlock{ToolUseContent{ID: "call-1", Name: "write"}}, Metadata: &MessageMetadata{TurnID: "t1"}},
		{ID: "tail-2", Role: RoleUser, Content: []ContentBlock{ToolResultContent{ToolUseID: "call-1", Content: "ok"}}, Metadata: &MessageMetadata{TurnID: "t1"}},
	}
	hash, err := CanonicalTranscriptHash(kept)
	if err != nil {
		t.Fatal(err)
	}
	boundary := &CompactionMetadata{
		Kind: "summary", BoundaryVersion: CompactionBoundaryVersionV1,
		PreservedMessages: 2, PreservedTurns: 1, PreservedToolPairs: 1,
		FirstPreservedMessageID: "tail-1", LastPreservedMessageID: "tail-2", PreservedTailHash: hash,
	}
	summary := WithCompactionMetadata(Message{ID: "compact_summary", Role: RoleSystem, Content: []ContentBlock{TextContent{Text: "summary"}}}, boundary)
	marker := WithCompactionMetadata(Message{ID: "compact_marker", Role: RoleSystem, Content: []ContentBlock{TextContent{Text: "marker"}}}, boundary)

	conversation := append([]Message{summary, marker}, kept...)
	if err := ValidateCompactionBoundary(conversation); err != nil {
		t.Fatalf("right after the compaction: %v", err)
	}

	conversation = append(conversation, Message{ID: "later", Role: RoleUser, Content: []ContentBlock{TextContent{Text: "go on"}}, Metadata: &MessageMetadata{TurnID: "t2"}})
	if err := ValidateCompactionBoundary(conversation); err != nil {
		t.Fatalf("after the session went on: %v", err)
	}

	// A tail that lost a message is still refused.
	if err := ValidateCompactionBoundary(conversation[:3]); err == nil {
		t.Fatal("a preserved tail with a missing message must not validate")
	}
}
