package engine

import (
	"context"
	"testing"
	"time"

	"github.com/KPO-Tech/seshat-runtime/internal/execution"
	"github.com/KPO-Tech/seshat-runtime/internal/permissions"
	"github.com/KPO-Tech/seshat-runtime/internal/prompt"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

type recordedExtraction struct {
	user     string
	messages int
}

type recordingExtractor struct{ got chan recordedExtraction }

func (r *recordingExtractor) Extract(_ context.Context, user string, msgs []types.Message) error {
	r.got <- recordedExtraction{user: user, messages: len(msgs)}
	return nil
}

func extractionSession(t *testing.T, extractor MemoryExtractor) *Session {
	t.Helper()
	cfg := DefaultConfig()
	cfg.MemoryExtractor = extractor
	engine := NewEngine(nil, execution.NewOrchestrator(), nil, prompt.NewAssembler(),
		permissions.NewIntegrator(permissions.NewEngine()), registry.NewRegistry(), nil, cfg, nil, nil)
	msgs := []types.Message{types.UserMessage("m1", "hello"), types.UserMessage("m2", "again")}
	s, err := engine.NewSessionFromState(context.Background(), "s-extract", nil, msgs)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	return s
}

func TestClosingASessionHandsTheConversationToTheExtractor(t *testing.T) {
	ex := &recordingExtractor{got: make(chan recordedExtraction, 1)}
	s := extractionSession(t, ex)
	s.rememberUserID(types.WithAgentUserID(context.Background(), "user-42"))

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-ex.got:
		if got.user != "user-42" || got.messages != 2 {
			t.Errorf("extraction = %+v, want user-42 with the 2 messages", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the extractor was never called")
	}
}

func TestExtractionFallsBackToTheLocalUser(t *testing.T) {
	ex := &recordingExtractor{got: make(chan recordedExtraction, 1)}
	s := extractionSession(t, ex)
	_ = s.Close()
	select {
	case got := <-ex.got:
		if got.user != "local" {
			t.Errorf("user = %q, want local", got.user)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the extractor was never called")
	}
}

func TestNoExtractorMeansNothingIsStored(t *testing.T) {
	s := extractionSession(t, nil)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}
