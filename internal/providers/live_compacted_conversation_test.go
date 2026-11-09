//go:build live

// Live check of the conversation a compaction leaves, against z.ai or Mistral:
//
//	[SESHAT_LIVE_PROVIDER=mistral] ZHIPUAI_API_KEY=... go test -tags live ./internal/providers/ -run TestLiveCompacted -v
//
// It sends the shape that z.ai refused ("The messages parameter is illegal": system summary, system marker, the agent's call,
// its result, no user message) once as it was written before, and once through the client.
package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	toolschema "github.com/KPO-Tech/seshat-runtime/internal/tools/schema"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

func compactedConversation() []types.Message {
	return []types.Message{
		textMessage("compact_summary", types.RoleSystem, "The conversation was compacted. The user asked for a one-line file named notes.txt to be written with the word hello."),
		textMessage("compact_marker", types.RoleSystem, "[Context compacted for token pressure]"),
		{ID: "a1", Role: types.RoleAssistant, Content: []types.ContentBlock{types.ToolUseContent{ID: "call_1", Name: "write_file", Input: map[string]any{"path": "notes.txt", "content": "hello"}}}},
		{ID: "r1", Role: types.RoleUser, Content: []types.ContentBlock{types.ToolResultContent{ToolUseID: "call_1", Content: "wrote notes.txt"}}},
	}
}

// liveTarget is the provider the live checks run against: z.ai by default, Mistral with SESHAT_LIVE_PROVIDER=mistral.
func liveTarget(t *testing.T) (provider types.APIProvider, key, baseURL, model string) {
	t.Helper()
	provider, keyName, baseURL, model := types.APIProviderZAi, "ZHIPUAI_API_KEY", "https://api.z.ai/api/paas/v4", "glm-4.5"
	if os.Getenv("SESHAT_LIVE_PROVIDER") == "mistral" {
		provider, keyName, baseURL, model = types.APIProviderMistral, "MISTRAL_API_KEY", "https://api.mistral.ai/v1", "ministral-3b-latest"
	}
	if m := os.Getenv("SESHAT_LIVE_MODEL"); m != "" {
		model = m
	}
	key = os.Getenv(keyName)
	if key == "" {
		t.Skip(keyName + " is not set")
	}
	return provider, key, baseURL, model
}

func liveWriteTool() []types.APIToolDefinition {
	return []types.APIToolDefinition{{
		Name: "write_file", Description: "Write a file",
		InputSchema: toolschema.JSONSchema{Type: "object", Properties: map[string]toolschema.JSONSchema{"path": {Type: "string"}, "content": {Type: "string"}}},
	}}
}

func TestLiveCompactedConversationOldShapeIsRefused(t *testing.T) {
	_, key, baseURL, model := liveTarget(t)
	// The messages as they were sent before the fix: no conversion of the leading system messages.
	messages := []map[string]any{}
	for _, m := range compactedConversation() {
		messages = append(messages, openAIMessageParts(m)...)
	}
	body, _ := json.Marshal(map[string]any{
		"model": model, "messages": messages, "max_tokens": 64,
		"tools": openAITools(liveWriteTool()),
	})
	req, _ := http.NewRequest(http.MethodPost, baseURL+"/chat/completions", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	t.Logf("old shape: HTTP %d: %.300s", resp.StatusCode, raw)
}

func TestLiveCompactedConversationIsAcceptedThroughTheClient(t *testing.T) {
	provider, key, baseURL, model := liveTarget(t)
	client := NewClientWithConfig(key, &Config{Provider: provider, BaseURL: baseURL, APIKey: key})
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	resp, err := client.CreateMessage(ctx, types.APIRequest{
		Model:        types.ModelIdentifier{Provider: provider, Model: model},
		SystemPrompt: "You are a helpful agent. Answer in one short sentence.",
		Messages:     compactedConversation(),
		Tools:        liveWriteTool(),
		MaxTokens:    128,
	})
	if err != nil {
		t.Fatalf("the provider refused the compacted conversation: %v", err)
	}
	t.Logf("accepted: stop=%s content blocks=%d", resp.StopReason, len(resp.Content))
}
