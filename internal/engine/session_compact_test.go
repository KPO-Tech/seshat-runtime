package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/KPO-Tech/seshat-runtime/internal/execution"
	"github.com/KPO-Tech/seshat-runtime/internal/permissions"
	"github.com/KPO-Tech/seshat-runtime/internal/prompt"
	compact "github.com/KPO-Tech/seshat-runtime/internal/runtime/memory"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// manualStrategy is a compactor that can compact on demand and records what it was asked.
type manualStrategy struct {
	fakeCompactionStrategy
	calls    int
	gotExtra int
	// grows makes the compactor return a result no smaller than its input.
	grows bool
}

func (m *manualStrategy) Compact(_ context.Context, _ string, msgs []types.Message, _ types.ModelIdentifier, _ types.SessionID, _ types.TurnID, tr *compact.TrackingState) (compact.CompactionResult, error) {
	m.calls++
	if tr != nil {
		m.gotExtra = tr.ExtraTokens
	}
	post := 800
	if m.grows {
		post = 6_000
	}
	return compact.CompactionResult{
		Messages:          []types.Message{types.SystemMessage("compact_summary", "summary"), msgs[len(msgs)-1]},
		DidCompact:        true,
		PreCompactTokens:  5_000,
		PostCompactTokens: post,
	}, nil
}

func sessionWithMessages(t *testing.T, strategy compact.CompactionStrategy, n int) *Session {
	t.Helper()
	engine := NewEngine(nil, execution.NewOrchestrator(), nil, prompt.NewAssembler(),
		permissions.NewIntegrator(permissions.NewEngine()), registry.NewRegistry(), nil, DefaultConfig(), nil, nil)
	engine.loop.compactor = strategy
	msgs := make([]types.Message, n)
	for i := range msgs {
		role := types.RoleUser
		if i%2 == 1 {
			role = types.RoleAssistant
		}
		msgs[i] = types.Message{ID: types.MessageID(string(rune('a' + i))), Role: role, Content: []types.ContentBlock{types.TextContent{Text: "text"}}}
	}
	s, err := engine.NewSessionFromState(context.Background(), "s-compact", nil, msgs)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	return s
}

func TestSessionCompactReplacesTheConversation(t *testing.T) {
	strategy := &manualStrategy{}
	s := sessionWithMessages(t, strategy, 8)

	out, err := s.Compact(context.Background())
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if out.PreTokens != 5_000 || out.PostTokens != 800 {
		t.Errorf("outcome = %+v, want 5,000 -> 800", out)
	}
	if got := len(s.state.CloneMessages()); got != 2 {
		t.Errorf("the session holds %d messages, want the 2 the compactor returned", got)
	}
	if strategy.calls != 1 {
		t.Errorf("compactor called %d times, want 1", strategy.calls)
	}
	// The session is usable again afterwards.
	if _, err := s.Compact(context.Background()); errors.Is(err, ErrNothingToCompact) == false && err != nil {
		t.Errorf("a second compaction must not leave the session busy, got %v", err)
	}
}

func TestSessionCompactRefusesAShortConversation(t *testing.T) {
	strategy := &manualStrategy{}
	s := sessionWithMessages(t, strategy, 2)
	if _, err := s.Compact(context.Background()); !errors.Is(err, ErrNothingToCompact) {
		t.Errorf("err = %v, want ErrNothingToCompact", err)
	}
	if strategy.calls != 0 {
		t.Error("a short conversation must not reach the compactor")
	}
}

func TestSessionCompactRefusesWhileATurnRuns(t *testing.T) {
	s := sessionWithMessages(t, &manualStrategy{}, 8)
	if err := s.beginTurn(); err != nil {
		t.Fatal(err)
	}
	defer s.endTurn()
	if _, err := s.Compact(context.Background()); err == nil {
		t.Error("Compact must fail while a turn is in progress")
	}
}

func TestSessionCompactNeedsAManualCompactor(t *testing.T) {
	s := sessionWithMessages(t, &fakeCompactionStrategy{}, 8)
	if _, err := s.Compact(context.Background()); err == nil {
		t.Error("a strategy that cannot compact on demand must be reported")
	}
}

func TestSessionCompactLeavesTheConversationWhenItWouldNotShrink(t *testing.T) {
	s := sessionWithMessages(t, &manualStrategy{grows: true}, 8)
	if _, err := s.Compact(context.Background()); !errors.Is(err, ErrCompactionWouldNotShrink) {
		t.Fatalf("err = %v, want ErrCompactionWouldNotShrink", err)
	}
	if got := len(s.state.CloneMessages()); got != 8 {
		t.Errorf("the conversation must be untouched, %d messages left", got)
	}
}
