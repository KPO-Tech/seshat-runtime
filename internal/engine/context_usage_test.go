package engine

import (
	"context"
	"testing"

	"github.com/KPO-Tech/seshat-runtime/internal/permissions"
	compact "github.com/KPO-Tech/seshat-runtime/internal/runtime/memory"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

func runWithUsageEvents(t *testing.T, usage types.TokenUsage) []*types.ContextUsageRuntimeEvent {
	t.Helper()
	loop := NewLoop(nil, nil, compact.NewEngine(nil, nil), nil, permissions.NewIntegrator(permissions.NewEngine()), nil,
		&LoopConfig{MaxIterations: 1, AutoCompact: true, EnableStreaming: false}, nil)
	loop.callModelFn = func(ctx context.Context, state *MutableState, req RunRequest) (*types.APIResponse, error) {
		return &types.APIResponse{
			Role: types.RoleAssistant, Content: []types.ContentBlock{types.TextContent{Text: "done"}},
			StopReason: types.StopReasonEndTurn, Model: req.Model, ID: "r1", Usage: usage,
		}, nil
	}
	var events []*types.ContextUsageRuntimeEvent
	ctx := context.WithValue(context.Background(), types.RuntimeEventEmitterKey, func(ev types.RuntimeEvent) {
		if ev.Type == types.RuntimeEventTypeContextUsage {
			events = append(events, ev.ContextUsageEvent)
		}
	})
	result := loop.Run(ctx, RunRequest{
		Messages:  []types.Message{types.UserMessage("m1", "hi")},
		SessionID: types.SessionID("s1"),
		TurnID:    types.TurnID("t1"),
		Model:     types.ModelIdentifier{Provider: types.APIProviderAnthropic, Model: "claude-3-5-sonnet-20241022"},
	})
	if result.Error != nil {
		t.Fatalf("Run failed: %v", result.Error)
	}
	return events
}

func TestContextUsageIsEmittedBeforeAndAfterTheModelCall(t *testing.T) {
	events := runWithUsageEvents(t, types.TokenUsage{InputTokens: 1_000, OutputTokens: 234, CacheReadInputTokens: 66})
	if len(events) != 2 {
		t.Fatalf("expected an estimate before the call and the reported figure after it, got %d events", len(events))
	}
	before, after := events[0], events[1]
	if before.Source != "estimate" || before.UsedTokens <= 0 {
		t.Errorf("before the call: %+v, want a positive estimate", before)
	}
	if after.Source != "reported" || after.UsedTokens != 1_300 {
		t.Errorf("after the call: %+v, want the reported 1,000 + 66 cached + 234 output = 1,300", after)
	}
	if after.WindowTokens <= 0 || after.AutoCompactAtTokens <= 0 || after.AutoCompactAtTokens >= after.BlockingLimitTokens {
		t.Errorf("thresholds out of order: %+v", after)
	}
}

func TestContextUsageFallsBackToTheEstimateWhenTheProviderReportsNothing(t *testing.T) {
	events := runWithUsageEvents(t, types.TokenUsage{})
	if len(events) != 2 || events[1].Source != "estimate" || events[1].UsedTokens <= 0 {
		t.Errorf("without usage the figure stays an estimate, got %+v", events)
	}
}

func TestContextUsageStaysSilentWithoutAnEmitter(t *testing.T) {
	loop := NewLoop(nil, nil, compact.NewEngine(nil, nil), nil, permissions.NewIntegrator(permissions.NewEngine()), nil,
		&LoopConfig{MaxIterations: 1, AutoCompact: true}, nil)
	// No emitter in the context: must not panic.
	loop.emitContextUsage(context.Background(), &MutableState{}, RunRequest{})
}

// reportingStrategy is a fakeCompactionStrategy that also reports usage, as the default Engine does.
type reportingStrategy struct {
	fakeCompactionStrategy
	due bool
}

func (r *reportingStrategy) UsedTokens(string, []types.Message, *compact.TrackingState) int {
	return 900
}
func (r *reportingStrategy) Budget(types.ModelIdentifier) compact.ContextBudget {
	return compact.ContextBudget{WindowTokens: 1_000, AutoCompactAtTokens: 850, BlockingLimitTokens: 970}
}
func (r *reportingStrategy) ShouldCompact(string, []types.Message, types.ModelIdentifier, *compact.TrackingState) bool {
	return r.due
}

func TestCompactionIsAnnouncedAndReportedWhenItHappens(t *testing.T) {
	strategy := &reportingStrategy{due: true}
	strategy.didCompact = true
	strategy.compactedMessages = []types.Message{types.UserMessage("c1", "summary")}
	strategy.preTokens, strategy.postTokens = 900, 300

	loop := NewLoop(nil, nil, strategy, nil, permissions.NewIntegrator(permissions.NewEngine()), nil,
		&LoopConfig{MaxIterations: 1, AutoCompact: true}, nil)
	loop.callModelFn = func(ctx context.Context, state *MutableState, req RunRequest) (*types.APIResponse, error) {
		return &types.APIResponse{Role: types.RoleAssistant, Content: []types.ContentBlock{types.TextContent{Text: "ok"}},
			StopReason: types.StopReasonEndTurn, Model: req.Model, ID: "r1"}, nil
	}
	var seen []types.RuntimeEventType
	var compacted *types.CompactionRuntimeEvent
	ctx := context.WithValue(context.Background(), types.RuntimeEventEmitterKey, func(ev types.RuntimeEvent) {
		seen = append(seen, ev.Type)
		if ev.Type == types.RuntimeEventTypeCompaction {
			compacted = ev.CompactionEvent
		}
	})
	result := loop.Run(ctx, RunRequest{
		Messages:  []types.Message{types.UserMessage("m1", "hi")},
		SessionID: "s1", TurnID: "t1",
		Model: types.ModelIdentifier{Provider: types.APIProviderAnthropic, Model: "claude-3-5-sonnet-20241022"},
	})
	if result.Error != nil {
		t.Fatalf("Run failed: %v", result.Error)
	}
	order := map[types.RuntimeEventType]int{}
	for i, typ := range seen {
		if _, ok := order[typ]; !ok {
			order[typ] = i
		}
	}
	compacting, hasCompacting := order[types.RuntimeEventTypeCompacting]
	done, hasDone := order[types.RuntimeEventTypeCompaction]
	usage, hasUsage := order[types.RuntimeEventTypeContextUsage]
	if !hasCompacting || !hasDone || !hasUsage || !(compacting < done && done < usage) {
		t.Fatalf("want compacting, then compacted, then the usage after it; got %v", seen)
	}
	if compacted == nil || compacted.PreCompactTokens != 900 || compacted.PostCompactTokens != 300 {
		t.Errorf("compacted event = %+v, want 900 -> 300", compacted)
	}
}

func TestNoCompactingEventWhenNothingIsDue(t *testing.T) {
	strategy := &reportingStrategy{due: false}
	loop := NewLoop(nil, nil, strategy, nil, permissions.NewIntegrator(permissions.NewEngine()), nil,
		&LoopConfig{MaxIterations: 1, AutoCompact: true}, nil)
	loop.callModelFn = func(ctx context.Context, state *MutableState, req RunRequest) (*types.APIResponse, error) {
		return &types.APIResponse{Role: types.RoleAssistant, Content: []types.ContentBlock{types.TextContent{Text: "ok"}},
			StopReason: types.StopReasonEndTurn, Model: req.Model, ID: "r1"}, nil
	}
	ctx := context.WithValue(context.Background(), types.RuntimeEventEmitterKey, func(ev types.RuntimeEvent) {
		if ev.Type == types.RuntimeEventTypeCompacting || ev.Type == types.RuntimeEventTypeCompaction {
			t.Errorf("unexpected %s event", ev.Type)
		}
	})
	loop.Run(ctx, RunRequest{
		Messages:  []types.Message{types.UserMessage("m1", "hi")},
		SessionID: "s1", TurnID: "t1",
		Model: types.ModelIdentifier{Provider: types.APIProviderAnthropic, Model: "claude-3-5-sonnet-20241022"},
	})
}

func TestFirstFigureOfATurnStartsFromTheAnchorOfThePreviousTurn(t *testing.T) {
	loop := NewLoop(nil, nil, compact.NewEngine(nil, nil), nil, permissions.NewIntegrator(permissions.NewEngine()), nil,
		&LoopConfig{MaxIterations: 1, AutoCompact: true, EnableStreaming: false}, nil)
	loop.callModelFn = func(ctx context.Context, state *MutableState, req RunRequest) (*types.APIResponse, error) {
		return &types.APIResponse{
			Role: types.RoleAssistant, Content: []types.ContentBlock{types.TextContent{Text: "done"}},
			StopReason: types.StopReasonEndTurn, Model: req.Model, ID: "r1", Usage: types.TokenUsage{InputTokens: 40_000, OutputTokens: 100},
		}, nil
	}
	var events []*types.ContextUsageRuntimeEvent
	ctx := context.WithValue(context.Background(), types.RuntimeEventEmitterKey, func(ev types.RuntimeEvent) {
		if ev.Type == types.RuntimeEventTypeContextUsage {
			events = append(events, ev.ContextUsageEvent)
		}
	})
	result := loop.Run(ctx, RunRequest{
		Messages:    []types.Message{types.UserMessage("m1", "hi")},
		SessionID:   types.SessionID("s1"),
		TurnID:      types.TurnID("t2"),
		Model:       types.ModelIdentifier{Provider: types.APIProviderAnthropic, Model: "claude-3-5-sonnet-20241022"},
		UsageAnchor: &compact.UsageAnchor{Tokens: 30_000, MessageCount: 1},
	})
	if result.Error != nil {
		t.Fatalf("Run failed: %v", result.Error)
	}
	if len(events) == 0 || events[0].Source != "reported" || events[0].UsedTokens != 30_000 {
		t.Errorf("first figure = %+v, want the carried-over 30,000 reported", events)
	}
	if result.UsageAnchor == nil || result.UsageAnchor.Tokens != 40_100 {
		t.Errorf("result anchor = %+v, want the new reported 40,100", result.UsageAnchor)
	}
}

func TestSessionStateDropsTheAnchorWhenMessagesOrModelChange(t *testing.T) {
	a := types.ModelIdentifier{Provider: types.APIProviderAnthropic, Model: "a"}
	b := types.ModelIdentifier{Provider: types.APIProviderAnthropic, Model: "b"}
	s := &SessionState{}
	s.RememberUsageAnchor(&compact.UsageAnchor{Tokens: 10, MessageCount: 2}, a)
	if got := s.UsageAnchorFor(a); got == nil || got.Tokens != 10 {
		t.Fatalf("same model: %+v", got)
	}
	if s.UsageAnchorFor(b) != nil {
		t.Error("another model counts differently, the anchor must not carry over")
	}
	s.ReplaceMessages(nil)
	if s.UsageAnchorFor(a) != nil {
		t.Error("rewriting the messages must drop the anchor")
	}
}

func TestUsageAnchorSurvivesALoadOfTheSession(t *testing.T) {
	model := types.ModelIdentifier{Provider: types.APIProviderAnthropic, Model: "a"}
	other := types.ModelIdentifier{Provider: types.APIProviderOpenAI, Model: "b"}
	messages := []types.Message{types.UserMessage("m1", "hi"), types.UserMessage("m2", "again")}

	metadata := &types.SessionMetadata{ID: "s1"}
	live := NewSessionState("s1", "t1", messages, nil, 1, 0, metadata)
	live.RememberUsageAnchor(&compact.UsageAnchor{Tokens: 1234, MessageCount: len(messages)}, model)

	// A host that keeps no live session loads it again from the stored metadata and messages.
	loaded := NewSessionState("s1", "t2", messages, nil, 1, 0, metadata)
	loaded.restoreUsageAnchor()
	if got := loaded.UsageAnchorFor(model); got == nil || got.Tokens != 1234 {
		t.Fatalf("anchor after a load = %+v, want 1234 tokens", got)
	}
	if loaded.UsageAnchorFor(other) != nil {
		t.Error("the model was switched between two turns: the anchor counted by the first model must not be used")
	}

	// Messages that no longer match what the anchor covered drop it.
	changed := NewSessionState("s1", "t2", messages[:1], nil, 1, 0, metadata)
	changed.restoreUsageAnchor()
	if changed.UsageAnchorFor(model) != nil {
		t.Error("an anchor that covers more messages than were loaded must be dropped")
	}

	// Rewriting the messages (compaction) clears what is stored too.
	live.ReplaceMessages(messages[:1])
	if metadata.UsageAnchorTokens != 0 || metadata.UsageAnchorModel != "" {
		t.Errorf("metadata still holds the anchor after the messages were rewritten: %+v", metadata)
	}
}
