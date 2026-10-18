package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/KPO-Tech/seshat-runtime/internal/permissions"
	compact "github.com/KPO-Tech/seshat-runtime/internal/runtime/memory"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// maskingStrategy reports a configurable size and masks like the default Engine.
type maskingStrategy struct {
	reportingStrategy
	used int
}

func (m *maskingStrategy) UsedTokens(string, []types.Message, *compact.TrackingState) int {
	return m.used
}

func (m *maskingStrategy) MaskObservations(msgs []types.Message) ([]types.Message, compact.MaskResult) {
	return compact.MaskOldObservations(msgs)
}

func toolRounds(n int) []types.Message {
	msgs := []types.Message{types.UserMessage("u0", "read all the files")}
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("t%02d", i)
		msgs = append(msgs,
			types.Message{ID: types.MessageID("a" + id), Role: types.RoleAssistant, Content: []types.ContentBlock{
				types.ToolUseContent{ID: id, Name: "read_file", Input: map[string]any{"file_path": fmt.Sprintf("/f%02d.go", i)}}}},
			types.Message{ID: types.MessageID("r" + id), Role: types.RoleUser, Content: []types.ContentBlock{
				types.ToolResultContent{ToolUseID: id, Content: fmt.Sprintf("FILE %02d ", i) + strings.Repeat("x", 5_000)}}},
		)
	}
	return msgs
}

func resultOf(m types.Message) string {
	return m.Content[0].(types.ToolResultContent).Content
}

func runMasking(t *testing.T, used int) []types.Message {
	t.Helper()
	strategy := &maskingStrategy{used: used}
	loop := NewLoop(nil, nil, strategy, nil, permissions.NewIntegrator(permissions.NewEngine()), nil,
		&LoopConfig{MaxIterations: 1, AutoCompact: true}, nil)
	loop.callModelFn = func(ctx context.Context, state *MutableState, req RunRequest) (*types.APIResponse, error) {
		return &types.APIResponse{Role: types.RoleAssistant, Content: []types.ContentBlock{types.TextContent{Text: "ok"}},
			StopReason: types.StopReasonEndTurn, Model: req.Model, ID: "r1"}, nil
	}
	result := loop.Run(context.Background(), RunRequest{
		Messages:  toolRounds(14),
		SessionID: "s1", TurnID: "t1",
		Model: types.ModelIdentifier{Provider: types.APIProviderAnthropic, Model: "claude-3-5-sonnet-20241022"},
	})
	if result.Error != nil {
		t.Fatalf("Run failed: %v", result.Error)
	}
	return result.Messages
}

func TestOldToolResultsAreClearedWhenTheConversationNearsTheThreshold(t *testing.T) {
	// The strategy reports AutoCompactAt = 850; 700 is over 70 % of it.
	msgs := runMasking(t, 700)
	if !strings.HasPrefix(resultOf(msgs[2]), "[Result cleared") {
		t.Errorf("the oldest tool result must be cleared, got %q", resultOf(msgs[2])[:40])
	}
	if last := resultOf(msgs[len(msgs)-2]); !strings.Contains(last, strings.Repeat("x", 5_000)) {
		t.Error("the most recent tool result must stay whole")
	}
}

func TestToolResultsAreLeftAloneWhileThereIsRoom(t *testing.T) {
	// 300 is well under 70 % of 850.
	msgs := runMasking(t, 300)
	if strings.HasPrefix(resultOf(msgs[2]), "[Result cleared") {
		t.Error("nothing may be cleared while the conversation is small")
	}
}
