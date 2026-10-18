package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/KPO-Tech/seshat-runtime/internal/execution"
	"github.com/KPO-Tech/seshat-runtime/internal/permissions"
	"github.com/KPO-Tech/seshat-runtime/internal/prompt"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

type countingRecaller struct {
	block   string
	calls   int
	gotUser string
	gotText string
}

func (r *countingRecaller) Recall(_ context.Context, user, query string) string {
	r.calls++
	r.gotUser, r.gotText = user, query
	return r.block
}

func recallSession(t *testing.T, recaller MemoryRecaller) *Session {
	t.Helper()
	cfg := DefaultConfig()
	cfg.MemoryRecaller = recaller
	engine := NewEngine(nil, execution.NewOrchestrator(), nil, prompt.NewAssembler(),
		permissions.NewIntegrator(permissions.NewEngine()), registry.NewRegistry(), nil, cfg, nil, nil)
	s, err := engine.NewSessionFromState(context.Background(), "s-recall", nil,
		[]types.Message{types.UserMessage("m1", "how do I cut a release of seshat-runtime?")})
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	return s
}

func TestSessionPromptCarriesTheRecalledMemory(t *testing.T) {
	r := &countingRecaller{block: "## Memory from earlier sessions\n**user** (preference)\n- prefers French\n"}
	s := recallSession(t, r)
	ctx := types.WithAgentUserID(context.Background(), "user-7")

	req, err := s.buildAPIRequest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(req.SystemPrompt, "prefers French") {
		t.Errorf("the system prompt must carry the recalled memory")
	}
	if r.gotUser != "user-7" || !strings.Contains(r.gotText, "seshat-runtime") {
		t.Errorf("recall got user %q and text %q, want user-7 and the first message", r.gotUser, r.gotText)
	}
}

func TestRecallIsComputedOncePerSession(t *testing.T) {
	r := &countingRecaller{block: "## Memory from earlier sessions\n- a fact\n"}
	s := recallSession(t, r)
	for i := 0; i < 3; i++ {
		if _, err := s.buildAPIRequest(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if r.calls != 1 {
		t.Errorf("recall ran %d times, want once so the prompt stays cacheable", r.calls)
	}
}

func TestNoRecallerMeansNoMemoryBlock(t *testing.T) {
	s := recallSession(t, nil)
	req, err := s.buildAPIRequest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(req.SystemPrompt, "Memory from earlier sessions") {
		t.Error("without a recaller the prompt must carry no long-term memory")
	}
}

func TestEmptyRecallAddsNothing(t *testing.T) {
	s := recallSession(t, &countingRecaller{block: ""})
	req, _ := s.buildAPIRequest(context.Background())
	if strings.Contains(req.SystemPrompt, "Memory from earlier sessions") {
		t.Error("an empty recall must add nothing")
	}
}
