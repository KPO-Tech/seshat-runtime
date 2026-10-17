package memory

import (
	"errors"
	"testing"

	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// These guard M1 in TODO.md: BlockingLimitTokens/ManualCompactBufferTokens
// were computed but nothing ever checked a request against them, so an
// oversized request (even after compaction) failed at the provider instead
// of with a clear, typed, local error.

func TestCheckBlockingLimit_ReturnsTypedErrorWhenOverLimit(t *testing.T) {
	cfg := DefaultConfig()
	// Drives the blocking limit deep negative regardless of the real model's
	// context window size, so a short test message is enough to exceed it
	// without needing to generate a huge amount of content or hardcode the
	// window size of whatever model the test happens to use.
	cfg.ManualCompactBufferTokens = 1 << 30
	engine := NewEngine(nil, cfg)
	model := types.ModelIdentifier{Provider: types.APIProviderAnthropic, Model: "claude-3-5-sonnet-20241022"}
	messages := []types.Message{types.UserMessage("m1", "this message alone is already more than ten tokens long")}

	err := engine.CheckBlockingLimit("", messages, model, nil)
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
