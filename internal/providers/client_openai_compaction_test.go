package providers

import (
	"testing"

	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

func textMessage(id string, role types.Role, text string) types.Message {
	return types.Message{ID: types.MessageID(id), Role: role, Content: []types.ContentBlock{types.TextContent{Text: text}}}
}

func TestACompactedConversationOpensWithAUserMessage(t *testing.T) {
	call := types.Message{ID: "a1", Role: types.RoleAssistant, Content: []types.ContentBlock{types.ToolUseContent{ID: "call-1", Name: "write", Input: map[string]any{"path": "x"}}}}
	result := types.Message{ID: "r1", Role: types.RoleUser, Content: []types.ContentBlock{types.ToolResultContent{ToolUseID: "call-1", Content: "ok"}}}
	c := &Client{}

	messages := c.buildOpenAIMessages(types.APIRequest{
		SystemPrompt: "You are Seshat.",
		Messages: []types.Message{
			textMessage("compact_summary", types.RoleSystem, "Summary: the user asked for a mockup."),
			textMessage("compact_marker", types.RoleSystem, "[Context compacted]"),
			call,
			result,
		},
	})

	roles := make([]string, 0, len(messages))
	for _, m := range messages {
		roles = append(roles, m["role"].(string))
	}
	want := []string{"system", "user", "assistant", "tool"}
	if len(roles) != len(want) {
		t.Fatalf("roles = %v, want %v", roles, want)
	}
	for i := range want {
		if roles[i] != want[i] {
			t.Fatalf("roles = %v, want %v", roles, want)
		}
	}
	if got := messages[1]["content"].(string); got != "Summary: the user asked for a mockup.\n\n[Context compacted]" {
		t.Errorf("the summary must be sent as the user's turn, got %q", got)
	}
}

func TestAConversationThatAlreadyHasAUserMessageIsLeftAlone(t *testing.T) {
	c := &Client{}
	messages := c.buildOpenAIMessages(types.APIRequest{
		Messages: []types.Message{
			textMessage("compact_summary", types.RoleSystem, "Summary"),
			textMessage("u1", types.RoleUser, "continue"),
		},
	})
	if len(messages) != 2 || messages[0]["role"] != "system" || messages[1]["role"] != "user" {
		t.Fatalf("a conversation that opens with a summary then a user message must not change: %v", messages)
	}
}
