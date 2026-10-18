package memory

import (
	"strings"
	"testing"

	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

func TestPreparingAMessageDoesNotTrimTheCallersCopy(t *testing.T) {
	p := NewMessagePreparer()
	big := strings.Repeat("y", 50_000)
	msg := types.Message{ID: "r1", Role: types.RoleUser, Content: []types.ContentBlock{
		types.ToolResultContent{ToolUseID: "t1", Content: big},
	}}

	prepared := p.PrepareMessageForAPI(msg)

	if got := msg.Content[0].(types.ToolResultContent).Content; got != big {
		t.Errorf("the caller's message was trimmed in place: %d characters left of %d", len(got), len(big))
	}
	if got := prepared.Content[0].(types.ToolResultContent).Content; len(got) >= len(big) {
		t.Errorf("the prepared message must be trimmed, it holds %d characters", len(got))
	}
}
