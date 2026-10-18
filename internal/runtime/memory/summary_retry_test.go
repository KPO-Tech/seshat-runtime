package memory

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/KPO-Tech/seshat-runtime/internal/providers"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// emptyThenTextServer answers the first request with no text (a reasoning model that spent its whole budget thinking,
// ending on the length limit) and the next ones with a summary. It records the token budget of each request.
func emptyThenTextServer(t *testing.T, emptyReplies int) (*httptest.Server, *[]int) {
	t.Helper()
	var mu sync.Mutex
	budgets := &[]int{}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			MaxTokens           int `json:"max_tokens"`
			MaxCompletionTokens int `json:"max_completion_tokens"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		calls++
		n := calls
		*budgets = append(*budgets, max(body.MaxTokens, body.MaxCompletionTokens))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if n <= emptyReplies {
			_, _ = w.Write([]byte(`{"id":"r","choices":[{"message":{"role":"assistant","content":""},"finish_reason":"length"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"r","choices":[{"message":{"role":"assistant","content":"1. goal\n2. decisions\n3. done\n4. state\n5. facts\n6. preferences"},"finish_reason":"stop"}]}`))
	}))
	t.Cleanup(server.Close)
	return server, budgets
}

func retryEngine(server *httptest.Server) *Engine {
	client := providers.NewClientWithConfig("k", &providers.Config{Provider: types.APIProviderOpenAI, BaseURL: server.URL})
	client.SetHTTPClient(server.Client())
	return NewEngine(client, DefaultConfig())
}

func TestSummaryIsRequestedAgainWithALargerBudgetWhenTheModelReturnsNoText(t *testing.T) {
	server, budgets := emptyThenTextServer(t, 1)
	e := retryEngine(server)
	got, err := e.generateSummary(t.Context(), []types.Message{types.UserMessage("m1", "hello")},
		types.ModelIdentifier{Provider: types.APIProviderOpenAI, Model: "gpt-4o"}, "s", "t")
	if err != nil || !strings.HasPrefix(got, "1. goal") {
		t.Fatalf("generateSummary = %q, %v; want the summary from the second attempt", got, err)
	}
	if len(*budgets) != 2 || (*budgets)[1] <= (*budgets)[0] {
		t.Errorf("budgets = %v, want a second, larger attempt", *budgets)
	}
}

func TestSummaryGivesUpAfterItsAttemptsAndSaysWhy(t *testing.T) {
	server, budgets := emptyThenTextServer(t, 99)
	e := retryEngine(server)
	_, err := e.generateSummary(t.Context(), []types.Message{types.UserMessage("m1", "hello")},
		types.ModelIdentifier{Provider: types.APIProviderOpenAI, Model: "gpt-4o"}, "s", "t")
	if err == nil || !strings.Contains(err.Error(), "no summary generated") {
		t.Fatalf("err = %v, want the no-summary error", err)
	}
	if len(*budgets) != summaryAttempts {
		t.Errorf("requests = %d, want %d attempts", len(*budgets), summaryAttempts)
	}
}
