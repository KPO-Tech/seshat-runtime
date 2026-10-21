package memory

import (
	"errors"
	"strings"
	"testing"

	"github.com/KPO-Tech/seshat-runtime/internal/model"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// These guard M1 in TODO.md: BlockingLimitTokens/ManualCompactBufferTokens
// were computed but nothing ever checked a request against them, so an
// oversized request (even after compaction) failed at the provider instead
// of with a clear, typed, local error.

func TestCheckBlockingLimit_ReturnsTypedErrorWhenOverLimit(t *testing.T) {
	// A model with a small window, so that a plain long message is enough to exceed its blocking limit.
	model.Global.Register(model.Metadata{ID: "blocking-limit-small", Provider: "test-small", ContextWindow: model.ContextWindow{MaxTokens: 4_096}})
	engine := NewEngine(nil, DefaultConfig())
	m := types.ModelIdentifier{Provider: "test-small", Model: "blocking-limit-small"}
	messages := []types.Message{types.UserMessage("m1", strings.Repeat("this message is far longer than the window of the model. ", 1_000))}

	err := engine.CheckBlockingLimit("", messages, m, nil)
	if err == nil {
		t.Fatal("expected a blocking-limit error")
	}
	var engineErr *types.EngineError
	if !errors.As(err, &engineErr) {
		t.Fatalf("expected a *types.EngineError, got %T: %v", err, err)
	}
	if engineErr.Code != types.ErrCodeCompactFull {
		t.Errorf("expected code %q, got %q", types.ErrCodeCompactFull, engineErr.Code)
	}
}

func TestCheckBlockingLimit_NilWhenWithinLimit(t *testing.T) {
	engine := NewEngine(nil, DefaultConfig())
	model := types.ModelIdentifier{Provider: types.APIProviderAnthropic, Model: "claude-3-5-sonnet-20241022"}
	messages := []types.Message{types.UserMessage("m1", "hi")}

	if err := engine.CheckBlockingLimit("", messages, model, nil); err != nil {
		t.Fatalf("expected no error for a small request, got %v", err)
	}
}
