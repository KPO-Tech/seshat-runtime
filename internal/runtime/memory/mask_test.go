package memory

import (
	"fmt"
	"strings"
	"testing"

	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// bigRound is one tool call and its result of size chars.
func bigRound(i, size int) []types.Message {
	id := fmt.Sprintf("t%02d", i)
	return []types.Message{
		toolCall(id, "read_file", map[string]any{"file_path": fmt.Sprintf("/repo/f%02d.go", i)}),
		toolResult(id, fmt.Sprintf("FILE %02d START ", i)+strings.Repeat("x", size), false),
	}
}

func rounds(n, size int) []types.Message {
	msgs := []types.Message{makeTextMsg("u0", types.RoleUser, "please read all these files")}
	for i := 0; i < n; i++ {
		msgs = append(msgs, bigRound(i, size)...)
	}
	return msgs
}

func resultText(m types.Message) string {
	for _, b := range m.Content {
		if r, ok := b.(types.ToolResultContent); ok {
			return r.Content
		}
	}
	return ""
}

func TestMaskingClearsOldLargeResultsAndKeepsTheRecentOnes(t *testing.T) {
	msgs := rounds(12, 5_000)
	out, res := MaskOldObservations(msgs)
	if res.Cleared != 12-maskKeepRecentResults || res.FreedChars < 20_000 {
		t.Fatalf("result = %+v, want %d cleared", res, 12-maskKeepRecentResults)
	}
	// The first round is cleared, with the tool, the size and the start of it.
	first := resultText(out[2])
	for _, want := range []string{maskedPrefix, "read_file returned", "FILE 00 START", "Run the tool again"} {
		if !strings.Contains(first, want) {
			t.Errorf("cleared result lacks %q: %s", want, first)
		}
	}
	// The last six are untouched.
	for i := 12 - maskKeepRecentResults; i < 12; i++ {
		if got := resultText(out[2+2*i]); !strings.Contains(got, strings.Repeat("x", 5_000)) {
			t.Errorf("round %d is recent and must stay whole", i)
		}
	}
	// The input is not modified, and tool calls and the user's words are untouched.
	if strings.HasPrefix(resultText(msgs[2]), maskedPrefix) {
		t.Error("the caller's messages must not be modified")
	}
	if len(out) != len(msgs) || out[0].Role != types.RoleUser {
		t.Error("masking must not add or drop messages")
	}
	for i := range out {
		if _, ok := out[i].Content[0].(types.ToolUseContent); ok && !reflectEqualUse(out[i], msgs[i]) {
			t.Errorf("tool call %d changed", i)
		}
	}
}

func reflectEqualUse(a, b types.Message) bool {
	return a.Content[0].(types.ToolUseContent).ID == b.Content[0].(types.ToolUseContent).ID &&
		a.Content[0].(types.ToolUseContent).Name == b.Content[0].(types.ToolUseContent).Name
}

func TestMaskingWaitsForABatchWorthClearing(t *testing.T) {
	// Twelve rounds of 1,000 characters: eligible ones total under the batch size.
	msgs := rounds(12, 1_000)
	out, res := MaskOldObservations(msgs)
	if res.Cleared != 0 || resultText(out[2]) != resultText(msgs[2]) {
		t.Errorf("a small batch must not be masked: %+v", res)
	}
	// Few results: nothing is old enough.
	if _, res := MaskOldObservations(rounds(maskKeepRecentResults, 50_000)); res.Cleared != 0 {
		t.Errorf("the recent results are never cleared: %+v", res)
	}
}

func TestMaskingIsIdempotentAndSkipsSmallResults(t *testing.T) {
	msgs := rounds(14, 5_000)
	once, res1 := MaskOldObservations(msgs)
	twice, res2 := MaskOldObservations(once)
	if res1.Cleared == 0 || res2.Cleared != 0 {
		t.Errorf("second pass cleared %d, want 0 (first cleared %d)", res2.Cleared, res1.Cleared)
	}
	for i := range once {
		if resultText(once[i]) != resultText(twice[i]) {
			t.Fatalf("message %d changed on the second pass", i)
		}
	}
	// A small old result stays whole.
	mixed := append([]types.Message{makeTextMsg("u0", types.RoleUser, "go")}, bigRound(0, 100)...)
	mixed = append(mixed, rounds(12, 5_000)[1:]...)
	out, _ := MaskOldObservations(mixed)
	if strings.HasPrefix(resultText(out[2]), maskedPrefix) {
		t.Error("a result under the minimum size must stay whole")
	}
}

func TestMaskedResultFitsOnOneLineAndSurvivesOddContent(t *testing.T) {
	got := maskedResult("bash", "line one\nline two\t\ttabbed "+strings.Repeat("é", 500))
	if strings.Contains(got, "\n") || !strings.Contains(got, "line one line two tabbed") {
		t.Errorf("preview must be flattened: %q", got)
	}
	if !strings.Contains(maskedResult("", "x"), "a tool returned") {
		t.Error("an unknown tool is named generically")
	}
}

func TestMaskingKeepsErrorsMarkedAsErrors(t *testing.T) {
	msgs := rounds(12, 5_000)
	r := msgs[2].Content[0].(types.ToolResultContent)
	r.IsError = true
	msgs[2].Content[0] = r
	out, _ := MaskOldObservations(msgs)
	if !out[2].Content[0].(types.ToolResultContent).IsError {
		t.Error("a cleared error is still an error")
	}
}

func TestPreparingAMessageDoesNotTrimTheCallersCopy(t *testing.T) {
	p := NewMessagePreparer()
	big := strings.Repeat("y", 50_000)
	msg := toolResult("t1", big, false)
	_ = p.PrepareMessageForAPI(msg)
	if got := resultText(msg); got != big {
		t.Errorf("the caller's message was trimmed in place: %d characters left of %d", len(got), len(big))
	}
}
