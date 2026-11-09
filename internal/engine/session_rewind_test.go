package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/KPO-Tech/seshat-runtime/internal/execution"
	"github.com/KPO-Tech/seshat-runtime/internal/permissions"
	"github.com/KPO-Tech/seshat-runtime/internal/prompt"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

func inTurn(turn string, message types.Message) types.Message {
	message.Metadata = &types.MessageMetadata{TurnID: turn}
	return message
}

func TestTheLastTurnStartsAtItsPrompt(t *testing.T) {
	messages := []types.Message{
		inTurn("t1", types.UserMessage("u1", "first")),
		inTurn("t1", types.AssistantMessage("a1", []types.ContentBlock{types.TextContent{Text: "answer"}})),
		inTurn("t2", types.UserMessage("u2", "second")),
		inTurn("t2", assistantWithCalls("c1")),
		inTurn("t2", resultFor("c1")),
		inTurn("t2", types.UserMessage("interrupted", "[The previous turn stopped because of an error]")),
	}
	start, ok := lastTurnStart(messages)
	if !ok || start != 2 {
		t.Fatalf("the last turn starts at its prompt (index 2) and takes its calls, results and error note, got %d, %v", start, ok)
	}
}

func TestAConversationOfOneTurnCanBeRewoundToNothing(t *testing.T) {
	messages := []types.Message{
		inTurn("t1", types.UserMessage("u1", "only")),
		inTurn("t1", types.AssistantMessage("a1", []types.ContentBlock{types.TextContent{Text: "answer"}})),
	}
	if start, ok := lastTurnStart(messages); !ok || start != 0 {
		t.Fatalf("got %d, %v, want 0, true", start, ok)
	}
}

func TestWithoutTurnIdsTheTurnStartsAtTheLastUserMessage(t *testing.T) {
	messages := []types.Message{
		types.UserMessage("u1", "first"),
		types.AssistantMessage("a1", []types.ContentBlock{types.TextContent{Text: "one"}}),
		types.UserMessage("u2", "second"),
		assistantWithCalls("c1"),
		resultFor("c1"),
	}
	if start, ok := lastTurnStart(messages); !ok || start != 2 {
		t.Fatalf("got %d, %v, want 2, true", start, ok)
	}
}

func TestAnEmptyConversationHasNoTurnToRewind(t *testing.T) {
	if _, ok := lastTurnStart(nil); ok {
		t.Fatal("nothing to rewind")
	}
}

func TestATurnInsideWhatACompactionPreservedIsNotRewound(t *testing.T) {
	boundary := &types.CompactionMetadata{Kind: "summary", BoundaryVersion: types.CompactionBoundaryVersionV1, PreservedMessages: 2}
	summary := types.WithCompactionMetadata(types.Message{ID: "compact_summary", Role: types.RoleSystem, Content: []types.ContentBlock{types.TextContent{Text: "summary"}}}, boundary)
	marker := types.WithCompactionMetadata(types.Message{ID: "compact_marker", Role: types.RoleSystem, Content: []types.ContentBlock{types.TextContent{Text: "marker"}}}, boundary)
	messages := []types.Message{summary, marker, inTurn("t2", assistantWithCalls("c1")), inTurn("t2", resultFor("c1"))}
	if _, ok := lastTurnStart(messages); ok {
		t.Fatal("the turn starts in the part the compaction preserved: rewinding it would leave a marker that describes messages that are gone")
	}
}

func TestRewindingTheLastTurnLetsTheNextPromptStartFromTheConversationBeforeIt(t *testing.T) {
	config := DefaultConfig()
	config.PermissionMode = types.PermissionModeBypass
	e := NewEngine(nil, execution.NewOrchestrator(), nil, prompt.NewAssembler(), permissions.NewIntegrator(permissions.NewEngine()), registry.NewRegistry(), nil, config, nil, nil)
	e.loop.config.AutoCompact = false

	var seen [][]string
	e.loop.callModelFn = func(ctx context.Context, state *MutableState, req RunRequest) (*types.APIResponse, error) {
		texts := make([]string, 0, len(state.Messages))
		for _, m := range state.Messages {
			texts = append(texts, extractEngineMessageText(m))
		}
		seen = append(seen, texts)
		return &types.APIResponse{
			Role:       types.RoleAssistant,
			Content:    []types.ContentBlock{types.TextContent{Text: fmt.Sprintf("answer %d", len(seen))}},
			StopReason: types.StopReasonEndTurn,
			Model:      req.Model,
			ID:         fmt.Sprintf("resp-%d", len(seen)),
		}, nil
	}

	session, err := e.NewSession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, prompt := range []string{"first question", "second question"} {
		if _, err := session.SubmitMessage(context.Background(), prompt); err != nil {
			t.Fatalf("submit %q: %v", prompt, err)
		}
	}

	removed, err := session.RewindLastTurn()
	if err != nil {
		t.Fatalf("rewind: %v", err)
	}
	if removed != 2 {
		t.Errorf("removed %d messages, want the second question and its answer", removed)
	}
	if got := len(session.GetMessages()); got != 2 {
		t.Fatalf("%d messages remain, want the first question and its answer", got)
	}

	if _, err := session.SubmitMessage(context.Background(), "second question, again"); err != nil {
		t.Fatalf("submit again: %v", err)
	}
	last := seen[len(seen)-1]
	for _, text := range last {
		if strings.Contains(text, "answer 2") || text == "second question" {
			t.Errorf("the model still sees the attempt that was taken back: %v", last)
		}
	}
	if len(last) != 3 {
		t.Errorf("the request holds %v, want the first exchange and the new prompt", last)
	}
}
