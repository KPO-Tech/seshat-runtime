package engine

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/KPO-Tech/seshat-runtime/internal/execution"
	"github.com/KPO-Tech/seshat-runtime/internal/permissions"
	"github.com/KPO-Tech/seshat-runtime/internal/prompt"
	"github.com/KPO-Tech/seshat-runtime/internal/providers"
	compact "github.com/KPO-Tech/seshat-runtime/internal/runtime/memory"
	"github.com/KPO-Tech/seshat-runtime/internal/runtime/state"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// A real compaction, summarized by a fake model, on a real store: the full history must outlive it.
func TestCompactionKeepsTheFullHistoryReadable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"r","choices":[{"message":{"role":"assistant","content":"1. Goal: test. 2. none"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	client := providers.NewClientWithConfig("k", &providers.Config{Provider: types.APIProviderOpenAI, BaseURL: server.URL})
	client.SetHTTPClient(server.Client())

	store, err := state.NewStoreWithBackend(state.NewMemoryBackend())
	if err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.Model = types.ModelIdentifier{Provider: types.APIProviderOpenAI, Model: "gpt-4o"}
	engine := NewEngine(client, execution.NewOrchestrator(), compact.NewEngine(client, nil), prompt.NewAssembler(),
		permissions.NewIntegrator(permissions.NewEngine()), registry.NewRegistry(), store, cfg, nil, nil)

	var msgs []types.Message
	for i := 0; i < 12; i++ {
		role := types.RoleUser
		if i%2 == 1 {
			role = types.RoleAssistant
		}
		msgs = append(msgs, types.Message{ID: types.MessageID(fmt.Sprintf("m%02d", i)), Role: role,
			Content: []types.ContentBlock{types.TextContent{Text: fmt.Sprintf("message %02d %s", i, strings.Repeat("words ", 80))}}})
	}
	s, err := engine.NewSessionFromState(context.Background(), "s-history", nil, msgs)
	if err != nil {
		t.Fatalf("session: %v", err)
	}

	before, err := s.History()
	if err != nil || len(before.Archived) != 0 || len(before.Current) != 12 {
		t.Fatalf("before compaction: archived %d, current %d, err %v", len(before.Archived), len(before.Current), err)
	}

	if _, err := s.Compact(context.Background()); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	after, err := s.History()
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Current) >= 12 {
		t.Fatalf("the model's view must shrink, still %d messages", len(after.Current))
	}
	// Every message of the original conversation is still readable: archived or kept.
	seen := map[types.MessageID]bool{}
	for _, m := range after.Archived {
		seen[m.ID] = true
	}
	for _, m := range after.Current {
		seen[m.ID] = true
	}
	for _, m := range msgs {
		if !seen[m.ID] {
			t.Errorf("message %s was lost by the compaction", m.ID)
		}
	}
	if len(after.Archived) == 0 {
		t.Error("the messages the compaction dropped must be archived")
	}
	// The model's view is what GetMessages says, and the archive is not in it.
	view := map[types.MessageID]bool{}
	for _, m := range s.GetMessages() {
		view[m.ID] = true
	}
	for _, m := range after.Archived {
		if view[m.ID] {
			t.Errorf("archived message %s is still in the model's view", m.ID)
		}
	}
}

func TestHistoryWithoutAStoreIsTheCurrentMessages(t *testing.T) {
	s := sessionWithMessages(t, &manualStrategy{}, 6)
	h, err := s.History()
	if err != nil || len(h.Archived) != 0 || len(h.Current) != 6 {
		t.Errorf("got archived %d, current %d, err %v", len(h.Archived), len(h.Current), err)
	}
}
