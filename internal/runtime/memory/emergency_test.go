package memory

import (
	"fmt"
	"strings"
	"testing"

	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

var emergencyModel = types.ModelIdentifier{Provider: types.APIProviderZAi, Model: "glm-4.5"} // 128k window, target 63,000

func bigConversation(n, wordsEach int) []types.Message {
	var out []types.Message
	for i := 0; i < n; i++ {
		role := types.RoleUser
		if i%2 == 1 {
			role = types.RoleAssistant
		}
		out = append(out, makeTextMsg(fmt.Sprintf("m%03d", i), role, fmt.Sprintf("message %03d %s", i, strings.Repeat("word ", wordsEach))))
	}
	return out
}

func emToolCall(id, name string, input map[string]any) types.Message {
	return types.Message{ID: types.MessageID("a-" + id), Role: types.RoleAssistant,
		Content: []types.ContentBlock{types.ToolUseContent{ID: id, Name: name, Input: input}}}
}

func emToolResult(id, content string, isError bool) types.Message {
	return types.Message{ID: types.MessageID("r-" + id), Role: types.RoleUser,
		Content: []types.ContentBlock{types.ToolResultContent{ToolUseID: id, Content: content, IsError: isError}}}
}

func textOf(m types.Message) string {
	var b strings.Builder
	for _, c := range m.Content {
		if t, ok := c.(types.TextContent); ok {
			b.WriteString(t.Text)
		}
	}
	return b.String()
}

func TestEmergencyTrimDropsTheOldestMessagesBehindAMarker(t *testing.T) {
	e := NewEngine(nil, DefaultConfig())
	msgs := bigConversation(200, 1_000) // about 250,000 tokens: far over the window
	result, ok := e.EmergencyTrim("", msgs, emergencyModel, nil)
	if !ok || !result.DidCompact {
		t.Fatal("a conversation over the window must be trimmed")
	}
	if result.PostCompactTokens > e.TargetTokens(emergencyModel)+5_000 || result.PostCompactTokens >= result.PreCompactTokens {
		t.Errorf("trimmed to %d tokens from %d, want about the %d target", result.PostCompactTokens, result.PreCompactTokens, e.TargetTokens(emergencyModel))
	}
	first := result.Messages[0]
	if first.ID != compactMarkerMessageID || first.Metadata == nil || first.Metadata.Compaction == nil || first.Metadata.Compaction.Kind != "emergency_trim" {
		t.Fatalf("the trimmed conversation must start with a marker carrying the compaction metadata, got %+v", first)
	}
	if !strings.Contains(textOf(first), "could not be summarized") {
		t.Error("the marker must say why the earlier part is gone")
	}
	if last := result.Messages[len(result.Messages)-1]; last.ID != "m199" {
		t.Errorf("the newest message must be kept, last is %s", last.ID)
	}
	// What is kept is the newest messages, contiguous.
	kept := len(result.Messages) - 1
	for i := 1; i < len(result.Messages); i++ {
		want := fmt.Sprintf("m%03d", 200-kept+i-1)
		if string(result.Messages[i].ID) != want {
			t.Fatalf("message %d is %s, want %s", i, result.Messages[i].ID, want)
		}
	}
}

func TestEmergencyTrimFirstTrimsToolResultsAndKeepsEveryMessageIfThatIsEnough(t *testing.T) {
	e := NewEngine(nil, DefaultConfig())
	var msgs []types.Message
	for i := 0; i < 12; i++ {
		msgs = append(msgs, emToolCall(fmt.Sprintf("t%d", i), "read_file", map[string]any{"file_path": "/f.go"}))
		msgs = append(msgs, emToolResult(fmt.Sprintf("t%d", i), strings.Repeat("x", 200_000), false)) // 50,000 tokens each
	}
	result, ok := e.EmergencyTrim("", msgs, emergencyModel, nil)
	if !ok {
		t.Fatal("trimming the tool results must be enough here")
	}
	if len(result.Messages) != len(msgs) {
		t.Errorf("no message needs to go when trimming the tool results is enough: %d of %d left", len(result.Messages), len(msgs))
	}
	if result.PostCompactTokens > e.TargetTokens(emergencyModel) {
		t.Errorf("%d tokens left, over the target", result.PostCompactTokens)
	}
}

func TestEmergencyTrimReportsFalseWhenThereIsNothingToDo(t *testing.T) {
	e := NewEngine(nil, DefaultConfig())
	if _, ok := e.EmergencyTrim("", bigConversation(4, 10), emergencyModel, nil); ok {
		t.Error("a small conversation has nothing to trim")
	}
	// One message larger than the window: nothing older to drop.
	if _, ok := e.EmergencyTrim("", bigConversation(1, 300_000), emergencyModel, nil); ok {
		t.Error("a single message cannot be dropped in favour of itself")
	}
}

func TestEmergencyTrimKeepsToolCallsAndResultsTogether(t *testing.T) {
	e := NewEngine(nil, DefaultConfig())
	msgs := bigConversation(120, 1_000)
	msgs = append(msgs, emToolCall("tx", "read_file", map[string]any{"file_path": "/f.go"}), emToolResult("tx", "result", false))
	result, ok := e.EmergencyTrim("", msgs, emergencyModel, nil)
	if !ok {
		t.Fatal("expected a trim")
	}
	uses, results := map[string]bool{}, map[string]bool{}
	for _, m := range result.Messages {
		for _, b := range m.Content {
			switch c := b.(type) {
			case types.ToolUseContent:
				uses[c.ID] = true
			case types.ToolResultContent:
				results[c.ToolUseID] = true
			}
		}
	}
	for id := range results {
		if !uses[id] {
			t.Errorf("tool result %s is kept without the call that produced it", id)
		}
	}
}
