//go:build live

// Live test of automatic compaction at the size that triggers it, on a real agent conversation.
//
// The conversation is made of real SWE-agent trajectories (Hugging Face dataset nebius/SWE-agent-trajectories),
// converted to a JSON list of lists of {role, text} and given in SESHAT_LIVE_CONV. It is grown to just past the size
// at which the runtime compacts by itself (the auto-compact threshold of the model), twelve planted facts are spread
// through it in assistant messages, and AutoCompact is run for real. The test prints what happened and which facts
// the conversation still holds afterwards.
//
//	SESHAT_LIVE_CONV=conv.json SESHAT_LIVE_PROVIDER=zai SESHAT_LIVE_MODEL=glm-4.5 ZHIPUAI_API_KEY=... \
//	  go test -tags live ./internal/runtime/memory/ -run TestLiveAutoCompactionAtTheThreshold -v -timeout 30m
package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

type convMsg struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

func loadConversation(t *testing.T) []types.Message {
	t.Helper()
	path := os.Getenv("SESHAT_LIVE_CONV")
	if path == "" {
		t.Skip("SESHAT_LIVE_CONV is not set")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var tasks [][]convMsg
	if err := json.Unmarshal(raw, &tasks); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	var out []types.Message
	for _, task := range tasks {
		for _, m := range task {
			role := types.RoleUser
			if m.Role == "assistant" {
				role = types.RoleAssistant
			}
			out = append(out, liveTextMsg(fmt.Sprintf("c%04d", len(out)), role, m.Text))
		}
	}
	return out
}

// needles are planted in assistant messages, so that only the summary can keep them (the user's own messages are
// carried over word for word).
var needles = []fact{
	{"ticket OPS-4417 for the credential rotation", []string{"ops-4417"}},
	{"flaky test test_cache_eviction", []string{"test_cache_eviction"}},
	{"staging proxy listens on port 8443", []string{"8443"}},
	{"last known good commit 9f3c2ab", []string{"9f3c2ab"}},
	{"env var SEARCH_TIMEOUT_MS is 2500", []string{"search_timeout_ms"}},
	{"Priya Nair owns the billing queue", []string{"priya nair"}},
	{"decision: Postgres 15, not 16", []string{"postgres 15"}},
	{"change freeze on 3 December", []string{"3 december"}},
	{"duplicate bug BUG-20931", []string{"bug-20931"}},
	{"export directory /srv/exports/nightly", []string{"/srv/exports/nightly"}},
	{"rule: no migrations on Fridays", []string{"fridays", "friday"}},
	{"cluster orion-eu-2", []string{"orion-eu-2"}},
}

var needleText = []string{
	"Heads-up for the record: the credential rotation is tracked in ticket OPS-4417.",
	"Note: the flaky test is test_cache_eviction, it fails about one run in eight.",
	"Noted: the staging proxy listens on port 8443, not 443.",
	"For the record, the last known good commit is 9f3c2ab.",
	"Reminder: the env var SEARCH_TIMEOUT_MS is set to 2500 in production.",
	"FYI Priya Nair owns the billing queue, ask her before touching it.",
	"Decision logged: we stay on Postgres 15, not 16.",
	"Calendar note: the change freeze starts on 3 December.",
	"Tracking: the duplicate-charge bug is BUG-20931.",
	"Heads-up: nightly exports land in /srv/exports/nightly.",
	"Rule of the team: no database migrations on Fridays.",
	"The affected cluster is orion-eu-2.",
}

func TestLiveAutoCompactionAtTheThreshold(t *testing.T) {
	client, model := liveClientAndModel(t)
	cfg := DefaultConfig()
	// SESHAT_LIVE_SUMMARY_MODEL picks the model that writes the summary, on the session's provider: the session runs
	// on SESHAT_LIVE_MODEL, so the two can be compared.
	if m := os.Getenv("SESHAT_LIVE_SUMMARY_MODEL"); m != "" {
		cfg.SummaryModel = types.ModelIdentifier{Provider: model.Provider, Model: m}
	}
	e := NewEngine(client, cfg)
	source := loadConversation(t)
	threshold := e.AutoCompactThresholdTokens(model)

	// Grow the conversation message by message until it is just past the threshold, leaving room for the needles.
	var messages []types.Message
	needleTokens := 12 * 40
	for _, m := range source {
		if e.CalculateUsage("", messages)+needleTokens >= threshold {
			break
		}
		messages = append(messages, m)
	}
	// Plant the needles, evenly spread, each followed by a short user acknowledgement.
	var planted []types.Message
	every := len(messages) / (len(needleText) + 1)
	next := 0
	positions := map[int]int{}
	for i, m := range messages {
		planted = append(planted, m)
		if (i+1)%every == 0 && next < len(needleText) {
			positions[next] = len(planted)
			planted = append(planted,
				liveTextMsg(fmt.Sprintf("n%da", next), types.RoleAssistant, needleText[next]),
				liveTextMsg(fmt.Sprintf("n%du", next), types.RoleUser, "ok"))
			next++
		}
	}
	messages = planted
	used := e.CalculateUsage("", messages)
	t.Logf("conversation: %d messages, %d tokens estimated; auto-compact threshold %d (%.1f%% of it)", len(messages), used, threshold, 100*float64(used)/float64(threshold))
	if used < threshold {
		t.Logf("note: %d tokens under the threshold, so AutoCompact will not trigger; the conversation source is too short", threshold-used)
	}

	start := time.Now()
	result, err := e.AutoCompact(context.Background(), "", messages, model, "s", "t", &TrackingState{})
	if err != nil {
		t.Fatalf("AutoCompact: %v", err)
	}
	t.Logf("AutoCompact: did=%v summary=%v %d -> %d tokens, %d -> %d messages, in %s",
		result.DidCompact, result.UsedSummaryCompact, result.PreCompactTokens, result.PostCompactTokens,
		len(messages), len(result.Messages), time.Since(start).Round(time.Second))

	var all strings.Builder
	for _, m := range result.Messages {
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
	if result.Summary != "" {
		t.Logf("summary written by the model (%d characters):\n%s", len(result.Summary), result.Summary)
	}

	score := 0
	for i, f := range needles {
		found := false
		for _, a := range f.any {
			found = found || strings.Contains(seen, strings.ToLower(a))
		}
		where := fmt.Sprintf("at %d%% of the conversation", 100*positions[i]/len(messages))
		if found {
			score++
			t.Logf("kept: %s (%s)", f.name, where)
		} else {
			t.Logf("LOST: %s (%s)", f.name, where)
		}
	}
	t.Logf("SCORE: %d / %d planted facts survived the automatic compaction", score, len(needles))
}
