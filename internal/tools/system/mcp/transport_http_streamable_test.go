package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A server that behaves like huggingface.co/mcp: it refuses a client that does not accept both forms with a 406, gives a
// session at initialization and wants it back afterwards. It answers tools/list as an event stream, after a notification.
func strictStreamableServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var notified atomic.Int32
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		accept := r.Header.Get("Accept")
		if !strings.Contains(accept, "application/json") || !strings.Contains(accept, "text/event-stream") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotAcceptable)
			fmt.Fprint(w, `{"jsonrpc":"2.0","error":{"code":-32000,"message":"Not Acceptable: Client must accept both application/json and text/event-stream"},"id":null}`)
			return
		}
		var req struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("bad request: %v", err)
		}
		if req.Method == "initialize" {
			w.Header().Set("Mcp-Session-Id", "session-1")
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":"2025-03-26","capabilities":{"tools":{}},"serverInfo":{"name":"strict","version":"1"}}}`, req.ID)
			return
		}
		if r.Header.Get("Mcp-Session-Id") != "session-1" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, "missing session")
			return
		}
		if req.Method == "notifications/initialized" {
			notified.Add(1)
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		fmt.Fprint(w, ": a comment\n\n")
		fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{}}\n\n")
		fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":%d,\"method\":\"sampling/createMessage\",\"params\":{}}\n\n", req.ID+1000)
		fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":%d,\n", req.ID)
		fmt.Fprint(w, "data: \"result\":{\"tools\":[{\"name\":\"hub_search\",\"inputSchema\":{\"type\":\"object\"}}]}}\n\n")
	})), &notified
}

func TestHTTPTransportTalksToAStrictStreamableServer(t *testing.T) {
	server, notified := strictStreamableServer(t)
	defer server.Close()

	client, err := NewClient(ServerConfig{Name: "strict", URL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := client.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := client.Initialize(ctx); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	defer client.Close()
	tools, err := client.ListTools(ctx)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "hub_search" {
		t.Fatalf("tools %+v", tools)
	}
	if notified.Load() != 1 {
		t.Fatalf("the notification should carry the session, reached the server %d times", notified.Load())
	}
}

func TestHTTPTransportSaysWhatTheServerRefused(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"jsonrpc":"2.0","error":{"code":-32001,"message":"Invalid token"},"id":null}`)
	}))
	defer server.Close()
	transport, err := NewHTTPTransport(HTTPTransportConfig{URL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if err := transport.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, err = transport.Send(context.Background(), &JSONRPCRequest{JSONRPC: "2.0", ID: 1, Method: "ping"})
	if err == nil || !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "Invalid token") {
		t.Fatalf("error %v", err)
	}
}

func TestReadEventStreamResponseEndsWithAnError(t *testing.T) {
	_, err := readEventStreamResponse(strings.NewReader("data: {\"jsonrpc\":\"2.0\",\"method\":\"ping\"}\n\n"), int64(1))
	if err == nil {
		t.Fatal("a stream with no response must say so")
	}
}

// TestHTTPTransportAgainstARealServer talks to the public Hugging Face server, when asked:
// SESHAT_MCP_LIVE=1 go test ./internal/tools/system/mcp -run AgainstARealServer -v
func TestHTTPTransportAgainstARealServer(t *testing.T) {
	if os.Getenv("SESHAT_MCP_LIVE") == "" {
		t.Skip("set SESHAT_MCP_LIVE=1 to reach huggingface.co")
	}
	client, err := NewClient(ServerConfig{Name: "huggingface", URL: "https://huggingface.co/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := client.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := client.Initialize(ctx); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	defer client.Close()
	tools, err := client.ListTools(ctx)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(tools) == 0 {
		t.Fatal("no tools")
	}
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	t.Logf("%d tools: %v", len(tools), names)
}
