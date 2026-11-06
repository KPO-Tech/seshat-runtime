package providers

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

const thinkingLockedAnswer = `{"error":{"code":"1210","message":"This model always engages in thinking and cannot be disabled; please use low, high, or max"}}`

const thinkingLockedStream = "data: {\"id\":\"1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"<block>no</block>\"},\"finish_reason\":null}]}\n\n" +
	"data: {\"id\":\"1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2}}\n\n" +
	"data: [DONE]\n\n"

// A server that behaves like z.ai for glm-5.3: it refuses the switch that turns reasoning off, and answers a request without it.
func thinkingLockedServer(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(data))
		mu.Unlock()
		if strings.Contains(string(data), `"thinking"`) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, thinkingLockedAnswer)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, thinkingLockedStream)
	}))
	t.Cleanup(server.Close)
	return server, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), bodies...)
	}
}

func classifierRequest(model string) types.APIRequest {
	return types.APIRequest{
		Model:           types.ModelIdentifier{Provider: types.APIProviderZAi, Model: model},
		Messages:        []types.Message{types.UserMessage("m1", "classify")},
		MaxTokens:       2112,
		DisableThinking: true,
	}
}

func TestAModelThatAlwaysReasonsIsAskedAgainWithoutTheSwitch(t *testing.T) {
	const model = "glm-always-thinks-test"
	t.Cleanup(func() { thinkingLocked.Delete(thinkingLockKey(classifierRequest(model).Model)) })
	server, bodies := thinkingLockedServer(t)
	client := NewClientWithConfig("k", &Config{Provider: types.APIProviderZAi, BaseURL: server.URL})

	resp, err := client.CreateMessage(context.Background(), classifierRequest(model))
	if err != nil {
		t.Fatalf("the refusal of the switch must not reach the caller: %v", err)
	}
	var text string
	for _, block := range resp.Content {
		if tc, ok := block.(types.TextContent); ok {
			text += tc.Text
		}
	}
	if !strings.Contains(text, "<block>no</block>") {
		t.Fatalf("the answer of the second request is expected, got %q", text)
	}
	sent := bodies()
	if len(sent) != 2 || !strings.Contains(sent[0], `"thinking":{"type":"disabled"}`) || strings.Contains(sent[1], "thinking") {
		t.Fatalf("first request with the switch, second without, got %d: %v", len(sent), sent)
	}

	// Learned: the next request does not send the switch at all.
	if _, err := client.CreateMessage(context.Background(), classifierRequest(model)); err != nil {
		t.Fatal(err)
	}
	if sent = bodies(); len(sent) != 3 || strings.Contains(sent[2], "thinking") {
		t.Fatalf("a model known to always reason must not be sent the switch again, got %d: %v", len(sent), sent)
	}
}

func TestAnotherRefusalIsNotMistakenForThatOne(t *testing.T) {
	if cannotDisableThinking(nil) || cannotDisableThinking(io.ErrUnexpectedEOF) {
		t.Fatal("only the refusal of the switch is recognised")
	}
	if !cannotDisableThinking(types.NewError(types.ErrCodeAPIInvalid, thinkingLockedAnswer)) {
		t.Fatal("the refusal of z.ai must be recognised")
	}
}
