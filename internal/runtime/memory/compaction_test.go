package memory

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// ─── message helpers ──────────────────────────────────────────────────────────

func makeToolUseMsg(id, toolUseID, toolName string) types.Message {
	return types.AssistantMessage(id, []types.ContentBlock{
		types.ToolUseContent{ID: toolUseID, Name: toolName, Input: map[string]any{}},
	})
}

func makeToolResultMsg(id, toolUseID, content string) types.Message {
	msg := types.UserMessage(id, "")
	msg.Content = []types.ContentBlock{
		types.ToolResultContent{ToolUseID: toolUseID, Content: content},
	}
	return msg
}

// makeTextMsg creates a plain text message for the given role.
func makeTextMsg(id string, role types.Role, content string) types.Message {
	if role == types.RoleAssistant {
		return types.AssistantMessage(id, []types.ContentBlock{types.TextContent{Text: content}})
	}
	return types.UserMessage(id, content)
}

// ─── adjustPreservedStartForToolPairs ─────────────────────────────────────────

// When no tool results are in the preserved tail, start must not change.
func TestAdjustPreservedStart_NoToolResults(t *testing.T) {
	messages := []types.Message{
		makeTextMsg("m0", types.RoleUser, "hello"),
		makeTextMsg("m1", types.RoleAssistant, "world"),
		makeTextMsg("m2", types.RoleUser, "again"),
	}
	// start = 1 → keep m1, m2; no tool results → no adjustment
	got := adjustPreservedStartForToolPairs(messages, 1)
	if got != 1 {
		t.Errorf("expected start=1, got %d", got)
	}
}

// When a tool_result is in the preserved tail but its tool_use is outside,
// the start must be widened to include the matching tool_use message.
func TestAdjustPreservedStart_WidentToIncludeToolUse(t *testing.T) {
	messages := []types.Message{
		makeToolUseMsg("m0", "tu-1", "bash"),           // index 0 — outside preserved
		makeToolResultMsg("m1", "tu-1", "ok"),          // index 1 — inside preserved tail
		makeTextMsg("m2", types.RoleAssistant, "done"), // index 2
	}
	// start = 1 means we preserve [m1, m2]; m1 has a tool_result for tu-1
	// but the matching tool_use is at m0 — expected: start widens to 0
	got := adjustPreservedStartForToolPairs(messages, 1)
	if got != 0 {
		t.Errorf("expected start widened to 0, got %d", got)
	}
}

// Multiple tool pairs where only some results are inside the tail.
func TestAdjustPreservedStart_MultiplePairsPreserved(t *testing.T) {
	messages := []types.Message{
		makeToolUseMsg("m0", "tu-1", "bash"),          // 0
		makeToolResultMsg("m1", "tu-1", "r1"),         // 1
		makeToolUseMsg("m2", "tu-2", "grep"),          // 2
		makeToolResultMsg("m3", "tu-2", "r2"),         // 3 — preserved start
		makeTextMsg("m4", types.RoleAssistant, "fin"), // 4
	}
	// start = 3 → preserve [m3, m4]; m3 references tu-2 whose use is at m2
	// → widen to 2; m2 has no tool_result dependency → stop
	got := adjustPreservedStartForToolPairs(messages, 3)
	if got != 2 {
		t.Errorf("expected start=2, got %d", got)
	}
}

// start at 0 means everything is preserved — no adjustment possible.
func TestAdjustPreservedStart_AlreadyAtZero(t *testing.T) {
	messages := []types.Message{
		makeToolUseMsg("m0", "tu-1", "bash"),
		makeToolResultMsg("m1", "tu-1", "ok"),
	}
	got := adjustPreservedStartForToolPairs(messages, 0)
	if got != 0 {
		t.Errorf("expected start=0, got %d", got)
	}
}

// start beyond slice length is a no-op.
func TestAdjustPreservedStart_StartBeyondSlice(t *testing.T) {
	messages := []types.Message{
		makeTextMsg("m0", types.RoleUser, "x"),
	}
	got := adjustPreservedStartForToolPairs(messages, 5)
	if got != 5 {
		t.Errorf("expected start=5 (no-op), got %d", got)
	}
}

// ─── MicroCompactor ──────────────────────────────────────────────────────────

func TestMicroCompactor_SmallResult_Unchanged(t *testing.T) {
	mc := NewMicroCompactor()
	result := types.ToolResultContent{ToolUseID: "id-1", Content: "small"}
	trimmed := mc.TrimToolResult(result)
	if trimmed.Content != "small" {
		t.Errorf("expected content unchanged, got %q", trimmed.Content)
	}
	if trimmed.Metadata != nil {
		t.Error("expected no metadata for small results")
	}
}

func TestMicroCompactor_LargeResult_Trimmed(t *testing.T) {
	mc := NewMicroCompactor()
	mc.SetMaxToolResultSize(100)
	mc.SetTrimStrategy(TrimStrategyTruncate) // Truncate respects maxToolResultSize exactly
	large := strings.Repeat("x", 500)
	result := types.ToolResultContent{ToolUseID: "id-2", Content: large}
	trimmed := mc.TrimToolResult(result)
	if len(trimmed.Content) > 100 {
		t.Errorf("expected content trimmed to ≤100 chars, got %d", len(trimmed.Content))
	}
}

func TestMicroCompactor_LargeResult_HasReplacementMetadata(t *testing.T) {
	mc := NewMicroCompactor()
	mc.SetMaxToolResultSize(50)
	large := strings.Repeat("y", 200)
	result := types.ToolResultContent{ToolUseID: "id-3", Content: large}
	trimmed := mc.TrimToolResult(result)

	if trimmed.Metadata == nil {
		t.Fatal("expected metadata to be set after trimming")
	}
	meta := *trimmed.Metadata
	if _, ok := meta["content_replacement"]; !ok {
		t.Error("expected content_replacement key in metadata")
	}
}

// ─── Engine — threshold calculations ─────────────────────────────────────────

// Target tokens must be ≤ threshold which must be ≤ effective window.
func TestEngine_Thresholds_Monotonic(t *testing.T) {
	e := NewEngine(nil, DefaultConfig())
	model := types.ModelIdentifier{
		Provider: types.APIProviderAnthropic,
		Model:    "claude-3-5-haiku-20241022",
	}
	effective := e.EffectiveContextWindow(model)
	threshold := e.AutoCompactThresholdTokens(model)
	target := e.TargetTokens(model)

	if threshold > effective {
		t.Errorf("threshold (%d) must be ≤ effective (%d)", threshold, effective)
	}
	if target > effective {
		t.Errorf("target (%d) must be ≤ effective (%d)", target, effective)
	}
	if threshold <= 0 {
		t.Errorf("threshold must be positive, got %d", threshold)
	}
	if target <= 0 {
		t.Errorf("target must be positive, got %d", target)
	}
}

// ─── Engine — AutoCompact paths ──────────────────────────────────────────────

// Circuit breaker: when ConsecutiveFailures ≥ MaxConsecutiveFailures, AutoCompact
// must return no-op immediately without calling ShouldCompact or the API.
func TestAutoCompact_CircuitBreakerTripped(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxConsecutiveFailures = 2
	e := NewEngine(nil, cfg) // nil apiClient — must not be reached
	model := types.ModelIdentifier{
		Provider: types.APIProviderAnthropic,
		Model:    "claude-3-5-haiku-20241022",
	}
	messages := []types.Message{makeTextMsg("m0", types.RoleUser, "ping")}
	tracking := &TrackingState{ConsecutiveFailures: 2}

	result, err := e.AutoCompact(context.Background(), "", messages, model, "s-1", "t-1", tracking)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.DidCompact {
		t.Error("expected no compaction when circuit breaker is tripped")
	}
	if result.ConsecutiveFailures != 2 {
		t.Errorf("expected ConsecutiveFailures=2, got %d", result.ConsecutiveFailures)
	}
}

// Below threshold: short content must not trigger compaction.
func TestAutoCompact_BelowThreshold(t *testing.T) {
	e := NewEngine(nil, DefaultConfig())
	model := types.ModelIdentifier{
		Provider: types.APIProviderAnthropic,
		Model:    "claude-3-5-haiku-20241022",
	}
	messages := []types.Message{
		makeTextMsg("m0", types.RoleUser, "hello"),
		makeTextMsg("m1", types.RoleAssistant, "world"),
	}
	tracking := &TrackingState{}

	result, err := e.AutoCompact(context.Background(), "", messages, model, "s-1", "t-1", tracking)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.DidCompact {
		t.Error("expected no compaction for short content below threshold")
	}
}

// Micro-compact sufficient: large tool results get trimmed without calling the
// summary API. The apiClient is nil to prove the API is not touched.
func TestAutoCompact_MicroCompactSufficient(t *testing.T) {
	e := NewEngine(nil, DefaultConfig()) // nil apiClient — must not be called
	model := types.ModelIdentifier{
		Provider: types.APIProviderAnthropic,
		Model:    "claude-3-5-haiku-20241022",
	}

	// Each message carries a 70 KB tool result (≈17.5K tokens). With 10 messages
	// that is ≈175K tokens — above the ~168K threshold for a 200K context model.
	// After micro-compact each is trimmed to 10K chars (≈2.5K tokens), giving
	// ≈25K total — well under the 99K target, so no summary API call is needed.
	largeContent := strings.Repeat("x", 70_000)
	messages := make([]types.Message, 10)
	for i := range messages {
		toolUseID := fmt.Sprintf("tu-%d", i)
		messages[i] = makeToolResultMsg(fmt.Sprintf("m-%d", i), toolUseID, largeContent)
	}

	tracking := &TrackingState{ConsecutiveFailures: 0}
	result, err := e.AutoCompact(context.Background(), "", messages, model, "s-1", "t-1", tracking)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.DidCompact {
		t.Error("expected compaction to trigger for large content")
	}
	if !result.UsedMicroCompact {
		t.Error("expected UsedMicroCompact=true")
	}
	if result.UsedSummaryCompact {
		t.Error("expected UsedSummaryCompact=false (no API call needed)")
	}
	if result.PostCompactTokens >= result.PreCompactTokens {
		t.Errorf("expected post-compact tokens (%d) < pre-compact (%d)",
			result.PostCompactTokens, result.PreCompactTokens)
	}
	if result.ConsecutiveFailures != 0 {
		t.Errorf("expected ConsecutiveFailures=0 after success, got %d", result.ConsecutiveFailures)
	}
}

// ShouldCompact returns false for content below threshold.
func TestShouldCompact_BelowThreshold(t *testing.T) {
	e := NewEngine(nil, DefaultConfig())
	model := types.ModelIdentifier{
		Provider: types.APIProviderAnthropic,
		Model:    "claude-3-5-haiku-20241022",
	}
	short := makeTextMsg("m0", types.RoleUser, "hi")
	if e.ShouldCompact("", []types.Message{short}, model, nil) {
		t.Error("expected ShouldCompact=false for tiny content")
	}
}

// ShouldCompact returns true when content fills the context.
func TestShouldCompact_AboveThreshold(t *testing.T) {
	e := NewEngine(nil, DefaultConfig())
	model := types.ModelIdentifier{
		Provider: types.APIProviderAnthropic,
		Model:    "claude-3-5-haiku-20241022",
	}
	threshold := e.AutoCompactThresholdTokens(model)
	// Rough char-to-token ratio is 4:1; pad 20% to ensure we cross the threshold.
	large := strings.Repeat("a", threshold*4+threshold)
	msg := makeTextMsg("m0", types.RoleUser, large)
	if !e.ShouldCompact("", []types.Message{msg}, model, nil) {
		t.Error("expected ShouldCompact=true for content above threshold")
	}
}

// buildPostCompactMessages must produce a summary message followed by a compact
// marker, then the preserved tail — in exactly that order.
func TestBuildPostCompactMessages_Order(t *testing.T) {
	e := NewEngine(nil, DefaultConfig())
	messages := []types.Message{
		makeTextMsg("m0", types.RoleUser, "old history"),
		makeTextMsg("m1", types.RoleAssistant, "response"),
		makeTextMsg("m2", types.RoleUser, "recent"),
	}
	result := e.buildPostCompactMessages(messages, "test summary", 0)

	if len(result) < 3 {
		t.Fatalf("expected at least 3 messages, got %d", len(result))
	}
	if result[0].ID != compactSummaryMessageID {
		t.Errorf("first message must be compact summary, got ID %q", result[0].ID)
	}
	if result[1].ID != compactMarkerMessageID {
		t.Errorf("second message must be compact marker, got ID %q", result[1].ID)
	}
}
