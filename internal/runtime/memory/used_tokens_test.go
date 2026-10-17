package memory

import (
	"strings"
	"testing"

	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

func usedTokensModel() types.ModelIdentifier {
	return types.ModelIdentifier{Provider: types.APIProviderAnthropic, Model: "claude-3-5-sonnet-20241022"}
}

func TestUsedTokensWithoutTrackingIsTheEstimate(t *testing.T) {
	e := NewEngine(nil, DefaultConfig())
	messages := []types.Message{types.UserMessage("m1", strings.Repeat("word ", 400))}
	if got, want := e.UsedTokens("system", messages, nil), e.CalculateUsage("system", messages); got != want {
		t.Errorf("UsedTokens = %d, want the estimate %d", got, want)
	}
}

func TestUsedTokensCountsToolDefinitions(t *testing.T) {
	e := NewEngine(nil, DefaultConfig())
	messages := []types.Message{types.UserMessage("m1", "hi")}
	base := e.UsedTokens("", messages, nil)
	if got := e.UsedTokens("", messages, &TrackingState{ExtraTokens: 16_000}); got != base+16_000 {
		t.Errorf("UsedTokens = %d, want %d: the tool definitions must be counted", got, base+16_000)
	}
}

func TestUsedTokensStartsFromTheProvidersFigure(t *testing.T) {
	e := NewEngine(nil, DefaultConfig())
	messages := []types.Message{
		types.UserMessage("m1", "first"),
		types.UserMessage("m2", "second"),
		types.UserMessage("m3", strings.Repeat("added since ", 100)),
	}
	tracking := &TrackingState{ExtraTokens: 99_999, Anchor: &UsageAnchor{Tokens: 50_000, MessageCount: 2}}
	got := e.UsedTokens("a long system prompt that the anchor already covers", messages, tracking)
	added := e.CalculateUsage("", messages[2:])
	if got != 50_000+added {
		t.Errorf("UsedTokens = %d, want 50,000 plus the %d tokens added since", got, added)
	}
}

func TestUsedTokensIgnoresAnAnchorLongerThanTheMessages(t *testing.T) {
	e := NewEngine(nil, DefaultConfig())
	messages := []types.Message{types.UserMessage("m1", "hi")}
	got := e.UsedTokens("", messages, &TrackingState{Anchor: &UsageAnchor{Tokens: 50_000, MessageCount: 9}})
	if got != e.CalculateUsage("", messages) {
		t.Errorf("a stale anchor must be ignored, got %d", got)
	}
}

func TestShouldCompactSeesTheToolDefinitions(t *testing.T) {
	e := NewEngine(nil, DefaultConfig())
	model := usedTokensModel()
	messages := []types.Message{types.UserMessage("m1", "hi")}
	if e.ShouldCompact("", messages, model, nil) {
		t.Fatal("a one-word conversation must not compact")
	}
	heavy := &TrackingState{ExtraTokens: e.AutoCompactThresholdTokens(model)}
	if !e.ShouldCompact("", messages, model, heavy) {
		t.Error("tool definitions that fill the threshold must trigger compaction")
	}
}

func TestBudgetReportsTheThresholds(t *testing.T) {
	e := NewEngine(nil, DefaultConfig())
	b := e.Budget(usedTokensModel())
	if b.WindowTokens <= 0 || b.AutoCompactAtTokens <= 0 || b.AutoCompactAtTokens >= b.BlockingLimitTokens || b.BlockingLimitTokens > b.WindowTokens {
		t.Errorf("budget out of order: %+v", b)
	}
}
