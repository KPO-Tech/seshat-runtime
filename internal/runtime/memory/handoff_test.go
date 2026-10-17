package memory

import (
	"strings"
	"testing"

	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

func toolRoundTrip() []types.Message {
	return []types.Message{
		{ID: "a1", Role: types.RoleAssistant, Content: []types.ContentBlock{
			types.ToolUseContent{ID: "tu1", Name: "read_file", Input: map[string]any{"path": "/repo/main.go"}},
		}},
		{ID: "r1", Role: types.RoleUser, Content: []types.ContentBlock{
			types.ToolResultContent{ToolUseID: "tu1", Content: "package main\n// the entry point of the service"},
		}},
	}
}

func TestSummaryPromptShowsWhatTheToolsAskedAndReturned(t *testing.T) {
	e := NewEngine(nil, DefaultConfig())
	prompt := e.buildSummaryPrompt(toolRoundTrip())
	for _, want := range []string{"read_file", "/repo/main.go", "the entry point of the service", "Facts to keep exactly"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the summarizer must be shown %q, got:\n%s", want, prompt)
		}
	}
}

func TestSummaryPromptClipsHugeToolResults(t *testing.T) {
	e := NewEngine(nil, DefaultConfig())
	msgs := []types.Message{{ID: "r1", Role: types.RoleUser, Content: []types.ContentBlock{
		types.ToolResultContent{ToolUseID: "x", Content: strings.Repeat("z", 50_000)},
	}}}
	if got := e.buildSummaryPrompt(msgs); len(got) > len(summaryInstructions)+summaryToolResultChars+200 {
		t.Errorf("a 50,000 byte tool result must be clipped, prompt is %d bytes", len(got))
	}
}

func TestCompactedSummaryCarriesTheHandoffAndTheUsersWords(t *testing.T) {
	e := NewEngine(nil, DefaultConfig())
	messages := []types.Message{
		makeTextMsg("m0", types.RoleUser, "please migrate the billing module"),
		makeTextMsg("m1", types.RoleAssistant, "on it"),
		makeTextMsg("m2", types.RoleUser, "and keep the old API working"),
		makeTextMsg("m3", types.RoleAssistant, "done so far: step one"),
		makeTextMsg("m4", types.RoleUser, "recent question"),
	}
	out := e.assembleCompacted(messages, 4, "the model's summary")
	text := ""
	for _, b := range out[0].Content {
		if c, ok := b.(types.TextContent); ok {
			text += c.Text
		}
	}
	for _, want := range []string{"Another model worked on this before you", "the model's summary",
		"please migrate the billing module", "and keep the old API working"} {
		if !strings.Contains(text, want) {
			t.Errorf("summary message lacks %q:\n%s", want, text)
		}
	}
	if strings.Index(text, "please migrate") > strings.Index(text, "and keep the old") {
		t.Error("the user's messages must keep their order, oldest first")
	}
	if strings.Contains(text, "on it") || strings.Contains(text, "recent question") {
		t.Error("only the dropped user messages belong in the summary, not assistant text or the preserved tail")
	}
	if len(out) != 3 || out[1].ID != compactMarkerMessageID {
		t.Errorf("want summary, marker and the one preserved message, got %d messages", len(out))
	}
}

func TestKeptUserMessagesStopAtTheirBudget(t *testing.T) {
	e := NewEngine(nil, DefaultConfig())
	huge := strings.Repeat("old words ", keptUserMessageTokens) // far over the budget
	dropped := []types.Message{
		makeTextMsg("m0", types.RoleUser, huge),
		makeTextMsg("m1", types.RoleUser, "the newest request"),
	}
	text := e.summaryText(dropped, "s")
	if !strings.Contains(text, "the newest request") {
		t.Error("the newest user message is kept first")
	}
	if strings.Contains(text, "old words") {
		t.Error("a message that does not fit the budget is dropped whole")
	}
}
