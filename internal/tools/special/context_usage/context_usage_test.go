package contextusage

import (
	"context"
	"strings"
	"testing"

	tool "github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

func callFor(t *testing.T, id types.SessionID) string {
	t.Helper()
	in := tool.CallInput{SessionID: id}
	res, err := NewTool().Call(context.Background(), in, nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	return res.Content
}

func TestSnapshotRemaining(t *testing.T) {
	s := Snapshot{UsedTokens: 90_000, WindowTokens: 128_000, AutoCompactAtTokens: 100_000}
	if s.Remaining() != 38_000 || s.UntilCompaction() != 10_000 {
		t.Errorf("remaining %d, until compaction %d", s.Remaining(), s.UntilCompaction())
	}
	over := Snapshot{UsedTokens: 200_000, WindowTokens: 128_000, AutoCompactAtTokens: 100_000}
	if over.Remaining() != 0 || over.UntilCompaction() != 0 {
		t.Error("figures must not go negative")
	}
}

func TestToolReportsTheRecordedSnapshot(t *testing.T) {
	Record("s-ctx", Snapshot{UsedTokens: 52_000, WindowTokens: 200_000, AutoCompactAtTokens: 170_000, Source: "reported"})
	defer Forget("s-ctx")
	got := callFor(t, "s-ctx")
	for _, want := range []string{"52000 of 200000", "148000 remaining", "170000", "reported"} {
		if !strings.Contains(got, want) {
			t.Errorf("result %q lacks %q", got, want)
		}
	}
}

func TestToolSaysWhenNothingIsKnownAndForgets(t *testing.T) {
	if got := callFor(t, "never-seen"); !strings.Contains(got, "not known yet") {
		t.Errorf("got %q", got)
	}
	Record("s-gone", Snapshot{UsedTokens: 1, WindowTokens: 2})
	Forget("s-gone")
	if _, ok := Get("s-gone"); ok {
		t.Error("a forgotten session must have no snapshot")
	}
}
