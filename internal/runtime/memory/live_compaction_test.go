//go:build live

// Live test of the summary across three compactions, against a real model:
//
//	ZHIPUAI_API_KEY=... SESHAT_LIVE_PROVIDER=zai go test -tags live ./internal/runtime/memory/ -run TestLive -v
//
// It prints what the next model sees after the last compaction and a score: how many of the facts planted in the
// conversation are still in it. The facts are in tool results and in what the assistant says, never in the user's
// messages, because those are carried over word for word whatever the summary does.
package memory

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/KPO-Tech/seshat-runtime/internal/providers"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

func liveClientAndModel(t *testing.T) (*providers.Client, types.ModelIdentifier) {
	t.Helper()
	provider, keyName, base, model := types.APIProviderOpenRouter, "OPENROUTER_API_KEY", "https://openrouter.ai/api/v1", "nvidia/nemotron-3-super-120b-a12b:free"
	if os.Getenv("SESHAT_LIVE_PROVIDER") == "zai" {
		provider, keyName, base, model = types.APIProviderZAi, "ZHIPUAI_API_KEY", "https://api.z.ai/api/paas/v4", "glm-5.3-flash"
	}
	if m := os.Getenv("SESHAT_LIVE_MODEL"); m != "" {
		model = m
	}
	key := os.Getenv(keyName)
	if key == "" {
		t.Skip(keyName + " is not set")
	}
	return providers.NewClientWithConfig(key, &providers.Config{Provider: provider, BaseURL: base, APIKey: key}),
		types.ModelIdentifier{Provider: provider, Model: model}
}

func liveTextMsg(id string, role types.Role, text string) types.Message {
	return types.Message{ID: types.MessageID(id), Role: role, Content: []types.ContentBlock{types.TextContent{Text: text}}}
}

func liveTool(id, name string, input map[string]any, result string) []types.Message {
	return []types.Message{
		{ID: types.MessageID("a-" + id), Role: types.RoleAssistant, Content: []types.ContentBlock{types.ToolUseContent{ID: id, Name: name, Input: input}}},
		{ID: types.MessageID("r-" + id), Role: types.RoleUser, Content: []types.ContentBlock{types.ToolResultContent{ToolUseID: id, Content: result}}},
	}
}

// phase is the n-th stretch of the conversation: a tool call whose result holds facts, a finding or decision by the
// assistant, and chatter with unrelated numbers that gives the summary something to wrongly prefer.
func phase(n int) []types.Message {
	var msgs []types.Message
	switch n {
	case 1:
		msgs = append(msgs, liveTextMsg("u1", types.RoleUser, "We are debugging the worker pool of our Go service. Always answer me in French, and never use emojis."))
		msgs = append(msgs, liveTool("t1", "read_file", map[string]any{"file_path": "src/pool/worker.go"},
			"// worker.go\nconst shutdownTimeout = 37 * time.Second\n// on timeout the pool logs ERR_QUEUE_0917 and drops the batch")...)
		msgs = append(msgs, liveTextMsg("a1", types.RoleAssistant, "Finding: the root cause is a race in the worker pool shutdown, between Close and the drain loop."))
	case 2:
		msgs = append(msgs, liveTextMsg("u2", types.RoleUser, "Next."))
		msgs = append(msgs, liveTool("t2", "read_file", map[string]any{"file_path": "config/limits.yaml"},
			"max_connections: 412\nretry_attempts: 3")...)
		msgs = append(msgs, liveTextMsg("a2", types.RoleAssistant, "Decision: bump the pool size to 64 workers and raise the retry to 5 attempts."))
	case 3:
		msgs = append(msgs, liveTextMsg("u3", types.RoleUser, "Next."))
		msgs = append(msgs, liveTool("t3", "write_file", map[string]any{"file_path": "docs/runbook.md"},
			"wrote docs/runbook.md\non-call contact: Lea Martin, pager 5512")...)
		msgs = append(msgs, liveTextMsg("a3", types.RoleAssistant, "Next step: rerun the load test with 64 workers once the race is fixed."))
	}
	for k := 0; k < 4; k++ {
		msgs = append(msgs,
			liveTextMsg(fmt.Sprintf("d%du%d", n, k), types.RoleUser, "ok"),
			liveTextMsg(fmt.Sprintf("d%da%d", n, k), types.RoleAssistant,
				fmt.Sprintf("Acknowledged. For reference the build number is %d and the cache size is %d MB.", 8000+n*100+k, 100+k*7)),
		)
	}
	return msgs
}

// fact is planted in the conversation; any of the spellings counts.
type fact struct {
	name string
	any  []string
}

var plantedFacts = []fact{
	{"file: src/pool/worker.go", []string{"worker.go"}},
	{"timeout 37 s", []string{"37"}},
	{"error ERR_QUEUE_0917", []string{"err_queue_0917"}},
	{"root cause: race in the pool shutdown", []string{"race"}},
	{"file: config/limits.yaml", []string{"limits.yaml"}},
	{"max_connections 412", []string{"412"}},
	{"decision: 64 workers", []string{"64"}},
	{"decision: 5 retry attempts", []string{"5 attempts", "5 tentatives", "retry to 5", "5 retries"}},
	{"file: docs/runbook.md", []string{"runbook.md"}},
	{"on-call Lea Martin", []string{"lea martin", "léa martin"}},
	{"pager 5512", []string{"5512"}},
	{"next step: rerun the load test", []string{"load test", "test de charge"}},
	{"preference: French", []string{"french", "français", "francais"}},
	{"preference: no emojis", []string{"emoji"}},
}

func TestLiveSummaryKeepsFactsAcrossThreeCompactions(t *testing.T) {
	client, model := liveClientAndModel(t)
	e := NewEngine(client, DefaultConfig())
	ctx := context.Background()

	var last CompactionResult
	for n := 1; n <= 3; n++ {
		conversation := phase(n)
		if n > 1 {
			conversation = append(append([]types.Message(nil), last.Messages...), phase(n)...)
		}
		var err error
		last, err = e.Compact(ctx, "", conversation, model, "s", types.TurnID(fmt.Sprintf("t%d", n)), &TrackingState{Manual: true})
		if err != nil {
			t.Fatalf("compaction %d: %v", n, err)
		}
		t.Logf("compaction %d: %d -> %d tokens, %d messages left", n, last.PreCompactTokens, last.PostCompactTokens, len(last.Messages))
	}

	// What the next model sees of the conversation: every message after the last compaction.
	var all strings.Builder
	for _, m := range last.Messages {
		for _, b := range m.Content {
			switch c := b.(type) {
			case types.TextContent:
				all.WriteString(c.Text + "\n")
			case types.ToolResultContent:
				all.WriteString(c.Content + "\n")
			}
		}
	}
	seen := strings.ToLower(all.String())
	t.Logf("what the next model sees:\n%s", all.String())

	score := 0
	for _, f := range plantedFacts {
		found := false
		for _, a := range f.any {
			found = found || strings.Contains(seen, strings.ToLower(a))
		}
		if found {
			score++
		} else {
			t.Logf("LOST: %s", f.name)
		}
	}
	t.Logf("SCORE: %d / %d facts survived three compactions", score, len(plantedFacts))
}
