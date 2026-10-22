package sdk

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/KPO-Tech/seshat-runtime/internal/providers"
)

// Two tool calls in one streamed answer, each with its own input, must each get their own result under their own id. A real
// conversation showed two glob calls on different folders both answered with the files of the second folder, the second call's id on
// both results. Two reads of different files stand for them here: they need nothing installed.
func TestParallelToolCallsKeepTheirInputsAndResults(t *testing.T) {
	dir := t.TempDir()
	fileA, fileB := filepath.Join(dir, "a.txt"), filepath.Join(dir, "b.txt")
	if err := os.WriteFile(fileA, []byte("content of the first file ALPHA"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fileB, []byte("content of the second file BRAVO"), 0o644); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var secondRequest []byte
	calls := 0
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		if _, hasTools := req["tools"]; !hasTools {
			// The title request.
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":"t","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"title"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
			return
		}
		mu.Lock()
		calls++
		n := calls
		if n == 2 {
			secondRequest = body
		}
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		chunk := func(delta string, finish string) {
			fmt.Fprintf(w, "data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":%s,\"finish_reason\":%s}]}\n\n", delta, finish)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
		if n == 1 {
			argA, _ := json.Marshal(map[string]string{"file_path": fileA})
			argB, _ := json.Marshal(map[string]string{"file_path": fileB})
			quote := func(b []byte) string { q, _ := json.Marshal(string(b)); return string(q) }
			// Each call opens with its id and name and gets its arguments in two pieces, the way providers stream them.
			half := func(b []byte) (string, string) { m := len(b) / 2; return quote(b[:m]), quote(b[m:]) }
			a1, a2 := half(argA)
			b1, b2 := half(argB)
			chunk(`{"role":"assistant","tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"read_file","arguments":`+a1+`}}]}`, "null")
			chunk(`{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"read_file","arguments":`+b1+`}}]}`, "null")
			chunk(`{"tool_calls":[{"index":0,"function":{"arguments":`+a2+`}}]}`, "null")
			chunk(`{"tool_calls":[{"index":1,"function":{"arguments":`+b2+`}}]}`, "null")
			chunk(`{}`, `"tool_calls"`)
		} else {
			chunk(`{"role":"assistant","content":"done"}`, "null")
			chunk(`{}`, `"stop"`)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer stub.Close()

	cfg := auditConfig(t, SandboxKindLocal)
	cfg.Model = ModelIdentifier{Provider: "openai", Model: "gpt-4o-mini"}
	cfg.APIKey = "k"
	cfg.ProviderConfig = &providers.Config{Provider: "openai", BaseURL: stub.URL + "/v1", APIKey: "k"}
	cfg.PermissionMode = PermissionModeBypass
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	s, err := c.CreateSession(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SubmitMessage(t.Context(), "read the two files"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if secondRequest == nil {
		t.Fatal("the model was never sent the tool results")
	}

	var req struct {
		Messages []struct {
			Role       string `json:"role"`
			ToolCallID string `json:"tool_call_id"`
			Content    any    `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(secondRequest, &req); err != nil {
		t.Fatal(err)
	}
	t.Logf("conversation sent back:\n%s", dumpConversation(secondRequest))
	results := map[string]string{}
	for _, m := range req.Messages {
		if m.Role == "tool" {
			results[m.ToolCallID] = fmt.Sprint(m.Content)
		}
	}
	if len(results) != 2 {
		t.Fatalf("want one result for each of the two calls, got %v", results)
	}
	if got := results["call_a"]; !strings.Contains(got, "ALPHA") || strings.Contains(got, "BRAVO") {
		t.Errorf("call_a must return its own file (ALPHA), got %q", got)
	}
	if got := results["call_b"]; !strings.Contains(got, "BRAVO") || strings.Contains(got, "ALPHA") {
		t.Errorf("call_b must return its own file (BRAVO), got %q", got)
	}
}

// dumpConversation renders the messages of a request other than the system prompt, one per line.
func dumpConversation(raw []byte) string {
	var m struct {
		Messages []map[string]any `json:"messages"`
	}
	_ = json.Unmarshal(raw, &m)
	var lines []string
	for _, msg := range m.Messages {
		if msg["role"] == "system" {
			continue
		}
		b, _ := json.Marshal(msg)
		lines = append(lines, string(b))
	}
	return strings.Join(lines, "\n")
}
