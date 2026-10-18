package memory

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

func toolCall(id, name string, input map[string]any) types.Message {
	return types.Message{ID: types.MessageID("a-" + id), Role: types.RoleAssistant,
		Content: []types.ContentBlock{types.ToolUseContent{ID: id, Name: name, Input: input}}}
}

func toolResult(id, content string, isError bool) types.Message {
	return types.Message{ID: types.MessageID("r-" + id), Role: types.RoleUser,
		Content: []types.ContentBlock{types.ToolResultContent{ToolUseID: id, Content: content, IsError: isError}}}
}

func sorted(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func TestFileTrailListsWhatTheToolCallsTouched(t *testing.T) {
	msgs := []types.Message{
		toolCall("1", "read_file", map[string]any{"file_path": "/repo/a.go"}),
		toolResult("1", "package a", false),
		toolCall("2", "edit_file", map[string]any{"file_path": "/repo/b.go"}),
		toolResult("2", "ok", false),
		toolCall("3", "write_file", map[string]any{"file_path": "/repo/c.go"}),
		toolResult("3", "permission denied", true), // failed: the file was not written
		toolCall("4", "create_directory", map[string]any{"path": "/repo/out"}),
		toolResult("4", "ok", false),
		toolCall("5", "bash", map[string]any{"command": "ls /repo/secret"}), // not a file tool
		toolResult("5", "x", false),
	}
	got := collectFileTrail(msgs)
	if !reflect.DeepEqual(sorted(got.Modified), []string{"/repo/b.go", "/repo/out"}) {
		t.Errorf("modified = %v, want b.go and out (c.go failed)", got.Modified)
	}
	if !reflect.DeepEqual(got.Read, []string{"/repo/a.go"}) {
		t.Errorf("read = %v, want a.go", got.Read)
	}
}

func TestFileTrailReadsPatchesAndKeepsTheMostRecentTouchLast(t *testing.T) {
	msgs := []types.Message{
		toolCall("1", "apply_patch", map[string]any{"patch": "*** Begin Patch\n*** Update File: src/x.go\n@@\n-a\n+b\n*** Add File: src/y.go\n+z\n*** End Patch"}),
		toolCall("2", "apply_patch", map[string]any{"patch": "--- a/src/old.go\n+++ b/src/new.go\n@@\n-a\n+b"}),
		toolCall("3", "edit_file", map[string]any{"file_path": "src/x.go"}),
	}
	got := collectFileTrail(msgs)
	want := []string{"src/y.go", "src/new.go", "src/x.go"}
	if !reflect.DeepEqual(got.Modified, want) {
		t.Errorf("modified = %v, want %v (x.go touched again, so it is last)", got.Modified, want)
	}
}

func TestFileTrailMergeLetsAModificationSupersedeARead(t *testing.T) {
	older := fileTrail{Read: []string{"a.go", "b.go"}}
	later := fileTrail{Modified: []string{"a.go"}}
	got := older.merge(later)
	if !reflect.DeepEqual(got.Modified, []string{"a.go"}) || !reflect.DeepEqual(got.Read, []string{"b.go"}) {
		t.Errorf("merged = %+v, want a.go modified and only b.go read", got)
	}
}

func TestFileTrailIsCapped(t *testing.T) {
	var list []string
	for i := 0; i < maxTrailPaths+15; i++ {
		list = appendUnique(list, fmt.Sprintf("f%03d.go", i))
	}
	if len(list) != maxTrailPaths || list[len(list)-1] != fmt.Sprintf("f%03d.go", maxTrailPaths+14) || list[0] != "f015.go" {
		t.Errorf("trail has %d paths from %s to %s, want the newest %d", len(list), list[0], list[len(list)-1], maxTrailPaths)
	}
}

func TestFileTrailRoundTrips(t *testing.T) {
	trail := fileTrail{Modified: []string{"a.go", "dir/b.go"}, Read: []string{"c.go"}}
	if got := parseFileTrail(trail.render()); !reflect.DeepEqual(got, trail) {
		t.Errorf("round trip = %+v, want %+v", got, trail)
	}
	if (fileTrail{}).render() != "" {
		t.Error("an empty trail renders nothing")
	}
}

func summaryMessage(text string) types.Message {
	m := types.SystemMessage(compactSummaryMessageID, text)
	return m
}

func TestSummaryMessageCanBeReadBack(t *testing.T) {
	e := NewEngine(nil, DefaultConfig())
	dropped := []types.Message{
		makeTextMsg("u1", types.RoleUser, "please migrate the billing module"),
		toolCall("1", "edit_file", map[string]any{"file_path": "billing/tax.go"}),
		toolResult("1", "ok", false),
		makeTextMsg("u2", types.RoleUser, "and keep the old API working"),
	}
	text := e.summaryText(dropped, "1. Goal: migrate billing.\n2. Decisions: none")
	prev, ok := previousSummaryOf(summaryMessage(text))
	if !ok {
		t.Fatal("the summary message must be readable")
	}
	if !strings.Contains(prev.body, "1. Goal: migrate billing.") || strings.Contains(prev.body, "Files touched") || strings.Contains(prev.body, "word for word") {
		t.Errorf("the body must be the model's summary alone, got %q", prev.body)
	}
	if !reflect.DeepEqual(prev.files.Modified, []string{"billing/tax.go"}) {
		t.Errorf("files = %+v", prev.files)
	}
	if !reflect.DeepEqual(prev.users, []string{"please migrate the billing module", "and keep the old API working"}) {
		t.Errorf("users = %v", prev.users)
	}
	if _, ok := previousSummaryOf(makeTextMsg("x", types.RoleUser, "hello")); ok {
		t.Error("an ordinary message is not a summary")
	}
}

func TestSecondCompactionCarriesEverythingOver(t *testing.T) {
	e := NewEngine(nil, DefaultConfig())

	first := e.summaryText([]types.Message{
		makeTextMsg("u1", types.RoleUser, "first request: keep BILLING_V2 behind a flag"),
		toolCall("1", "edit_file", map[string]any{"file_path": "billing/tax.go"}),
		toolResult("1", "ok", false),
	}, "1. Goal: billing migration.")

	// The second compaction drops the first summary together with newer messages.
	second := e.summaryText([]types.Message{
		summaryMessage(first),
		makeTextMsg("u2", types.RoleUser, "second request: never touch the schema"),
		toolCall("2", "edit_file", map[string]any{"file_path": "billing/export.go"}),
		toolResult("2", "ok", false),
	}, "1. Goal: billing migration, now with export.")

	for _, want := range []string{
		"billing migration, now with export",  // the new summary
		"billing/tax.go", "billing/export.go", // both files, one from each compaction
		"first request: keep BILLING_V2", "second request: never touch the schema", // both user messages
	} {
		if !strings.Contains(second, want) {
			t.Errorf("the second summary lost %q:\n%s", want, second)
		}
	}
	if strings.Index(second, "first request") > strings.Index(second, "second request") {
		t.Error("the user's messages keep their order across compactions")
	}
	if strings.Count(second, "Another model worked on this before you") != 1 {
		t.Error("the hand-off prefix must not be nested")
	}
}

func TestSummaryPromptUpdatesAnEarlierSummaryInsteadOfRestarting(t *testing.T) {
	e := NewEngine(nil, DefaultConfig())
	prev := e.summaryText(nil, "1. Goal: ship the billing migration.\n5. Facts: ERR_TAX_0042")
	prompt := e.buildSummaryPrompt([]types.Message{
		summaryMessage(prev),
		makeTextMsg("u1", types.RoleUser, "new work since then"),
	})
	for _, want := range []string{"You are updating the handoff", "Previous summary:", "ERR_TAX_0042", "What happened since:", "new work since then", "Never drop an exact fact"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("update prompt lacks %q", want)
		}
	}
	if strings.Contains(prompt, "[system]") {
		t.Error("the earlier summary is the base, not a message of the conversation")
	}

	fresh := e.buildSummaryPrompt([]types.Message{makeTextMsg("u1", types.RoleUser, "hello")})
	if !strings.Contains(fresh, "You are writing a handoff") || strings.Contains(fresh, "Previous summary:") {
		t.Error("without an earlier summary the prompt asks for a first one")
	}
	if !strings.Contains(fresh, `written "none"`) {
		t.Error("every section must be required, written none when empty")
	}
}
