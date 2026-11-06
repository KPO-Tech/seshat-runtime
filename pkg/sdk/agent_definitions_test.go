package sdk

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/KPO-Tech/seshat-runtime/pkg/agent"
)

func newAgentDefsClient(t *testing.T) *Client {
	t.Helper()
	client, err := NewClient(&ClientConfig{PersistSessions: false, WorkingDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

// typesOffered returns the type enum a registered tool shows the model.
func typesOffered(t *testing.T, client *Client, toolName string) []string {
	t.Helper()
	registered, ok := client.registry.Get(toolName)
	if !ok {
		t.Fatalf("%s is not registered", toolName)
	}
	raw, err := json.Marshal(registered.Definition().InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"type", "agent_type"} {
		if p, ok := schema.Properties[key]; ok {
			return p.Enum
		}
	}
	t.Fatalf("%s has no type property", toolName)
	return nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func TestSetAgentDefinitions_RegisterReplaceRemove(t *testing.T) {
	client := newAgentDefsClient(t)

	if err := client.SetAgentDefinitions([]*agent.AgentDefinition{
		{AgentType: "org-reviewer", WhenToUse: "reviews", DisallowedTools: []string{"bash"}},
		{AgentType: "org-writer", WhenToUse: "writes"},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	for _, toolName := range []string{"agent", "spawn_agent"} {
		offered := typesOffered(t, client, toolName)
		if !contains(offered, "org-reviewer") || !contains(offered, "org-writer") {
			t.Errorf("%s offers %v", toolName, offered)
		}
	}
	if got := client.AgentDefinitions(); len(got) != 2 || got[0].AgentType != "org-reviewer" {
		t.Errorf("AgentDefinitions() = %+v", got)
	}

	// Replace: the whole set.
	if err := client.SetAgentDefinitions([]*agent.AgentDefinition{{AgentType: "org-writer"}}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	offered := typesOffered(t, client, "agent")
	if contains(offered, "org-reviewer") || !contains(offered, "org-writer") {
		t.Errorf("after replace the agent tool offers %v", offered)
	}

	// Remove.
	if err := client.SetAgentDefinitions(nil); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if offered := typesOffered(t, client, "agent"); contains(offered, "org-writer") {
		t.Errorf("after removal the agent tool offers %v", offered)
	}
	if len(client.AgentDefinitions()) != 0 {
		t.Error("definitions remain after removal")
	}
}

func TestSetAgentDefinitions_ShadowingRefusedOthersKept(t *testing.T) {
	client := newAgentDefsClient(t)
	err := client.SetAgentDefinitions([]*agent.AgentDefinition{
		{AgentType: "explore", Tools: []string{"*"}},
		{AgentType: "Not Valid"},
		{AgentType: "fine"},
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errors.Is(err, agent.ErrShadowsExistingAgent) {
		t.Errorf("error does not report the shadowing: %v", err)
	}
	var herr *HostDefinitionError
	if !errors.As(err, &herr) {
		t.Errorf("error carries no *HostDefinitionError: %v", err)
	}
	got := client.AgentDefinitions()
	if len(got) != 1 || got[0].AgentType != "fine" {
		t.Errorf("registered = %+v, want only fine", got)
	}
	offered := typesOffered(t, client, "agent")
	count := 0
	for _, v := range offered {
		if v == "explore" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("explore listed %d times in %v", count, offered)
	}
}

func TestSetAgentDefinitions_NilClient(t *testing.T) {
	var client *Client
	if err := client.SetAgentDefinitions(nil); err == nil {
		t.Fatal("expected an error on a nil client")
	}
	if client.AgentDefinitions() != nil {
		t.Fatal("expected no definitions from a nil client")
	}
}

// Safe for concurrent use while the tools read the registry. Run with -race.
func TestSetAgentDefinitions_Concurrent(t *testing.T) {
	client := newAgentDefsClient(t)
	agentTool, _ := client.registry.Get("agent")
	spawnTool, _ := client.registry.Get("spawn_agent")

	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				defs := []*agent.AgentDefinition{{AgentType: "set-a"}}
				if (i+w)%2 == 0 {
					defs = []*agent.AgentDefinition{{AgentType: "set-b"}, {AgentType: "set-c"}}
				}
				if i%5 == 0 {
					defs = nil
				}
				_ = client.SetAgentDefinitions(defs)
			}
		}(w)
	}
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				_ = agentTool.Definition()
				_ = spawnTool.Definition()
				_ = client.AgentDefinitions()
			}
		}()
	}
	wg.Wait()
}
