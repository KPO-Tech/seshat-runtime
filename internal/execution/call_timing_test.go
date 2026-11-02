package execution

import (
	"context"
	"testing"
	"time"

	tool "github.com/KPO-Tech/seshat-runtime/internal/tools/contract"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

func runStub(t *testing.T, call func(context.Context, tool.CallInput, types.CanUseToolFn) (tool.CallResult, error)) tool.CallResult {
	t.Helper()
	allow := func(ctx context.Context, request types.ToolPermissionRequest) types.PermissionResult {
		return types.AllowWithInput("allow", nil)
	}
	result, err := NewOrchestrator().Execute(context.Background(), ExecuteRequest{
		ToolUses:        []types.ToolUseContent{{ID: "t1", Name: "stub", Input: map[string]any{}}},
		Tools:           map[string]tool.Tool{"stub": &stubTool{call: call}},
		PermissionCheck: allow,
		SessionID:       types.SessionID("s1"),
		TurnID:          types.TurnID("turn-1"),
		PermissionMode:  types.PermissionModeOnRequest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Results) != 1 || result.Results[0].Metadata == nil {
		t.Fatalf("want one result with metadata, got %+v", result.Results)
	}
	return result.Results[0]
}

// Every call says when it started and how long it took, so a host can tell what
// the call did (a pull request created inside that window) from what it read.
func TestToolCallRecordsWhenItStartedAndHowLongItTook(t *testing.T) {
	before := time.Now()
	result := runStub(t, func(context.Context, tool.CallInput, types.CanUseToolFn) (tool.CallResult, error) {
		time.Sleep(20 * time.Millisecond)
		return tool.NewTextResult("ok"), nil
	})
	after := time.Now()

	raw, _ := result.Metadata.Additional[ExecutionStartedAtKey].(string)
	started, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		t.Fatalf("start %q: %v", raw, err)
	}
	if started.Before(before.Add(-time.Millisecond)) || started.After(after) {
		t.Fatalf("start %v is outside the call (%v to %v)", started, before, after)
	}
	if result.Metadata.ExecutionDuration < 20 {
		t.Fatalf("duration %d ms, want at least 20", result.Metadata.ExecutionDuration)
	}
}

func TestToolCallKeepsTheDurationTheToolGave(t *testing.T) {
	result := runStub(t, func(context.Context, tool.CallInput, types.CanUseToolFn) (tool.CallResult, error) {
		res := tool.NewTextResult("ok")
		res.Metadata = &tool.ResultMetadata{ExecutionDuration: 1234}
		return res, nil
	})
	if result.Metadata.ExecutionDuration != 1234 {
		t.Fatalf("duration %d, want the tool's 1234", result.Metadata.ExecutionDuration)
	}
	if _, ok := result.Metadata.Additional[ExecutionStartedAtKey].(string); !ok {
		t.Fatal("the start is missing")
	}
}

func TestFailedToolCallIsTimedToo(t *testing.T) {
	result := runStub(t, func(context.Context, tool.CallInput, types.CanUseToolFn) (tool.CallResult, error) {
		return tool.CallResult{}, context.DeadlineExceeded
	})
	if _, ok := result.Metadata.Additional[ExecutionStartedAtKey].(string); !ok {
		t.Fatal("a failed call has no start")
	}
}
