package memory

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/KPO-Tech/seshat-runtime/internal/providers"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// formattedReply is a summary that follows the six numbered sections, as JSON text.
const formattedReply = `1. goal\n2. decisions\n3. done\n4. state\n5. facts\n6. preferences`

// scriptedServer answers the n-th request with replies[n] (the last one repeats). A reply starting with "ERR:" is an
// HTTP 400 carrying that message. It records the size of each request body and the number of messages it carried.
type scriptedServer struct {
	*httptest.Server
	mu       sync.Mutex
	sizes    []int
	messages []int
}

func newScriptedServer(t *testing.T, replies ...string) *scriptedServer {
	t.Helper()
	s := &scriptedServer{}
	calls := 0
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var raw strings.Builder
		var body struct {
			Messages []json.RawMessage `json:"messages"`
		}
		_ = json.NewDecoder(&teeReader{r: r, b: &raw}).Decode(&body)
		s.mu.Lock()
		n := min(calls, len(replies)-1)
		calls++
		s.sizes = append(s.sizes, raw.Len())
		s.messages = append(s.messages, len(body.Messages))
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if msg, ok := strings.CutPrefix(replies[n], "ERR:"); ok {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"` + msg + `"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"r","choices":[{"message":{"role":"assistant","content":"` + replies[n] + `"},"finish_reason":"stop"}]}`))
	}))
	t.Cleanup(s.Close)
	return s
}

type teeReader struct {
	r *http.Request
	b *strings.Builder
}

func (t *teeReader) Read(p []byte) (int, error) {
	n, err := t.r.Body.Read(p)
	t.b.Write(p[:n])
	return n, err
}

func (s *scriptedServer) engine() *Engine {
	client := providers.NewClientWithConfig("k", &providers.Config{Provider: types.APIProviderOpenAI, BaseURL: s.URL})
	client.SetHTTPClient(s.Client())
	return NewEngine(client, DefaultConfig())
}

func someMessages(n int) []types.Message {
	var out []types.Message
	for i := 0; i < n; i++ {
		role := types.RoleUser
		if i%2 == 1 {
			role = types.RoleAssistant
		}
		out = append(out, makeTextMsg(fmt.Sprintf("m%03d", i), role, fmt.Sprintf("message %03d %s", i, strings.Repeat("words ", 200))))
	}
	return out
}

var gpt = types.ModelIdentifier{Provider: types.APIProviderOpenAI, Model: "gpt-4o"}

func TestSummaryPromptLeavesOutTheOldestMessagesFirst(t *testing.T) {
	e := NewEngine(nil, DefaultConfig())
	msgs := someMessages(40)
	full, omitted := e.buildSummaryPromptWithin(msgs, 1<<30)
	if omitted != 0 || !strings.Contains(full, "message 000") {
		t.Fatal("a request within the budget carries everything")
	}
	budget := estimateTextTokens(full) / 2
	prompt, omitted := e.buildSummaryPromptWithin(msgs, budget)
	if omitted == 0 || omitted >= len(msgs) {
		t.Fatalf("omitted = %d, want some but not all", omitted)
	}
	note := fmt.Sprintf("[%d older messages are left out: the conversation is too long to be summarized at once.]\n", omitted)
	if estimateTextTokens(prompt) > budget+estimateTextTokens(note) {
		t.Errorf("the prompt is %d tokens, over its budget of %d", estimateTextTokens(prompt), budget)
	}
	if strings.Contains(prompt, "message 000") || !strings.Contains(prompt, "message 039") {
		t.Error("the oldest messages go first and the newest is kept")
	}
	if !strings.Contains(prompt, note) {
		t.Error("the prompt says how many messages were left out")
	}
	// However small the budget, the newest message stays.
	tiny, omitted := e.buildSummaryPromptWithin(msgs, 1)
	if omitted != len(msgs)-1 || !strings.Contains(tiny, "message 039") {
		t.Errorf("with no room the newest message is still kept, omitted %d", omitted)
	}
}

func TestSummaryPromptBudgetFollowsTheModelsWindow(t *testing.T) {
	small := summaryPromptBudget(types.ModelIdentifier{Provider: types.APIProviderZAi, Model: "glm-4.5"}) // 128k
	large := summaryPromptBudget(types.ModelIdentifier{Provider: types.APIProviderZAi, Model: "glm-5.3-flash"})
	if small >= 128_000 || small < 100_000 {
		t.Errorf("a 128k window leaves %d tokens for the conversation, want it under the window by the output reserve", small)
	}
	if large <= small {
		t.Errorf("a larger window must allow a larger request: %d vs %d", large, small)
	}
}

func TestARequestRejectedAsTooLongIsShrunkAndSentAgain(t *testing.T) {
	server := newScriptedServer(t, "ERR:This model has a maximum context length of 128000 tokens", "ERR:prompt is too long", formattedReply)
	e := server.engine()
	got, err := e.generateSummary(t.Context(), someMessages(60), gpt, "s", "t")
	if err != nil || !strings.HasPrefix(got, "1. goal") {
		t.Fatalf("generateSummary = %q, %v; want the summary after two rejections", got, err)
	}
	if len(server.sizes) != 3 {
		t.Fatalf("requests = %d, want 3 (two rejected, one accepted)", len(server.sizes))
	}
	if !(server.sizes[1] < server.sizes[0] && server.sizes[2] < server.sizes[1]) {
		t.Errorf("each attempt must be smaller than the last, sizes %v", server.sizes)
	}
}

func TestShrinkingGivesUpAfterItsSteps(t *testing.T) {
	server := newScriptedServer(t, "ERR:maximum context length exceeded")
	_, err := server.engine().generateSummary(t.Context(), someMessages(60), gpt, "s", "t")
	if err == nil {
		t.Fatal("a request that is always rejected must end in an error")
	}
	if len(server.sizes) != len(summaryShrinkSteps)+1 {
		t.Errorf("requests = %d, want the first plus %d shrunk ones", len(server.sizes), len(summaryShrinkSteps))
	}
}

func TestAnErrorThatIsNotAnOverflowIsNotAnsweredByShrinking(t *testing.T) {
	server := newScriptedServer(t, "ERR:invalid api key")
	if _, err := server.engine().generateSummary(t.Context(), someMessages(4), gpt, "s", "t"); err == nil {
		t.Fatal("expected the error")
	}
	// The client may retry a transient failure itself, but shrinking must not be what it does.
	for i := 1; i < len(server.sizes); i++ {
		if server.sizes[i] < server.sizes[0] {
			t.Errorf("request %d is smaller than the first, sizes %v: an unrelated error shrank the request", i, server.sizes)
		}
	}
}

func TestAReplyOutsideTheFormatIsSentBackOnceWithAReminder(t *testing.T) {
	server := newScriptedServer(t, "just some free text about the work", formattedReply)
	got, err := server.engine().generateSummary(t.Context(), someMessages(4), gpt, "s", "t")
	if err != nil || !strings.HasPrefix(got, "1. goal") {
		t.Fatalf("generateSummary = %q, %v; want the reformatted reply", got, err)
	}
	if len(server.messages) != 2 || server.messages[0] != 1 || server.messages[1] != 3 {
		t.Errorf("messages per request = %v, want the request, then request + draft + reminder", server.messages)
	}
}

func TestAReplyThatStaysOutsideTheFormatIsStillUsed(t *testing.T) {
	server := newScriptedServer(t, "free text one", "free text two")
	got, err := server.engine().generateSummary(t.Context(), someMessages(4), gpt, "s", "t")
	if err != nil || got != "free text two" {
		t.Fatalf("generateSummary = %q, %v; a summary a little off beats a compaction that fails", got, err)
	}
	if len(server.sizes) != 2 {
		t.Errorf("requests = %d, want exactly one reminder", len(server.sizes))
	}
}

func TestAnEmptyReplyToTheReminderKeepsTheFirstDraft(t *testing.T) {
	server := newScriptedServer(t, "first draft without sections", "")
	got, err := server.engine().generateSummary(t.Context(), someMessages(4), gpt, "s", "t")
	if err != nil || got != "first draft without sections" {
		t.Fatalf("generateSummary = %q, %v; want the first draft", got, err)
	}
}

func TestContextOverflowIsRecognisedFromProviderWording(t *testing.T) {
	for _, msg := range []string{
		"This model has a maximum context length of 128000 tokens",
		"Error: context_length_exceeded",
		"prompt is too long: 215000 tokens > 200000 maximum",
		"Request too large for gpt-4o",
		"input is too long for requested model",
		"Your input exceeds the model limit of the context window",
	} {
		if !isContextOverflow(errors.New(msg)) {
			t.Errorf("%q must be recognised as an overflow", msg)
		}
	}
	for _, msg := range []string{"invalid api key", "rate limit exceeded", "connection reset by peer", ""} {
		if isContextOverflow(errors.New(msg)) {
			t.Errorf("%q is not an overflow", msg)
		}
	}
	if isContextOverflow(nil) {
		t.Error("nil is not an overflow")
	}
}

func TestSummaryFormatDetection(t *testing.T) {
	good := []string{
		"1. a\n2. b\n3. c\n4. d\n5. e\n6. f",
		"## 1. Goal\nx\n## 2. Decisions\ny\n## 3. Done\n## 4. State\n",
		"**1. Goal**\n**2. Decisions**\n**3. Done**\n**4. State**",
		"1) a\n2) b\n3) c\n4) d",
	}
	for _, s := range good {
		if !followsSummaryFormat(s) {
			t.Errorf("%q follows the format", s)
		}
	}
	for _, s := range []string{"", "plain prose with 1. one mention only", "1. a\n2. b\n3. c", "no numbers here at all"} {
		if followsSummaryFormat(s) {
			t.Errorf("%q does not follow the format", s)
		}
	}
}
