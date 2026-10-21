package memory

import (
	"fmt"
	"testing"

	"github.com/KPO-Tech/seshat-runtime/internal/model"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// A small local model (8k, 16k, 32k) must be compacted in time: the thresholds have to sit inside its real window, with room
// for the next tool result between the automatic threshold and the blocking limit, and without compacting at every turn.
func TestThresholdsFitSmallWindows(t *testing.T) {
	for _, window := range []int{8_192, 16_384, 32_768, 65_536, 131_072, 200_000} {
		t.Run(fmt.Sprint(window), func(t *testing.T) {
			id := fmt.Sprintf("small-window-%d", window)
			model.Global.Register(model.Metadata{ID: id, Provider: "test-small", ContextWindow: model.ContextWindow{MaxTokens: window}})
			m := types.ModelIdentifier{Provider: "test-small", Model: id}
			e := NewEngine(nil, DefaultConfig())

			budget := e.Budget(m)
			if budget.WindowTokens != window {
				t.Fatalf("window = %d, want %d", budget.WindowTokens, window)
			}
			if budget.BlockingLimitTokens >= window {
				t.Errorf("blocking limit %d is not inside the window %d: the provider would refuse the request first", budget.BlockingLimitTokens, window)
			}
			if budget.AutoCompactAtTokens >= budget.BlockingLimitTokens {
				t.Errorf("auto-compact at %d is not before the blocking limit %d", budget.AutoCompactAtTokens, budget.BlockingLimitTokens)
			}
			if budget.AutoCompactAtTokens < window*40/100 {
				t.Errorf("auto-compact at %d is under 40%% of the window %d: it would compact at nearly every turn", budget.AutoCompactAtTokens, window)
			}
			if room := budget.BlockingLimitTokens - budget.AutoCompactAtTokens; room < window/20 {
				t.Errorf("only %d tokens between the threshold and the blocking limit (window %d)", room, window)
			}
		})
	}
}
