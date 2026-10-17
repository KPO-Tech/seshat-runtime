package mcp

import (
	"fmt"
	"strings"
	"testing"
)

func capsTool(name string, descLen int) Tool {
	return Tool{Name: name, Description: strings.Repeat("d", descLen), InputSchema: map[string]any{"type": "object"}}
}

func wrapNames(t *testing.T, cfg ServerConfig, tools []Tool) []string {
	t.Helper()
	cfg.Name = "srv"
	wrapped, err := NewWrapper(nil, cfg, nil).WrapTools(tools)
	if err != nil {
		t.Fatalf("WrapTools: %v", err)
	}
	names := make([]string, len(wrapped))
	for i, w := range wrapped {
		n := w.Definition().Name
		names[i] = n[strings.LastIndex(n, "_")+1:]
	}
	return names
}

func TestWrapToolsLeavesOutATooLargeTool(t *testing.T) {
	got := wrapNames(t, ServerConfig{}, []Tool{capsTool("small", 100), capsTool("huge", 20_000), capsTool("other", 100)})
	if strings.Join(got, ",") != "small,other" {
		t.Errorf("wrapped = %v, want small and other", got)
	}
}

func TestWrapToolsStopsAtTheServerTotal(t *testing.T) {
	var tools []Tool
	for i := 0; i < 20; i++ {
		tools = append(tools, capsTool(fmt.Sprintf("t%02d", i), 5_000))
	}
	got := wrapNames(t, ServerConfig{}, tools)
	if len(got) == 0 || len(got) >= 20 {
		t.Fatalf("expected some but not all of 20 tools of 5,000 bytes, got %d", len(got))
	}
	if want := DefaultMaxServerSpecBytes / 5_000; len(got) > want {
		t.Errorf("wrapped %d tools, over the %d the total allows", len(got), want)
	}
	if got[0] != "t00" {
		t.Errorf("the first tools must be the ones kept, got %v", got[:1])
	}
}

func TestWrapToolsCapsCanBeConfiguredOrRemoved(t *testing.T) {
	tools := []Tool{capsTool("huge", 20_000)}
	if got := wrapNames(t, ServerConfig{MaxToolSpecBytes: -1, MaxServerSpecBytes: -1}, tools); len(got) != 1 {
		t.Errorf("a negative cap removes the limit, got %v", got)
	}
	if got := wrapNames(t, ServerConfig{MaxToolSpecBytes: 30_000}, tools); len(got) != 1 {
		t.Errorf("a raised cap lets the tool in, got %v", got)
	}
	if got := wrapNames(t, ServerConfig{MaxToolSpecBytes: 50}, []Tool{capsTool("a", 100)}); len(got) != 0 {
		t.Errorf("a lowered cap leaves the tool out, got %v", got)
	}
}
