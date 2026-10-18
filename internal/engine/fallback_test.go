package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/KPO-Tech/seshat-runtime/internal/permissions"
	compact "github.com/KPO-Tech/seshat-runtime/internal/runtime/memory"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// failingSummaryStrategy is a compactor whose summary can never be written. The conversation is over the hard limit
// until it has been trimmed, if it can be.
type failingSummaryStrategy struct {
	fakeCompactionStrategy
	over      bool
	canTrim   bool
	trimCalls int
	trimmed   bool
}

func (f *failingSummaryStrategy) AutoCompact(_ context.Context, _ string, msgs []types.Message, _ types.ModelIdentifier, _ types.SessionID, _ types.TurnID, tr *compact.TrackingState) (compact.CompactionResult, error) {
	failures := 0
	if tr != nil {
		failures = tr.ConsecutiveFailures
	}
	return compact.CompactionResult{Messages: msgs, ConsecutiveFailures: failures + 1}, errors.New("no summary generated")
}

func (f *failingSummaryStrategy) CheckBlockingLimit(string, []types.Message, types.ModelIdentifier, *compact.TrackingState) error {
	if f.over && !f.trimmed {
		return types.NewError(types.ErrCodeCompactFull, "request over the hard limit")
	}
	return nil
}

func (f *failingSummaryStrategy) EmergencyTrim(_ string, msgs []types.Message, _ types.ModelIdentifier, _ *compact.TrackingState) (compact.CompactionResult, bool) {
	f.trimCalls++
	if !f.canTrim {
		return compact.CompactionResult{}, false
	}
	f.trimmed = true
	return compact.CompactionResult{Messages: msgs[len(msgs)-2:], DidCompact: true, PreCompactTokens: 900, PostCompactTokens: 200}, true
}

func runWithFailingSummary(t *testing.T, f *failingSummaryStrategy) (RunResult, []types.RuntimeEventType) {
	t.Helper()
	loop := NewLoop(nil, nil, f, nil, permissions.NewIntegrator(permissions.NewEngine()), nil,
		&LoopConfig{MaxIterations: 1, AutoCompact: true}, nil)
	loop.callModelFn = func(ctx context.Context, state *MutableState, req RunRequest) (*types.APIResponse, error) {
		return &types.APIResponse{Role: types.RoleAssistant, Content: []types.ContentBlock{types.TextContent{Text: "ok"}},
			StopReason: types.StopReasonEndTurn, Model: req.Model, ID: "r1"}, nil
	}
	var seen []types.RuntimeEventType
	ctx := context.WithValue(context.Background(), types.RuntimeEventEmitterKey, func(ev types.RuntimeEvent) { seen = append(seen, ev.Type) })
	var msgs []types.Message
	for i := 0; i < 6; i++ {
		msgs = append(msgs, types.UserMessage(fmt.Sprintf("m%d", i), strings.Repeat("x", 50)))
	}
	result := loop.Run(ctx, RunRequest{Messages: msgs, SessionID: "s1", TurnID: "t1",
		Model: types.ModelIdentifier{Provider: types.APIProviderAnthropic, Model: "claude-3-5-sonnet-20241022"}})
	return result, seen
}

func TestAFailedSummaryNoLongerEndsTheTurn(t *testing.T) {
	// The summary fails but the conversation still fits: the turn goes on, and the failure is counted.
	result, _ := runWithFailingSummary(t, &failingSummaryStrategy{})
	if result.Error != nil {
		t.Fatalf("a summary that cannot be written must not fail the turn: %v", result.Error)
	}
	if result.AutoCompactFailureCount != 1 {
		t.Errorf("the failure must still count toward the circuit breaker, count = %d", result.AutoCompactFailureCount)
	}
}

func TestAFailedSummaryOverTheLimitIsTrimmedWithoutAModel(t *testing.T) {
	f := &failingSummaryStrategy{over: true, canTrim: true}
	result, events := runWithFailingSummary(t, f)
	if result.Error != nil {
		t.Fatalf("the turn must go on after an emergency trim: %v", result.Error)
	}
	if f.trimCalls != 1 || !result.Compacted {
		t.Errorf("trim calls %d, compacted %v", f.trimCalls, result.Compacted)
	}
	// 6 messages before, 2 kept, plus the assistant answer of the turn.
	if len(result.Messages) != 3 {
		t.Errorf("%d messages after the trim and the answer, want 3", len(result.Messages))
	}
	found := false
	for _, e := range events {
		found = found || e == types.RuntimeEventTypeCompaction
	}
	if !found {
		t.Error("the host must be told the conversation was compacted")
	}
}

func TestAFailedSummaryOverTheLimitThatCannotBeTrimmedStillFailsLocally(t *testing.T) {
	f := &failingSummaryStrategy{over: true, canTrim: false}
	result, _ := runWithFailingSummary(t, f)
	var engineErr *types.EngineError
	if !errors.As(result.Error, &engineErr) || engineErr.Code != types.ErrCodeCompactFull {
		t.Fatalf("error = %v, want the typed compact_full error: a request that cannot fit must not reach the provider", result.Error)
	}
}
