package builtin

import (
	"encoding/json"
	"testing"
)

// registeredLater are names the SDK or the host register after the builtin registry is built, or only when the
// matching feature is configured (MCP servers).
var registeredLater = map[string]bool{
	"agent": true, "spawn_agent": true, "resume_agent": true,
	"mcp_list_resources": true, "mcp_read_resource": true,
}

func TestEnterpriseAgentListsAreRegisteredAndDisjoint(t *testing.T) {
	reg, err := NewBuiltinRegistryWithConfig(&Config{})
	if err != nil {
		t.Fatalf("build registry: %v", err)
	}
	loaded := map[string]bool{}
	for _, name := range EnterpriseAgentToolNames() {
		if loaded[name] {
			t.Errorf("%q is listed twice in the loaded list", name)
		}
		loaded[name] = true
		if !registeredBySDK[name] && !reg.HasTool(name) {
			t.Errorf("%q is in the loaded list but no tool has that name", name)
		}
	}
	for _, name := range EnterpriseAgentDeferredToolNames() {
		if loaded[name] {
			t.Errorf("%q is both loaded and deferred", name)
		}
		if !registeredLater[name] && !reg.HasTool(name) {
			t.Errorf("%q is in the deferred list but no tool has that name", name)
		}
	}
}

func TestEnterpriseAgentKeepsTheTerminalCoreAndAddsOnlyWhatWasDecided(t *testing.T) {
	enterprise := map[string]bool{}
	for _, name := range EnterpriseAgentToolNames() {
		enterprise[name] = true
	}
	for _, name := range GeneralAgentToolNames() {
		if !enterprise[name] {
			t.Errorf("the common core tool %q is missing", name)
		}
	}
	for _, name := range []string{"enter_worktree", "exit_worktree", "browser_open", "browser_click", "browser_screenshot"} {
		if !enterprise[name] {
			t.Errorf("%q must be loaded", name)
		}
	}
	// Decided: these are found on demand, not loaded, and the writers of Word, Excel and PDF files are gone.
	for _, name := range []string{"browser_wait", "git_commit", "schedule_job", "convert_document", "calculator"} {
		if enterprise[name] {
			t.Errorf("%q must be deferred, not loaded", name)
		}
	}
	if n := len(EnterpriseAgentToolNames()); n > 45 {
		t.Errorf("the enterprise agent loads %d tools; keep the core small", n)
	}
}

func TestEnterpriseAgentLoadedToolsStayUnderTheTokenBudget(t *testing.T) {
	reg, err := NewBuiltinRegistryWithConfig(&Config{})
	if err != nil {
		t.Fatalf("build registry: %v", err)
	}
	chars := 0
	for _, name := range EnterpriseAgentToolNames() {
		if registeredBySDK[name] {
			continue
		}
		tool, ok := reg.Resolve(name)
		if !ok {
			continue
		}
		d := tool.Definition()
		raw, _ := json.Marshal(map[string]any{"name": d.Name, "description": d.Description, "input_schema": d.InputSchema})
		chars += len(raw)
	}
	// About 4 characters a token. The classification measured 16,300 tokens for this list; the budget leaves room for
	// growth but not for a drift back toward the 36,000 tokens of the whole registry.
	if tokens := chars / 4; tokens > 20_000 {
		t.Errorf("the loaded tools cost about %d tokens; the budget is 20,000", tokens)
	}
}
