package memory

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/KPO-Tech/seshat-runtime/internal/providers"
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

func TestKeptUserMessagesAreClippedAndStopAtTheirBudget(t *testing.T) {
	e := NewEngine(nil, DefaultConfig())

	// A pasted blob is carried over clipped, not whole: otherwise compaction would copy it back.
	blob := strings.Repeat("pasted log line\n", 5_000)
	text := e.summaryText([]types.Message{makeTextMsg("m0", types.RoleUser, blob), makeTextMsg("m1", types.RoleUser, "the newest request")}, "s")
	if !strings.Contains(text, "the newest request") {
		t.Error("the newest user message is kept")
	}
	if len(text) > keptUserMessageChars*2+len(handoffPrefix)+500 {
		t.Errorf("a pasted blob was carried over almost whole: summary text is %d bytes", len(text))
	}

	// Many messages: the newest are kept until the token budget is spent, the oldest are dropped.
	var many []types.Message
	for i := 0; i < 400; i++ {
		many = append(many, makeTextMsg(fmt.Sprintf("u%d", i), types.RoleUser, fmt.Sprintf("request number %03d %s", i, strings.Repeat("filler ", 300))))
	}
	text = e.summaryText(many, "s")
	if !strings.Contains(text, "request number 399") || strings.Contains(text, "request number 000") {
		t.Error("the newest requests are kept and the oldest dropped once the budget is spent")
	}
}

func TestManualCompactionSummarizesEvenWhenTheConversationIsSmall(t *testing.T) {
	var gotModel string
	server := fakeChatCompletionServer(t, &gotModel, "the summary of the old part")
	client := providers.NewClientWithConfig("k", &providers.Config{Provider: types.APIProviderOpenAI, BaseURL: server.URL})
	client.SetHTTPClient(server.Client())
	e := NewEngine(client, DefaultConfig())
	model := types.ModelIdentifier{Provider: types.APIProviderOpenAI, Model: "gpt-4o"}

	var messages []types.Message
	for i := 0; i < 10; i++ {
		role := types.RoleUser
		if i%2 == 1 {
			role = types.RoleAssistant
		}
		messages = append(messages, makeTextMsg(fmt.Sprintf("m%d", i), role, strings.Repeat("a fact worth a few tokens. ", 20)))
	}

	// Automatic rules on a small conversation: trimming is enough, nothing is summarized or dropped.
	auto, err := e.Compact(context.Background(), "", messages, model, "s", "t", nil)
	if err != nil {
		t.Fatal(err)
	}
	if auto.UsedSummaryCompact || len(auto.Messages) != len(messages) {
		t.Fatalf("the automatic path must leave a small conversation alone: %+v", auto)
	}

	manual, err := e.Compact(context.Background(), "", messages, model, "s", "t", &TrackingState{Manual: true})
	if err != nil {
		t.Fatal(err)
	}
	if !manual.UsedSummaryCompact || manual.Summary == "" {
		t.Fatal("a manual compaction must summarize whatever the size")
	}
	if len(manual.Messages) >= len(messages) || manual.PostCompactTokens >= manual.PreCompactTokens {
		t.Errorf("a manual compaction must shrink the conversation: %d -> %d messages, %d -> %d tokens",
			len(messages), len(manual.Messages), manual.PreCompactTokens, manual.PostCompactTokens)
	}
}

func TestPreservedTailStaysWithinTheTargetAndSkipsAGiantMessage(t *testing.T) {
	e := NewEngine(nil, DefaultConfig())
	messages := []types.Message{
		makeTextMsg("m0", types.RoleUser, "start"),
		makeTextMsg("m1", types.RoleUser, strings.Repeat("pasted log line ", 4_000)), // giant
		makeTextMsg("m2", types.RoleAssistant, "short answer"),
		makeTextMsg("m3", types.RoleUser, "short follow-up"),
	}
	// A target that fits the two short messages but not the giant one.
	start := e.findPreservedStart(messages, 200)
	if start != 2 {
		t.Errorf("preserved start = %d, want 2: the tail must stay within the target instead of swallowing the giant message", start)
	}
	// Whatever the target, the last message is kept.
	if got := e.findPreservedStart(messages, 1); got != 3 {
		t.Errorf("with a tiny target the last message is still kept, got start %d", got)
	}
}
