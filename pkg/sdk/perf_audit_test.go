// Measurement harness for the performance audit (docs/audits/performance-2026-10.md). Nothing here runs by default: set AUDIT=1.
//
//	AUDIT=1 go test ./pkg/sdk -run TestAudit -v          what NewClient and CreateSession cost
//	AUDIT=1 go test ./pkg/sdk -run TestAuditBaselineRequest -v   what the request of a one word turn carries, tool by tool
package sdk

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime/pprof"
	"sort"
	"testing"
	"time"

	"github.com/KPO-Tech/seshat-runtime/internal/providers"
)

// auditConfig builds a client configuration the way the desktop backend does.
func auditConfig(t *testing.T, kind SandboxKind) *ClientConfig {
	t.Helper()
	return &ClientConfig{
		Model:                  DefaultClientConfig().Model,
		PermissionMode:         PermissionModeNever,
		AutoCompact:            true,
		PersistSessions:        true,
		EnableMonitoring:       true,
		SessionSQLitePath:      filepath.Join(t.TempDir(), "sessions.db"),
		SandboxKind:            kind,
		MainAgentTools:         EnterpriseAgentTools(),
		MainAgentDeferredTools: EnterpriseAgentDeferredTools(),
		EnableToolSearch:       true,
	}
}

func TestAuditNewClient(t *testing.T) {
	if os.Getenv("AUDIT") == "" {
		t.Skip("AUDIT is not set")
	}
	for _, kind := range []SandboxKind{SandboxKindLocal, SandboxKindDocker} {
		for i := 0; i < 3; i++ {
			start := time.Now()
			c, err := NewClient(auditConfig(t, kind))
			if err != nil {
				t.Fatal(err)
			}
			built := time.Since(start)
			sessStart := time.Now()
			s, err := c.CreateSession(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			_ = s
			t.Logf("sandbox=%v run %d: NewClient %s, CreateSession %s", kind, i, built.Round(time.Millisecond), time.Since(sessStart).Round(time.Millisecond))
			_ = c.Close()
		}
	}
}

func TestAuditNewClientProfile(t *testing.T) {
	if os.Getenv("AUDIT") == "" {
		t.Skip("AUDIT is not set")
	}
	f, err := os.Create(filepath.Join(os.TempDir(), "newclient.cpu.pprof"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := pprof.StartCPUProfile(f); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		c, err := NewClient(auditConfig(t, SandboxKindDocker))
		if err != nil {
			t.Fatal(err)
		}
		_ = c.Close()
	}
	pprof.StopCPUProfile()
	t.Logf("profile written to %s", f.Name())
}

// TestAuditBaselineRequest sends one short message through the engine to a local stub of an OpenAI compatible endpoint and reports
// what the request carried: the system prompt, and the tool definitions one by one. This is what every turn pays before the model
// reads a word of the person's message.
func TestAuditBaselineRequest(t *testing.T) {
	if os.Getenv("AUDIT") == "" {
		t.Skip("AUDIT is not set")
	}
	var captured []byte
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if len(body) > len(captured) {
			captured = body
		}
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		if stream, _ := req["stream"].(bool); stream {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":null}]}\n\n")
			fmt.Fprint(w, "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer stub.Close()

	cfg := auditConfig(t, SandboxKindLocal)
	cfg.Model = ModelIdentifier{Provider: "openai", Model: "gpt-4o-mini"}
	cfg.APIKey = "k"
	cfg.ProviderConfig = &providers.Config{Provider: "openai", BaseURL: stub.URL + "/v1", APIKey: "k"}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	s, err := c.CreateSession(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SubmitMessage(t.Context(), "ok"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if captured == nil {
		t.Fatal("the stub saw no request")
	}
	var req struct {
		Messages []map[string]any `json:"messages"`
		Tools    []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(captured, &req); err != nil {
		t.Fatal(err)
	}
	t.Logf("request: %d bytes in total (about %d tokens), %d messages, %d tools", len(captured), len(captured)/4, len(req.Messages), len(req.Tools))
	for i, m := range req.Messages {
		raw, _ := json.Marshal(m)
		t.Logf("  message %d (%v): %d bytes", i, m["role"], len(raw))
	}
	type sized struct {
		name  string
		bytes int
	}
	var tools []sized
	total := 0
	for _, tool := range req.Tools {
		raw, _ := json.Marshal(tool)
		name := ""
		if fn, ok := tool["function"].(map[string]any); ok {
			name, _ = fn["name"].(string)
		}
		tools = append(tools, sized{name, len(raw)})
		total += len(raw)
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].bytes > tools[j].bytes })
	t.Logf("tool definitions: %d bytes (about %d tokens)", total, total/4)
	for i, tool := range tools {
		if i >= 25 {
			break
		}
		t.Logf("  %-28s %6d bytes (~%d tokens)", tool.name, tool.bytes, tool.bytes/4)
	}
	_ = os.WriteFile(filepath.Join(os.TempDir(), "baseline-request.json"), captured, 0o644)
}
