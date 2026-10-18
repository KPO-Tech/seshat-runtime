package sdk

import (
	"encoding/json"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
	"slices"
	"testing"
)

func TestEnterpriseAgentListsDoNotOverlap(t *testing.T) {
	loaded := EnterpriseAgentTools()
	for _, name := range EnterpriseAgentDeferredTools() {
		if slices.Contains(loaded, name) {
			t.Errorf("%q is both loaded and deferred", name)
		}
	}
}

// toolDefinitionBytes is the size of the definitions of the named tools as a request carries them.
func toolDefinitionBytes(t *testing.T, c *Client, names []string) (total, found int) {
	t.Helper()
	for _, name := range names {
		registered, ok := c.registry.Get(name)
		if !ok {
			continue
		}
		raw, err := json.Marshal(registered.Definition())
		if err != nil {
			t.Fatal(err)
		}
		total += len(raw)
		found++
	}
	return total, found
}

// TestEnterpriseAgentProfileCutsTheToolDefinitions measures what the profile saves: the size of the tool definitions of
// every registered tool against those the profile loads. Bytes stand for tokens at about four to one. With the tools a default
// client registers it is about 45% less; the tools the profile defers add nothing until the agent searches for them.
func TestEnterpriseAgentProfileCutsTheToolDefinitions(t *testing.T) {
	client, err := NewClient(&ClientConfig{PersistSessions: false})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	all := client.registry.GetToolNames()
	allBytes, allCount := toolDefinitionBytes(t, client, all)
	loadedBytes, loadedCount := toolDefinitionBytes(t, client, EnterpriseAgentTools())
	t.Logf("all tools: %d definitions, %d bytes (about %d tokens); enterprise profile: %d definitions, %d bytes (about %d tokens)",
		allCount, allBytes, allBytes/4, loadedCount, loadedBytes, loadedBytes/4)

	// Measured 2026-10-18: 54.7% of the bytes remain (about 19.4k of 35.5k tokens). The heaviest loaded definitions are bash,
	// agent, grep, read_file and ask_user_question (4 to 6 KB each); trimming them is the next saving.
	if loadedBytes*100 > allBytes*60 {
		t.Fatalf("the profile loads %d of %d bytes of tool definitions, want at most 60%%", loadedBytes, allBytes)
	}
}

// TestEnterpriseAgentProfileNamesExist keeps the lists honest: a tool that was renamed or removed must leave them.
func TestEnterpriseAgentProfileNamesExist(t *testing.T) {
	client, err := NewClient(&ClientConfig{PersistSessions: false})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	// Tools the SDK registers only when something is configured, or after the engine exists.
	conditional := map[string]bool{"agent": true, "spawn_agent": true, "wait_agent": true, "close_agent": true, "resume_agent": true, "send_agent_message": true, "list_agents": true,
		// Registered when an MCP server is connected.
		"mcp_list_resources": true, "mcp_read_resource": true}
	for _, name := range append(EnterpriseAgentTools(), EnterpriseAgentDeferredTools()...) {
		if conditional[name] {
			continue
		}
		if _, ok := client.registry.Get(name); !ok {
			t.Errorf("%q is in the enterprise profile but is not a registered tool", name)
		}
	}
}

// TestEveryDeferredToolHasAWrittenShortDescription: the names of the deferred tools are listed in the system prompt with a
// line each, and a tool the agent has to find should not depend on the first sentence of a long description.
func TestEveryDeferredToolHasAWrittenShortDescription(t *testing.T) {
	for _, name := range EnterpriseAgentDeferredTools() {
		if !registry.HasBuiltinShortDescription(name) {
			t.Errorf("%q is deferred but has no line in the short description catalog", name)
		}
	}
}

// TestDeferredToolListStaysSmall measures what listing the deferred tools with a line each costs in the system prompt of every
// request: it must stay a small fraction of the roughly 16k tokens the profile saves.
func TestDeferredToolListStaysSmall(t *testing.T) {
	client, err := NewClient(&ClientConfig{PersistSessions: false})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	total, entries := 0, 0
	for _, name := range EnterpriseAgentDeferredTools() {
		line := name
		if registered, ok := client.registry.Get(name); ok {
			line = name + ": " + registry.ShortDescription(registered.Definition())
		}
		total += len(line) + 1
		entries++
	}
	t.Logf("%d deferred tools listed with a line each: %d bytes (about %d tokens); names alone were about 207", entries, total, total/4)
	if total > 6000 {
		t.Fatalf("the list takes %d bytes, want at most 6000 (about 1,500 tokens)", total)
	}
}
