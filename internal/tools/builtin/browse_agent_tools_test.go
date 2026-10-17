package builtin

import (
	"testing"

	"github.com/KPO-Tech/seshat-runtime/internal/agent"
)

// The browse agent's allowlist once named tools that no longer exist (wikipedia, scholarly_search, web_map, web_crawl,
// tree); a name that matches nothing hides that the agent cannot do what its list suggests.
func TestBrowseAgentToolsAreAllRegistered(t *testing.T) {
	reg, err := NewBuiltinRegistryWithConfig(&Config{})
	if err != nil {
		t.Fatalf("build registry: %v", err)
	}
	for _, name := range agent.BrowseAgentToolNames() {
		if !registeredBySDK[name] && !registeredLater[name] && !reg.HasTool(name) {
			t.Errorf("the browse agent lists %q but no tool has that name", name)
		}
	}
}
